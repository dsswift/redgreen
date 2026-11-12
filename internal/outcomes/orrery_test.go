package outcomes

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/dsswift/orrery/sdk"
)

// hub answers list reads from canned entities by blueprint, and keeps every question asked.
type hub struct {
	mu     sync.Mutex
	asked  []url.Values
	answer map[string][]map[string]any
}

func (h *hub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.mu.Lock()
	q := r.URL.Query()
	h.asked = append(h.asked, q)
	list := h.answer[q.Get("blueprint")]
	h.mu.Unlock()
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

func read(t *testing.T, h *hub) ([]HubDeployment, []HubApp, []HubWorkload, []HubIncident) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	syncs, apps, workloads, incidents, err := Reader{Client: sdk.New(srv.URL, "token")}.Read(context.Background(), time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	return syncs, apps, workloads, incidents
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
