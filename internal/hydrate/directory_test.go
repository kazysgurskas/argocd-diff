package hydrate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kazysgurskas/argocd-diff/internal/application"
)

func writeTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for name, content := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestRenderDirectory(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"b.yaml":         "kind: B\n",
		"a.yaml":         "---\nkind: A\n",
		"notes.txt":      "kind: Ignored\n",
		"sub/c.yaml":     "kind: C\n",
		"sub/deep/d.yml": "kind: Ignored\n",
	})
	source := &application.Source{Path: root, Directory: &application.DirectorySource{}}

	flat, err := renderDirectory(source, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if want := "---\nkind: A\n---\nkind: B"; flat != want {
		t.Errorf("without recurse:\n%q\nwant\n%q", flat, want)
	}

	source.Directory.Recurse = true
	deep, err := renderDirectory(source, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if want := "---\nkind: A\n---\nkind: B\n---\nkind: C"; deep != want {
		t.Errorf("with recurse:\n%q\nwant\n%q", deep, want)
	}
	if strings.Contains(deep, "Ignored") {
		t.Error("a non-.yaml file was included")
	}
}

func TestRenderDirectoryRejectsFile(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{"a.yaml": "kind: A\n"})
	source := &application.Source{Path: filepath.Join(root, "a.yaml"), Directory: &application.DirectorySource{}}
	if _, err := renderDirectory(source, Options{}); err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Errorf("want a 'not a directory' error, got %v", err)
	}
	source.Path = filepath.Join(root, "missing")
	if _, err := renderDirectory(source, Options{}); err == nil {
		t.Error("want an error for a missing directory")
	}
}

func TestRenderSkipsUnknownSources(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{"a.yaml": "kind: ConfigMap\nmetadata:\n  name: a\n"})
	var app application.Application
	app.Metadata.Name = "web"
	app.Spec.Sources = []*application.Source{
		{RepoURL: "https://github.com/example/gitops", Ref: "values"},
		{RepoURL: "https://github.com/example/gitops", Path: root, Directory: &application.DirectorySource{}},
		{RepoURL: "https://github.com/example/gitops", Path: "apps/web"},
	}
	manifests, err := Render(app, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(manifests) != 1 || manifests[0].Name != "a" {
		t.Errorf("manifests = %+v", manifests)
	}
}

// TestRenderDirectoryResolvesAgainstRoot pins that source paths are relative
// to Options.Root, the checkout, and not to the working directory.
func TestRenderDirectoryResolvesAgainstRoot(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{"apps/web/a.yaml": "kind: A\n"})
	source := &application.Source{Path: "apps/web", Directory: &application.DirectorySource{}}
	out, err := renderDirectory(source, Options{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	if out != "---\nkind: A" {
		t.Errorf("got %q", out)
	}
	if _, err := renderDirectory(source, Options{}); err == nil {
		t.Error("with no Root the path is relative to the working directory, where apps/web does not exist")
	}
}
