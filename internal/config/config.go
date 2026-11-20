// Package config reads redgreen's settings from the environment.
package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// Config is every setting, read once at start.
type Config struct {
	Port         int
	DataDir      string
	PublicURL    string
	SyncInterval time.Duration
	Concurrency  int
	BotAuthors   []string
	// ProbePaths are files or folders, from the repository root, looked for
	// on each default branch. Empty looks for none.
	ProbePaths []string
	// ComponentFields are the orrery.yaml fields whose value names a service
	// in its deployments' environments and jobs.
	ComponentFields []string
	// ArgoServices are the operator's links from an Argo CD app, by its key
	// or name, or from the source its manifests are read from, to the
	// service it deploys.
	ArgoServices map[string]string
	// The flaky rule: the newest finished runs read for evidence, the passes
	// in a row that clear it, and the oldest evidence that counts, in days.
	FlakyWindowRuns int
	FlakyClearRuns  int
	FlakyMaxAgeDays int
	// SecretKey seals credentials kept in the state: base64 of 32 bytes.
	SecretKey string

	OrreryURL       string
	OrreryToken     string
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
	listOr := func(name string, fallback []string) []string {
		if v := list(name); len(v) > 0 {
			return v
		}
		return fallback
	}
	c := Config{
		Port:         num("PORT", 8080),
		DataDir:      str("DATA_DIR", "./data"),
		PublicURL:    link("PUBLIC_URL", "http://localhost:8080"),
		SyncInterval: time.Duration(num("SYNC_INTERVAL_MINUTES", 5)) * time.Minute,
		Concurrency:  num("SYNC_CONCURRENCY", 4),
		BotAuthors:   list("BOT_AUTHORS"),
		ProbePaths:   probePaths(list("PROBE_PATHS"), &errs),

		ComponentFields: listOr("DEPLOY_COMPONENT_FIELDS", []string{"deploy_component"}),
		ArgoServices:    links("ARGOCD_SERVICES", get("ARGOCD_SERVICES"), &errs),

		FlakyWindowRuns: num("FLAKY_WINDOW_RUNS", 20),
		FlakyClearRuns:  num("FLAKY_CLEAR_RUNS", 5),
		FlakyMaxAgeDays: num("FLAKY_MAX_AGE_DAYS", 180),
		SecretKey:       str("SECRET_KEY", ""),

		OrreryURL:       link("ORRERY_URL", ""),
		OrreryToken:     str("ORRERY_TOKEN", ""),
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

// links reads `<argocd app or source>=<service key>` pairs, split by commas
// or line breaks, so a long list can be written one pair a line. A source
// may hold an "=" of its own; a service key never does. A pair with either
// side empty, or one side linked to two services, is an error.
func links(name, raw string, errs *[]error) map[string]string {
	out := map[string]string{}
	seen := map[string]string{}
	for _, pair := range strings.FieldsFunc(raw, func(r rune) bool { return r == ',' || r == '\n' || r == '\r' }) {
		if pair = strings.TrimSpace(pair); pair == "" {
			continue
		}
		i := strings.LastIndex(pair, "=")
		from, service := "", ""
		if i >= 0 {
			from, service = strings.TrimSpace(pair[:i]), strings.TrimSpace(pair[i+1:])
		}
		if from == "" || service == "" || strings.ContainsFunc(from+service, unicode.IsSpace) {
			*errs = append(*errs, fmt.Errorf("%s: %q is not <argocd app or source>=<service key>", name, pair))
			continue
		}
		if had, ok := seen[strings.ToLower(from)]; ok {
			if !strings.EqualFold(out[had], service) {
				*errs = append(*errs, fmt.Errorf("%s: %q is linked to %q and %q", name, from, out[had], service))
			}
			continue
		}
		seen[strings.ToLower(from)] = from
		out[from] = service
	}
	return out
}

// probePaths cleans each path to the form a forge lists, slash separated
// from the root, and drops repeats. A path that leaves the root is an error.
func probePaths(in []string, errs *[]error) []string {
	var out []string
	for _, p := range in {
		clean := strings.Trim(path.Clean("/"+strings.TrimSpace(p)), "/")
		if clean == "" || slices.Contains(strings.Split(p, "/"), "..") {
			*errs = append(*errs, fmt.Errorf("PROBE_PATHS: %q is not a path inside the repository", p))
			continue
		}
		if !slices.Contains(out, clean) {
			out = append(out, clean)
		}
	}
	return out
}
