package services

import (
	"context"
	"path"
	"slices"
	"strings"
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
	// Problems say why an orrery.yaml, or the logo it names, cannot be used.
	Problems []string `json:"problems,omitempty"`
}

// Description is one orrery.yaml, with the logo it names when that could be read.
type Description struct {
	Descriptor
	// File is where it was read, from the repository root.
	File string `json:"file"`
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

// Read finds the services at the head of a repository's default branch, and
// reads its orrery.yaml. The last answer stands while the branch has not
// moved and the rules have not changed, so a quiet repository costs one look
// at its head.
func Read(ctx context.Context, files Files, repoKey, repoName string, previous *Found, bots Bots) (*Found, Outcome, error) {
	if files == nil {
		return &Found{Rules: Rules}, Outcome{}, nil
	}
	if previous != nil && previous.Commit == files.Commit() && previous.Rules == Rules {
		return previous, Outcome{Reused: true}, nil
	}
	paths, err := files.List(ctx)
	if err != nil {
		return nil, Outcome{}, err
	}
	reader := &budgeted{ctx: ctx, files: files, budget: ReadBudget}
	out := &Found{Commit: files.Commit(), Rules: Rules}
	out.Services = Detect(paths, reader.read, repoName)
	if reader.err != nil {
		return nil, Outcome{}, reader.err
	}
	if err := out.describe(ctx, files, paths, repoKey); err != nil {
		return nil, Outcome{}, err
	}
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

// describe reads the orrery.yaml at the root and the one beside each
// service. The root one names a service of this repository; one in a folder
// names a service at or under that folder. Paths in a file are
// from the repository root. A file that cannot be used, or a second one for
// the same service, is a problem, not an error: the services still stand,
// described by their files alone. A logo that cannot be used costs only the
// logo.
func (f *Found) describe(ctx context.Context, files Files, paths []string, repoKey string) error {
	where := []string{DescriptorFile}
	for _, s := range f.Services {
		if s.Path != "" {
			where = append(where, s.Path+"/"+DescriptorFile)
		}
	}
	slices.Sort(where[1:])
	where = slices.Compact(where)
	for _, file := range where {
		if !slices.Contains(paths, file) {
			continue
		}
		scope := repoKey
		if dir := path.Dir(file); dir != "." {
			scope += "/" + strings.ToLower(dir)
		}
		d, problem, err := readDescription(ctx, files, paths, file, scope)
		if err != nil {
			return err
		}
		if problem != "" {
			f.Problems = append(f.Problems, file+": "+problem)
		}
		if d == nil {
			continue
		}
		if f.Describing(d.Service) != nil {
			f.Problems = append(f.Problems, file+": service "+d.Service+" is described twice; the first file stands")
			continue
		}
		f.Descriptions = append(f.Descriptions, *d)
	}
	return nil
}

// readDescription reads one orrery.yaml, whose service is scope or under it, and the logo it names.
func readDescription(ctx context.Context, files Files, paths []string, file, scope string) (*Description, string, error) {
	text, err := files.Read(ctx, file)
	if err != nil || text == nil {
		return nil, "", err
	}
	d, err := ParseDescriptor(text, scope)
	if err != nil {
		return nil, err.Error(), nil
	}
	out := &Description{Descriptor: *d, File: file}
	if d.Icon == nil {
		return out, "", nil
	}
	logo := strings.TrimPrefix(strings.TrimPrefix(d.Icon.File, "./"), "/")
	missing := `icon file "` + d.Icon.File + `" is not in the repository`
	if !slices.Contains(paths, logo) {
		return out, missing, nil
	}
	data, err := files.Read(ctx, logo)
	if err != nil {
		return nil, "", err
	}
	if data == nil {
		return out, missing, nil
	}
	icon, err := CheckIcon(*d.Icon, data)
	if err != nil {
		return out, err.Error(), nil
	}
	out.Logo = icon
	return out, "", nil
}
