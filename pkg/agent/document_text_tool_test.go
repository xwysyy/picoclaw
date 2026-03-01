package agent

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/tools"
)

func writeDocx(t *testing.T, path string, documentXML string) {
	t.Helper()

	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}
	defer f.Close()

	zw := zip.NewWriter(f)
	defer zw.Close()

	w, err := zw.Create("word/document.xml")
	if err != nil {
		t.Fatalf("zip create failed: %v", err)
	}
	if _, err := w.Write([]byte(documentXML)); err != nil {
		t.Fatalf("zip write failed: %v", err)
	}
}

func buildSimplePDF(text string) []byte {
	// Keep this generator dependency-free. It produces a minimal one-page PDF with
	// an uncompressed content stream containing a single text draw command.
	esc := strings.NewReplacer("\\", "\\\\", "(", "\\(", ")", "\\)").Replace(text)

	var buf bytes.Buffer
	buf.WriteString("%PDF-1.4\n")

	// Object offsets are byte offsets from the start of the file.
	offsets := make([]int, 6) // objects: 0..5 (0 is the xref free object)
	writeObj := func(n int, body string) {
		offsets[n] = buf.Len()
		fmt.Fprintf(&buf, "%d 0 obj\n%s\nendobj\n", n, body)
	}

	// A simple content stream drawing one string at a fixed position.
	// The trailing newline is intentional and counted in /Length.
	content := fmt.Sprintf("BT\n/F1 24 Tf\n72 120 Td\n(%s) Tj\nET\n", esc)

	writeObj(1, "<< /Type /Catalog /Pages 2 0 R >>")
	writeObj(2, "<< /Type /Pages /Kids [3 0 R] /Count 1 >>")
	writeObj(3, "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 200 200] /Contents 4 0 R /Resources << /Font << /F1 5 0 R >> >> >>")
	writeObj(4, fmt.Sprintf("<< /Length %d >>\nstream\n%sendstream", len([]byte(content)), content))
	writeObj(5, "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>")

	xrefPos := buf.Len()
	buf.WriteString("xref\n0 6\n")
	buf.WriteString("0000000000 65535 f \n")
	for i := 1; i <= 5; i++ {
		fmt.Fprintf(&buf, "%010d 00000 n \n", offsets[i])
	}
	buf.WriteString("trailer\n<< /Size 6 /Root 1 0 R >>\n")
	buf.WriteString("startxref\n")
	fmt.Fprintf(&buf, "%d\n", xrefPos)
	buf.WriteString("%%EOF\n")

	return buf.Bytes()
}

func TestDocumentTextTool_DOCX_Generated(t *testing.T) {
	workspace := t.TempDir()

	docxRel := filepath.Join("uploads", "test.docx")
	docxAbs := filepath.Join(workspace, docxRel)
	if err := os.MkdirAll(filepath.Dir(docxAbs), 0o755); err != nil {
		t.Fatalf("MkdirAll failed: %v", err)
	}

	writeDocx(t, docxAbs, `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main">
  <w:body>
    <w:p><w:r><w:t>Hello</w:t></w:r></w:p>
    <w:p><w:r><w:t>World</w:t></w:r></w:p>
  </w:body>
</w:document>`)

	tool := tools.NewDocumentTextTool(workspace, true)
	res := tool.Execute(context.Background(), map[string]any{
		"path":      docxRel,
		"max_chars": 2000,
	})
	if res == nil {
		t.Fatalf("expected result, got nil")
	}
	if res.IsError {
		t.Fatalf("expected no error, got: %s", res.ForLLM)
	}
	if !strings.Contains(res.ForLLM, "Hello") || !strings.Contains(res.ForLLM, "World") {
		t.Fatalf("expected extracted text in result, got: %s", res.ForLLM)
	}
}

