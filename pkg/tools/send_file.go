package tools

import (
	"context"
	"fmt"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/xwysyy/X-Claw/pkg/bus"
)

type PublishOutboundMediaCallback func(ctx context.Context, msg bus.OutboundMediaMessage) error
type StoreMediaRefCallback func(ctx context.Context, localPath string, meta bus.MediaPart) (string, error)

type SendFileTool struct {
	workspace       string
	restrict        bool
	maxBytes        int64
	storeRef        StoreMediaRefCallback
	publishCallback PublishOutboundMediaCallback
}

func NewSendFileTool(workspace string, restrict bool, maxBytes int64) *SendFileTool {
	if maxBytes <= 0 {
		maxBytes = 10 * 1024 * 1024
	}
	return &SendFileTool{
		workspace: workspace,
		restrict:  restrict,
		maxBytes:  maxBytes,
	}
}

func (t *SendFileTool) Name() string {
	return "send_file"
}

func (t *SendFileTool) Description() string {
	return "Send a local file to the user on a chat channel. " +
		"Input: path (string, required), channel (string, optional), chat_id (string, optional). " +
		"Output: confirmation that the file was sent (silent — user receives the file directly). " +
		"If channel/chat_id are omitted, uses the current conversation context."
}

func (t *SendFileTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{
				"type":        "string",
				"description": "Path to the file to send",
			},
			"channel": map[string]any{
				"type":        "string",
				"description": "Optional: target channel (telegram, whatsapp, etc.)",
			},
			"chat_id": map[string]any{
				"type":        "string",
				"description": "Optional: target chat/user ID",
			},
		},
		"required": []string{"path"},
	}
}

func (t *SendFileTool) SetPublishCallback(callback PublishOutboundMediaCallback) {
	t.publishCallback = callback
}

func (t *SendFileTool) SetStoreRefCallback(callback StoreMediaRefCallback) {
	t.storeRef = callback
}

func (t *SendFileTool) Execute(ctx context.Context, args map[string]any) *ToolResult {
	path, ok := getStringArg(args, "path")
	if !ok || strings.TrimSpace(path) == "" {
		return ErrorResult("path is required")
	}
	if t == nil {
		return ErrorResult("send_file tool is not configured")
	}

	absPath, err := validatePath(path, t.workspace, t.restrict)
	if err != nil {
		return ErrorResult(err.Error()).WithError(err)
	}

	info, err := os.Stat(absPath)
	if err != nil {
		return ErrorResult(fmt.Sprintf("failed to stat file: %v", err)).WithError(err)
	}
	if info.IsDir() {
		return ErrorResult("path must point to a file")
	}
	if t.maxBytes > 0 && info.Size() > t.maxBytes {
		return ErrorResult(fmt.Sprintf("file exceeds max size of %d bytes", t.maxBytes))
	}

	channel, _ := getStringArg(args, "channel")
	chatID, _ := getStringArg(args, "chat_id")
	if strings.TrimSpace(channel) == "" {
		channel = ToolChannel(ctx)
	}
	if strings.TrimSpace(chatID) == "" {
		chatID = ToolChatID(ctx)
	}
	if channel == "" || chatID == "" {
		return ErrorResult("No target channel/chat specified")
	}
	part := bus.MediaPart{
		Type:        "file",
		Filename:    filepath.Base(absPath),
		ContentType: detectFileContentType(absPath),
	}
	if t.storeRef == nil {
		return ErrorResult("media store not configured")
	}
	ref, err := t.storeRef(ctx, absPath, part)
	if err != nil {
		return ErrorResult(fmt.Sprintf("storing file in media store: %v", err)).WithError(err)
	}
	part.Ref = ref
	if t.publishCallback == nil {
		return ErrorResult("File sending not configured")
	}

	msg := bus.OutboundMediaMessage{
		Channel: channel,
		ChatID:  chatID,
		Parts:   []bus.MediaPart{part},
	}
	if err := t.publishCallback(ctx, msg); err != nil {
		return ErrorResult(fmt.Sprintf("sending file: %v", err)).WithError(err)
	}

	if tracker := messageRoundTrackerFromContext(ctx); tracker != nil {
		tracker.MarkSent()
	}

	return SilentResult(fmt.Sprintf("File sent to %s:%s", channel, chatID))
}

func detectFileContentType(path string) string {
	ext := strings.TrimSpace(filepath.Ext(path))
	if ext != "" {
		if guessed := strings.TrimSpace(mime.TypeByExtension(ext)); guessed != "" {
			if mediaType, _, err := mime.ParseMediaType(guessed); err == nil && strings.TrimSpace(mediaType) != "" {
				return mediaType
			}
			return guessed
		}
	}

	file, err := os.Open(path)
	if err != nil {
		return "application/octet-stream"
	}
	defer file.Close()

	buf := make([]byte, 512)
	n, readErr := file.Read(buf)
	if readErr != nil && readErr.Error() != "EOF" {
		return "application/octet-stream"
	}
	if n <= 0 {
		return "application/octet-stream"
	}
	return http.DetectContentType(buf[:n])
}
