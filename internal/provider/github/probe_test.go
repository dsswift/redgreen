package github

import (
	"context"
	"reflect"
	"testing"

	"github.com/dsswift/redgreen/internal/provider"
	"github.com/dsswift/redgreen/internal/provider/providertest"
	"github.com/dsswift/redgreen/internal/services"
)

func probeGitHub(t *testing.T, truncated bool, probe []string) ([]string, []string) {
	t.Helper()
	r := routes()
	cut := "false"
	if truncated {
		cut = "true"
	}
	r["GET "+repoPath+"/git/trees/head1"] = providertest.Route{Body: `{"truncated":` + cut + `,"tree":[{"path":"docs","type":"tree","sha":"d"},{"path":"docs/notes","type":"tree","sha":"n"},{"path":"docs/notes/0001.md","type":"blob","sha":"b1"},{"path":"LICENSE","type":"blob","sha":"b2"}]}`}
	forge := providertest.New(t, r)
	p := newProvider(t, forge, Config{})
	files, err := p.Files(context.Background(), acme, &provider.Repo{FullName: "acme/mirror", DefaultBranch: "main"})
	if err != nil {
		t.Fatal(err)
	}
	found, _, err := services.Read(context.Background(), files, "github/acme/mirror", "mirror", nil, services.Bots{}, probe)
	if err != nil {
		t.Fatal(err)
	}
	return found.PathsPresent, forge.Requests
}

func TestProbeGitHub(t *testing.T) {
	present, withProbe := probeGitHub(t, false, []string{"docs/notes", "LICENSE", "docs/guides"})
	if !reflect.DeepEqual(present, []string{"docs/notes", "LICENSE"}) {
		t.Errorf("present = %#v", present)
	}
	if absent, _ := probeGitHub(t, false, []string{"docs/guides"}); absent == nil || len(absent) != 0 {
		t.Errorf("absent = %#v, want an empty list", absent)
	}
	off, without := probeGitHub(t, false, nil)
	if off != nil {
		t.Errorf("no probe = %#v, want nil", off)
	}
	if !reflect.DeepEqual(withProbe, without) {
		t.Errorf("a probe made its own calls:\nwith    %v\nwithout %v", withProbe, without)
	}
	if cut, _ := probeGitHub(t, true, []string{"docs/guides"}); cut != nil {
		t.Errorf("a truncated tree = %#v, want unknown", cut)
	}
}
