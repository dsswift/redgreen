// Package config reads redgreen's settings from the environment.
package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config is every setting, read once at start.
type Config struct {
	Port         int
	DataDir      string
	PublicURL    string
	SyncInterval time.Duration
	Concurrency  int
	BotAuthors   []string
	// SecretKey seals credentials kept in the state: base64 of 32 bytes.
	SecretKey string

	OrreryURL       string
	OrreryToken     string
	OrreryPushToken string
	OrreryReadToken string

	OtelEndpoint string
	OtelService  string

	GitHubAppID         string
	GitHubAppPrivateKey string
	GitHubWebhookSecret string
	GitHubAllowed       []string
	GitHubToken         string
	GitHubTokenAccounts []string
	GitLabURL           string
	GitLabAllowed       []string
	GitLabClientID      string
	GitLabClientSecret  string
	GitLabRefreshToken  string
	GitLabWebhookSecret string
	GitLabToken         string
	GitLabTokenAccounts []string
	AzureTenantID       string
	AzureClientID       string
	AzureClientSecret   string
	AzureOrganizations  []string
}

// Load reads the environment. It fails on a value it cannot use.
func Load() (Config, error) {
	return Parse(os.Getenv)
}

// Parse reads settings through get, so a test can hand in its own.
func Parse(get func(string) string) (Config, error) {
	var errs []error
	str := func(name, fallback string) string {
		if v := strings.TrimSpace(get(name)); v != "" {
			return v
		}
		return fallback
	}
	num := func(name string, fallback int) int {
		v := strings.TrimSpace(get(name))
		if v == "" {
			return fallback
		}
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			errs = append(errs, fmt.Errorf("%s: %q is not a positive whole number", name, v))
			return fallback
		}
		return n
	}
	link := func(name, fallback string) string {
		v := str(name, fallback)
		if v == "" {
			return ""
		}
		if u, err := url.Parse(v); err != nil || u.Scheme == "" || u.Host == "" {
			errs = append(errs, fmt.Errorf("%s: %q is not a URL", name, v))
		}
		return strings.TrimRight(v, "/")
	}
	list := func(name string) []string {
		var out []string
		for _, v := range strings.Split(get(name), ",") {
			if v = strings.TrimSpace(v); v != "" {
				out = append(out, v)
			}
		}
		return out
	}
	c := Config{
		Port:         num("PORT", 8080),
		DataDir:      str("DATA_DIR", "./data"),
		PublicURL:    link("PUBLIC_URL", "http://localhost:8080"),
		SyncInterval: time.Duration(num("SYNC_INTERVAL_MINUTES", 5)) * time.Minute,
		Concurrency:  num("SYNC_CONCURRENCY", 4),
		BotAuthors:   list("BOT_AUTHORS"),
		SecretKey:    str("SECRET_KEY", ""),

		OrreryURL:       link("ORRERY_URL", ""),
		OrreryToken:     str("ORRERY_TOKEN", ""),
		OrreryPushToken: str("ORRERY_PUSH_TOKEN", ""),
		OrreryReadToken: str("ORRERY_READ_TOKEN", ""),

		OtelEndpoint: link("OTEL_EXPORTER_OTLP_ENDPOINT", ""),
		OtelService:  str("OTEL_SERVICE_NAME", ""),

		GitHubAppID:         str("GITHUB_APP_ID", ""),
		GitHubAppPrivateKey: strings.ReplaceAll(str("GITHUB_APP_PRIVATE_KEY", ""), `\n`, "\n"),
		GitHubWebhookSecret: str("GITHUB_APP_WEBHOOK_SECRET", ""),
		GitHubAllowed:       list("GITHUB_ALLOWED_ACCOUNTS"),
		GitHubToken:         str("GITHUB_TOKEN", ""),
		GitHubTokenAccounts: list("GITHUB_TOKEN_ACCOUNTS"),
		GitLabURL:           link("GITLAB_URL", "https://gitlab.com"),
		GitLabAllowed:       list("GITLAB_ALLOWED_ACCOUNTS"),
		GitLabClientID:      str("GITLAB_CLIENT_ID", ""),
		GitLabClientSecret:  str("GITLAB_CLIENT_SECRET", ""),
		GitLabRefreshToken:  str("GITLAB_REFRESH_TOKEN", ""),
		GitLabWebhookSecret: str("GITLAB_WEBHOOK_SECRET", ""),
		GitLabToken:         str("GITLAB_TOKEN", ""),
		GitLabTokenAccounts: list("GITLAB_TOKEN_ACCOUNTS"),
		AzureTenantID:       str("AZURE_DEVOPS_TENANT_ID", ""),
		AzureClientID:       str("AZURE_DEVOPS_CLIENT_ID", ""),
		AzureClientSecret:   str("AZURE_DEVOPS_CLIENT_SECRET", ""),
		AzureOrganizations:  list("AZURE_DEVOPS_ORGANIZATIONS"),
	}
	if c.GitHubToken != "" && len(c.GitHubTokenAccounts) == 0 {
		errs = append(errs, errors.New("GITHUB_TOKEN_ACCOUNTS: required with GITHUB_TOKEN"))
	}
	if c.GitLabToken != "" && len(c.GitLabTokenAccounts) == 0 {
		errs = append(errs, errors.New("GITLAB_TOKEN_ACCOUNTS: required with GITLAB_TOKEN"))
	}
	if c.AzureClientSecret != "" && len(c.AzureOrganizations) == 0 {
		errs = append(errs, errors.New("AZURE_DEVOPS_ORGANIZATIONS: required with the Azure DevOps service principal"))
	}
	if err := errors.Join(errs...); err != nil {
		return Config{}, fmt.Errorf("invalid configuration: %w", err)
	}
	return c, nil
}
