package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xwysyy/picoclaw/pkg/bus"
	"github.com/xwysyy/picoclaw/pkg/media"
)

func TestImportInboundMediaToWorkspace_CopiesFileAndBuildsNote(t *testing.T) {
	workspace := t.TempDir()
	srcDir := t.TempDir()

	srcPath := filepath.Join(srcDir, "hello.txt")
	if err := os.WriteFile(srcPath, []byte("hello world\n"), 0o600); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	store := media.NewFileMediaStore()
	ref, err := store.Store(srcPath, media.MediaMeta{
		Filename:    "hello.txt",
		ContentType: "text/plain",
		Source:      "test",
	}, "scope")
	if err != nil {
		t.Fatalf("Store failed: %v", err)
	}

	al := &AgentLoop{mediaResolver: media.AsMediaResolver(store)}
	msg := bus.InboundMessage{
		Channel:   "feishu",
		ChatID:    "oc_test",
		MessageID: "om_test",
		Media:     []string{ref},
	}

	imported, skipped := al.importInboundMediaToWorkspace(workspace, msg)
	if skipped != 0 {
		t.Fatalf("unexpected skipped=%d", skipped)
	}
	if len(imported) != 1 {
		t.Fatalf("expected 1 imported file, got %d", len(imported))
	}

	dstPath := filepath.Join(workspace, filepath.FromSlash(imported[0].RelativePath))
	data, err := os.ReadFile(dstPath)
	if err != nil {
		t.Fatalf("ReadFile(dst) failed: %v", err)
	}
	if string(data) != "hello world\n" {
		t.Fatalf("unexpected dst content: %q", string(data))
	}

	note := formatInboundMediaNote(imported, skipped)
	if !strings.Contains(note, imported[0].RelativePath) {
		t.Fatalf("note should contain relative path, got: %s", note)
	}
	if !strings.Contains(note, "content_type=text/plain") {
		t.Fatalf("note should contain content type, got: %s", note)
	}
}
