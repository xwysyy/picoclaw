package utils

import (
	"os"
	"path/filepath"
)

const (
	// MediaTempDirName is the canonical temp directory name used for downloaded
	// media files. Keep this stable to simplify debugging and operational
	// cleanup.
	MediaTempDirName = "x_claw_media"

	// LegacyMediaTempDirName is the historical temp directory name used by the
	// project before the X-Claw rebrand. We keep it to ease troubleshooting and
	// to allow backward-compatible fallbacks when needed.
	LegacyMediaTempDirName = "picoclaw_media"
)

func MediaTempDir() string {
	return filepath.Join(os.TempDir(), MediaTempDirName)
}

func LegacyMediaTempDir() string {
	return filepath.Join(os.TempDir(), LegacyMediaTempDirName)
}

