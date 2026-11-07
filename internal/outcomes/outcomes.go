// Package outcomes works out what each deployment says about delivery: its
// lead time, the commits it carried, whether it only fixed, whether it was a
// failed change, and how long its restore took. Only these need a forge call
// or neighbouring events. Orrery aggregates them per service and per team and
// grades them; nothing here sums or bands.
package outcomes

import (
	"math"
	"regexp"
	"sort"
	"time"

	"github.com/dsswift/redgreen/internal/catalog"
	"github.com/dsswift/redgreen/internal/provider"
)

// followWithin is how soon after a deployment a fix-only deployment, or an
// incident, makes it a failed change.
const followWithin = 24 * time.Hour

// Record is one deployment that reached an end, from a forge or from Argo CD.
type Record struct {
	// Key is the entity's key on the hub.
	Key string `json:"key"`
	// Source is forge or argo.
	Source  string `json:"source"`
	RepoKey string `json:"repoKey"`
	// Folder is the service's folder in the repository, "" for the whole of it.
	Folder      string    `json:"folder"`
	Environment string    `json:"environment"`
	Status      string    `json:"status"`
	Revision    string    `json:"revision"`
	FinishedAt  time.Time `json:"finishedAt"`
	// Elsewhere is set when the revision is a commit in another repository,
	// such as a deployment manifest; the repository's release tags stand in for it.
	Elsewhere bool `json:"elsewhere,omitempty"`
}

// Stream is the line of deployments one record belongs to.
func (r Record) Stream() string { return r.RepoKey + "\n" + r.Folder + "\n" + r.Environment }

// Carried is what a deployment carried, once read. Unknown is set when the
// forge could not say.
type Carried struct {
	Base    string            `json:"base"`
	Commits []provider.Commit `json:"commits"`
	Unknown bool              `json:"unknown,omitempty"`
}

// Incident is something that went wrong in production, from Orrery's it_incident.
type Incident struct {
	RepoKey     string
	Folder      string
	Severity    string
	OpenedAt    time.Time
	MitigatedAt *time.Time
	ResolvedAt  *time.Time
}

var (
	mergeTitle  = regexp.MustCompile(`(?i)^merge `)
	repairTitle = regexp.MustCompile(`(?i)^(fix|revert)(\(.*?\))?!?:|^revert\b`)
)

// IsRepair says whether a commit only repairs: a conventional fix or revert,
// or a forge's revert. A merge commit says nothing either way (ok is false).
func IsRepair(title string) (repair, ok bool) {
	if mergeTitle.MatchString(title) {
		return false, false
	}
	return repairTitle.MatchString(title), true
}

func round1(x float64) float64 { return math.Round(x*10) / 10 }

// Carries is the lead time, commit count, and fix-only flag of one deployment
// from the commits it carried. Each is nil when it cannot be told.
func Carries(r Record, c *Carried) (lead *float64, count *int, fixOnly *bool) {
	if c == nil || c.Unknown {
		return nil, nil, nil
	}
	n := len(c.Commits)
	count = &n
	var hours []float64
	judged, repairs := 0, 0
	for _, cm := range c.Commits {
		if h := r.FinishedAt.Sub(cm.AuthoredAt).Hours(); h >= 0 {
			hours = append(hours, h)
		}
		if repair, ok := IsRepair(cm.Title); ok {
			judged++
			if repair {
				repairs++
			}
		}
	}
	if len(hours) > 0 {
		sort.Float64s(hours)
		m := hours[len(hours)/2]
		if len(hours)%2 == 0 {
			m = (hours[len(hours)/2-1] + hours[len(hours)/2]) / 2
		}
		m = round1(m)
		lead = &m
	}
	if judged > 0 {
		all := judged == repairs
		fixOnly = &all
	}
	return lead, count, fixOnly
}

