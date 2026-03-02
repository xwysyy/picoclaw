package gateway

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
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
	provider         providers.LLMProvider
	msgBus           *bus.MessageBus
	agentLoop        *agent.AgentLoop
	cronService      *cron.CronService
	heartbeatService *heartbeat.HeartbeatService
	mediaStore       *media.FileMediaStore
	channelManager   *channels.Manager
	healthServer     *health.Server
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
	signal.Notify(sigChan, os.Interrupt)
	<-sigChan

	return shutdownGateway(svc, cancel)
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
		APIKey: svc.cfg.Gateway.APIKey,
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
