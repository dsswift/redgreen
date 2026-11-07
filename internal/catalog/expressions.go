package catalog

import "strings"

// Every calculation redgreen offers, in Orrery's expression language. They
// live here and nowhere else, so scripts/check-expressions.mts can compile and
// evaluate each one with Orrery's own compiler.
//
// A calculation sees the fields before it in its blueprint, so the order of
// Calculations is the order they are offered in.

// Severities a rule takes. Info shows the rule without colouring anything; off silences it.
var Severities = []string{"red", "amber", "info", "off"}

// Rule is one reason a pipeline or a repository is not green.
type Rule struct {
	// Field is the boolean fact or calculation that says the rule holds.
	Field    string
	Severity string
	Title    string
	Help     string
	// Calculation computes the field in Orrery; empty when redgreen pushes it.
	Calculation string
}

// level is the calculation of a rule's level: its severity while it holds and is not off.
func (r Rule) level() string {
	return r.Field + " == true && " + r.Field + "_severity != 'off' ? " + r.Field + "_severity : null"
}

// PipelineRules are the rules on sw_pipeline.
var PipelineRules = []Rule{
	{Field: "default_branch_failing", Severity: "red", Title: "Default branch failing", Help: "The latest finished automatic run on the default branch failed. Manual runs do not count."},
	{Field: "schedule_stale", Severity: "amber", Title: "Schedule stopped", Help: "The schedule missed its runs, or the forge switched it off for inactivity.",
		Calculation: "schedule_state == 'missed' || schedule_state == 'disabled'"},
	{Field: "release_build_failed", Severity: "red", Title: "Release build failed", Help: "The run for the repository's newest release or tag failed."},
	{Field: "flaky", Severity: "amber", Title: "Flaky", Help: "In the last 30 days one commit both passed and failed."},
}

// RepositoryRules are the rules on sw_repository. Its pipelines' rules reach it through their status.
var RepositoryRules = []Rule{
	{Field: "critical_alerts", Severity: "red", Title: "Critical alerts open", Help: "Open critical dependency or code scanning alerts.",
		Calculation: "coalesce(dependabot_critical, 0) + coalesce(code_scanning_critical, 0) > 0"},
	{Field: "pr_checks_failing", Severity: "red", Title: "Pull request checks failing", Help: "An open, ready-for-review pull or merge request has failing checks.",
		Calculation: "coalesce(prs_failing_checks, 0) > 0"},
	{Field: "archive_candidate", Severity: "amber", Title: "Archive candidate", Help: "Idle 90 days or more, and red.",
		Calculation: "coalesce(days_idle, 0) >= 90 && (critical_alerts_level == 'red' || pr_checks_failing_level == 'red' || pipelines_red > 0)"},
}

func levels(rules []Rule) string {
	var parts []string
	for _, r := range rules {
		parts = append(parts, r.Field+"_level")
	}
	return "list(" + strings.Join(parts, ", ") + ")"
}

// Pipeline calculations, in order.
var (
	pipelineStatus = "muted == true || state == 'disabled' ? 'grey' : contains(" + levels(PipelineRules) + ", 'red') ? 'red' : contains(" + levels(PipelineRules) + ", 'amber') ? 'amber' : 'green'"
	pipelineReason = "status == 'grey' ? (muted == true ? 'muted' : 'disabled') : status == 'green' ? null" +
		" : default_branch_failing_level == status ? name + ' failing on ' + default_branch" +
		" : schedule_stale_level == status ? (schedule_state == 'disabled' ? name + ' schedule switched off by the forge' : name + ' schedule stopped')" +
		" : release_build_failed_level == status ? name + ' build failed for release ' + coalesce(release_ref, '')" +
		" : name + ' is flaky'"
)

