package status

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/xwysyy/picoclaw/cmd/x-claw/internal"
	"github.com/xwysyy/picoclaw/pkg/auth"
	"github.com/xwysyy/picoclaw/cmd/x-claw/internal/cliutil"
	"github.com/xwysyy/picoclaw/pkg/cron"
	"github.com/xwysyy/picoclaw/pkg/session"
	"github.com/xwysyy/picoclaw/pkg/state"
)

type statusReport struct {
	Kind string `json:"kind"`

	Version string `json:"version"`
	Build   string `json:"build,omitempty"`

	Config struct {
		Path   string `json:"path"`
		Exists bool   `json:"exists"`
	} `json:"config"`

	Workspace struct {
		Path   string `json:"path"`
		Exists bool   `json:"exists"`
	} `json:"workspace"`

	Gateway struct {
		Host      string `json:"host,omitempty"`
		Port      int    `json:"port,omitempty"`
		APIKeySet bool   `json:"api_key_set,omitempty"`
	} `json:"gateway,omitempty"`

	LastActive struct {
		Raw       string `json:"raw,omitempty"`
		Channel   string `json:"channel,omitempty"`
		ChatID    string `json:"chat_id,omitempty"`
		Timestamp string `json:"timestamp,omitempty"`
	} `json:"last_active,omitempty"`

	Tools struct {
		Trace struct {
			Enabled       bool   `json:"enabled"`
			Dir           string `json:"dir,omitempty"`
			PerCallFiles  bool   `json:"write_per_call_files,omitempty"`
			EventsBaseDir string `json:"events_base_dir,omitempty"`
		} `json:"trace"`
		ErrorTemplate struct {
			Enabled       bool `json:"enabled"`
			IncludeSchema bool `json:"include_schema"`
		} `json:"error_template"`
	} `json:"tools,omitempty"`

	Cron struct {
		StorePath   string `json:"store_path,omitempty"`
		StoreExists bool   `json:"store_exists,omitempty"`
		TotalJobs   int    `json:"total_jobs,omitempty"`
		EnabledJobs int    `json:"enabled_jobs,omitempty"`
		RunningJobs int    `json:"running_jobs,omitempty"`
	} `json:"cron,omitempty"`

	Sessions struct {
		Dir              string `json:"dir,omitempty"`
		Count            int    `json:"count,omitempty"`
		MostRecentKey    string `json:"most_recent_key,omitempty"`
		MostRecentUpdate string `json:"most_recent_update,omitempty"`
	} `json:"sessions,omitempty"`

	Model string `json:"model,omitempty"`

	Providers []statusProvider `json:"providers,omitempty"`

	OAuth []statusOAuth `json:"oauth,omitempty"`
}

type statusProvider struct {
	Name   string `json:"name"`
	Status string `json:"status"` // "set", "not_set", or a URL
}

type statusOAuth struct {
	Provider   string `json:"provider"`
	AuthMethod string `json:"auth_method,omitempty"`
	Status     string `json:"status"` // authenticated / expired / needs_refresh
}

