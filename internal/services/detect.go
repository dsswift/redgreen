// Package services finds the services a repository holds from the files that
// build them: a .NET project that is an app, a package.json that starts one, a
// Go main package, a Functions host, a Dockerfile, a Terraform or Bicep root.
// Every forge hands over the same two things, the file list at a commit and a
// way to read one file, so these rules are the only place a service is
// recognised. The same commit always gives the same services.
package services

import (
	"encoding/json"
	"regexp"
	"slices"
	"sort"
	"strings"
)

// Rules is the version of the rules below. Bump it on any change, so every
// repository is read again instead of trusting its last answer.
const Rules = 10

// Service types.
const (
	Backend        = "backend"
	Frontend       = "frontend"
	Library        = "library"
	Infrastructure = "infrastructure"
	Tool           = "tool"
)

// Service is one piece of software a repository builds that runs on its own.
type Service struct {
	// ID is unique in the repository and stable while the code stays put: the folder, or "" for the root.
	ID       string `json:"id"`
	Path     string `json:"path"`
	Name     string `json:"name"`
	Type     string `json:"type"`
	Language string `json:"language,omitempty"`

	ChangedAt           string `json:"changedAt,omitempty"`
	ChangedBy           string `json:"changedBy,omitempty"`
	ChangedByEmail      string `json:"changedByEmail,omitempty"`
	HumanChangedAt      string `json:"humanChangedAt,omitempty"`
	HumanChangedBy      string `json:"humanChangedBy,omitempty"`
	HumanChangedByEmail string `json:"humanChangedByEmail,omitempty"`

	Files ServiceFiles `json:"files"`
}

// ServiceFiles is what one service carries in its own folder.
type ServiceFiles struct {
	Readme     bool `json:"readme"`
	Dockerfile bool `json:"dockerfile"`
	Tests      bool `json:"tests"`
}

var skippedDirs = map[string]bool{}

func init() {
	for _, d := range strings.Fields(`test tests __tests__ __mocks__ testdata fixtures e2e spec specs
		sample samples example examples benchmarks
		vendor node_modules third_party bower_components packages-cache
		dist build bin obj out target .terraform
		docs doc archive archives
		.git .github .gitlab .vscode .idea .venv venv __pycache__ .obsidian`) {
		skippedDirs[d] = true
	}
}

var testProject = regexp.MustCompile(`(?i)(^|[.\-_])(unit|integration|functional|acceptance|e2e)?tests?$`)

// markers are the kinds of build file, strongest first: where two claim one folder, the first wins.
var markers = []string{"dotnet", "go", "rust", "swift", "python", "node", "functions", "docker"}

type found struct {
	path, name, typ, language, marker string
}

type libraryMark struct{ path, language string }

func dirOf(p string) string {
	if i := strings.LastIndex(p, "/"); i >= 0 {
		return p[:i]
	}
	return ""
}

func baseOf(p string) string { return p[strings.LastIndex(p, "/")+1:] }

func within(p, dir string) bool { return dir == "" || p == dir || strings.HasPrefix(p, dir+"/") }

func join(dir, name string) string {
	if dir == "" {
		return name
	}
	return dir + "/" + name
}

var hiddenDir = regexp.MustCompile(`^[._]`)

// skipped says whether a path sits in a folder named for tests, samples, or
// output, or one a leading `.` or `_` marks as tooling or parked.
func skipped(p string) bool {
	parts := strings.Split(p, "/")
	for _, part := range parts[:len(parts)-1] {
		if skippedDirs[strings.ToLower(part)] || testProject.MatchString(part) || hiddenDir.MatchString(part) {
			return true
		}
	}
	return false
}

// commonDir is the deepest folder every path sits in.
func commonDir(paths []string) string {
	if len(paths) == 0 {
		return ""
	}
	common := strings.Split(dirOf(paths[0]), "/")
	for _, p := range paths[1:] {
		parts := strings.Split(dirOf(p), "/")
		i := 0
		for i < len(common) && i < len(parts) && common[i] == parts[i] {
			i++
		}
		common = common[:i]
	}
	return strings.Join(common, "/")
}

