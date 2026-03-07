//go:build amd64 || arm64 || riscv64 || mips64 || ppc64

package feishu

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"regexp"
	"sort"
	"strings"

	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"

	"github.com/xwysyy/X-Claw/pkg/channels"
	"github.com/xwysyy/X-Claw/pkg/logger"
)

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

// sendCard sends an interactive card message to a chat and returns the platform message ID.
func (c *FeishuChannel) sendCard(ctx context.Context, chatID, cardContent string) (string, error) {
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
		return "", fmt.Errorf("feishu send card: %w", channels.ErrTemporary)
	}

	if !resp.Success() {
		return "", fmt.Errorf("feishu api error (code=%d msg=%s): %w", resp.Code, resp.Msg, channels.ErrTemporary)
	}

	messageID := ""
	if resp.Data != nil && resp.Data.MessageId != nil {
		messageID = *resp.Data.MessageId
	}

	logger.DebugCF("feishu", "Feishu card message sent", map[string]any{
		"chat_id":    chatID,
		"message_id": messageID,
	})

	return messageID, nil
}

// sendImage uploads an image and sends it as a message.

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