func statusCmd(opts statusOptions) error {
	cfg, err := internal.LoadConfig()
	if err != nil {
		return fmt.Errorf("error loading config: %w", err)
	}

	configPath := internal.GetConfigPath()
	workspace := strings.TrimSpace(cfg.WorkspacePath())

	report := statusReport{
		Kind:    "x_claw_status",
		Version: internal.FormatVersion(),
	}
	build, _ := internal.FormatBuildInfo()
	report.Build = build

	report.Config.Path = configPath
	report.Config.Exists = cliutil.FileExists(configPath)

	report.Workspace.Path = workspace
	report.Workspace.Exists = workspace != "" && cliutil.DirExists(workspace)

	// gateway
	report.Gateway.Host = strings.TrimSpace(cfg.Gateway.Host)
	report.Gateway.Port = cfg.Gateway.Port
	report.Gateway.APIKeySet = cfg.Gateway.APIKey.Present()

	// last_active (best-effort)
	if report.Workspace.Exists {
		raw, channel, chatID, ts := readLastActive(workspace)
		report.LastActive.Raw = raw
		report.LastActive.Channel = channel
		report.LastActive.ChatID = chatID
		if !ts.IsZero() {
			report.LastActive.Timestamp = ts.UTC().Format(time.RFC3339)
		}
	}

	// tools
	report.Tools.Trace.Enabled = cfg.Tools.Trace.Enabled
	report.Tools.Trace.Dir = strings.TrimSpace(cfg.Tools.Trace.Dir)
	report.Tools.Trace.PerCallFiles = cfg.Tools.Trace.WritePerCallFiles
	if report.Workspace.Exists {
		report.Tools.Trace.EventsBaseDir = filepath.Join(workspace, ".x-claw", "audit", "tools")
	}
	report.Tools.ErrorTemplate.Enabled = cfg.Tools.ErrorTemplate.Enabled
	report.Tools.ErrorTemplate.IncludeSchema = cfg.Tools.ErrorTemplate.IncludeSchema

	// cron store stats
	if report.Workspace.Exists {
		storePath := filepath.Join(workspace, "cron", "jobs.json")
		report.Cron.StorePath = storePath
		report.Cron.StoreExists = cliutil.FileExists(storePath)
		cs := cron.NewCronService(storePath, nil)
		jobs := cs.ListJobs(true)
		report.Cron.TotalJobs = len(jobs)
		for _, j := range jobs {
			if j.Enabled {
				report.Cron.EnabledJobs++
			}
			if j.State.Running {
				report.Cron.RunningJobs++
			}
		}
	}

	// sessions
	if report.Workspace.Exists {
		sessionsDir := filepath.Join(workspace, "sessions")
		report.Sessions.Dir = sessionsDir
		if cliutil.DirExists(sessionsDir) {
			sm := session.NewSessionManager(sessionsDir)
			snaps := sm.ListSessionSnapshots()
			report.Sessions.Count = len(snaps)
			if len(snaps) > 0 {
				report.Sessions.MostRecentKey = snaps[0].Key
				report.Sessions.MostRecentUpdate = snaps[0].Updated.UTC().Format(time.RFC3339)
			}
		}
	}

	report.Model = cfg.Agents.Defaults.GetModelName()

	report.Providers = append(report.Providers,
		statusProvider{Name: "OpenRouter API", Status: boolStatus(cfg.Providers.OpenRouter.APIKey.Present())},
		statusProvider{Name: "Anthropic API", Status: boolStatus(cfg.Providers.Anthropic.APIKey.Present())},
		statusProvider{Name: "OpenAI API", Status: boolStatus(cfg.Providers.OpenAI.APIKey.Present())},
		statusProvider{Name: "Gemini API", Status: boolStatus(cfg.Providers.Gemini.APIKey.Present())},
		statusProvider{Name: "Zhipu API", Status: boolStatus(cfg.Providers.Zhipu.APIKey.Present())},
		statusProvider{Name: "Qwen API", Status: boolStatus(cfg.Providers.Qwen.APIKey.Present())},
		statusProvider{Name: "Groq API", Status: boolStatus(cfg.Providers.Groq.APIKey.Present())},
		statusProvider{Name: "Moonshot API", Status: boolStatus(cfg.Providers.Moonshot.APIKey.Present())},
		statusProvider{Name: "DeepSeek API", Status: boolStatus(cfg.Providers.DeepSeek.APIKey.Present())},
		statusProvider{Name: "VolcEngine API", Status: boolStatus(cfg.Providers.VolcEngine.APIKey.Present())},
		statusProvider{Name: "Nvidia API", Status: boolStatus(cfg.Providers.Nvidia.APIKey.Present())},
	)

	report.Providers = append(report.Providers,
		statusProvider{Name: "vLLM/Local", Status: urlOrNotSet(cfg.Providers.VLLM.APIBase)},
		statusProvider{Name: "Ollama", Status: urlOrNotSet(cfg.Providers.Ollama.APIBase)},
	)

	store, _ := auth.LoadStore()
	if store != nil && len(store.Credentials) > 0 {
		providers := make([]string, 0, len(store.Credentials))
		for p := range store.Credentials {
			providers = append(providers, p)
		}
		sort.Strings(providers)

		for _, provider := range providers {
			cred := store.Credentials[provider]
			s := "authenticated"
			if cred.IsExpired() {
				s = "expired"
			} else if cred.NeedsRefresh() {
				s = "needs refresh"
			}
			report.OAuth = append(report.OAuth, statusOAuth{
				Provider:   provider,
				AuthMethod: cred.AuthMethod,
				Status:     s,
			})
		}
	}

	if opts.JSON {
		data, err := cliutil.MarshalIndentNoEscape(report)
		if err != nil {
			return err
		}
		fmt.Println(string(data))
		return nil
	}

	// Human output
	fmt.Printf("%s X-Claw Status\n", internal.Logo)
	fmt.Printf("Version: %s\n", report.Version)
	if report.Build != "" {
		fmt.Printf("Build: %s\n", report.Build)
	}
	fmt.Println()

	fmt.Printf("Config: %s %s\n", report.Config.Path, yesNo(report.Config.Exists))
	fmt.Printf("Workspace: %s %s\n", report.Workspace.Path, yesNo(report.Workspace.Exists))

	if report.Gateway.Host != "" || report.Gateway.Port != 0 {
		apiKeyStatus := "not set"
		if report.Gateway.APIKeySet {
			apiKeyStatus = "set"
		}
		fmt.Printf("Gateway: %s:%d (api_key: %s)\n", report.Gateway.Host, report.Gateway.Port, apiKeyStatus)
	}

	if report.LastActive.Raw != "" {
		if report.LastActive.Timestamp != "" {
			fmt.Printf("Last active: %s (%s)\n", report.LastActive.Raw, report.LastActive.Timestamp)
		} else {
			fmt.Printf("Last active: %s\n", report.LastActive.Raw)
		}
	}

	fmt.Println("\nTools:")
	fmt.Printf("  Trace: %s\n", enabledDisabled(report.Tools.Trace.Enabled))
	if report.Tools.Trace.Dir != "" {
		fmt.Printf("    Dir: %s\n", report.Tools.Trace.Dir)
	} else if report.Tools.Trace.EventsBaseDir != "" {
		fmt.Printf("    Base: %s\n", report.Tools.Trace.EventsBaseDir)
	}
	if report.Tools.Trace.Enabled {
		fmt.Printf("    Per-call files: %s\n", yesNo(report.Tools.Trace.PerCallFiles))
	}
	fmt.Printf("  Error template: %s (include_schema=%t)\n", enabledDisabled(report.Tools.ErrorTemplate.Enabled), report.Tools.ErrorTemplate.IncludeSchema)

	if report.Workspace.Exists {
		fmt.Println("\nCron:")
		fmt.Printf("  Store: %s %s\n", report.Cron.StorePath, yesNo(report.Cron.StoreExists))
		fmt.Printf("  Jobs: total=%d enabled=%d running=%d\n", report.Cron.TotalJobs, report.Cron.EnabledJobs, report.Cron.RunningJobs)
	}

	if report.Sessions.Dir != "" {
		fmt.Println("\nSessions:")
		fmt.Printf("  Dir: %s\n", report.Sessions.Dir)
		fmt.Printf("  Count: %d\n", report.Sessions.Count)
		if report.Sessions.MostRecentKey != "" {
			fmt.Printf("  Most recent: %s (%s)\n", report.Sessions.MostRecentKey, report.Sessions.MostRecentUpdate)
		}
	}

	if report.Model != "" {
		fmt.Printf("\nModel: %s\n", report.Model)
	}
	if len(report.Providers) > 0 {
		fmt.Println("\nProviders:")
		for _, p := range report.Providers {
			fmt.Printf("  %s: %s\n", p.Name, p.Status)
		}
	}
	if len(report.OAuth) > 0 {
		fmt.Println("\nOAuth/Token Auth:")
		for _, o := range report.OAuth {
			fmt.Printf("  %s (%s): %s\n", o.Provider, o.AuthMethod, o.Status)
		}
	}

	return nil
}

func yesNo(ok bool) string {
	if ok {
		return "✓"
	}
	return "✗"
}

func enabledDisabled(ok bool) string {
	if ok {
		return "enabled"
	}
	return "disabled"
}

func boolStatus(set bool) string {
	if set {
		return "set"
	}
	return "not_set"
}

func urlOrNotSet(url string) string {
	url = strings.TrimSpace(url)
	if url == "" {
		return "not_set"
	}
	return url
}

func readLastActive(workspace string) (raw string, channel string, chatID string, ts time.Time) {
	sm := state.NewManager(workspace)
	raw = strings.TrimSpace(sm.GetLastChannel())
	ts = sm.GetTimestamp()
	if raw == "" {
		return "", "", "", ts
	}
	parts := strings.SplitN(raw, ":", 2)
	if len(parts) != 2 {
		return raw, "", "", ts
	}
	channel = strings.TrimSpace(parts[0])
	chatID = strings.TrimSpace(parts[1])
	return raw, channel, chatID, ts
}
