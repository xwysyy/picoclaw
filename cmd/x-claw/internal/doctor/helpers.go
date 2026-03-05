package doctor

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/xwysyy/X-Claw/cmd/x-claw/internal"
	"github.com/xwysyy/X-Claw/cmd/x-claw/internal/cliutil"
	cfgpkg "github.com/xwysyy/X-Claw/pkg/config"
)

type doctorSeverity string

const (
	severityInfo  doctorSeverity = "info"
	severityWarn  doctorSeverity = "warn"
	severityError doctorSeverity = "error"
)

type doctorCheck struct {
	ID       string         `json:"id"`
	OK       bool           `json:"ok"`
	Severity doctorSeverity `json:"severity"`

	Path    string `json:"path,omitempty"`
	Message string `json:"message"`
	Hint    string `json:"hint,omitempty"`
}

type doctorReport struct {
	Kind    string `json:"kind"`
	OK      bool   `json:"ok"`
	Version string `json:"version"`

	ConfigPath   string `json:"config_path,omitempty"`
	ConfigExists bool   `json:"config_exists,omitempty"`

	Workspace       string `json:"workspace,omitempty"`
	WorkspaceExists bool   `json:"workspace_exists,omitempty"`

	Checks []doctorCheck `json:"checks,omitempty"`
}

type gatewayStatusResponse struct {
	Status string `json:"status"`
}

func doctorCmd(opts doctorOptions) error {
	path := strings.TrimSpace(opts.Path)
	if path == "" {
		path = internal.GetConfigPath()
	}
	path = filepath.Clean(path)

	report := doctorReport{
		Kind:         "x_claw_doctor",
		Version:      internal.FormatVersion(),
		ConfigPath:   path,
		ConfigExists: cliutil.FileExists(path),
	}

	if !report.ConfigExists {
		report.Checks = append(report.Checks, doctorCheck{
			ID:       "config.exists",
			OK:       false,
			Severity: severityWarn,
			Message:  "config file not found; defaults will be used",
			Hint:     "run `x-claw onboard` or set $X_CLAW_CONFIG to a valid config.json",
		})
	} else {
		report.Checks = append(report.Checks, doctorCheck{
			ID:       "config.exists",
			OK:       true,
			Severity: severityInfo,
			Message:  "config file exists",
		})
	}

	cfg, problems, loadErr := cfgpkg.ValidateConfigFile(path)
	if loadErr != nil {
		report.Checks = append(report.Checks, doctorCheck{
			ID:       "config.load",
			OK:       false,
			Severity: severityError,
			Message:  "failed to load config",
			Hint:     loadErr.Error(),
		})
	} else {
		report.Checks = append(report.Checks, doctorCheck{
			ID:       "config.load",
			OK:       true,
			Severity: severityInfo,
			Message:  "config loaded",
		})
	}

	if len(problems) > 0 {
		for _, p := range problems {
			path := strings.TrimSpace(p.Path)
			msg := strings.TrimSpace(p.Message)
			if msg == "" {
				msg = "invalid"
			}
			report.Checks = append(report.Checks, doctorCheck{
				ID:       "config.validate",
				OK:       false,
				Severity: severityError,
				Path:     path,
				Message:  msg,
				Hint:     "run `x-claw config validate` for a focused report",
			})
		}
	} else if loadErr == nil {
		report.Checks = append(report.Checks, doctorCheck{
			ID:       "config.validate",
			OK:       true,
			Severity: severityInfo,
			Message:  "config validation passed",
		})
	}

	workspace := ""
	if cfg != nil {
		workspace = strings.TrimSpace(cfg.WorkspacePath())
	}
	report.Workspace = workspace
	report.WorkspaceExists = workspace != "" && cliutil.DirExists(workspace)

	if workspace == "" {
		report.Checks = append(report.Checks, doctorCheck{
			ID:       "workspace.path",
			OK:       false,
			Severity: severityError,
			Message:  "workspace path is empty",
			Hint:     "set agents.defaults.workspace in config.json",
		})
	} else if report.WorkspaceExists {
		report.Checks = append(report.Checks, doctorCheck{
			ID:       "workspace.exists",
			OK:       true,
			Severity: severityInfo,
			Message:  "workspace directory exists",
		})
	} else {
		report.Checks = append(report.Checks, doctorCheck{
			ID:       "workspace.exists",
			OK:       false,
			Severity: severityWarn,
			Message:  "workspace directory does not exist yet",
			Hint:     "gateway/agent will create it on first run; if it fails, check permissions",
		})
	}

	if cfg != nil && strings.TrimSpace(cfg.Gateway.Host) != "" && cfg.Gateway.Port > 0 {
		host := strings.TrimSpace(cfg.Gateway.Host)
		if host == "0.0.0.0" || host == "::" {
			// 0.0.0.0 is a bind address, not a dial target.
			host = "127.0.0.1"
		}
		baseURL := fmt.Sprintf("http://%s:%d", host, cfg.Gateway.Port)

		if ok, msg := probeGatewayStatus(baseURL + "/healthz"); ok {
			report.Checks = append(report.Checks, doctorCheck{
				ID:       "gateway.healthz",
				OK:       true,
				Severity: severityInfo,
				Message:  "gateway /healthz reachable",
			})
		} else {
			report.Checks = append(report.Checks, doctorCheck{
				ID:       "gateway.healthz",
				OK:       false,
				Severity: severityWarn,
				Message:  "gateway /healthz not reachable",
				Hint:     fmt.Sprintf("%s (%s)", msg, baseURL),
			})
		}

		if ok, msg := probeGatewayStatus(baseURL + "/readyz"); ok {
			report.Checks = append(report.Checks, doctorCheck{
				ID:       "gateway.readyz",
				OK:       true,
				Severity: severityInfo,
				Message:  "gateway /readyz reachable",
			})
		} else {
			report.Checks = append(report.Checks, doctorCheck{
				ID:       "gateway.readyz",
				OK:       false,
				Severity: severityWarn,
				Message:  "gateway /readyz not ready or not reachable",
				Hint:     fmt.Sprintf("%s (%s)", msg, baseURL),
			})
		}
	}

	// Compute overall OK: any severityError makes the report not OK.
	report.OK = true
	for _, c := range report.Checks {
		if c.Severity == severityError {
			report.OK = false
			break
		}
	}

	if opts.JSON {
		data, err := cliutil.MarshalIndentNoEscape(report)
		if err != nil {
			return err
		}
		fmt.Println(string(data))
	} else {
		printDoctorReport(report)
	}

	if report.OK {
		return nil
	}
	return fmt.Errorf("doctor found problems")
}

