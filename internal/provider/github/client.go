package github

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"sync"
	"time"

	"github.com/dsswift/redgreen/internal/provider"
)

// API is GitHub's REST root.
const API = "https://api.github.com"

// renewAhead is how long before expiry an installation token is replaced, so
// a sync never straddles the expiry.
const renewAhead = 5 * time.Minute

// appAuth signs as the GitHub App and holds one token per installation.
type appAuth struct {
	api  string
	id   string
	key  *rsa.PrivateKey
	http *http.Client
	mu   sync.Mutex
	held map[string]installationToken
}

type installationToken struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
}

// ParseKey reads a GitHub App private key in PKCS#1 or PKCS#8 PEM.
func ParseKey(pemText string) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode([]byte(pemText))
	if block == nil {
		return nil, errors.New("github app private key is not PEM")
	}
	if k, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return k, nil
	}
	k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("github app private key: %w", err)
	}
	rsaKey, ok := k.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("github app private key is not RSA")
	}
	return rsaKey, nil
}

// jwt is the short-lived token that signs as the app itself.
func (a *appAuth) jwt(now time.Time) (string, error) {
	enc := base64.RawURLEncoding
	header := enc.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	claims, _ := json.Marshal(map[string]any{"iat": now.Add(-60 * time.Second).Unix(), "exp": now.Add(9 * time.Minute).Unix(), "iss": a.id})
	signing := header + "." + enc.EncodeToString(claims)
	sum := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, a.key, crypto.SHA256, sum[:])
	if err != nil {
		return "", err
	}
	return signing + "." + enc.EncodeToString(sig), nil
}

func (a *appAuth) asApp(context.Context) (map[string]string, error) {
	t, err := a.jwt(time.Now())
	if err != nil {
		return nil, err
	}
	return map[string]string{"Authorization": "Bearer " + t, "X-GitHub-Api-Version": "2022-11-28"}, nil
}

// asInstallation authenticates as one installation, asking for a new token
// when the held one is about to expire.
func (a *appAuth) asInstallation(installation string) provider.Auth {
	return func(ctx context.Context) (map[string]string, error) {
		a.mu.Lock()
		defer a.mu.Unlock()
		if t, ok := a.held[installation]; ok && time.Until(t.ExpiresAt) > renewAhead {
			return map[string]string{"Authorization": "Bearer " + t.Token, "X-GitHub-Api-Version": "2022-11-28"}, nil
		}
		jwt, err := a.jwt(time.Now())
		if err != nil {
			return nil, err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.api+"/app/installations/"+installation+"/access_tokens", nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+jwt)
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("User-Agent", "redgreen")
		res, err := a.http.Do(req)
		if err != nil {
			return nil, err
		}
		defer res.Body.Close()
		if res.StatusCode != http.StatusCreated {
			return nil, &provider.HTTPError{Status: res.StatusCode, URL: req.URL.String(), Body: "installation token refused"}
		}
		var t installationToken
		if err := json.NewDecoder(res.Body).Decode(&t); err != nil {
			return nil, err
		}
		if a.held == nil {
			a.held = map[string]installationToken{}
		}
		a.held[installation] = t
		return map[string]string{"Authorization": "Bearer " + t.Token, "X-GitHub-Api-Version": "2022-11-28"}, nil
	}
}

var nextLink = regexp.MustCompile(`<([^>]+)>\s*;\s*rel="next"`)

// next is the URL a Link header marks rel="next", or "".
func next(h http.Header) string {
	if m := nextLink.FindStringSubmatch(h.Get("Link")); m != nil {
		return m[1]
	}
	return ""
}

// page is one page of a list and where the next one is.
type page[T any] struct {
	Items []T    `json:"items"`
	Next  string `json:"next,omitempty"`
}

// list reads every page of a list answered as a JSON array.
func list[T any](ctx context.Context, f *provider.Fetcher, url string) ([]T, error) {
	return listOf(ctx, f, url, func(body []byte) ([]T, error) {
		var items []T
		err := json.Unmarshal(body, &items)
		return items, err
	})
}

// listOf reads every page of a list, pulling the items out of each page with items.
func listOf[T any](ctx context.Context, f *provider.Fetcher, url string, items func([]byte) ([]T, error)) ([]T, error) {
	var out []T
	for url != "" {
		p, err := provider.Get(ctx, f, url, func(a provider.Answer) (page[T], error) {
			got, err := items(a.Body)
			return page[T]{Items: got, Next: next(a.Header)}, err
		})
		if err != nil {
			return nil, err
		}
		out = append(out, p.Items...)
		url = p.Next
	}
	return out, nil
}

// one reads one JSON answer.
func one[T any](ctx context.Context, f *provider.Fetcher, url string) (T, error) {
	return provider.Get(ctx, f, url, provider.JSON[T])
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }
