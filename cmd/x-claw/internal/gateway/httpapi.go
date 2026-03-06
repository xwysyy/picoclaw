package gateway

import (
	"context"
	"fmt"
	"net/http"
	"time"

	coregateway "github.com/xwysyy/X-Claw/internal/gateway"
	pkghttpapi "github.com/xwysyy/X-Claw/pkg/httpapi"
	"github.com/xwysyy/X-Claw/pkg/session"
)

type gatewayHTTPRegistration struct {
	pattern string
	handler http.Handler
}

func buildGatewayHTTPRegistrations(svc *gatewayServices) ([]gatewayHTTPRegistration, error) {
	if svc == nil || svc.cfg == nil {
		return nil, nil
	}

	apiKey, err := resolveGatewayAPIKey(svc.cfg)
	if err != nil {
		return nil, fmt.Errorf("gateway.api_key: %w", err)
	}

	regs := []gatewayHTTPRegistration{}

	regs = append(regs, gatewayHTTPRegistration{
		pattern: "/api/notify",
		handler: coregateway.NewNotifyHandler(coregateway.NotifyHandlerOptions{
			Sender: svc.channelManager,
			APIKey: apiKey,
			LastActive: func() (string, string) {
				if svc.agentLoop == nil {
					return "", ""
				}
				return svc.agentLoop.LastActive()
			},
		}),
	})

	regs = append(regs, gatewayHTTPRegistration{
		pattern: "/api/resume_last_task",
		handler: coregateway.NewResumeLastTaskHandler(coregateway.ResumeLastTaskHandlerOptions{
			APIKey:  apiKey,
			Timeout: 2 * time.Minute,
			Resume: func(ctx context.Context) (any, string, error) {
				if svc.agentLoop == nil {
					return nil, "", fmt.Errorf("agent loop not available")
				}
				candidate, response, err := svc.agentLoop.ResumeLastTask(ctx)
				return candidate, response, err
			},
		}),
	})

	regs = append(regs, gatewayHTTPRegistration{
		pattern: "/api/session_model",
		handler: pkghttpapi.NewSessionModelHandler(pkghttpapi.SessionModelHandlerOptions{
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
		}),
	})

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
	regs = append(regs,
		gatewayHTTPRegistration{pattern: "/console/", handler: console},
		gatewayHTTPRegistration{pattern: "/api/console/", handler: console},
	)

	return regs, nil
}

func registerGatewayHTTPAPI(svc *gatewayServices) error {
	if svc == nil || svc.channelManager == nil {
		return nil
	}

	regs, err := buildGatewayHTTPRegistrations(svc)
	if err != nil {
		return err
	}
	for _, reg := range regs {
		if err := svc.channelManager.RegisterHTTPHandler(reg.pattern, reg.handler); err != nil {
			fmt.Printf("⚠ Warning: failed to register %s: %v\n", reg.pattern, err)
		}
	}
	return nil
}
