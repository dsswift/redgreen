package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/dsswift/redgreen/internal/provider"
	_ "modernc.org/sqlite"
)

// oldStore reads what the old web app kept in its settings table, sealed with
// its SECRET_KEY, so an upgrade carries the forge credentials over.
type oldStore struct {
	db  *sql.DB
	box *provider.Box
}

func openOldStore(path, key string) (*oldStore, error) {
	box, err := provider.NewBox(key)
	if err != nil {
		return nil, fmt.Errorf("SECRET_KEY: %w", err)
	}
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		return nil, err
	}
	return &oldStore{db: db, box: box}, nil
}

func (s *oldStore) Close() error { return s.db.Close() }

// setting decodes one setting's JSON value into v.
func (s *oldStore) setting(key string, v any) error {
	var raw string
	if err := s.db.QueryRow(`SELECT value FROM settings WHERE key = ?`, key).Scan(&raw); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("the store has no %s", key)
		}
		return err
	}
	return json.Unmarshal([]byte(raw), v)
}

func (s *oldStore) open(name, sealed string) (string, error) {
	plain, err := s.box.Open(sealed)
	if err != nil {
		return "", fmt.Errorf("%s: %w (is SECRET_KEY the old store's?)", name, err)
	}
	return plain, nil
}

// activeAccounts lists the logins the old app was switched on for, for the
// provider's allowed-accounts setting.
func (s *oldStore) activeAccounts(provider string) ([]string, error) {
	rows, err := s.db.Query(`SELECT login FROM accounts WHERE provider = ? AND status = 'active' ORDER BY login`, provider)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var login string
		if err := rows.Scan(&login); err != nil {
			return nil, err
		}
		out = append(out, login)
	}
	return out, rows.Err()
}

// gitlabFromStore prints the OAuth application, its current refresh token,
// and the webhook secret. GitLab rotates a refresh token when it is used, so
// the old app must be stopped before this runs.
func gitlabFromStore(w io.Writer, s *oldStore) error {
	var app struct {
		ClientID     string `json:"clientId"`
		ClientSecret string `json:"clientSecret"`
	}
	if err := s.setting("gitlab.app", &app); err != nil {
		return err
	}
	secret, err := s.open("client secret", app.ClientSecret)
	if err != nil {
		return err
	}
	var token struct {
		RefreshToken string `json:"refreshToken"`
	}
	if err := s.setting("gitlab.token", &token); err != nil {
		return fmt.Errorf("%w: authorize again with redgreen setup gitlab", err)
	}
	refresh, err := s.open("refresh token", token.RefreshToken)
	if err != nil {
		return err
	}
	fmt.Fprintln(w, "Put these in Key Vault:")
	fmt.Fprintln(w, "GITLAB_CLIENT_ID="+app.ClientID)
	fmt.Fprintln(w, "GITLAB_CLIENT_SECRET="+secret)
	fmt.Fprintln(w, "GITLAB_REFRESH_TOKEN="+refresh)
	var sealedHook string
	if err := s.setting("gitlab.webhookSecret", &sealedHook); err == nil {
		hook, err := s.open("webhook secret", sealedHook)
		if err != nil {
			return err
		}
		fmt.Fprintln(w, "GITLAB_WEBHOOK_SECRET="+hook)
	}
	accounts, err := s.activeAccounts("gitlab")
	if err != nil {
		return err
	}
	fmt.Fprintln(w, "GITLAB_ALLOWED_ACCOUNTS="+strings.Join(accounts, ","))
	fmt.Fprintln(w, "The old app must stay stopped: GitLab replaces the refresh token the first time either app uses it.")
	return nil
}

// azureDevOpsFromStore prints the service principal and the organizations
// the old app read.
func azureDevOpsFromStore(w io.Writer, s *oldStore) error {
	var app struct {
		TenantID      string   `json:"tenantId"`
		ClientID      string   `json:"clientId"`
		ClientSecret  string   `json:"clientSecret"`
		Organizations []string `json:"organizations"`
	}
	if err := s.setting("azuredevops.app", &app); err != nil {
		return err
	}
	secret, err := s.open("client secret", app.ClientSecret)
	if err != nil {
		return err
	}
	fmt.Fprintln(w, "Put these in Key Vault:")
	fmt.Fprintln(w, "AZURE_DEVOPS_TENANT_ID="+app.TenantID)
	fmt.Fprintln(w, "AZURE_DEVOPS_CLIENT_ID="+app.ClientID)
	fmt.Fprintln(w, "AZURE_DEVOPS_CLIENT_SECRET="+secret)
	fmt.Fprintln(w, "AZURE_DEVOPS_ORGANIZATIONS="+strings.Join(app.Organizations, ","))
	return nil
}
