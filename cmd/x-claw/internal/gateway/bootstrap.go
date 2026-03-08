package gateway

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"github.com/xwysyy/X-Claw/cmd/x-claw/internal"
	"github.com/xwysyy/X-Claw/cmd/x-claw/internal/cliutil"
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
	"github.com/xwysyy/X-Claw/pkg/tools"
	"github.com/xwysyy/X-Claw/pkg/voice"
)

func initGatewayServices(debug bool) (*gatewayServices, error) {
	configPath := internal.GetConfigPath()
	runtime, err := cliutil.BootstrapAgentRuntime("")
	if err != nil {
		return nil, err
	}

	cfg := runtime.Config
	provider := runtime.Provider
	msgBus := runtime.MessageBus
	agentLoop := runtime.AgentLoop

	printStartupInfo(agentLoop)

	execTimeout := time.Duration(cfg.Tools.Cron.ExecTimeoutMinutes) * time.Minute
	cronService, err := setupCronTool(agentLoop, msgBus, cfg.WorkspacePath(), cfg.Agents.Defaults.RestrictToWorkspace, execTimeout, cfg)
	if err != nil {
		msgBus.Close()
		return nil, fmt.Errorf("error setting up cron tool: %w", err)
	}

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
		msgBus.Close()
		return nil, fmt.Errorf("error creating channel manager: %w", err)
	}

	agentLoop.SetChannelManager(channelManager)
	agentLoop.SetMediaStore(mediaStore)
	agentLoop.SetMediaResolver(media.AsMediaResolver(mediaStore))

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
) (*cron.CronService, error) {
	cronStorePath := filepath.Join(workspace, "cron", "jobs.json")
	cronService := cron.NewCronService(cronStorePath, nil)

	var cronTool *tools.CronTool
	if cfg.Tools.IsToolEnabled("cron") {
		var err error
		cronTool, err = tools.NewCronTool(cronService, agentLoop, msgBus, workspace, restrict, execTimeout, cfg)
		if err != nil {
			return nil, fmt.Errorf("initialize cron tool: %w", err)
		}

		agentLoop.RegisterTool(cronTool)
	}

	if cronTool != nil {
		cronService.SetOnJob(func(job *cron.CronJob) (string, error) {
			return cronTool.ExecuteJob(context.Background(), job)
		})
	}

	return cronService, nil
}
