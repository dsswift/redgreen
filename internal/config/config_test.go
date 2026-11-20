package config

import (
	"reflect"
	"strings"
	"testing"
)

func env(vars map[string]string) func(string) string {
	return func(name string) string { return vars[name] }
}

func TestProbePathsAbsentLooksForNone(t *testing.T) {
	c, err := Parse(env(nil))
	if err != nil {
		t.Fatal(err)
	}
	if c.ProbePaths != nil {
		t.Fatalf("probe = %#v, want nil", c.ProbePaths)
	}
	c, err = Parse(env(map[string]string{"PROBE_PATHS": " , "}))
	if err != nil || c.ProbePaths != nil {
		t.Fatalf("blank list: probe = %#v, %v", c.ProbePaths, err)
	}
}

func TestProbePathsAreCleanedFromTheRoot(t *testing.T) {
	c, err := Parse(env(map[string]string{"PROBE_PATHS": "docs/notes, /docs/guides/ ,./LICENSE,docs/notes/,v1..v2"}))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"docs/notes", "docs/guides", "LICENSE", "v1..v2"}
	if !reflect.DeepEqual(c.ProbePaths, want) {
		t.Fatalf("probe = %#v, want %#v", c.ProbePaths, want)
	}
}

func TestProbePathOutsideTheRootFails(t *testing.T) {
	for _, bad := range []string{"../elsewhere", "docs/../../x", "/", "."} {
		_, err := Parse(env(map[string]string{"PROBE_PATHS": bad}))
		if err == nil || !strings.Contains(err.Error(), "PROBE_PATHS") {
			t.Errorf("%q: err = %v, want a PROBE_PATHS error", bad, err)
		}
	}
}

func TestComponentFieldsDefaultToDeployComponent(t *testing.T) {
	c, err := Parse(env(nil))
	if err != nil || !reflect.DeepEqual(c.ComponentFields, []string{"deploy_component"}) {
		t.Fatalf("component fields = %#v, %v", c.ComponentFields, err)
	}
	c, err = Parse(env(map[string]string{"DEPLOY_COMPONENT_FIELDS": "component, release_component"}))
	if err != nil || !reflect.DeepEqual(c.ComponentFields, []string{"component", "release_component"}) {
		t.Fatalf("component fields = %#v, %v", c.ComponentFields, err)
	}
}

func TestArgoServicesArePairsByCommaOrLine(t *testing.T) {
	c, err := Parse(env(map[string]string{"ARGOCD_SERVICES": "orders=gitlab/acme/orders,\n  prod/argocd/billing = gitlab/acme/billing\nhttps://gitlab.example.com/ops/gitops/-/tree/main/apps/jobs?ref=main=gitlab/acme/jobs ,orders=gitlab/acme/orders"}))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"orders":              "gitlab/acme/orders",
		"prod/argocd/billing": "gitlab/acme/billing",
		"https://gitlab.example.com/ops/gitops/-/tree/main/apps/jobs?ref=main": "gitlab/acme/jobs",
	}
	if !reflect.DeepEqual(c.ArgoServices, want) {
		t.Fatalf("argo services = %#v, want %#v", c.ArgoServices, want)
	}
	if c, err = Parse(env(nil)); err != nil || len(c.ArgoServices) != 0 {
		t.Fatalf("none set: %#v, %v", c.ArgoServices, err)
	}
}

func TestArgoServicesRefuseAPairTheyCannotRead(t *testing.T) {
	for _, bad := range []string{"orders", "orders=", "=gitlab/acme/orders", "orders=gitlab/acme/orders,Orders=gitlab/acme/billing"} {
		_, err := Parse(env(map[string]string{"ARGOCD_SERVICES": bad}))
		if err == nil || !strings.Contains(err.Error(), "ARGOCD_SERVICES") {
			t.Errorf("%q: err = %v, want an ARGOCD_SERVICES error", bad, err)
		}
	}
}
