package constants

import "testing"

func TestIsInternalChannel(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		channel string
		want    bool
	}{
		{name: "cli", channel: "cli", want: true},
		{name: "system", channel: "system", want: true},

		{name: "empty", channel: "", want: false},
		{name: "unknown", channel: "slack", want: false},
		{name: "case_sensitive", channel: "CLI", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsInternalChannel(tt.channel); got != tt.want {
				t.Fatalf("IsInternalChannel(%q) = %v, want %v", tt.channel, got, tt.want)
			}
		})
	}
}