// Classify works out every record's outcome. A deployment is a failed change
// when it failed, when a fix-only deployment of the same stream followed it
// within a day, or when a critical or warning incident opened on its service
// within a day after it. Its restore is the next good deployment, the fix, or
// the incident's mitigation, else its resolution.
func Classify(records []Record, carried map[string]*Carried, incidents []Incident) map[string]catalog.Outcome {
	streams := map[string][]Record{}
	for _, r := range records {
		streams[r.Stream()] = append(streams[r.Stream()], r)
	}
	out := map[string]catalog.Outcome{}
	for _, list := range streams {
		sort.SliceStable(list, func(i, j int) bool { return list[i].FinishedAt.Before(list[j].FinishedAt) })
		fixes := make([]*bool, len(list))
		for i, r := range list {
			o := catalog.Outcome{}
			o.LeadTimeHours, o.CommitCount, o.FixOnly = Carries(r, carried[r.Key])
			fixes[i] = o.FixOnly
			out[r.Key] = o
		}
		for i, r := range list {
			o := out[r.Key]
			restored := func(at time.Time) {
				h := round1(at.Sub(r.FinishedAt).Hours())
				o.RestoreHours = &h
			}
			switch {
			case r.Status == provider.DeployFailed:
				o.FailedChange = true
				for _, next := range list[i+1:] {
					if next.Status == provider.DeploySucceeded {
						restored(next.FinishedAt)
						break
					}
				}
			case repairAfter(list, fixes, i) >= 0:
				o.FailedChange = true
				restored(list[repairAfter(list, fixes, i)].FinishedAt)
			default:
				if inc := incidentAfter(r, incidents); inc != nil {
					o.FailedChange = true
					if inc.MitigatedAt != nil {
						restored(*inc.MitigatedAt)
					} else if inc.ResolvedAt != nil {
						restored(*inc.ResolvedAt)
					}
				}
			}
			out[r.Key] = o
		}
	}
	return out
}

// repairAfter is the index of a fix-only deployment within a day after list[i], or -1.
func repairAfter(list []Record, fixes []*bool, i int) int {
	for j := i + 1; j < len(list); j++ {
		if list[j].FinishedAt.Sub(list[i].FinishedAt) > followWithin {
			break
		}
		if list[j].Status == provider.DeploySucceeded && fixes[j] != nil && *fixes[j] {
			return j
		}
	}
	return -1
}

// incidentAfter is the first critical or warning incident on the record's service opened within a day after it.
func incidentAfter(r Record, incidents []Incident) *Incident {
	var best *Incident
	for i, inc := range incidents {
		if inc.RepoKey != r.RepoKey || inc.Folder != r.Folder || (inc.Severity != "critical" && inc.Severity != "warning") {
			continue
		}
		gap := inc.OpenedAt.Sub(r.FinishedAt)
		if gap < 0 || gap > followWithin {
			continue
		}
		if best == nil || inc.OpenedAt.Before(best.OpenedAt) {
			best = &incidents[i]
		}
	}
	return best
}

// Range is the revisions whose commits a deployment carried.
type Range struct {
	// Base is "" for the first known deployment of its stream.
	Base, Head string
}

// RangeOf finds what to compare for one record: from the previous successful
// deployment of its stream to it. A revision in another repository is stood in
// for by the newest release published by then. ok is false when no release stands in.
func RangeOf(r Record, records []Record, releases []provider.Release) (Range, bool) {
	revision := func(x Record) (string, bool) {
		if !x.Elsewhere {
			return x.Revision, true
		}
		tag := StandIn(releases, x.FinishedAt)
		return tag, tag != ""
	}
	head, ok := revision(r)
	if !ok {
		return Range{}, false
	}
	var prev *Record
	for i, x := range records {
		if x.Key != r.Key && x.Stream() == r.Stream() && x.Status == provider.DeploySucceeded && x.FinishedAt.Before(r.FinishedAt) && (prev == nil || x.FinishedAt.After(prev.FinishedAt)) {
			prev = &records[i]
		}
	}
	if prev == nil {
		return Range{Head: head}, true
	}
	base, ok := revision(*prev)
	if !ok {
		return Range{Head: head}, true
	}
	return Range{Base: base, Head: head}, true
}

// StandIn is the tag of the newest full release published at or before at.
func StandIn(releases []provider.Release, at time.Time) string {
	var best *provider.Release
	for i, r := range releases {
		if r.Prerelease || r.PublishedAt.After(at) {
			continue
		}
		if best == nil || r.PublishedAt.After(best.PublishedAt) {
			best = &releases[i]
		}
	}
	if best == nil {
		return ""
	}
	return best.Tag
}
