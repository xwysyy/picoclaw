//go:build amd64 || arm64 || riscv64 || mips64 || ppc64

package feishu

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"unicode/utf8"

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
	"github.com/xwysyy/X-Claw/pkg/media"
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
	if !c.IsRunning() {
		return channels.ErrNotRunning
	}

	if msg.ChatID == "" {
		return fmt.Errorf("chat ID is empty: %w", channels.ErrSendFailed)
	}

	content := normalizeFeishuMarkdownLinks(msg.Content)

	// Build interactive card with markdown content
	cardContent, err := buildMarkdownCard(content)
	if err != nil {
		return fmt.Errorf("feishu send: card build failed: %w", err)
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
		delReq := larkim.NewDeleteMessageReactionReqBuilder().
			MessageId(messageID).
			ReactionId(reactionID).
			Build()
		_, _ = c.client.Im.V1.MessageReaction.Delete(context.Background(), delReq)
	}
	return undo, nil
}

// SendMedia implements channels.MediaSender.
// Uploads images/files via Feishu API then sends as messages.
func (c *FeishuChannel) SendMedia(ctx context.Context, msg bus.OutboundMediaMessage) error {
	if !c.IsRunning() {
		return channels.ErrNotRunning
	}

	if msg.ChatID == "" {
		return fmt.Errorf("chat ID is empty: %w", channels.ErrSendFailed)
	}

	store := c.GetMediaStore()
	if store == nil {
		return fmt.Errorf("no media store available: %w", channels.ErrSendFailed)
	}

	for _, part := range msg.Parts {
		if err := c.sendMediaPart(ctx, msg.ChatID, part, store); err != nil {
			return err
		}
		if strings.TrimSpace(part.Caption) != "" {
			if err := c.Send(ctx, bus.OutboundMessage{
				Channel: "feishu",
				ChatID:  msg.ChatID,
				Content: part.Caption,
			}); err != nil {
				return err
			}
		}
	}

	return nil
}

// sendMediaPart resolves and sends a single media part.
func (c *FeishuChannel) sendMediaPart(
	ctx context.Context,
	chatID string,
	part bus.MediaPart,
	store media.MediaStore,
) error {
	localPath, err := store.Resolve(part.Ref)
	if err != nil {
		logger.ErrorCF("feishu", "Failed to resolve media ref", map[string]any{
			"ref":   part.Ref,
			"error": err.Error(),
		})
		return nil // skip this part
	}

	file, err := os.Open(localPath)
	if err != nil {
		logger.ErrorCF("feishu", "Failed to open media file", map[string]any{
			"path":  localPath,
			"error": err.Error(),
		})
		return nil // skip this part
	}
	defer file.Close()

	switch part.Type {
	case "image":
		err = c.sendImage(ctx, chatID, file)
	default:
		filename := part.Filename
		if filename == "" {
			filename = "file"
		}
		err = c.sendFile(ctx, chatID, file, filename, part.Type)
	}

	if err != nil {
		logger.ErrorCF("feishu", "Failed to send media", map[string]any{
			"type":  part.Type,
			"error": err.Error(),
		})
		return fmt.Errorf("feishu send media: %w", channels.ErrTemporary)
	}
	return nil
}

// --- Inbound message handling ---

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

	metadata := map[string]string{}
	if messageID != "" {
		metadata["message_id"] = messageID
	}
	if messageType != "" {
		metadata["message_type"] = messageType
	}
	chatType := stringValue(message.ChatType)
	if chatType != "" {
		metadata["chat_type"] = chatType
	}
	if sender != nil && sender.TenantKey != nil {
		metadata["tenant_key"] = *sender.TenantKey
	}

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
func extractContent(messageType, rawContent string) string {
	if rawContent == "" {
		return ""
	}

	switch messageType {
	case larkim.MsgTypeText:
		var textPayload struct {
			Text string `json:"text"`
		}
		if err := json.Unmarshal([]byte(rawContent), &textPayload); err == nil {
			return textPayload.Text
		}
		return rawContent

	case larkim.MsgTypePost:
		// Pass raw JSON to LLM — structured rich text is more informative than flattened plain text
		return rawContent

	case larkim.MsgTypeImage:
		// Image messages don't have text content
		return ""

	case larkim.MsgTypeFile, larkim.MsgTypeAudio, larkim.MsgTypeMedia:
		// File/audio/video messages may have a filename
		name := extractFileName(rawContent)
		if name != "" {
			return name
		}
		return ""

	default:
		return rawContent
	}
}

