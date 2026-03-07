package gateway

import (
	"context"
	"fmt"
	"time"

	coregateway "github.com/xwysyy/X-Claw/internal/gateway"
	oldhttpapi "github.com/xwysyy/X-Claw/pkg/httpapi"
	"github.com/xwysyy/X-Claw/pkg/session"
)

func registerGatewayHTTPAPI(svc *gatewayServices) error {
	if svc == nil || svc.channelManager == nil {
		return nil
	}

	apiKey, err := resolveGatewayAPIKey(svc.cfg)
	if err != nil {
		return fmt.Errorf("gateway.api_key: %w", err)
	}

	notify := coregateway.NewNotifyHandler(coregateway.NotifyHandlerOptions{
		Sender: svc.channelManager,
		APIKey: apiKey,
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

	resume := coregateway.NewResumeLastTaskHandler(coregateway.ResumeLastTaskHandlerOptions{
		APIKey:  apiKey,
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

	estop := oldhttpapi.NewEstopHandler(oldhttpapi.EstopHandlerOptions{
		APIKey:       apiKey,
		Workspace:    svc.cfg.WorkspacePath(),
		Enabled:      svc.cfg.Tools.Estop.Enabled,
		FailClosed:   svc.cfg.Tools.Estop.FailClosed,
		MaxBodyBytes: 8 << 10,
	})
	if err := svc.channelManager.RegisterHTTPHandler("/api/estop", estop); err != nil {
		fmt.Printf("⚠ Warning: failed to register /api/estop: %v\n", err)
	}

	sessionModel := oldhttpapi.NewSessionModelHandler(oldhttpapi.SessionModelHandlerOptions{
		APIKey:    apiKey,
		Workspace: svc.cfg.WorkspacePath(),
		Sessions: func() session.Store {
			if svc.agentLoop == nil {
				return nil
			}
			return svc.agentLoop.SessionStore()
		}(),
		Enabled:      true,
		MaxBodyBytes: 8 << 10,
	})
	if err := svc.channelManager.RegisterHTTPHandler("/api/session_model", sessionModel); err != nil {
		fmt.Printf("⚠ Warning: failed to register /api/session_model: %v\n", err)
	}

	security := oldhttpapi.NewSecurityHandler(oldhttpapi.SecurityHandlerOptions{
		APIKey:    apiKey,
		Workspace: svc.cfg.WorkspacePath(),
		Config:    svc.cfg,
	})
	if err := svc.channelManager.RegisterHTTPHandler("/api/security", security); err != nil {
		fmt.Printf("⚠ Warning: failed to register /api/security: %v\n", err)
	}

	console := coregateway.NewConsoleHandler(coregateway.ConsoleHandlerOptions{
		Workspace: svc.cfg.WorkspacePath(),
		APIKey:    apiKey,
		LastActive: func() (string, string) {
			if svc.agentLoop == nil {
				return "", ""
			}
			return svc.agentLoop.LastActive()
		},
		Info: coregateway.ConsoleInfo{
			Model:                      svc.cfg.Agents.Defaults.ModelName,
			NotifyOnTaskComplete:       svc.cfg.Notify.OnTaskComplete,
			ToolTraceEnabled:           svc.cfg.Tools.Trace.Enabled,
			RunTraceEnabled:            svc.cfg.Tools.Trace.Enabled,
			WebEvidenceMode:            svc.cfg.Tools.Web.Evidence.Enabled,
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

	return nil
}
