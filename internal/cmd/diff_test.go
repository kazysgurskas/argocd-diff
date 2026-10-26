package cmd

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPRNumberFromRef(t *testing.T) {
	tests := map[string]int{
		"refs/pull/42/merge": 42,
		"refs/pull/7/head":   7,
		"refs/heads/master":  0,
		"refs/pull/x/merge":  0,
		"refs/pull/42":       0,
		"":                   0,
	}
	for ref, want := range tests {
		t.Run(ref, func(t *testing.T) {
			if got := prNumberFromRef(ref); got != want {
				t.Errorf("prNumberFromRef(%q) = %d, want %d", ref, got, want)
			}
		})
	}
}

func TestPRNumberFromEnv(t *testing.T) {
	t.Setenv("GITHUB_PR_NUMBER", "")
	t.Setenv("GITHUB_REF", "refs/pull/314228/merge")
	if got := prNumberFromEnv(); got != 314228 {
		t.Errorf("from GITHUB_REF: %d", got)
	}
	t.Setenv("GITHUB_PR_NUMBER", "9")
	if got := prNumberFromEnv(); got != 9 {
		t.Errorf("GITHUB_PR_NUMBER should win: %d", got)
	}
}

func TestApplicationsOf(t *testing.T) {
	base, head := t.TempDir(), t.TempDir()
	app := func(name, project, cluster string) string {
		return "---\napiVersion: argoproj.io/v1alpha1\nkind: Application\nmetadata:\n  name: " + name +
			"\nspec:\n  project: " + project + "\n  destination:\n    name: " + cluster + "\n"
	}
	if err := os.WriteFile(filepath.Join(base, "applications.yaml"), []byte(app("a", "prod", "c1")+app("gone", "prod", "c1")), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(head, "applications.yaml"), []byte(app("a", "prod", "c2")+app("new", "stage", "c3")), 0o644); err != nil {
		t.Fatal(err)
	}
	apps, err := applicationsOf(base, "", head, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(apps) != 3 || apps["a"].Cluster != "c2" || apps["gone"].Project != "prod" || apps["new"].Cluster != "c3" {
		t.Errorf("apps = %+v", apps)
	}
	// No applications.yaml in a tree is fine; an explicit missing file is not.
	if apps, err := applicationsOf(t.TempDir(), "", t.TempDir(), ""); err != nil || len(apps) != 0 {
		t.Errorf("no files: %v %v", apps, err)
	}
	if _, err := applicationsOf(base, filepath.Join(base, "nope.yaml"), head, ""); err == nil {
		t.Error("explicit missing file should fail")
	}
}

// TestRootDiffsRenderedTrees pins --no-hydrate: two trees rendered earlier
// are diffed as they are, into the report and the summary.
func TestRootDiffsRenderedTrees(t *testing.T) {
	dir := t.TempDir()
	for side, replicas := range map[string]string{"base": "1", "head": "2"} {
		path := filepath.Join(dir, side, "web.stage.c1", "shop", "Deployment", "web.yaml")
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		content := "apiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: web\n  namespace: shop\nspec:\n  replicas: " + replicas + "\n"
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	html, summary := filepath.Join(dir, "diff.html"), filepath.Join(dir, "diff.md")

	root := New()
	root.SetOut(io.Discard)
	root.SetArgs([]string{"--no-hydrate", "--base", filepath.Join(dir, "base"), "--head", filepath.Join(dir, "head"), "--report", html, "--summary", summary})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	page, err := os.ReadFile(html)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(page), `<script type="application/json" id="diff-data">`) {
		t.Error("report lacks the embedded diff data")
	}
	md, err := os.ReadFile(summary)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(md), "**1 manifest changed**") {
		t.Errorf("summary:\n%s", md)
	}
}

