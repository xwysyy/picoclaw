package tools

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/xwysyy/X-Claw/pkg/bus"
	"github.com/xwysyy/X-Claw/pkg/config"
	"github.com/xwysyy/X-Claw/pkg/constants"
	"github.com/xwysyy/X-Claw/pkg/cron"
	"github.com/xwysyy/X-Claw/pkg/utils"
)

// JobExecutor is the interface for executing cron jobs through the agent
type JobExecutor interface {
	ProcessDirectWithChannel(ctx context.Context, content, sessionKey, channel, chatID string) (string, error)
}

type lastActiveProvider interface {
	LastActive() (channel string, chatID string)
}

// CronTool provides scheduling capabilities for the agent
type CronTool struct {
	cronService *cron.CronService
	executor    JobExecutor
	msgBus      *bus.MessageBus
	execTool    *ExecTool
}

func isNoUpdateResponse(raw string) bool {
	s := strings.TrimSpace(raw)
	if s == "" {
		return true
	}
	if strings.EqualFold(s, "HEARTBEAT_OK") {
		return true
	}

	upper := strings.ToUpper(s)
	switch upper {
	case "NO_UPDATE", "NO-UPDATE", "NOUPDATE":
		return true
	}
	if strings.HasPrefix(upper, "NO_UPDATE") {
		rest := strings.TrimSpace(strings.TrimPrefix(upper, "NO_UPDATE"))
		if rest == "" || strings.HasPrefix(rest, ":") || strings.HasPrefix(rest, "-") {
			return true
		}
	}
	return false
}

// NewCronTool creates a new CronTool
// execTimeout: 0 means no timeout, >0 sets the timeout duration
func NewCronTool(
	cronService *cron.CronService, executor JobExecutor, msgBus *bus.MessageBus, workspace string, restrict bool,
	execTimeout time.Duration, config *config.Config,
) (*CronTool, error) {
	execTool, err := NewExecToolWithConfig(workspace, restrict, config)
	if err != nil {
		return nil, fmt.Errorf("unable to configure exec tool: %w", err)
	}

	execTool.SetTimeout(execTimeout)
	return &CronTool{
		cronService: cronService,
		executor:    executor,
		msgBus:      msgBus,
		execTool:    execTool,
	}, nil
}

// Name returns the tool name
func (t *CronTool) Name() string {
	return "cron"
}

// Description returns the tool description
func (t *CronTool) Description() string {
	return "Schedule reminders, tasks, or system commands. IMPORTANT: When user asks to be reminded or scheduled, you MUST call this tool. Use 'at_seconds' for one-time reminders (e.g., 'remind me in 10 minutes' → at_seconds=600). Use 'every_seconds' ONLY for recurring tasks (e.g., 'every 2 hours' → every_seconds=7200). Use 'cron_expr' for complex recurring schedules. Use 'command' to execute shell commands directly."
}

// Parameters returns the tool parameters schema
func (t *CronTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"action": map[string]any{
				"type":        "string",
				"enum":        []string{"add", "list", "remove", "enable", "disable"},
				"description": "Action to perform. Use 'add' when user wants to schedule a reminder or task.",
			},
			"message": map[string]any{
				"type":        "string",
				"description": "The reminder/task message to display when triggered. If 'command' is used, this describes what the command does.",
			},
			"command": map[string]any{
				"type":        "string",
				"description": "Optional: Shell command to execute directly (e.g., 'df -h'). If set, the agent will run this command and report output instead of just showing the message. 'deliver' will be forced to false for commands.",
			},
			"at_seconds": map[string]any{
				"type":        "integer",
				"description": "One-time reminder: seconds from now when to trigger (e.g., 600 for 10 minutes later). Use this for one-time reminders like 'remind me in 10 minutes'.",
			},
			"every_seconds": map[string]any{
				"type":        "integer",
				"description": "Recurring interval in seconds (e.g., 3600 for every hour). Use this ONLY for recurring tasks like 'every 2 hours' or 'daily reminder'.",
			},
			"cron_expr": map[string]any{
				"type":        "string",
				"description": "Cron expression for complex recurring schedules (e.g., '0 9 * * *' for daily at 9am). Use this for complex recurring schedules.",
			},
			"timezone": map[string]any{
				"type":        "string",
				"description": "Optional IANA timezone for cron_expr (e.g., 'Asia/Shanghai'). Defaults to local timezone.",
			},
			"job_id": map[string]any{
				"type":        "string",
				"description": "Job ID (for remove/enable/disable)",
			},
			"deliver": map[string]any{
				"type":        "boolean",
				"description": "If true, send message directly to channel. If false, let agent process message (for complex tasks). Default: true",
			},
		},
		"required": []string{"action"},
	}
}