// downloadInboundMedia downloads media from inbound messages and stores in MediaStore.
func (c *FeishuChannel) downloadInboundMedia(
	ctx context.Context,
	chatID, messageID, messageType, rawContent string,
	store media.MediaStore,
) []string {
	var refs []string
	scope := channels.BuildMediaScope("feishu", chatID, messageID)

	switch messageType {
	case larkim.MsgTypeImage:
		imageKey := extractImageKey(rawContent)
		if imageKey == "" {
			return nil
		}
		ref := c.downloadResource(ctx, messageID, imageKey, "image", ".jpg", store, scope)
		if ref != "" {
			refs = append(refs, ref)
		}

	case larkim.MsgTypeFile, larkim.MsgTypeAudio, larkim.MsgTypeMedia:
		fileKey := extractFileKey(rawContent)
		if fileKey == "" {
			return nil
		}
		// Derive a fallback extension from the message type.
		var ext string
		switch messageType {
		case larkim.MsgTypeAudio:
			ext = ".ogg"
		case larkim.MsgTypeMedia:
			ext = ".mp4"
		default:
			ext = "" // generic file — rely on resp.FileName
		}
		ref := c.downloadResource(ctx, messageID, fileKey, "file", ext, store, scope)
		if ref != "" {
			refs = append(refs, ref)
		}
	}

	return refs
}

// downloadResource downloads a message resource (image/file) from Feishu,
// writes it to the project media directory, and stores the reference in MediaStore.
// fallbackExt (e.g. ".jpg") is appended when the resolved filename has no extension.
func (c *FeishuChannel) downloadResource(
	ctx context.Context,
	messageID, fileKey, resourceType, fallbackExt string,
	store media.MediaStore,
	scope string,
) string {
	req := larkim.NewGetMessageResourceReqBuilder().
		MessageId(messageID).
		FileKey(fileKey).
		Type(resourceType).
		Build()

	resp, err := c.client.Im.V1.MessageResource.Get(ctx, req)
	if err != nil {
		logger.ErrorCF("feishu", "Failed to download resource", map[string]any{
			"message_id": messageID,
			"file_key":   fileKey,
			"error":      err.Error(),
		})
		return ""
	}
	if !resp.Success() {
		logger.ErrorCF("feishu", "Resource download api error", map[string]any{
			"code": resp.Code,
			"msg":  resp.Msg,
		})
		return ""
	}

	if resp.File == nil {
		return ""
	}
	// Safely close the underlying reader if it implements io.Closer (e.g. HTTP response body).
	if closer, ok := resp.File.(io.Closer); ok {
		defer closer.Close()
	}

	filename := resp.FileName
	if filename == "" {
		filename = fileKey
	}
	// If filename still has no extension, append the fallback (like Telegram's ext parameter).
	if filepath.Ext(filename) == "" && fallbackExt != "" {
		filename += fallbackExt
	}

	// Write to the shared media temp directory using a unique name to avoid collisions.
	mediaDir := utils.MediaTempDir()
	if mkdirErr := os.MkdirAll(mediaDir, 0o700); mkdirErr != nil {
		logger.ErrorCF("feishu", "Failed to create media directory", map[string]any{
			"error": mkdirErr.Error(),
		})
		return ""
	}
	ext := filepath.Ext(filename)
	localPath := filepath.Join(mediaDir, utils.SanitizeFilename(messageID+"-"+fileKey+ext))

	out, err := os.Create(localPath)
	if err != nil {
		logger.ErrorCF("feishu", "Failed to create local file for resource", map[string]any{
			"error": err.Error(),
		})
		return ""
	}

	if _, copyErr := io.Copy(out, resp.File); copyErr != nil {
		out.Close()
		os.Remove(localPath)
		logger.ErrorCF("feishu", "Failed to write resource to file", map[string]any{
			"error": copyErr.Error(),
		})
		return ""
	}
	out.Close()

	ref, err := store.Store(localPath, media.MediaMeta{
		Filename: filename,
		Source:   "feishu",
	}, scope)
	if err != nil {
		logger.ErrorCF("feishu", "Failed to store downloaded resource", map[string]any{
			"file_key": fileKey,
			"error":    err.Error(),
		})
		os.Remove(localPath)
		return ""
	}

	return ref
}

