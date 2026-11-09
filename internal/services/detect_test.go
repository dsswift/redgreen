package services

import (
	"context"
	"encoding/json"
	"reflect"
	"sort"
	"strings"
	"testing"
)

func reader(files map[string]string) ([]string, Reader) {
	paths := make([]string, 0, len(files))
	for p := range files {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	return paths, func(p string) (string, bool) {
		s, ok := files[p]
		return s, ok
	}
}

func detect(files map[string]string, name string) []string {
	paths, read := reader(files)
	out := []string{}
	for _, s := range Detect(paths, read, name) {
		id, lang := s.ID, s.Language
		if id == "" {
			id = "."
		}
		if lang == "" {
			lang = "null"
		}
		out = append(out, strings.Join([]string{id, s.Type, lang, s.Name}, " "))
	}
	return out
}

func csproj(body, sdk string) string {
	if sdk == "" {
		sdk = "Microsoft.NET.Sdk"
	}
	return `<Project Sdk="` + sdk + `"><PropertyGroup>` + body + `</PropertyGroup></Project>`
}

func js(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func same(t *testing.T, got, want any) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got  %#v\nwant %#v", got, want)
	}
}

func TestMonorepoFindsEachAppOnce(t *testing.T) {
	same(t, detect(map[string]string{
		"src/Orders.Api/Orders.Api.csproj":             csproj("<AzureFunctionsVersion>v4</AzureFunctionsVersion>", ""),
		"src/Orders.Api/host.json":                     "{}",
		"src/Orders.Api/Run.cs":                        "",
		"src/Portal/Portal.csproj":                     csproj("", "Microsoft.NET.Sdk.Web"),
		"src/Portal/Pages/Index.razor":                 "",
		"src/Shared/Shared.csproj":                     csproj("", ""),
		"src/Orders.Api.Tests/Orders.Api.Tests.csproj": csproj("<OutputType>Exe</OutputType>", ""),
		"cmd/shopctl/main.go":                          "package main\n\nfunc main() {}\n",
		"cmd/shopd/main.go":                            "package main\n\nfunc main() { http.ListenAndServe(\":80\", nil) }\n",
		"web/package.json":                             js(map[string]any{"name": "@shop/web", "dependencies": map[string]string{"next": "15"}, "devDependencies": map[string]string{"typescript": "5"}}),
		"tools/report/pyproject.toml":                  "[project]\nname = \"report\"\n\n[project.scripts]\nreport = \"report:main\"\n",
		"samples/demo/package.json":                    js(map[string]any{"name": "demo", "scripts": map[string]string{"start": "node ."}}),
		"infra/main.tf":                                "terraform {\n  backend azurerm {}\n}\n",
		"Dockerfile":                                   "FROM hashicorp/terraform\n",
	}, "shop"), []string{
		"cmd/shopctl tool Go shopctl",
		"cmd/shopd backend Go shopd",
		"src/Orders.Api backend C# Orders.Api",
		"src/Portal frontend C# Portal",
		"tools/report tool Python report",
		"web frontend TypeScript web",
		"infra infrastructure Terraform shop infrastructure",
	})
}

func TestFrontEndAndDotnetHostAreOneApp(t *testing.T) {
	same(t, detect(map[string]string{
		"apps/s6/package.json":   js(map[string]any{"dependencies": map[string]string{"@angular/core": "20"}}),
		"apps/s6/tsconfig.json":  "{}",
		"apps/s6/bff/Bff.csproj": csproj("", "Microsoft.NET.Sdk.Web"),
	}, "shop"), []string{"apps/s6 frontend TypeScript s6"})
}

func TestFunctionsHostHoldingAppsIsSharedSettings(t *testing.T) {
	same(t, detect(map[string]string{"host.json": "{}", "a/A.csproj": csproj("<AzureFunctionsVersion>v4</AzureFunctionsVersion>", ""), "a/host.json": "{}"}, "shop"), []string{"a backend C# A"})
}

