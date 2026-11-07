// Package providertest serves recorded forge answers for the provider tests.
package providertest

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/dsswift/redgreen/internal/state"
)

// Route is one recorded answer. File is read from testdata; Status defaults to
// 200; ETag, when set, is sent and answered with 304 when it comes back.
type Route struct {
	File   string
	Body   string
	Status int
	ETag   string
	Header map[string]string
}

// Forge is a fake forge. Routes are keyed "METHOD /path", or with "?query" to
// tell two answers of one path apart.
type Forge struct {
	*httptest.Server
	mu          sync.Mutex
	Routes      map[string]Route
	Requests    []string
	NotModified int
}

// New serves routes until the test ends.
func New(t *testing.T, routes map[string]Route) *Forge {
	t.Helper()
	f := &Forge{Routes: routes}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.Requests = append(f.Requests, r.Method+" "+r.URL.RequestURI())
		route, ok := f.Routes[r.Method+" "+r.URL.Path+"?"+r.URL.RawQuery]
		if !ok {
			route, ok = f.Routes[r.Method+" "+r.URL.Path]
		}
		if !ok {
			http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
			return
		}
		for k, v := range route.Header {
			w.Header().Set(k, v)
		}
		if route.ETag != "" {
			w.Header().Set("ETag", route.ETag)
			if r.Header.Get("If-None-Match") == route.ETag {
				f.NotModified++
				w.WriteHeader(http.StatusNotModified)
				return
			}
		}
		body := []byte(route.Body)
		if route.File != "" {
			var err error
			if body, err = os.ReadFile(filepath.Join("testdata", route.File)); err != nil {
				t.Errorf("fixture %s: %v", route.File, err)
			}
		}
		status := route.Status
		if status == 0 {
			status = http.StatusOK
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write(body)
	}))
	t.Cleanup(f.Close)
	return f
}

// Store is a fresh state in the test's temporary folder.
func Store(t *testing.T) *state.Store {
	t.Helper()
	s, err := state.Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}