// appendMediaTags appends media type tags to content (like Telegram's "[image: photo]").
func appendMediaTags(content, messageType string, mediaRefs []string) string {
	if len(mediaRefs) == 0 {
		return content
	}

	var tag string
	switch messageType {
	case larkim.MsgTypeImage:
		tag = "[image: photo]"
	case larkim.MsgTypeAudio:
		tag = "[audio]"
	case larkim.MsgTypeMedia:
		tag = "[video]"
	case larkim.MsgTypeFile:
		tag = "[file]"
	default:
		tag = "[attachment]"
	}

	if content == "" {
		return tag
	}
	return content + " " + tag
}

// sendCard sends an interactive card message to a chat.
func (c *FeishuChannel) sendCard(ctx context.Context, chatID, cardContent string) error {
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
		return fmt.Errorf("feishu send card: %w", channels.ErrTemporary)
	}

	if !resp.Success() {
		return fmt.Errorf("feishu api error (code=%d msg=%s): %w", resp.Code, resp.Msg, channels.ErrTemporary)
	}

	logger.DebugCF("feishu", "Feishu card message sent", map[string]any{
		"chat_id": chatID,
	})

	return nil
}

// sendImage uploads an image and sends it as a message.
func (c *FeishuChannel) sendImage(ctx context.Context, chatID string, file *os.File) error {
	// Upload image to get image_key
	uploadReq := larkim.NewCreateImageReqBuilder().
		Body(larkim.NewCreateImageReqBodyBuilder().
			ImageType("message").
			Image(file).
			Build()).
		Build()

	uploadResp, err := c.client.Im.V1.Image.Create(ctx, uploadReq)
	if err != nil {
		return fmt.Errorf("feishu image upload: %w", err)
	}
	if !uploadResp.Success() {
		return fmt.Errorf("feishu image upload api error (code=%d msg=%s)", uploadResp.Code, uploadResp.Msg)
	}
	if uploadResp.Data == nil || uploadResp.Data.ImageKey == nil {
		return fmt.Errorf("feishu image upload: no image_key returned")
	}

	imageKey := *uploadResp.Data.ImageKey

	// Send image message
	content, _ := json.Marshal(map[string]string{"image_key": imageKey})
	req := larkim.NewCreateMessageReqBuilder().
		ReceiveIdType(larkim.ReceiveIdTypeChatId).
		Body(larkim.NewCreateMessageReqBodyBuilder().
			ReceiveId(chatID).
			MsgType(larkim.MsgTypeImage).
			Content(string(content)).
			Build()).
		Build()

	resp, err := c.client.Im.V1.Message.Create(ctx, req)
	if err != nil {
		return fmt.Errorf("feishu image send: %w", err)
	}
	if !resp.Success() {
		return fmt.Errorf("feishu image send api error (code=%d msg=%s)", resp.Code, resp.Msg)
	}
	return nil
}

