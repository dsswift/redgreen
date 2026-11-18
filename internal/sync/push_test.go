package sync

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	gosync "sync"
	"testing"

	"github.com/dsswift/orrery/sdk"
	"github.com/dsswift/redgreen/internal/catalog"
	"github.com/dsswift/redgreen/internal/provider"
	"github.com/dsswift/redgreen/internal/provider/providertest"
	"github.com/dsswift/redgreen/internal/services"
	"github.com/dsswift/redgreen/internal/telemetry"
)

// hub is a fake Orrery with its write rules: a run's write replaces every
// field its source wrote before, a patch merges, and a field the hub does
// not define refuses the whole entity.
type hub struct {
	*httptest.Server
	mu       gosync.Mutex
	entities map[string]map[string]any
	lacks    map[string]bool
}

func newHub(t *testing.T, lacks ...string) *hub {
	t.Helper()
	h := &hub{entities: map[string]map[string]any{}, lacks: map[string]bool{}}
	for _, f := range lacks {
		h.lacks[f] = true
	}
	h.Server = httptest.NewServer(http.HandlerFunc(h.serve))
	t.Cleanup(h.Close)
	return h
}

func (h *hub) serve(w http.ResponseWriter, r *http.Request) {
	h.mu.Lock()
	defer h.mu.Unlock()
	body, _ := io.ReadAll(r.Body)
	w.Header().Set("Content-Type", "application/json")
	path := r.URL.Path
	switch {
	case r.Method == http.MethodPut && path == "/v1/ingest/blueprints":
		_, _ = w.Write([]byte(`{"created":[],"extended":[],"kept":[],"rejected":[]}`))
	case r.Method == http.MethodPost && path == "/v1/ingest/runs":
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "run", "sightings": false})
	case r.Method == http.MethodPut && strings.HasSuffix(path, "/entities"):
		var in struct{ Entities []sdk.Entity }
		_ = json.Unmarshal(body, &in)
		rejected := []sdk.WriteError{}
		for _, e := range in.Entities {
			if f := h.refused(e.Fields); f != "" {
				rejected = append(rejected, sdk.WriteError{Blueprint: e.Blueprint, Key: e.Key, Field: f, Reason: "not a field"})
				continue
			}
			h.entities[e.ID()] = clone(e.Fields)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"accepted": len(in.Entities) - len(rejected), "rejected": rejected})
	case r.Method == http.MethodPost && strings.HasSuffix(path, "/commit"):
		_, _ = w.Write([]byte(`{"id":"run","state":"committed","counts":{}}`))
	case r.Method == http.MethodPost && strings.HasSuffix(path, "/fail"):
		_, _ = w.Write([]byte(`{}`))
	case r.Method == http.MethodPatch && strings.HasPrefix(path, "/v1/ingest/entities/"):
		var e sdk.Entity
		_ = json.Unmarshal(body, &e)
		parts := strings.SplitN(strings.TrimPrefix(r.URL.EscapedPath(), "/v1/ingest/entities/"), "/", 2)
		bp, _ := url.PathUnescape(parts[0])
		key, _ := url.PathUnescape(parts[1])
		if f := h.refused(e.Fields); f != "" {
			w.WriteHeader(http.StatusUnprocessableEntity)
			_ = json.NewEncoder(w).Encode(map[string]any{"message": "refused", "errors": []sdk.WriteError{{Blueprint: bp, Key: key, Field: f, Reason: "not a field"}}})
			return
		}
		id := bp + ":" + key
		if h.entities[id] == nil {
			h.entities[id] = map[string]any{}
		}
		for k, v := range e.Fields {
			h.entities[id][k] = v
		}
		_, _ = w.Write([]byte(`{}`))
	default:
		http.Error(w, `{"message":"not found"}`, http.StatusNotFound)
	}
}

func (h *hub) refused(fields map[string]any) string {
	for k := range fields {
		if h.lacks[k] {
			return k
		}
	}
	return ""
}

func (h *hub) fields(blueprint, key string) map[string]any {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.entities[blueprint+":"+key]
}

func clone(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// described is an engine over one tracked repository whose orrery.yaml
// declares fields for the service at its root, writing to h. key is the
// repository's, and the service's.
func described(t *testing.T, h *hub, fields map[string]any) (*Engine, provider.Account, string) {
	t.Helper()
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	tel, err := telemetry.Setup(ctx, "", "", "test", log)
	if err != nil {
		t.Fatal(err)
	}
	e := New(Options{Store: providertest.Store(t), Push: sdk.New(h.URL, "token", sdk.WithLogger(log)), Tel: tel, Log: log})
	a := provider.Account{ID: "1", Provider: provider.GitLab, Login: "example"}
	repo := &provider.Repo{ID: "7", Provider: provider.GitLab, AccountLogin: "example", Name: "service", FullName: "example/group/service",
		URL: "https://forge.example.com/example/group/service", DefaultBranch: "main"}
	key := catalog.RepoKey(repo.Provider, repo.FullName)
	found := &services.Found{
		Services:     []services.Service{{ID: "", Name: "service", Type: "app", Language: "Go"}},
		Descriptions: []services.Description{{Descriptor: services.Descriptor{Service: key, Fields: fields}, File: "orrery.yaml"}},
	}
	e.repos[key] = &tracked{account: a, ref: provider.RepoRef{ID: repo.ID, FullName: repo.FullName}, repo: repo, found: found, ok: true}
	return e, a, key
}

func TestASweepAfterAPushKeepsTheDeclaredFields(t *testing.T) {
	h := newHub(t)
	declared := map[string]any{"description": "Takes orders", "lifecycle": "production", "tier": "2", "deploy_component": "service-api"}
	e, a, key := described(t, h, declared)
	ctx := context.Background()

	e.pushRepo(ctx, key)
	e.pushAccount(ctx, a, []string{key})

	got := h.fields(catalog.Service, key)
	for k, v := range declared {
		if got[k] != v {
			t.Errorf("after the sweep, %s = %v, want %v (service holds %v)", k, got[k], v, got)
		}
	}
	if got["origin"] != "built" {
		t.Errorf("the service lost what redgreen reads itself: %v", got)
	}

	e.pushAccount(ctx, a, []string{key})
	if got := h.fields(catalog.Service, key); got["tier"] != "2" {
		t.Errorf("a second sweep blanked the declared fields: %v", got)
	}
}

func TestAPlaceTheHubRefusesLeavesTheServiceStanding(t *testing.T) {
	h := newHub(t, "tier")
	e, a, key := described(t, h, map[string]any{"lifecycle": "production", "tier": "2"})
	ctx := context.Background()

	e.pushAccount(ctx, a, []string{key})

	got := h.fields(catalog.Service, key)
	if got == nil || got["origin"] != "built" {
		t.Fatalf("a place the hub refuses took its service with it: %v", got)
	}
	if _, ok := got["tier"]; ok {
		t.Errorf("the hub lacks tier, yet the service holds it: %v", got)
	}
}