// languageUnder is the language most of the code under a folder is written in.
func languageUnder(files []string, dir string) string {
	counts := map[string]float64{}
	for _, f := range files {
		if !within(f, dir) {
			continue
		}
		if l := LanguageOfFile(f); l != "" {
			counts[l]++
		}
	}
	return MainLanguage(counts)
}

// Reader reads one file's text: ok is false when it cannot be read.
type Reader func(path string) (text string, ok bool)

var (
	reDotnetProj   = regexp.MustCompile(`(?i)\.(cs|fs|vb)proj$`)
	reDotnetExt    = regexp.MustCompile(`(?i)\.\w+proj$`)
	reTestSdk      = regexp.MustCompile(`(?i)Microsoft\.NET\.Test\.Sdk|<IsTestProject>\s*true`)
	reAssembly     = regexp.MustCompile(`<AssemblyName>\s*([^<\s]+)\s*<`)
	reCsproj       = regexp.MustCompile(`(?i)\.csproj$`)
	rePages        = regexp.MustCompile(`(?i)\.(razor|cshtml|aspx)$`)
	reFunctionsVer = regexp.MustCompile(`(?i)<AzureFunctionsVersion>`)
	reBlazor       = regexp.MustCompile(`(?i)Sdk="Microsoft\.NET\.Sdk\.BlazorWebAssembly"`)
	reWebSdk       = regexp.MustCompile(`(?i)Sdk="Microsoft\.NET\.Sdk\.Web"`)
	reWebForms     = regexp.MustCompile(`(?i)349c5851-65df-11da-9384-00065b846f21`)
	reWorker       = regexp.MustCompile(`(?i)Sdk="Microsoft\.NET\.Sdk\.Worker"`)
	reExe          = regexp.MustCompile(`(?i)<OutputType>\s*(Win)?Exe\s*<`)
	reGoCmd        = regexp.MustCompile(`(^|/)cmd/[^/]+/[^/]+\.go$`)
	reGoMain       = regexp.MustCompile(`(?m)^package main\b`)
	reGoFunc       = regexp.MustCompile(`\bfunc main\(`)
	reGoServes     = regexp.MustCompile(`ListenAndServe|http\.Server\{|grpc\.NewServer`)
	reDockerAny    = regexp.MustCompile(`(?i)(^|/)Dockerfile$|\.dockerfile$`)
	reDockerfile   = regexp.MustCompile(`(?i)(^|/)Dockerfile$`)
	reTerraform    = regexp.MustCompile(`(?i)terraform`)
	reWorkspace    = regexp.MustCompile(`(?m)^\[workspace\]`)
	rePackage      = regexp.MustCompile(`(?m)^\[package\]`)
	reTomlName     = regexp.MustCompile(`(?m)^name\s*=\s*"([^"]+)"`)
	reRustBin      = regexp.MustCompile(`(?m)^\[\[bin\]\]`)
	reRustServer   = regexp.MustCompile(`(?m)^\s*(actix-web|axum|rocket|warp|hyper|tonic)\s*=`)
	reXcode        = regexp.MustCompile(`^(.*?)/?[^/]+\.xcodeproj/`)
	reSwiftExec    = regexp.MustCompile(`executableTarget`)
	reSwiftName    = regexp.MustCompile(`name:\s*"([^"]+)"`)
	rePyName       = regexp.MustCompile(`(?m)^name\s*=\s*["']([^"']+)["']`)
	rePyNameAny    = regexp.MustCompile(`\bname\s*=\s*["']([^"']+)["']`)
	rePyServes     = regexp.MustCompile(`(?i)["'\s](fastapi|flask|django|uvicorn|gunicorn|starlette|aiohttp|azure-functions)\b`)
	rePyScripts    = regexp.MustCompile(`(?m)^\[(project|tool\.poetry)\.scripts\]`)
	rePyConsole    = regexp.MustCompile(`console_scripts`)
	reNodeScope    = regexp.MustCompile(`^@[^/]+/`)
	reK8s          = regexp.MustCompile(`(^|/)(kustomization\.ya?ml|Chart\.yaml)$`)
	reBicepModule  = regexp.MustCompile(`(^|/)modules/`)
	reTfVars       = regexp.MustCompile(`\.(tfvars|tfbackend)$|\.tfvars\.json$`)
	reTfLikely     = regexp.MustCompile(`^(backend|main|providers|versions|terraform)\.tf$`)
	reTfBackend    = regexp.MustCompile(`(?m)^\s*backend\s+"?[\w-]+"?\s*\{`)
	reProse        = regexp.MustCompile(`(?i)\.(md|mdx|txt|rst|adoc)$`)
)

