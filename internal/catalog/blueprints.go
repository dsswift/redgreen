// Package catalog is what redgreen writes to Orrery: the blueprints it
// offers, and the mapping from what a forge said to the entities written.
// Health and DORA are calculations and aggregations in the blueprints, so
// Orrery computes them when a page is read and a rule change needs no resync.
package catalog

import (
	"bytes"
	"encoding/json"

	"github.com/dsswift/orrery/sdk"
	"github.com/dsswift/redgreen/internal/services"
)

// Field is one field definition, in the order it is offered.
type Field struct {
	Name string
	Def  map[string]any
}

// Fields keeps its order on the wire: a calculation sees only the fields before it.
type Fields []Field

// MarshalJSON writes the fields as one object in their order.
func (fs Fields) MarshalJSON() ([]byte, error) {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, f := range fs {
		if i > 0 {
			b.WriteByte(',')
		}
		k, _ := json.Marshal(f.Name)
		v, err := json.Marshal(f.Def)
		if err != nil {
			return nil, err
		}
		b.Write(k)
		b.WriteByte(':')
		b.Write(v)
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

// Has says whether a field is defined.
func (fs Fields) Has(name string) bool {
	for _, f := range fs {
		if f.Name == name {
			return true
		}
	}
	return false
}

func f(name string, def map[string]any) Field { return Field{Name: name, Def: def} }

func typed(t string, extra ...any) map[string]any {
	m := map[string]any{"type": t}
	for i := 0; i+1 < len(extra); i += 2 {
		m[extra[i].(string)] = extra[i+1]
	}
	return m
}

func eq(field string, value any) map[string]any {
	return map[string]any{"field": field, "op": "eq", "value": value}
}

func agg(relation, fn, field string, where map[string]any, window map[string]any, per string) map[string]any {
	a := map[string]any{"relation": relation, "fn": fn}
	if field != "" {
		a["field"] = field
	}
	if where != nil {
		a["where"] = where
	}
	if window != nil {
		a["window"] = window
	}
	if per != "" {
		a["per"] = per
	}
	return a
}

func window(field, last, offset string) map[string]any {
	w := map[string]any{"field": field, "last": last}
	if offset != "" {
		w["offset"] = offset
	}
	return w
}

// Blueprint ids.
const (
	Repository = "sw_repository"
	Pipeline   = "sw_pipeline"
	Service    = "sw_service"
	API        = "sw_api"
	Deployment = "sw_deployment"
	Issue      = "sw_issue"
	Team       = "team"
)

var levelValues = []string{"red", "amber", "green", "grey"}

func rules(rs []Rule) Fields {
	var out Fields
	for _, r := range rs {
		if r.Calculation != "" {
			out = append(out, f(r.Field, typed("boolean", "title", r.Title, "description", r.Help, "calculation", r.Calculation)))
		}
		out = append(out, f(r.Field+"_level", typed("enum", "values", Severities[:3], "title", r.Title+" level", "description", "The rule's severity while it holds; empty while it does not or is off.", "calculation", r.level())))
	}
	return out
}

func severities(rs []Rule) Fields {
	var out Fields
	for _, r := range rs {
		out = append(out, f(r.Field+"_severity", typed("enum", "values", Severities, "default", r.Severity, "title", r.Title+" severity",
			"description", "How much "+r.Title+" counts: red, amber, info, or off. The blueprint default sets it for every one; set it on one to override.")))
	}
	return out
}

func status(calc, reason string) Fields {
	return Fields{
		f("status", typed("enum", "values", levelValues, "overridable", false, "description", "Red, amber, green, or grey, worked out from the rules and their severities.", "calculation", calc)),
		f("status_reason", typed("string", "overridable", false, "description", "Why it is not green.", "calculation", reason)),
	}
}

func join(parts ...Fields) Fields {
	var out Fields
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

// PipelineBlueprint is one workflow, build definition, or schedule. Runs stay on the forge.
func PipelineBlueprint() sdk.Blueprint {
	var facts Fields
	for _, r := range PipelineRules {
		if r.Calculation == "" {
			facts = append(facts, f(r.Field, typed("boolean", "title", r.Title, "description", r.Help)))
		}
	}
	return sdk.Blueprint{
		"id": Pipeline, "title": "Pipeline", "group": "Operations", "icon": "pipeline", "formerly": []string{"pipeline"},
		"description": "A CI or release pipeline: a workflow, a build definition, or a schedule, with its latest result and how healthy it is. Its runs stay in the system that runs them.",
		"status":      map[string]any{"field": "status"},
		"fields": join(Fields{
			f("url", typed("url", "title", "Pipeline")),
			f("system", typed("string", "chip", true, "description", "The forge that runs it: github, gitlab, or azuredevops.")),
			f("name", typed("string", "description", "The pipeline's name on the forge.")),
			f("path", typed("string", "description", "The file that defines it.")),
			f("default_branch", typed("string")),
			f("state", typed("enum", "values", []string{"enabled", "disabled", "dormant"}, "description", "Disabled by a person, or dormant: switched off by the forge for inactivity.")),
			f("last_status", typed("enum", "values", []string{"success", "failed", "canceled", "running", "other"}, "description", "The newest automatic run on the default branch.")),
			f("last_run_at", typed("datetime")),
			f("last_run_url", typed("url", "title", "Last run")),
			f("failing_runs", typed("number", "description", "Runs in the failing streak on the default branch.")),
			f("failing_since", typed("datetime", "description", "When the oldest run of the streak started, as far back as the forge's history reaches.")),
			f("schedule_state", typed("enum", "values", []string{"none", "on_time", "missed", "disabled"}, "description", "None for a pipeline with no schedule; missed after two expected runs did not come; disabled when the forge switched it off.")),
			f("schedule_missed_since", typed("datetime")),
			f("last_scheduled_run_at", typed("datetime")),
			f("release_ref", typed("string", "description", "The newest release or tag the repository's pipelines ran for.")),
			f("runs_30d", typed("number", "title", "Runs (30d)")),
			f("success_rate_30d", typed("number", "title", "Success rate (30d, %)")),
			f("duration_p50_seconds", typed("number", "title", "Median duration (s)", "description", "Of successful runs in the last 30 days.")),
			f("trend_month", typed("enum", "values", []string{"improving", "steady", "worsening"}, "description", "The last 30 days' success rate against the 30 before, by 10 points or more.")),
			f("success_trend", typed("trend", "title", "Success rate (60d)", "description", "The weekly success rate of default-branch runs over 60 days.")),
			f("muted", typed("boolean", "default", false, "description", "A muted pipeline is grey and colours nothing.")),
			f("repository_muted", typed("boolean", "title", "Repository muted", "mirror", "repository.muted", "description", "A pipeline of a muted repository is grey too.")),
		}, facts, severities(PipelineRules), rules(PipelineRules), status(pipelineStatus, pipelineReason)),
		"relations": map[string]any{"repository": map[string]any{"target": Repository, "many": false, "title": "Repository", "inverseTitle": "Pipelines"}},
	}
}

const fromPipelines = "<-" + Pipeline + ":repository"

// RepositoryBlueprint is a repository on a forge, with its health.
func RepositoryBlueprint() sdk.Blueprint {
	notGrey := map[string]any{"field": "status", "op": "neq", "value": "grey"}
	return sdk.Blueprint{
		"id": Repository, "title": "Repository", "group": "Catalog", "icon": "repository", "formerly": []string{"repository"},
		"description": "A source repository on a forge, keyed <forge>/<owner>/<name>: language, branch protection, last push, alert counts, and health from its own rules and its pipelines.",
		"status":      map[string]any{"field": "status"},
		"fields": join(Fields{
			f("description", typed("string")),
			f("url", typed("url", "title", "Repository")),
			f("provider", typed("string", "chip", true, "description", "The forge: github, gitlab, or azuredevops.")),
			f("organization", typed("string", "chip", true, "description", "The account or group the repository lives under.")),
			f("name", typed("string", "description", "The repository name without its organization.")),
			f("language", typed("enum", "values", services.Languages, "sticky", true, "description", "The language with the largest share of its code. A best guess: a person who knows better changes it, and that change stays.")),
			f("visibility", typed("enum", "values", []string{"public", "private", "internal"})),
			f("archived", typed("boolean")),
			f("fork", typed("boolean")),
			f("topics", typed("array")),
			f("default_branch", typed("string")),
			f("branch_protected", typed("boolean", "title", "Is branch protected")),
			f("requires_code_review", typed("boolean", "title", "Requires code review")),
			f("required_approvals", typed("number")),
			f("requires_code_owner_review", typed("boolean", "title", "Require code owner review")),
			f("has_codeowners", typed("boolean", "title", "Has CODEOWNERS")),
			f("has_readme", typed("boolean", "title", "Has README")),
			f("has_dockerfile", typed("boolean", "title", "Has Dockerfile")),
			f("has_ci", typed("boolean", "title", "Has CI")),
			f("last_push", typed("datetime")),
			f("last_committer", typed("string", "description", "Who made the newest commit on the default branch, as the forge names them.")),
			f("last_committer_email", typed("string", "title", "Last committer email", "description", "The address on that commit, lowercase. It may be a personal or made-up address.")),
			f("last_commit_at", typed("datetime")),
			f("open_prs", typed("number", "title", "Open PRs")),
			f("stale_prs", typed("number", "title", "Stale PRs (7d+)")),
			f("prs_failing_checks", typed("number", "title", "PRs failing checks", "description", "Open pull requests, not drafts, whose checks failed.")),
			f("open_issues", typed("number")),
			f("stars", typed("number")),
			f("pipelines", typed("number", "description", "Pipelines the forge lists, disabled ones included.")),
			f("last_release", typed("string")),
			f("last_release_at", typed("datetime")),
			f("dependabot_critical", typed("number", "title", "Dependency alerts critical")),
			f("dependabot_high", typed("number", "title", "Dependency alerts high")),
			f("code_scanning_critical", typed("number", "title", "Code scanning critical")),
			f("code_scanning_high", typed("number", "title", "Code scanning high")),
			f("notes", typed("markdown")),
			f("muted", typed("boolean", "default", false, "description", "A muted repository is grey and colours nothing.")),
			f("pipelines_active", typed("number", "description", "Its pipelines that are neither muted nor disabled.", "aggregation", agg(fromPipelines, "count", "", notGrey, nil, ""))),
			f("pipelines_red", typed("number", "aggregation", agg(fromPipelines, "count", "", eq("status", "red"), nil, ""))),
			f("pipelines_amber", typed("number", "aggregation", agg(fromPipelines, "count", "", eq("status", "amber"), nil, ""))),
			f("pipeline_red_reason", typed("string", "description", "Why one of its red pipelines is red.", "aggregation", agg(fromPipelines, "max", "status_reason", eq("status", "red"), nil, ""))),
			f("pipeline_amber_reason", typed("string", "description", "Why one of its amber pipelines is amber.", "aggregation", agg(fromPipelines, "max", "status_reason", eq("status", "amber"), nil, ""))),
			f("open_alerts", typed("number", "title", "Total open alerts", "calculation", openAlerts)),
		}, severities(RepositoryRules), repositoryRules(), status(repositoryStatus, repositoryReason)),
		"relations": map[string]any{},
	}
}

// repositoryRules puts days_idle before the rule that reads it.
func repositoryRules() Fields {
	out := rules(RepositoryRules[:2])
	out = append(out, f("days_idle", typed("number", "title", "Days idle", "description", "Days since the last push.", "calculation", daysIdle)))
	return append(out, rules(RepositoryRules[2:])...)
}

// DORA is the four measures over one relation path, for a service or a team.
func DORA(relation string) Fields {
	ok := eq("status", "succeeded")
	ended := map[string]any{"field": "status", "op": "in", "value": []string{"succeeded", "failed"}}
	failed := eq("failed_change", true)
	now, before := window("deployed_at", "30d", ""), window("deployed_at", "30d", "30d")
	n := func(name, title, help string, a map[string]any) Field {
		return f(name, typed("number", "title", title, "description", help, "aggregation", a))
	}
	c := func(name, t, title, help, calc string, extra ...any) Field {
		return f(name, typed(t, append([]any{"title", title, "description", help, "calculation", calc}, extra...)...))
	}
	band := []string{"elite", "high", "medium", "low"}
	// The headline measures keep two years of history, draw a weekly trend from
	// it, and are exported as the hub's field gauge for dashboards.
	kept := func(fd Field) Field {
		fd.Def["history"] = map[string]any{"keep": "730d"}
		fd.Def["metric"] = true
		return fd
	}
	trend := func(of, title string) Field {
		return f(of+"_trend", typed("trend", "title", title, "description", "Weekly, over two years, from the field's history.", "history", map[string]any{"of": of, "every": "1w", "last": "104w"}))
	}
	return Fields{
		n("dora_deployments_30d", "Deployments (30d)", "Successful deployments in the last 30 days.", agg(relation, "count", "", ok, now, "")),
		n("dora_deployments_prev_30d", "Deployments (30d before)", "Successful deployments in the 30 days before those.", agg(relation, "count", "", ok, before, "")),
		kept(n("dora_deploy_frequency_30d", "Deploys per day (30d)", "Successful deployments per day over the last 30 days.", agg(relation, "count", "", ok, now, "day"))),
		n("dora_deploy_frequency_prev_30d", "Deploys per day (30d before)", "The same over the 30 days before.", agg(relation, "count", "", ok, before, "day")),
		n("dora_changes_30d", "Changes (30d)", "Deployments that reached an end in the last 30 days, failed ones included.", agg(relation, "count", "", ended, now, "")),
		n("dora_changes_prev_30d", "Changes (30d before)", "The same over the 30 days before.", agg(relation, "count", "", ended, before, "")),
		n("dora_failed_changes_30d", "Failed changes (30d)", "Deployments in the last 30 days that failed, needed a fix within a day, or were followed by an incident.", agg(relation, "count", "", failed, now, "")),
		n("dora_failed_changes_prev_30d", "Failed changes (30d before)", "The same over the 30 days before.", agg(relation, "count", "", failed, before, "")),
		kept(n("dora_lead_time_p50_hours", "Lead time p50 (h)", "Median of the successful deployments' lead times, the last 30 days.", agg(relation, "median", "lead_time_hours", ok, now, ""))),
		n("dora_lead_time_p90_hours", "Lead time p90 (h)", "The 90th percentile of the same.", agg(relation, "p90", "lead_time_hours", ok, now, "")),
		n("dora_lead_time_p50_prev_hours", "Lead time p50 (h, 30d before)", "The median over the 30 days before.", agg(relation, "median", "lead_time_hours", ok, before, "")),
		n("dora_lead_time_p90_prev_hours", "Lead time p90 (h, 30d before)", "The 90th percentile over the 30 days before.", agg(relation, "p90", "lead_time_hours", ok, before, "")),
		kept(n("dora_time_to_restore_p50_hours", "Time to restore p50 (h)", "Median hours from a failed change to its restore, the last 30 days.", agg(relation, "median", "restore_hours", failed, now, ""))),
		n("dora_time_to_restore_p50_prev_hours", "Time to restore p50 (h, 30d before)", "The same over the 30 days before.", agg(relation, "median", "restore_hours", failed, before, "")),
		kept(c("dora_change_failure_rate_30d", "number", "Change failure rate (%)", "Failed changes as a share of changes, the last 30 days.", changeFailureRate)),
		c("dora_change_failure_rate_prev_30d", "number", "Change failure rate (%, 30d before)", "The same over the 30 days before.", changeFailureRatePrev),
		c("dora_frequency_band", "enum", "Deploy frequency band", "The DORA band of deploy frequency.", frequencyBand, "values", band),
		c("dora_lead_time_band", "enum", "Lead time band", "The DORA band of the median lead time.", leadTimeBand, "values", band),
		c("dora_change_failure_band", "enum", "Change failure band", "The DORA band of the change failure rate.", changeFailureBand, "values", band),
		c("dora_restore_band", "enum", "Time to restore band", "The DORA band of the median time to restore.", restoreBand, "values", band),
		kept(c("dora_grade", "enum", "DORA grade", "The worst band among the measures with data. Empty where nothing deployed in 30 days.", doraGrade, "values", band)),
		trend("dora_deploy_frequency_30d", "Deploys per day, two years"),
		trend("dora_lead_time_p50_hours", "Lead time p50, two years"),
		trend("dora_change_failure_rate_30d", "Change failure rate, two years"),
		trend("dora_time_to_restore_p50_hours", "Time to restore p50, two years"),
	}
}

// ServiceBlueprint is one piece of software found from the files that build it, with its delivery outcomes.
func ServiceBlueprint() sdk.Blueprint {
	return sdk.Blueprint{
		"id": Service, "title": "Service", "group": "Catalog", "icon": "azure_service_fabric_clusters", "formerly": []string{"service"},
		"description": "One deployable piece of software, keyed by its repository path. redgreen finds each from the files that build it and writes where it lives, what it is, and what it is written in, with its DORA measures from its deployments. A repository's orrery.yaml adds the APIs its service offers and calls.",
		"fields": join(Fields{
			f("origin", typed("enum", "values", []string{"built", "adopted"}, "chip", true, "description", "Built here, or open source the estate runs. Every service redgreen finds is built here.")),
			f("path", typed("string", "description", "Where it sits in its repository, empty for one that is the whole repository.")),
			f("type", typed("enum", "values", []string{"backend", "frontend", "library", "infrastructure", "tool", "docs", "other"}, "description", "What kind of thing it is, read from the files that build it.")),
			f("language", typed("enum", "values", services.Languages, "sticky", true, "description", "What it is written in, read from the files that build it. A person who knows better changes it, and that change stays.")),
			f("last_changed_at", typed("datetime", "title", "Last changed", "description", "When the newest commit on the default branch touched its folder.")),
			f("last_changed_by", typed("string", "title", "Last changed by")),
			f("last_changed_by_email", typed("string", "title", "Last changed by email")),
			f("last_human_changed_at", typed("datetime", "title", "Last human change", "description", "When the newest commit by a person, not a bot or release tool, touched its folder.")),
			f("last_human_changed_by", typed("string", "title", "Last human change by")),
			f("last_human_changed_by_email", typed("string", "title", "Last human change by email")),
			f("has_readme", typed("boolean", "title", "Has README", "description", "A README in its own folder, or in the nearest folder above it that holds no other service.")),
			f("has_dockerfile", typed("boolean", "title", "Has Dockerfile", "description", "A Dockerfile that builds it.")),
			f("has_tests", typed("boolean", "title", "Has tests", "description", "Automated tests that belong to it.")),
		}, DORA("<-"+Deployment+":service")),
		"relations": map[string]any{
			"repository":   map[string]any{"target": Repository, "many": false, "title": "Homed in", "inverseTitle": "Services"},
			"consumes":     map[string]any{"target": API, "many": true, "title": "Consumes", "inverseTitle": "Consumed by", "description": "The APIs it calls today, from its repository's orrery.yaml."},
			"will_consume": map[string]any{"target": API, "many": true, "title": "Will consume", "inverseTitle": "Wanted by", "description": "The APIs it should call and does not yet, from its repository's orrery.yaml."},
		},
	}
}

// APIBlueprint is an interface a service offers, as its repository's orrery.yaml names it.
func APIBlueprint() sdk.Blueprint {
	return sdk.Blueprint{
		"id": API, "title": "API", "group": "Catalog", "formerly": []string{"api"},
		"description": "An interface a service offers others, keyed <service key>/<id>. redgreen writes each one a repository's orrery.yaml provides.",
		"fields": Fields{
			f("description", typed("string")),
			f("kind", typed("enum", "values", services.APIKinds)),
			f("visibility", typed("enum", "values", []string{"internal", "partner", "public"})),
			f("external", typed("boolean")),
			f("spec", typed("url", "title", "Definition", "description", "Where its machine-readable definition is read: a file in its repository, or the URL that serves it.")),
		},
		"relations": map[string]any{"service": map[string]any{"target": Service, "many": false, "title": "Provided by", "inverseTitle": "Provides"}},
	}
}

// TeamDORA is the DORA measures of a team, over its services' deployments. It is offered on its own,
// after the rest: it reads the standard's sw_service.team, which an estate may not have.
func TeamDORA() sdk.Blueprint {
	return sdk.Blueprint{"id": Team, "title": "Team", "fields": DORA("<-" + Service + ":team.<-" + Deployment + ":service"), "relations": map[string]any{}}
}

// DeploymentBlueprint is one rollout. History: it leaves 180 days after it ended.
func DeploymentBlueprint() sdk.Blueprint {
	return sdk.Blueprint{
		"id": Deployment, "title": "Deployment", "group": "Kubernetes", "icon": "rocket", "formerly": []string{"deployment"},
		"description": "One rollout of software to an environment. History, kept 180 days. redgreen writes the ones a forge records and adds lead time, failure, and restore to every one, Argo CD syncs included.",
		"lifecycle":   map[string]any{"expire": map[string]any{"after": "180d", "field": "deployed_at"}},
		"fields": Fields{
			f("status", typed("enum", "values", []string{"succeeded", "failed", "running", "cancelled"})),
			f("environment_name", typed("string", "chip", true, "title", "Environment")),
			f("revision", typed("string", "description", "The commit that was deployed.")),
			f("ref", typed("string", "description", "The branch or tag it was deployed from.")),
			f("deployed_at", typed("datetime", "description", "When it finished.")),
			f("started_at", typed("datetime")),
			f("duration_seconds", typed("number", "title", "Duration (s)")),
			f("initiated_by", typed("string")),
			f("url", typed("url", "title", "Open")),
			f("lead_time_hours", typed("number", "title", "Lead time (h)", "description", "Median hours from a carried commit being written to the end of this deployment.")),
			f("commit_count", typed("number", "title", "Commits", "description", "Commits this deployment carried since the last good one.")),
			f("fix_only", typed("boolean", "title", "Fix only", "description", "Every commit it carried is a fix or a revert.")),
			f("failed_change", typed("boolean", "title", "Failed change", "description", "It failed, a fix-only deployment followed within a day, or a critical or warning incident opened on it within a day.")),
			f("restore_hours", typed("number", "title", "Restore (h)", "description", "Hours from a failed change to the next good deployment, or to the incident's mitigation.")),
		},
		"relations": map[string]any{
			"repository": map[string]any{"target": Repository, "many": false, "title": "Repository", "inverseTitle": "Deployments"},
			"service":    map[string]any{"target": Service, "many": false, "title": "Service", "inverseTitle": "Deployments"},
		},
	}
}

// IssueBlueprint is an open issue or pull request. A record: it leaves 90 days after closing.
func IssueBlueprint() sdk.Blueprint {
	return sdk.Blueprint{
		"id": Issue, "title": "Issue", "group": "Operations", "icon": "ticket",
		"description": "An issue or pull request on a forge, keyed under its repository. A record: kept 90 days after it closes.",
		"lifecycle":   map[string]any{"expire": map[string]any{"after": "90d", "field": "closed_at"}},
		"fields": Fields{
			f("kind", typed("enum", "values", []string{"issue", "pull_request"}, "chip", true)),
			f("state", typed("enum", "values", []string{"open", "closed"}, "chip", true, "description", "Closed once it left the forge's open list.")),
			f("number", typed("number")),
			f("url", typed("url", "title", "Open")),
			f("repo", typed("string", "description", "The repository's full name.")),
			f("author", typed("string")),
			f("labels", typed("array")),
			f("assignees", typed("array")),
			f("comments", typed("number")),
			f("milestone", typed("string")),
			f("issue_type", typed("string")),
			f("draft", typed("boolean")),
			f("checks", typed("enum", "values", []string{"success", "failure", "pending", "none"}, "description", "A pull request's checks; empty where they were not read.")),
			f("created_at", typed("datetime")),
			f("updated_at", typed("datetime")),
			f("closed_at", typed("datetime", "description", "When redgreen first saw it gone from the open list.")),
		},
		"relations": map[string]any{"repository": map[string]any{"target": Repository, "many": false, "title": "Repository", "inverseTitle": "Issues"}},
	}
}

// Blueprints are offered in this order; the hub accepts relations in any order.
func Blueprints() []sdk.Blueprint {
	return []sdk.Blueprint{RepositoryBlueprint(), PipelineBlueprint(), ServiceBlueprint(), APIBlueprint(), DeploymentBlueprint(), IssueBlueprint(), TeamDORA()}
}
