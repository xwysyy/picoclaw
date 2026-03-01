package utils

import "testing"

func TestValidateSkillIdentifier(t *testing.T) {
	tests := []struct {
		name       string
		identifier string
		wantErr    bool
	}{
		{name: "empty", identifier: "", wantErr: true},
		{name: "spaces only", identifier: "   ", wantErr: true},
		{name: "ok", identifier: "hardware", wantErr: false},
		{name: "ok trims spaces", identifier: "  registry-skill  ", wantErr: false},
		{name: "reject forward slash", identifier: "a/b", wantErr: true},
		{name: "reject backslash", identifier: "a\\b", wantErr: true},
		{name: "reject traversal", identifier: "../x", wantErr: true},
		{name: "reject contains ..", identifier: "a..b", wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateSkillIdentifier(tc.identifier)
			if tc.wantErr && err == nil {
				t.Fatalf("expected error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("expected nil error, got %v", err)
			}
		})
	}
}