// Detect finds every service in one repository's files, each with what sits
// beside it. repoName names a service at the root that names nothing itself.
func Detect(paths []string, read Reader, repoName string) []Service {
	services := find(paths, read, repoName)
	files := serviceFiles(paths, services, read)
	for i := range services {
		services[i].Files = files[i]
	}
	return services
}

func find(paths []string, read Reader, repoName string) []Service {
	var files []string
	for _, p := range paths {
		if !skipped(p) {
			files = append(files, p)
		}
	}
	sort.Strings(files)
	has := func(p string) bool { return slices.Contains(files, p) }
	name := func(dir string) string {
		if dir == "" {
			return repoName
		}
		return baseOf(dir)
	}
	var all []found
	var libraries []libraryMark
	add := func(f found) { all = append(all, f) }

	// .NET: a project is an app when it is a web app, a Functions app, a worker, or an exe.
	for _, file := range files {
		if !reDotnetProj.MatchString(file) {
			continue
		}
		dir := dirOf(file)
		project := reDotnetExt.ReplaceAllString(baseOf(file), "")
		if testProject.MatchString(project) {
			continue
		}
		xml, ok := read(file)
		if !ok || reTestSdk.MatchString(xml) {
			continue
		}
		nm := project
		if m := reAssembly.FindStringSubmatch(xml); m != nil {
			nm = m[1]
		}
		language := "Other"
		if reCsproj.MatchString(file) {
			language = "C#"
		}
		pages := false
		for _, f := range files {
			if within(f, dir) && rePages.MatchString(f) {
				pages = true
				break
			}
		}
		typ := ""
		switch {
		case reFunctionsVer.MatchString(xml):
			typ = Backend
		case reBlazor.MatchString(xml):
			typ = Frontend
		case reWebSdk.MatchString(xml):
			typ = Backend
			if pages {
				typ = Frontend
			}
		case reWebForms.MatchString(xml) || has(join(dir, "Web.config")):
			typ = Frontend
		case reWorker.MatchString(xml):
			typ = Backend
		case reExe.MatchString(xml):
			typ = Tool
		}
		if typ != "" {
			add(found{dir, nm, typ, language, "dotnet"})
		} else {
			libraries = append(libraries, libraryMark{dir, language})
		}
	}

	// Go: every main package. One that serves, or the only one with a Dockerfile to run it, is a backend.
	type goMain struct{ dir, text string }
	var goMains []goMain
	for _, file := range files {
		if !(baseOf(file) == "main.go" || (reGoCmd.MatchString(file) && !strings.HasSuffix(file, "_test.go"))) {
			continue
		}
		dir := dirOf(file)
		if slices.ContainsFunc(goMains, func(m goMain) bool { return m.dir == dir }) {
			continue
		}
		text, ok := read(file)
		if ok && reGoMain.MatchString(text) && reGoFunc.MatchString(text) {
			goMains = append(goMains, goMain{dir, text})
		}
	}
	dockerized := slices.ContainsFunc(files, reDockerAny.MatchString)
	for _, m := range goMains {
		typ := Tool
		if reGoServes.MatchString(m.text) || (len(goMains) == 1 && dockerized) {
			typ = Backend
		}
		add(found{m.dir, name(m.dir), typ, "Go", "go"})
	}
	if len(goMains) == 0 {
		for _, f := range files {
			if baseOf(f) == "go.mod" {
				libraries = append(libraries, libraryMark{dirOf(f), "Go"})
			}
		}
	}

	// Rust: a crate with a binary.
	for _, file := range files {
		if baseOf(file) != "Cargo.toml" {
			continue
		}
		dir := dirOf(file)
		toml, ok := read(file)
		if !ok || (reWorkspace.MatchString(toml) && !rePackage.MatchString(toml)) {
			continue
		}
		nm := name(dir)
		if m := reTomlName.FindStringSubmatch(toml); m != nil {
			nm = m[1]
		}
		if reRustBin.MatchString(toml) || has(join(dir, "src/main.rs")) {
			typ := Tool
			if reRustServer.MatchString(toml) {
				typ = Backend
			}
			add(found{dir, nm, typ, "Rust", "rust"})
		} else {
			libraries = append(libraries, libraryMark{dir, "Rust"})
		}
	}

	// Swift: an Xcode app, or a package with an executable.
	xcode := map[string]bool{}
	var xcodeDirs []string
	for _, f := range files {
		if m := reXcode.FindStringSubmatch(f); m != nil && !xcode[m[1]] {
			xcode[m[1]] = true
			xcodeDirs = append(xcodeDirs, m[1])
		}
	}
	for _, dir := range xcodeDirs {
		add(found{dir, name(dir), Frontend, "Swift", "swift"})
	}
	for _, file := range files {
		if baseOf(file) != "Package.swift" {
			continue
		}
		dir := dirOf(file)
		if xcode[dir] {
			continue
		}
		text, ok := read(file)
		if ok && reSwiftExec.MatchString(text) {
			nm := name(dir)
			if m := reSwiftName.FindStringSubmatch(text); m != nil {
				nm = m[1]
			}
			add(found{dir, nm, Tool, "Swift", "swift"})
		} else {
			libraries = append(libraries, libraryMark{dir, "Swift"})
		}
	}

	// Python: a project that serves or installs a command, a Django site, or a setup.py with console scripts.
	for _, file := range files {
		b := baseOf(file)
		if b != "pyproject.toml" && b != "setup.py" && b != "manage.py" {
			continue
		}
		dir := dirOf(file)
		if slices.ContainsFunc(all, func(f found) bool { return f.marker == "python" && f.path == dir }) {
			continue
		}
		text, ok := read(file)
		if !ok {
			continue
		}
		nm := name(dir)
		if m := rePyName.FindStringSubmatch(text); m != nil {
			nm = m[1]
		} else if m := rePyNameAny.FindStringSubmatch(text); m != nil {
			nm = m[1]
		}
		serves := b == "manage.py" || rePyServes.MatchString(text)
		command := rePyScripts.MatchString(text) || rePyConsole.MatchString(text)
		if serves || command {
			typ := Tool
			if serves {
				typ = Backend
			}
			add(found{dir, nm, typ, "Python", "python"})
		} else {
			libraries = append(libraries, libraryMark{dir, "Python"})
		}
	}

	// Node: a package that is a web app, a server, or a command. A workspace root only gathers others.
	for _, file := range files {
		if baseOf(file) != "package.json" {
			continue
		}
		dir := dirOf(file)
		text, ok := read(file)
		if !ok {
			text = "null"
		}
		var pkg struct {
			Name            string         `json:"name"`
			Bin             any            `json:"bin"`
			Workspaces      any            `json:"workspaces"`
			Scripts         map[string]any `json:"scripts"`
			Dependencies    map[string]any `json:"dependencies"`
			DevDependencies map[string]any `json:"devDependencies"`
		}
		if err := json.Unmarshal([]byte(text), &pkg); err != nil {
			continue
		}
		if truthy(pkg.Workspaces) {
			continue
		}
		deps := func(names ...string) bool {
			for _, n := range names {
				if _, ok := pkg.Dependencies[n]; ok {
					return true
				}
				if _, ok := pkg.DevDependencies[n]; ok {
					return true
				}
			}
			return false
		}
		nm := reNodeScope.ReplaceAllString(pkg.Name, "")
		if nm == "" {
			nm = name(dir)
		}
		language := "JavaScript"
		if deps("typescript") || has(join(dir, "tsconfig.json")) {
			language = "TypeScript"
		}
		frontend := deps("next", "nuxt", "@angular/core", "react-scripts", "astro", "gatsby", "@sveltejs/kit", "@remix-run/react", "electron") ||
			(deps("vite") && deps("react", "vue", "svelte", "solid-js", "preact", "lit"))
		backend := deps("express", "hono", "fastify", "koa", "@nestjs/core", "@hapi/hapi", "@azure/functions", "restify") || truthy(pkg.Scripts["start"])
		typ := ""
		switch {
		case frontend:
			typ = Frontend
		case backend:
			typ = Backend
		case truthy(pkg.Bin):
			typ = Tool
		}
		if typ != "" {
			add(found{dir, nm, typ, language, "node"})
		} else {
			libraries = append(libraries, libraryMark{dir, language})
		}
	}

	// Azure Functions: a host.json, or a lone function.json from before hosts were required.
	var hosts []string
	for _, f := range files {
		if baseOf(f) == "host.json" {
			hosts = append(hosts, dirOf(f))
		}
	}
	for _, dir := range hosts {
		add(found{dir, name(dir), Backend, languageUnder(files, dir), "functions"})
	}
	for _, file := range files {
		if baseOf(file) != "function.json" {
			continue
		}
		fn := dirOf(file)
		if slices.ContainsFunc(hosts, func(h string) bool { return within(fn, h) }) {
			continue
		}
		add(found{fn, name(fn), Backend, languageUnder(files, fn), "functions"})
	}

	// A Dockerfile nothing else explains runs something of its own. One that installs Terraform only runs the infrastructure.
	for _, file := range files {
		if !reDockerfile.MatchString(file) {
			continue
		}
		dir := dirOf(file)
		if slices.ContainsFunc(all, func(f found) bool { return within(f.path, dir) || within(dir, f.path) }) {
			continue
		}
		text, ok := read(file)
		if !ok || reTerraform.MatchString(text) {
			continue
		}
		add(found{dir, name(dir), Backend, languageUnder(files, dir), "docker"})
	}

	var services []Service
	for _, f := range merge(all) {
		services = append(services, Service{ID: f.path, Path: f.path, Name: f.name, Type: f.typ, Language: f.language})
	}

	// Infrastructure: a Terraform root that keeps state, or Bicep templates. Terraform with no root is a module library.
	var terraform, bicep []string
	for _, f := range files {
		if strings.HasSuffix(f, ".tf") {
			terraform = append(terraform, f)
		}
		if strings.HasSuffix(f, ".bicep") {
			bicep = append(bicep, f)
		}
	}
	type infraKind struct {
		kind, language string
		files          []string
	}
	var infra *infraKind
	if len(terraform) > 0 || len(bicep) > 0 {
		language := "Terraform"
		if len(bicep) > len(terraform) {
			language = "Bicep"
		}
		deployed := slices.ContainsFunc(bicep, func(f string) bool { return !reBicepModule.MatchString(f) }) || terraformRoot(files, terraform, read)
		kind := Library
		if deployed {
			kind = Infrastructure
		}
		infra = &infraKind{kind, language, append(append([]string{}, terraform...), bicep...)}
	} else if len(services) == 0 {
		var k8s []string
		for _, f := range files {
			if reK8s.MatchString(f) {
				k8s = append(k8s, f)
			}
		}
		if len(k8s) > 0 {
			infra = &infraKind{Infrastructure, "Other", k8s}
		}
	}
	// Two or more Terraform roots, each keeping its own state, are separate
	// projects: one service each, in its own folder.
	if roots := terraformRoots(terraform, read); len(roots) > 1 && len(bicep) == 0 {
		for _, r := range roots {
			id := r
			if slices.ContainsFunc(services, func(s Service) bool { return s.ID == r }) {
				id = join(r, "infrastructure")
			}
			services = append(services, Service{ID: id, Path: r, Name: rootName(r, repoName), Type: Infrastructure, Language: "Terraform"})
		}
		infra = nil
	}
	if infra != nil {
		p := commonDir(infra.files)
		alone := len(services) == 0
		id := p
		if slices.ContainsFunc(services, func(s Service) bool { return s.ID == p }) {
			id = join(p, "infrastructure")
		}
		nm := repoName
		if !alone {
			nm = repoName + " infrastructure"
		}
		language := infra.language
		if language == "Other" {
			language = ""
		}
		services = append(services, Service{ID: id, Path: p, Name: nm, Type: infra.kind, Language: language})
	}
	if len(services) > 0 {
		return services
	}

	// Nothing runs. Libraries make one library; scripts in a repository that is not mostly prose make one tool.
	if len(libraries) > 0 {
		counts := map[string]float64{}
		var marks []string
		for _, l := range libraries {
			if l.language != "" {
				counts[l.language]++
			}
			marks = append(marks, join(l.path, "x"))
		}
		p := commonDir(marks)
		nm := repoName
		if p != "" {
			nm = baseOf(p)
		}
		return []Service{{ID: p, Path: p, Name: nm, Type: Library, Language: MainLanguage(counts)}}
	}
	var code []string
	prose := 0
	for _, f := range files {
		if l := LanguageOfFile(f); l != "" && l != "HTML" && l != "CSS" && l != "SQL" {
			code = append(code, f)
		}
		if reProse.MatchString(f) {
			prose++
		}
	}
	if len(code) > 0 && prose <= len(code)*3 {
		return []Service{{ID: "", Path: "", Name: repoName, Type: Tool, Language: languageUnder(code, "")}}
	}
	return nil
}

func truthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case string:
		return x != ""
	case float64:
		return x != 0
	default:
		return true
	}
}

// merge makes one folder one service. Where two build files claim it, the
// stronger kind names it, and a web front end in it makes the whole a front
// end. A front end and a .NET web host nested one in the other are one app.
// A Functions host or a Dockerfile inside another service belongs to it, and
// one that holds other services is only their shared settings.
func merge(all []found) []found {
	rank := func(f found) int { return slices.Index(markers, f.marker) }
	sorted := append([]found{}, all...)
	sort.SliceStable(sorted, func(i, j int) bool { return rank(sorted[i]) < rank(sorted[j]) })
	byPath := map[string]*found{}
	var order []*found
	for _, f := range sorted {
		if held, ok := byPath[f.path]; ok {
			if f.typ == Frontend {
				held.typ = Frontend
			}
			continue
		}
		c := f
		byPath[f.path] = &c
		order = append(order, &c)
	}
	web := func(f *found) bool {
		return (f.marker == "node" && f.typ == Frontend) || (f.marker == "dotnet" && (f.typ == Frontend || f.typ == Backend))
	}
	var out []found
	for _, f := range order {
		inner := false
		for _, o := range order {
			if o.path != f.path && within(o.path, f.path) {
				inner = true
				break
			}
		}
		if (f.marker == "functions" || f.marker == "docker") && inner {
			continue
		}
		var owner *found
		for _, o := range order {
			if o.path != f.path && within(f.path, o.path) {
				owner = o
				break
			}
		}
		if owner == nil {
			out = append(out, *f)
			continue
		}
		if web(f) && web(owner) && f.marker != owner.marker && (f.typ == Frontend || owner.typ == Frontend) {
			owner.typ = Frontend
			continue
		}
		if f.marker != "functions" && f.marker != "docker" {
			out = append(out, *f)
		}
	}
	// An owner may have become a front end after it was copied out; read the final type back.
	for i := range out {
		out[i].typ = byPath[out[i].path].typ
	}
	sort.SliceStable(out, func(i, j int) bool { return localeLess(out[i].path, out[j].path) })
	return out
}

