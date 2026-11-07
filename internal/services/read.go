package services

import (
	"context"
	"slices"
	"strings"
)

// DescriptorFile is where a repository describes itself.
const DescriptorFile = "orrery.yaml"

// Found is the services at one commit of the default branch, and the logo
// the repository's orrery.yaml names.
type Found struct {
	// Commit is where they were read; "" for a repository with no commits.
	Commit   string    `json:"commit"`
	Rules    int       `json:"rules"`
	Services []Service `json:"services"`
	Icon     *Icon     `json:"icon,omitempty"`
	// IconProblem says why a named logo cannot be used.
	IconProblem string `json:"iconProblem,omitempty"`
}

// Outcome says how a read went.
type Outcome struct {
	Reused bool
	// Read is how many files were read; past ReadBudget the rest were not.
	Read int
}

// Read finds the services at the head of a repository's default branch.
// The last answer stands while the branch has not moved and the rules have
// not changed, so a quiet repository costs one look at its head.
func Read(ctx context.Context, files Files, repoName string, previous *Found, bots Bots) (*Found, Outcome, error) {
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
	out.Icon, out.IconProblem, err = readLogo(ctx, files, paths)
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

// readLogo reads the logo the root orrery.yaml names for its service, at the
// same commit as the services. A logo named the wrong way is a problem, not
// an error: the services still stand.
func readLogo(ctx context.Context, files Files, paths []string) (*Icon, string, error) {
	if !slices.Contains(paths, DescriptorFile) {
		return nil, "", nil
	}
	text, err := files.Read(ctx, DescriptorFile)
	if err != nil || text == nil {
		return nil, "", err
	}
	service, ref, err := DescribedIcon(string(text))
	if err != nil {
		return nil, err.Error(), nil
	}
	if ref == nil {
		return nil, "", nil
	}
	file := strings.TrimPrefix(strings.TrimPrefix(ref.File, "./"), "/")
	if !slices.Contains(paths, file) {
		return nil, `icon file "` + ref.File + `" is not in the repository`, nil
	}
	data, err := files.Read(ctx, file)
	if err != nil {
		return nil, "", err
	}
	if data == nil {
		return nil, `icon file "` + ref.File + `" is not in the repository`, nil
	}
	icon, err := CheckIcon(*ref, data)
	if err != nil {
		return nil, err.Error(), nil
	}
	icon.Service = strings.ToLower(service)
	return icon, "", nil
}
