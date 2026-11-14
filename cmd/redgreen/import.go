package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/dsswift/orrery/sdk"
	"github.com/dsswift/redgreen/internal/catalog"
	"github.com/dsswift/redgreen/internal/config"
	"github.com/dsswift/redgreen/internal/outcomes"
	"github.com/dsswift/redgreen/internal/provider"
	"github.com/dsswift/redgreen/internal/services"
	"github.com/dsswift/redgreen/internal/telemetry"
)

// oldDeployment is one row of the old store's deployments table, with its repository.
type oldDeployment struct {
	Key, RepoID, Service, Source, Environment, Status, Revision string
	Elsewhere                                                   bool
	StartedAt, FinishedAt, URL, Base, Commits                   sql.NullString
	Provider, FullName, Login, Snapshot                         sql.NullString
}

// readHistory reads every deployment the old store kept.
func readHistory(db *sql.DB) ([]oldDeployment, error) {
	rows, err := db.Query(`SELECT d.key, d.repo_id, d.service, d.source, d.environment, d.status, d.revision, d.revision_elsewhere,
		d.started_at, d.finished_at, d.url, d.base_revision, d.commits, r.provider, r.full_name, a.login, r.snapshot
		FROM deployments d LEFT JOIN repos r ON r.id = d.repo_id LEFT JOIN accounts a ON a.id = r.account_id ORDER BY d.finished_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []oldDeployment
	for rows.Next() {
		var d oldDeployment
		var elsewhere int
		if err := rows.Scan(&d.Key, &d.RepoID, &d.Service, &d.Source, &d.Environment, &d.Status, &d.Revision, &elsewhere,
			&d.StartedAt, &d.FinishedAt, &d.URL, &d.Base, &d.Commits, &d.Provider, &d.FullName, &d.Login, &d.Snapshot); err != nil {
			return nil, err
		}
		d.Elsewhere = elsewhere != 0
		out = append(out, d)
	}
	return out, rows.Err()
}

// plan is what an import writes: forge deployments by run scope, and the
// outcomes added to Argo CD syncs another source owns.
type plan struct {
	forge   map[string][]sdk.Entity
	argo    []sdk.Entity
	skipped map[string]int
}

// expiry is how long the hub keeps a deployment; an older one is not sent.
const expiry = 180 * 24 * time.Hour

// planImport shapes the old store's deployments as entities, with the
// outcomes worked out over all of them and the incidents given.
func planImport(rows []oldDeployment, incidents []outcomes.Incident, now time.Time) plan {
	p := plan{forge: map[string][]sdk.Entity{}, skipped: map[string]int{}}
	type kept struct {
		row     oldDeployment
		record  outcomes.Record
		hubKey  string
		repoKey string
		found   *services.Found
		name    string
	}
	var all []kept
	var records []outcomes.Record
	carried := map[string]*outcomes.Carried{}
	for _, d := range rows {
		if !d.Provider.Valid || !d.FullName.Valid {
			p.skipped["repo_missing"]++
			continue
		}
		finished, err := time.Parse(time.RFC3339, d.FinishedAt.String)
		if err != nil || (d.Status != provider.DeploySucceeded && d.Status != provider.DeployFailed) {
			p.skipped["unfinished"]++
			continue
		}
		_, own, ok := strings.Cut(d.Key, "/"+d.Source+"/")
		if (d.Source != "forge" && d.Source != "orrery") || !ok || own == "" {
			p.skipped["bad_key"]++
			continue
		}
		repoKey := catalog.RepoKey(provider.Kind(d.Provider.String), d.FullName.String)
		hubKey := own
		if d.Source == "forge" {
			hubKey = repoKey + "/" + own
		}
		var snap struct {
			Name     string `json:"name"`
			Services *struct {
				Services []services.Service `json:"services"`
			} `json:"services"`
		}
		_ = json.Unmarshal([]byte(d.Snapshot.String), &snap)
		var found *services.Found
		if snap.Services != nil {
			found = &services.Found{Services: snap.Services.Services}
		}
		r := outcomes.Record{Key: hubKey, Source: d.Source, RepoKey: repoKey, Folder: d.Service, Environment: d.Environment, Status: d.Status, Revision: d.Revision, FinishedAt: finished, Elsewhere: d.Elsewhere}
		if d.Commits.Valid {
			var commits []provider.Commit
			if json.Unmarshal([]byte(d.Commits.String), &commits) == nil {
				carried[hubKey] = &outcomes.Carried{Base: d.Base.String, Commits: commits}
			}
		}
		records = append(records, r)
		name := snap.Name
		if name == "" {
			name = d.FullName.String[strings.LastIndex(d.FullName.String, "/")+1:]
		}
		all = append(all, kept{row: d, record: r, hubKey: hubKey, repoKey: repoKey, found: found, name: name})
	}
	results := outcomes.Classify(records, carried, incidents)
	for _, k := range all {
		if now.Sub(k.record.FinishedAt) > expiry {
			p.skipped["expired"]++
			continue
		}
		o := results[k.hubKey]
		service := catalog.DeploymentService(k.repoKey, k.found, k.record.Folder)
		if k.record.Source == "orrery" {
			rel := map[string]any{"repository": k.repoKey}
			if service != "" {
				rel["service"] = service
			}
			p.argo = append(p.argo, sdk.Entity{Blueprint: catalog.Deployment, Key: k.hubKey, Fields: o.Fields(), Relations: rel})
			continue
		}
		created := k.record.FinishedAt
		if t, err := time.Parse(time.RFC3339, k.row.StartedAt.String); err == nil {
			created = t
		}
		finished := k.record.FinishedAt
		d := provider.Deployment{ID: k.hubKey[len(k.repoKey)+1:], Environment: k.record.Environment, Status: k.record.Status, SHA: k.record.Revision, CreatedAt: created, FinishedAt: &finished, URL: k.row.URL.String}
		scope := strings.ToLower(k.row.Provider.String+"/"+k.row.Login.String) + "/deployments"
		p.forge[scope] = append(p.forge[scope], catalog.DeploymentEntity(k.repoKey, k.name, service, d, o))
	}
	return p
}

// importHistory carries the old store's deployments to Orrery once, so the
// DORA windows are not empty on day one, and counts what became of each.
func importHistory(path string) error {
	log := telemetry.NewLogger(os.Stdout).With("component", "import-history")
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if cfg.OrreryURL == "" || cfg.OrreryToken == "" {
		return errors.New("set ORRERY_URL and ORRERY_TOKEN, the same source token the service writes with")
	}
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		return err
	}
	defer db.Close()
	rows, err := readHistory(db)
	if err != nil {
		return fmt.Errorf("reading %s: %w", path, err)
	}
	ctx := context.Background()
	client := &http.Client{Timeout: 60 * time.Second}
	var incidents []outcomes.Incident
	if cfg.OrreryReadToken != "" {
		r := outcomes.NewReader(cfg.OrreryURL, cfg.OrreryReadToken, nil, client, log)
		_, _, workloads, raw, err := r.Read(ctx, time.Now().Add(-expiry-30*24*time.Hour))
		if err != nil {
			return err
		}
		host := ""
		if u, err := url.Parse(cfg.GitLabURL); err == nil {
			host = u.Hostname()
		}
		incidents, _ = outcomes.MapIncidents(raw, workloads, host)
	} else {
		log.Info("ORRERY_READ_TOKEN is not set; failed changes are judged without incidents")
	}
	p := planImport(rows, incidents, time.Now())
	push := sdk.New(cfg.OrreryURL, cfg.OrreryToken, sdk.WithHTTPClient(client), sdk.WithLogger(log))
	if _, err := push.OfferBlueprints(ctx, catalog.Blueprints()[:5]); err != nil {
		return err
	}
	var total sdk.RunCounts
	write := func(scope string, list []sdk.Entity, opts ...sdk.RunOption) error {
		if len(list) == 0 {
			return nil
		}
		res, err := push.Incremental(ctx, scope, func(add func(sdk.Entity) error) error {
			for _, x := range list {
				if err := add(x); err != nil {
					return err
				}
			}
			return nil
		}, opts...)
		if err != nil {
			return err
		}
		total.Received += res.Counts.Received
		total.Created += res.Counts.Created
		total.Updated += res.Counts.Updated
		total.Unchanged += res.Counts.Unchanged
		total.Rejected += res.Counts.Rejected
		for _, e := range res.Errors {
			log.Warn("orrery rejected a deployment", "key", e.Key, "field", e.Field, "reason", e.Reason)
		}
		return nil
	}
	for scope, list := range p.forge {
		if err := write(scope, list); err != nil {
			return err
		}
	}
	if err := write("argo-outcomes", p.argo, sdk.EnrichOnly()); err != nil {
		return err
	}
	skipped := 0
	for _, n := range p.skipped {
		skipped += n
	}
	log.Info("import done", "read", len(rows), "sent", total.Received, "created", total.Created, "updated", total.Updated, "unchanged", total.Unchanged,
		"rejected", total.Rejected, "skipped", skipped, "skippedBy", p.skipped)
	fmt.Printf("read %d, created %d, updated %d, unchanged %d, rejected %d, skipped %d %v\n", len(rows), total.Created, total.Updated, total.Unchanged, total.Rejected, skipped, p.skipped)
	return nil
}