func TestLoneGoProgram(t *testing.T) {
	same(t, detect(map[string]string{"main.go": "package main\nfunc main() {}\n", "Dockerfile": "FROM golang"}, "pusher"), []string{". backend Go pusher"})
}

func TestInfrastructureSharingAFolder(t *testing.T) {
	same(t, detect(map[string]string{"package.json": js(map[string]any{"scripts": map[string]string{"start": "node server.js"}}), "main.bicep": ""}, "shop"),
		[]string{". backend JavaScript shop", "infrastructure infrastructure Bicep shop infrastructure"})
}

func TestNothingThatRuns(t *testing.T) {
	same(t, detect(map[string]string{"terraform/main.tf": "", "terraform/prod.tfvars": "", "README.md": ""}, "az-lz-shop"), []string{"terraform infrastructure Terraform az-lz-shop"})
	same(t, detect(map[string]string{"modules/vault/main.tf": `resource "x" "y" {}`, "modules/tags/variables.tf": ""}, "tfhub"), []string{"modules library Terraform tfhub"})
	same(t, detect(map[string]string{"Checks.csproj": csproj("", ""), "Checks.cs": ""}, "checks"), []string{". library C# checks"})
	same(t, detect(map[string]string{"apps/kustomization.yaml": "", "apps/argo/kustomization.yaml": "", "_images/shell/Dockerfile": "FROM node"}, "k8s"), []string{"apps infrastructure null k8s"})
	same(t, detect(map[string]string{"scripts/a.ps1": "", "scripts/b.ps1": "", "README.md": ""}, "defaults"), []string{". tool PowerShell defaults"})
	same(t, detect(map[string]string{"a.md": "", "b.md": "", "c.md": "", "d.md": "", "toc.js": ""}, "notes"), []string{})
	same(t, detect(map[string]string{"README.md": ""}, "empty"), []string{})
}

func carried(files map[string]string, name string) map[string]ServiceFiles {
	paths, read := reader(files)
	out := map[string]ServiceFiles{}
	for _, s := range Detect(paths, read, name) {
		id := s.ID
		if id == "" {
			id = "."
		}
		out[id] = s.Files
	}
	return out
}

func TestEachServiceOnItsOwnFiles(t *testing.T) {
	fn := csproj("<AzureFunctionsVersion>v4</AzureFunctionsVersion>", "")
	same(t, carried(map[string]string{
		"README.md":                                     "",
		"workloads/projects/README.md":                  "",
		"workloads/projects/Api/Api.csproj":             fn,
		"workloads/projects/Api/Dockerfile":             "",
		"workloads/projects/Tests.Api/Tests.Api.csproj": csproj("<IsTestProject>true</IsTestProject>", ""),
		"workloads/projects/Tests.Api/HandlerTests.cs":  "",
		"workloads/billing/Ingestor/Ingestor.csproj":    fn,
		"workloads/billing/Ingestor/README.md":          "",
		"workloads/web/console/Console.csproj":          csproj("", "Microsoft.NET.Sdk.Web"),
		"workloads/web/console/Dockerfile":              "",
	}, "shop"), map[string]ServiceFiles{
		"workloads/billing/Ingestor": {Readme: true},
		"workloads/projects/Api":     {Readme: true, Dockerfile: true, Tests: true},
		"workloads/web/console":      {Dockerfile: true},
	})
}

func TestTestProjectNamedForAService(t *testing.T) {
	svcs := []Service{{ID: "src/Orders.Api", Path: "src/Orders.Api", Name: "Orders.Api", Type: Backend}, {ID: "src/Billing", Path: "src/Billing", Name: "Billing", Type: Backend}}
	none := func(string) (string, bool) { return "", false }
	tests := func(paths []string) []bool {
		var out []bool
		for _, f := range serviceFiles(paths, svcs, none) {
			out = append(out, f.Tests)
		}
		return out
	}
	same(t, tests([]string{"test/Orders.Api.UnitTests/OrderTests.cs"}), []bool{true, false})
	same(t, tests([]string{"test/Tests.Billing/BillTests.cs"}), []bool{false, true})
}

