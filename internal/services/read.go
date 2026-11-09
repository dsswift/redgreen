package services

import (
	"context"
	"slices"
	"strings"
)

// DescriptorFile is where a repository describes itself.
const DescriptorFile = "orrery.yaml"

// Found is the services at one commit of the default branch, and what the
// repository's orrery.yaml says about one of them.
type Found struct {
	// Commit is where they were read; "" for a repository with no commits.
	Commit    string      `json:"commit"`
	Rules     int         `json:"rules"`
	Services  []Service   `json:"services"`
	Described *Descriptor `json:"described,omitempty"`
	Icon      *Icon       `json:"icon,omitempty"`
	// Problem says why the orrery.yaml, or the logo it names, cannot be used.
	Problem string `json:"problem,omitempty"`
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
	out.Described, out.Icon, out.Problem, err = readDescriptor(ctx, files, paths, repoKey)
	if err != nil {
		return nil, Outcome{}, err
	}
	out.Services = Detect(paths, reader.read, repoName)
	if reader.err != nil {
		return nil, Outcome{}, reader.err
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

// readDescriptor reads the root orrery.yaml and the logo it names, at the
// same commit as the services. A file that cannot be used is a problem, not
// an error: the services still stand, described by their files alone. A logo
// that cannot be used costs only the logo.
func readDescriptor(ctx context.Context, files Files, paths []string, repoKey string) (*Descriptor, *Icon, string, error) {
	if !slices.Contains(paths, DescriptorFile) {
		return nil, nil, "", nil
	}
	text, err := files.Read(ctx, DescriptorFile)
	if err != nil || text == nil {
		return nil, nil, "", err
	}
	d, err := ParseDescriptor(text, repoKey)
	if err != nil {
		return nil, nil, err.Error(), nil
	}
	if d.Icon == nil {
		return d, nil, "", nil
	}
	file := strings.TrimPrefix(strings.TrimPrefix(d.Icon.File, "./"), "/")
	missing := `icon file "` + d.Icon.File + `" is not in the repository`
	if !slices.Contains(paths, file) {
		return d, nil, missing, nil
	}
	data, err := files.Read(ctx, file)
	if err != nil {
		return nil, nil, "", err
	}
	if data == nil {
		return d, nil, missing, nil
	}
	icon, err := CheckIcon(*d.Icon, data)
	if err != nil {
		return d, nil, err.Error(), nil
	}
	return d, icon, "", nil
}
