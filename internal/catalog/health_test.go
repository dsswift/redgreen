package catalog

import (
	"reflect"
	"strconv"
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
	return Judge(p, "main", nil, JudgeRelease([]provider.Pipeline{p}), DefaultFlaky, now)
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
	if !h.Flaky || h.FlakyClearsIn != 4 {
		t.Errorf("a pass and a fail on one commit is flaky, 4 more passes from clear: %v %d", h.Flaky, h.FlakyClearsIn)
	}
	h = judge(pipe(run("2026-09-29T10:00:00Z", func(r *provider.Run) { r.SHA = "new" }), run("2026-09-29T09:00:00Z", fails)))
	if h.Flaky || h.FlakyClearsIn != 0 {
		t.Error("a fix on a new commit is not flaky")
	}
}

// cadence is a pipeline's finished runs, one every step and the newest one
// step before now, newest first. Each letter is a run: P passed, F failed. A
// lower-case letter ran on its own new commit; an upper-case one ran again on
// the commit of the run before it.
func cadence(step time.Duration, newestFirst string) []provider.Run {
	n := len(newestFirst)
	runs := make([]provider.Run, n)
	sha := ""
	for i := n - 1; i >= 0; i-- {
		c := newestFirst[i]
		if sha == "" || c == 'p' || c == 'f' {
			sha = "commit-" + strconv.Itoa(i)
		}
		at := now.Add(-time.Duration(i+1) * step)
		runs[i] = run(at.Format(time.RFC3339Nano), func(r *provider.Run) {
			r.SHA = sha
			if c == 'f' || c == 'F' {
				r.Conclusion = provider.Failure
			}
		})
	}
	return runs
}

// TestFlakyFollowsRunsNotDays checks one rule fits every cadence: the flag
// counts runs since a commit last changed its result, whatever the days.
func TestFlakyFollowsRunsNotDays(t *testing.T) {
	hour, day := time.Hour, 24*time.Hour
	cases := []struct {
		name     string
		step     time.Duration
		runs     string
		flaky    bool
		clearsIn int
	}{
		// An hourly pipeline: a commit failed 6 hours ago, passed on a second try, and 4 more runs passed.
		{"hourly, cleared by 5 passes in 5 hours", hour, "ppppPf", false, 0},
		{"hourly, 3 passes so far: 2 more clear it", hour, "ppPf", true, 2},
		// The flake is a day old: 21 runs back, outside the 20 read.
		{"hourly, evidence older than 20 runs", hour, "fffffffffffffffffffpPf", false, 0},
		// A nightly schedule on one unchanged commit: one bad night, 7 good nights since.
		{"nightly on one commit, 7 good nights since", day, "PPPPPPPFp", false, 0},
		{"nightly on one commit, 4 good nights since", day, "PPPPFp", true, 1},
		{"every 12 days, 36 days after the evidence but only 3 passes", 12 * day, "ppPf", true, 2},
		// A pipeline that runs four times a year: 30 days would have forgotten this.
		{"quarterly, evidence 90 days old still counts", 90 * day, "Pf", true, 4},
		{"quarterly, one pass since the evidence", 80 * day, "pPf", true, 3},
		{"quarterly, evidence past 180 days is dropped", 90 * day, "ppPf", false, 0},
		// A failure after the evidence starts the count again.
		{"a new failure restarts the count", hour, "ppfppppPf", true, 3},
		{"failing again on the same commit is new evidence", hour, "FPf", true, 5},
		{"failures on different commits are not flaky", hour, "pfpfpf", false, 0},
	}
	for _, c := range cases {
		h := Judge(pipe(), "main", cadence(c.step, c.runs), Release{}, DefaultFlaky, now)
		if h.Flaky != c.flaky || h.FlakyClearsIn != c.clearsIn {
			t.Errorf("%s: flaky %v clears in %d, want %v %d", c.name, h.Flaky, h.FlakyClearsIn, c.flaky, c.clearsIn)
		}
	}
}