func TestTestFileNames(t *testing.T) {
	var got []bool
	for _, p := range []string{"sync/run_test.go", "src/app.test.ts", "src/app.spec.tsx", "pkg/test_sync.py", "src/app.ts", "docs/testing.md"} {
		got = append(got, serviceFiles([]string{p}, []Service{{Name: "x", Type: Tool}}, func(string) (string, bool) { return "", false })[0].Tests)
	}
	same(t, got, []bool{true, true, true, true, false, false})
}

func TestLoneServiceTakesRootReadme(t *testing.T) {
	same(t, carried(map[string]string{"README.md": "", "Dockerfile": "FROM golang", "main.go": "package main\nfunc main() {}\n", "main_test.go": "", "infra/main.tf": "terraform {\n  backend azurerm {}\n}\n"}, "pusher"),
		map[string]ServiceFiles{".": {Readme: true, Dockerfile: true, Tests: true}, "infra": {}})
}

func TestDockerfileAboveSeveralServices(t *testing.T) {
	same(t, carried(map[string]string{
		"web/README.md":            "",
		"web/Dockerfile":           "COPY Portal/Portal.csproj Portal/\nCOPY . .\n",
		"web/Portal/Portal.csproj": csproj("", "Microsoft.NET.Sdk.Web"),
		"web/Admin/Admin.csproj":   csproj("<OutputType>Exe</OutputType>", ""),
	}, "shop"), map[string]ServiceFiles{"web/Admin": {}, "web/Portal": {Dockerfile: true}})
}

func TestDockerfileHeldByOneServiceBuildsOnesBelow(t *testing.T) {
	same(t, carried(map[string]string{
		"package.json":      js(map[string]any{"scripts": map[string]string{"start": "node ."}}),
		"go.Dockerfile":     "COPY cmd ./cmd\nRUN go build -o /out/agent ./cmd/agent\n",
		"cmd/agent/main.go": "package main\nfunc main() {}\n",
		"cmd/admin/main.go": "package main\nfunc main() {}\n",
	}, "shop"), map[string]ServiceFiles{".": {Dockerfile: true}, "cmd/admin": {}, "cmd/agent": {Dockerfile: true}})
}

func TestReadmeInsideAServiceIsNotItsOwn(t *testing.T) {
	same(t, carried(map[string]string{"site/Site.csproj": csproj("", "Microsoft.NET.Sdk.Web"), "site/wwwroot/lib/README.md": ""}, "shop"), map[string]ServiceFiles{"site": {}})
}

func TestDependencyFoldersCountForNothing(t *testing.T) {
	same(t, carried(map[string]string{"package.json": js(map[string]any{"scripts": map[string]string{"start": "node ."}}), "node_modules/lib/README.md": "", "node_modules/lib/lib.test.js": ""}, "shop"), map[string]ServiceFiles{".": {}})
}

type memFiles struct {
	commit string
	files  map[string][]byte
	reads  *[]string
	recent []Commit
}

func (m memFiles) Commit() string { return m.commit }
func (m memFiles) List(context.Context) ([]string, error) {
	var out []string
	for p := range m.files {
		out = append(out, p)
	}
	sort.Strings(out)
	return out, nil
}
func (m memFiles) Read(_ context.Context, p string) ([]byte, error) {
	if m.reads != nil {
		*m.reads = append(*m.reads, p)
	}
	return m.files[p], nil
}
func (m memFiles) Commits(context.Context, string) ([]Commit, error) { return m.recent, nil }

