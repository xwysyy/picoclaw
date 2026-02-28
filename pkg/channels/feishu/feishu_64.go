//go:build amd64 || arm64 || riscv64 || mips64 || ppc64

package feishu

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkdispatcher "github.com/larksuite/oapi-sdk-go/v3/event/dispatcher"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	larkws "github.com/larksuite/oapi-sdk-go/v3/ws"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/channels"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/identity"
	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/utils"
)

type FeishuChannel struct {
	*channels.BaseChannel
	config   config.FeishuConfig
	client   *lark.Client
	wsClient *larkws.Client

	mu     sync.Mutex
	cancel context.CancelFunc
}

func NewFeishuChannel(cfg config.FeishuConfig, bus *bus.MessageBus) (*FeishuChannel, error) {
	base := channels.NewBaseChannel("feishu", cfg, bus, cfg.AllowFrom,
		channels.WithGroupTrigger(cfg.GroupTrigger),
		channels.WithReasoningChannelID(cfg.ReasoningChannelID),
	)

	return &FeishuChannel{
		BaseChannel: base,
		config:      cfg,
		client:      lark.NewClient(cfg.AppID, cfg.AppSecret),
	}, nil
}

func (c *FeishuChannel) Start(ctx context.Context) error {
	if c.config.AppID == "" || c.config.AppSecret == "" {
		return fmt.Errorf("feishu app_id or app_secret is empty")
	}

	dispatcher := larkdispatcher.NewEventDispatcher(c.config.VerificationToken, c.config.EncryptKey).
		OnP2MessageReceiveV1(c.handleMessageReceive).
		// Backward compatibility for legacy "message" event subscriptions.
		OnP1MessageReceiveV1(c.handleLegacyMessageReceive)

	runCtx, cancel := context.WithCancel(ctx)

	c.mu.Lock()
	c.cancel = cancel
	c.wsClient = larkws.NewClient(
		c.config.AppID,
		c.config.AppSecret,
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

func (c *FeishuChannel) Send(ctx context.Context, msg bus.OutboundMessage) error {
	if !c.IsRunning() {
		return channels.ErrNotRunning
	}

	if msg.ChatID == "" {
		return fmt.Errorf("chat ID is empty")
	}

	content := strings.TrimSpace(msg.Content)
	if content == "" {
		return nil
	}

	// Send as "post" with markdown to match Feishu's rich rendering behavior.
	// Inspired by: ref/clawdbot-feishu/src/send.ts (tag: "md")
	content = normalizeFeishuMarkdownLinks(content)
	chunks := utils.SplitMessage(content, feishuPostChunkLimit)
	if len(chunks) == 0 {
		return nil
	}

	sendID := time.Now().UnixNano()
	for idx, chunk := range chunks {
		chunk = strings.TrimSpace(chunk)
		if chunk == "" {
			continue
		}

		postJSON, err := buildFeishuPostMarkdownContent(chunk)
		if err != nil {
			return err
		}

		req := larkim.NewCreateMessageReqBuilder().
			ReceiveIdType(larkim.ReceiveIdTypeChatId).
			Body(larkim.NewCreateMessageReqBodyBuilder().
				ReceiveId(msg.ChatID).
				MsgType("post").
				Content(postJSON).
				Uuid(fmt.Sprintf("picoclaw-%d-%d", sendID, idx)).
				Build()).
			Build()

		resp, err := c.client.Im.V1.Message.Create(ctx, req)
		if err != nil {
			return fmt.Errorf("%w: feishu send: %v", channels.ErrTemporary, err)
		}

		if !resp.Success() {
			fields := map[string]any{
				"chat_id": msg.ChatID,
				"chunk":   idx,
				"code":    resp.Code,
				"msg":     resp.Msg,
			}
			if resp.Code == 99991672 {
				fields["hint"] = "missing app scopes, enable im:message:send_as_bot (or equivalent) and publish the app"
			}
			logger.ErrorCF("feishu", "Feishu message send rejected", fields)

			if resp.Code == 99991672 {
				return fmt.Errorf("%w: feishu api error: code=%d msg=%s", channels.ErrSendFailed, resp.Code, resp.Msg)
			}
			return fmt.Errorf("%w: feishu api error: code=%d msg=%s", channels.ErrTemporary, resp.Code, resp.Msg)
		}

		logger.DebugCF("feishu", "Feishu message sent", map[string]any{
			"chat_id": msg.ChatID,
			"chunk":   idx,
		})
	}

	return nil
}

func (c *FeishuChannel) handleMessageReceive(ctx context.Context, event *larkim.P2MessageReceiveV1) error {
	if event == nil || event.Event == nil || event.Event.Message == nil {
		logger.DebugC("feishu", "Ignored empty P2 message event")
		return nil
	}

	message := event.Event.Message
	sender := event.Event.Sender

	chatID := stringValue(message.ChatId)
	if chatID == "" {
		logger.DebugC("feishu", "Ignored P2 message event with empty chat_id")
		return nil
	}

	senderID := extractFeishuSenderID(sender)
	if senderID == "" {
		senderID = "unknown"
	}

	content := extractFeishuMessageContent(message)
	if content == "" {
		content = "[empty message]"
	}

	metadata := map[string]string{}
	messageID := ""
	if mid := stringValue(message.MessageId); mid != "" {
		messageID = mid
	}
	if messageType := stringValue(message.MessageType); messageType != "" {
		metadata["message_type"] = messageType
	}
	if chatType := stringValue(message.ChatType); chatType != "" {
		metadata["chat_type"] = chatType
	}
	if sender != nil && sender.TenantKey != nil {
		metadata["tenant_key"] = *sender.TenantKey
	}

	chatType := stringValue(message.ChatType)
	var peer bus.Peer
	if chatType == "p2p" {
		peer = bus.Peer{Kind: "direct", ID: senderID}
	} else {
		peer = bus.Peer{Kind: "group", ID: chatID}
		// In group chats, apply unified group trigger filtering
		respond, cleaned := c.ShouldRespondInGroup(false, content)
		if !respond {
			return nil
		}
		content = cleaned
	}

	logger.InfoCF("feishu", "Feishu message received", map[string]any{
		"sender_id": senderID,
		"chat_id":   chatID,
		"preview":   utils.Truncate(content, 80),
	})

	senderInfo := bus.SenderInfo{
		Platform:    "feishu",
		PlatformID:  senderID,
		CanonicalID: identity.BuildCanonicalID("feishu", senderID),
	}

	if !c.IsAllowedSender(senderInfo) {
		return nil
	}

	c.HandleMessage(ctx, peer, messageID, senderID, chatID, content, nil, metadata, senderInfo)
	return nil
}

func (c *FeishuChannel) handleLegacyMessageReceive(ctx context.Context, event *larkim.P1MessageReceiveV1) error {
	if event == nil || event.Event == nil {
		logger.DebugC("feishu", "Ignored empty P1 legacy message event")
		return nil
	}

	payload := event.Event
	chatID := strings.TrimSpace(payload.OpenChatID)
	if chatID == "" {
		logger.DebugC("feishu", "Ignored P1 legacy event with empty open_chat_id")
		return nil
	}

	senderID := strings.TrimSpace(payload.EmployeeID)
	if senderID == "" {
		senderID = strings.TrimSpace(payload.OpenID)
	}
	if senderID == "" {
		senderID = strings.TrimSpace(payload.UnionID)
	}
	if senderID == "" {
		senderID = "unknown"
	}

	content := strings.TrimSpace(payload.TextWithoutAtBot)
	if content == "" {
		content = strings.TrimSpace(payload.Text)
	}
	if content == "" {
		content = "[empty message]"
	}

	metadata := map[string]string{}
	messageID := strings.TrimSpace(payload.OpenMessageID)
	if messageID != "" {
		metadata["message_id"] = messageID
	}
	if payload.MsgType != "" {
		metadata["message_type"] = payload.MsgType
	}
	if payload.ChatType != "" {
		metadata["chat_type"] = payload.ChatType
	}
	if payload.TenantKey != "" {
		metadata["tenant_key"] = payload.TenantKey
	}

	chatType := strings.ToLower(strings.TrimSpace(payload.ChatType))
	peer := bus.Peer{Kind: "group", ID: chatID}
	if chatType == "p2p" || chatType == "private" {
		peer = bus.Peer{Kind: "direct", ID: senderID}
		metadata["peer_kind"] = "direct"
		metadata["peer_id"] = senderID
	} else {
		metadata["peer_kind"] = "group"
		metadata["peer_id"] = chatID

		// In group chats, apply unified group trigger filtering
		respond, cleaned := c.ShouldRespondInGroup(false, content)
		if !respond {
			return nil
		}
		content = cleaned
	}

	logger.InfoCF("feishu", "Feishu message received (legacy event)", map[string]any{
		"sender_id": senderID,
		"chat_id":   chatID,
		"preview":   utils.Truncate(content, 80),
	})

	senderInfo := bus.SenderInfo{
		Platform:    "feishu",
		PlatformID:  senderID,
		CanonicalID: identity.BuildCanonicalID("feishu", senderID),
	}

	c.HandleMessage(ctx, peer, messageID, senderID, chatID, content, nil, metadata, senderInfo)
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

func extractFeishuMessageContent(message *larkim.EventMessage) string {
	if message == nil || message.Content == nil || *message.Content == "" {
		return ""
	}

	raw := *message.Content
	messageType := ""
	if message.MessageType != nil {
		messageType = strings.TrimSpace(*message.MessageType)
	}

	switch messageType {
	case larkim.MsgTypeText:
		var textPayload struct {
			Text string `json:"text"`
		}
		if err := json.Unmarshal([]byte(raw), &textPayload); err == nil {
			return textPayload.Text
		}
	case "post":
		if extracted := extractFeishuPostContent(raw); extracted != "" {
			return extracted
		}
		// Fall through: return raw JSON if we failed to parse
	case "image":
		return "<media:image>"
	case "file":
		return "<media:file>"
	case "audio":
		return "<media:audio>"
	case "video", "media":
		return "<media:video>"
	case "sticker":
		return "<media:sticker>"
	}

	return raw
}

func extractFeishuPostContent(rawJSON string) string {
	var payload map[string]any
	if err := json.Unmarshal([]byte(rawJSON), &payload); err != nil {
		return ""
	}

	title, _ := payload["title"].(string)

	var content any
	if zh, ok := payload["zh_cn"].(map[string]any); ok {
		content = zh["content"]
	} else {
		content = payload["content"]
	}

	lines := extractFeishuPostLines(content)
	if len(lines) == 0 {
		return strings.TrimSpace(title)
	}

	if strings.TrimSpace(title) != "" {
		lines = append([]string{title}, lines...)
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

func extractFeishuPostLines(content any) []string {
	paras, ok := content.([]any)
	if !ok || len(paras) == 0 {
		return nil
	}

	lines := make([]string, 0, len(paras))
	for _, para := range paras {
		nodes, ok := para.([]any)
		if !ok {
			continue
		}
		var sb strings.Builder
		for _, node := range nodes {
			obj, ok := node.(map[string]any)
			if !ok {
				continue
			}
			appendFeishuPostNodeText(&sb, obj)
		}
		line := strings.TrimSpace(sb.String())
		if line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

func appendFeishuPostNodeText(sb *strings.Builder, node map[string]any) {
	tag, _ := node["tag"].(string)
	tag = strings.ToLower(strings.TrimSpace(tag))
	switch tag {
	case "text", "md":
		if text, _ := node["text"].(string); text != "" {
			sb.WriteString(text)
		}
	case "a":
		if text, _ := node["text"].(string); text != "" {
			sb.WriteString(text)
			return
		}
		if href, _ := node["href"].(string); href != "" {
			sb.WriteString(href)
		}
	case "at":
		// Best-effort: keep something readable, actual mention expansion is not available here.
		if name, _ := node["user_name"].(string); name != "" {
			sb.WriteString("@")
			sb.WriteString(name)
			return
		}
		if name, _ := node["name"].(string); name != "" {
			sb.WriteString("@")
			sb.WriteString(name)
			return
		}
		if openID, _ := node["open_id"].(string); openID != "" {
			sb.WriteString("@")
			sb.WriteString(openID)
			return
		}
	case "img":
		sb.WriteString("[image]")
	default:
		if text, _ := node["text"].(string); text != "" {
			sb.WriteString(text)
		}
	}
}
