package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/dsswift/redgreen/internal/state"
	"github.com/dsswift/redgreen/internal/telemetry"
)

// HTTPError is any answer that is not usable. Status lets a caller read 403
// and 404 as "this feed is not available".
type HTTPError struct {
	Status int
	URL    string
	Body   string
}

func (e *HTTPError) Error() string {
	body := e.Body
	if len(body) > 200 {
		body = body[:200]
	}
	return fmt.Sprintf("%d for %s: %s", e.Status, e.URL, body)
}

// IsStatus says whether err is an HTTPError with one of codes.
func IsStatus(err error, codes ...int) bool {
	var h *HTTPError
	if !errors.As(err, &h) {
		return false
	}
	for _, c := range codes {
		if h.Status == c {
			return true
		}
	}
	return false
}

// Auth returns the headers that authenticate one request. It is called per
// request, so a token can be renewed underneath.
type Auth func(ctx context.Context) (map[string]string, error)

// Fetcher makes conditional requests for one credential. When an answer
// carried an ETag, the next request for the same URL sends it back, and a 304
// reuses the facts read from the first answer. The facts are kept, never the
// body, and they are keyed by credential so two never share an answer.
type Fetcher struct {
	Store  *state.Store
	HTTP   *http.Client
	Scope  string
	Auth   Auth
	Quotas *Quotas
	Log    *slog.Logger
	// Redirects is an error when set: a redirect means the forge asked for a sign-in.
	Redirects bool
}

// Answer is what a parser gets: the body and the headers that came with it.
type Answer struct {
	Body   []byte
	Header http.Header
}

// Get does a conditional GET of url and returns the facts parse read from the
// answer, or the kept facts on a 304.
func Get[T any](ctx context.Context, f *Fetcher, url string, parse func(Answer) (T, error)) (T, error) {
	var zero T
	key := f.Scope + " " + url
	kept, err := f.Store.Response(ctx, key)
	if err != nil {
		return zero, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return zero, err
	}
	if err := f.authorize(ctx, req); err != nil {
		return zero, err
	}
	req.Header.Set("Accept", "application/json")
	if kept != nil {
		req.Header.Set("If-None-Match", kept.ETag)
	}
	res, err := f.client().Do(req)
	if err != nil {
		return zero, err
	}
	defer res.Body.Close()
	f.Quotas.Record(f.Scope, res.Header)
	if res.StatusCode == http.StatusNotModified && kept != nil {
		var v T
		if err := json.Unmarshal(kept.Facts, &v); err == nil {
			_ = f.Store.TouchResponse(ctx, key, time.Now())
			f.Log.DebugContext(ctx, "served from kept facts", "url", url)
			return v, nil
		}
		f.Log.WarnContext(ctx, "kept facts unreadable; asking again in full", "url", url)
		return getFresh(ctx, f, url, parse)
	}
	return read(ctx, f, key, url, res, kept != nil, parse)
}

// Once reads an answer that is never asked for again, such as a tree at one
// commit: it sends no ETag and keeps nothing, so the state does not grow with
// every commit.
func Once[T any](ctx context.Context, f *Fetcher, url string, parse func(Answer) (T, error)) (T, error) {
	return fetchOnce(ctx, f, url, parse, false)
}

// getFresh asks again without the ETag, for when the kept facts cannot be read.
func getFresh[T any](ctx context.Context, f *Fetcher, url string, parse func(Answer) (T, error)) (T, error) {
	return fetchOnce(ctx, f, url, parse, true)
}

func fetchOnce[T any](ctx context.Context, f *Fetcher, url string, parse func(Answer) (T, error), keep bool) (T, error) {
	var zero T
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return zero, err
	}
	if err := f.authorize(ctx, req); err != nil {
		return zero, err
	}
	req.Header.Set("Accept", "application/json")
	res, err := f.client().Do(req)
	if err != nil {
		return zero, err
	}
	defer res.Body.Close()
	f.Quotas.Record(f.Scope, res.Header)
	key := f.Scope + " " + url
	if !keep {
		key = ""
	}
	return read(ctx, f, key, url, res, false, parse)
}