func TestReadStampsLastChange(t *testing.T) {
	f := memFiles{commit: "a", files: map[string][]byte{"main.go": []byte("package main\nfunc main() {}\n")}, recent: []Commit{
		{At: "3", By: "release-damnit"}, {At: "2", By: "dependabot[bot]"}, {At: "1", By: "a-developer", Email: " A.Developer@Example.com "},
	}}
	found, _, err := Read(context.Background(), f, "github/acme/tool", "tool", nil, Bots{})
	if err != nil {
		t.Fatal(err)
	}
	s := found.Services[0]
	same(t, []string{s.ChangedAt, s.ChangedBy, s.ChangedByEmail, s.HumanChangedAt, s.HumanChangedBy, s.HumanChangedByEmail}, []string{"3", "release-damnit", "", "1", "a-developer", "a.developer@example.com"})
}

func TestReadAgainOnlyWhenBranchOrRulesMoved(t *testing.T) {
	var reads []string
	ctx := context.Background()
	files := func(commit string) memFiles {
		return memFiles{commit: commit, files: map[string][]byte{"main.go": []byte("package main\nfunc main() {}\n")}, reads: &reads}
	}
	first, out, _ := Read(ctx, files("a"), "github/acme/tool", "tool", nil, Bots{})
	if out.Reused || first.Commit != "a" || first.Rules != Rules || first.Services[0].Type != Tool || first.Services[0].Language != "Go" {
		t.Fatalf("first read: %+v %+v", first, out)
	}
	if _, out, _ := Read(ctx, files("a"), "github/acme/tool", "tool", first, Bots{}); !out.Reused {
		t.Fatal("same commit read again")
	}
	if _, out, _ := Read(ctx, files("b"), "github/acme/tool", "tool", first, Bots{}); out.Reused {
		t.Fatal("moved branch reused")
	}
	older := *first
	older.Rules = Rules - 1
	if _, out, _ := Read(ctx, files("a"), "github/acme/tool", "tool", &older, Bots{}); out.Reused {
		t.Fatal("older rules reused")
	}
	if len(reads) != 3 {
		t.Fatalf("reads = %d, want 3", len(reads))
	}
	empty, _, _ := Read(ctx, nil, "github/acme/x", "x", nil, Bots{})
	same(t, *empty, Found{Rules: Rules})
}

func TestBudget(t *testing.T) {
	b := &budgeted{ctx: context.Background(), files: memFiles{files: map[string][]byte{"a": []byte("x"), "b": []byte("x"), "c": []byte("x")}}, budget: 2}
	var got []bool
	for _, p := range []string{"a", "b", "c"} {
		_, ok := b.read(p)
		got = append(got, ok)
	}
	same(t, got, []bool{true, true, false})
}

func TestBots(t *testing.T) {
	same(t, EmailOf("Dev@Example.COM"), "dev@example.com")
	same(t, EmailOf("not an address"), "")
	if l := (Bots{}).LastChangeOf([]Commit{{At: "1", By: "x", Email: "ci[bot]@example.com"}}); l.HumanAt != "" || l.HumanBy != "" {
		t.Fatalf("bot counted as a person: %+v", l)
	}
	if (Bots{}).LastChangeOf(nil) != nil {
		t.Fatal("no commits must answer nil")
	}
	l := (Bots{}).LastChangeOf([]Commit{{At: "2", By: "Cloud Operations", Email: "cloud-operations@example.invalid"}, {At: "1", By: "A Developer", Email: "dev@example.com"}})
	same(t, []string{l.By, l.HumanBy, l.HumanEmail}, []string{"Cloud Operations", "A Developer", "dev@example.com"})
	same(t, Bots{Extra: []string{"deploy robot"}}.Is(Commit{By: "Deploy Robot"}), true)
	same(t, Bots{}.Is(Commit{By: "Deploy Robot"}), false)
}