// sendFile uploads a file and sends it as a message.
func (c *FeishuChannel) sendFile(ctx context.Context, chatID string, file *os.File, filename, fileType string) error {
	filename = sanitizeFeishuUploadFilename(filename)

	// Map part type to Feishu file type
	feishuFileType := "stream"
	switch fileType {
	case "audio":
		feishuFileType = "opus"
	case "video":
		feishuFileType = "mp4"
	}

	// Upload file to get file_key
	uploadReq := larkim.NewCreateFileReqBuilder().
		Body(larkim.NewCreateFileReqBodyBuilder().
			FileType(feishuFileType).
			FileName(filename).
			File(file).
			Build()).
		Build()

	uploadResp, err := c.client.Im.V1.File.Create(ctx, uploadReq)
	if err != nil {
		return fmt.Errorf("feishu file upload: %w", err)
	}
	if !uploadResp.Success() {
		return fmt.Errorf("feishu file upload api error (code=%d msg=%s)", uploadResp.Code, uploadResp.Msg)
	}
	if uploadResp.Data == nil || uploadResp.Data.FileKey == nil {
		return fmt.Errorf("feishu file upload: no file_key returned")
	}

	fileKey := *uploadResp.Data.FileKey

	// Send file message
	content, _ := json.Marshal(map[string]string{"file_key": fileKey})
	req := larkim.NewCreateMessageReqBuilder().
		ReceiveIdType(larkim.ReceiveIdTypeChatId).
		Body(larkim.NewCreateMessageReqBodyBuilder().
			ReceiveId(chatID).
			MsgType(larkim.MsgTypeFile).
			Content(string(content)).
			Build()).
		Build()

	resp, err := c.client.Im.V1.Message.Create(ctx, req)
	if err != nil {
		return fmt.Errorf("feishu file send: %w", err)
	}
	if !resp.Success() {
		return fmt.Errorf("feishu file send api error (code=%d msg=%s)", resp.Code, resp.Msg)
	}
	return nil
}

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

var mentionPlaceholderRegex = regexp.MustCompile(`@_user_\d+`)

// stringValue safely dereferences a *string pointer.
func stringValue(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}

// buildMarkdownCard builds a Feishu Interactive Card JSON 2.0 string with markdown content.
// JSON 2.0 cards support full CommonMark standard markdown syntax.
func buildMarkdownCard(content string) (string, error) {
	card := map[string]any{
		"schema": "2.0",
		"body": map[string]any{
			"elements": []map[string]any{
				{
					"tag":     "markdown",
					"content": content,
				},
			},
		},
	}
	data, err := json.Marshal(card)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// extractJSONStringField unmarshals content as JSON and returns the value of the given string field.
// Returns "" if the content is invalid JSON or the field is missing/empty.
func extractJSONStringField(content, field string) string {
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(content), &m); err != nil {
		return ""
	}
	raw, ok := m[field]
	if !ok {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return ""
	}
	return s
}

// extractImageKey extracts the image_key from a Feishu image message content JSON.
// Format: {"image_key": "img_xxx"}
func extractImageKey(content string) string { return extractJSONStringField(content, "image_key") }

// extractFileKey extracts the file_key from a Feishu file/audio message content JSON.
// Format: {"file_key": "file_xxx", "file_name": "...", ...}
func extractFileKey(content string) string { return extractJSONStringField(content, "file_key") }

// extractFileName extracts the file_name from a Feishu file message content JSON.
func extractFileName(content string) string { return extractJSONStringField(content, "file_name") }

// stripMentionPlaceholders removes @_user_N placeholders from the text content.
// These are inserted by Feishu when users @mention someone in a message.
func stripMentionPlaceholders(content string, mentions []*larkim.MentionEvent) string {
	if len(mentions) == 0 {
		return content
	}
	for _, m := range mentions {
		if m.Key != nil && *m.Key != "" {
			content = strings.ReplaceAll(content, *m.Key, "")
		}
	}
	// Also clean up any remaining @_user_N patterns
	content = mentionPlaceholderRegex.ReplaceAllString(content, "")
	return strings.TrimSpace(content)
}

