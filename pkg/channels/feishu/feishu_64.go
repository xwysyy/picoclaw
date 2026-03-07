//go:build amd64 || arm64 || riscv64 || mips64 || ppc64

package feishu

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	larkdispatcher "github.com/larksuite/oapi-sdk-go/v3/event/dispatcher"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	larkws "github.com/larksuite/oapi-sdk-go/v3/ws"

	"github.com/xwysyy/X-Claw/pkg/bus"
	"github.com/xwysyy/X-Claw/pkg/channels"
	"github.com/xwysyy/X-Claw/pkg/config"
	"github.com/xwysyy/X-Claw/pkg/identity"
	"github.com/xwysyy/X-Claw/pkg/logger"
	"github.com/xwysyy/X-Claw/pkg/utils"
)

type FeishuChannel struct {
	*channels.BaseChannel
	config    config.FeishuConfig
	client    *lark.Client
	wsClient  *larkws.Client
	appSecret string

	botOpenID atomic.Value // stores string; populated lazily for @mention detection

	mu     sync.Mutex
	cancel context.CancelFunc
}

func NewFeishuChannel(cfg config.FeishuConfig, bus *bus.MessageBus) (*FeishuChannel, error) {
	base := channels.NewBaseChannel("feishu", cfg, bus, cfg.AllowFrom,
		channels.WithGroupTrigger(cfg.GroupTrigger),
		channels.WithPlaceholder(cfg.Placeholder),
		channels.WithReasoningChannelID(cfg.ReasoningChannelID),
		// Message splitting is handled by channels.Manager. Keep a conservative
		// cap for markdown payloads to avoid Feishu API rejections.
		channels.WithMaxMessageLength(feishuPostChunkLimit),
	)

	ch := &FeishuChannel{
		BaseChannel: base,
		config:      cfg,
		client:      nil,
	}
	ch.SetOwner(ch)
	return ch, nil
}

func (c *FeishuChannel) Start(ctx context.Context) error {
	if strings.TrimSpace(c.config.AppID) == "" || !c.config.AppSecret.Present() {
		return fmt.Errorf("feishu app_id or app_secret is empty")
	}

	appSecret, err := c.config.AppSecret.Resolve("")
	if err != nil {
		return fmt.Errorf("resolve feishu app_secret: %w", err)
	}
	appSecret = strings.TrimSpace(appSecret)
	if appSecret == "" {
		return fmt.Errorf("feishu app_secret is empty")
	}
	c.appSecret = appSecret
	c.client = lark.NewClient(c.config.AppID, appSecret)

	// Fetch bot open_id via API for reliable @mention detection.
	if err := c.fetchBotOpenID(ctx); err != nil {
		logger.ErrorCF("feishu", "Failed to fetch bot open_id, @mention detection may not work", map[string]any{
			"error": err.Error(),
		})
	}

	verificationToken := ""
	if c.config.VerificationToken.Present() {
		if v, err := c.config.VerificationToken.Resolve(""); err == nil {
			verificationToken = strings.TrimSpace(v)
		} else {
			return fmt.Errorf("resolve feishu verification_token: %w", err)
		}
	}
	encryptKey := ""
	if c.config.EncryptKey.Present() {
		if v, err := c.config.EncryptKey.Resolve(""); err == nil {
			encryptKey = strings.TrimSpace(v)
		} else {
			return fmt.Errorf("resolve feishu encrypt_key: %w", err)
		}
	}

	dispatcher := larkdispatcher.NewEventDispatcher(verificationToken, encryptKey).
		OnP2MessageReceiveV1(c.handleMessageReceive)

	runCtx, cancel := context.WithCancel(ctx)

	c.mu.Lock()
	c.cancel = cancel
	c.wsClient = larkws.NewClient(
		c.config.AppID,
		appSecret,
		larkws.WithEventHandler(dispatcher),
	)
	wsClient := c.wsClient
	c.mu.Unlock()

	c.SetRunning(true)
	logger.InfoC("feishu", "Feishu channel started (websocket mode)")

	go func() {
		if err := wsClient.Start(runCtx); err != nil {
			logger.ErrorCF("feishu", "Feishu websocket stopped with error", map[string]any{
				"error": err.Error(),
			})
		}
	}()

	return nil
}

