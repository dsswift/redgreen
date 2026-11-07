package state

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func open(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestResponsesKeepTheETagAndFacts(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	if r, err := s.Response(ctx, "k"); err != nil || r != nil {
		t.Fatalf("an unknown key has nothing kept: %v %v", r, err)
	}
	now := time.Now()
	if err := s.PutResponse(ctx, "k", `"v1"`, []byte(`{"a":1}`), now); err != nil {
		t.Fatal(err)
	}
	if err := s.PutResponse(ctx, "k", `"v2"`, []byte(`{"a":2}`), now); err != nil {
		t.Fatal(err)
	}
	r, _ := s.Response(ctx, "k")
	if r.ETag != `"v2"` || string(r.Facts) != `{"a":2}` {
		t.Fatalf("kept = %+v; the newest answer replaces the last", r)
	}
	// An answer untouched for a week goes; one touched stays.
	_ = s.PutResponse(ctx, "old", `"x"`, []byte(`{}`), now.Add(-8*24*time.Hour))
	_ = s.PutResponse(ctx, "touched", `"x"`, []byte(`{}`), now.Add(-8*24*time.Hour))
	_ = s.TouchResponse(ctx, "touched", now)
	p, err := s.Prune(ctx, now)
	if err != nil || p.Responses != 1 {
		t.Fatalf("pruned %+v %v", p, err)
	}
	if r, _ := s.Response(ctx, "touched"); r == nil {
		t.Fatal("a touched answer was pruned")
	}
}

func TestDeliveriesAreSeenOnce(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	if seen, _ := s.Seen(ctx, "github:d1", time.Now()); seen {
		t.Fatal("a first delivery was seen before")
	}
	if seen, _ := s.Seen(ctx, "github:d1", time.Now()); !seen {
		t.Fatal("a repeated delivery was not caught")
	}
}

func TestHistoryLeavesByAge(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	now := time.Now()
	type run struct{ ID string }
	_ = s.PutRun(ctx, "p", "1", now.Add(-61*24*time.Hour), run{"1"})
	_ = s.PutRun(ctx, "p", "2", now.Add(-time.Hour), run{"2"})
	_ = s.PutDeployment(ctx, "d-old", "r", now.Add(-201*24*time.Hour), map[string]string{"k": "d-old"})
	_ = s.PutCommits(ctx, "d-old", "", []string{}, now)
	_ = s.PutDeployment(ctx, "d-new", "r", now.Add(-time.Hour), map[string]string{"k": "d-new"})
	closed := now.Add(-91 * 24 * time.Hour)
	_ = s.PutIssue(ctx, "i-closed", "r", map[string]string{}, &closed)
	_ = s.PutIssue(ctx, "i-open", "r", map[string]string{}, nil)
	p, err := s.Prune(ctx, now)
	if err != nil || p.Runs != 1 || p.Deployments != 1 || p.Commits != 1 || p.Issues != 1 {
		t.Fatalf("pruned %+v %v", p, err)
	}
	runs, _ := Runs[run](ctx, s, "p")
	deployments, _ := Deployments[map[string]string](ctx, s, "r")
	open, _ := s.OpenIssues(ctx, "r")
	if len(runs) != 1 || len(deployments) != 1 || len(open) != 1 || open[0].Key != "i-open" {
		t.Fatalf("left: runs %v deployments %v issues %v", runs, deployments, open)
	}
}

func TestWrittenAndSettings(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	if h, _ := s.Written(ctx, "sw_deployment", "k"); h != "" {
		t.Fatal("nothing written yet")
	}
	_ = s.PutWritten(ctx, "sw_deployment", "k", "h1")
	if h, _ := s.Written(ctx, "sw_deployment", "k"); h != "h1" {
		t.Fatalf("hash = %q", h)
	}
	var v struct{ A int }
	if ok, _ := s.Setting(ctx, "x", &v); ok {
		t.Fatal("no setting yet")
	}
	_ = s.PutSetting(ctx, "x", struct{ A int }{7})
	if ok, _ := s.Setting(ctx, "x", &v); !ok || v.A != 7 {
		t.Fatalf("setting = %+v", v)
	}
}
