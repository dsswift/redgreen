package services

import (
	"context"
	"regexp"
	"slices"
	"strings"
)

// Commit is one commit as a forge names it. A field is empty when the forge
// left it out.
type Commit struct {
	At    string
	By    string
	Email string
}

// LastChange is the newest commit that touched a folder, and the newest of
// those a person made.
type LastChange struct {
	At, By, Email                string
	HumanAt, HumanBy, HumanEmail string
}

// HumanLookback is how many commits are read per folder while looking for
// the newest one a person made.
const HumanLookback = 30

// ReadBudget is how many files are read per repository at most. A monorepo
// past it is read in part; the services found so far stand.
const ReadBudget = 400

// Files is a repository's files at the head of its default branch, as a
// forge hands them over.
type Files interface {
	Commit() string
	// List is every file path, relative to the root, slash separated.
	List(ctx context.Context) ([]string, error)
	// Read is one file's bytes; nil with no error when it cannot be read.
	Read(ctx context.Context, path string) ([]byte, error)
	// Commits is the newest HumanLookback commits at or before this one that
	// touched path, a folder or "" for the root, newest first.
	Commits(ctx context.Context, path string) ([]Commit, error)
}

// botPattern matches automation: a release tool, a dependency updater, a CI
// job. `.invalid` is a domain that can never be real (RFC 2606), so an address
// on it is a job signing its own commits.
var botPattern = regexp.MustCompile(`(?i)@[^@\s]+\.invalid$|\[bot\]|(^|[-_ ])bot$|^bot[-_ ]|release-damnit|github-actions|dependabot|renovate|project_\d+_bot|^gitlab-?ci`)

// Bots tells automation from people. Extra holds more names or addresses, lowercase.
type Bots struct{ Extra []string }

// Is says whether a commit came from automation.
func (b Bots) Is(c Commit) bool {
	for _, n := range []string{c.By, c.Email} {
		if n == "" {
			continue
		}
		if botPattern.MatchString(n) || slices.Contains(b.Extra, strings.ToLower(n)) {
			return true
		}
	}
	return false
}

// LastChangeOf is the newest commit, and the newest a person made, from
// commits listed newest first. Nil when there are none.
func (b Bots) LastChangeOf(commits []Commit) *LastChange {
	if len(commits) == 0 {
		return nil
	}
	newest := commits[0]
	out := &LastChange{At: newest.At, By: newest.By, Email: EmailOf(newest.Email)}
	for _, c := range commits {
		if !b.Is(c) {
			out.HumanAt, out.HumanBy, out.HumanEmail = c.At, c.By, EmailOf(c.Email)
			break
		}
	}
	return out
}

var emailPattern = regexp.MustCompile(`^[^@\s]+@[^@\s]+$`)

// EmailOf is an address as the hub matches it, lowercase and trimmed; "" for
// anything that is not an address.
func EmailOf(v string) string {
	s := strings.ToLower(strings.TrimSpace(v))
	if emailPattern.MatchString(s) {
		return s
	}
	return ""
}
