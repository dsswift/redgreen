package services

import "strings"

// Partial is a Files whose List may have left paths out, such as a tree the
// forge cut short. A path missing from a partial list may still exist.
type Partial interface {
	Partial() bool
}

// Present is the paths in probe, in probe's order, that exist in a listing
// of every file. A path is present as a file, or as a folder that holds a
// file at any depth. It is never nil, so "looked, found none" reads apart
// from "never looked".
func Present(paths, probe []string) []string {
	out := []string{}
	for _, want := range probe {
		for _, p := range paths {
			if p == want || strings.HasPrefix(p, want+"/") {
				out = append(out, want)
				break
			}
		}
	}
	return out
}
