package main

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/dsswift/orrery/sdk"
)

// oldSchema is the part of the old web app's store the import reads.
const oldSchema = `
CREATE TABLE accounts (id TEXT PRIMARY KEY, provider TEXT NOT NULL, login TEXT NOT NULL, name TEXT NOT NULL, kind TEXT NOT NULL, avatar_url TEXT, url TEXT NOT NULL, status TEXT NOT NULL, connection TEXT NOT NULL, last_error TEXT, seen_at TEXT NOT NULL);
CREATE TABLE repos (id TEXT PRIMARY KEY, provider TEXT NOT NULL, account_id TEXT NOT NULL, full_name TEXT NOT NULL, snapshot TEXT, synced_at TEXT, sync_error TEXT, seen_at TEXT NOT NULL);
CREATE TABLE deployments (key TEXT PRIMARY KEY, repo_id TEXT NOT NULL, service TEXT NOT NULL, source TEXT NOT NULL, environment TEXT NOT NULL, status TEXT NOT NULL, revision TEXT NOT NULL, revision_elsewhere INTEGER NOT NULL, started_at TEXT, finished_at TEXT NOT NULL, url TEXT, base_revision TEXT, commits TEXT, seen_at TEXT NOT NULL);
`

func TestImportCarriesTheOldDeployments(t *testing.T) {
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "old.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Now().UTC()
	at := func(daysAgo float64) string {
		return now.Add(-time.Duration(daysAgo * float64(24*time.Hour))).Format(time.RFC3339)
	}
	snapshot := `{"name":"api","services":{"commit":"c","rules":8,"services":[{"id":"","path":"","name":"api","type":"backend"}]}}`
	monorepo := `{"name":"mono","services":{"commit":"c","rules":8,"services":[{"id":"apps/web","path":"apps/web","name":"web","type":"frontend"},{"id":"apps/job","path":"apps/job","name":"job","type":"tool"}]}}`
	stmts := []string{
		oldSchema,
		`INSERT INTO accounts VALUES ('github:10','github','Acme','Acme','organization',NULL,'u','active','{}',NULL,'x')`,
		`INSERT INTO repos VALUES ('github:1','github','github:10','Acme/API','` + snapshot + `',NULL,NULL,'x')`,
		`INSERT INTO repos VALUES ('github:2','github','github:10','Acme/mono','` + monorepo + `',NULL,NULL,'x')`,
	}
	rows := [][]any{
		{"github:1/forge/11", "github:1", "", "forge", "production", "succeeded", "a1", 0, at(10.2), at(10), "https://x/11", nil, `[{"sha":"a1","authoredAt":"` + at(12) + `","title":"feat: x"}]`},
		{"github:1/forge/12", "github:1", "", "forge", "production", "failed", "a2", 0, at(9.1), at(9), nil, "a1", `[]`},
		{"github:1/forge/13", "github:1", "", "forge", "production", "succeeded", "a3", 0, at(8.6), at(8.5), nil, "a1", `[{"sha":"a3","authoredAt":"` + at(9) + `","title":"fix: it"}]`},
		{"github:2/orrery/home/argocd/web/4", "github:2", "apps/web", "orrery", "production", "succeeded", "m1", 1, nil, at(3), nil, nil, nil},
		{"github:1/forge/1", "github:1", "", "forge", "production", "succeeded", "old", 0, nil, at(190), nil, nil, nil},
		{"github:9/forge/5", "github:9", "", "forge", "production", "succeeded", "x", 0, nil, at(5), nil, nil, nil},
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			t.Fatal(err)
		}
	}
	for _, r := range rows {
		if _, err := db.Exec(`INSERT INTO deployments VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,'x')`, r...); err != nil {
			t.Fatal(err)
		}
	}
	history, err := readHistory(db)
	if err != nil {
		t.Fatal(err)
	}
	p := planImport(history, nil, now)
	forge := p.forge["github/acme/deployments"]
	byKey := map[string]sdk.Entity{}
	for _, e := range forge {
		byKey[e.Key] = e
	}
	if len(forge) != 3 || len(p.argo) != 1 {
		t.Fatalf("forge %d argo %d, want 3 and 1", len(forge), len(p.argo))
	}
	first := byKey["github/acme/api/11"]
	if first.Relations["service"] != "github/acme/api" || first.Relations["repository"] != "github/acme/api" || first.Fields["lead_time_hours"] != 48.0 || first.Fields["commit_count"] != 1 {
		t.Errorf("first = %+v", first)
	}
	if f := byKey["github/acme/api/12"].Fields; f["failed_change"] != true || f["restore_hours"] != 12.0 {
		t.Errorf("a failed deployment restores at the next good one: %+v", f)
	}
	if f := byKey["github/acme/api/13"].Fields; f["fix_only"] != true || f["failed_change"] != false {
		t.Errorf("the fix: %+v", f)
	}
	argo := p.argo[0]
	if argo.Key != "home/argocd/web/4" || argo.Relations["service"] != "github/acme/mono/apps/web" || argo.Fields["lead_time_hours"] != nil {
		t.Errorf("an argo sync is enriched under its own key, on its service: %+v", argo)
	}
	if p.skipped["expired"] != 1 || p.skipped["repo_missing"] != 1 {
		t.Errorf("skipped = %v", p.skipped)
	}
}
