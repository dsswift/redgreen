package azuredevops

import (
	"context"
	"io"
	"log/slog"
	"reflect"
	"testing"

	"github.com/dsswift/redgreen/internal/provider"
	"github.com/dsswift/redgreen/internal/provider/providertest"
	"github.com/dsswift/redgreen/internal/services"
)

func probeAzureDevOps(t *testing.T, probe []string) ([]string, []string) {
	t.Helper()
	r := adoRoutes()
	r["GET /acme/_apis/git/repositories/r1/commits"] = providertest.Route{Body: `{"value":[{"commitId":"head"}]}`}
	r["GET /acme/_apis/git/repositories/r1/items"] = providertest.Route{Body: `{"value":[{"path":"/","isFolder":true},{"path":"/docs","isFolder":true},{"path":"/docs/notes","isFolder":true},{"path":"/docs/notes/0001.md"},{"path":"/LICENSE"}]}`}
	forge := providertest.New(t, r)
	p := New(Config{Root: forge.URL, Login: forge.URL, TenantID: "tenant", ClientID: "c", ClientSecret: "s", Organizations: []string{"acme"}}, providertest.Store(t), forge.Client(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	files, err := p.Files(context.Background(), provider.Account{Login: "acme"}, &provider.Repo{ID: "azuredevops:r1", FullName: "acme/Home/site", DefaultBranch: "main"})
	if err != nil {
		t.Fatal(err)
	}
	found, _, err := services.Read(context.Background(), files, "azuredevops/acme/home/site", "site", nil, services.Bots{}, probe)
	if err != nil {
		t.Fatal(err)
	}
	return found.PathsPresent, forge.Requests
}

func TestProbeAzureDevOps(t *testing.T) {
	present, withProbe := probeAzureDevOps(t, []string{"docs/notes", "LICENSE", "docs/guides"})
	if !reflect.DeepEqual(present, []string{"docs/notes", "LICENSE"}) {
		t.Errorf("present = %#v", present)
	}
	if absent, _ := probeAzureDevOps(t, []string{"docs/guides"}); absent == nil || len(absent) != 0 {
		t.Errorf("absent = %#v, want an empty list", absent)
	}
	off, without := probeAzureDevOps(t, nil)
	if off != nil {
		t.Errorf("no probe = %#v, want nil", off)
	}
	if !reflect.DeepEqual(withProbe, without) {
		t.Errorf("a probe made its own calls:\nwith    %v\nwithout %v", withProbe, without)
	}
}