func (c *FeishuChannel) Stop(ctx context.Context) error {
	c.mu.Lock()
	if c.cancel != nil {
		c.cancel()
		c.cancel = nil
	}
	c.wsClient = nil
	c.mu.Unlock()

	c.SetRunning(false)
	logger.InfoC("feishu", "Feishu channel stopped")
	return nil
}

// Send sends a message using Interactive Card format for markdown rendering.
func (c *FeishuChannel) Send(ctx context.Context, msg bus.OutboundMessage) error {
	_, err := c.SendWithMessageID(ctx, msg)
	return err
}

func (c *FeishuChannel) SendWithMessageID(ctx context.Context, msg bus.OutboundMessage) (string, error) {
	if !c.IsRunning() {
		return "", channels.ErrNotRunning
	}

	if msg.ChatID == "" {
		return "", fmt.Errorf("chat ID is empty: %w", channels.ErrSendFailed)
	}

	content := normalizeFeishuMarkdownLinks(msg.Content)

	// Build interactive card with markdown content
	cardContent, err := buildMarkdownCard(content)
	if err != nil {
		return "", fmt.Errorf("feishu send: card build failed: %w", err)
	}
	return c.sendCard(ctx, msg.ChatID, cardContent)
}

// EditMessage implements channels.MessageEditor.
// Uses Message.Patch to update an interactive card message.
func (c *FeishuChannel) EditMessage(ctx context.Context, chatID, messageID, content string) error {
	cardContent, err := buildMarkdownCard(normalizeFeishuMarkdownLinks(content))
	if err != nil {
		return fmt.Errorf("feishu edit: card build failed: %w", err)
	}

	req := larkim.NewPatchMessageReqBuilder().
		MessageId(messageID).
		Body(larkim.NewPatchMessageReqBodyBuilder().Content(cardContent).Build()).
		Build()

	resp, err := c.client.Im.V1.Message.Patch(ctx, req)
	if err != nil {
		return fmt.Errorf("feishu edit: %w", err)
	}
	if !resp.Success() {
		return fmt.Errorf("feishu edit api error (code=%d msg=%s)", resp.Code, resp.Msg)
	}
	return nil
}

// SendPlaceholder implements channels.PlaceholderCapable.
// Sends an interactive card with placeholder text and returns its message ID.
func (c *FeishuChannel) SendPlaceholder(ctx context.Context, chatID string) (string, error) {
	if !c.config.Placeholder.Enabled {
		logger.DebugCF("feishu", "Placeholder disabled, skipping", map[string]any{
			"chat_id": chatID,
		})
		return "", nil
	}

	text := c.config.Placeholder.Text
	if text == "" {
		text = "Thinking..."
	}

	cardContent, err := buildMarkdownCard(normalizeFeishuMarkdownLinks(text))
	if err != nil {
		return "", fmt.Errorf("feishu placeholder: card build failed: %w", err)
	}

	req := larkim.NewCreateMessageReqBuilder().
		ReceiveIdType(larkim.ReceiveIdTypeChatId).
		Body(larkim.NewCreateMessageReqBodyBuilder().
			ReceiveId(chatID).
			MsgType(larkim.MsgTypeInteractive).
			Content(cardContent).
			Build()).
		Build()

	resp, err := c.client.Im.V1.Message.Create(ctx, req)
	if err != nil {
		return "", fmt.Errorf("feishu placeholder send: %w", err)
	}
	if !resp.Success() {
		return "", fmt.Errorf("feishu placeholder api error (code=%d msg=%s)", resp.Code, resp.Msg)
	}

	if resp.Data != nil && resp.Data.MessageId != nil {
		return *resp.Data.MessageId, nil
	}
	return "", nil
}

