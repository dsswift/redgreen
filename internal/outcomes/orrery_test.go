package outcomes

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/dsswift/redgreen/internal/state"
)

// hub answers list reads from canned entities by blueprint, and keeps every
// question asked. Like the hub, it tags each answer by the question and what
// it holds, and answers a question asked with that tag with 304.
type hub struct {
	mu        sync.Mutex
	asked     []url.Values
	answer    map[string][]map[string]any
	moved     int
	tags      []string
	unchanged int
}

func (h *hub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.mu.Lock()
	defer h.mu.Unlock()
	q := r.URL.Query()
	h.asked = append(h.asked, q)
	h.tags = append(h.tags, r.Header.Get("If-None-Match"))
	etag := fmt.Sprintf(`W/"%d.%x"`, h.moved, sha256.Sum256([]byte(r.URL.RawQuery)))
	w.Header().Set("ETag", etag)
	w.Header().Set("Cache-Control", "private, no-cache")
	if r.Header.Get("If-None-Match") == etag {
		h.unchanged++
		w.WriteHeader(http.StatusNotModified)
		return
	}
	list := h.answer[q.Get("blueprint")]
	if list == nil {
		list = []map[string]any{}
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"entities": list, "total": len(list)})
}

func (h *hub) blueprints() []string {
	var out []string
	for _, q := range h.asked {
		out = append(out, q.Get("blueprint"))
	}
	return out
}

// from is the moment every read here asks from: mid-day, so the hub is asked from its start.
var from = time.Date(2026, 5, 1, 13, 0, 0, 0, time.UTC)

func reader(t *testing.T, h *hub) Reader {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	store, err := state.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	return NewReader(srv.URL, "token", store, srv.Client(), slog.New(slog.DiscardHandler))
}

func readWith(t *testing.T, r Reader) ([]HubDeployment, []HubApp, []HubWorkload, []HubIncident) {
	t.Helper()
	syncs, apps, workloads, incidents, err := r.Read(context.Background(), from)
	if err != nil {
		t.Fatal(err)
	}
	return syncs, apps, workloads, incidents
}

func read(t *testing.T, h *hub) ([]HubDeployment, []HubApp, []HubWorkload, []HubIncident) {
	t.Helper()
	return readWith(t, reader(t, h))
}

// estate is a hub with one sync placed through its app and workload, and one incident.
func estate() *hub {
	return &hub{answer: map[string][]map[string]any{
		"sw_workload":   {{"key": "w1", "fields": map[string]any{"repo": "https://github.com/acme/app"}}},
		"argocd_app":    {{"key": "a1", "fields": map[string]any{"repo_url": "https://github.com/acme/gitops"}, "relations": map[string]any{"workload": []any{map[string]any{"key": "w1"}}}}},
		"sw_deployment": {{"key": "d1", "fields": map[string]any{"status": "succeeded", "revision": "abc", "deployed_at": "2026-05-02T00:00:00Z"}, "relations": map[string]any{"app": []any{map[string]any{"key": "a1"}}}}},
		"it_incident":   {{"key": "i1", "fields": map[string]any{"severity": "sev2", "opened_at": "2026-05-02T00:00:00Z"}, "relations": map[string]any{"workload": []any{map[string]any{"key": "w1"}}}}},
	}}
}

func TestReadUsesTheKeptPageWhenTheHubAnswersUnchanged(t *testing.T) {
	h := estate()
	r := reader(t, h)
	readWith(t, r)
	syncs, apps, workloads, incidents := readWith(t, r)
	if h.unchanged != 4 {
		t.Fatalf("answered unchanged %d of the second read's 4 pages; tags sent %q", h.unchanged, h.tags)
	}
	for i, tag := range h.tags {
		if (i < 4) != (tag == "") {
			t.Errorf("question %d sent tag %q", i, tag)
		}
	}
	if len(syncs) != 1 || syncs[0].Fields.Revision != "abc" || syncs[0].Relations.App[0].Key != "a1" || len(apps) != 1 || apps[0].Relations.Workload[0].Key != "w1" ||
		len(workloads) != 1 || workloads[0].Fields.Repo != "https://github.com/acme/app" || len(incidents) != 1 || incidents[0].Fields.Severity != "sev2" {
		t.Fatalf("kept pages read %+v %+v %+v %+v", syncs, apps, workloads, incidents)
	}
}

