package catalog

import (
	"testing"
	"time"

	"github.com/dsswift/redgreen/internal/provider"
)

var now = time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)

func ts(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

func run(created string, mod func(*provider.Run)) provider.Run {
	r := provider.Run{ID: created, Number: 1, Trigger: provider.TriggerPush, Ref: "main", SHA: "abc", Status: provider.StatusCompleted, Conclusion: provider.Success, CreatedAt: ts(created), UpdatedAt: ts(created).Add(2 * time.Minute)}
	if mod != nil {
		mod(&r)
	}
	return r
}

func pipe(runs ...provider.Run) provider.Pipeline {
	return provider.Pipeline{ID: "p1", Name: "CI", State: provider.Enabled, CreatedAt: ts("2026-01-01T00:00:00Z"), UpdatedAt: ts("2026-01-01T00:00:00Z"), Runs: runs}
}

func judge(p provider.Pipeline) Health {
	return Judge(p, "main", nil, JudgeRelease([]provider.Pipeline{p}), now)
}

func fails(r *provider.Run)     { r.Conclusion = provider.Failure }
func cancelled(r *provider.Run) { r.Conclusion = provider.Cancelled }

func TestGreenWhenTheLatestRunPassed(t *testing.T) {
	h := judge(pipe(run("2026-09-29T10:00:00Z", nil), run("2026-09-28T10:00:00Z", fails)))
	if h.Failing || h.LastStatus != "success" || *h.SuccessRate != 50 {
		t.Errorf("health = %+v", h)
	}
}

func TestFailingStreakReadsThroughCancelledRuns(t *testing.T) {
	h := judge(pipe(
		run("2026-09-29T10:00:00Z", fails),
		run("2026-09-29T09:00:00Z", cancelled),
		run("2026-09-29T08:00:00Z", fails),
		run("2026-09-28T08:00:00Z", nil),
	))
	if !h.Failing || h.FailingRuns != 2 || !h.FailingFrom.Equal(ts("2026-09-29T08:00:00Z")) {
		t.Errorf("health = %+v", h)
	}
}

func TestManualRunsDoNotJudgeTheBranch(t *testing.T) {
	h := judge(pipe(run("2026-09-29T10:00:00Z", func(r *provider.Run) { r.Trigger = provider.TriggerManual; r.Conclusion = provider.Failure }), run("2026-09-28T10:00:00Z", nil)))
	if h.Failing || h.LastStatus != "success" {
		t.Errorf("a manual failure judged the branch: %+v", h)
	}
}

func scheduled(runs ...provider.Run) provider.Pipeline {
	p := pipe(runs...)
	p.Schedules = []provider.Schedule{{Cron: "0 6 * * *", TimeZone: "UTC"}}
	return p
}

func onSchedule(r *provider.Run) { r.Trigger = provider.TriggerSchedule }

func TestSchedules(t *testing.T) {
	if s := judge(scheduled(run("2026-09-29T06:04:00Z", onSchedule))).Schedule; s != "on_time" {
		t.Errorf("a daily schedule that keeps running is %s", s)
	}
	if s := judge(scheduled(run("2026-09-28T06:04:00Z", onSchedule))).Schedule; s != "on_time" {
		t.Errorf("a single late run is %s", s)
	}
	h := judge(scheduled(run("2026-09-27T06:04:00Z", onSchedule)))
	if h.Schedule != "missed" || !h.ScheduleMissedSince.Equal(ts("2026-09-28T06:00:00Z")) {
		t.Errorf("two missed fires: %+v %v", h.Schedule, h.ScheduleMissedSince)
	}
	// 06:00 in Tokyo is 21:00 UTC the day before; read as UTC both fires would look missed.
	tokyo := scheduled(run("2026-09-27T21:04:00Z", onSchedule))
	tokyo.Schedules[0].TimeZone = "Asia/Tokyo"
	if s := judge(tokyo).Schedule; s != "on_time" {
		t.Errorf("the cron is read in its own zone: %s", s)
	}
	dormant := scheduled()
	dormant.State = provider.Dormant
	if s := judge(dormant).Schedule; s != "disabled" {
		t.Errorf("a schedule the forge switched off is %s", s)
	}
	if s := judge(pipe()).Schedule; s != "none" {
		t.Errorf("no schedule is %s", s)
	}
}

func tagged(ref string, mod func(*provider.Run)) func(*provider.Run) {
	return func(r *provider.Run) {
		r.Trigger, r.Ref = provider.TriggerTag, ref
		if mod != nil {
			mod(r)
		}
	}
}

func TestReleaseBuilds(t *testing.T) {
	h := judge(pipe(
		run("2026-09-28T00:00:00Z", tagged("v2.0.0", fails)),
		run("2026-09-01T00:00:00Z", tagged("v1.0.0", nil)),
		run("2026-09-27T00:00:00Z", nil),
	))
	if !h.ReleaseFailed || h.ReleaseRef != "v2.0.0" {
		t.Errorf("the newest tag's failed build: %+v", h)
	}
	h = judge(pipe(run("2026-09-28T01:00:00Z", tagged("v2.0.0", nil)), run("2026-09-28T00:00:00Z", tagged("v2.0.0", fails))))
	if h.ReleaseFailed {
		t.Error("a retry of the same tag that passed clears it")
	}
}

func TestFlaky(t *testing.T) {
	h := judge(pipe(run("2026-09-29T10:00:00Z", nil), run("2026-09-29T09:00:00Z", fails)))
	if !h.Flaky {
		t.Error("a pass and a fail on one commit is flaky")
	}
	h = judge(pipe(run("2026-09-29T10:00:00Z", func(r *provider.Run) { r.SHA = "new" }), run("2026-09-29T09:00:00Z", fails)))
	if h.Flaky {
		t.Error("a fix on a new commit is not flaky")
	}
	old := Judge(pipe(), "main", []provider.Run{run("2026-07-01T10:00:00Z", nil), run("2026-07-01T09:00:00Z", fails)}, Release{}, now)
	if old.Flaky {
		t.Error("only the last 30 days count")
	}
}

func TestMeasuresUseTheKeptHistory(t *testing.T) {
	var history []provider.Run
	for d := 1; d <= 50; d++ {
		at := now.Add(-time.Duration(d) * 24 * time.Hour).Format(time.RFC3339)
		history = append(history, run(at, func(r *provider.Run) {
			if d > 30 && d%2 == 0 {
				r.Conclusion = provider.Failure
			}
		}))
	}
	h := Judge(pipe(), "main", history, Release{}, now)
	if h.Runs30d != 29 || *h.SuccessRate != 100 || h.TrendMonth != "improving" || *h.DurationP50 != 120 || h.Trend == nil {
		t.Errorf("runs %d rate %v trend %s p50 %v", h.Runs30d, *h.SuccessRate, h.TrendMonth, *h.DurationP50)
	}
}
