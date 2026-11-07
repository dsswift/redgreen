package describe

import (
	"reflect"
	"strings"
	"testing"
)

func TestTheRepositoryDescribesItself(t *testing.T) {
	d, err := Load("../../orrery.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if d.Service != "github/dsswift/redgreen" || d.Image == nil || d.Image.Type != "image/svg+xml" {
		t.Fatalf("description = %+v", d)
	}
	writes := Writes(d)
	svc := writes[0].Entity
	if writes[0].Put || svc.Blueprint != "sw_service" || svc.Icon != "redgreen" || svc.Fields["origin"] != "built" {
		t.Fatalf("service write = %+v; the service is patched, wearing its logo", writes[0])
	}
	if c := svc.Relations["consumes"].([]string); !reflect.DeepEqual(c, d.Consumes) || len(c) == 0 {
		t.Errorf("consumes = %v", c)
	}
	if w := svc.Relations["will_consume"].([]string); w == nil || len(w) != 0 {
		t.Errorf("an empty list is written as one, so a removed connector leaves: %v", w)
	}
}

func TestProvidedAPIs(t *testing.T) {
	d, err := Parse([]byte("service: github/acme/app\nprovides:\n- id: events\n  title: Events\n  kind: event\n"))
	if err != nil {
		t.Fatal(err)
	}
	w := Writes(d)[1]
	if !w.Put || w.Entity.Key != "github/acme/app/events" || w.Entity.Relations["service"] != "github/acme/app" || w.Entity.Fields["kind"] != "event" {
		t.Errorf("api write = %+v", w)
	}
}

func TestRefusesWhatTheHubWould(t *testing.T) {
	for want, text := range map[string]string{
		"service is required":     "consumes: []\n",
		"field \"Bad\"":           "service: s\nfields: {Bad: 1}\n",
		"kind \"soap\"":           "service: s\nprovides: [{id: a, title: A, kind: soap}]\n",
		"listed twice":            "service: s\nprovides: [{id: a, title: A, kind: rest}, {id: a, title: B, kind: rest}]\n",
		"not .svg, .png":          "service: s\nicon: {name: app, file: logo.gif}\n",
		"field unknown not found": "service: s\nunknown: 1\n",
	} {
		_, err := Parse([]byte(text))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: %v, want it to say %q", text, err, want)
		}
	}
}
