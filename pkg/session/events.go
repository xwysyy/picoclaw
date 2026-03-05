package session

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"

	coresession "github.com/xwysyy/X-Claw/internal/core/session"
	"github.com/xwysyy/X-Claw/pkg/fileutil"
)

// Type aliases keep existing imports stable while moving canonical session domain
// types into internal/core.
type (
	EventType    = coresession.EventType
	SessionEvent = coresession.SessionEvent
	SessionMeta  = coresession.SessionMeta
)

const (
	EventSessionMessage       EventType = coresession.EventSessionMessage
	EventSessionSummary       EventType = coresession.EventSessionSummary
	EventSessionActiveAgent   EventType = coresession.EventSessionActiveAgent
	EventSessionCompactionInc EventType = coresession.EventSessionCompactionInc
	EventSessionMemoryFlush   EventType = coresession.EventSessionMemoryFlush
	EventSessionHistorySet    EventType = coresession.EventSessionHistorySet
	EventSessionHistoryTrunc  EventType = coresession.EventSessionHistoryTrunc
)

func newEventID() string { return uuid.NewString() }

func appendJSONLEvent(path string, event SessionEvent) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("mkdir: %w", err)
	}

	payload, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	payload = append(payload, '\n')

	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("open: %w", err)
	}
	defer f.Close()

	if _, err := f.Write(payload); err != nil {
		return fmt.Errorf("append: %w", err)
	}
	_ = f.Sync()
	return nil
}

func writeMetaFile(path string, meta SessionMeta) error {
	payload, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	return fileutil.WriteFileAtomic(path, payload, 0o644)
}

func readJSONLEvents(path string) ([]SessionEvent, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	events := make([]SessionEvent, 0, 128)
	scanner := bufio.NewScanner(f)
	// Allow larger session events (tool outputs can be large).
	// This is still bounded to avoid unbounded memory on corrupt inputs.
	buf := make([]byte, 0, 64<<10)
	scanner.Buffer(buf, 32<<20)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var ev SessionEvent
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			continue
		}
		events = append(events, ev)
	}
	if err := scanner.Err(); err != nil {
		return events, err
	}
	return events, nil
}
