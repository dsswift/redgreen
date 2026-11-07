package services

import (
	"path"
	"slices"
	"sort"
	"strings"
)

// Languages is the list a main language is picked from, in the order the hub
// offers them. A language not on it counts as Other.
var Languages = []string{"C#", "Go", "Python", "TypeScript", "JavaScript", "Terraform", "Bicep", "PowerShell", "Shell", "Swift", "Rust", "C++", "Lua", "GDScript", "HTML", "CSS", "SQL", "Other"}

// named is what a forge calls a language, where that differs from the list.
var named = map[string]string{
	"HCL": "Terraform", "TSX": "TypeScript", "JSX": "JavaScript", "Vue": "JavaScript", "Svelte": "JavaScript",
	"SCSS": "CSS", "Sass": "CSS", "Less": "CSS", "Batchfile": "Shell", "TSQL": "SQL", "PLpgSQL": "SQL", "PLSQL": "SQL",
	"Jupyter Notebook": "Python", "C": "C++", "Objective-C": "Swift",
}

// extensions maps a source file's extension to its language. Files that are not code have none.
var extensions = map[string]string{
	"cs": "C#", "razor": "C#", "cshtml": "C#",
	"go": "Go",
	"py": "Python",
	"ts": "TypeScript", "tsx": "TypeScript", "mts": "TypeScript", "cts": "TypeScript",
	"js": "JavaScript", "jsx": "JavaScript", "mjs": "JavaScript", "cjs": "JavaScript", "vue": "JavaScript", "svelte": "JavaScript",
	"tf":    "Terraform",
	"bicep": "Bicep",
	"ps1":   "PowerShell", "psm1": "PowerShell",
	"sh": "Shell", "bash": "Shell",
	"swift": "Swift",
	"rs":    "Rust",
	"c":     "C++", "cc": "C++", "cpp": "C++", "h": "C++", "hpp": "C++",
	"lua":  "Lua",
	"gd":   "GDScript",
	"html": "HTML",
	"css":  "CSS", "scss": "CSS", "sass": "CSS", "less": "CSS",
	"sql":  "SQL",
	"java": "Other", "kt": "Other", "rb": "Other", "php": "Other", "fs": "Other", "vb": "Other", "scala": "Other", "dart": "Other",
}

// LanguageOfFile is the language of one file, or "" for a file that is not code.
func LanguageOfFile(p string) string {
	name := path.Base(p)
	dot := strings.LastIndex(name, ".")
	if dot <= 0 {
		return ""
	}
	return extensions[strings.ToLower(name[dot+1:])]
}

// ListedLanguage is the name on the list for what a forge reported, Other
// when it is not on it, and "" for no language.
func ListedLanguage(name string) string {
	if name == "" {
		return ""
	}
	if n, ok := named[name]; ok {
		name = n
	}
	if slices.Contains(Languages, name) {
		return name
	}
	return "Other"
}

// MainLanguage is the listed language with the largest share. Shares under
// different names for one language add up. A listed language beats Other
// however large Other is; a tie goes to the first by name. "" with no code.
func MainLanguage(shares map[string]float64) string {
	sum := map[string]float64{}
	for name, share := range shares {
		if l := ListedLanguage(name); l != "" {
			sum[l] += share
		}
	}
	type kv struct {
		name  string
		share float64
	}
	var ranked []kv
	for n, s := range sum {
		if n != "Other" {
			ranked = append(ranked, kv{n, s})
		}
	}
	sort.Slice(ranked, func(i, j int) bool {
		if ranked[i].share != ranked[j].share {
			return ranked[i].share > ranked[j].share
		}
		return ranked[i].name < ranked[j].name
	})
	if len(ranked) > 0 {
		return ranked[0].name
	}
	if len(sum) > 0 {
		return "Other"
	}
	return ""
}
