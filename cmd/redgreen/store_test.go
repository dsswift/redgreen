package main

import (
	"bytes"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dsswift/redgreen/internal/provider"
)

// A store the old web app left, with its settings sealed the way it sealed them.
func sealedStore(t *testing.T) (path, key string) {
	t.Helper()
	raw := make([]byte, 32)
	_, _ = rand.Read(raw)
	key = base64.StdEncoding.EncodeToString(raw)
	box, err := provider.NewBox(key)
	if err != nil {
		t.Fatal(err)
	}
	seal := func(s string) string {
		v, err := box.Seal(s)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	path = filepath.Join(t.TempDir(), "old.sqlite")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(oldSchema + `CREATE TABLE settings (key TEXT PRIMARY KEY, value TEXT NOT NULL);`); err != nil {
		t.Fatal(err)
	}
	put := func(k string, v any) {
		b, _ := json.Marshal(v)
		if _, err := db.Exec(`INSERT INTO settings VALUES (?, ?)`, k, string(b)); err != nil {
			t.Fatal(err)
		}
	}
	put("gitlab.app", map[string]string{"clientId": "app-1", "clientSecret": seal("app-secret")})
	put("gitlab.token", map[string]string{"accessToken": seal("access"), "refreshToken": seal("refresh-1"), "expiresAt": "2026-01-01T00:00:00Z", "username": "someone"})
	put("gitlab.webhookSecret", seal("hook"))
	put("azuredevops.app", map[string]any{"tenantId": "t", "clientId": "c", "clientSecret": seal("sp-secret"), "organizations": []string{"org-a", "org-b"}})
	for _, a := range [][]string{{"gitlab:1", "gitlab", "platform", "active"}, {"gitlab:2", "gitlab", "someone", "ignored"}, {"gitlab:3", "gitlab", "apps", "active"}} {
		if _, err := db.Exec(`INSERT INTO accounts VALUES (?,?,?,?,'organization',NULL,'u',?,'{}',NULL,'x')`, a[0], a[1], a[2], a[2], a[3]); err != nil {
			t.Fatal(err)
		}
	}
	return path, key
}

func TestGitLabCredentialsCarryOverFromAnOldStore(t *testing.T) {
	path, key := sealedStore(t)
	s, err := openOldStore(path, key)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var out bytes.Buffer
	if err := gitlabFromStore(&out, s); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"GITLAB_CLIENT_ID=app-1\n", "GITLAB_CLIENT_SECRET=app-secret\n", "GITLAB_REFRESH_TOKEN=refresh-1\n", "GITLAB_WEBHOOK_SECRET=hook\n", "GITLAB_ALLOWED_ACCOUNTS=apps,platform\n"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %q in:\n%s", want, out.String())
		}
	}
}

func TestAzureDevOpsCredentialsCarryOverFromAnOldStore(t *testing.T) {
	path, key := sealedStore(t)
	s, err := openOldStore(path, key)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var out bytes.Buffer
	if err := azureDevOpsFromStore(&out, s); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "AZURE_DEVOPS_CLIENT_SECRET=sp-secret\n") || !strings.Contains(out.String(), "AZURE_DEVOPS_ORGANIZATIONS=org-a,org-b\n") {
		t.Errorf("unexpected:\n%s", out.String())
	}
}

func TestAWrongKeySaysSo(t *testing.T) {
	path, _ := sealedStore(t)
	other := make([]byte, 32)
	_, _ = rand.Read(other)
	s, err := openOldStore(path, base64.StdEncoding.EncodeToString(other))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := gitlabFromStore(&bytes.Buffer{}, s); err == nil || !strings.Contains(err.Error(), "SECRET_KEY") {
		t.Fatalf("err = %v, want a hint about SECRET_KEY", err)
	}
}