// ReactToMessage implements channels.ReactionCapable.
// Adds an "Pin" reaction and returns an undo function to remove it.
func (c *FeishuChannel) ReactToMessage(ctx context.Context, chatID, messageID string) (func(), error) {
	req := larkim.NewCreateMessageReactionReqBuilder().
		MessageId(messageID).
		Body(larkim.NewCreateMessageReactionReqBodyBuilder().
			ReactionType(larkim.NewEmojiBuilder().EmojiType("Pin").Build()).
			Build()).
		Build()

	resp, err := c.client.Im.V1.MessageReaction.Create(ctx, req)
	if err != nil {
		logger.ErrorCF("feishu", "Failed to add reaction", map[string]any{
			"message_id": messageID,
			"error":      err.Error(),
		})
		return func() {}, fmt.Errorf("feishu react: %w", err)
	}
	if !resp.Success() {
		logger.ErrorCF("feishu", "Reaction API error", map[string]any{
			"message_id": messageID,
			"code":       resp.Code,
			"msg":        resp.Msg,
		})
		return func() {}, fmt.Errorf("feishu react api error (code=%d msg=%s)", resp.Code, resp.Msg)
	}

	var reactionID string
	if resp.Data != nil && resp.Data.ReactionId != nil {
		reactionID = *resp.Data.ReactionId
	}
	if reactionID == "" {
		return func() {}, nil
	}

	var undone atomic.Bool
	undo := func() {
		if !undone.CompareAndSwap(false, true) {
			return
		}
		delCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		delReq := larkim.NewDeleteMessageReactionReqBuilder().
			MessageId(messageID).
			ReactionId(reactionID).
			Build()
		if _, err := c.client.Im.V1.MessageReaction.Delete(delCtx, delReq); err != nil {
			logger.DebugCF("feishu", "Failed to undo reaction", map[string]any{
				"message_id":  messageID,
				"reaction_id": reactionID,
				"error":       err.Error(),
			})
		}
	}
	return undo, nil
}

// SendMedia implements channels.MediaSender.
// Uploads images/files via Feishu API then sends as messages.
func (c *FeishuChannel) handleMessageReceive(ctx context.Context, event *larkim.P2MessageReceiveV1) error {
	if event == nil || event.Event == nil || event.Event.Message == nil {
		return nil
	}

	message := event.Event.Message
	sender := event.Event.Sender

	chatID := stringValue(message.ChatId)
	if chatID == "" {
		return nil
	}

	senderID := extractFeishuSenderID(sender)
	if senderID == "" {
		senderID = "unknown"
	}

	messageType := stringValue(message.MessageType)
	messageID := stringValue(message.MessageId)
	rawContent := stringValue(message.Content)

	// Check allowlist early to avoid downloading media for rejected senders.
	// BaseChannel.HandleMessage will check again, but this avoids wasted network I/O.
	senderInfo := bus.SenderInfo{
		Platform:    "feishu",
		PlatformID:  senderID,
		CanonicalID: identity.BuildCanonicalID("feishu", senderID),
	}
	if !c.IsAllowedSender(senderInfo) {
		return nil
	}

	// Extract/normalize content based on message type
	content := extractFeishuMessageContent(message)

	// Handle media messages (download and store)
	var mediaRefs []string
	if store := c.GetMediaStore(); store != nil && messageID != "" {
		mediaRefs = c.downloadInboundMedia(ctx, chatID, messageID, messageType, rawContent, store)
	}

	// Append media tags to content (like Telegram does)
	content = appendMediaTags(content, messageType, mediaRefs)
	if c.GetMediaStore() != nil && messageID != "" && len(mediaRefs) == 0 {
		switch messageType {
		case larkim.MsgTypeImage, larkim.MsgTypeFile, larkim.MsgTypeAudio, larkim.MsgTypeMedia:
			// Feishu may reject media downloads when the bot lacks permissions or the
			// resource is not shared to the bot. Surface a lightweight hint so the agent
			// can guide the user instead of silently losing the attachment.
			hint := "[media: unavailable - 请确认图片/文件已共享给机器人且机器人具备下载权限]"
			if strings.TrimSpace(content) != "" {
				content = strings.TrimSpace(content) + "\n\n" + hint
			} else {
				content = hint
			}
		}
	}

	if content == "" {
		content = "[empty message]"
	}

	metadata := buildFeishuInboundMetadata(message, sender)
	chatType := metadata["chat_type"]

	var peer bus.Peer
	// Lark may report private chats as chat_type="private" instead of "p2p".
	// Treat any non-group chat as a direct message to avoid breaking DM UX.
	if chatType != "group" {
		peer = bus.Peer{Kind: "direct", ID: senderID}
	} else {
		peer = bus.Peer{Kind: "group", ID: chatID}

		knownID, _ := c.botOpenID.Load().(string)

		isMentioned := false
		switch messageType {
		case larkim.MsgTypeText:
			// Preserve mention semantics in content ("@Name") while stripping bot mentions
			// (or leading mentions when bot id is unknown) for trigger detection.
			var mentioned bool
			content, mentioned = feishuCleanTextMentions(content, message.Mentions, knownID)
			isMentioned = mentioned
		default:
			// For non-text messages (post/json etc), we cannot reliably rewrite mention
			// placeholders, but we can still detect mention hits.
			isMentioned, content = feishuDetectAndStripBotMention(message, content, knownID)
		}

		// In group chats, apply unified group trigger filtering
		respond, cleaned := c.ShouldRespondInGroup(isMentioned, content)
		if !respond {
			return nil
		}
		content = cleaned
	}

	logger.InfoCF("feishu", "Feishu message received", map[string]any{
		"sender_id":  senderID,
		"chat_id":    chatID,
		"message_id": messageID,
		"preview":    utils.Truncate(content, 80),
	})

	c.HandleMessage(ctx, peer, messageID, senderID, chatID, content, mediaRefs, metadata, senderInfo)
	return nil
}

