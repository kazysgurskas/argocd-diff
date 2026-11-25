package cmd

import (
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const manifests = `apiVersion: v1
kind: Namespace
metadata:
  name: shop
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: web
  namespace: shop
spec:
  replicas: 1
---
apiVersion: v1
kind: Secret
metadata:
  name: web-env
  namespace: shop
stringData:
  token: hunter2
`

// writeFixture writes a directory-source application and returns the
// applications file path.
func writeFixture(t *testing.T, dir string) string {
	t.Helper()
	source := filepath.Join(dir, "src")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "all.yaml"), []byte(manifests), 0o644); err != nil {
		t.Fatal(err)
	}
	applications := filepath.Join(dir, "applications.yaml")
	content := "apiVersion: argoproj.io/v1alpha1\nkind: Application\nmetadata:\n  name: web.stage.c1\nspec:\n  destination:\n    namespace: shop\n  source:\n    repoURL: https://github.com/example/gitops\n    path: " + source + "\n    directory: {}\n"
	if err := os.WriteFile(applications, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return applications
}

// writeChartCheckout writes a checkout at root whose one application, web,
// is a chart that takes replicas from $values/, a file in the checkout, and
// tag from $versions/, a file in the releases repository.
func writeChartCheckout(t *testing.T, root, replicas string) {
	t.Helper()
	writeFile(t, filepath.Join(root, "charts", "web", "Chart.yaml"), "apiVersion: v2\nname: web\nversion: 0.1.0\n")
	writeFile(t, filepath.Join(root, "charts", "web", "templates", "configmap.yaml"),
		"apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: web\n  namespace: {{ .Release.Namespace }}\ndata:\n  replicas: {{ .Values.replicas | quote }}\n  tag: {{ .Values.tag }}\n")
	writeFile(t, filepath.Join(root, "apps", "web", "values.yaml"), "replicas: "+replicas+"\n")
	writeFile(t, filepath.Join(root, "applications.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: web
spec:
  destination:
    namespace: shop
  sources:
    - repoURL: https://github.com/example/gitops
      path: charts/web
      helm:
        valueFiles:
          - $values/apps/web/values.yaml
          - $versions/apps/web/version.yaml
    - repoURL: https://github.com/example/gitops
      ref: values
    - repoURL: https://github.com/example/releases
      ref: versions
`)
}

func TestHydrateWritesManifestTree(t *testing.T) {
	dir := t.TempDir()
	applications := writeFixture(t, dir)
	out := filepath.Join(dir, "out")

	root := New()
	root.SetArgs([]string{"hydrate", "--applications", applications, "--rendered-dir", out})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}

	for path, want := range map[string]string{
		"web.stage.c1/Namespace/shop.yaml":      "---\napiVersion: v1\nkind: Namespace\nmetadata:\n  name: shop\n",
		"web.stage.c1/shop/Deployment/web.yaml": "---\napiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: web\n  namespace: shop\nspec:\n  replicas: 1\n",
		"web.stage.c1/shop/Secret/web-env.yaml": "token: '***REDACTED***'",
	} {
		got, err := os.ReadFile(filepath.Join(out, filepath.FromSlash(path)))
		if err != nil {
			t.Errorf("%s: %v", path, err)
			continue
		}
		if !strings.Contains(string(got), want) {
			t.Errorf("%s:\n%s\nwant to contain:\n%s", path, got, want)
		}
	}
	if strings.Contains(readFile(t, filepath.Join(out, "web.stage.c1", "shop", "Secret", "web-env.yaml")), "hunter2") {
		t.Error("secret value written in clear")
	}
}

// TestHydrateSkipUnrenderableFlag pins that a source ArgoCD would refuse
// fails the command, and that --skip-unrenderable renders the other
// applications instead.
func TestHydrateSkipUnrenderableFlag(t *testing.T) {
	dir := t.TempDir()
	applications := writeFixture(t, dir)
	bad := "---\napiVersion: argoproj.io/v1alpha1\nkind: Application\nmetadata:\n  name: bad.stage.c1\nspec:\n  source:\n    repoURL: https://github.com/example/gitops\n    path: nowhere\n    helm: {}\n    directory: {}\n"
	f, err := os.OpenFile(applications, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(bad); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	root := New()
	root.SetArgs([]string{"hydrate", "--applications", applications, "--rendered-dir", filepath.Join(dir, "strict")})
	if err := root.Execute(); err == nil || !strings.Contains(err.Error(), "multiple source types set: Helm, Directory") {
		t.Errorf("want ArgoCD's refusal, got %v", err)
	}

	out := filepath.Join(dir, "lenient")
	root = New()
	root.SetArgs([]string{"hydrate", "--applications", applications, "--rendered-dir", out, "--skip-unrenderable"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(out, "web.stage.c1", "shop", "Deployment", "web.yaml")); err != nil {
		t.Error("the valid application was not rendered")
	}
}

func TestHydrateRefusesUnsupportedKustomizeBuildOptions(t *testing.T) {
	root := New()
	root.SetArgs([]string{"hydrate", "--applications", writeFixture(t, t.TempDir()), "--rendered-dir", t.TempDir(), "--kustomize-build-options", "--enable-alpha-plugins"})
	if err := root.Execute(); err == nil || !strings.Contains(err.Error(), "--kustomize-build-options") {
		t.Errorf("want a refusal naming the flag, got %v", err)
	}
}

// TestHydrateRefRepoFlag pins that hydrate takes --ref-repo: a $<ref>/
// value file from another repository is read from the directory given, and
// a value that cannot be used fails the command, naming the flag.
func TestHydrateRefRepoFlag(t *testing.T) {
	dir := t.TempDir()
	checkout, releases := filepath.Join(dir, "checkout"), filepath.Join(dir, "releases")
	writeChartCheckout(t, checkout, "1")
	writeFile(t, filepath.Join(releases, "apps", "web", "version.yaml"), "tag: v2\n")
	t.Chdir(checkout)

	root := New()
	root.SetArgs([]string{"hydrate", "--ref-repo", releases})
	if err := root.Execute(); err == nil || !strings.HasPrefix(err.Error(), "--ref-repo: ") {
		t.Errorf("want a refusal naming the flag, got %v", err)
	}

	root = New()
	root.SetArgs([]string{"hydrate", "--ref-repo", "https://github.com/example/releases=" + releases})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	configMap := readFile(t, filepath.Join("rendered", "web", "shop", "ConfigMap", "web.yaml"))
	if !strings.Contains(configMap, "tag: v2") {
		t.Errorf("the value from the ref repo was not applied:\n%s", configMap)
	}
}

// TestOptionsRefuseRepoURLAsRefRepo pins that --repo-url and --ref-repo
// cannot name one repository, under any of its URLs: it is either the
// checkout's or another one.
func TestOptionsRefuseRepoURLAsRefRepo(t *testing.T) {
	const releases = "https://github.com/example/releases"
	refRepo := releases + "=" + t.TempDir()
	tests := []struct {
		name    string
		repoURL string
		wantErr bool
	}{
		{name: "no --repo-url"},
		{name: "--repo-url of another repository", repoURL: "https://github.com/example/gitops"},
		{name: "--repo-url of the ref repo", repoURL: releases, wantErr: true},
		{name: "--repo-url of the ref repo, by its scp-like url", repoURL: "git@github.com:Example/Releases.git", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			flags := renderFlags{repoURL: tt.repoURL, refRepos: []string{refRepo}}
			opts, err := flags.options()
			if tt.wantErr {
				if err == nil || !strings.Contains(err.Error(), "--ref-repo and --repo-url both name "+releases) {
					t.Errorf("options() error = %v, want a refusal naming both flags and the repository", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if opts.RepoURL != tt.repoURL || len(opts.RefRepos) != 1 {
				t.Errorf("options() = %+v, want --repo-url and the one ref repo carried over", opts)
			}
		})
	}
}

// TestParseRefRepos pins the form of a --ref-repo value, <url>=<dir> split
// at the first =, and what is refused.
func TestParseRefRepos(t *testing.T) {
	dir, other := t.TempDir(), t.TempDir()
	file, equals := filepath.Join(dir, "file"), filepath.Join(dir, "a=b")
	writeFile(t, file, "")
	if err := os.Mkdir(equals, 0o755); err != nil {
		t.Fatal(err)
	}
	const releases, shared = "https://github.com/example/releases", "git@github.com:example/shared.git"
	tests := []struct {
		name    string
		values  []string
		want    map[string]string
		wantErr string
	}{
		{name: "no values", want: map[string]string{}},
		{name: "one repository", values: []string{releases + "=" + dir}, want: map[string]string{releases: dir}},
		{name: "two repositories", values: []string{releases + "=" + dir, shared + "=" + other}, want: map[string]string{releases: dir, shared: other}},
		{name: "directory with an equals sign", values: []string{releases + "=" + equals}, want: map[string]string{releases: equals}},
		{name: "no separator", values: []string{dir}, wantErr: "is not of the form <url>=<dir>"},
		{name: "no url", values: []string{"=" + dir}, wantErr: "is not of the form <url>=<dir>"},
		{name: "no directory", values: []string{releases + "="}, wantErr: "is not of the form <url>=<dir>"},
		{name: "directory that does not exist", values: []string{releases + "=" + filepath.Join(dir, "missing")}, wantErr: "checkout of " + releases + ": "},
		{name: "file instead of a directory", values: []string{releases + "=" + file}, wantErr: file + " is not a directory"},
		{name: "repository given twice", values: []string{releases + "=" + dir, "git@github.com:Example/Releases.git=" + other}, wantErr: "name the same repository"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseRefRepos(tt.values)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Errorf("parseRefRepos() error = %v, want it to say %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !maps.Equal(got, tt.want) {
				t.Errorf("parseRefRepos() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestHydrateMissingApplicationsFileIsAnError(t *testing.T) {
	root := New()
	root.SetArgs([]string{"hydrate", "--applications", filepath.Join(t.TempDir(), "missing.yaml"), "--rendered-dir", t.TempDir()})
	err := root.Execute()
	if err == nil || !strings.HasPrefix(err.Error(), "load applications: ") {
		t.Errorf("want a wrapped load error, got %v", err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(content)
}
