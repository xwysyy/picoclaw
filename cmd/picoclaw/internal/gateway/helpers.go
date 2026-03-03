package gateway

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/sipeed/picoclaw/cmd/picoclaw/internal"
	"github.com/sipeed/picoclaw/pkg/agent"
	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/channels"
	_ "github.com/sipeed/picoclaw/pkg/channels/dingtalk"
	_ "github.com/sipeed/picoclaw/pkg/channels/discord"
	_ "github.com/sipeed/picoclaw/pkg/channels/feishu"
	_ "github.com/sipeed/picoclaw/pkg/channels/line"
	_ "github.com/sipeed/picoclaw/pkg/channels/onebot"
	_ "github.com/sipeed/picoclaw/pkg/channels/pico"
	_ "github.com/sipeed/picoclaw/pkg/channels/qq"
	_ "github.com/sipeed/picoclaw/pkg/channels/slack"
	_ "github.com/sipeed/picoclaw/pkg/channels/telegram"
	_ "github.com/sipeed/picoclaw/pkg/channels/wecom"
	_ "github.com/sipeed/picoclaw/pkg/channels/whatsapp"
	_ "github.com/sipeed/picoclaw/pkg/channels/whatsapp_native"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/cron"
	"github.com/sipeed/picoclaw/pkg/health"
	"github.com/sipeed/picoclaw/pkg/heartbeat"
	"github.com/sipeed/picoclaw/pkg/httpapi"
	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/media"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/tools"
)

// gatewayServices groups all long-lived services for clean startup/shutdown.
type gatewayServices struct {
	cfg              *config.Config
	configPath       string
	provider         providers.LLMProvider
	msgBus           *bus.MessageBus
	agentLoop        *agent.AgentLoop
	cronService      *cron.CronService
	heartbeatService *heartbeat.HeartbeatService
	mediaStore       *media.FileMediaStore
	channelManager   *channels.Manager
	healthServer     *health.Server

	reloadMu sync.Mutex
}

func gatewayCmd(debug bool) error {
	if debug {
		logger.SetLevel(logger.DEBUG)
		fmt.Println("🔍 Debug mode enabled")
	}

	svc, err := initGatewayServices(debug)
	if err != nil {
		return err
	}

	return runGateway(svc)
}

// initGatewayServices creates and wires all services needed by the gateway.
func initGatewayServices(debug bool) (*gatewayServices, error) {
	configPath := internal.GetConfigPath()
	cfg, err := internal.LoadConfig()
	if err != nil {
		return nil, fmt.Errorf("error loading config: %w", err)
	}

	provider, modelID, err := providers.CreateProvider(cfg)
	if err != nil {
		return nil, fmt.Errorf("error creating provider: %w", err)
	}
	if modelID != "" {
		cfg.Agents.Defaults.ModelName = modelID
	}

	msgBus := bus.NewMessageBus()
	agentLoop := agent.NewAgentLoop(cfg, msgBus, provider)

	printStartupInfo(agentLoop)

	execTimeout := time.Duration(cfg.Tools.Cron.ExecTimeoutMinutes) * time.Minute
	cronService := setupCronTool(agentLoop, msgBus, cfg.WorkspacePath(), cfg.Agents.Defaults.RestrictToWorkspace, execTimeout, cfg)

	heartbeatService := setupHeartbeat(cfg, msgBus, agentLoop)

	mediaStore := media.NewFileMediaStoreWithCleanup(media.MediaCleanerConfig{
		Enabled:  cfg.Tools.MediaCleanup.Enabled,
		MaxAge:   time.Duration(cfg.Tools.MediaCleanup.MaxAge) * time.Minute,
		Interval: time.Duration(cfg.Tools.MediaCleanup.Interval) * time.Minute,
	})
	mediaStore.Start()

	channelManager, err := channels.NewManager(cfg, msgBus, mediaStore)
	if err != nil {
		mediaStore.Stop()
		return nil, fmt.Errorf("error creating channel manager: %w", err)
	}

	agentLoop.SetChannelManager(channelManager)
	agentLoop.SetMediaStore(mediaStore)

	enabledChannels := channelManager.GetEnabledChannels()
	if len(enabledChannels) > 0 {
		fmt.Printf("✓ Channels enabled: %s\n", enabledChannels)
	} else {
		fmt.Println("⚠ Warning: No channels enabled")
	}

	return &gatewayServices{
		cfg:              cfg,
		configPath:       configPath,
		provider:         provider,
		msgBus:           msgBus,
		agentLoop:        agentLoop,
		cronService:      cronService,
		heartbeatService: heartbeatService,
		mediaStore:       mediaStore,
		channelManager:   channelManager,
		healthServer:     health.NewServer(cfg.Gateway.Host, cfg.Gateway.Port),
	}, nil
}

