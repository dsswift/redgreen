package catalog

import (
	"math"
	"sort"
	"time"

	"github.com/dsswift/redgreen/internal/provider"
	"github.com/robfig/cron/v3"
)

// scheduleGrace is how late a scheduled run may start before it counts as
// missed: forges routinely start them late.
const scheduleGrace = 90 * time.Minute

var (
	failed = map[string]bool{provider.Failure: true, provider.TimedOut: true, provider.StartupFailure: true}
	// inconclusive conclusions say nothing about health, so a streak reads straight through them.
	inconclusive = map[string]bool{provider.Cancelled: true, provider.Skipped: true, provider.Neutral: true, provider.Stale: true, provider.ActionRequired: true}
	// branchTriggers are the runs that say whether the default branch is healthy. A manual run is an ad-hoc action.
	branchTriggers = map[string]bool{provider.TriggerPush: true, provider.TriggerSchedule: true, provider.TriggerOther: true}
)

func decisive(r provider.Run) bool {
	return r.Status == provider.StatusCompleted && r.Conclusion != "" && !inconclusive[r.Conclusion]
}

// Health is what redgreen works out about one pipeline from its runs. The
// rules and their severities are applied in Orrery.
type Health struct {
	LastStatus  string
	LastRunAt   *time.Time
	LastRunURL  string
	Failing     bool
	FailingRuns int
	FailingFrom *time.Time

	Schedule            string
	ScheduleMissedSince *time.Time
	LastScheduledRun    *time.Time

	ReleaseRef    string
	ReleaseFailed bool

	Flaky bool
	// FlakyClearsIn is how many more passes in a row clear Flaky; 0 while it is not flaky.
	FlakyClearsIn int
	Runs30d       int
	SuccessRate   *float64
	DurationP50   *float64
	TrendMonth    string
	Trend         map[string]any
}

// Judge works out a pipeline's health at now. runs are the forge's recent
// runs, newest first; history is every run redgreen kept for it, for the
// windows a forge's short list cannot cover.
func Judge(p provider.Pipeline, defaultBranch string, history []provider.Run, release Release, flaky FlakyRule, now time.Time) Health {
	var h Health
	var branch []provider.Run
	for _, r := range p.Runs {
		if r.Ref == defaultBranch && branchTriggers[r.Trigger] {
			branch = append(branch, r)
		}
	}
	if len(branch) > 0 {
		r := branch[0]
		h.LastStatus, h.LastRunAt, h.LastRunURL = lastStatus(r), &r.CreatedAt, r.URL
	}
	h.Failing, h.FailingRuns, h.FailingFrom = streak(branch)
	h.Schedule, h.ScheduleMissedSince, h.LastScheduledRun = scheduleState(p, now)
	if release.Ref != "" {
		h.ReleaseRef = release.Ref
		h.ReleaseFailed = release.Failed[p.ID]
	}
	all := merged(p.Runs, history)
	h.Flaky, h.FlakyClearsIn = flaky.judge(all, now)
	h.Runs30d, h.SuccessRate, h.DurationP50, h.TrendMonth, h.Trend = measures(all, defaultBranch, now)
	return h
}

func lastStatus(r provider.Run) string {
	switch {
	case r.Status != provider.StatusCompleted:
		return "running"
	case r.Conclusion == provider.Success:
		return "success"
	case failed[r.Conclusion]:
		return "failed"
	case r.Conclusion == provider.Cancelled:
		return "canceled"
	}
	return "other"
}

// streak is the unbroken run of failures at the head of runs, newest first.
func streak(runs []provider.Run) (bool, int, *time.Time) {
	var d []provider.Run
	for _, r := range runs {
		if decisive(r) {
			d = append(d, r)
		}
	}
	if len(d) == 0 || !failed[d[0].Conclusion] {
		return false, 0, nil
	}
	n := 0
	for n < len(d) && failed[d[n].Conclusion] {
		n++
	}
	since := d[n-1].CreatedAt
	return true, n, &since
}

// scheduleState says whether a scheduled pipeline is firing. Two expected
// runs that did not come, past the grace, make it missed.
func scheduleState(p provider.Pipeline, now time.Time) (string, *time.Time, *time.Time) {
	if len(p.Schedules) == 0 {
		return "none", nil, nil
	}
	var last *time.Time
	for _, r := range p.Runs {
		if r.Trigger == provider.TriggerSchedule {
			at := r.CreatedAt
			last = &at
			break
		}
	}
	if p.State == provider.Dormant {
		at := p.UpdatedAt
		return "disabled", &at, last
	}
	// With no scheduled run on record, the clock starts when the pipeline last changed.
	baseline := p.UpdatedAt
	if last != nil {
		baseline = *last
	}
	cutoff := now.Add(-scheduleGrace)
	var missed []time.Time
	for _, s := range p.Schedules {
		for _, t := range NextFires(s, baseline, 2) {
			if !t.After(cutoff) {
				missed = append(missed, t)
			}
		}
	}
	if len(missed) < 2 {
		return "on_time", nil, last
	}
	sort.Slice(missed, func(i, j int) bool { return missed[i].Before(missed[j]) })
	return "missed", &missed[0], last
}

