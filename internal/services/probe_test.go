package services

import (
	"context"
	"testing"
)

func TestPresentFindsFilesAndFolders(t *testing.T) {
	paths := []string{"README.md", "docs/notes/0001-start.md", "docs/notesbook.md", "src/main.go"}
	same(t, Present(paths, []string{"docs/notes", "README.md", "docs/guides", "src/main"}), []string{"docs/notes", "README.md"})
	same(t, Present(paths, []string{"docs/guides"}), []string{})
	same(t, Present(nil, []string{"docs/notes"}), []string{})
}

type partialFiles struct{ memFiles }

func (partialFiles) Partial() bool { return true }

func TestReadProbes(t *testing.T) {
	ctx := context.Background()
	files := memFiles{commit: "a", files: map[string][]byte{"docs/notes/0001.md": []byte("# One")}}
	probe := []string{"docs/notes", "docs/guides"}

	found, _, err := Read(ctx, files, "github/acme/site", "site", nil, Bots{}, probe)
	if err != nil {
		t.Fatal(err)
	}
	same(t, found.PathsPresent, []string{"docs/notes"})
	same(t, found.Probed, probe)

	none, _, _ := Read(ctx, files, "github/acme/site", "site", nil, Bots{}, []string{"docs/guides"})
	if none.PathsPresent == nil || len(none.PathsPresent) != 0 {
		t.Fatalf("probed and none found = %#v, want an empty list", none.PathsPresent)
	}

	off, _, _ := Read(ctx, files, "github/acme/site", "site", nil, Bots{}, nil)
	if off.PathsPresent != nil {
		t.Fatalf("no probe = %#v, want nil", off.PathsPresent)
	}

	empty, _, _ := Read(ctx, nil, "github/acme/blank", "blank", nil, Bots{}, probe)
	if empty.PathsPresent == nil || len(empty.PathsPresent) != 0 {
		t.Fatalf("a repository with no commits holds none of the paths: %#v", empty.PathsPresent)
	}

	cut, _, _ := Read(ctx, partialFiles{files}, "github/acme/site", "site", nil, Bots{}, probe)
	if cut.PathsPresent != nil || len(cut.Problems) != 1 {
		t.Fatalf("a partial tree cannot say a path is absent: %#v %v", cut.PathsPresent, cut.Problems)
	}
}

func TestReadAgainWhenTheProbeChanges(t *testing.T) {
	ctx := context.Background()
	files := memFiles{commit: "a", files: map[string][]byte{"docs/notes/0001.md": []byte("# One")}}
	first, _, _ := Read(ctx, files, "github/acme/site", "site", nil, Bots{}, []string{"docs/notes"})
	if _, out, _ := Read(ctx, files, "github/acme/site", "site", first, Bots{}, []string{"docs/notes"}); !out.Reused {
		t.Fatal("same commit and probe read again")
	}
	again, out, _ := Read(ctx, files, "github/acme/site", "site", first, Bots{}, []string{"docs/guides"})
	if out.Reused {
		t.Fatal("a changed probe reused the old answer")
	}
	same(t, again.PathsPresent, []string{})
}
