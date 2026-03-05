package cron

import (
	"fmt"
	"strings"
	"time"

	"github.com/xwysyy/X-Claw/cmd/x-claw/internal/cliutil"
	"github.com/xwysyy/X-Claw/pkg/cron"
)

func cronListCmd(storePath string) {
	cs := cron.NewCronService(storePath, nil)
	jobs := cs.ListJobs(true) // Show all jobs, including disabled

	if len(jobs) == 0 {
		fmt.Println("No scheduled jobs.")
		return
	}

	fmt.Println("\nScheduled Jobs:")
	fmt.Println("----------------")
	for _, job := range jobs {
		var schedule string
		if job.Schedule.Kind == "every" && job.Schedule.EveryMS != nil {
			schedule = fmt.Sprintf("every %ds", *job.Schedule.EveryMS/1000)
		} else if job.Schedule.Kind == "cron" {
			tz := job.Schedule.TZ
			if tz == "" {
				tz = "local"
			}
			schedule = fmt.Sprintf("%s (tz=%s)", job.Schedule.Expr, tz)
		} else {
			schedule = "one-time"
		}

		nextRun := "scheduled"
		if job.State.NextRunAtMS != nil {
			nextTime := time.UnixMilli(*job.State.NextRunAtMS)
			nextRun = nextTime.Format("2006-01-02 15:04:05")
		}

		status := "enabled"
		if !job.Enabled {
			status = "disabled"
		}
		if job.State.Running {
			status += ", running"
		}

		lastRun := "never"
		if job.State.LastRunAtMS != nil {
			lastRun = time.UnixMilli(*job.State.LastRunAtMS).Format("2006-01-02 15:04:05")
		}

		fmt.Printf("  %s (%s)\n", job.Name, job.ID)
		fmt.Printf("    Schedule: %s\n", schedule)
		fmt.Printf("    Status: %s\n", status)
		fmt.Printf("    Next run: %s\n", nextRun)
		fmt.Printf("    Last run: %s\n", lastRun)
		if job.State.LastStatus != "" {
			fmt.Printf("    Last status: %s\n", job.State.LastStatus)
		}
		if job.State.LastDurationMS != nil {
			fmt.Printf("    Last duration: %dms\n", *job.State.LastDurationMS)
		}
		if job.State.LastError != "" {
			fmt.Printf("    Last error: %s\n", job.State.LastError)
		}
	}
}

func cronShowCmd(storePath, jobID string, jsonOut bool) error {
	jobID = strings.TrimSpace(jobID)
	if jobID == "" {
		return fmt.Errorf("job_id is required")
	}

	cs := cron.NewCronService(storePath, nil)
	jobs := cs.ListJobs(true) // include disabled

	var found *cron.CronJob
	for i := range jobs {
		if jobs[i].ID == jobID {
			found = &jobs[i]
			break
		}
	}
	if found == nil {
		return fmt.Errorf("job %s not found", jobID)
	}

	if jsonOut {
		data, err := cliutil.MarshalIndentNoEscape(found)
		if err != nil {
			return err
		}
		fmt.Println(string(data))
		return nil
	}

	fmt.Printf("\nCron Job: %s (%s)\n", found.Name, found.ID)
	fmt.Println("------------------------")
	fmt.Printf("Enabled: %t\n", found.Enabled)
	fmt.Printf("Schedule: %s\n", formatSchedule(found.Schedule))
	fmt.Printf("Deliver: %t\n", found.Payload.Deliver)
	if found.Payload.Channel != "" || found.Payload.To != "" {
		fmt.Printf("Channel/To: %s/%s\n", found.Payload.Channel, found.Payload.To)
	}
	if found.Payload.Command != "" {
		fmt.Printf("Command: %s\n", found.Payload.Command)
	}
	if strings.TrimSpace(found.Payload.Message) != "" {
		fmt.Printf("Message: %s\n", found.Payload.Message)
	}

	if found.State.NextRunAtMS != nil {
		fmt.Printf("Next run: %s\n", time.UnixMilli(*found.State.NextRunAtMS).Format("2006-01-02 15:04:05"))
	}
	if found.State.LastRunAtMS != nil {
		fmt.Printf("Last run: %s\n", time.UnixMilli(*found.State.LastRunAtMS).Format("2006-01-02 15:04:05"))
	}
	if found.State.LastStatus != "" {
		fmt.Printf("Last status: %s\n", found.State.LastStatus)
	}
	if found.State.LastDurationMS != nil {
		fmt.Printf("Last duration: %dms\n", *found.State.LastDurationMS)
	}
	if found.State.LastError != "" {
		fmt.Printf("Last error: %s\n", found.State.LastError)
	}
	if strings.TrimSpace(found.State.LastOutputPreview) != "" {
		fmt.Printf("\nLast output preview:\n%s\n", found.State.LastOutputPreview)
	}
	if len(found.State.RunHistory) > 0 {
		fmt.Printf("\nRun history (latest %d):\n", len(found.State.RunHistory))
		for _, r := range found.State.RunHistory {
			started := time.UnixMilli(r.StartedAtMS).Format("2006-01-02 15:04:05")
			fmt.Printf("  - %s %s (%dms)\n", started, r.Status, r.DurationMS)
			if r.Error != "" {
				fmt.Printf("    error: %s\n", r.Error)
			}
		}
	}
	fmt.Println()
	return nil
}

func formatSchedule(s cron.CronSchedule) string {
	switch s.Kind {
	case "every":
		if s.EveryMS != nil {
			return fmt.Sprintf("every %ds", *s.EveryMS/1000)
		}
		return "every"
	case "cron":
		tz := s.TZ
		if tz == "" {
			tz = "local"
		}
		return fmt.Sprintf("%s (tz=%s)", s.Expr, tz)
	case "at":
		if s.AtMS != nil {
			return fmt.Sprintf("at %s", time.UnixMilli(*s.AtMS).Format("2006-01-02 15:04:05"))
		}
		return "at"
	default:
		return s.Kind
	}
}

func cronRemoveCmd(storePath, jobID string) {
	cs := cron.NewCronService(storePath, nil)
	if cs.RemoveJob(jobID) {
		fmt.Printf("✓ Removed job %s\n", jobID)
	} else {
		fmt.Printf("✗ Job %s not found\n", jobID)
	}
}

func cronSetJobEnabled(storePath, jobID string, enabled bool) {
	cs := cron.NewCronService(storePath, nil)
	job := cs.EnableJob(jobID, enabled)
	if job != nil {
		status := "enabled"
		if !enabled {
			status = "disabled"
		}
		fmt.Printf("✓ Job '%s' %s\n", job.Name, status)
	} else {
		fmt.Printf("✗ Job %s not found\n", jobID)
	}
}