// localeLess orders folders the way a person reads them: case folded first.
func localeLess(a, b string) bool {
	la, lb := strings.ToLower(a), strings.ToLower(b)
	if la != lb {
		return la < lb
	}
	return a < b
}

// terraformRoot says whether any Terraform here is applied on its own: it
// keeps state in a backend, or has variables files to apply with.
func terraformRoot(files, terraform []string, read Reader) bool {
	if len(terraform) == 0 {
		return false
	}
	if slices.ContainsFunc(files, reTfVars.MatchString) {
		return true
	}
	n := 0
	for _, f := range terraform {
		if !reTfLikely.MatchString(baseOf(f)) {
			continue
		}
		if n++; n > 40 {
			break
		}
		if text, ok := read(f); ok && reTfBackend.MatchString(text) {
			return true
		}
	}
	return false
}

// terraformRoots are the folders whose Terraform keeps state: a likely root
// file there declares a backend. A root inside another is not counted.
func terraformRoots(terraform []string, read Reader) []string {
	var roots []string
	n := 0
	for _, f := range terraform {
		if !reTfLikely.MatchString(baseOf(f)) {
			continue
		}
		if n++; n > 200 {
			break
		}
		if text, ok := read(f); ok && reTfBackend.MatchString(text) && !slices.Contains(roots, dirOf(f)) {
			roots = append(roots, dirOf(f))
		}
	}
	sort.Strings(roots)
	var outer []string
	for _, r := range roots {
		if !slices.ContainsFunc(outer, func(o string) bool { return o == "" || strings.HasPrefix(r, o+"/") }) {
			outer = append(outer, r)
		}
	}
	return outer
}