func sanitizeFeishuUploadFilename(filename string) string {
	filename = strings.TrimSpace(filename)
	if filename == "" {
		return "file"
	}

	// Prevent path traversal and oddities leaking into multipart headers.
	filename = strings.ReplaceAll(filename, "/", "_")
	filename = strings.ReplaceAll(filename, "\\", "_")
	filename = strings.TrimSpace(filename)
	if filename == "" {
		return "file"
	}

	// Remove invalid UTF-8 and control characters (Feishu/lark SDK may choke).
	filename = strings.Map(func(r rune) rune {
		// Drop ASCII control chars. Keep standard whitespace.
		if r < 0x20 && r != '\t' && r != '\n' && r != '\r' {
			return -1
		}
		if r == 0x7f {
			return -1
		}
		return r
	}, filename)
	filename = strings.TrimSpace(filename)
	if filename == "" {
		return "file"
	}
	if !utf8.ValidString(filename) {
		// Best-effort fallback: percent-encode raw bytes.
		return url.PathEscape(string([]byte(filename)))
	}

	// Keep filenames reasonably sized to avoid gigantic multipart headers.
	const maxRunes = 120
	r := []rune(filename)
	if len(r) > maxRunes {
		filename = string(r[:maxRunes])
	}

	// Multipart headers are historically ASCII-hostile; percent-encode when any
	// non-ASCII runes exist to keep uploads reliable in more environments.
	for _, ch := range filename {
		if ch > 0x7f {
			return url.PathEscape(filename)
		}
	}
	return filename
}

func feishuUserIDMatches(id *larkim.UserId, expected string) bool {
	expected = strings.TrimSpace(expected)
	if id == nil || expected == "" {
		return false
	}
	if id.UserId != nil && strings.TrimSpace(*id.UserId) == expected {
		return true
	}
	if id.OpenId != nil && strings.TrimSpace(*id.OpenId) == expected {
		return true
	}
	if id.UnionId != nil && strings.TrimSpace(*id.UnionId) == expected {
		return true
	}
	return false
}

// feishuCleanTextMentions:
// - removes bot mentions (when botID is configured)
// - strips leading mentions when botID is unknown (best-effort "mention to trigger")
// - replaces remaining mention placeholders with "@Name" for readability
//
// Returns (cleanedText, mentionedBotOrLeadingMention).
func feishuCleanTextMentions(content string, mentions []*larkim.MentionEvent, botID string) (string, bool) {
	if strings.TrimSpace(content) == "" || len(mentions) == 0 {
		return strings.TrimSpace(content), false
	}

	type mentionInfo struct {
		key      string
		display  string
		isBotHit bool
	}

	infos := make([]mentionInfo, 0, len(mentions))
	keys := make([]string, 0, len(mentions))
	for _, m := range mentions {
		if m == nil || m.Key == nil {
			continue
		}
		key := strings.TrimSpace(*m.Key)
		if key == "" {
			continue
		}
		name := ""
		if m.Name != nil {
			name = strings.TrimSpace(*m.Name)
		}
		display := "@"
		if name != "" {
			display += name
		}
		isBot := false
		if strings.TrimSpace(botID) != "" && m.Id != nil {
			isBot = feishuUserIDMatches(m.Id, botID)
		}

		infos = append(infos, mentionInfo{key: key, display: display, isBotHit: isBot})
		keys = append(keys, key)
	}

	mentioned := false
	out := content

	if strings.TrimSpace(botID) != "" {
		for _, info := range infos {
			if !info.isBotHit {
				continue
			}
			if strings.Contains(out, info.key) {
				mentioned = true
				out = strings.ReplaceAll(out, info.key, "")
			}
		}
	} else {
		// Bot ID unknown: treat leading mention placeholders as trigger and strip them.
		trimmed, removed := feishuStripLeadingMentions(out, keys)
		if removed > 0 {
			mentioned = true
			out = trimmed
		}
	}

	// Replace non-bot (or non-leading-stripped) placeholders with display names.
	for _, info := range infos {
		if strings.TrimSpace(botID) != "" && info.isBotHit {
			continue
		}
		if info.display == "@" { // unknown name: best-effort drop "@"
			continue
		}
		out = strings.ReplaceAll(out, info.key, info.display)
	}

	out = strings.TrimSpace(feishuSpacesRe.ReplaceAllString(out, " "))
	return out, mentioned
}

