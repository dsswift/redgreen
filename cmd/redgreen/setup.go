package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"html/template"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/dsswift/redgreen/internal/config"
	"github.com/dsswift/redgreen/internal/provider/gitlab"
)

// The one-time forge setup runs on the owner's machine. Each flow serves a
// page on 127.0.0.1, sends the browser to the forge, takes the answer back on
// a local callback, and prints what goes into Key Vault.

// callback serves the local page and waits for the forge to send the browser
// back to /callback. It returns the query the forge sent.
func callback(port int, start http.HandlerFunc) (url.Values, error) {
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return nil, err
	}
	got := make(chan url.Values, 1)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", start)
	mux.HandleFunc("GET /callback", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, "Done. Go back to the terminal.")
		got <- r.URL.Query()
	})
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = server.Serve(ln) }()
	defer server.Close()
	fmt.Printf("Open http://127.0.0.1:%d in a browser.\n", port)
	select {
	case q := <-got:
		return q, nil
	case <-time.After(15 * time.Minute):
		return nil, errors.New("no answer from the forge in 15 minutes")
	}
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

var manifestPage = template.Must(template.New("m").Parse(`<!doctype html><title>redgreen setup</title>
<form method="post" action="{{.Action}}"><input type="hidden" name="manifest" value="{{.Manifest}}">
<p>Creates the GitHub App redgreen reads with. Read-only.</p><button>Create the app on GitHub</button></form>`))

// setupGitHub runs the GitHub App manifest flow, or reads the app an old
// store created through its settings page.
func setupGitHub(args []string) error {
	fs := flag.NewFlagSet("setup github", flag.ContinueOnError)
	org := fs.String("org", "", "create the app under this organization instead of your account")
	port := fs.Int("port", 8765, "local port for the callback")
	name := fs.String("name", "redgreen", "the app's name on GitHub")
	fromStore := fs.String("from-store", "", "read the app from an old redgreen store instead, with SECRET_KEY")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *fromStore != "" {
		return githubFromStore(*fromStore, os.Getenv("SECRET_KEY"))
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if !strings.HasPrefix(cfg.PublicURL, "https://") {
		return errors.New("set PUBLIC_URL to where GitHub reaches redgreen's webhooks, https://")
	}
	manifest, _ := json.Marshal(map[string]any{
		"name": *name, "url": cfg.PublicURL, "description": "The forges' door into Orrery. Read-only.", "public": true,
		"redirect_url":    fmt.Sprintf("http://127.0.0.1:%d/callback", *port),
		"hook_attributes": map[string]any{"url": cfg.PublicURL + "/api/webhooks/github", "active": true},
		"default_permissions": map[string]string{
			"actions": "read", "administration": "read", "checks": "read", "contents": "read", "deployments": "read", "issues": "read",
			"metadata": "read", "pull_requests": "read", "statuses": "read", "vulnerability_alerts": "read", "security_events": "read",
		},
		"default_events": []string{"check_suite", "code_scanning_alert", "create", "delete", "dependabot_alert", "issues", "pull_request", "push", "release", "repository", "workflow_run", "workflow_job", "deployment_status"},
	})
	action := "https://github.com/settings/apps/new"
	if *org != "" {
		action = "https://github.com/organizations/" + url.PathEscape(*org) + "/settings/apps/new"
	}
	q, err := callback(*port, func(w http.ResponseWriter, _ *http.Request) {
		_ = manifestPage.Execute(w, map[string]string{"Action": action, "Manifest": string(manifest)})
	})
	if err != nil {
		return err
	}
	code := q.Get("code")
	if code == "" {
		return errors.New("github sent no code")
	}
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, "https://api.github.com/app-manifests/"+url.PathEscape(code)+"/conversions", nil)
	req.Header.Set("Accept", "application/vnd.github+json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	var app struct {
		ID            int64  `json:"id"`
		Slug          string `json:"slug"`
		PEM           string `json:"pem"`
		WebhookSecret string `json:"webhook_secret"`
	}
	if res.StatusCode != http.StatusCreated {
		return fmt.Errorf("github refused the manifest code: %d", res.StatusCode)
	}
	if err := json.NewDecoder(res.Body).Decode(&app); err != nil {
		return err
	}
	printGitHub(fmt.Sprint(app.ID), app.Slug, app.PEM, app.WebhookSecret)
	return nil
}

func printGitHub(id, slug, pem, secret string) {
	fmt.Println("Put these in Key Vault:")
	fmt.Println("GITHUB_APP_ID=" + id)
	fmt.Println("GITHUB_APP_WEBHOOK_SECRET=" + secret)
	fmt.Println("GITHUB_APP_PRIVATE_KEY=" + strings.ReplaceAll(strings.TrimSpace(pem), "\n", `\n`))
	if slug != "" {
		fmt.Printf("Install it on each account: https://github.com/apps/%s/installations/new\n", slug)
		fmt.Println("Then list each account in GITHUB_ALLOWED_ACCOUNTS.")
	}
}