// Repository calculations, in order.
var (
	openAlerts       = "coalesce(dependabot_critical, 0) + coalesce(dependabot_high, 0) + coalesce(code_scanning_critical, 0) + coalesce(code_scanning_high, 0)"
	daysIdle         = "days_since(last_push)"
	repositoryStatus = "muted == true || archived == true ? 'grey'" +
		" : contains(" + levels(RepositoryRules) + ", 'red') || pipelines_red > 0 ? 'red'" +
		" : contains(" + levels(RepositoryRules) + ", 'amber') || pipelines_amber > 0 ? 'amber'" +
		" : pipelines_active > 0 ? 'green' : 'grey'"
	repositoryReason = "status == 'grey' ? (muted == true ? 'muted' : archived == true ? 'archived' : 'no pipelines') : status == 'green' ? null" +
		" : archive_candidate_level == status ? 'idle ' + days_idle + ' days and red: a candidate to archive'" +
		" : critical_alerts_level == status ? (coalesce(dependabot_critical, 0) + coalesce(code_scanning_critical, 0)) + ' critical alerts open'" +
		" : pr_checks_failing_level == status ? prs_failing_checks + (prs_failing_checks == 1 ? ' pull request' : ' pull requests') + ' failing checks'" +
		" : status == 'red' ? pipeline_red_reason : pipeline_amber_reason"
)

// DORA calculations. The bands are the DORA report's: deploys per day at
// least 1, 1/7, 1/30; lead time under a day, a week, 30 days; change failure
// rate at most 5, 10, 15 percent; restore under an hour, a day, a week. Each
// band reads counts, not the rounded rate, so a band edge is exact.
var (
	changeFailureRate     = "dora_changes_30d > 0 ? round(dora_failed_changes_30d / dora_changes_30d * 100, 1) : null"
	changeFailureRatePrev = "dora_changes_prev_30d > 0 ? round(dora_failed_changes_prev_30d / dora_changes_prev_30d * 100, 1) : null"
	frequencyBand         = "dora_changes_30d > 0 ? (dora_deployments_30d >= 30 ? 'elite' : dora_deployments_30d * 7 >= 30 ? 'high' : dora_deployments_30d >= 1 ? 'medium' : 'low') : null"
	leadTimeBand          = "dora_lead_time_p50_hours == null ? null : dora_lead_time_p50_hours < 24 ? 'elite' : dora_lead_time_p50_hours < 168 ? 'high' : dora_lead_time_p50_hours < 720 ? 'medium' : 'low'"
	changeFailureBand     = "dora_changes_30d > 0 ? (dora_failed_changes_30d * 20 <= dora_changes_30d ? 'elite' : dora_failed_changes_30d * 10 <= dora_changes_30d ? 'high' : dora_failed_changes_30d * 100 <= dora_changes_30d * 15 ? 'medium' : 'low') : null"
	restoreBand           = "dora_time_to_restore_p50_hours == null ? null : dora_time_to_restore_p50_hours < 1 ? 'elite' : dora_time_to_restore_p50_hours < 24 ? 'high' : dora_time_to_restore_p50_hours < 168 ? 'medium' : 'low'"
	bands                 = "list(dora_frequency_band, dora_lead_time_band, dora_change_failure_band, dora_restore_band)"
	doraGrade             = "dora_frequency_band == null ? null : contains(" + bands + ", 'low') ? 'low' : contains(" + bands + ", 'medium') ? 'medium' : contains(" + bands + ", 'high') ? 'high' : 'elite'"
)

// Calculation is one calculated field, named by blueprint and field.
type Calculation struct {
	Blueprint, Field, Source string
}

// Calculations lists every calculation redgreen offers, in the order each
// blueprint offers them.
func Calculations() []Calculation {
	var out []Calculation
	for _, bp := range Blueprints() {
		id, _ := bp["id"].(string)
		fields, _ := bp["fields"].(Fields)
		for _, f := range fields {
			if c, ok := f.Def["calculation"].(string); ok {
				out = append(out, Calculation{Blueprint: id, Field: f.Name, Source: c})
			}
		}
	}
	return out
}