// NextFires is the next count times a schedule fires after t, in its time zone.
func NextFires(s provider.Schedule, after time.Time, count int) []time.Time {
	tz := s.TimeZone
	if tz == "" {
		tz = "UTC"
	}
	sched, err := cron.ParseStandard("CRON_TZ=" + tz + " " + s.Cron)
	if err != nil {
		return nil
	}
	var out []time.Time
	t := after
	for range count {
		t = sched.Next(t)
		if t.IsZero() {
			break
		}
		out = append(out, t)
	}
	return out
}

// Release is the repository's newest release or tag that pipelines ran for,
// and which pipelines' latest run for it failed.
type Release struct {
	Ref    string
	Failed map[string]bool
}

// JudgeRelease finds the newest release or tag run across a repository's
// enabled pipelines and which of them failed it.
func JudgeRelease(pipelines []provider.Pipeline) Release {
	isRelease := func(r provider.Run) bool {
		return r.Trigger == provider.TriggerRelease || r.Trigger == provider.TriggerTag
	}
	var newest *provider.Run
	for _, p := range pipelines {
		if p.State == provider.Disabled {
			continue
		}
		for i, r := range p.Runs {
			if isRelease(r) && (newest == nil || r.CreatedAt.After(newest.CreatedAt)) {
				newest = &p.Runs[i]
			}
		}
	}
	out := Release{Failed: map[string]bool{}}
	if newest == nil {
		return out
	}
	out.Ref = newest.Ref
	for _, p := range pipelines {
		if p.State == provider.Disabled {
			continue
		}
		for _, r := range p.Runs {
			if isRelease(r) && r.Ref == out.Ref && decisive(r) {
				if failed[r.Conclusion] {
					out.Failed[p.ID] = true
				}
				break
			}
		}
	}
	return out
}

// merged is the forge's runs and the kept ones, each once.
func merged(runs, history []provider.Run) []provider.Run {
	seen := map[string]bool{}
	var out []provider.Run
	for _, list := range [][]provider.Run{runs, history} {
		for _, r := range list {
			if !seen[r.ID] {
				seen[r.ID] = true
				out = append(out, r)
			}
		}
	}
	return out
}

// FlakyRule sizes the flaky rule in runs, so it follows how often a pipeline
// runs: a busy one looks back hours, a rare one months.
type FlakyRule struct {
	// Window is how many of the newest finished runs are read for evidence.
	Window int
	// Clear is how many passes in a row, with no evidence after the first, clear the flag.
	Clear int
	// MaxAge is how old evidence may be. Past it, a pipeline that runs too
	// seldom to prove a fix stops being held by old history.
	MaxAge time.Duration
}

// DefaultFlaky reads the newest 20 finished runs, clears after 5 passes in a
// row, and drops evidence older than 180 days.
var DefaultFlaky = FlakyRule{Window: 20, Clear: 5, MaxAge: 180 * 24 * time.Hour}

// OrDefault fills each unset part from DefaultFlaky.
func (f FlakyRule) OrDefault() FlakyRule {
	if f.Window < 1 {
		f.Window = DefaultFlaky.Window
	}
	if f.Clear < 1 {
		f.Clear = DefaultFlaky.Clear
	}
	if f.MaxAge <= 0 {
		f.MaxAge = DefaultFlaky.MaxAge
	}
	return f
}