// githubFromStore reads the app the old web app created through its setup
// flow, and the accounts it was switched on for.
func githubFromStore(path, key string) error {
	s, err := openOldStore(path, key)
	if err != nil {
		return err
	}
	defer s.Close()
	var app struct {
		AppID         int64  `json:"appId"`
		Slug          string `json:"slug"`
		PrivateKey    string `json:"privateKey"`
		WebhookSecret string `json:"webhookSecret"`
	}
	if err := s.setting("github.app", &app); err != nil {
		return err
	}
	pem, err := s.open("private key", app.PrivateKey)
	if err != nil {
		return err
	}
	secret, err := s.open("webhook secret", app.WebhookSecret)
	if err != nil {
		return err
	}
	printGitHub(fmt.Sprint(app.AppID), app.Slug, pem, secret)
	accounts, err := s.activeAccounts("github")
	if err != nil {
		return err
	}
	fmt.Println("GITHUB_ALLOWED_ACCOUNTS=" + strings.Join(accounts, ","))
	return nil
}

// setupGitLab authorizes the OAuth application once and prints the refresh
// token redgreen starts from. The application's redirect URI must be
// http://127.0.0.1:<port>/callback, with the read_api scope.
func setupGitLab(args []string) error {
	fs := flag.NewFlagSet("setup gitlab", flag.ContinueOnError)
	port := fs.Int("port", 8765, "local port for the callback")
	fromStore := fs.String("from-store", "", "read the OAuth application and its tokens from an old redgreen store instead, with SECRET_KEY")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *fromStore != "" {
		s, err := openOldStore(*fromStore, os.Getenv("SECRET_KEY"))
		if err != nil {
			return err
		}
		defer s.Close()
		return gitlabFromStore(os.Stdout, s)
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if cfg.GitLabClientID == "" || cfg.GitLabClientSecret == "" {
		return errors.New("set GITLAB_CLIENT_ID and GITLAB_CLIENT_SECRET to the OAuth application's")
	}
	redirect := fmt.Sprintf("http://127.0.0.1:%d/callback", *port)
	st := randomHex(16)
	authorize := cfg.GitLabURL + "/oauth/authorize?" + url.Values{"client_id": {cfg.GitLabClientID}, "redirect_uri": {redirect}, "response_type": {"code"}, "state": {st}, "scope": {"read_api"}}.Encode()
	q, err := callback(*port, func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, authorize, http.StatusFound) })
	if err != nil {
		return err
	}
	if q.Get("state") != st {
		return errors.New("the authorization did not start here; start again")
	}
	if q.Get("code") == "" {
		return fmt.Errorf("gitlab sent no code: %s", q.Get("error_description"))
	}
	tokens, err := gitlab.Exchange(context.Background(), http.DefaultClient, cfg.GitLabURL, url.Values{"grant_type": {"authorization_code"}, "code": {q.Get("code")},
		"client_id": {cfg.GitLabClientID}, "client_secret": {cfg.GitLabClientSecret}, "redirect_uri": {redirect}})
	if err != nil {
		return err
	}
	fmt.Println("Put these in Key Vault:")
	fmt.Println("GITLAB_REFRESH_TOKEN=" + tokens.RefreshToken)
	if cfg.GitLabWebhookSecret == "" {
		fmt.Println("GITLAB_WEBHOOK_SECRET=" + randomHex(24))
	}
	if cfg.SecretKey == "" {
		key := make([]byte, 32)
		_, _ = rand.Read(key)
		fmt.Println("SECRET_KEY=" + base64.StdEncoding.EncodeToString(key))
	}
	fmt.Println("redgreen replaces the refresh token on first use and keeps the new one in its state, sealed with SECRET_KEY.")
	fmt.Println("Point each project or group webhook at " + cfg.PublicURL + "/api/webhooks/gitlab with the secret token above.")
	return nil
}

// setupAzureDevOps reads the service principal from an old redgreen store.
// A new one is registered in Entra, not here; see the README.
func setupAzureDevOps(args []string) error {
	fs := flag.NewFlagSet("setup azuredevops", flag.ContinueOnError)
	fromStore := fs.String("from-store", "", "the old redgreen store to read, with SECRET_KEY")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *fromStore == "" {
		return errors.New("setup azuredevops reads an old store: pass --from-store")
	}
	s, err := openOldStore(*fromStore, os.Getenv("SECRET_KEY"))
	if err != nil {
		return err
	}
	defer s.Close()
	return azureDevOpsFromStore(os.Stdout, s)
}