// --- Internal helpers ---

func buildFeishuInboundMetadata(message *larkim.EventMessage, sender *larkim.EventSender) map[string]string {
	metadata := map[string]string{}
	if message == nil {
		return metadata
	}
	if messageID := stringValue(message.MessageId); messageID != "" {
		metadata["message_id"] = messageID
	}
	if messageType := stringValue(message.MessageType); messageType != "" {
		metadata["message_type"] = messageType
	}
	if chatType := stringValue(message.ChatType); chatType != "" {
		metadata["chat_type"] = chatType
	}
	if threadID := stringValue(message.ThreadId); threadID != "" {
		metadata["thread_id"] = threadID
	}
	if parentID := stringValue(message.ParentId); parentID != "" {
		metadata["parent_message_id"] = parentID
		metadata["reply_to_message_id"] = parentID
	}
	if rootID := stringValue(message.RootId); rootID != "" {
		metadata["root_message_id"] = rootID
	}
	if sender != nil && sender.TenantKey != nil {
		metadata["tenant_key"] = *sender.TenantKey
	}
	return metadata
}

// fetchBotOpenID calls the Feishu bot info API to retrieve and store the bot's open_id.
func (c *FeishuChannel) fetchBotOpenID(ctx context.Context) error {
	resp, err := c.client.Do(ctx, &larkcore.ApiReq{
		HttpMethod:                http.MethodGet,
		ApiPath:                   "/open-apis/bot/v3/info",
		SupportedAccessTokenTypes: []larkcore.AccessTokenType{larkcore.AccessTokenTypeTenant},
	})
	if err != nil {
		return fmt.Errorf("bot info request: %w", err)
	}

	var result struct {
		Code int `json:"code"`
		Bot  struct {
			OpenID string `json:"open_id"`
		} `json:"bot"`
	}
	if err := json.Unmarshal(resp.RawBody, &result); err != nil {
		return fmt.Errorf("bot info parse: %w", err)
	}
	if result.Code != 0 {
		return fmt.Errorf("bot info api error (code=%d)", result.Code)
	}
	if result.Bot.OpenID == "" {
		return fmt.Errorf("bot info: empty open_id")
	}

	c.botOpenID.Store(result.Bot.OpenID)
	logger.InfoCF("feishu", "Fetched bot open_id from API", map[string]any{
		"open_id": result.Bot.OpenID,
	})
	return nil
}

// isBotMentioned checks if the bot was @mentioned in the message.
func (c *FeishuChannel) isBotMentioned(message *larkim.EventMessage) bool {
	if message.Mentions == nil {
		return false
	}

	knownID, _ := c.botOpenID.Load().(string)
	if knownID == "" {
		logger.DebugCF("feishu", "Bot open_id unknown, cannot detect @mention", nil)
		return false
	}

	for _, m := range message.Mentions {
		if m.Id == nil {
			continue
		}
		if m.Id.OpenId != nil && *m.Id.OpenId == knownID {
			return true
		}
	}
	return false
}

// extractContent extracts text content from different message types.
func extractFeishuSenderID(sender *larkim.EventSender) string {
	if sender == nil || sender.SenderId == nil {
		return ""
	}

	if sender.SenderId.UserId != nil && *sender.SenderId.UserId != "" {
		return *sender.SenderId.UserId
	}
	if sender.SenderId.OpenId != nil && *sender.SenderId.OpenId != "" {
		return *sender.SenderId.OpenId
	}
	if sender.SenderId.UnionId != nil && *sender.SenderId.UnionId != "" {
		return *sender.SenderId.UnionId
	}

	return ""
}