func TestDocumentTextTool_DOCX_TruncationAndMinMaxChars(t *testing.T) {
	workspace := t.TempDir()

	docxRel := filepath.Join("uploads", "long.docx")
	docxAbs := filepath.Join(workspace, docxRel)
	if err := os.MkdirAll(filepath.Dir(docxAbs), 0o755); err != nil {
		t.Fatalf("MkdirAll failed: %v", err)
	}

	longText := strings.Repeat("a", 400)
	writeDocx(t, docxAbs, `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main">
  <w:body>
    <w:p><w:r><w:t>`+longText+`</w:t></w:r></w:p>
  </w:body>
</w:document>`)

	tool := tools.NewDocumentTextTool(workspace, true)
	res := tool.Execute(context.Background(), map[string]any{
		"path":      docxRel,
		"max_chars": 200,
	})
	if res == nil {
		t.Fatalf("expected result, got nil")
	}
	if res.IsError {
		t.Fatalf("expected no error, got: %s", res.ForLLM)
	}
	if !strings.Contains(res.ForLLM, "truncated=true") {
		t.Fatalf("expected truncated=true, got: %s", res.ForLLM)
	}

	parts := strings.SplitN(res.ForLLM, "\n\n", 2)
	if len(parts) != 2 {
		t.Fatalf("expected header and body separated by blank line, got: %s", res.ForLLM)
	}
	bodyRunes := len([]rune(strings.TrimSpace(parts[1])))
	if bodyRunes > 200 {
		t.Fatalf("expected extracted body to be <= 200 runes, got %d", bodyRunes)
	}

	// max_chars must be >= 200.
	bad := tool.Execute(context.Background(), map[string]any{
		"path":      docxRel,
		"max_chars": 199,
	})
	if bad == nil {
		t.Fatalf("expected result, got nil")
	}
	if !bad.IsError {
		t.Fatalf("expected error for max_chars < 200, got: %s", bad.ForLLM)
	}
}

func TestDocumentTextTool_PDF_Generated(t *testing.T) {
	workspace := t.TempDir()

	pdfRel := filepath.Join("uploads", "test.pdf")
	pdfAbs := filepath.Join(workspace, pdfRel)
	if err := os.MkdirAll(filepath.Dir(pdfAbs), 0o755); err != nil {
		t.Fatalf("MkdirAll failed: %v", err)
	}
	if err := os.WriteFile(pdfAbs, buildSimplePDF("Hello PDF"), 0o600); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	tool := tools.NewDocumentTextTool(workspace, true)
	res := tool.Execute(context.Background(), map[string]any{
		"path":      pdfRel,
		"max_chars": 12000,
	})
	if res == nil {
		t.Fatalf("expected result, got nil")
	}
	if res.IsError {
		t.Fatalf("expected no error, got: %s", res.ForLLM)
	}
	if !strings.Contains(res.ForLLM, "Hello PDF") {
		t.Fatalf("expected extracted text to contain %q, got: %s", "Hello PDF", res.ForLLM)
	}
}

func TestDocumentTextTool_PDF_InvalidPDFErrors(t *testing.T) {
	workspace := t.TempDir()

	pdfRel := filepath.Join("uploads", "invalid.pdf")
	pdfAbs := filepath.Join(workspace, pdfRel)
	if err := os.MkdirAll(filepath.Dir(pdfAbs), 0o755); err != nil {
		t.Fatalf("MkdirAll failed: %v", err)
	}
	if err := os.WriteFile(pdfAbs, []byte("not a pdf"), 0o600); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	tool := tools.NewDocumentTextTool(workspace, true)
	res := tool.Execute(context.Background(), map[string]any{
		"path":      pdfRel,
		"max_chars": 2000,
	})
	if res == nil {
		t.Fatalf("expected result, got nil")
	}
	if !res.IsError {
		t.Fatalf("expected error for invalid PDF, got: %s", res.ForLLM)
	}
	if !strings.Contains(strings.ToLower(res.ForLLM), "pdf") {
		t.Fatalf("expected PDF-related error message, got: %s", res.ForLLM)
	}
}

func TestDocumentTextTool_UnsupportedDoc(t *testing.T) {
	workspace := t.TempDir()

	docRel := filepath.Join("uploads", "fixture.doc")
	docAbs := filepath.Join(workspace, docRel)
	if err := os.MkdirAll(filepath.Dir(docAbs), 0o755); err != nil {
		t.Fatalf("MkdirAll failed: %v", err)
	}
	if err := os.WriteFile(docAbs, []byte("not a real doc"), 0o600); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	tool := tools.NewDocumentTextTool(workspace, true)
	res := tool.Execute(context.Background(), map[string]any{
		"path":      docRel,
		"max_chars": 2000,
	})
	if res == nil {
		t.Fatalf("expected result, got nil")
	}
	if !res.IsError {
		t.Fatalf("expected error for unsupported .doc, got: %s", res.ForLLM)
	}
	if !strings.Contains(strings.ToLower(res.ForLLM), "unsupported") {
		t.Fatalf("expected unsupported error message, got: %s", res.ForLLM)
	}
}