func feishuStripLeadingMentions(content string, keys []string) (string, int) {
	s := content
	removed := 0
	for {
		s = strings.TrimLeft(s, " \t\r\n")
		if s == "" {
			break
		}
		matched := false
		for _, key := range keys {
			if key == "" {
				continue
			}
			if strings.HasPrefix(s, key) {
				s = s[len(key):]
				removed++
				matched = true
				break
			}
		}
		if !matched {
			break
		}
	}
	return strings.TrimLeft(s, " \t\r\n"), removed
}

// feishuDetectAndStripBotMention is a lightweight mention detector for non-text
// message types (e.g. post). It trims content and returns whether the bot was
// mentioned.
//
// If botID is empty (bot open_id unknown), any mention is treated as a mention hit.
func feishuDetectAndStripBotMention(message *larkim.EventMessage, content string, botID string) (bool, string) {
	cleaned := strings.TrimSpace(content)
	if message == nil || len(message.Mentions) == 0 {
		return false, cleaned
	}
	if strings.TrimSpace(botID) == "" {
		return true, cleaned
	}
	for _, m := range message.Mentions {
		if m == nil || m.Id == nil {
			continue
		}
		if feishuUserIDMatches(m.Id, botID) {
			return true, cleaned
		}
	}
	return false, cleaned
}

var (
	feishuBrTagRe       = regexp.MustCompile(`(?i)<br\s*/?>`)
	feishuPStartTagRe   = regexp.MustCompile(`(?i)<p[^>]*>`)
	feishuPEndTagRe     = regexp.MustCompile(`(?i)</p>`)
	feishuAnyTagRe      = regexp.MustCompile(`(?s)<[^>]+>`)
	feishuListNewlineRe = regexp.MustCompile(`(?m)(^|\n)([-*]|\d+\.)\s*\n\s*`)
	feishuSpacesRe      = regexp.MustCompile(`[ \t]+`)
	feishuManyNewlineRe = regexp.MustCompile(`\n{3,}`)
)

// normalizeFeishuText converts Feishu's HTML-ish rich-text fragments into a
// predictable plain-text representation suitable for LLM consumption.
//
// It is intentionally conservative: it strips tags, unescapes HTML entities,
// normalizes whitespace/newlines, and fixes list marker quirks like "-\n1" → "- 1".
func normalizeFeishuText(in string) string {
	if strings.TrimSpace(in) == "" {
		return ""
	}

	s := in

	// Normalize line endings early.
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")

	// Convert common tags to newlines.
	s = feishuBrTagRe.ReplaceAllString(s, "\n")
	s = feishuPStartTagRe.ReplaceAllString(s, "")
	s = feishuPEndTagRe.ReplaceAllString(s, "\n")

	// Strip any remaining tags.
	s = feishuAnyTagRe.ReplaceAllString(s, "")

	// Decode entities (&nbsp; etc). html.UnescapeString turns &nbsp; into U+00A0.
	s = html.UnescapeString(s)
	s = strings.ReplaceAll(s, "\u00a0", " ")

	// Fix list/newline quirks: "-\n1" → "- 1", "2.\nsecond" → "2. second".
	s = feishuListNewlineRe.ReplaceAllString(s, "${1}${2} ")

	// Normalize spaces per-line for deterministic outputs.
	lines := strings.Split(s, "\n")
	for i := range lines {
		lines[i] = strings.TrimSpace(feishuSpacesRe.ReplaceAllString(lines[i], " "))
	}
	s = strings.Join(lines, "\n")

	// Collapse excessive blank lines (keep at most one empty line).
	s = feishuManyNewlineRe.ReplaceAllString(s, "\n\n")

	return strings.TrimSpace(s)
}

