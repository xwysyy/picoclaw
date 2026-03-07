package channels

import "testing"

func TestMediaTypeFromMIME(t *testing.T) {
	tests := []struct {
		name string
		mime string
		want string
	}{
		{name: "image", mime: "image/png", want: "image"},
		{name: "audio", mime: "audio/opus", want: "audio"},
		{name: "video", mime: "video/mp4", want: "video"},
		{name: "default file", mime: "application/octet-stream", want: "file"},
		{name: "trim and case normalize", mime: "  VIDEO/MP4 ", want: "video"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := MediaTypeFromMIME(tc.mime); got != tc.want {
				t.Fatalf("MediaTypeFromMIME(%q) = %q, want %q", tc.mime, got, tc.want)
			}
		})
	}
}
