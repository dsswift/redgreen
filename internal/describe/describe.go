// Package describe writes redgreen's own description to the Orrery it runs
// beside, from the orrery.yaml at the repository root: its logo, the service
// it is, the fields and relations that place it in the estate's own scheme,
// the APIs it provides, and the APIs it calls. The service is patched, so the
// fields other sources write stay theirs, and no snapshot is opened, so
// nothing another source wrote is removed.
package describe

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"time"

	"github.com/dsswift/orrery/sdk"
	"github.com/dsswift/redgreen/internal/services"
	"github.com/dsswift/redgreen/internal/telemetry"
	"gopkg.in/yaml.v3"
)

// API is one interface the app offers.
type API struct {
	ID          string `yaml:"id"`
	Title       string `yaml:"title"`
	Kind        string `yaml:"kind"`
	Description string `yaml:"description"`
}

// Descriptor is an orrery.yaml.
type Descriptor struct {
	Service     string            `yaml:"service"`
	Icon        *services.IconRef `yaml:"icon"`
	Fields      map[string]any    `yaml:"fields"`
	Relations   map[string]any    `yaml:"relations"`
	Provides    []API             `yaml:"provides"`
	Consumes    []string          `yaml:"consumes"`
	WillConsume []string          `yaml:"will_consume"`
	// Image is the logo's bytes, read from beside the file.
	Image *services.Icon `yaml:"-"`
}

var (
	name     = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
	apiKinds = []string{"rest", "graphql", "grpc", "event", "web", "mcp"}
)

// Parse reads an orrery.yaml and refuses one the hub would refuse.
func Parse(text []byte) (*Descriptor, error) {
	var d Descriptor
	dec := yaml.NewDecoder(bytes.NewReader(text))
	dec.KnownFields(true)
	if err := dec.Decode(&d); err != nil {
		return nil, fmt.Errorf("orrery.yaml: %w", err)
	}
	if d.Service == "" {
		return nil, errors.New("orrery.yaml: service is required")
	}
	if d.Icon != nil {
		if !services.IconName.MatchString(d.Icon.Name) || d.Icon.File == "" {
			return nil, fmt.Errorf("orrery.yaml: icon needs a name matching %s and a file", services.IconName)
		}
		if services.IconType(d.Icon.File) == "" {
			return nil, fmt.Errorf("orrery.yaml: icon file %q is not .svg, .png, or .webp", d.Icon.File)
		}
	}
	for k := range d.Fields {
		if !name.MatchString(k) {
			return nil, fmt.Errorf("orrery.yaml: field %q is not a field name", k)
		}
	}
	for k := range d.Relations {
		if !name.MatchString(k) {
			return nil, fmt.Errorf("orrery.yaml: relation %q is not a relation name", k)
		}
	}
	seen := map[string]bool{}
	for i, a := range d.Provides {
		if a.ID == "" || a.Title == "" {
			return nil, fmt.Errorf("orrery.yaml: provides[%d] needs an id and a title", i)
		}
		if !slices.Contains(apiKinds, a.Kind) {
			return nil, fmt.Errorf("orrery.yaml: provides[%d] kind %q is not one of %v", i, a.Kind, apiKinds)
		}
		if seen[a.ID] {
			return nil, fmt.Errorf("orrery.yaml: api %q is listed twice", a.ID)
		}
		seen[a.ID] = true
	}
	return &d, nil
}

// Load reads the description at path and the logo it names from the same folder.
func Load(path string) (*Descriptor, error) {
	text, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	d, err := Parse(text)
	if err != nil || d.Icon == nil {
		return d, err
	}
	data, err := os.ReadFile(filepath.Join(filepath.Dir(path), d.Icon.File))
	if err != nil {
		return nil, err
	}
	if d.Image, err = services.CheckIcon(*d.Icon, data); err != nil {
		return nil, err
	}
	return d, nil
}

// Write is one write of the description.
type Write struct {
	Put    bool
	Entity sdk.Entity
}

