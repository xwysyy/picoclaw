package auditlog

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/sipeed/picoclaw/cmd/picoclaw/internal"
	"github.com/sipeed/picoclaw/pkg/auditlog"
)

type verifyOptions struct {
	Path          string
	All           bool
	Key           string
	AllowUnsigned bool
	Quiet         bool
}

func newVerifyCommand() *cobra.Command {
	var opts verifyOptions
	cmd := &cobra.Command{
		Use:   "verify",
		Short: "Verify audit.jsonl HMAC signatures",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			return verifyCmd(opts)
		},
	}

	cmd.Flags().StringVar(&opts.Path, "path", "", "Audit log file path (defaults to <workspace>/.picoclaw/audit/audit.jsonl)")
	cmd.Flags().BoolVar(&opts.All, "all", false, "Verify audit.jsonl and rotated backups in the audit directory")
	cmd.Flags().StringVar(&opts.Key, "key", "", "HMAC key (defaults to config audit_log.hmac_key; prefer env)")
	cmd.Flags().BoolVar(&opts.AllowUnsigned, "allow-unsigned", false, "Allow unsigned lines (for legacy logs)")
	cmd.Flags().BoolVar(&opts.Quiet, "quiet", false, "Only print errors; exit non-zero on failure")

	return cmd
}

type verifyStats struct {
	File          string `json:"file"`
	TotalLines    int    `json:"total_lines"`
	SignedLines   int    `json:"signed_lines"`
	UnsignedLines int    `json:"unsigned_lines"`
	Valid         int    `json:"valid"`
	Invalid       int    `json:"invalid"`
}

func verifyCmd(opts verifyOptions) error {
	cfg, err := internal.LoadConfig()
	if err != nil {
		return fmt.Errorf("error loading config: %w", err)
	}

	workspace := strings.TrimSpace(cfg.WorkspacePath())
	if workspace == "" {
		return fmt.Errorf("workspace is not configured")
	}

	key := strings.TrimSpace(opts.Key)
	if key == "" {
		key = strings.TrimSpace(cfg.AuditLog.HMACKey)
	}
	if key == "" {
		return fmt.Errorf("missing HMAC key (set audit_log.hmac_key or pass --key)")
	}
	keyBytes := []byte(key)

	files := []string{}
	if strings.TrimSpace(opts.Path) != "" {
		files = append(files, strings.TrimSpace(opts.Path))
	} else {
		dir := strings.TrimSpace(cfg.AuditLog.Dir)
		if dir == "" {
			dir = filepath.Join(workspace, ".picoclaw", "audit")
		}

		if opts.All {
			entries, err := os.ReadDir(dir)
			if err != nil {
				return err
			}
			for _, e := range entries {
				if e == nil || e.IsDir() {
					continue
				}
				name := strings.TrimSpace(e.Name())
				if name == "" {
					continue
				}
				if !strings.HasPrefix(name, "audit") || !strings.HasSuffix(name, ".jsonl") {
					continue
				}
				files = append(files, filepath.Join(dir, name))
			}
			sort.Strings(files)
		} else {
			files = append(files, filepath.Join(dir, "audit.jsonl"))
		}
	}

	if len(files) == 0 {
		return fmt.Errorf("no audit log files found")
	}

	stats := make([]verifyStats, 0, len(files))
	for _, path := range files {
		s, err := verifyOneFile(path, keyBytes, opts.AllowUnsigned)
		if err != nil {
			return err
		}
		stats = append(stats, s)
	}

	totalInvalid := 0
	for _, s := range stats {
		totalInvalid += s.Invalid
	}

	if opts.Quiet {
		if totalInvalid > 0 {
			return fmt.Errorf("auditlog verify failed (%d invalid lines)", totalInvalid)
		}
		return nil
	}

	for _, s := range stats {
		fmt.Printf("File: %s\n", s.File)
		fmt.Printf("  lines: total=%d signed=%d unsigned=%d\n", s.TotalLines, s.SignedLines, s.UnsignedLines)
		fmt.Printf("  result: valid=%d invalid=%d\n", s.Valid, s.Invalid)
	}

	if totalInvalid > 0 {
		return fmt.Errorf("auditlog verify failed (%d invalid lines)", totalInvalid)
	}
	return nil
}

func verifyOneFile(path string, key []byte, allowUnsigned bool) (verifyStats, error) {
	st := verifyStats{File: path}

	f, err := os.Open(path)
	if err != nil {
		return st, err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	buf := make([]byte, 0, 64<<10)
	scanner.Buffer(buf, 32<<20)

	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		st.TotalLines++

		var ev auditlog.Event
		if err := json.Unmarshal(line, &ev); err != nil {
			st.Invalid++
			continue
		}

		if strings.TrimSpace(ev.Sig) == "" {
			st.UnsignedLines++
			if allowUnsigned {
				st.Valid++
			} else {
				st.Invalid++
			}
			continue
		}

		st.SignedLines++
		ok, err := auditlog.VerifyHMACSignature(ev, key)
		if err != nil {
			st.Invalid++
			continue
		}
		if ok {
			st.Valid++
		} else {
			st.Invalid++
		}
	}
	if err := scanner.Err(); err != nil {
		return st, err
	}

	return st, nil
}
