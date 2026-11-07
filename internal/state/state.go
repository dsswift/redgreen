// Package state is redgreen's small, rebuildable store: per request URL the
// ETag and the normalised facts read from that answer, never the raw body;
// webhook delivery ids; the run, deployment, and issue facts that outlive one
// forge answer; and what was last written to Orrery. Losing the file costs
// one full sweep.
package state

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

const schema = `
CREATE TABLE IF NOT EXISTS responses (
  key TEXT PRIMARY KEY,
  etag TEXT NOT NULL,
  facts BLOB NOT NULL,
  touched_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS responses_touched ON responses(touched_at);
CREATE TABLE IF NOT EXISTS deliveries (
  id TEXT PRIMARY KEY,
  at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS runs (
  pipeline TEXT NOT NULL,
  id TEXT NOT NULL,
  facts TEXT NOT NULL,
  created_at TEXT NOT NULL,
  PRIMARY KEY (pipeline, id)
);
CREATE INDEX IF NOT EXISTS runs_created ON runs(created_at);
CREATE TABLE IF NOT EXISTS deployments (
  key TEXT PRIMARY KEY,
  repo TEXT NOT NULL,
  facts TEXT NOT NULL,
  finished_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS deployments_repo ON deployments(repo, finished_at);
CREATE TABLE IF NOT EXISTS commits (
  deployment TEXT PRIMARY KEY,
  base TEXT,
  facts TEXT NOT NULL,
  read_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS issues (
  key TEXT PRIMARY KEY,
  repo TEXT NOT NULL,
  entity TEXT NOT NULL,
  closed_at TEXT
);
CREATE INDEX IF NOT EXISTS issues_repo ON issues(repo);
CREATE TABLE IF NOT EXISTS services (
  repo TEXT PRIMARY KEY,
  facts TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS written (
  blueprint TEXT NOT NULL,
  key TEXT NOT NULL,
  hash TEXT NOT NULL,
  PRIMARY KEY (blueprint, key)
);
CREATE TABLE IF NOT EXISTS settings (
  key TEXT PRIMARY KEY,
  value TEXT NOT NULL
);
`

// Store is one SQLite file. Every call is short; the pool holds one
// connection, so writes never contend.
type Store struct {
	db *sql.DB
}

// Open opens or creates the file at path.
func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("state schema: %w", err)
	}
	return &Store{db: db}, nil
}

// Close closes the file.
func (s *Store) Close() error { return s.db.Close() }

func stamp(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

// Response is what one conditional request left behind.
type Response struct {
	ETag  string
	Facts []byte
}

// Response returns the kept answer for key, or nil.
func (s *Store) Response(ctx context.Context, key string) (*Response, error) {
	var r Response
	err := s.db.QueryRowContext(ctx, `SELECT etag, facts FROM responses WHERE key = ?`, key).Scan(&r.ETag, &r.Facts)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return &r, err
}

// PutResponse keeps the ETag and the facts read from one answer.
func (s *Store) PutResponse(ctx context.Context, key, etag string, facts []byte, now time.Time) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO responses (key, etag, facts, touched_at) VALUES (?, ?, ?, ?)
		ON CONFLICT(key) DO UPDATE SET etag = excluded.etag, facts = excluded.facts, touched_at = excluded.touched_at`, key, etag, facts, stamp(now))
	return err
}

// TouchResponse marks a kept answer as still in use.
func (s *Store) TouchResponse(ctx context.Context, key string, now time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE responses SET touched_at = ? WHERE key = ?`, stamp(now), key)
	return err
}

// Seen records a webhook delivery and says whether it was seen before.
func (s *Store) Seen(ctx context.Context, id string, now time.Time) (bool, error) {
	res, err := s.db.ExecContext(ctx, `INSERT INTO deliveries (id, at) VALUES (?, ?) ON CONFLICT(id) DO NOTHING`, id, stamp(now))
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 0, err
}

// PutRun keeps one run's facts under its pipeline.
func (s *Store) PutRun(ctx context.Context, pipeline, id string, createdAt time.Time, facts any) error {
	b, err := json.Marshal(facts)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO runs (pipeline, id, facts, created_at) VALUES (?, ?, ?, ?)
		ON CONFLICT(pipeline, id) DO UPDATE SET facts = excluded.facts, created_at = excluded.created_at`, pipeline, id, string(b), stamp(createdAt))
	return err
}

// Runs returns every kept run of a pipeline, decoded into T.
func Runs[T any](ctx context.Context, s *Store, pipeline string) ([]T, error) {
	return decodeAll[T](ctx, s, `SELECT facts FROM runs WHERE pipeline = ?`, pipeline)
}

// PutDeployment keeps one finished deployment's facts.
func (s *Store) PutDeployment(ctx context.Context, key, repo string, finishedAt time.Time, facts any) error {
	b, err := json.Marshal(facts)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO deployments (key, repo, facts, finished_at) VALUES (?, ?, ?, ?)
		ON CONFLICT(key) DO UPDATE SET repo = excluded.repo, facts = excluded.facts, finished_at = excluded.finished_at`, key, repo, string(b), stamp(finishedAt))
	return err
}

// Deployments returns the kept deployments of one repository, decoded into T.
func Deployments[T any](ctx context.Context, s *Store, repo string) ([]T, error) {
	return decodeAll[T](ctx, s, `SELECT facts FROM deployments WHERE repo = ? ORDER BY finished_at`, repo)
}

