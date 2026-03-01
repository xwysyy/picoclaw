package cron

import (
	"fmt"
	"time"

	"github.com/sipeed/picoclaw/pkg/cron"
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
