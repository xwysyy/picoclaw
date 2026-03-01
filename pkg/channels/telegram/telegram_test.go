package telegram

import (
	"strings"
	"testing"
)

func TestParseChatID(t *testing.T) {
	t.Parallel()

	id, err := parseChatID("123")
	if err != nil {
		t.Fatalf("parseChatID error: %v", err)
	}
	if id != 123 {
		t.Fatalf("parseChatID = %d, want %d", id, 123)
	}

	if _, err := parseChatID("not-a-number"); err == nil {
		t.Fatalf("expected error for invalid chat ID")
	}
}

func TestEscapeHTML(t *testing.T) {
	t.Parallel()

	if got := escapeHTML("<&>"); got != "&lt;&amp;&gt;" {
		t.Fatalf("escapeHTML = %q, want %q", got, "&lt;&amp;&gt;")
	}
}

func TestExtractCodeBlocks(t *testing.T) {
	t.Parallel()

	in := "a\n```go\nx < y\n```\nend"
	m := extractCodeBlocks(in)

	if len(m.codes) != 1 {
		t.Fatalf("codes = %d, want %d", len(m.codes), 1)
	}
	if !strings.Contains(m.codes[0], "x < y") {
		t.Fatalf("code block content = %q, want to contain %q", m.codes[0], "x < y")
	}
	if !strings.Contains(m.text, "\x00CB0\x00") {
		t.Fatalf("placeholder missing from text: %q", m.text)
	}
}

func TestExtractInlineCodes(t *testing.T) {
	t.Parallel()

	in := "a `x < y` b"
	m := extractInlineCodes(in)
	if len(m.codes) != 1 {
		t.Fatalf("codes = %d, want %d", len(m.codes), 1)
	}
	if m.codes[0] != "x < y" {
		t.Fatalf("inline code = %q, want %q", m.codes[0], "x < y")
	}
	if !strings.Contains(m.text, "\x00IC0\x00") {
		t.Fatalf("placeholder missing from text: %q", m.text)
	}
}

func TestMarkdownToTelegramHTML_Basic(t *testing.T) {
	t.Parallel()

	in := strings.Join([]string{
		"# Title",
		"> quote",
		"- item",
		"**bold** __bold2__ _italic_ ~~strike~~",
		"[link](https://example.test)",
		"`a<b`",
		"```go",
		"**notbold**",
		"x < y",
		"```",
	}, "\n")

	out := markdownToTelegramHTML(in)

	// Headings and blockquotes are stripped.
	if strings.Contains(out, "# Title") || strings.Contains(out, "&gt; quote") || strings.Contains(out, "\n- item") {
		t.Fatalf("expected heading/blockquote/list markers to be removed; out=%q", out)
	}

	// List items become bullet points.
	if !strings.Contains(out, "• item") {
		t.Fatalf("expected list item bullet; out=%q", out)
	}

	// Formatting tags.
	for _, want := range []string{"<b>bold</b>", "<b>bold2</b>", "<i>italic</i>", "<s>strike</s>"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in out=%q", want, out)
		}
	}

	// Links.
	if !strings.Contains(out, `<a href="https://example.test">link</a>`) {
		t.Fatalf("missing link anchor in out=%q", out)
	}

	// Inline code should be wrapped and escaped.
	if !strings.Contains(out, "<code>a&lt;b</code>") {
		t.Fatalf("missing escaped inline code in out=%q", out)
	}

	// Code blocks should be wrapped, escaped, and not interpreted as markdown.
	if !strings.Contains(out, "<pre><code>") || !strings.Contains(out, "x &lt; y") {
		t.Fatalf("missing code block wrapper/escape in out=%q", out)
	}
	if strings.Contains(out, "<b>notbold</b>") {
		t.Fatalf("code block markdown should not be rendered; out=%q", out)
	}
}