// Execute runs the tool with the given arguments
func (t *CronTool) Execute(ctx context.Context, args map[string]any) *ToolResult {
	action, ok := args["action"].(string)
	if !ok {
		return ErrorResult("action is required")
	}

	switch action {
	case "add":
		return t.addJob(ctx, args)
	case "list":
		return t.listJobs()
	case "remove":
		return t.removeJob(args)
	case "enable":
		return t.enableJob(args, true)
	case "disable":
		return t.enableJob(args, false)
	default:
		return ErrorResult(fmt.Sprintf("unknown action: %s", action))
	}
}

func (t *CronTool) addJob(ctx context.Context, args map[string]any) *ToolResult {
	channel := ToolChannel(ctx)
	chatID := ToolChatID(ctx)

	if channel == "" || chatID == "" {
		return ErrorResult("no session context (channel/chat_id not set). Use this tool in an active conversation.")
	}

	message, ok := args["message"].(string)
	if !ok || message == "" {
		return ErrorResult("message is required for add")
	}

	var schedule cron.CronSchedule

	// Check for at_seconds (one-time), every_seconds (recurring), or cron_expr.
	// Some model providers emit unused numeric args as 0. Treat 0 as "unset" to avoid
	// mistakenly creating an immediate "at" schedule that fails validation.
	atSeconds, hasAt, err := parsePositiveSecondsArg(args, "at_seconds")
	if err != nil {
		return ErrorResult(err.Error())
	}
	everySeconds, hasEvery, err := parsePositiveSecondsArg(args, "every_seconds")
	if err != nil {
		return ErrorResult(err.Error())
	}
	cronExpr, _ := args["cron_expr"].(string)
	cronExpr = strings.TrimSpace(cronExpr)
	hasCron := cronExpr != ""
	timezone, _ := args["timezone"].(string)

	// Priority: cron_expr > every_seconds > at_seconds
	// This is resilient when LLMs include unused defaults (e.g. at_seconds=0).
	if hasCron {
		schedule = cron.CronSchedule{
			Kind: "cron",
			Expr: cronExpr,
			TZ:   timezone,
		}
	} else if hasEvery {
		everyMS := everySeconds * 1000
		schedule = cron.CronSchedule{
			Kind:    "every",
			EveryMS: &everyMS,
		}
	} else if hasAt {
		atMS := time.Now().UnixMilli() + atSeconds*1000
		schedule = cron.CronSchedule{
			Kind: "at",
			AtMS: &atMS,
		}
	} else {
		return ErrorResult("one of at_seconds (>0), every_seconds (>0), or cron_expr is required")
	}

	// Read deliver parameter, default to true
	deliver := true
	if d, ok := args["deliver"].(bool); ok {
		deliver = d
	}

	command, _ := args["command"].(string)
	if command != "" {
		// Commands must be processed by agent/exec tool, so deliver must be false (or handled specifically)
		// Actually, let's keep deliver=false to let the system know it's not a simple chat message
		// But for our new logic in ExecuteJob, we can handle it regardless of deliver flag if Payload.Command is set.
		// However, logically, it's not "delivered" to chat directly as is.
		deliver = false
	}

	// Truncate message for job name (max 30 chars)
	messagePreview := utils.Truncate(message, 30)

	job, err := t.cronService.AddJob(
		messagePreview,
		schedule,
		message,
		deliver,
		channel,
		chatID,
	)
	if err != nil {
		return ErrorResult(fmt.Sprintf("Error adding job: %v", err))
	}

	if command != "" {
		job.Payload.Command = command
		// Need to save the updated payload
		t.cronService.UpdateJob(job)
	}

	return SilentResult(fmt.Sprintf("Cron job added: %s (id: %s)", job.Name, job.ID))
}

