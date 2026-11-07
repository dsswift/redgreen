package catalog

import (
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/dsswift/orrery/sdk"
	"github.com/dsswift/redgreen/internal/provider"
	"github.com/dsswift/redgreen/internal/services"
)

var (
	quoted = regexp.MustCompile(`'[^']*'|"[^"]*"`)
	ident  = regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_]*\s*\(?`)
)

// reads lists the field names an expression reads: not its functions, not its literals.
func reads(expr string) []string {
	var out []string
	for _, m := range ident.FindAllString(quoted.ReplaceAllString(expr, "''"), -1) {
		m = strings.TrimSpace(m)
		if strings.HasSuffix(m, "(") || slices.Contains([]string{"true", "false", "null", "key", "title"}, m) {
			continue
		}
		out = append(out, m)
	}
	return out
}

func byID() map[string]sdk.Blueprint {
	out := map[string]sdk.Blueprint{}
	for _, bp := range Blueprints() {
		out[bp["id"].(string)] = bp
	}
	return out
}

// TestEveryCalculationReadsFieldsItCanSee checks each calculation reads only
// fields its blueprint defines, and a calculated one only when it comes
// earlier: the hub works calculations out in order.
func TestEveryCalculationReadsFieldsItCanSee(t *testing.T) {
	for _, bp := range Blueprints() {
		fields := bp["fields"].(Fields)
		for i, f := range fields {
			calc, ok := f.Def["calculation"].(string)
			if !ok {
				continue
			}
			for _, name := range reads(calc) {
				at := slices.IndexFunc(fields, func(x Field) bool { return x.Name == name })
				if at < 0 {
					t.Errorf("%s.%s reads %s, which the blueprint does not define", bp["id"], f.Name, name)
					continue
				}
				if _, calculated := fields[at].Def["calculation"]; calculated && at >= i {
					t.Errorf("%s.%s reads %s, a calculation that comes after it", bp["id"], f.Name, name)
				}
			}
		}
	}
}

// TestEveryAggregationReadsFieldsTheRelatedBlueprintHas follows each
// aggregation's path to the blueprint it ends at and checks the fields it
// reads, filters on, and windows on are there.
func TestEveryAggregationReadsFieldsTheRelatedBlueprintHas(t *testing.T) {
	all := byID()
	hop := regexp.MustCompile(`^<-([a-z_]+):([a-z_]+)$`)
	for _, bp := range Blueprints() {
		for _, f := range bp["fields"].(Fields) {
			a, ok := f.Def["aggregation"].(map[string]any)
			if !ok {
				continue
			}
			end := ""
			for _, step := range strings.Split(a["relation"].(string), ".") {
				m := hop.FindStringSubmatch(step)
				if m == nil {
					t.Fatalf("%s.%s: hop %q is not <-blueprint:relation", bp["id"], f.Name, step)
				}
				end = m[1]
				if _, ok := all[end]["relations"].(map[string]any)[m[2]]; !ok && end != Service {
					t.Errorf("%s.%s: %s has no relation %s", bp["id"], f.Name, end, m[2])
				}
			}
			target := all[end]["fields"].(Fields)
			var used []string
			if v, ok := a["field"].(string); ok {
				used = append(used, v)
			}
			if w, ok := a["window"].(map[string]any); ok {
				used = append(used, w["field"].(string))
			}
			if w, ok := a["where"].(map[string]any); ok {
				used = append(used, w["field"].(string))
			}
			for _, name := range used {
				if !target.Has(name) {
					t.Errorf("%s.%s reads %s.%s, which is not defined", bp["id"], f.Name, end, name)
				}
			}
		}
	}
}

func TestStatusFieldsAndReasons(t *testing.T) {
	for _, id := range []string{Repository, Pipeline} {
		bp := byID()[id]
		field := bp["status"].(map[string]any)["field"].(string)
		fields := bp["fields"].(Fields)
		if !fields.Has(field) || !fields.Has(field+"_reason") {
			t.Errorf("%s: status %s needs %s and %s_reason", id, field, field, field)
		}
	}
}

// TestEntitiesWriteOnlyDefinedFields checks every field and relation an
// entity writes is one its blueprint offers.
func TestEntitiesWriteOnlyDefinedFields(t *testing.T) {
	now := time.Now()
	finished := now
	repo := &provider.Repo{Provider: provider.GitHub, FullName: "acme/api", Name: "api", Facts: &provider.Facts{}, Dependabot: &provider.Alerts{},
		Releases: []provider.Release{{Tag: "v1"}}, Issues: []provider.Issue{{Number: 1}}, PullRequests: []provider.PullRequest{{Number: 2}}}
	p := provider.Pipeline{ID: "7", Name: "CI"}
	found := &services.Found{Services: []services.Service{{ID: "", Name: "api", Language: "Go"}}}
	entities := []sdk.Entity{
		RepositoryEntity(repo, now),
		PipelineEntity(repo, p, Judge(p, "main", nil, Release{}, now)),
		DeploymentEntity("github/acme/api", "api", "github/acme/api", provider.Deployment{ID: "1", FinishedAt: &finished}, Outcome{}),
	}
	entities = append(entities, ServiceEntities(repo, found, nil)...)
	entities = append(entities, IssueEntities(repo)...)
	all := byID()
	for _, e := range entities {
		bp := all[e.Blueprint]
		fields := bp["fields"].(Fields)
		for name := range e.Fields {
			if !fields.Has(name) {
				t.Errorf("%s writes %s, which the blueprint does not offer", e.Blueprint, name)
			}
		}
		for name := range e.Relations {
			if _, ok := bp["relations"].(map[string]any)[name]; !ok {
				t.Errorf("%s writes relation %s, which the blueprint does not offer", e.Blueprint, name)
			}
		}
	}
}

func TestRuleSeveritiesHaveTheirDefaults(t *testing.T) {
	want := map[string]string{"default_branch_failing": "red", "schedule_stale": "amber", "release_build_failed": "red", "flaky": "amber",
		"critical_alerts": "red", "pr_checks_failing": "red", "archive_candidate": "amber"}
	for _, bp := range Blueprints() {
		for _, f := range bp["fields"].(Fields) {
			if rule, ok := strings.CutSuffix(f.Name, "_severity"); ok {
				if f.Def["default"] != want[rule] {
					t.Errorf("%s defaults to %v, want %s", f.Name, f.Def["default"], want[rule])
				}
				delete(want, rule)
			}
		}
	}
	if len(want) > 0 {
		t.Errorf("rules without a severity: %v", want)
	}
}

// TestDeliveryTrendsReadKeptMeasures checks every trend of the delivery
// measures reads a field of its own blueprint that keeps a history and is
// exported as a gauge: the hub refuses a blueprint whose trend reads less.
func TestDeliveryTrendsReadKeptMeasures(t *testing.T) {
	for _, relation := range []string{"<-" + Deployment + ":service", "<-" + Service + ":team.<-" + Deployment + ":service"} {
		defs := map[string]map[string]any{}
		for _, fd := range DORA(relation) {
			defs[fd.Name] = fd.Def
		}
		trends := 0
		for name, def := range defs {
			if def["type"] != "trend" {
				continue
			}
			trends++
			of := def["history"].(map[string]any)["of"].(string)
			read, ok := defs[of]
			if !ok {
				t.Fatalf("%s reads %s, which the measures lack", name, of)
			}
			if _, kept := read["history"].(map[string]any)["keep"]; !kept || read["metric"] != true {
				t.Errorf("%s reads %s, which keeps no history or is no gauge", name, of)
			}
		}
		if trends != 4 {
			t.Errorf("%d trends, want one per measure", trends)
		}
	}
}
