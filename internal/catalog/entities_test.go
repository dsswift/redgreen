package catalog

import (
	"reflect"
	"testing"

	"github.com/dsswift/redgreen/internal/provider"
	"github.com/dsswift/redgreen/internal/services"
)

func TestTheDescribedServiceCarriesItsFile(t *testing.T) {
	repo := &provider.Repo{Provider: provider.GitHub, FullName: "Acme/Shop", URL: "https://github.com/Acme/Shop"}
	d := &services.Descriptor{Service: "github/acme/shop/cmd/shop", Icon: &services.IconRef{Name: "shop", File: "logo.svg"},
		Fields: map[string]any{"category": "Portal"}, Relations: map[string]any{"pillars": []any{"platform/product"}},
		Provides: []services.API{{ID: "V1", Title: "Shop API", Kind: "rest", Spec: "openapi.yaml"}}, Consumes: []string{"github/acme/hub/catalog"}}
	found := &services.Found{Described: d, Services: []services.Service{{ID: "cmd/shop", Name: "shop", Language: "Go"}, {ID: "web", Name: "web", Language: "TypeScript"}}}

	svcs := ServiceEntities(repo, found, "shop")
	if len(svcs) != 2 {
		t.Fatalf("services = %+v", svcs)
	}
	shop, web := svcs[0], svcs[1]
	if shop.Icon != "shop" || shop.Fields["origin"] != "built" || !reflect.DeepEqual(shop.Relations["consumes"], []string{"github/acme/hub/catalog"}) {
		t.Errorf("described service = %+v", shop)
	}
	if w, ok := shop.Relations["will_consume"].([]string); !ok || len(w) != 0 {
		t.Errorf("an empty list is written as one, so a removed connector leaves: %v", shop.Relations["will_consume"])
	}
	if _, ok := shop.Fields["category"]; ok {
		t.Error("the estate's own fields go in the placement, never in the snapshot")
	}
	if web.Icon != "typescript" || web.Relations["consumes"] != nil {
		t.Errorf("an undescribed service wears its language and no connectors: %+v", web)
	}
	if kept := ServiceEntities(repo, found, ""); kept[0].Icon != "" {
		t.Errorf("a logo the hub lacks is left off, so the hub keeps its own: %q", kept[0].Icon)
	}

	apis := APIEntities(repo, found)
	if len(apis) != 1 || apis[0].Key != "github/acme/shop/cmd/shop/v1" || apis[0].Relations["service"] != d.Service || apis[0].Fields["spec"] != "https://github.com/Acme/Shop/blob/HEAD/openapi.yaml" {
		t.Errorf("apis = %+v", apis)
	}
	p := Placement(found)
	if p == nil || p.Key != d.Service || p.Fields["category"] != "Portal" || p.Relations["pillars"] == nil || p.Fields["origin"] != nil {
		t.Errorf("placement = %+v", p)
	}
}

func TestADescriptorWithNoBuildFileStillNamesItsService(t *testing.T) {
	repo := &provider.Repo{Provider: provider.GitHub, FullName: "acme/hub"}
	found := &services.Found{Described: &services.Descriptor{Service: "github/acme/hub"}}
	svcs := ServiceEntities(repo, found, "")
	if len(svcs) != 1 || svcs[0].Key != "github/acme/hub" || svcs[0].Title != "hub" || svcs[0].Relations["repository"] != "github/acme/hub" {
		t.Fatalf("services = %+v", svcs)
	}
	if Placement(found) != nil || APIEntities(repo, &services.Found{}) != nil {
		t.Error("nothing to place or offer writes nothing")
	}
}