func parsePositiveSecondsArg(args map[string]any, key string) (int64, bool, error) {
	raw, exists := args[key]
	if !exists || raw == nil {
		return 0, false, nil
	}

	n, err := toInt(raw)
	if err != nil {
		return 0, false, fmt.Errorf("%s must be an integer", key)
	}
	if n < 0 {
		return 0, false, fmt.Errorf("%s must be >= 0", key)
	}
	if n == 0 {
		return 0, false, nil
	}
	return int64(n), true, nil
}

func (t *CronTool) listJobs() *ToolResult {
	jobs := t.cronService.ListJobs(false)

	if len(jobs) == 0 {
		return SilentResult("No scheduled jobs")
	}

	var result strings.Builder
	result.WriteString("Scheduled jobs:\n")
	for _, j := range jobs {
		var scheduleInfo string
		if j.Schedule.Kind == "every" && j.Schedule.EveryMS != nil {
			scheduleInfo = fmt.Sprintf("every %ds", *j.Schedule.EveryMS/1000)
		} else if j.Schedule.Kind == "cron" {
			tz := strings.TrimSpace(j.Schedule.TZ)
			if tz == "" {
				tz = "local"
			}
			scheduleInfo = fmt.Sprintf("%s (tz=%s)", j.Schedule.Expr, tz)
		} else if j.Schedule.Kind == "at" {
			scheduleInfo = "one-time"
		} else {
			scheduleInfo = "unknown"
		}

		status := "enabled"
		if !j.Enabled {
			status = "disabled"
		}
		if j.State.Running {
			status += ", running"
		}

		nextRun := "n/a"
		if j.State.NextRunAtMS != nil {
			nextRun = time.UnixMilli(*j.State.NextRunAtMS).Format("2006-01-02 15:04:05")
		}
		lastRun := "never"
		if j.State.LastRunAtMS != nil {
			lastRun = time.UnixMilli(*j.State.LastRunAtMS).Format("2006-01-02 15:04:05")
		}

		result.WriteString(fmt.Sprintf("- %s (id: %s)\n", j.Name, j.ID))
		result.WriteString(fmt.Sprintf("  schedule: %s\n", scheduleInfo))
		result.WriteString(fmt.Sprintf("  status: %s\n", status))
		result.WriteString(fmt.Sprintf("  next_run_at: %s\n", nextRun))
		result.WriteString(fmt.Sprintf("  last_run_at: %s\n", lastRun))

		if j.State.LastStatus != "" {
			result.WriteString(fmt.Sprintf("  last_status: %s\n", j.State.LastStatus))
		}
		if j.State.LastDurationMS != nil {
			result.WriteString(fmt.Sprintf("  last_duration_ms: %d\n", *j.State.LastDurationMS))
		}
		if strings.TrimSpace(j.State.LastError) != "" {
			result.WriteString(fmt.Sprintf("  last_error: %s\n", strings.TrimSpace(j.State.LastError)))
		}
	}

	return SilentResult(result.String())
}

func (t *CronTool) removeJob(args map[string]any) *ToolResult {
	jobID, ok := args["job_id"].(string)
	if !ok || jobID == "" {
		return ErrorResult("job_id is required for remove")
	}

	if t.cronService.RemoveJob(jobID) {
		return SilentResult(fmt.Sprintf("Cron job removed: %s", jobID))
	}
	return ErrorResult(fmt.Sprintf("Job %s not found", jobID))
}

func (t *CronTool) enableJob(args map[string]any, enable bool) *ToolResult {
	jobID, ok := args["job_id"].(string)
	if !ok || jobID == "" {
		return ErrorResult("job_id is required for enable/disable")
	}

	job := t.cronService.EnableJob(jobID, enable)
	if job == nil {
		return ErrorResult(fmt.Sprintf("Job %s not found", jobID))
	}

	status := "enabled"
	if !enable {
		status = "disabled"
	}
	return SilentResult(fmt.Sprintf("Cron job '%s' %s", job.Name, status))
}

