package gateway

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/xwysyy/X-Claw/cmd/x-claw/internal"
	"github.com/xwysyy/X-Claw/pkg/agent"
	"github.com/xwysyy/X-Claw/pkg/bus"
	"github.com/xwysyy/X-Claw/pkg/channels"
	_ "github.com/xwysyy/X-Claw/pkg/channels/feishu"
	_ "github.com/xwysyy/X-Claw/pkg/channels/telegram"
	"github.com/xwysyy/X-Claw/pkg/config"
	"github.com/xwysyy/X-Claw/pkg/cron"
	"github.com/xwysyy/X-Claw/pkg/health"
	"github.com/xwysyy/X-Claw/pkg/heartbeat"
	"github.com/xwysyy/X-Claw/pkg/logger"
	"github.com/xwysyy/X-Claw/pkg/media"
	"github.com/xwysyy/X-Claw/pkg/providers"
	"github.com/xwysyy/X-Claw/pkg/tools"
	"github.com/xwysyy/X-Claw/pkg/voice"
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
	agentLoop.SetMediaResolver(media.AsMediaResolver(mediaStore))

	// Wire up voice transcription if a supported provider is configured.
	if transcriber := voice.DetectTranscriber(cfg); transcriber != nil {
		agentLoop.SetTranscriber(transcriber)
		logger.InfoCF("voice", "Transcription enabled (agent-level)", map[string]any{"provider": transcriber.Name()})
	}

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
	if err := registerGatewayHTTPAPI(svc); err != nil {
		return fmt.Errorf("register http api: %w", err)
	}

	if err := svc.channelManager.StartAll(ctx); err != nil {
		fmt.Printf("Error starting channels: %v\n", err)
		return err
	}

	fmt.Printf("✓ Health endpoints available at http://%s:%d/health (/healthz) and /ready (/readyz)\n", svc.cfg.Gateway.Host, svc.cfg.Gateway.Port)

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

	// Create and register CronTool if enabled
	var cronTool *tools.CronTool
	if cfg.Tools.IsToolEnabled("cron") {
		var err error
		cronTool, err = tools.NewCronTool(cronService, agentLoop, msgBus, workspace, restrict, execTimeout, cfg)
		if err != nil {
			log.Fatalf("Critical error during CronTool initialization: %v", err)
		}

		agentLoop.RegisterTool(cronTool)
	}

	// Set onJob handler
	if cronTool != nil {
		cronService.SetOnJob(func(job *cron.CronJob) (string, error) {
			return cronTool.ExecuteJob(context.Background(), job)
		})
	}

	return cronService
}