// runGateway starts all services and blocks until SIGINT.
func runGateway(svc *gatewayServices) error {
	fmt.Printf("✓ Gateway started on %s:%d\n", svc.cfg.Gateway.Host, svc.cfg.Gateway.Port)
	fmt.Println("Press Ctrl+C to stop")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := svc.cronService.Start(); err != nil {
		fmt.Printf("Error starting cron service: %v\n", err)
	}
	fmt.Println("✓ Cron service started")

	if err := svc.heartbeatService.Start(); err != nil {
		fmt.Printf("Error starting heartbeat service: %v\n", err)
	}
	fmt.Println("✓ Heartbeat service started")

	addr := fmt.Sprintf("%s:%d", svc.cfg.Gateway.Host, svc.cfg.Gateway.Port)
	svc.channelManager.SetupHTTPServer(addr, svc.healthServer)
	registerGatewayHTTPAPI(svc)

	if err := svc.channelManager.StartAll(ctx); err != nil {
		fmt.Printf("Error starting channels: %v\n", err)
		return err
	}

	fmt.Printf("✓ Health endpoints available at http://%s:%d/health and /ready\n", svc.cfg.Gateway.Host, svc.cfg.Gateway.Port)

	go svc.agentLoop.Run(ctx)

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)

	// Optional: config hot reload watcher (polling).
	if svc != nil && svc.cfg != nil && svc.cfg.Gateway.Reload.Enabled && svc.cfg.Gateway.Reload.Watch {
		interval := time.Duration(svc.cfg.Gateway.Reload.IntervalSeconds) * time.Second
		if interval <= 0 {
			interval = 2 * time.Second
		}
		go watchConfigFile(ctx, svc.configPath, interval, func() {
			if err := svc.reload(ctx, "watch"); err != nil {
				logger.WarnCF("gateway", "Config hot reload failed (watch)", map[string]any{
					"error": err.Error(),
				})
			}
		})
	}

	for {
		sig := <-sigChan
		if sig == syscall.SIGHUP {
			if svc != nil && svc.cfg != nil && !svc.cfg.Gateway.Reload.Enabled {
				logger.InfoCF("gateway", "Ignoring SIGHUP (gateway.reload.enabled=false)", nil)
				continue
			}
			if err := svc.reload(ctx, "signal"); err != nil {
				logger.WarnCF("gateway", "Config hot reload failed (SIGHUP)", map[string]any{
					"error": err.Error(),
				})
			} else {
				logger.InfoCF("gateway", "Config hot reload applied", map[string]any{
					"source": "SIGHUP",
				})
			}
			continue
		}

		return shutdownGateway(svc, cancel)
	}
}

func watchConfigFile(ctx context.Context, path string, interval time.Duration, onChange func()) {
	path = strings.TrimSpace(path)
	if path == "" || interval <= 0 || onChange == nil {
		return
	}

	lastStamp := ""
	if fi, err := os.Stat(path); err == nil && fi != nil {
		lastStamp = fmt.Sprintf("%d:%d", fi.ModTime().UnixNano(), fi.Size())
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			fi, err := os.Stat(path)
			if err != nil || fi == nil {
				continue
			}
			stamp := fmt.Sprintf("%d:%d", fi.ModTime().UnixNano(), fi.Size())
			if stamp == lastStamp {
				continue
			}
			lastStamp = stamp
			onChange()
		}
	}
}