func printDoctorReport(report doctorReport) {
	fmt.Println("Doctor Report")
	fmt.Printf("Version: %s\n", report.Version)
	fmt.Printf("Config: %s (exists=%v)\n", report.ConfigPath, report.ConfigExists)
	if strings.TrimSpace(report.Workspace) != "" {
		fmt.Printf("Workspace: %s (exists=%v)\n", report.Workspace, report.WorkspaceExists)
	}
	fmt.Println()

	if len(report.Checks) == 0 {
		fmt.Println("No checks were run.")
		return
	}

	fmt.Println("Checks:")
	for _, c := range report.Checks {
		status := "OK"
		if !c.OK {
			status = strings.ToUpper(string(c.Severity))
		}
		line := fmt.Sprintf("  - [%s] %s", status, c.Message)
		if strings.TrimSpace(c.Path) != "" {
			line += fmt.Sprintf(" (%s)", strings.TrimSpace(c.Path))
		}
		fmt.Println(line)
		if strings.TrimSpace(c.Hint) != "" && (!c.OK || c.Severity != severityInfo) {
			fmt.Printf("    hint: %s\n", strings.TrimSpace(c.Hint))
		}
	}
}

func probeGatewayStatus(url string) (bool, string) {
	url = strings.TrimSpace(url)
	if url == "" {
		return false, "url is empty"
	}

	client := &http.Client{Timeout: 1200 * time.Millisecond}
	resp, err := client.Get(url)
	if err != nil {
		return false, err.Error()
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if resp.StatusCode != http.StatusOK {
		return false, fmt.Sprintf("http %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	// Best-effort JSON parse for status.
	var s gatewayStatusResponse
	if err := json.Unmarshal(body, &s); err == nil {
		if strings.TrimSpace(s.Status) == "" {
			return true, "ok"
		}
		// /healthz => "ok", /readyz => "ready". We treat either as success
		// if HTTP is 200, but keep a hint in the message for debugging.
		return true, strings.TrimSpace(s.Status)
	}

	return true, "ok"
}
