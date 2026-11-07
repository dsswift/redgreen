package outcomes

import (
	"net/url"
	"slices"
	"strings"

	"github.com/dsswift/redgreen/internal/provider"
)

// RepoRef is a repository, and a folder in it, named by a browser URL in a catalog.
type RepoRef struct {
	Provider provider.Kind
	// FullName is as the forge names it: owner/name, group/sub/project, or org/project/repo.
	FullName string
	// Path is the folder the URL points into, "" for the root.
	Path string
}

// Key is the repository's key on the hub.
func (r RepoRef) Key() string { return strings.ToLower(string(r.Provider) + "/" + r.FullName) }

// ParseRepoURL reads a catalog's repository URL: a GitHub repository, with or
// without /tree/<branch>/<folder>; a GitLab project, /-/tree/<branch>/<folder>
// allowed; or an Azure DevOps repository. gitlabHost is a self-managed
// instance, if any. ok is false when it is none of those.
func ParseRepoURL(raw, gitlabHost string) (RepoRef, bool) {
	if raw == "" {
		return RepoRef{}, false
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return RepoRef{}, false
	}
	var parts []string
	for _, p := range strings.Split(u.Path, "/") {
		if p != "" {
			parts = append(parts, p)
		}
	}
	host := strings.ToLower(u.Hostname())
	switch {
	case host == "github.com":
		if len(parts) < 2 {
			return RepoRef{}, false
		}
		ref := RepoRef{Provider: provider.GitHub, FullName: parts[0] + "/" + strings.TrimSuffix(parts[1], ".git")}
		if len(parts) > 3 && (parts[2] == "tree" || parts[2] == "blob") {
			ref.Path = strings.Join(parts[4:], "/")
		}
		return ref, true
	case host == "dev.azure.com" || strings.HasSuffix(host, ".visualstudio.com"):
		git := slices.Index(parts, "_git")
		if git < 1 || git+1 >= len(parts) {
			return RepoRef{}, false
		}
		org := strings.Split(host, ".")[0]
		if host == "dev.azure.com" {
			org = parts[0]
		}
		name, _ := url.PathUnescape(parts[git+1])
		return RepoRef{Provider: provider.AzureDevOps, FullName: org + "/" + parts[git-1] + "/" + name}, true
	case host == strings.ToLower(gitlabHost) || host == "gitlab.com":
		dash := slices.Index(parts, "-")
		project := parts
		if dash >= 0 {
			project = parts[:dash]
		}
		if len(project) < 2 {
			return RepoRef{}, false
		}
		project = append(append([]string{}, project[:len(project)-1]...), strings.TrimSuffix(project[len(project)-1], ".git"))
		ref := RepoRef{Provider: provider.GitLab, FullName: strings.Join(project, "/")}
		if dash >= 0 && dash+1 < len(parts) && (parts[dash+1] == "tree" || parts[dash+1] == "blob") && dash+3 <= len(parts) {
			ref.Path = strings.Join(parts[dash+3:], "/")
		}
		return ref, true
	}
	return RepoRef{}, false
}

// SameRepo says whether two refs name one repository, however written.
func SameRepo(a, b RepoRef) bool {
	return a.Provider == b.Provider && strings.EqualFold(a.FullName, b.FullName)
}