func (svc *gatewayServices) reload(ctx context.Context, reason string) error {
	if svc == nil {
		return fmt.Errorf("gateway services is nil")
	}

	svc.reloadMu.Lock()
	defer svc.reloadMu.Unlock()

	path := strings.TrimSpace(svc.configPath)
	if path == "" {
		path = internal.GetConfigPath()
	}

	newCfg, err := config.LoadConfig(path)
	if err != nil {
		return fmt.Errorf("reload config: %w", err)
	}

	// Preflight: ensure new config enables at least one channel, otherwise keep the current gateway running.
	preflightCM, err := channels.NewManager(newCfg, svc.msgBus, svc.mediaStore)
	if err != nil {
		return fmt.Errorf("reload: create channel manager: %w", err)
	}
	if len(preflightCM.GetEnabledChannels()) == 0 {
		return fmt.Errorf("reload aborted: no channels enabled in new config")
	}

	// Apply config to agent loop (tools/policy/notify settings).
	svc.cfg = newCfg
	if svc.agentLoop != nil {
		svc.agentLoop.SetConfig(newCfg)
		// Refresh MCP tools in-place.
		svc.agentLoop.ReloadMCPTools(ctx)
	}

	// Restart channel manager + HTTP server (re-registers webhook handlers + /api/* handlers).
	if svc.channelManager != nil {
		stopCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		_ = svc.channelManager.StopAll(stopCtx)
		cancel()
	}

	svc.channelManager = preflightCM
	svc.healthServer = health.NewServer(newCfg.Gateway.Host, newCfg.Gateway.Port)

	if svc.agentLoop != nil {
		svc.agentLoop.SetChannelManager(svc.channelManager)
	}

	addr := fmt.Sprintf("%s:%d", newCfg.Gateway.Host, newCfg.Gateway.Port)
	svc.channelManager.SetupHTTPServer(addr, svc.healthServer)
	registerGatewayHTTPAPI(svc)

	if err := svc.channelManager.StartAll(ctx); err != nil {
		return fmt.Errorf("reload: start channels: %w", err)
	}

	logger.InfoCF("gateway", "Config reloaded", map[string]any{
		"reason":           reason,
		"config_path":      path,
		"enabled_channels": svc.channelManager.GetEnabledChannels(),
		"listen":           addr,
	})

	return nil
}

func registerGatewayHTTPAPI(svc *gatewayServices) {
	if svc == nil || svc.channelManager == nil {
		return
	}

	notify := httpapi.NewNotifyHandler(httpapi.NotifyHandlerOptions{
		Sender: svc.channelManager,
		APIKey: svc.cfg.Gateway.APIKey,
		LastActive: func() (string, string) {
			if svc.agentLoop == nil {
				return "", ""
			}
			return svc.agentLoop.LastActive()
		},
	})

	if err := svc.channelManager.RegisterHTTPHandler("/api/notify", notify); err != nil {
		fmt.Printf("⚠ Warning: failed to register /api/notify: %v\n", err)
	}

	resume := httpapi.NewResumeLastTaskHandler(httpapi.ResumeLastTaskHandlerOptions{
		APIKey:  svc.cfg.Gateway.APIKey,
		Timeout: 2 * time.Minute,
		Resume: func(ctx context.Context) (any, string, error) {
			if svc.agentLoop == nil {
				return nil, "", fmt.Errorf("agent loop not available")
			}
			candidate, response, err := svc.agentLoop.ResumeLastTask(ctx)
			return candidate, response, err
		},
	})
	if err := svc.channelManager.RegisterHTTPHandler("/api/resume_last_task", resume); err != nil {
		fmt.Printf("⚠ Warning: failed to register /api/resume_last_task: %v\n", err)
	}

	estop := httpapi.NewEstopHandler(httpapi.EstopHandlerOptions{
		APIKey:       svc.cfg.Gateway.APIKey,
		Workspace:    svc.cfg.WorkspacePath(),
		Enabled:      svc.cfg.Tools.Estop.Enabled,
		FailClosed:   svc.cfg.Tools.Estop.FailClosed,
		MaxBodyBytes: 8 << 10,
	})
	if err := svc.channelManager.RegisterHTTPHandler("/api/estop", estop); err != nil {
		fmt.Printf("⚠ Warning: failed to register /api/estop: %v\n", err)
	}

	console := httpapi.NewConsoleHandler(httpapi.ConsoleHandlerOptions{
		Workspace: svc.cfg.WorkspacePath(),
		APIKey:    svc.cfg.Gateway.APIKey,
		LastActive: func() (string, string) {
			if svc.agentLoop == nil {
				return "", ""
			}
			return svc.agentLoop.LastActive()
		},
		Info: httpapi.ConsoleInfo{
			Model:                svc.cfg.Agents.Defaults.ModelName,
			NotifyOnTaskComplete: svc.cfg.Notify.OnTaskComplete,
			ToolTraceEnabled:     svc.cfg.Tools.Trace.Enabled,
			RunTraceEnabled:      svc.cfg.Tools.Trace.Enabled,
			WebEvidenceMode:      svc.cfg.Tools.Web.Evidence.Enabled,

			InboundQueueEnabled:        svc.cfg.Gateway.InboundQueue.Enabled,
			InboundQueueMaxConcurrency: svc.cfg.Gateway.InboundQueue.MaxConcurrency,
		},
	})
	if err := svc.channelManager.RegisterHTTPHandler("/console/", console); err != nil {
		fmt.Printf("⚠ Warning: failed to register /console/: %v\n", err)
	}
	if err := svc.channelManager.RegisterHTTPHandler("/api/console/", console); err != nil {
		fmt.Printf("⚠ Warning: failed to register /api/console/: %v\n", err)
	}
}