// Carried is the commits read for one deployment.
type Carried struct {
	Base  string
	Facts []byte
}

// Commits returns the commits read for a deployment, or nil when unread.
func (s *Store) Commits(ctx context.Context, deployment string) (*Carried, error) {
	var c Carried
	var base sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT base, facts FROM commits WHERE deployment = ?`, deployment).Scan(&base, &c.Facts)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	c.Base = base.String
	return &c, err
}

// PutCommits keeps the commits a deployment carried, counted from base.
func (s *Store) PutCommits(ctx context.Context, deployment, base string, facts any, now time.Time) error {
	b, err := json.Marshal(facts)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO commits (deployment, base, facts, read_at) VALUES (?, ?, ?, ?)
		ON CONFLICT(deployment) DO UPDATE SET base = excluded.base, facts = excluded.facts, read_at = excluded.read_at`, deployment, base, string(b), stamp(now))
	return err
}

// OpenIssue is an issue or pull request last seen open, with the entity
// written for it.
type OpenIssue struct {
	Key    string
	Entity []byte
}

// OpenIssues returns the issues of a repository last seen open.
func (s *Store) OpenIssues(ctx context.Context, repo string) ([]OpenIssue, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT key, entity FROM issues WHERE repo = ? AND closed_at IS NULL`, repo)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []OpenIssue
	for rows.Next() {
		var o OpenIssue
		if err := rows.Scan(&o.Key, &o.Entity); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// PutIssue keeps an issue seen open, or marks one closed when closedAt is set.
func (s *Store) PutIssue(ctx context.Context, key, repo string, entity any, closedAt *time.Time) error {
	b, err := json.Marshal(entity)
	if err != nil {
		return err
	}
	var closed any
	if closedAt != nil {
		closed = stamp(*closedAt)
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO issues (key, repo, entity, closed_at) VALUES (?, ?, ?, ?)
		ON CONFLICT(key) DO UPDATE SET repo = excluded.repo, entity = excluded.entity, closed_at = excluded.closed_at`, key, repo, string(b), closed)
	return err
}

// Services returns the services last read for a repository, decoded into v.
func (s *Store) Services(ctx context.Context, repo string, v any) (bool, error) {
	var facts string
	err := s.db.QueryRowContext(ctx, `SELECT facts FROM services WHERE repo = ?`, repo).Scan(&facts)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, json.Unmarshal([]byte(facts), v)
}

// PutServices keeps the services read for a repository.
func (s *Store) PutServices(ctx context.Context, repo string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO services (repo, facts) VALUES (?, ?) ON CONFLICT(repo) DO UPDATE SET facts = excluded.facts`, repo, string(b))
	return err
}

// Written returns the hash of what was last written for an entity.
func (s *Store) Written(ctx context.Context, blueprint, key string) (string, error) {
	var h string
	err := s.db.QueryRowContext(ctx, `SELECT hash FROM written WHERE blueprint = ? AND key = ?`, blueprint, key).Scan(&h)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return h, err
}

// PutWritten records the hash of what was written for an entity.
func (s *Store) PutWritten(ctx context.Context, blueprint, key, hash string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO written (blueprint, key, hash) VALUES (?, ?, ?)
		ON CONFLICT(blueprint, key) DO UPDATE SET hash = excluded.hash`, blueprint, key, hash)
	return err
}

// Setting decodes a kept value into v and says whether there was one.
func (s *Store) Setting(ctx context.Context, key string, v any) (bool, error) {
	var raw string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, key).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, json.Unmarshal([]byte(raw), v)
}

// PutSetting keeps a value.
func (s *Store) PutSetting(ctx context.Context, key string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO settings (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, string(b))
	return err
}

// Pruned counts what Prune removed, by table.
type Pruned struct {
	Responses, Deliveries, Runs, Deployments, Commits, Issues int64
}

// Prune drops what no window reads any more: answers untouched for a week,
// deliveries older than a week, runs older than sixty days, deployments and
// their commits older than two hundred days, and issues closed ninety days ago.
func (s *Store) Prune(ctx context.Context, now time.Time) (Pruned, error) {
	var p Pruned
	day := 24 * time.Hour
	steps := []struct {
		n     *int64
		query string
		at    time.Time
	}{
		{&p.Responses, `DELETE FROM responses WHERE touched_at < ?`, now.Add(-7 * day)},
		{&p.Deliveries, `DELETE FROM deliveries WHERE at < ?`, now.Add(-7 * day)},
		{&p.Runs, `DELETE FROM runs WHERE created_at < ?`, now.Add(-60 * day)},
		{&p.Commits, `DELETE FROM commits WHERE deployment IN (SELECT key FROM deployments WHERE finished_at < ?)`, now.Add(-200 * day)},
		{&p.Deployments, `DELETE FROM deployments WHERE finished_at < ?`, now.Add(-200 * day)},
		{&p.Issues, `DELETE FROM issues WHERE closed_at IS NOT NULL AND closed_at < ?`, now.Add(-90 * day)},
	}
	for _, st := range steps {
		res, err := s.db.ExecContext(ctx, st.query, stamp(st.at))
		if err != nil {
			return p, err
		}
		*st.n, _ = res.RowsAffected()
	}
	return p, nil
}

func decodeAll[T any](ctx context.Context, s *Store, query string, args ...any) ([]T, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []T
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var v T
		if err := json.Unmarshal([]byte(raw), &v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