func TestReadKeepsTheNewPageWhenTheHubMoved(t *testing.T) {
	h := estate()
	r := reader(t, h)
	readWith(t, r)
	h.mu.Lock()
	h.moved++
	h.answer["sw_deployment"][0]["fields"].(map[string]any)["revision"] = "def"
	h.mu.Unlock()
	syncs, _, _, _ := readWith(t, r)
	if h.unchanged != 0 || len(syncs) != 1 || syncs[0].Fields.Revision != "def" {
		t.Fatalf("after a move: unchanged %d, syncs %+v", h.unchanged, syncs)
	}
	syncs, _, _, _ = readWith(t, r)
	if h.unchanged != 4 || len(syncs) != 1 || syncs[0].Fields.Revision != "def" {
		t.Fatalf("the new page was not kept: unchanged %d, syncs %+v", h.unchanged, syncs)
	}
}

func TestReadAsksFromTheDayAndTrimsToTheMoment(t *testing.T) {
	h := estate()
	h.answer["sw_deployment"] = append(h.answer["sw_deployment"], map[string]any{"key": "d0", "fields": map[string]any{"status": "succeeded", "revision": "old", "deployed_at": "2026-05-01T06:00:00Z"}, "relations": map[string]any{"app": []any{map[string]any{"key": "a1"}}}})
	h.answer["it_incident"] = append(h.answer["it_incident"], map[string]any{"key": "i0", "fields": map[string]any{"opened_at": "2026-05-01T12:59:59Z"}, "relations": map[string]any{"workload": []any{map[string]any{"key": "w1"}}}})
	syncs, _, _, incidents := read(t, h)
	if len(syncs) != 1 || syncs[0].Key != "d1" || len(incidents) != 1 || incidents[0].Key != "i1" {
		t.Fatalf("kept what came before the moment: %+v %+v", syncs, incidents)
	}
}

func TestReadAsksOnlyForWhatItMaps(t *testing.T) {
	h := &hub{answer: map[string][]map[string]any{
		"sw_workload":   {{"key": "w1", "fields": map[string]any{"repo": "https://github.com/acme/app"}}},
		"argocd_app":    {{"key": "a1", "fields": map[string]any{"repo_url": "https://github.com/acme/gitops"}, "relations": map[string]any{"workload": []any{map[string]any{"key": "w1"}}}}},
		"sw_deployment": {{"key": "d1", "fields": map[string]any{"status": "succeeded", "revision": "abc", "deployed_at": "2026-05-02T00:00:00Z"}, "relations": map[string]any{"app": []any{map[string]any{"key": "a1"}}}}},
	}}
	syncs, apps, workloads, _ := read(t, h)
	if len(syncs) != 1 || syncs[0].Fields.Status != "succeeded" || syncs[0].Relations.App[0].Key != "a1" || len(apps) != 1 || len(workloads) != 1 {
		t.Fatalf("read %+v %+v %+v", syncs, apps, workloads)
	}
	want := map[string]struct{ fields, relations, filter string }{
		"sw_workload":   {"repo", "", ""},
		"argocd_app":    {"repo_url", "workload,environment", ""},
		"sw_deployment": {"status,revision,deployed_at", "app", `{"and":[{"field":"deployed_at","op":"gte","value":"2026-05-01T00:00:00Z"},{"relation":"app"}]}`},
		"it_incident":   {"severity,opened_at,mitigated_at,resolved_at", "workload", `{"and":[{"field":"opened_at","op":"gte","value":"2026-05-01T00:00:00Z"},{"relation":"workload"}]}`},
	}
	for _, q := range h.asked {
		w := want[q.Get("blueprint")]
		// Given empty, a picker asks for none: the parameter is sent either way.
		if !q.Has("fields") || !q.Has("relations") || q.Get("fields") != w.fields || q.Get("relations") != w.relations || q.Get("filter") != w.filter {
			t.Errorf("%s asked fields=%q relations=%q filter=%s", q.Get("blueprint"), q.Get("fields"), q.Get("relations"), q.Get("filter"))
		}
	}
	if len(h.asked) != 4 {
		t.Errorf("asked %v", h.blueprints())
	}
}

func TestReadAsksNothingMoreOfAHubWithNoWorkloads(t *testing.T) {
	h := &hub{answer: map[string][]map[string]any{}}
	syncs, apps, workloads, incidents := read(t, h)
	if syncs != nil || apps != nil || workloads != nil || incidents != nil {
		t.Fatalf("read %v %v %v %v", syncs, apps, workloads, incidents)
	}
	if got := h.blueprints(); len(got) != 1 || got[0] != "sw_workload" {
		t.Errorf("asked %v", got)
	}
}

func TestReadAsksForNoSyncsWithoutAnApp(t *testing.T) {
	h := &hub{answer: map[string][]map[string]any{"sw_workload": {{"key": "w1", "fields": map[string]any{"repo": "https://github.com/acme/app"}}}}}
	read(t, h)
	for _, b := range h.blueprints() {
		if b == "sw_deployment" {
			t.Errorf("asked for syncs with no app to place them: %v", h.blueprints())
		}
	}
}
