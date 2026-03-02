//go:build amd64 || arm64 || riscv64 || mips64 || ppc64

package feishu

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkdispatcher "github.com/larksuite/oapi-sdk-go/v3/event/dispatcher"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	larkws "github.com/larksuite/oapi-sdk-go/v3/ws"

	"github.com/google/uuid"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/channels"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/identity"
	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/media"
	"github.com/sipeed/picoclaw/pkg/utils"
)

const feishuMaxPostImageAttachments = 4

var (
	feishuBRTagRe          = regexp.MustCompile(`(?i)<\s*br\s*/?>`)
	feishuParagraphJoinRe  = regexp.MustCompile(`(?i)<\s*/p\s*>\s*<\s*p\s*>`)
	feishuParagraphOpenRe  = regexp.MustCompile(`(?i)<\s*p\s*>`)
	feishuParagraphCloseRe = regexp.MustCompile(`(?i)<\s*/p\s*>`)
	feishuAnyTagRe         = regexp.MustCompile(`<[^>]+>`)
	feishuMultiNewlineRe   = regexp.MustCompile(`\n{3,}`)
	feishuBrokenBulletRe   = regexp.MustCompile(`(?m)(^|\n)([-*•])\n([^\s])`)
	feishuBrokenNumberedRe = regexp.MustCompile(`(?m)(^|\n)([0-9]+[.)])\n([^\s])`)
)

type FeishuChannel struct {
	*channels.BaseChannel
	config   config.FeishuConfig
	client   *lark.Client
	wsClient *larkws.Client

	mu     sync.Mutex
	cancel context.CancelFunc

	processedMu   sync.Mutex
	processedIDs  map[string]struct{}
	processedRing []string
	processedHead int
}

func NewFeishuChannel(cfg config.FeishuConfig, bus *bus.MessageBus) (*FeishuChannel, error) {
	base := channels.NewBaseChannel("feishu", cfg, bus, cfg.AllowFrom,
		channels.WithGroupTrigger(cfg.GroupTrigger),
		channels.WithReasoningChannelID(cfg.ReasoningChannelID),
	)

	return &FeishuChannel{
		BaseChannel:  base,
		config:       cfg,
		client:       lark.NewClient(cfg.AppID, cfg.AppSecret),
		processedIDs: make(map[string]struct{}),
	}, nil
}