// rootName names a Terraform root for its folder, or the folder above when
// its own name only says it is Terraform.
func rootName(dir, repoName string) string {
	switch b := baseOf(dir); {
	case dir == "":
		return repoName
	case b == "terraform" || b == "tf" || b == "infra" || b == "infrastructure":
		if parent := dirOf(dir); parent != "" {
			return baseOf(parent)
		}
		return repoName
	default:
		return b
	}
}

var (
	foreign    = map[string]bool{"node_modules": true, "vendor": true, "third_party": true, "bower_components": true, "dist": true, "build": true, "bin": true, "obj": true, "out": true, "target": true, ".git": true, ".terraform": true}
	testDir    = regexp.MustCompile(`(?i)^(tests?|__tests__|e2e|specs?|(unit|integration|functional|acceptance)[.\-_]?tests?)$|^tests?[.\-_]|[.\-_](unit|integration|functional|acceptance|e2e)?tests?$`)
	testFile   = regexp.MustCompile(`(_test\.go|\.(test|spec)\.[cm]?[jt]sx?|(^|/)test_[^/]*\.py|_test\.py|Tests?\.(cs|swift))$`)
	readmeName = regexp.MustCompile(`(?i)^readme(\.|$)`)
	dockerName = regexp.MustCompile(`(?i)^dockerfile$|\.dockerfile$`)
	bareTests  = regexp.MustCompile(`(?i)^(tests?|__tests__|e2e|specs?)$`)
	testPrefix = regexp.MustCompile(`(?i)^tests?[.\-_]`)
	testSuffix = regexp.MustCompile(`(?i)[.\-_](unit|integration|functional|acceptance|e2e)?tests?$`)
)

