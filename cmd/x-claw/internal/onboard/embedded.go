package onboard

import "io/fs"

// EmbeddedWorkspaceFS returns the embedded workspace template filesystem.
//
// It is embedded via `//go:embed workspace` in command.go.
func EmbeddedWorkspaceFS() fs.FS {
	return embeddedFiles
}
