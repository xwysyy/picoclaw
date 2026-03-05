package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/xwysyy/X-Claw/cmd/x-claw/internal"
	"github.com/xwysyy/X-Claw/cmd/x-claw/internal/cliutil"
	cfgpkg "github.com/xwysyy/X-Claw/pkg/config"
)

type validateReport struct {
	OK       bool                       `json:"ok"`
	Path     string                     `json:"path,omitempty"`
	Problems []cfgpkg.ValidationProblem `json:"problems,omitempty"`
	Error    string                     `json:"error,omitempty"`
}

func validateCmd(opts validateOptions) error {
	path := strings.TrimSpace(opts.Path)
	if path == "" {
		path = internal.GetConfigPath()
	}
	path = filepath.Clean(path)

	if _, err := os.Stat(path); err != nil {
		report := validateReport{
			OK:    false,
			Path:  path,
			Error: fmt.Sprintf("config file not found: %v", err),
		}
		_ = writeValidateReport(report, opts.JSON)
		return fmt.Errorf("config validate failed")
	}

	_, problems, err := cfgpkg.ValidateConfigFile(path)
	if err != nil {
		report := validateReport{
			OK:    false,
			Path:  path,
			Error: err.Error(),
		}
		_ = writeValidateReport(report, opts.JSON)
		return fmt.Errorf("config validate failed")
	}

	if len(problems) > 0 {
		report := validateReport{
			OK:       false,
			Path:     path,
			Problems: problems,
		}
		_ = writeValidateReport(report, opts.JSON)
		return fmt.Errorf("config is invalid")
	}

	report := validateReport{
		OK:   true,
		Path: path,
	}
	_ = writeValidateReport(report, opts.JSON)
	return nil
}

func writeValidateReport(report validateReport, jsonOut bool) error {
	if jsonOut {
		data, err := cliutil.MarshalIndentNoEscape(report)
		if err != nil {
			return err
		}
		fmt.Println(string(data))
		return nil
	}

	if report.OK {
		fmt.Printf("%s config is valid: %s\n", internal.Logo, report.Path)
		return nil
	}

	fmt.Printf("%s config is invalid: %s\n", internal.Logo, report.Path)
	if strings.TrimSpace(report.Error) != "" {
		fmt.Printf("Error: %s\n", report.Error)
	}
	if len(report.Problems) > 0 {
		fmt.Println("Problems:")
		for _, p := range report.Problems {
			path := strings.TrimSpace(p.Path)
			msg := strings.TrimSpace(p.Message)
			if path == "" {
				path = "(unknown)"
			}
			if msg == "" {
				msg = "invalid"
			}
			fmt.Printf("  - %s: %s\n", path, msg)
		}
	}
	return nil
}