// ExecuteJob executes a cron job through the agent
func (t *CronTool) ExecuteJob(ctx context.Context, job *cron.CronJob) (string, error) {
	if job == nil {
		return "", fmt.Errorf("job is nil")
	}

	// Get channel/chatID from job payload
	channel := strings.TrimSpace(job.Payload.Channel)
	chatID := strings.TrimSpace(job.Payload.To)

	// Prefer last_active when destination is missing (or internal).
	if (channel == "" || chatID == "" || constants.IsInternalChannel(channel)) && t.executor != nil {
		if lap, ok := t.executor.(lastActiveProvider); ok {
			lastCh, lastID := lap.LastActive()
			lastCh = strings.TrimSpace(lastCh)
			lastID = strings.TrimSpace(lastID)
			if lastCh != "" && lastID != "" && !constants.IsInternalChannel(lastCh) {
				switch {
				case channel == "" && chatID == "":
					channel, chatID = lastCh, lastID
				case channel != "" && chatID == "" && strings.EqualFold(channel, lastCh):
					chatID = lastID
				case channel == "" && chatID != "" && chatID == lastID:
					channel = lastCh
				}
			}
		}
	}

	if channel == "" || chatID == "" {
		// Backward-compatible fallback (CLI), but treat as an error in gateway mode so job state shows it.
		// Note: internal channels are not delivered to users.
		if channel == "" {
			channel = "cli"
		}
		if chatID == "" {
			chatID = "direct"
		}
		return "", fmt.Errorf("no delivery destination for cron job %q (channel/to missing and last_active unavailable)", job.ID)
	}

	// Execute command if present
	if job.Payload.Command != "" {
		args := map[string]any{
			"command": job.Payload.Command,
		}

		result := t.execTool.Execute(ctx, args)
		var output string
		if result.IsError {
			output = fmt.Sprintf("Cron job '%s' failed executing command:\n%s", job.Name, result.ForLLM)
		} else {
			output = fmt.Sprintf("Cron job '%s' executed command:\n%s", job.Name, result.ForLLM)
		}

		pubCtx, pubCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer pubCancel()
		if err := t.msgBus.PublishOutbound(pubCtx, bus.OutboundMessage{
			Channel: channel,
			ChatID:  chatID,
			Content: output,
		}); err != nil {
			return "", err
		}
		if result.IsError {
			return output, fmt.Errorf("cron command failed: %s", strings.TrimSpace(result.ForLLM))
		}
		return output, nil
	}

	// If deliver=true, send message directly without agent processing
	if job.Payload.Deliver {
		pubCtx, pubCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer pubCancel()
		if err := t.msgBus.PublishOutbound(pubCtx, bus.OutboundMessage{
			Channel: channel,
			ChatID:  chatID,
			Content: job.Payload.Message,
		}); err != nil {
			return "", err
		}
		return job.Payload.Message, nil
	}

	// For deliver=false, process through agent (for complex tasks)
	if t.executor == nil {
		return "", fmt.Errorf("cron executor is not configured")
	}
	sessionKey := fmt.Sprintf("cron-%s", job.ID)

	// Call agent with job's message
	response, err := t.executor.ProcessDirectWithChannel(
		ctx,
		job.Payload.Message,
		sessionKey,
		channel,
		chatID,
	)
	if err != nil {
		pubCtx, pubCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer pubCancel()
		_ = t.msgBus.PublishOutbound(pubCtx, bus.OutboundMessage{
			Channel: channel,
			ChatID:  chatID,
			Content: fmt.Sprintf("Cron job '%s' failed: %v", job.Name, err),
		})
		return "", err
	}

	// In gateway mode, ProcessDirectWithChannel returns the response but does NOT publish it.
	// Publish here so scheduled tasks can proactively notify the user (e.g. Feishu).
	if strings.TrimSpace(response) != "" && !isNoUpdateResponse(response) {
		pubCtx, pubCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer pubCancel()
		if err := t.msgBus.PublishOutbound(pubCtx, bus.OutboundMessage{
			Channel: channel,
			ChatID:  chatID,
			Content: fmt.Sprintf("Cron job '%s' completed.\n\n%s", job.Name, response),
		}); err != nil {
			return "", err
		}
	}

	if strings.TrimSpace(response) == "" {
		return "ok", nil
	}
	if isNoUpdateResponse(response) {
		return "NO_UPDATE", nil
	}
	return response, nil
}