// TestRootHintsNoHydrate pins the error for a rendered tree given without
// --no-hydrate: it names the flag.
func TestRootHintsNoHydrate(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "base", "app", "Kind", "x.yaml"), "kind: Kind\n")
	writeFile(t, filepath.Join(dir, "head", "app", "Kind", "x.yaml"), "kind: Kind\n")
	root := New()
	root.SetOut(io.Discard)
	root.SetArgs([]string{"--base", filepath.Join(dir, "base"), "--head", filepath.Join(dir, "head"), "--report", filepath.Join(dir, "diff.html")})
	err := root.Execute()
	if err == nil || !strings.Contains(err.Error(), "--no-hydrate") {
		t.Errorf("want an error that names --no-hydrate, got %v", err)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestRootHydratesAndDiffsCheckouts pins the one-command form: --applications
// names the Application file inside each checkout, whose source paths are
// relative to that checkout, and the rendered trees land under --rendered.
func TestRootHydratesAndDiffsCheckouts(t *testing.T) {
	dir := t.TempDir()
	for side, replicas := range map[string]string{"base": "1", "head": "2"} {
		root := filepath.Join(dir, side)
		writeFile(t, filepath.Join(root, "apps", "web", "deployment.yaml"),
			"apiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: web\n  namespace: shop\nspec:\n  replicas: "+replicas+"\n")
		writeFile(t, filepath.Join(root, "manifests", "applications.yaml"),
			"apiVersion: argoproj.io/v1alpha1\nkind: Application\nmetadata:\n  name: web.stage.c1\nspec:\n  project: stage\n  destination:\n    name: c1\n    namespace: shop\n  source:\n    repoURL: https://github.com/example/gitops\n    path: apps/web\n    directory: {}\n")
	}
	rendered := filepath.Join(dir, "rendered")
	html, summary := filepath.Join(dir, "diff.html"), filepath.Join(dir, "diff.md")
	// Left by an earlier run; must not reach the diff.
	stale := filepath.Join(rendered, "head", "gone.stage.c1", "Kind", "x.yaml")
	writeFile(t, stale, "kind: Kind\nmetadata:\n  name: x\n")

	root := New()
	root.SetOut(io.Discard)
	root.SetArgs([]string{"--base", filepath.Join(dir, "base"), "--head", filepath.Join(dir, "head"),
		"--applications", "manifests/applications.yaml", "--rendered-dir", rendered, "--report", html, "--summary", summary})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Error("a stale file under --rendered survived")
	}
	md, err := os.ReadFile(summary)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"**1 manifest changed**", "| `web.stage.c1` | `stage` | `c1` |"} {
		if !strings.Contains(string(md), want) {
			t.Errorf("missing %q in:\n%s", want, md)
		}
	}
	head, err := os.ReadFile(filepath.Join(rendered, "head", "web.stage.c1", "shop", "Deployment", "web.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(head), "replicas: 2") {
		t.Errorf("rendered head tree:\n%s", head)
	}
	if _, err := os.Stat(html); err != nil {
		t.Error(err)
	}
}

func TestRootRefusesPerTreeApplicationsWhenHydrating(t *testing.T) {
	root := New()
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	root.SetArgs([]string{"--base", t.TempDir(), "--head", t.TempDir(), "--head-applications", "x.yaml"})
	if err := root.Execute(); err == nil || !strings.Contains(err.Error(), "--no-hydrate") {
		t.Errorf("want an error that says the flag needs --no-hydrate, got %v", err)
	}
}

// TestRootDefaultsToBaseAndHead pins the bare invocation: base/ and head/ in
// the working directory, each with applications.yaml.
func TestRootDefaultsToBaseAndHead(t *testing.T) {
	dir := t.TempDir()
	for side, replicas := range map[string]string{"base": "1", "head": "2"} {
		writeFile(t, filepath.Join(dir, side, "apps", "web", "deployment.yaml"),
			"apiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: web\n  namespace: shop\nspec:\n  replicas: "+replicas+"\n")
		writeFile(t, filepath.Join(dir, side, "applications.yaml"),
			"apiVersion: argoproj.io/v1alpha1\nkind: Application\nmetadata:\n  name: web\nspec:\n  destination:\n    namespace: shop\n  source:\n    repoURL: https://github.com/example/gitops\n    path: apps/web\n    directory: {}\n")
	}
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(wd) }()

	root := New()
	root.SetOut(io.Discard)
	root.SetArgs([]string{"--summary", "diff.md"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"diff.html", "diff.md"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Error(err)
		}
	}
}

// TestDeprecatedFlagsStillWork pins that the 1.0 names keep working.
func TestDeprecatedFlagsStillWork(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "base", "app", "Kind", "x.yaml"), "kind: Kind\nmetadata:\n  name: x\n")
	writeFile(t, filepath.Join(dir, "head", "app", "Kind", "x.yaml"), "kind: Kind\nmetadata:\n  name: y\n")
	html := filepath.Join(dir, "old-name.html")
	root := New()
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	root.SetArgs([]string{"--no-hydrate", "--base", filepath.Join(dir, "base"), "--head", filepath.Join(dir, "head"), "--output", html})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(html); err != nil {
		t.Error(err)
	}
}
