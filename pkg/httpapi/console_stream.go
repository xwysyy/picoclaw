package httpapi

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

func (h *ConsoleHandler) handleStream(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": "method not allowed"})
		return
	}

	rel := strings.TrimSpace(r.URL.Query().Get("path"))
	if rel == "" {
		h.writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "path is required"})
		return
	}

	tail := 200
	if v := strings.TrimSpace(r.URL.Query().Get("tail")); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			tail = n
		}
	}
	if tail < 0 {
		tail = 0
	}
	if tail > 500 {
		tail = 500
	}

	abs, relClean, err := h.resolveConsolePath(rel)
	if err != nil {
		h.writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}

	f, err := os.Open(abs)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			h.writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "file not found"})
			return
		}
		h.writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	defer f.Close()

	flusher, ok := w.(http.Flusher)
	if !ok {
		h.writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "streaming not supported"})
		return
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	if tail > 0 {
		const maxBytes = int64(1 << 20)
		if st, err := f.Stat(); err == nil && st != nil && st.Size() > 0 {
			start := st.Size() - maxBytes
			if start < 0 {
				start = 0
			}
			if _, err := f.Seek(start, io.SeekStart); err == nil {
				buf, _ := io.ReadAll(f)
				lines := strings.Split(string(buf), "\n")
				if start > 0 && len(lines) > 0 {
					lines = lines[1:]
				}
				trimmed := make([]string, 0, len(lines))
				for _, line := range lines {
					line = strings.TrimSpace(line)
					if line == "" {
						continue
					}
					trimmed = append(trimmed, line)
				}
				if len(trimmed) > tail {
					trimmed = trimmed[len(trimmed)-tail:]
				}
				for _, line := range trimmed {
					_, _ = io.WriteString(w, line+"\n")
				}
				flusher.Flush()
			}
		}
	}

	reader := bufio.NewReader(f)
	lastKeepAlive := time.Now()
	lastStat := time.Now()
	var lastSize int64
	if st, err := f.Stat(); err == nil && st != nil {
		lastSize = st.Size()
	}

	for {
		select {
		case <-r.Context().Done():
			return
		default:
			line, err := reader.ReadString('\n')
			if err == nil {
				line = strings.TrimSpace(line)
				if line != "" {
					_, _ = io.WriteString(w, line+"\n")
					flusher.Flush()
				}
				continue
			}

			if !errors.Is(err, io.EOF) {
				_, _ = io.WriteString(w, fmt.Sprintf("{\"ok\":false,\"error\":%q,\"path\":%q}\n", err.Error(), relClean))
				flusher.Flush()
				return
			}

			if time.Since(lastKeepAlive) > 10*time.Second {
				_, _ = io.WriteString(w, "\n")
				flusher.Flush()
				lastKeepAlive = time.Now()
			}

			if time.Since(lastStat) > 2*time.Second {
				lastStat = time.Now()
				if st, err := os.Stat(abs); err == nil && st != nil {
					if st.Size() < lastSize {
						if _, err := f.Seek(0, io.SeekStart); err == nil {
							reader.Reset(f)
						}
					}
					lastSize = st.Size()
				}
			}

			time.Sleep(150 * time.Millisecond)
		}
	}
}