func TestReadDescriptor(t *testing.T) {
	ctx := context.Background()
	svg := []byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`)
	read := func(files map[string][]byte) (*Descriptor, *Icon, string) {
		m := memFiles{commit: "c", files: files}
		paths, _ := m.List(ctx)
		f := &Found{}
		if err := f.describe(ctx, m, paths, "github/example/app"); err != nil {
			t.Fatal(err)
		}
		problem := strings.Join(f.Problems, "; ")
		if len(f.Descriptions) == 0 {
			return nil, nil, problem
		}
		return &f.Descriptions[0].Descriptor, f.Descriptions[0].Logo, problem
	}
	d, icon, _ := read(map[string][]byte{"orrery.yaml": []byte("service: GitHub/Example/App/cmd/app\nicon: {name: app, file: ./brand/logo.svg}\nprovides: [{id: v1, title: API, kind: rest, spec: api/openapi.yaml}]\nconsumes: [github/acme/hub/catalog]\nfields: {category: Portal}\n"), "brand/logo.svg": svg})
	if d == nil || d.Service != "github/example/app/cmd/app" || len(d.Provides) != 1 || d.Consumes[0] != "github/acme/hub/catalog" || d.Fields["category"] != "Portal" {
		t.Fatalf("descriptor = %+v", d)
	}
	if icon == nil || icon.Name != "app" || icon.Type != "image/svg+xml" || len(icon.SHA256) != 64 {
		t.Fatalf("icon = %+v", icon)
	}
	if d, icon, problem := read(map[string][]byte{"main.go": nil}); d != nil || icon != nil || problem != "" {
		t.Fatal("no orrery.yaml describes nothing")
	}
	if d, icon, problem := read(map[string][]byte{"orrery.yaml": []byte("service: github/example/app\n")}); d == nil || icon != nil || problem != "" {
		t.Fatal("no icon key names no logo, and the description stands")
	}
	if d, _, problem := read(map[string][]byte{"orrery.yaml": []byte("service: github/example/app\nicon: {name: app, file: logo.svg}\n")}); d == nil || !strings.Contains(problem, "not in the repository") {
		t.Fatalf("a missing logo costs only the logo: %+v, %q", d, problem)
	}
	for want, files := range map[string]map[string][]byte{
		"a name matching ^":        {"orrery.yaml": []byte("service: github/example/app\nicon: {name: App, file: logo.svg}\n"), "logo.svg": svg},
		"not .svg, .png, or .webp": {"orrery.yaml": []byte("service: github/example/app\nicon: {name: app, file: logo.gif}\n"), "logo.gif": svg},
		"over 262144":              {"orrery.yaml": []byte("service: github/example/app\nicon: {name: app, file: logo.png}\n"), "logo.png": make([]byte, 256*1024+1)},
		"service is required":      {"orrery.yaml": []byte("icon: {name: app, file: logo.svg}\n"), "logo.svg": svg},
		"a name matching":          {"orrery.yaml": []byte("service: github/example/app\nicon: {name: app}\n")},
		"or a folder in it":        {"orrery.yaml": []byte("service: github/example/application\n")},
		"kind \"soap\"":            {"orrery.yaml": []byte("service: github/example/app\nprovides: [{id: a, title: A, kind: soap}]\n")},
		"listed twice":             {"orrery.yaml": []byte("service: github/example/app\nprovides: [{id: a, title: A, kind: rest}, {id: a, title: B, kind: rest}]\n")},
		"leaves the repository":    {"orrery.yaml": []byte("service: github/example/app\nprovides: [{id: a, title: A, kind: rest, spec: ../x.yaml}]\n")},
		"field \"Bad\"":            {"orrery.yaml": []byte("service: github/example/app\nfields: {Bad: 1}\n")},
		"field unknown not found":  {"orrery.yaml": []byte("service: github/example/app\nunknown: 1\n")},
	} {
		if _, _, problem := read(files); !strings.Contains(problem, want) {
			t.Errorf("problem %q, want it to say %q", problem, want)
		}
	}
}

func TestEachServiceMayHaveItsOwnDescriptor(t *testing.T) {
	ctx := context.Background()
	m := memFiles{commit: "c", files: map[string][]byte{
		"orrery.yaml":               []byte("service: github/acme/mono\n"),
		"apps/shop/go.mod":          []byte("module shop\n"),
		"apps/shop/main.go":         []byte("package main\nfunc main() {}\n"),
		"apps/shop/orrery.yaml":     []byte("service: github/acme/mono/apps/shop\nprovides: [{id: v1, title: Shop, kind: rest}]\n"),
		"apps/cart/go.mod":          []byte("module cart\n"),
		"apps/cart/main.go":         []byte("package main\nfunc main() {}\n"),
		"apps/cart/orrery.yaml":     []byte("service: github/acme/mono/apps/shop\n"),
		"deploy/alerts/orrery.yaml": []byte("groups: []\n"),
	}}
	found, _, err := Read(ctx, m, "github/acme/mono", "mono", nil, Bots{})
	if err != nil {
		t.Fatal(err)
	}
	var keys []string
	for _, d := range found.Descriptions {
		keys = append(keys, d.File+"="+d.Service)
	}
	same(t, keys, []string{"orrery.yaml=github/acme/mono", "apps/shop/orrery.yaml=github/acme/mono/apps/shop"})
	same(t, len(found.Problems), 1)
	if !strings.Contains(found.Problems[0], `apps/cart/orrery.yaml: orrery.yaml: service "github/acme/mono/apps/shop" is not "github/acme/mono/apps/cart" or a folder in it`) {
		t.Errorf("problems = %v", found.Problems)
	}
	if found.Describing("github/acme/mono/apps/shop") == nil || found.Describing("github/acme/mono/apps/cart") != nil {
		t.Error("Describing finds a service by its key")
	}
}

func TestSpecURL(t *testing.T) {
	same(t, SpecURL("api/openapi.yaml", "https://github.com/acme/app", "github"), any("https://github.com/acme/app/blob/HEAD/api/openapi.yaml"))
	same(t, SpecURL("./api/openapi.yaml", "https://gitlab.example.com/g/app", "gitlab"), any("https://gitlab.example.com/g/app/-/blob/HEAD/api/openapi.yaml"))
	same(t, SpecURL("api.yaml", "https://dev.azure.com/o/p/_git/app", "azuredevops"), any("https://dev.azure.com/o/p/_git/app?path=/api.yaml"))
	same(t, SpecURL("https://example.com/spec.json", "https://github.com/acme/app", "github"), any("https://example.com/spec.json"))
	same(t, SpecURL("", "https://github.com/acme/app", "github"), nil)
}

func TestEachTerraformRootIsItsOwnService(t *testing.T) {
	backend := "terraform {\n  backend \"azurerm\" {\n  }\n}\n"
	files := map[string]string{
		"infrastructure/cloudflare/versions.tf":                backend,
		"infrastructure/cloudflare/dns.tf":                     `resource "x" "y" {}`,
		"infrastructure/azure/terraform/versions.tf":           backend,
		"infrastructure/azure/terraform/deploy--common.tf":     "",
		"infrastructure/azure/terraform/modules/vault/main.tf": "",
		"workloads/api/main.go":                                "package main\nfunc main() {}\n",
		"workloads/api/Dockerfile":                             "FROM golang",
	}
	same(t, detect(files, "mono"), []string{
		"workloads/api backend Go api",
		"infrastructure/azure/terraform infrastructure Terraform azure",
		"infrastructure/cloudflare infrastructure Terraform cloudflare",
	})
	// One root keeps the repository's one infrastructure service.
	same(t, detect(map[string]string{"terraform/versions.tf": backend, "terraform/main.tf": ""}, "az-lz-shop"), []string{"terraform infrastructure Terraform az-lz-shop"})
}