// shutdownGateway performs graceful shutdown of all services.
func shutdownGateway(svc *gatewayServices, cancel context.CancelFunc) error {
	fmt.Println("\nShutting down...")
	if cp, ok := svc.provider.(providers.StatefulProvider); ok {
		cp.Close()
	}
	cancel()
	svc.msgBus.Close()

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer shutdownCancel()

	svc.channelManager.StopAll(shutdownCtx)
	svc.heartbeatService.Stop()
	svc.cronService.Stop()
	svc.mediaStore.Stop()
	svc.agentLoop.Stop()
	fmt.Println("✓ Gateway stopped")
	return nil
}

func printStartupInfo(agentLoop *agent.AgentLoop) {
	fmt.Println("\n📦 Agent Status:")
	startupInfo := agentLoop.GetStartupInfo()
	toolsInfo := startupInfo["tools"].(map[string]any)
	skillsInfo := startupInfo["skills"].(map[string]any)
	fmt.Printf("  • Tools: %d loaded\n", toolsInfo["count"])
	fmt.Printf("  • Skills: %d/%d available\n", skillsInfo["available"], skillsInfo["total"])

	logger.InfoCF("agent", "Agent initialized", map[string]any{
		"tools_count":      toolsInfo["count"],
		"skills_total":     skillsInfo["total"],
		"skills_available": skillsInfo["available"],
	})
}

func setupHeartbeat(cfg *config.Config, msgBus *bus.MessageBus, agentLoop *agent.AgentLoop) *heartbeat.HeartbeatService {
	svc := heartbeat.NewHeartbeatService(cfg.WorkspacePath(), cfg.Heartbeat.Interval, cfg.Heartbeat.Enabled)
	svc.SetBus(msgBus)
	svc.SetHandler(func(prompt, channel, chatID string) *tools.ToolResult {
		if channel == "" || chatID == "" {
			channel, chatID = "cli", "direct"
		}
		response, err := agentLoop.ProcessHeartbeat(context.Background(), prompt, channel, chatID)
		if err != nil {
			return tools.ErrorResult(fmt.Sprintf("Heartbeat error: %v", err))
		}
		if response == "HEARTBEAT_OK" {
			return tools.SilentResult("Heartbeat OK")
		}
		return tools.SilentResult(response)
	})
	return svc
}

func setupCronTool(
	agentLoop *agent.AgentLoop,
	msgBus *bus.MessageBus,
	workspace string,
	restrict bool,
	execTimeout time.Duration,
	cfg *config.Config,
) *cron.CronService {
	cronStorePath := filepath.Join(workspace, "cron", "jobs.json")
	cronService := cron.NewCronService(cronStorePath, nil)

	cronTool, err := tools.NewCronTool(cronService, agentLoop, msgBus, workspace, restrict, execTimeout, cfg)
	if err != nil {
		log.Fatalf("Critical error during CronTool initialization: %v", err)
	}

	agentLoop.RegisterTool(cronTool)

	cronService.SetOnJob(func(job *cron.CronJob) (string, error) {
		return cronTool.ExecuteJob(context.Background(), job)
	})

	return cronService
}