// feishuExtractJSONString extracts the first non-empty string value from raw JSON
// by trying keys in order. It returns "" on invalid JSON or if all values are empty.
func feishuExtractJSONString(raw string, keys ...string) string {
	if strings.TrimSpace(raw) == "" || len(keys) == 0 {
		return ""
	}

	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil || m == nil {
		return ""
	}

	for _, key := range keys {
		v, ok := m[key]
		if !ok {
			continue
		}
		s, ok := v.(string)
		if !ok {
			continue
		}
		s = strings.TrimSpace(s)
		if s != "" {
			return s
		}
	}
	return ""
}

// extractFeishuPostImageKeys finds all image keys inside a post payload JSON.
// It supports both snake_case ("image_key") and camelCase ("imageKey") and
// deduplicates + sorts the result.
//
// Returns nil on invalid JSON.
func extractFeishuPostImageKeys(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}

	var v any
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		return nil
	}

	set := make(map[string]struct{})
	var walk func(any)
	walk = func(node any) {
		switch n := node.(type) {
		case map[string]any:
			for k, vv := range n {
				lk := strings.ToLower(strings.TrimSpace(k))
				if lk == "image_key" || lk == "imagekey" {
					if s, ok := vv.(string); ok {
						s = strings.TrimSpace(s)
						if s != "" {
							set[s] = struct{}{}
						}
					}
				}
				walk(vv)
			}
		case []any:
			for _, vv := range n {
				walk(vv)
			}
		}
	}
	walk(v)

	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// extractFeishuPostContent flattens a Feishu "post" message payload into plain text.
//
// It returns "" on invalid JSON.
func extractFeishuPostContent(raw string) string {
	if strings.TrimSpace(raw) == "" {
		return ""
	}

	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil || m == nil {
		return ""
	}

	title, _ := m["title"].(string)
	title = normalizeFeishuText(title)

	// Prefer zh_cn.content when available; fall back to top-level content for older payloads.
	var content any
	if zh, ok := m["zh_cn"].(map[string]any); ok && zh != nil {
		if c, ok := zh["content"]; ok {
			content = c
		}
	}
	if content == nil {
		content = m["content"]
	}

	var paragraphs []string
	if rows, ok := content.([]any); ok {
		for _, row := range rows {
			switch r := row.(type) {
			case []any:
				p := buildFeishuPostParagraph(r)
				if p != "" {
					paragraphs = append(paragraphs, p)
				}
			case map[string]any:
				p := buildFeishuPostParagraph([]any{r})
				if p != "" {
					paragraphs = append(paragraphs, p)
				}
			}
		}
	}

	var out []string
	if strings.TrimSpace(title) != "" {
		out = append(out, strings.TrimSpace(title))
	}
	out = append(out, paragraphs...)
	return strings.TrimSpace(strings.Join(out, "\n"))
}

func buildFeishuPostParagraph(nodes []any) string {
	if len(nodes) == 0 {
		return ""
	}

	var b strings.Builder
	for _, n := range nodes {
		elem, ok := n.(map[string]any)
		if !ok || elem == nil {
			continue
		}

		tag, _ := elem["tag"].(string)
		tag = strings.ToLower(strings.TrimSpace(tag))

		switch tag {
		case "text", "md":
			text, _ := elem["text"].(string)
			b.WriteString(stripFeishuInlineHTML(text))
		case "a":
			text, _ := elem["text"].(string)
			text = stripFeishuInlineHTML(text)
			if strings.TrimSpace(text) != "" {
				b.WriteString(text)
				break
			}
			href, _ := elem["href"].(string)
			href = strings.TrimSpace(href)
			if href != "" {
				b.WriteString(href)
			}
		case "at":
			// Field names differ between payload variants ("name" vs "user_name").
			name, _ := elem["name"].(string)
			if strings.TrimSpace(name) == "" {
				name, _ = elem["user_name"].(string)
			}
			name = strings.TrimSpace(name)
			if name != "" {
				b.WriteString("@")
				b.WriteString(name)
			}
		case "img":
			// Keep a compact placeholder; the actual image is available via mediaRefs.
			b.WriteString("[image]")
		default:
			// Ignore unknown tags.
		}
	}

	out := b.String()
	out = strings.ReplaceAll(out, "\u00a0", " ")
	out = feishuSpacesRe.ReplaceAllString(out, " ")
	return strings.TrimSpace(out)
}

