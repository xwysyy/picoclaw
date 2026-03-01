//go:build amd64 || arm64 || riscv64 || mips64 || ppc64

package feishu

import (
	"reflect"
	"testing"
)

func TestNormalizeFeishuText_HTMLAndListQuirks(t *testing.T) {
	in := "<p>-\n1</p><p>2.\nsecond</p><p>a&nbsp;&lt;b&gt;</p>"
	got := normalizeFeishuText(in)
	want := "- 1\n2. second\na <b>"
	if got != want {
		t.Fatalf("normalizeFeishuText() = %q, want %q", got, want)
	}
}

func TestExtractFeishuPostImageKeys(t *testing.T) {
	raw := `{
		"title":"x",
		"zh_cn": {
			"content": [
				[
					{"tag":"img","image_key":"img_a"},
					{"tag":"text","text":"hello"},
					{"nested":{"image_key":"img_b"}}
				],
				[
					{"tag":"img","image_key":"img_a"}
				]
			]
		}
	}`

	got := extractFeishuPostImageKeys(raw)
	want := []string{"img_a", "img_b"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("extractFeishuPostImageKeys() = %#v, want %#v", got, want)
	}
}

func TestResolveFeishuFileUploadTypes(t *testing.T) {
	tests := []struct {
		name        string
		mediaType   string
		filename    string
		contentType string
		wantFile    string
		wantMsg     string
	}{
		{
			name:        "video mp4",
			mediaType:   "video",
			filename:    "clip.mp4",
			contentType: "video/mp4",
			wantFile:    "mp4",
			wantMsg:     "media",
		},
		{
			name:        "audio opus",
			mediaType:   "audio",
			filename:    "voice.opus",
			contentType: "audio/opus",
			wantFile:    "opus",
			wantMsg:     "audio",
		},
		{
			name:        "audio non opus fallback to file",
			mediaType:   "audio",
			filename:    "voice.m4a",
			contentType: "audio/mp4",
			wantFile:    "m4a",
			wantMsg:     "file",
		},
		{
			name:        "infer mp4 from content type",
			mediaType:   "video",
			filename:    "clip",
			contentType: "video/mp4",
			wantFile:    "mp4",
			wantMsg:     "media",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gotFile, gotMsg := resolveFeishuFileUploadTypes(tc.mediaType, tc.filename, tc.contentType)
			if gotFile != tc.wantFile || gotMsg != tc.wantMsg {
				t.Fatalf("resolveFeishuFileUploadTypes() = (%q, %q), want (%q, %q)", gotFile, gotMsg, tc.wantFile, tc.wantMsg)
			}
		})
	}
}