// Writes are the service, patched and wearing its logo, then one API per
// provided one. An empty list is written as an empty list, so a connector
// removed from the file is removed from the catalog.
func Writes(d *Descriptor) []Write {
	fields := map[string]any{}
	for k, v := range d.Fields {
		fields[k] = v
	}
	fields["origin"] = "built"
	relations := map[string]any{}
	for k, v := range d.Relations {
		relations[k] = v
	}
	relations["consumes"] = nonNil(d.Consumes)
	relations["will_consume"] = nonNil(d.WillConsume)
	svc := sdk.Entity{Blueprint: "sw_service", Key: d.Service, Fields: fields, Relations: relations}
	if d.Icon != nil {
		svc.Icon = d.Icon.Name
	}
	out := []Write{{Entity: svc}}
	for _, a := range d.Provides {
		out = append(out, Write{Put: true, Entity: sdk.Entity{Blueprint: "sw_api", Key: d.Service + "/" + a.ID, Title: a.Title,
			Fields:    map[string]any{"kind": a.Kind, "description": a.Description, "visibility": "internal", "external": false},
			Relations: map[string]any{"service": d.Service}}})
	}
	return out
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// Blueprints are what a description writes, named as the blueprint
// standard's software pack names them.
func Blueprints() []sdk.Blueprint {
	return []sdk.Blueprint{
		{"id": "sw_service", "title": "Service", "group": "Catalog", "formerly": []string{"service"},
			"description": "One deployable piece of software, keyed by its repository path. Built here or adopted open source.",
			"fields":      map[string]any{"origin": map[string]any{"type": "enum", "values": []string{"built", "adopted"}, "chip": true, "description": "Built here, or open source the estate runs."}},
			"relations": map[string]any{
				"consumes":     map[string]any{"target": "sw_api", "many": true, "title": "Consumes", "inverseTitle": "Consumed by"},
				"will_consume": map[string]any{"target": "sw_api", "many": true, "title": "Will consume", "inverseTitle": "Wanted by"},
			}},
		{"id": "sw_api", "title": "API", "group": "Catalog", "formerly": []string{"api"},
			"description": "An interface a service offers others, keyed <service key>/<id>.",
			"fields": map[string]any{
				"description": map[string]any{"type": "string"},
				"kind":        map[string]any{"type": "enum", "values": apiKinds},
				"visibility":  map[string]any{"type": "enum", "values": []string{"internal", "partner", "public"}},
				"external":    map[string]any{"type": "boolean"},
			},
			"relations": map[string]any{"service": map[string]any{"target": "sw_service", "title": "Provided by", "inverseTitle": "Provides"}}},
	}
}

// Every is how often the description is written again, so the catalog keeps seeing it.
const Every = 6 * time.Hour

var backoff = []time.Duration{30 * time.Second, time.Minute, 5 * time.Minute, 15 * time.Minute}

// Describer keeps redgreen described for as long as it runs.
type Describer struct {
	Client *sdk.Client
	Desc   *Descriptor
	Tel    *telemetry.Telemetry
	Log    *slog.Logger
}

// Run writes the description now, again every six hours, and sooner after a failure.
func (d Describer) Run(ctx context.Context) {
	failures := 0
	for {
		wait := Every
		if err := d.Once(ctx); err != nil {
			if ctx.Err() != nil {
				return
			}
			wait = backoff[min(failures, len(backoff)-1)]
			failures++
		} else {
			failures = 0
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}

// Once writes the description and says whether it took.
func (d Describer) Once(ctx context.Context) error {
	ctx, span := d.Tel.Root(ctx, "redgreen.describe")
	err := d.write(ctx)
	telemetry.End(span, err)
	if err != nil {
		d.Log.WarnContext(ctx, "orrery describe failed", "service", d.Desc.Service, "error", err)
		d.Tel.Described(ctx, "failed")
		return err
	}
	d.Log.InfoContext(ctx, "orrery described", "service", d.Desc.Service, "apis", len(d.Desc.Provides), "consumes", len(d.Desc.Consumes), "will_consume", len(d.Desc.WillConsume))
	d.Tel.Described(ctx, "ok")
	return nil
}

func (d Describer) write(ctx context.Context) error {
	if _, err := d.Client.OfferBlueprints(ctx, Blueprints()); err != nil {
		return err
	}
	// The logo goes first, so the service never names an icon the hub lacks.
	if img := d.Desc.Image; img != nil {
		if err := d.Client.PutIcon(ctx, img.Name, img.Type, img.Data); err != nil {
			return err
		}
	}
	for _, w := range Writes(d.Desc) {
		var err error
		if w.Put {
			err = d.Client.Put(ctx, w.Entity)
		} else {
			err = d.Client.Patch(ctx, w.Entity)
		}
		if err != nil {
			return err
		}
	}
	return nil
}
