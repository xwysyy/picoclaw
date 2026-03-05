package agent

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/xwysyy/X-Claw/pkg/bus"
	"github.com/xwysyy/X-Claw/pkg/logger"
	"github.com/xwysyy/X-Claw/pkg/utils"
)

type importedInboundMedia struct {
	RelativePath string
	Filename     string
	ContentType  string
	SizeBytes    int64
	SourceRef    string
}

func (al *AgentLoop) importInboundMediaAndBuildNote(agent *AgentInstance, msg bus.InboundMessage) string {
	if agent == nil || strings.TrimSpace(agent.Workspace) == "" {
		return ""
	}
	if len(msg.Media) == 0 {
		return ""
	}

	imported, skipped := al.importInboundMediaToWorkspace(agent.Workspace, msg)
	if len(imported) == 0 && skipped == 0 {
		return ""
	}
	return formatInboundMediaNote(imported, skipped)
}

func (al *AgentLoop) importInboundMediaToWorkspace(workspace string, msg bus.InboundMessage) ([]importedInboundMedia, int) {
	const maxFiles = 12
	const maxImportBytes = int64(30 * 1024 * 1024) // 30MB safety limit per file

	channel := sanitizePathSegment(msg.Channel)
	chatID := sanitizePathSegment(msg.ChatID)
	messageID := sanitizePathSegment(msg.MessageID)
	if messageID == "" || messageID == "unknown" {
		messageID = fmt.Sprintf("msg-%d", time.Now().UnixNano())
	}

	destDir := filepath.Join(workspace, "uploads", channel, chatID, messageID)
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		logger.WarnCF("agent", "Failed to create uploads directory", map[string]any{
			"dir":   destDir,
			"error": err.Error(),
		})
		return nil, 0
	}

	imported := make([]importedInboundMedia, 0, len(msg.Media))
	skipped := 0

	for _, item := range msg.Media {
		if len(imported) >= maxFiles {
			skipped += len(msg.Media) - len(imported)
			break
		}

		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}

		srcPath, meta, ok := al.resolveInboundMedia(item)
		if !ok || strings.TrimSpace(srcPath) == "" {
			skipped++
			continue
		}

		info, err := os.Stat(srcPath)
		if err != nil || info.IsDir() {
			skipped++
			continue
		}
		if info.Size() > maxImportBytes {
			skipped++
			continue
		}

		filename := strings.TrimSpace(meta.Filename)
		if filename == "" {
			filename = filepath.Base(srcPath)
		}
		filename = utils.SanitizeFilename(filename)
		if filename == "" || filename == "." || filename == string(os.PathSeparator) {
			filename = "file"
		}

		dstName := uuid.New().String()[:8] + "_" + filename
		dstPath := filepath.Join(destDir, dstName)

		size, err := copyFile(dstPath, srcPath, 0o600)
		if err != nil {
			logger.WarnCF("agent", "Failed to import inbound media into workspace", map[string]any{
				"src":   srcPath,
				"dst":   dstPath,
				"error": err.Error(),
			})
			skipped++
			continue
		}

		rel, err := filepath.Rel(workspace, dstPath)
		if err != nil || strings.HasPrefix(rel, "..") {
			// Should never happen, but keep a safe fallback.
			skipped++
			_ = os.Remove(dstPath)
			continue
		}

		imported = append(imported, importedInboundMedia{
			RelativePath: filepath.ToSlash(rel),
			Filename:     filename,
			ContentType:  strings.TrimSpace(meta.ContentType),
			SizeBytes:    size,
			SourceRef:    item,
		})
	}

	return imported, skipped
}

func (al *AgentLoop) resolveInboundMedia(item string) (string, MediaMeta, bool) {
	if strings.HasPrefix(item, "media://") && al.mediaResolver != nil {
		localPath, meta, err := al.mediaResolver.ResolveWithMeta(item)
		if err != nil {
			return "", MediaMeta{}, false
		}
		return localPath, meta, true
	}

	// Fallback: some channels may emit raw local paths when MediaStore is not set.
	if filepath.IsAbs(item) {
		return item, MediaMeta{Filename: filepath.Base(item)}, true
	}

	return "", MediaMeta{}, false
}

func formatInboundMediaNote(imported []importedInboundMedia, skipped int) string {
	if len(imported) == 0 && skipped == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("[Uploaded files]\n")
	sb.WriteString("The user uploaded file(s). They have been saved into your workspace so tools can access them.\n")

	for _, f := range imported {
		line := "- " + f.RelativePath
		if f.ContentType != "" {
			line += " (content_type=" + f.ContentType + ")"
		}
		sb.WriteString(line)
		sb.WriteString("\n")
	}

	if skipped > 0 {
		sb.WriteString(fmt.Sprintf("- (skipped %d attachment(s): missing/too large/unavailable)\n", skipped))
	}

	sb.WriteString("Tip: use document_text for PDF/DOCX, read_file for plain text, or exec to inspect/convert (file/head/python etc.).")
	return strings.TrimSpace(sb.String())
}

func sanitizePathSegment(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "unknown"
	}

	// Only keep common safe path characters; replace others with '_'.
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-' || r == '_' || r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}

	out := strings.Trim(b.String(), "_")
	if out == "" {
		out = "unknown"
	}
	if len(out) > 64 {
		out = out[:64]
	}
	return out
}

func copyFile(dstPath, srcPath string, perm os.FileMode) (int64, error) {
	in, err := os.Open(srcPath)
	if err != nil {
		return 0, err
	}
	defer in.Close()

	out, err := os.OpenFile(dstPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return 0, err
	}

	n, copyErr := io.Copy(out, in)
	closeErr := out.Close()
	if copyErr != nil {
		_ = os.Remove(dstPath)
		return n, copyErr
	}
	if closeErr != nil {
		_ = os.Remove(dstPath)
		return n, closeErr
	}
	return n, nil
}
