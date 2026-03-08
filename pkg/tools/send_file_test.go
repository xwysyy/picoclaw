package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xwysyy/X-Claw/pkg/bus"
)

func TestSendFileTool_PublishesOutboundMedia(t *testing.T) {
	workspace := t.TempDir()
	relPath := filepath.Join("uploads", "report.pdf")
	absPath := filepath.Join(workspace, relPath)
	if err := os.MkdirAll(filepath.Dir(absPath), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(absPath, []byte("%PDF-1.4\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	tool := NewSendFileTool(workspace, true, 1024)
	storeCalls := 0
	tool.SetStoreRefCallback(func(_ context.Context, localPath string, meta bus.MediaPart) (string, error) {
		storeCalls++
		if localPath != absPath {
			t.Fatalf("localPath = %q, want %q", localPath, absPath)
		}
		if meta.Filename != "report.pdf" {
			t.Fatalf("Filename = %q, want %q", meta.Filename, "report.pdf")
		}
		if meta.ContentType != "application/pdf" {
			t.Fatalf("ContentType = %q, want %q", meta.ContentType, "application/pdf")
		}
		return "media://test-ref", nil
	})

	var (
		publishCalls int
		gotMsg       bus.OutboundMediaMessage
	)
	tool.SetPublishCallback(func(_ context.Context, msg bus.OutboundMediaMessage) error {
		publishCalls++
		gotMsg = msg
		return nil
	})

	ctx := withExecutionContext(context.Background(), "telegram", "chat-42", "")
	result := tool.Execute(ctx, map[string]any{"path": relPath})

	if result == nil {
		t.Fatal("expected ToolResult")
	}
	if result.IsError {
		t.Fatalf("expected success, got error: %s", result.ForLLM)
	}
	if !result.Silent {
		t.Fatalf("expected Silent=true, got %+v", result)
	}
	if result.ForLLM != "File sent to telegram:chat-42" {
		t.Fatalf("ForLLM = %q, want %q", result.ForLLM, "File sent to telegram:chat-42")
	}
	if publishCalls != 1 {
		t.Fatalf("publishCalls = %d, want 1", publishCalls)
	}
	if storeCalls != 1 {
		t.Fatalf("storeCalls = %d, want 1", storeCalls)
	}
	if gotMsg.Channel != "telegram" {
		t.Fatalf("Channel = %q, want %q", gotMsg.Channel, "telegram")
	}
	if gotMsg.ChatID != "chat-42" {
		t.Fatalf("ChatID = %q, want %q", gotMsg.ChatID, "chat-42")
	}
	if len(gotMsg.Parts) != 1 {
		t.Fatalf("len(Parts) = %d, want 1", len(gotMsg.Parts))
	}
	part := gotMsg.Parts[0]
	if part.Ref != "media://test-ref" {
		t.Fatalf("Ref = %q, want %q", part.Ref, "media://test-ref")
	}
	if part.Filename != "report.pdf" {
		t.Fatalf("Filename = %q, want %q", part.Filename, "report.pdf")
	}
	if part.ContentType != "application/pdf" {
		t.Fatalf("ContentType = %q, want %q", part.ContentType, "application/pdf")
	}
	if part.Type != "file" {
		t.Fatalf("Type = %q, want %q", part.Type, "file")
	}
}

func TestSendFileTool_RejectsOutsideWorkspaceWhenRestricted(t *testing.T) {
	workspace := t.TempDir()
	outsideDir := t.TempDir()
	outsidePath := filepath.Join(outsideDir, "secret.pdf")
	if err := os.WriteFile(outsidePath, []byte("secret"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	tool := NewSendFileTool(workspace, true, 1024)
	ctx := withExecutionContext(context.Background(), "telegram", "chat-42", "")
	result := tool.Execute(ctx, map[string]any{"path": outsidePath})

	if result == nil {
		t.Fatal("expected ToolResult")
	}
	if !result.IsError {
		t.Fatalf("expected error, got %+v", result)
	}
	if !strings.Contains(result.ForLLM, "outside the workspace") {
		t.Fatalf("ForLLM = %q, want mention outside the workspace", result.ForLLM)
	}
}

func TestSendFileTool_ReturnsErrorWhenMediaStoreNotConfigured(t *testing.T) {
	workspace := t.TempDir()
	relPath := filepath.Join("uploads", "report.pdf")
	absPath := filepath.Join(workspace, relPath)
	if err := os.MkdirAll(filepath.Dir(absPath), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(absPath, []byte("%PDF-1.4\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	tool := NewSendFileTool(workspace, true, 1024)
	tool.SetPublishCallback(func(_ context.Context, _ bus.OutboundMediaMessage) error {
		t.Fatal("publish callback should not be called without media store")
		return nil
	})

	ctx := withExecutionContext(context.Background(), "telegram", "chat-42", "")
	result := tool.Execute(ctx, map[string]any{"path": relPath})

	if result == nil {
		t.Fatal("expected ToolResult")
	}
	if !result.IsError {
		t.Fatalf("expected error, got %+v", result)
	}
	if !strings.Contains(result.ForLLM, "media store") {
		t.Fatalf("ForLLM = %q, want mention media store", result.ForLLM)
	}
}
