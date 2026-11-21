package services

import (
	"context"
	"path"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

// DescriptorFile is where a repository describes itself.
const DescriptorFile = "orrery.yaml"

// Found is the services at one commit of the default branch, and what the
// repository's orrery.yaml files say about them.
type Found struct {
	// Commit is where they were read; "" for a repository with no commits.
	Commit       string        `json:"commit"`
	Rules        int           `json:"rules"`
	Services     []Service     `json:"services"`
	Descriptions []Description `json:"descriptions,omitempty"`
	// Connections are the hosts the services' config files name.
	Connections []Connection `json:"connections,omitempty"`
	// Probed is the paths the estate asked to look for when this was read.
	Probed []string `json:"probed,omitempty"`
	// PathsPresent is the probed paths that exist: nil when none were probed
	// or the listing was partial, empty when probed and none exist.
	PathsPresent []string `json:"pathsPresent"`
	// Problems say why an orrery.yaml, the logo it names, or a config file cannot be used in full.
	Problems []string `json:"problems,omitempty"`
}

// Description is one orrery.yaml, with the logo it names when that could be read.
type Description struct {
	Descriptor
	// File is where it was read, from the repository root.
	File string `json:"file"`
	// Path is the folder its service names, as the tree spells it where the
	// tree holds that folder; "" for the repository itself.
	Path string `json:"path"`
	Logo *Icon  `json:"logo,omitempty"`
}

// Describing is the description of the service at key, or nil.
func (f *Found) Describing(key string) *Description {
	for i := range f.Descriptions {
		if f.Descriptions[i].Service == key {
			return &f.Descriptions[i]
		}
	}
	return nil
}

// Outcome says how a read went.
type Outcome struct {
	Reused bool
	// Read is how many files were read; past ReadBudget the rest were not.
	Read int
}

// Read finds the services at the head of a repository's default branch,
// reads its orrery.yaml, reads the hosts each service's config files name,
// and says which of the probe paths exist. The last answer stands while the
// branch has not moved and the rules and the probe have not changed, so a
// quiet repository costs one look at its head.
func Read(ctx context.Context, files Files, repoKey, repoName string, previous *Found, bots Bots, probe []string) (*Found, Outcome, error) {
	if files == nil {
		out := &Found{Rules: Rules, Probed: probe}
		if len(probe) > 0 {
			out.PathsPresent = []string{}
		}
		return out, Outcome{}, nil
	}
	if previous != nil && previous.Commit == files.Commit() && previous.Rules == Rules && slices.Equal(previous.Probed, probe) {
		return previous, Outcome{Reused: true}, nil
	}
	paths, err := files.List(ctx)
	if err != nil {
		return nil, Outcome{}, err
	}
	reader := &budgeted{ctx: ctx, files: files, budget: ReadBudget}
	out := &Found{Commit: files.Commit(), Rules: Rules, Probed: probe}
	if len(probe) > 0 {
		if p, ok := files.(Partial); ok && p.Partial() {
			out.Problems = append(out.Problems, "the forge cut the file tree short, so which probe paths exist is not known")
		} else {
			out.PathsPresent = Present(paths, probe)
		}
	}
	unused, err := out.describe(ctx, files, paths, repoKey)
	if err != nil {
		return nil, Outcome{}, err
	}
	out.Services = Detect(paths, reader.read, repoName, out.bounds(paths)...)
	out.connect(paths, reader.read)
	if reader.err != nil {
		return nil, Outcome{}, reader.err
	}
	out.Problems = append(out.Problems, out.unusable(unused)...)
	for i, s := range out.Services {
		commits, err := files.Commits(ctx, s.Path)
		if err != nil {
			return nil, Outcome{}, err
		}
		last := bots.LastChangeOf(commits)
		if last == nil {
			continue
		}
		out.Services[i].ChangedAt, out.Services[i].ChangedBy, out.Services[i].ChangedByEmail = last.At, last.By, last.Email
		out.Services[i].HumanChangedAt, out.Services[i].HumanChangedBy, out.Services[i].HumanChangedByEmail = last.HumanAt, last.HumanBy, last.HumanEmail
	}
	return out, Outcome{Read: reader.spent}, nil
}

// budgeted reads at most budget files, then answers "cannot read", so one
// huge repository cannot hold a sweep up. The first error stops every later read.
type budgeted struct {
	ctx    context.Context
	files  Files
	budget int
	spent  int
	err    error
}

func (b *budgeted) read(path string) (string, bool) {
	if b.err != nil {
		return "", false
	}
	b.spent++
	if b.spent > b.budget {
		return "", false
	}
	data, err := b.files.Read(b.ctx, path)
	if err != nil {
		b.err = err
		return "", false
	}
	if data == nil {
		return "", false
	}
	return string(data), true
}

// unused is an orrery.yaml that could not be used, why, and whether it names a service.
type unused struct {
	file, problem string
	claims        bool
}

// describe reads every orrery.yaml in the tree, the root's first, outside
// the folders that hold other people's code. The root one names a service of
// this repository; one in a folder names a service at or under that folder.
// Paths in a file are from the repository root. A file that cannot be used,
// or a second one for the same service, is a problem, not an error: the
// services still stand, described by their files alone. A logo that cannot
// be used costs only the logo.
func (f *Found) describe(ctx context.Context, files Files, paths []string, repoKey string) ([]unused, error) {
	var where []string
	for _, p := range paths {
		if baseOf(p) == DescriptorFile && p != DescriptorFile && !foreignPath(p) {
			where = append(where, p)
		}
	}
	slices.Sort(where)
	if slices.Contains(paths, DescriptorFile) {
		where = append([]string{DescriptorFile}, where...)
	}
	var out []unused
	for _, file := range where {
		scope := repoKey
		if dir := path.Dir(file); dir != "." {
			scope += "/" + strings.ToLower(dir)
		}
		d, problem, claims, err := readDescription(ctx, files, paths, file, scope)
		if err != nil {
			return nil, err
		}
		if d == nil {
			if problem != "" {
				out = append(out, unused{file, problem, claims})
			}
			continue
		}
		if problem != "" {
			f.Problems = append(f.Problems, file+": "+problem)
		}
		if f.Describing(d.Service) != nil {
			f.Problems = append(f.Problems, file+": service "+d.Service+" is described twice; the first file stands")
			continue
		}
		d.Path = folderOf(paths, strings.TrimPrefix(strings.TrimPrefix(d.Service, repoKey), "/"))
		f.Descriptions = append(f.Descriptions, *d)
	}
	return out, nil
}

// unusable are the problems of the files that could not be used, where each
// was meant for redgreen: at the root, beside a service, or naming one. Other
// tools name their own files orrery.yaml too.
func (f *Found) unusable(files []unused) []string {
	var out []string
	for _, u := range files {
		dir := dirOf(u.file)
		if u.file != DescriptorFile && !u.claims && !slices.ContainsFunc(f.Services, func(s Service) bool { return s.Path == dir }) {
			continue
		}
		out = append(out, u.file+": "+u.problem)
	}
	return out
}

// bounds are the folders the tree holds that an orrery.yaml names as a service.
func (f *Found) bounds(paths []string) []string {
	var out []string
	for _, d := range f.Descriptions {
		if d.Path != "" && slices.ContainsFunc(paths, func(p string) bool { return strings.HasPrefix(p, d.Path+"/") }) {
			out = append(out, d.Path)
		}
	}
	return out
}

// folderOf is a folder as the tree spells it, found by its lower-case name,
// or that name where the tree lacks it.
func folderOf(paths []string, lower string) string {
	if lower == "" {
		return ""
	}
	for _, p := range paths {
		if len(p) > len(lower) && p[len(lower)] == '/' && strings.ToLower(p[:len(lower)]) == lower {
			return p[:len(lower)]
		}
	}
	return lower
}

// foreignPath says whether a path sits in a folder of other people's code or of build output.
func foreignPath(p string) bool {
	parts := strings.Split(p, "/")
	return slices.ContainsFunc(parts[:len(parts)-1], func(f string) bool { return foreign[strings.ToLower(f)] })
}

// readDescription reads one orrery.yaml, whose service is scope or under it,
// and the logo it names. claims says whether a file that cannot be used
// still names a service.
func readDescription(ctx context.Context, files Files, paths []string, file, scope string) (d *Description, problem string, claims bool, err error) {
	text, err := files.Read(ctx, file)
	if err != nil || text == nil {
		return nil, "", false, err
	}
	parsed, err := ParseDescriptor(text, scope)
	if err != nil {
		var named struct {
			Service string `yaml:"service"`
		}
		_ = yaml.Unmarshal(text, &named)
		return nil, err.Error(), named.Service != "", nil
	}
	out := &Description{Descriptor: *parsed, File: file}
	if parsed.Icon == nil {
		return out, "", false, nil
	}
	logo := strings.TrimPrefix(strings.TrimPrefix(parsed.Icon.File, "./"), "/")
	missing := `icon file "` + parsed.Icon.File + `" is not in the repository`
	if !slices.Contains(paths, logo) {
		return out, missing, false, nil
	}
	data, err := files.Read(ctx, logo)
	if err != nil {
		return nil, "", false, err
	}
	if data == nil {
		return out, missing, false, nil
	}
	icon, err := CheckIcon(*parsed.Icon, data)
	if err != nil {
		return out, err.Error(), false, nil
	}
	out.Logo = icon
	return out, "", false, nil
}
