package schedule

import (
	"errors"
	"testing"
	"time"

	"agentflow/internal/cronexpr"
	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"agentflow/internal/tracker"
)

const hourly = "0 * * * *"

func firstRunService(t *testing.T, jobs ...domain.ScheduleJob) (*repo.Store, *Service) {
	t.Helper()
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	for _, j := range jobs {
		if err := st.PutScheduleJob(j); err != nil {
			t.Fatal(err)
		}
	}
	svc := New(st, tracker.NewService(), nil)
	svc.SetAssistantPrep(func(domain.ScheduleJob) (string, error) { return "ok", nil })
	return st, svc
}

// assertFirstAt checks the timer leaves the job alone until at, then claims it.
func assertFirstAt(t *testing.T, svc *Service, id string, at time.Time) {
	t.Helper()
	if _, err := svc.claimJob(id, at.Add(-time.Nanosecond)); !errors.Is(err, errNotDue) {
		t.Fatalf("job ran before %s: err=%v", at, err)
	}
	if _, err := svc.claimJob(id, at); err != nil {
		t.Fatalf("job did not run at %s: %v", at, err)
	}
	svc.release(id)
}

func TestNewCronJobWaitsForFirstOccurrence(t *testing.T) {
	st, svc := firstRunService(t, domain.ScheduleJob{ID: "cron", Name: "c", Enabled: true, Cron: hourly, SkipDispatch: true, AssistantPrompt: "p"})
	job, _ := st.ScheduleJob("cron")
	if !job.ScheduleFrom.IsZero() {
		t.Fatalf("insert must not need ScheduleFrom: %+v", job)
	}
	next, _ := cronexpr.Next(hourly, job.CreatedAt)
	assertFirstAt(t, svc, "cron", next)
}

func TestNewIntervalJobWaitsOneInterval(t *testing.T) {
	st, svc := firstRunService(t, domain.ScheduleJob{ID: "iv", Name: "i", Enabled: true, IntervalSec: 3600, SkipDispatch: true, AssistantPrompt: "p"})
	job, _ := st.ScheduleJob("iv")
	assertFirstAt(t, svc, "iv", job.CreatedAt.Add(time.Hour))
}

func TestReenableAndRescheduleDoNotCatchUp(t *testing.T) {
	longAgo := time.Now().UTC().Add(-72 * time.Hour)
	for name, change := range map[string]func(st *repo.Store, j domain.ScheduleJob) error{
		"toggle": func(st *repo.Store, _ domain.ScheduleJob) error {
			_, err := st.SetScheduleJobEnabled("j", true)
			return err
		},
		"save enabled": func(st *repo.Store, j domain.ScheduleJob) error {
			j.Enabled = true
			return st.PutScheduleJob(j)
		},
	} {
		t.Run(name, func(t *testing.T) {
			st, svc := firstRunService(t, domain.ScheduleJob{ID: "j", Name: "j", Cron: hourly, LastStatus: "success", LastRunAt: longAgo, SkipDispatch: true, AssistantPrompt: "p"})
			j, _ := st.ScheduleJob("j")
			if err := change(st, j); err != nil {
				t.Fatal(err)
			}
			j, _ = st.ScheduleJob("j")
			if !j.Enabled || !j.ScheduleFrom.After(longAgo) {
				t.Fatalf("enable must move ScheduleFrom: %+v", j)
			}
			next, _ := cronexpr.Next(hourly, j.ScheduleFrom)
			assertFirstAt(t, svc, "j", next)
		})
	}
	t.Run("cron edit", func(t *testing.T) {
		st, svc := firstRunService(t, domain.ScheduleJob{ID: "j", Name: "j", Enabled: true, Cron: "0 9 * * *", LastStatus: "success", LastRunAt: longAgo, SkipDispatch: true, AssistantPrompt: "p"})
		j, _ := st.ScheduleJob("j")
		j.Cron = hourly
		if err := st.PutScheduleJob(j); err != nil {
			t.Fatal(err)
		}
		j, _ = st.ScheduleJob("j")
		next, _ := cronexpr.Next(hourly, j.ScheduleFrom)
		assertFirstAt(t, svc, "j", next)
		// Saving other fields keeps the anchor.
		j, _ = st.ScheduleJob("j")
		from := j.ScheduleFrom
		j.AssistantPrompt = "changed"
		if err := st.PutScheduleJob(j); err != nil {
			t.Fatal(err)
		}
		if j, _ = st.ScheduleJob("j"); !j.ScheduleFrom.Equal(from) {
			t.Fatalf("unrelated edit moved ScheduleFrom: %s -> %s", from, j.ScheduleFrom)
		}
	})
}

// A never-run job stored before ScheduleFrom existed counts from startup.
func TestLegacyNeverRunJobCountsFromStartup(t *testing.T) {
	st, svc := firstRunService(t, domain.ScheduleJob{ID: "old", Name: "o", Enabled: true, Cron: hourly, SkipDispatch: true, AssistantPrompt: "p"})
	job, _ := st.ScheduleJob("old")
	svc.startedAt = job.CreatedAt.Add(3 * time.Hour) // created long before this process
	next, _ := cronexpr.Next(hourly, svc.startedAt)
	assertFirstAt(t, svc, "old", next)
}

func TestManualRunIgnoresSchedule(t *testing.T) {
	_, svc := firstRunService(t, domain.ScheduleJob{ID: "m", Name: "m", Enabled: true, Cron: "0 9 * * *", SkipDispatch: true, AssistantPrompt: "p"})
	got, err := svc.RunNow("m")
	if err != nil || got.LastStatus != "success" {
		t.Fatalf("manual run: %+v %v", got, err)
	}
}