// testedName is what a test project's name says it tests: Tests.Orders.Api
// and Orders.Api.Tests both test Orders.Api. A bare tests folder names nothing.
func testedName(folder string) string {
	if bareTests.MatchString(folder) {
		return ""
	}
	return strings.ToLower(testSuffix.ReplaceAllString(testPrefix.ReplaceAllString(folder, ""), ""))
}

// serviceFiles says which service each README, Dockerfile, and test belongs to.
//
// A README documents the service whose folder holds it directly, or every
// service below it while that folder holds no other. A README deeper inside a
// service describes that subfolder and is not the service's.
//
// A Dockerfile builds the deepest service whose folder holds it, and any
// service below it that it names by folder. One in a folder above a single
// service builds that service.
//
// Tests belong to the deepest service whose folder holds them. A test project
// named for a service belongs to it wherever it sits. Infrastructure is left
// out of the sharing.
func serviceFiles(paths []string, services []Service, read Reader) []ServiceFiles {
	result := make([]ServiceFiles, len(services))
	type indexed struct {
		s Service
		i int
	}
	var apps []indexed
	for i, s := range services {
		if s.Type != Infrastructure {
			apps = append(apps, indexed{s, i})
		}
	}
	holder := func(p string) int {
		best := -1
		for i, s := range services {
			if within(p, s.Path) && (best == -1 || len(s.Path) > len(services[best].Path)) {
				best = i
			}
		}
		return best
	}
	below := func(dir string) []indexed {
		for d := dir; ; d = dirOf(d) {
			var under []indexed
			for _, a := range apps {
				if within(a.s.Path, d) {
					under = append(under, a)
				}
			}
			if len(under) > 0 || d == "" {
				return under
			}
		}
	}
	for _, p := range paths {
		parts := strings.Split(p, "/")
		folders := parts[:len(parts)-1]
		if slices.ContainsFunc(folders, func(f string) bool { return foreign[strings.ToLower(f)] }) {
			continue
		}
		name := baseOf(p)
		dir := dirOf(p)
		if readmeName.MatchString(name) {
			own := slices.IndexFunc(services, func(s Service) bool { return s.Path == dir })
			if own >= 0 {
				result[own].Readme = true
			} else if holder(p) == -1 {
				if under := below(dir); len(under) == 1 {
					result[under[0].i].Readme = true
				}
			}
			continue
		}
		if dockerName.MatchString(name) {
			own := holder(p)
			var under []indexed
			for _, a := range below(dir) {
				if a.s.Path != dir && within(a.s.Path, dir) && (own == -1 || a.s.Path != services[own].Path) {
					under = append(under, a)
				}
			}
			if own != -1 {
				result[own].Dockerfile = true
			} else if len(under) == 1 {
				result[under[0].i].Dockerfile = true
			}
			limit := 0
			if own == -1 {
				limit = 1
			}
			if len(under) > limit {
				text, _ := read(p)
				for _, a := range under {
					rel := a.s.Path
					if dir != "" {
						rel = a.s.Path[len(dir)+1:]
					}
					if regexp.MustCompile(`(?m)(^|[\s/"'.])` + regexp.QuoteMeta(rel) + `([\s/"']|$)`).MatchString(text) {
						result[a.i].Dockerfile = true
					}
				}
			}
			continue
		}
		if LanguageOfFile(p) == "" {
			continue
		}
		var testDirs []string
		for _, f := range folders {
			if testDir.MatchString(f) {
				testDirs = append(testDirs, f)
			}
		}
		if len(testDirs) == 0 && !testFile.MatchString(p) {
			continue
		}
		tested := ""
		for k := len(testDirs) - 1; k >= 0; k-- {
			if n := testedName(testDirs[k]); n != "" {
				tested = n
				break
			}
		}
		i := -1
		if tested != "" {
			for _, a := range apps {
				if strings.ToLower(baseOf(a.s.Path)) == tested || strings.ToLower(a.s.Name) == tested {
					i = a.i
					break
				}
			}
		}
		if i == -1 {
			i = holder(p)
		}
		if i == -1 {
			if under := below(dir); len(under) == 1 {
				i = under[0].i
			}
		}
		if i != -1 {
			result[i].Tests = true
		}
	}
	return result
}
