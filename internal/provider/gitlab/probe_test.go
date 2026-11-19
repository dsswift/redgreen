package gitlab

import (
	"context"
	"reflect"
	"testing"

	"github.com/dsswift/redgreen/internal/provider"
	"github.com/dsswift/redgreen/internal/provider/providertest"
	"github.com/dsswift/redgreen/internal/services"
)

func probeGitLab(t *testing.T, probe []string) ([]string, []string) {
	t.Helper()
	r := gitlabRoutes()
	r["GET /api/v4/projects/42/repository/tree"] = providertest.Route{Body: `[{"type":"tree","path":"docs"},{"type":"tree","path":"docs/notes"},{"type":"blob","path":"docs/notes/0001.md"},{"type":"blob","path":"LICENSE"}]`}
	forge := providertest.New(t, r)
	p, err := New(Config{URL: forge.URL, Token: "t", TokenAccounts: []string{"acme"}}, providertest.Store(t), forge.Client(), discard())
	if err != nil {
		t.Fatal(err)
	}
	files, err := p.Files(context.Background(), provider.Account{Login: "acme"}, &provider.Repo{ID: "gitlab:42", FullName: "acme/api", DefaultBranch: "main"})
	if err != nil {
		t.Fatal(err)
	}
	found, _, err := services.Read(context.Background(), files, "gitlab/acme/api", "api", nil, services.Bots{}, probe)
	if err != nil {
		t.Fatal(err)
	}
	return found.PathsPresent, forge.Requests
}

func TestProbeGitLab(t *testing.T) {
	present, withProbe := probeGitLab(t, []string{"docs/notes", "LICENSE", "docs/guides"})
	if !reflect.DeepEqual(present, []string{"docs/notes", "LICENSE"}) {
		t.Errorf("present = %#v", present)
	}
	if absent, _ := probeGitLab(t, []string{"docs/guides"}); absent == nil || len(absent) != 0 {
		t.Errorf("absent = %#v, want an empty list", absent)
	}
	off, without := probeGitLab(t, nil)
	if off != nil {
		t.Errorf("no probe = %#v, want nil", off)
	}
	if !reflect.DeepEqual(withProbe, without) {
		t.Errorf("a probe made its own calls:\nwith    %v\nwithout %v", withProbe, without)
	}
}
