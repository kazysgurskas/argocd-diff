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

// TestRenderSkipsRefOnlySourceAndReadsBarePathAsDirectory pins that a
// source lending its files under ref renders nothing, and that a path with
// neither Chart.yaml nor kustomization is a directory of manifests, as
// ArgoCD detects it, even without a directory block.
func TestRenderSkipsRefOnlySourceAndReadsBarePathAsDirectory(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"dir/b.yaml":      "kind: ConfigMap\nmetadata:\n  name: b\n",
		"apps/web/a.yaml": "kind: ConfigMap\nmetadata:\n  name: a\n",
	})
	var app application.Application
	app.Metadata.Name = "web"
	app.Spec.Sources = []*application.Source{
		{RepoURL: "https://github.com/example/gitops", Ref: "values"},
		{RepoURL: "https://github.com/example/gitops", Path: "dir", Directory: &application.DirectorySource{}},
		{RepoURL: "https://github.com/example/gitops", Path: "apps/web"},
	}
	manifests, err := Render(app, Options{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	if len(manifests) != 2 || manifests[0].Name != "b" || manifests[1].Name != "a" {
		t.Errorf("manifests = %+v, want b then a", manifests)
	}
}

// TestRenderBuildsKustomization pins that a Kustomize source, detected by
// its kustomization file or named by its block, is built, with the block's
// settings applied.
func TestRenderBuildsKustomization(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"apps/k/kustomization.yaml": "resources:\n  - deploy.yaml\n",
		"apps/k/deploy.yaml":        "apiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: k\n",
	})
	var app application.Application
	app.Metadata.Name = "k"
	app.Spec.Source = &application.Source{RepoURL: "https://github.com/example/gitops", Path: "apps/k"}
	manifests, err := Render(app, Options{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	if len(manifests) != 1 || manifests[0].Kind != "Deployment" || manifests[0].Name != "k" {
		t.Errorf("manifests = %+v, want the one Deployment", manifests)
	}

	app.Spec.Source.Kustomize = &application.KustomizeSource{NamePrefix: "stage-"}
	manifests, err = Render(app, Options{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	if len(manifests) != 1 || manifests[0].Name != "stage-k" {
		t.Errorf("manifests = %+v, want the Deployment prefixed by the block", manifests)
	}
}

// TestRenderSkipsPluginWithWarning pins that a plugin source, the one kind
// not rendered, is skipped with a warning rather than refused.
func TestRenderSkipsPluginWithWarning(t *testing.T) {
	logs := captureLogs(t)
	var app application.Application
	app.Metadata.Name = "p"
	app.Spec.Source = &application.Source{RepoURL: "https://github.com/example/gitops", Path: "apps/p", Plugin: &application.PluginSource{}}
	manifests, err := Render(app, Options{Root: t.TempDir()})
	if err != nil || len(manifests) != 0 {
		t.Errorf("manifests = %+v, err = %v; want none and nil", manifests, err)
	}
	for _, want := range []string{"level=WARN", "kind=Plugin", "application=p"} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("log lacks %q:\n%s", want, logs.String())
		}
	}
}

// TestRenderSkipsPathSourcesFromAnotherRepository pins that a path source,
// whether a directory or a chart, from a repository other than the values
// source's is not looked for in the checkout: it is skipped with a warning
// and the other sources still render. The repository is matched ignoring
// case and a .git suffix.
func TestRenderSkipsPathSourcesFromAnotherRepository(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{"apps/web/a.yaml": "kind: ConfigMap\nmetadata:\n  name: a\n"})
	var app application.Application
	app.Metadata.Name = "web"
	app.Spec.Sources = []*application.Source{
		{RepoURL: "https://github.com/example/ip-lists", Path: "kubernetes", Directory: &application.DirectorySource{}},
		{RepoURL: "https://github.com/example/charts", Path: "charts/web"},
		{RepoURL: "https://github.com/Example/gitops.git", Path: "apps/web", Directory: &application.DirectorySource{}},
		{RepoURL: "https://github.com/example/gitops", Ref: "values"},
	}

	logs := captureLogs(t)
	manifests, err := Render(app, Options{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	if len(manifests) != 1 || manifests[0].Name != "a" {
		t.Errorf("manifests = %+v", manifests)
	}
	for _, want := range []string{"level=WARN", "application=web", "repo=https://github.com/example/ip-lists", "path=kubernetes", "repo=https://github.com/example/charts", "path=charts/web"} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("log lacks %q:\n%s", want, logs.String())
		}
	}
}

// TestRenderReadsDirectorySourcesWithoutValuesSource pins that an
// Application with no values source, such as one rendered from a hydrated
// branch, has every directory source read from the checkout, whatever its
// repository.
func TestRenderReadsDirectorySourcesWithoutValuesSource(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{"apps/web/a.yaml": "kind: ConfigMap\nmetadata:\n  name: a\n"})
	var app application.Application
	app.Metadata.Name = "web"
	app.Spec.Source = &application.Source{RepoURL: "https://github.com/example/ip-lists", Path: "apps/web", Directory: &application.DirectorySource{}}
	manifests, err := Render(app, Options{Root: root})
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