func read[T any](ctx context.Context, f *Fetcher, key, url string, res *http.Response, conditional bool, parse func(Answer) (T, error)) (T, error) {
	var zero T
	if f.Redirects && (res.StatusCode == 203 || (res.StatusCode >= 300 && res.StatusCode < 400)) {
		return zero, &HTTPError{Status: http.StatusUnauthorized, URL: url, Body: "the forge asked for a sign-in; is the application a member of it?"}
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, 32<<20))
	if err != nil {
		return zero, err
	}
	if res.StatusCode < 200 || res.StatusCode > 299 {
		return zero, &HTTPError{Status: res.StatusCode, URL: url, Body: string(body)}
	}
	v, err := parse(Answer{Body: body, Header: res.Header})
	if err != nil {
		return zero, fmt.Errorf("reading %s: %w", url, err)
	}
	etag := res.Header.Get("ETag")
	f.Log.DebugContext(ctx, "fetched", "url", url, "status", res.StatusCode, "etag", etag != "", "conditional", conditional)
	if etag != "" && key != "" {
		facts, err := json.Marshal(v)
		if err == nil {
			err = f.Store.PutResponse(ctx, key, etag, facts, time.Now())
		}
		if err != nil {
			f.Log.WarnContext(ctx, "facts not kept; the next read asks in full", "url", url, "error", err)
		}
	}
	return v, nil
}

// Send makes one request that is never conditional, such as a POST.
func (f *Fetcher) Send(ctx context.Context, method, url string, body any, out any) error {
	var payload io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		payload = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, payload)
	if err != nil {
		return err
	}
	if err := f.authorize(ctx, req); err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := f.client().Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	f.Quotas.Record(f.Scope, res.Header)
	b, err := io.ReadAll(io.LimitReader(res.Body, 32<<20))
	if err != nil {
		return err
	}
	if res.StatusCode < 200 || res.StatusCode > 299 {
		return &HTTPError{Status: res.StatusCode, URL: url, Body: string(b)}
	}
	if out == nil || len(b) == 0 {
		return nil
	}
	return json.Unmarshal(b, out)
}

func (f *Fetcher) authorize(ctx context.Context, req *http.Request) error {
	req.Header.Set("User-Agent", "redgreen")
	if f.Auth == nil {
		return nil
	}
	h, err := f.Auth(ctx)
	if err != nil {
		return err
	}
	for k, v := range h {
		req.Header.Set(k, v)
	}
	return nil
}

func (f *Fetcher) client() *http.Client {
	if !f.Redirects {
		return f.HTTP
	}
	c := *f.HTTP
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &c
}

// JSON is a parser that decodes the body into T.
func JSON[T any](a Answer) (T, error) {
	var v T
	err := json.Unmarshal(a.Body, &v)
	return v, err
}

// Quotas keeps the rate-limit headers last seen per credential.
type Quotas struct {
	Provider string
	// Remaining, Limit name the headers the forge sends.
	Remaining, Limit string
	mu               sync.Mutex
	seen             map[string]telemetry.Quota
}

// Record keeps one answer's counters. An answer without them changes nothing.
func (q *Quotas) Record(scope string, h http.Header) {
	if q == nil {
		return
	}
	r, errR := strconv.ParseFloat(h.Get(q.Remaining), 64)
	l, errL := strconv.ParseFloat(h.Get(q.Limit), 64)
	if errR != nil || errL != nil {
		return
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.seen == nil {
		q.seen = map[string]telemetry.Quota{}
	}
	q.seen[scope] = telemetry.Quota{Provider: q.Provider, Remaining: r, Limit: l}
}

// List returns every credential's last counters.
func (q *Quotas) List() []telemetry.Quota {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := make([]telemetry.Quota, 0, len(q.seen))
	for _, v := range q.seen {
		out = append(out, v)
	}
	return out
}