// judge says whether a pipeline is flaky at now, and how many more passes in
// a row would clear it. Evidence is a run whose result differs from the run
// before it on the same commit: that commit both passed and failed. Only the
// newest Window finished runs are read, and evidence older than MaxAge is
// dropped. The flag holds until the newest Clear runs have all passed with no
// evidence after the first of them.
func (f FlakyRule) judge(runs []provider.Run, now time.Time) (bool, int) {
	f = f.OrDefault()
	var d []provider.Run
	for _, r := range runs {
		if decisive(r) && r.SHA != "" && (r.Conclusion == provider.Success || failed[r.Conclusion]) {
			d = append(d, r)
		}
	}
	sort.SliceStable(d, func(i, j int) bool { return d[i].CreatedAt.After(d[j].CreatedAt) })
	if len(d) > f.Window {
		d = d[:f.Window]
	}
	// evidence is the index of the newest run that changed its commit's result.
	evidence := -1
	last := map[string]string{}
	for i := len(d) - 1; i >= 0; i-- {
		r := d[i]
		outcome := provider.Success
		if failed[r.Conclusion] {
			outcome = provider.Failure
		}
		if before, ok := last[r.SHA]; ok && before != outcome && now.Sub(r.CreatedAt) <= f.MaxAge {
			evidence = i
		}
		last[r.SHA] = outcome
	}
	if evidence < 0 {
		return false, 0
	}
	// A pass that is itself the evidence counts: it is a green run too.
	passes := 0
	for passes <= evidence && d[passes].Conclusion == provider.Success {
		passes++
	}
	if passes >= f.Clear {
		return false, 0
	}
	return true, f.Clear - passes
}

// trendPoints is how far a window's success rate must move against the one
// before it to count as improving or worsening.
const trendPoints = 10

// measures counts the finished default-branch runs of the last 30 days, their
// success rate, the median duration of the successful ones, the trend against
// the 30 days before, and the success rate of each of the last nine calendar
// weeks. Every window ends on a day or week boundary, so a sweep moves a
// measure only when a run lands or a day rolls, never because ten minutes
// passed.
func measures(runs []provider.Run, branch string, now time.Time) (int, *float64, *float64, string, map[string]any) {
	type tally struct {
		runs, ok  int
		durations []float64
	}
	window := func(from, to time.Time) tally {
		var t tally
		for _, r := range runs {
			if r.Ref != branch || r.Trigger == provider.TriggerPR || r.Status != provider.StatusCompleted || r.CreatedAt.Before(from) || !r.CreatedAt.Before(to) {
				continue
			}
			t.runs++
			if r.Conclusion == provider.Success {
				t.ok++
				if d := r.UpdatedAt.Sub(r.CreatedAt).Seconds(); d >= 0 {
					t.durations = append(t.durations, math.Round(d))
				}
			}
		}
		return t
	}
	pct := func(t tally) float64 { return math.Round(float64(t.ok) / float64(t.runs) * 100) }
	dayEnd := dayStart(now).AddDate(0, 0, 1)
	month, prev := window(dayEnd.AddDate(0, 0, -30), dayEnd), window(dayEnd.AddDate(0, 0, -60), dayEnd.AddDate(0, 0, -30))
	var rate, p50 *float64
	if month.runs > 0 {
		r := pct(month)
		rate = &r
	}
	if len(month.durations) > 0 {
		m := median(month.durations)
		p50 = &m
	}
	trend := ""
	if month.runs > 0 && prev.runs > 0 {
		switch diff := pct(month) - pct(prev); {
		case diff >= trendPoints:
			trend = "improving"
		case diff <= -trendPoints:
			trend = "worsening"
		default:
			trend = "steady"
		}
	}
	// The current week is its own bucket, however far it has run, and each
	// point sits at its week's start.
	week := weekStart(now)
	var points []map[string]any
	for w := trendWeeks - 1; w >= 0; w-- {
		start := week.AddDate(0, 0, -7*w)
		t := window(start, start.AddDate(0, 0, 7))
		if t.runs == 0 {
			continue
		}
		points = append(points, map[string]any{"at": start.Format(time.RFC3339), "value": pct(t)})
	}
	var series map[string]any
	if len(points) > 0 {
		series = map[string]any{"points": points, "min": 0, "max": 100, "unit": "%", "from": week.AddDate(0, 0, -7*(trendWeeks-1)).Format(time.RFC3339), "to": week.Format(time.RFC3339)}
	}
	return month.runs, rate, p50, trend, series
}

// trendWeeks is how many calendar weeks the success trend holds, the current
// one included.
const trendWeeks = 9

// dayStart is 00:00 UTC on the day of t.
func dayStart(t time.Time) time.Time {
	y, m, d := t.UTC().Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// weekStart is 00:00 UTC on the Monday of t's week.
func weekStart(t time.Time) time.Time {
	d := dayStart(t)
	return d.AddDate(0, 0, -((int(d.Weekday()) + 6) % 7))
}

func median(xs []float64) float64 {
	s := append([]float64(nil), xs...)
	sort.Float64s(s)
	mid := len(s) / 2
	if len(s)%2 == 1 {
		return s[mid]
	}
	return math.Round((s[mid-1] + s[mid]) / 2)
}