// stripFeishuInlineHTML strips tags and unescapes entities for inline fragments
// while preserving leading/trailing whitespace (important for post node joins).
func stripFeishuInlineHTML(in string) string {
	if in == "" {
		return ""
	}
	s := in
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	s = feishuBrTagRe.ReplaceAllString(s, "\n")
	s = feishuPStartTagRe.ReplaceAllString(s, "")
	s = feishuPEndTagRe.ReplaceAllString(s, "\n")
	s = feishuAnyTagRe.ReplaceAllString(s, "")
	s = html.UnescapeString(s)
	return strings.ReplaceAll(s, "\u00a0", " ")
}

// resolveFeishuFileUploadTypes determines (fileType, messageType) for Feishu uploads.
// It is a small helper intended for stable unit-tested behavior.
func resolveFeishuFileUploadTypes(mediaType, filename, contentType string) (fileType string, messageType string) {
	mediaType = strings.ToLower(strings.TrimSpace(mediaType))
	contentType = strings.ToLower(strings.TrimSpace(contentType))

	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(strings.TrimSpace(filename)), "."))
	if ext == "" {
		detectedType := channels.MediaTypeFromMIME(contentType)
		if detectedType == "video" || detectedType == "audio" {
			parts := strings.SplitN(contentType, "/", 2)
			if len(parts) == 2 {
				sub := strings.TrimSpace(parts[1])
				// Avoid overly generic values like "application/octet-stream".
				if sub != "" && sub != "octet-stream" {
					ext = sub
				}
			}
		}
	}
	if ext == "" {
		ext = "file"
	}

	switch mediaType {
	case "audio":
		// Feishu audio messages are effectively "opus" only; other audio types should fall back to file.
		if ext == "opus" {
			messageType = "audio"
		} else {
			messageType = "file"
		}
	case "video", "media":
		messageType = "media"
	default:
		messageType = "file"
	}

	return ext, messageType
}

// extractFeishuMessageContent converts an inbound Feishu message into plain text.
// It returns raw JSON payloads as-is when decoding fails (so we don't lose evidence).
func extractFeishuMessageContent(msg *larkim.EventMessage) string {
	if msg == nil || msg.Content == nil {
		return ""
	}
	raw := strings.TrimSpace(stringValue(msg.Content))
	if raw == "" {
		return ""
	}

	msgType := ""
	if msg.MessageType != nil {
		msgType = strings.ToLower(strings.TrimSpace(*msg.MessageType))
	}

	switch msgType {
	case "", larkim.MsgTypeText:
		var payload struct {
			Text string `json:"text"`
		}
		if err := json.Unmarshal([]byte(raw), &payload); err != nil {
			return raw
		}
		return normalizeFeishuText(payload.Text)

	case larkim.MsgTypePost:
		if out := extractFeishuPostContent(raw); strings.TrimSpace(out) != "" {
			return out
		}
		// Preserve raw payload for debugging / evidence.
		return raw

	case larkim.MsgTypeImage:
		return ""

	case larkim.MsgTypeFile:
		if name := extractFileName(raw); strings.TrimSpace(name) != "" {
			return strings.TrimSpace(name)
		}
		return ""

	case larkim.MsgTypeAudio:
		if name := extractFileName(raw); strings.TrimSpace(name) != "" {
			return strings.TrimSpace(name)
		}
		return ""

	case larkim.MsgTypeMedia, "video":
		if name := extractFileName(raw); strings.TrimSpace(name) != "" {
			return strings.TrimSpace(name)
		}
		return ""

	default:
		// Unknown types: keep the raw payload.
		return raw
	}
}