func TestFlakyRuleTakesItsNumbers(t *testing.T) {
	runs := cadence(time.Hour, "ppPf")
	h := Judge(pipe(), "main", runs, Release{}, FlakyRule{Window: 20, Clear: 2, MaxAge: 24 * time.Hour}, now)
	if h.Flaky {
		t.Error("2 passes clear it when the rule says 2")
	}
	h = Judge(pipe(), "main", runs, Release{}, FlakyRule{Window: 3}, now)
	if h.Flaky {
		t.Error("a window of 3 runs does not reach evidence 4 runs back")
	}
	h = Judge(pipe(), "main", runs, Release{}, FlakyRule{}, now)
	if !h.Flaky || h.FlakyClearsIn != 2 {
		t.Errorf("an unset rule takes the defaults: %v %d", h.Flaky, h.FlakyClearsIn)
	}
}

func TestFlakyReadsThroughUnfinishedAndCancelledRuns(t *testing.T) {
	h := judge(pipe(
		run("2026-09-29T11:00:00Z", func(r *provider.Run) { r.Status, r.Conclusion, r.SHA = "in_progress", "", "next" }),
		run("2026-09-29T10:30:00Z", func(r *provider.Run) { r.Conclusion, r.SHA = provider.Cancelled, "next" }),
		run("2026-09-29T10:00:00Z", nil),
		run("2026-09-29T09:00:00Z", fails),
	))
	if !h.Flaky || h.FlakyClearsIn != 4 {
		t.Errorf("only finished passes and failures count: %v %d", h.Flaky, h.FlakyClearsIn)
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
	h := Judge(pipe(), "main", history, Release{}, DefaultFlaky, now)
	if h.Runs30d != 29 || *h.SuccessRate != 100 || h.TrendMonth != "improving" || *h.DurationP50 != 120 || h.Trend == nil {
		t.Errorf("runs %d rate %v trend %s p50 %v", h.Runs30d, *h.SuccessRate, h.TrendMonth, *h.DurationP50)
	}
}

// TestMeasuresHoldStillBetweenSweeps checks that a sweep ten minutes after
// another, with the same runs, writes the same measures: a run ageing past a
// window's edge in between must not move them.
func TestMeasuresHoldStillBetweenSweeps(t *testing.T) {
	history := []provider.Run{
		run("2026-09-29T09:00:00Z", nil),
		run("2026-09-28T00:05:00Z", fails),
		run("2026-09-27T23:55:00Z", nil),
		run("2026-09-14T08:00:00Z", fails),
		run(now.Add(-30*24*time.Hour+5*time.Minute).Format(time.RFC3339), fails),
		run(now.Add(-60*24*time.Hour+5*time.Minute).Format(time.RFC3339), nil),
		run("2026-08-03T08:00:00Z", nil),
	}
	first := Judge(pipe(), "main", history, Release{}, DefaultFlaky, now)
	second := Judge(pipe(), "main", history, Release{}, DefaultFlaky, now.Add(10*time.Minute))
	if !reflect.DeepEqual(first.Trend, second.Trend) {
		t.Errorf("success_trend moved between sweeps:\n%v\n%v", first.Trend, second.Trend)
	}
	if first.Runs30d != second.Runs30d || *first.SuccessRate != *second.SuccessRate || first.TrendMonth != second.TrendMonth || *first.DurationP50 != *second.DurationP50 {
		t.Errorf("30-day measures moved between sweeps: %d/%d runs, %v/%v rate, %s/%s trend", first.Runs30d, second.Runs30d, *first.SuccessRate, *second.SuccessRate, first.TrendMonth, second.TrendMonth)
	}
}

// TestSuccessTrendBucketsByCalendarWeek checks each point is a week from
// Monday 00:00 UTC, the current week its own bucket, and sits at its start.
func TestSuccessTrendBucketsByCalendarWeek(t *testing.T) {
	history := []provider.Run{
		run("2026-09-29T09:00:00Z", nil),
		run("2026-09-28T00:05:00Z", fails),
		run("2026-09-27T23:55:00Z", nil),
		run("2026-09-21T00:00:00Z", fails),
	}
	h := Judge(pipe(), "main", history, Release{}, DefaultFlaky, now)
	points := h.Trend["points"].([]map[string]any)
	want := []map[string]any{{"at": "2026-09-21T00:00:00Z", "value": 50.0}, {"at": "2026-09-28T00:00:00Z", "value": 50.0}}
	if !reflect.DeepEqual(points, want) {
		t.Errorf("points %v, want %v", points, want)
	}
	if h.Trend["from"] != "2026-08-03T00:00:00Z" || h.Trend["to"] != "2026-09-28T00:00:00Z" {
		t.Errorf("axis %v to %v", h.Trend["from"], h.Trend["to"])
	}
}
