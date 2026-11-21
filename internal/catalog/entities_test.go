package catalog

import (
	"reflect"
	"testing"
	"time"

	"github.com/dsswift/redgreen/internal/provider"
	"github.com/dsswift/redgreen/internal/services"
)

func TestTheDescribedServiceCarriesItsFile(t *testing.T) {
	repo := &provider.Repo{Provider: provider.GitHub, FullName: "Acme/Shop", URL: "https://github.com/Acme/Shop"}
	d := &services.Descriptor{Service: "github/acme/shop/cmd/shop", Icon: &services.IconRef{Name: "shop", File: "logo.svg"},
		Fields: map[string]any{"category": "Portal"}, Relations: map[string]any{"pillars": []any{"platform/product"}},
		Provides: []services.API{{ID: "V1", Title: "Shop API", Kind: "rest", Spec: "openapi.yaml"}}, Consumes: []string{"github/acme/hub/catalog"}}
	found := &services.Found{Descriptions: []services.Description{{Descriptor: *d, File: "orrery.yaml"}}, Services: []services.Service{{ID: "cmd/shop", Name: "shop", Language: "Go"}, {ID: "web", Name: "web", Language: "TypeScript"}}}

	svcs, bare := ServiceEntities(repo, found, map[string]string{"shop": "shop"})
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
	if shop.Fields["category"] != "Portal" || shop.Relations["pillars"] == nil {
		t.Errorf("the place travels on the service, so no write drops it: %+v", shop)
	}
	b, ok := bare[shop.Key]
	if !ok || len(bare) != 1 || b.Fields["category"] != nil || b.Relations["pillars"] != nil || b.Fields["origin"] != "built" || b.Icon != "shop" {
		t.Errorf("bare is the service as read, without its place: %+v", bare)
	}
	if web.Icon != "typescript" || web.Relations["consumes"] != nil {
		t.Errorf("an undescribed service wears its language and no connectors: %+v", web)
	}
	if kept, _ := ServiceEntities(repo, found, nil); kept[0].Icon != "" {
		t.Errorf("a logo the hub lacks is left off, so the hub keeps its own: %q", kept[0].Icon)
	}

	apis := APIEntities(repo, found)
	if len(apis) != 1 || apis[0].Key != "github/acme/shop/cmd/shop/v1" || apis[0].Relations["service"] != d.Service || apis[0].Fields["spec"] != "https://github.com/Acme/Shop/blob/HEAD/openapi.yaml" {
		t.Errorf("apis = %+v", apis)
	}
}

func TestADescriptorWithNoBuildFileStillNamesItsService(t *testing.T) {
	repo := &provider.Repo{Provider: provider.GitHub, FullName: "acme/hub"}
	found := &services.Found{Descriptions: []services.Description{{Descriptor: services.Descriptor{Service: "github/acme/hub"}}}}
	svcs, bare := ServiceEntities(repo, found, nil)
	if len(svcs) != 1 || svcs[0].Key != "github/acme/hub" || svcs[0].Title != "hub" || svcs[0].Relations["repository"] != "github/acme/hub" {
		t.Fatalf("services = %+v", svcs)
	}
	if len(bare) != 0 || APIEntities(repo, &services.Found{}) != nil {
		t.Error("nothing to place or offer writes nothing")
	}
}

func TestEachFolderServiceWearsItsOwnDescription(t *testing.T) {
	repo := &provider.Repo{Provider: provider.GitLab, FullName: "acme/envs"}
	found := &services.Found{
		Services: []services.Service{{ID: "prod", Path: "prod", Name: "prod", Type: services.Infrastructure}},
		Descriptions: []services.Description{
			{Descriptor: services.Descriptor{Service: "gitlab/acme/envs/prod", Fields: map[string]any{"lifecycle": "production"}, Relations: map[string]any{"program": []any{"7"}}}, File: "prod/orrery.yaml", Path: "prod"},
			{Descriptor: services.Descriptor{Service: "gitlab/acme/envs/docs"}, File: "docs/orrery.yaml", Path: "docs"},
		},
	}
	svcs, _ := ServiceEntities(repo, found, nil)
	if len(svcs) != 2 || svcs[0].Key != "gitlab/acme/envs/prod" || svcs[0].Fields["path"] != "prod" || svcs[0].Fields["lifecycle"] != "production" || svcs[0].Relations["program"] == nil {
		t.Fatalf("services = %+v", svcs)
	}
	if svcs[1].Key != "gitlab/acme/envs/docs" || svcs[1].Fields["path"] != "docs" {
		t.Errorf("a service only its file names sits at its folder: %+v", svcs[1])
	}
}

func TestConnectionsAreKeyedByServiceEnvironmentAndHost(t *testing.T) {
	repo := &provider.Repo{Provider: provider.GitHub, FullName: "Acme/Shop"}
	found := &services.Found{Services: []services.Service{{ID: "src/Api", Name: "Api"}}, Connections: []services.Connection{
		{Service: "src/Api", Environment: "Prod", Kind: services.SQL, Host: "shop-prod.database.windows.net", Database: "Shop", Settings: []string{"ConnectionStrings:Main"}},
		{Service: "src/Api", Environment: "unknown", Kind: services.AppInsights, AppInsightsKey: "0a1b2c3d-0000-4000-8000-000000000001", Settings: []string{"InstrumentationKey"}},
	}}
	got := ConnectionEntities(repo, found)
	if len(got) != 2 {
		t.Fatalf("connections = %+v", got)
	}
	sql, ai := got[0], got[1]
	if sql.Key != "github/acme/shop/src/api@Prod>shop-prod.database.windows.net/shop@code" || sql.Relations["service"] != "github/acme/shop/src/api" || sql.Title != "Api (Prod) to shop-prod.database.windows.net/Shop" {
		t.Errorf("sql = %+v", sql)
	}
	want := map[string]any{"host": "shop-prod.database.windows.net", "resource_name": "shop-prod", "database": "Shop", "kind": "sql", "evidence": "code",
		"environment_name": "Prod", "setting_names": []string{"ConnectionStrings:Main"}, "app_insights_key": nil}
	if !reflect.DeepEqual(sql.Fields, want) {
		t.Errorf("sql fields = %#v", sql.Fields)
	}
	if ai.Key != "github/acme/shop/src/api@unknown>0a1b2c3d-0000-4000-8000-000000000001@code" || ai.Fields["host"] != nil || ai.Fields["resource_name"] != nil || ai.Fields["app_insights_key"] != "0a1b2c3d-0000-4000-8000-000000000001" || ai.Fields["kind"] != "appinsights" {
		t.Errorf("app insights = %+v", ai)
	}
}

func TestRepositoryCarriesThePathsPresent(t *testing.T) {
	repo := &provider.Repo{Provider: provider.GitHub, FullName: "acme/site"}
	for _, c := range []struct {
		name  string
		found *services.Found
		want  any
	}{
		{"not read", nil, nil},
		{"not probed", &services.Found{}, nil},
		{"probed, none", &services.Found{PathsPresent: []string{}}, []string{}},
		{"probed, some", &services.Found{PathsPresent: []string{"docs/notes"}}, []string{"docs/notes"}},
	} {
		got, ok := RepositoryEntity(repo, c.found, time.Now()).Fields["paths_present"]
		if !ok || !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: paths_present = %#v (%v), want %#v", c.name, got, ok, c.want)
		}
	}
}