func (c *FeishuChannel) Start(ctx context.Context) error {
	if c.config.AppID == "" || c.config.AppSecret == "" {
		return fmt.Errorf("feishu app_id or app_secret is empty")
	}

	if c.config.GroupTrigger.MentionOnly && strings.TrimSpace(c.config.BotID) == "" {
		logger.WarnC("feishu", "group_trigger.mention_only is enabled but bot_id is empty; falling back to best-effort mention detection")
	}

	dispatcher := larkdispatcher.NewEventDispatcher(c.config.VerificationToken, c.config.EncryptKey).
		OnP2MessageReceiveV1(c.handleMessageReceive).
		// Backward compatibility for legacy "message" event subscriptions.
		OnP1MessageReceiveV1(c.handleLegacyMessageReceive)

	runCtx, cancel := context.WithCancel(ctx)

	c.mu.Lock()
	c.cancel = cancel
	c.mu.Unlock()

	c.SetRunning(true)
	logger.InfoC("feishu", "Feishu channel started (websocket mode)")

	go func() {
		backoff := 1 * time.Second
		for {
			c.mu.Lock()
			// Stop() sets cancel=nil; treat that as terminal.
			if c.cancel == nil {
				c.mu.Unlock()
				return
			}
			wsClient := larkws.NewClient(
				c.config.AppID,
				c.config.AppSecret,
				larkws.WithEventHandler(dispatcher),
			)
			c.wsClient = wsClient
			c.mu.Unlock()

			if err := wsClient.Start(runCtx); err != nil {
				// larkws client should handle reconnect internally, but keep a best-effort
				// outer restart loop to survive unexpected exits.
				if runCtx.Err() == nil {
					logger.ErrorCF("feishu", "Feishu websocket stopped with error", map[string]any{
						"error": err.Error(),
					})
				}
			}

			if runCtx.Err() != nil {
				return
			}

			logger.WarnCF("feishu", "Feishu websocket stopped; retrying", map[string]any{
				"backoff_s": backoff.Seconds(),
			})
			select {
			case <-runCtx.Done():
				return
			case <-time.After(backoff):
			}
			if backoff < 30*time.Second {
				backoff *= 2
				if backoff > 30*time.Second {
					backoff = 30 * time.Second
				}
			}
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

// EditMessage implements channels.MessageEditor.
func (c *FeishuChannel) EditMessage(ctx context.Context, chatID string, messageID string, content string) error {
	if !c.IsRunning() {
		return channels.ErrNotRunning
	}
	if strings.TrimSpace(messageID) == "" {
		return fmt.Errorf("message ID is empty")
	}
	if strings.TrimSpace(content) == "" {
		content = " "
	}

	payload, _ := json.Marshal(map[string]string{"text": content})
	req := larkim.NewUpdateMessageReqBuilder().
		MessageId(messageID).
		Body(larkim.NewUpdateMessageReqBodyBuilder().
			MsgType("text").
			Content(string(payload)).
			Build()).
		Build()

	resp, err := c.client.Im.V1.Message.Update(ctx, req)
	if err != nil {
		return fmt.Errorf("%w: feishu edit message: %v", channels.ErrTemporary, err)
	}
	if resp == nil || !resp.Success() {
		code := 0
		msgText := ""
		if resp != nil {
			code = resp.Code
			msgText = resp.Msg
		}
		errKind := channels.ErrTemporary
		if code == 99991672 {
			errKind = channels.ErrSendFailed
		}
		return fmt.Errorf("%w: feishu edit message rejected: code=%d msg=%s", errKind, code, msgText)
	}
	return nil
}

// SendPlaceholder implements channels.PlaceholderCapable.
func (c *FeishuChannel) SendPlaceholder(ctx context.Context, chatID string) (string, error) {
	if !c.config.Placeholder.Enabled {
		return "", nil
	}
	if strings.TrimSpace(chatID) == "" {
		return "", nil
	}

	text := strings.TrimSpace(c.config.Placeholder.Text)
	if text == "" {
		text = "正在思考..."
	}

	payload, _ := json.Marshal(map[string]string{"text": text})
	req := larkim.NewCreateMessageReqBuilder().
		ReceiveIdType(larkim.ReceiveIdTypeChatId).
		Body(larkim.NewCreateMessageReqBodyBuilder().
			ReceiveId(chatID).
			MsgType("text").
			Content(string(payload)).
			Uuid("picoclaw-ph-" + uuid.NewString()).
			Build()).
		Build()

	resp, err := c.client.Im.V1.Message.Create(ctx, req)
	if err != nil {
		return "", fmt.Errorf("%w: feishu send placeholder: %v", channels.ErrTemporary, err)
	}
	if resp == nil || !resp.Success() || resp.Data == nil || resp.Data.MessageId == nil || strings.TrimSpace(*resp.Data.MessageId) == "" {
		code := 0
		msgText := ""
		if resp != nil {
			code = resp.Code
			msgText = resp.Msg
		}
		errKind := channels.ErrTemporary
		if code == 99991672 {
			errKind = channels.ErrSendFailed
		}
		return "", fmt.Errorf("%w: feishu send placeholder rejected: code=%d msg=%s", errKind, code, msgText)
	}

	return strings.TrimSpace(*resp.Data.MessageId), nil
}

// SendMedia implements channels.MediaSender.
// It uploads files/images to Feishu and then sends them as native attachments.
func (c *FeishuChannel) SendMedia(ctx context.Context, msg bus.OutboundMediaMessage) error {
	if !c.IsRunning() {
		return channels.ErrNotRunning
	}

	if msg.ChatID == "" {
		return fmt.Errorf("chat ID is empty")
	}

	store := c.GetMediaStore()
	if store == nil {
		return fmt.Errorf("no media store available: %w", channels.ErrSendFailed)
	}

	sendID := time.Now().UnixNano()
	partIdx := 0

	for _, part := range msg.Parts {
		ref := strings.TrimSpace(part.Ref)
		if ref == "" {
			continue
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

		localPath, meta, err := store.ResolveWithMeta(ref)
		if err != nil {
			logger.ErrorCF("feishu", "Failed to resolve media ref", map[string]any{
				"ref":   ref,
				"error": err.Error(),
			})
			continue
		}

		filename := strings.TrimSpace(part.Filename)
		if filename == "" {
			filename = strings.TrimSpace(meta.Filename)
		}
		if filename == "" {
			filename = filepath.Base(localPath)
		}
		filename = utils.SanitizeFilename(filename)

		contentType := strings.TrimSpace(part.ContentType)
		if contentType == "" {
			contentType = strings.TrimSpace(meta.ContentType)
		}

		mediaType := strings.TrimSpace(part.Type)
		if mediaType == "" {
			if strings.HasPrefix(contentType, "image/") {
				mediaType = "image"
			} else {
				mediaType = "file"
			}
		}

		switch mediaType {
		case "image":
			// Feishu limits image uploads to 10MB (SDK comment).
			if st, statErr := os.Stat(localPath); statErr == nil && st.Size() > 10*1024*1024 {
				return fmt.Errorf("%w: feishu image too large (>10MB): %s", channels.ErrSendFailed, filename)
			}

			f, err := os.Open(localPath)
			if err != nil {
				return fmt.Errorf("feishu open image: %w", err)
			}

			imgReq := larkim.NewCreateImageReqBuilder().
				Body(larkim.NewCreateImageReqBodyBuilder().
					ImageType("message").
					Image(f).
					Build()).
				Build()

			imgResp, err := c.client.Im.V1.Image.Create(ctx, imgReq)
			f.Close()
			if err != nil {
				return fmt.Errorf("%w: feishu upload image: %v", channels.ErrTemporary, err)
			}
			if imgResp == nil || !imgResp.Success() || imgResp.Data == nil || imgResp.Data.ImageKey == nil || *imgResp.Data.ImageKey == "" {
				code := 0
				msgText := ""
				if imgResp != nil {
					code = imgResp.Code
					msgText = imgResp.Msg
				}
				errKind := channels.ErrTemporary
				if code == 99991672 {
					errKind = channels.ErrSendFailed
				}
				return fmt.Errorf("%w: feishu upload image rejected: code=%d msg=%s", errKind, code, msgText)
			}

			payload, _ := json.Marshal(map[string]string{
				"image_key": *imgResp.Data.ImageKey,
			})

			createReq := larkim.NewCreateMessageReqBuilder().
				ReceiveIdType(larkim.ReceiveIdTypeChatId).
				Body(larkim.NewCreateMessageReqBodyBuilder().
					ReceiveId(msg.ChatID).
					MsgType("image").
					Content(string(payload)).
					Uuid(fmt.Sprintf("picoclaw-media-%d-%d", sendID, partIdx)).
					Build()).
				Build()

			resp, err := c.client.Im.V1.Message.Create(ctx, createReq)
			if err != nil {
				return fmt.Errorf("%w: feishu send image: %v", channels.ErrTemporary, err)
			}
			if resp == nil || !resp.Success() {
				code := 0
				msgText := ""
				if resp != nil {
					code = resp.Code
					msgText = resp.Msg
				}
				errKind := channels.ErrTemporary
				if code == 99991672 {
					errKind = channels.ErrSendFailed
				}
				return fmt.Errorf("%w: feishu send image rejected: code=%d msg=%s", errKind, code, msgText)
			}

		default:
			f, err := os.Open(localPath)
			if err != nil {
				return fmt.Errorf("feishu open file: %w", err)
			}

			fileType, msgType := resolveFeishuFileUploadTypes(mediaType, filename, contentType)

			fileReq := larkim.NewCreateFileReqBuilder().
				Body(larkim.NewCreateFileReqBodyBuilder().
					FileType(fileType).
					FileName(filename).
					File(f).
					Build()).
				Build()

			fileResp, err := c.client.Im.V1.File.Create(ctx, fileReq)
			f.Close()
			if err != nil {
				return fmt.Errorf("%w: feishu upload file: %v", channels.ErrTemporary, err)
			}
			if fileResp == nil || !fileResp.Success() || fileResp.Data == nil || fileResp.Data.FileKey == nil || *fileResp.Data.FileKey == "" {
				code := 0
				msgText := ""
				if fileResp != nil {
					code = fileResp.Code
					msgText = fileResp.Msg
				}
				errKind := channels.ErrTemporary
				if code == 99991672 {
					errKind = channels.ErrSendFailed
				}
				return fmt.Errorf("%w: feishu upload file rejected: code=%d msg=%s", errKind, code, msgText)
			}

			payload, _ := json.Marshal(map[string]string{
				"file_key": *fileResp.Data.FileKey,
			})

			createReq := larkim.NewCreateMessageReqBuilder().
				ReceiveIdType(larkim.ReceiveIdTypeChatId).
				Body(larkim.NewCreateMessageReqBodyBuilder().
					ReceiveId(msg.ChatID).
					MsgType(msgType).
					Content(string(payload)).
					Uuid(fmt.Sprintf("picoclaw-media-%d-%d", sendID, partIdx)).
					Build()).
				Build()

			resp, err := c.client.Im.V1.Message.Create(ctx, createReq)
			if err != nil {
				return fmt.Errorf("%w: feishu send %s: %v", channels.ErrTemporary, msgType, err)
			}
			if resp == nil || !resp.Success() {
				code := 0
				msgText := ""
				if resp != nil {
					code = resp.Code
					msgText = resp.Msg
				}
				errKind := channels.ErrTemporary
				if code == 99991672 {
					errKind = channels.ErrSendFailed
				}
				return fmt.Errorf("%w: feishu send %s rejected: code=%d msg=%s", errKind, msgType, code, msgText)
			}
		}

		partIdx++
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

	senderInfo := bus.SenderInfo{
		Platform:    "feishu",
		PlatformID:  senderID,
		CanonicalID: identity.BuildCanonicalID("feishu", senderID),
	}

	// check allowlist early to avoid expensive processing (e.g. media downloads) for rejected users
	if !c.IsAllowedSender(senderInfo) {
		return nil
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
	if messageID != "" && c.isDuplicateMessageID(messageID) {
		return nil
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
		isMentioned, stripped := feishuDetectAndStripBotMention(message, content, strings.TrimSpace(c.config.BotID))
		// In group chats, apply unified group trigger filtering
		respond, cleaned := c.ShouldRespondInGroup(isMentioned, stripped)
		if !respond {
			return nil
		}
		content = cleaned
	}

	mediaRefs := []string{}
	rawContent := stringValue(message.Content)
	messageType := strings.TrimSpace(stringValue(message.MessageType))
	if messageID != "" && rawContent != "" {
		scope := channels.BuildMediaScope("feishu", chatID, messageID)

		// Helper to register a local file with the media store.
		storeMedia := func(localPath string, meta media.MediaMeta) string {
			if store := c.GetMediaStore(); store != nil {
				ref, err := store.Store(localPath, meta, scope)
				if err == nil {
					return ref
				}
			}
			return localPath // fallback: raw path
		}

		switch messageType {
		case "post":
			imageKeys := extractFeishuPostImageKeys(rawContent)
			for _, imageKey := range imageKeys {
				if len(mediaRefs) >= feishuMaxPostImageAttachments {
					break
				}
				localPath, filename, contentType, err := c.downloadMessageResource(ctx, messageID, imageKey, "image")
				if err != nil {
					logger.WarnCF("feishu", "Failed to download post image resource", map[string]any{
						"message_id": messageID,
						"image_key":  imageKey,
						"error":      err.Error(),
					})
					continue
				}
				mediaRefs = append(mediaRefs, storeMedia(localPath, media.MediaMeta{
					Filename:    filename,
					ContentType: contentType,
					Source:      "feishu",
				}))
			}
		case "image":
			imageKey := feishuExtractJSONString(rawContent, "image_key", "file_key", "imageKey", "fileKey")
			if imageKey != "" {
				localPath, filename, contentType, err := c.downloadMessageResource(ctx, messageID, imageKey, "image")
				if err != nil {
					logger.WarnCF("feishu", "Failed to download image resource", map[string]any{
						"message_id": messageID,
						"image_key":  imageKey,
						"error":      err.Error(),
					})
				} else {
					mediaRefs = append(mediaRefs, storeMedia(localPath, media.MediaMeta{
						Filename:    filename,
						ContentType: contentType,
						Source:      "feishu",
					}))
				}
			}
		case "file", "audio", "video", "media":
			fileKey := feishuExtractJSONString(rawContent, "file_key", "fileKey")
			if fileKey != "" {
				localPath, filename, contentType, err := c.downloadMessageResource(ctx, messageID, fileKey, "file")
				if err != nil {
					logger.WarnCF("feishu", "Failed to download file resource", map[string]any{
						"message_id": messageID,
						"file_key":   fileKey,
						"error":      err.Error(),
					})
				} else {
					mediaRefs = append(mediaRefs, storeMedia(localPath, media.MediaMeta{
						Filename:    filename,
						ContentType: contentType,
						Source:      "feishu",
					}))
				}
			}
		}

		if len(mediaRefs) > 0 {
			// make the content human-friendly (the media refs carry the actual attachment)
			label := "[media]"
			switch messageType {
			case "image":
				label = "[image]"
			case "audio":
				label = "[audio]"
			case "video", "media":
				label = "[video]"
			case "file":
				label = "[file]"
			case "post":
				label = "[image]"
			}

			// Replace placeholder-only content like "<media:image>" with a cleaner marker.
			if strings.HasPrefix(strings.TrimSpace(content), "<media:") {
				content = ""
			}
			if content != "" {
				content += "\n"
			}
			content += label
		}
	}

	logger.InfoCF("feishu", "Feishu message received", map[string]any{
		"sender_id": senderID,
		"chat_id":   chatID,
		"preview":   utils.Truncate(content, 80),
		"media":     len(mediaRefs),
	})

	c.HandleMessage(ctx, peer, messageID, senderID, chatID, content, mediaRefs, metadata, senderInfo)
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
	isMentioned := strings.TrimSpace(payload.TextWithoutAtBot) != "" &&
		strings.TrimSpace(payload.TextWithoutAtBot) != strings.TrimSpace(payload.Text)

	metadata := map[string]string{}
	messageID := strings.TrimSpace(payload.OpenMessageID)
	if messageID != "" && c.isDuplicateMessageID(messageID) {
		return nil
	}
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
		respond, cleaned := c.ShouldRespondInGroup(isMentioned, content)
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

func (c *FeishuChannel) isDuplicateMessageID(messageID string) bool {
	const maxProcessed = 2048

	messageID = strings.TrimSpace(messageID)
	if messageID == "" {
		return false
	}

	c.processedMu.Lock()
	defer c.processedMu.Unlock()

	if _, ok := c.processedIDs[messageID]; ok {
		return true
	}

	c.processedIDs[messageID] = struct{}{}
	c.processedRing = append(c.processedRing, messageID)

	// Evict oldest when over limit.
	if len(c.processedRing)-c.processedHead > maxProcessed {
		evict := c.processedRing[c.processedHead]
		delete(c.processedIDs, evict)
		c.processedHead++

		// Compact periodically to avoid unbounded growth of the underlying array.
		if c.processedHead > maxProcessed && c.processedHead*2 >= len(c.processedRing) {
			c.processedRing = append([]string{}, c.processedRing[c.processedHead:]...)
			c.processedHead = 0
		}
	}

	return false
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
			return normalizeFeishuText(textPayload.Text)
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

func normalizeFeishuText(raw string) string {
	t := strings.TrimSpace(raw)
	if t == "" {
		return ""
	}

	t = feishuBRTagRe.ReplaceAllString(t, "\n")
	t = feishuParagraphJoinRe.ReplaceAllString(t, "\n")
	t = feishuParagraphOpenRe.ReplaceAllString(t, "")
	t = feishuParagraphCloseRe.ReplaceAllString(t, "")
	t = feishuAnyTagRe.ReplaceAllString(t, "")
	t = html.UnescapeString(t)
	t = strings.ReplaceAll(t, "\u00a0", " ")

	t = strings.ReplaceAll(t, "\r\n", "\n")
	t = strings.ReplaceAll(t, "\r", "\n")
	t = feishuMultiNewlineRe.ReplaceAllString(t, "\n\n")

	// Fix Feishu list quirk: "-" or "1." marker and content split across lines.
	t = feishuBrokenBulletRe.ReplaceAllString(t, "$1$2 $3")
	t = feishuBrokenNumberedRe.ReplaceAllString(t, "$1$2 $3")

	return strings.TrimSpace(t)
}

func feishuDetectAndStripBotMention(message *larkim.EventMessage, extractedContent, botID string) (isMentioned bool, cleaned string) {
	cleaned = extractedContent
	if message == nil {
		return false, strings.TrimSpace(cleaned)
	}

	mentions := message.Mentions
	if len(mentions) == 0 {
		return false, strings.TrimSpace(cleaned)
	}

	messageType := strings.TrimSpace(stringValue(message.MessageType))

	// For plain text, content may include mention keys like "@_user_1" which we can strip/replace.
	if messageType == larkim.MsgTypeText {
		cleanedText, mentioned := feishuCleanTextMentions(cleaned, mentions, botID)
		return mentioned, cleanedText
	}

	// For non-text messages, we can only infer mention from the mentions array.
	if botID == "" {
		return true, strings.TrimSpace(cleaned)
	}

	for _, m := range mentions {
		if m == nil {
			continue
		}
		if feishuUserIDMatches(m.Id, botID) {
			return true, strings.TrimSpace(cleaned)
		}
	}

	return false, strings.TrimSpace(cleaned)
}

func feishuCleanTextMentions(text string, mentions []*larkim.MentionEvent, botID string) (cleaned string, isMentioned bool) {
	cleaned = strings.TrimSpace(text)
	if cleaned == "" || len(mentions) == 0 {
		return cleaned, false
	}

	// If botID is unknown, use a best-effort heuristic:
	// treat leading mention(s) as the "addressing" mention(s) and strip them.
	if botID == "" {
		for {
			removed := false
			for _, m := range mentions {
				if m == nil {
					continue
				}
				key := strings.TrimSpace(stringValue(m.Key))
				if key == "" {
					continue
				}
				if strings.HasPrefix(cleaned, key) {
					isMentioned = true
					cleaned = strings.TrimSpace(strings.TrimPrefix(cleaned, key))
					removed = true
					break
				}
			}
			if !removed {
				break
			}
		}

		// Replace remaining mention keys with "@Name" for readability.
		for _, m := range mentions {
			if m == nil {
				continue
			}
			key := strings.TrimSpace(stringValue(m.Key))
			name := strings.TrimSpace(stringValue(m.Name))
			if key == "" || name == "" {
				continue
			}
			cleaned = strings.ReplaceAll(cleaned, key, "@"+name)
		}

		return feishuNormalizeWhitespace(cleaned), isMentioned
	}

	// botID configured: detect bot mention by ID and strip bot mention key(s).
	botKeys := make(map[string]struct{})
	for _, m := range mentions {
		if m == nil {
			continue
		}
		if feishuUserIDMatches(m.Id, botID) {
			isMentioned = true
			if key := strings.TrimSpace(stringValue(m.Key)); key != "" {
				botKeys[key] = struct{}{}
			}
		}
	}

	for key := range botKeys {
		cleaned = strings.ReplaceAll(cleaned, key, "")
	}

	// Replace other mention keys with "@Name" for readability.
	for _, m := range mentions {
		if m == nil {
			continue
		}
		key := strings.TrimSpace(stringValue(m.Key))
		name := strings.TrimSpace(stringValue(m.Name))
		if key == "" || name == "" {
			continue
		}
		if _, ok := botKeys[key]; ok {
			continue
		}
		cleaned = strings.ReplaceAll(cleaned, key, "@"+name)
	}

	return feishuNormalizeWhitespace(cleaned), isMentioned
}

func feishuNormalizeWhitespace(s string) string {
	s = strings.TrimSpace(s)
	for strings.Contains(s, "  ") {
		s = strings.ReplaceAll(s, "  ", " ")
	}
	s = strings.ReplaceAll(s, "\n ", "\n")
	s = strings.ReplaceAll(s, " \n", "\n")
	return strings.TrimSpace(s)
}

func feishuUserIDMatches(id *larkim.UserId, want string) bool {
	if id == nil || want == "" {
		return false
	}
	if id.UserId != nil && *id.UserId == want {
		return true
	}
	if id.OpenId != nil && *id.OpenId == want {
		return true
	}
	if id.UnionId != nil && *id.UnionId == want {
		return true
	}
	return false
}

func feishuExtractJSONString(rawJSON string, keys ...string) string {
	rawJSON = strings.TrimSpace(rawJSON)
	if rawJSON == "" || len(keys) == 0 {
		return ""
	}

	var payload map[string]any
	if err := json.Unmarshal([]byte(rawJSON), &payload); err != nil {
		return ""
	}

	for _, key := range keys {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		if v, ok := payload[key]; ok {
			if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
				return strings.TrimSpace(s)
			}
		}
	}
	return ""
}

func extractFeishuPostImageKeys(rawJSON string) []string {
	rawJSON = strings.TrimSpace(rawJSON)
	if rawJSON == "" {
		return nil
	}

	var payload any
	if err := json.Unmarshal([]byte(rawJSON), &payload); err != nil {
		return nil
	}

	keys := map[string]struct{}{}
	var walk func(v any)
	walk = func(v any) {
		switch n := v.(type) {
		case map[string]any:
			if imageKey, ok := n["image_key"].(string); ok && strings.TrimSpace(imageKey) != "" {
				keys[strings.TrimSpace(imageKey)] = struct{}{}
			}
			if imageKey, ok := n["imageKey"].(string); ok && strings.TrimSpace(imageKey) != "" {
				keys[strings.TrimSpace(imageKey)] = struct{}{}
			}
			for _, child := range n {
				walk(child)
			}
		case []any:
			for _, child := range n {
				walk(child)
			}
		}
	}
	walk(payload)

	out := make([]string, 0, len(keys))
	for k := range keys {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func resolveFeishuFileUploadTypes(mediaType, filename, contentType string) (fileType, messageType string) {
	ext := strings.TrimPrefix(strings.ToLower(filepath.Ext(filename)), ".")
	if ext == "" {
		ct := strings.ToLower(strings.TrimSpace(contentType))
		switch {
		case strings.HasPrefix(ct, "video/mp4"):
			ext = "mp4"
		case strings.Contains(ct, "opus"):
			ext = "opus"
		}
	}
	if ext == "" {
		ext = "file"
	}

	messageType = "file"
	switch strings.ToLower(strings.TrimSpace(mediaType)) {
	case "video":
		if ext == "mp4" {
			messageType = "media"
		}
	case "audio":
		if ext == "opus" {
			messageType = "audio"
		}
	}
	return ext, messageType
}

func (c *FeishuChannel) downloadMessageResource(ctx context.Context, messageID, fileKey, resourceType string) (localPath, filename, contentType string, err error) {
	if messageID == "" {
		return "", "", "", fmt.Errorf("feishu resource download: message_id is empty")
	}
	if fileKey == "" {
		return "", "", "", fmt.Errorf("feishu resource download: file_key is empty")
	}
	resourceType = strings.TrimSpace(resourceType)
	if resourceType == "" {
		return "", "", "", fmt.Errorf("feishu resource download: type is empty")
	}

	req := larkim.NewGetMessageResourceReqBuilder().
		MessageId(messageID).
		FileKey(fileKey).
		Type(resourceType).
		Build()

	// Avoid blocking websocket handler forever on stuck downloads.
	dlCtx := ctx
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		dlCtx, cancel = context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
	}

	var resp *larkim.GetMessageResourceResp
	var lastErr error
	backoff := 400 * time.Millisecond
retry:
	for attempt := 0; attempt < 3; attempt++ {
		resp, err = c.client.Im.V1.MessageResource.Get(dlCtx, req)
		if err == nil {
			lastErr = nil
			break
		}
		lastErr = err
		// Respect cancellation/deadline immediately.
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || dlCtx.Err() != nil {
			break
		}
		select {
		case <-dlCtx.Done():
			break retry
		case <-time.After(backoff):
		}
		if backoff < 2*time.Second {
			backoff *= 2
		}
	}
	if lastErr != nil {
		return "", "", "", fmt.Errorf("%w: feishu resource download: %v", channels.ErrTemporary, lastErr)
	}
	if resp == nil || resp.File == nil {
		if resp != nil && !resp.Success() {
			return "", "", "", fmt.Errorf("feishu resource download rejected: code=%d msg=%s", resp.Code, resp.Msg)
		}
		return "", "", "", fmt.Errorf("feishu resource download: empty response")
	}

	filename = strings.TrimSpace(resp.FileName)
	if filename == "" {
		filename = fileKey
	}
	filename = utils.SanitizeFilename(filename)

	if resp.ApiResp != nil {
		contentType = strings.TrimSpace(resp.ApiResp.Header.Get("Content-Type"))
		if idx := strings.Index(contentType, ";"); idx >= 0 {
			contentType = strings.TrimSpace(contentType[:idx])
		}
	}

	maxBytes := int64(25 * 1024 * 1024) // default: 25MB for files
	if strings.EqualFold(resourceType, "image") {
		maxBytes = 10 * 1024 * 1024 // Feishu message images: 10MB limit
	}
	if resp.ApiResp != nil {
		if cl := strings.TrimSpace(resp.ApiResp.Header.Get("Content-Length")); cl != "" {
			if n, parseErr := strconv.ParseInt(cl, 10, 64); parseErr == nil && n > 0 && maxBytes > 0 && n > maxBytes {
				return "", "", "", fmt.Errorf("%w: feishu resource download: file too large (%d bytes > %d bytes)", channels.ErrSendFailed, n, maxBytes)
			}
		}
	}

	mediaDir := filepath.Join(os.TempDir(), "picoclaw_media")
	if err := os.MkdirAll(mediaDir, 0o700); err != nil {
		return "", "", "", fmt.Errorf("feishu resource download: create media dir: %w", err)
	}

	localPath = filepath.Join(mediaDir, uuid.New().String()[:8]+"_"+filename)
	f, err := os.Create(localPath)
	if err != nil {
		return "", "", "", fmt.Errorf("feishu resource download: create file: %w", err)
	}
	defer f.Close()

	if closer, ok := resp.File.(interface{ Close() error }); ok {
		defer closer.Close()
	}

	reader := io.Reader(resp.File)
	if maxBytes > 0 {
		reader = io.LimitReader(resp.File, maxBytes+1)
	}

	written, err := io.Copy(f, reader)
	if err != nil {
		f.Close()
		_ = os.Remove(localPath)
		return "", "", "", fmt.Errorf("feishu resource download: write file: %w", err)
	}
	if maxBytes > 0 && written > maxBytes {
		f.Close()
		_ = os.Remove(localPath)
		return "", "", "", fmt.Errorf("%w: feishu resource download: file too large (> %d bytes)", channels.ErrSendFailed, maxBytes)
	}

	return localPath, filename, contentType, nil
}

func extractFeishuPostContent(rawJSON string) string {
	var payload map[string]any
	if err := json.Unmarshal([]byte(rawJSON), &payload); err != nil {
		return ""
	}

	title, _ := payload["title"].(string)
	title = normalizeFeishuText(title)

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
		line := normalizeFeishuText(sb.String())
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
