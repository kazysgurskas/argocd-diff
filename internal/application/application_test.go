package application

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const stream = `# Rendered by the root chart.
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: not-an-application
---
apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: web
spec:
  destination:
    namespace: shop
  source:
    repoURL: https://charts.example.com
    chart: web
    targetRevision: 1.2.3
    helm:
      releaseName: storefront
      valueFiles:
        - $values/apps/web/values.yaml
---
apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: docs
  annotations:
    note: "dashes --- inside a string are not a document separator"
spec:
  sources:
    - repoURL: https://github.com/example/gitops
      path: apps/docs
      directory:
        recurse: true
    - repoURL: https://github.com/example/gitops
      ref: values
    - repoURL: https://github.com/example/gitops
      path: apps/docs-kustomize
      kustomize:
        namePrefix: docs-
`

func TestLoadKeepsOnlyArgoApplications(t *testing.T) {
	path := filepath.Join(t.TempDir(), "applications.yaml")
	if err := os.WriteFile(path, []byte(stream), 0o644); err != nil {
		t.Fatal(err)
	}
	apps, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(apps) != 2 {
		t.Fatalf("loaded %d applications, want 2", len(apps))
	}
	if apps[0].Metadata.Name != "web" || apps[1].Metadata.Name != "docs" {
		t.Errorf("names = %s, %s", apps[0].Metadata.Name, apps[1].Metadata.Name)
	}
	if apps[0].Spec.Source == nil || !apps[0].Spec.Source.IsChart() {
		t.Error("web should have a single chart source")
	}
	if !apps[0].Spec.Source.IsHelm() || apps[0].Spec.Source.Helm.ReleaseName != "storefront" {
		t.Errorf("web's helm block = %+v, want releaseName storefront", apps[0].Spec.Source.Helm)
	}
	if len(apps[1].Spec.Sources) != 3 {
		t.Fatalf("docs has %d sources, want 3", len(apps[1].Spec.Sources))
	}
	if k := apps[1].Spec.Sources[2].Kustomize; k == nil || k.NamePrefix != "docs-" {
		t.Errorf("kustomize block = %+v, want namePrefix docs-", k)
	}
}

func TestLoadMissingFile(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "missing.yaml")); err == nil {
		t.Error("expected an error for a missing file")
	}
}

func TestNamespaceDefaultsToName(t *testing.T) {
	var app Application
	app.Metadata.Name = "web"
	if got := app.Namespace(); got != "web" {
		t.Errorf("Namespace() = %q, want the application name", got)
	}
	app.Spec.Destination.Namespace = "shop"
	if got := app.Namespace(); got != "shop" {
		t.Errorf("Namespace() = %q, want shop", got)
	}
}

func TestSourcesSingleOrMany(t *testing.T) {
	single, many := &Source{Chart: "a"}, []*Source{{Chart: "b"}, {Ref: "values"}}
	var app Application
	app.Spec.Source = single
	app.Spec.Sources = many
	if got := app.Sources(); len(got) != 1 || got[0] != single {
		t.Errorf("a single source wins over sources: %v", got)
	}
	app.Spec.Source = nil
	if got := app.Sources(); len(got) != 2 || got[1] != many[1] {
		t.Errorf("Sources() = %v, want the sources list", got)
	}
}

// TestRefSources pins that ref sources are keyed $<ref>, as ArgoCD keys
// them, and that a ref set twice is refused.
func TestRefSources(t *testing.T) {
	values := &Source{RepoURL: "https://github.com/example/gitops", Ref: "values"}
	shared := &Source{RepoURL: "https://github.com/example/shared", Ref: "shared", Path: "defaults"}
	var app Application
	app.Spec.Sources = []*Source{{RepoURL: "https://charts.example.com", Chart: "web"}, values, shared}
	refs, err := app.RefSources()
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 2 || refs["$values"] != values || refs["$shared"] != shared {
		t.Errorf("RefSources() = %v", refs)
	}

	app.Spec.Sources = append(app.Spec.Sources, &Source{RepoURL: "https://github.com/example/other", Ref: "values"})
	if _, err := app.RefSources(); err == nil || !strings.Contains(err.Error(), `ref "values"`) {
		t.Errorf("want a duplicate ref error, got %v", err)
	}
}

func TestCheckoutRepoURL(t *testing.T) {
	gitops := "https://github.com/example/gitops"
	shared := "https://github.com/example/shared"
	other := "https://github.com/example/other"
	tests := []struct {
		name      string
		sources   []*Source
		want      string
		ambiguous bool
	}{
		{"no ref source", []*Source{{RepoURL: "https://charts.example.com", Chart: "web"}}, "", false},
		{"one ref source", []*Source{{RepoURL: gitops, Ref: "values"}}, gitops, false},
		{"ref sources on one repository", []*Source{{RepoURL: gitops, Ref: "values"}, {RepoURL: "https://github.com/Example/gitops.git", Ref: "shared"}}, gitops, false},
		{"ref sources on two repositories, values among them", []*Source{{RepoURL: shared, Ref: "shared"}, {RepoURL: gitops, Ref: "values"}}, gitops, false},
		{"ref sources on two repositories, values last", []*Source{{RepoURL: shared, Ref: "shared"}, {RepoURL: other, Ref: "other"}, {RepoURL: gitops, Ref: "values"}}, gitops, false},
		{"ref sources on two repositories, values first", []*Source{{RepoURL: gitops, Ref: "values"}, {RepoURL: shared, Ref: "shared"}, {RepoURL: other, Ref: "other"}}, gitops, false},
		{"ref sources on two repositories, no values", []*Source{{RepoURL: gitops, Ref: "shared"}, {RepoURL: other, Ref: "other"}}, "", true},
		{"ref sources on two repositories, the first repeated, no values", []*Source{{RepoURL: shared, Ref: "shared"}, {RepoURL: other, Ref: "other"}, {RepoURL: shared, Ref: "again"}}, "", true},
		{"ref sources on one repository, scp-like and HTTPS", []*Source{{RepoURL: "git@github.com:example/gitops.git", Ref: "shared"}, {RepoURL: gitops, Ref: "other"}}, "git@github.com:example/gitops.git", false},
		{"ref sources on one repository, ssh URL and HTTPS", []*Source{{RepoURL: "ssh://git@github.com/example/gitops", Ref: "shared"}, {RepoURL: gitops, Ref: "other"}}, "ssh://git@github.com/example/gitops", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var app Application
			app.Spec.Sources = tt.sources
			got, err := app.CheckoutRepoURL()
			if got != tt.want {
				t.Errorf("CheckoutRepoURL() = %q, want %q", got, tt.want)
			}
			if ambiguous := errors.Is(err, ErrAmbiguousCheckout); ambiguous != tt.ambiguous || (err != nil && !ambiguous) {
				t.Errorf("CheckoutRepoURL() error = %v, want ambiguous %t", err, tt.ambiguous)
			}
		})
	}
}

func TestSameRepoURL(t *testing.T) {
	tests := []struct {
		name string
		a, b string
		want bool
	}{
		{"equal", "https://github.com/example/gitops", "https://github.com/example/gitops", true},
		{"case", "https://github.com/Example/GitOps", "https://github.com/example/gitops", true},
		{"git suffix", "https://github.com/example/gitops.git", "https://github.com/example/gitops", true},
		{"trailing slash", "https://github.com/example/gitops/", "https://github.com/example/gitops", true},
		{"space", " https://github.com/example/gitops\n", "https://github.com/example/gitops", true},
		{"other repository", "https://github.com/example/ip-lists", "https://github.com/example/gitops", false},
		{"other host", "https://gitlab.com/example/gitops", "https://github.com/example/gitops", false},
		{"scp-like", "git@github.com:example/gitops.git", "https://github.com/example/gitops", true},
		{"ssh URL", "ssh://git@github.com/example/gitops.git", "https://github.com/example/gitops", true},
		{"ssh URL with port", "ssh://git@github.com:22/example/gitops", "git@github.com:example/gitops", true},
		{"user and token", "https://bot:token@github.com/example/gitops", "https://github.com/example/gitops", true},
		{"http", "http://github.com/example/gitops", "https://github.com/example/gitops", true},
		{"scp-like, other repository", "git@github.com:example/ip-lists.git", "https://github.com/example/gitops", false},
		{"scp-like, other host", "git@gitlab.com:example/gitops.git", "https://github.com/example/gitops", false},
		{"local path", "/srv/git/gitops.git", "/srv/git/gitops/", true},
		{"local path and URL", "/srv/git/gitops", "https://github.com/example/gitops", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := SameRepoURL(tt.a, tt.b); got != tt.want {
				t.Errorf("SameRepoURL(%q, %q) = %v, want %v", tt.a, tt.b, got, tt.want)
			}
		})
	}
}

func TestSourceKinds(t *testing.T) {
	tests := []struct {
		name                                   string
		source                                 Source
		chart, directory, helm, isRef, refOnly bool
	}{
		{"chart", Source{Chart: "web"}, true, false, false, false, false},
		{"chart with values", Source{Chart: "web", Helm: &HelmSource{}}, true, false, true, false, false},
		{"directory", Source{Path: "apps/web", Directory: &DirectorySource{}}, false, true, false, false, false},
		{"helm path", Source{Path: "charts/web", Helm: &HelmSource{}}, false, false, true, false, false},
		{"ref only", Source{Ref: "values"}, false, false, false, true, true},
		{"ref with path", Source{Ref: "values", Path: "apps/web"}, false, false, false, true, false},
		{"bare path", Source{Path: "apps/web"}, false, false, false, false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.source.IsChart(); got != tt.chart {
				t.Errorf("IsChart() = %v", got)
			}
			if got := tt.source.IsDirectory(); got != tt.directory {
				t.Errorf("IsDirectory() = %v", got)
			}
			if got := tt.source.IsHelm(); got != tt.helm {
				t.Errorf("IsHelm() = %v", got)
			}
			if got := tt.source.IsRef(); got != tt.isRef {
				t.Errorf("IsRef() = %v", got)
			}
			if got := tt.source.IsRefOnly(); got != tt.refOnly {
				t.Errorf("IsRefOnly() = %v", got)
			}
		})
	}
}

// TestValidate pins which sources are refused, with ArgoCD's reasons, and
// that every shape ArgoCD accepts passes.
func TestValidate(t *testing.T) {
	valid := map[string]Source{
		"chart":           {Chart: "web"},
		"chart with helm": {Chart: "web", Helm: &HelmSource{}},
		"helm path":       {Path: "charts/web", Helm: &HelmSource{}},
		"directory":       {Path: "apps/web", Directory: &DirectorySource{}},
		"kustomize":       {Path: "apps/web", Kustomize: &KustomizeSource{}},
		"plugin":          {Path: "apps/web", Plugin: &PluginSource{}},
		"bare path":       {Path: "apps/web"},
		"ref only":        {Ref: "values"},
		"ref with path":   {Ref: "shared_values-1", Path: "defaults"},
	}
	for name, s := range valid {
		t.Run(name, func(t *testing.T) {
			if err := s.Validate(); err != nil {
				t.Errorf("Validate() = %v, want nil", err)
			}
		})
	}
	invalid := map[string]struct {
		source Source
		want   string
	}{
		"helm and directory":     {Source{Path: "a", Helm: &HelmSource{}, Directory: &DirectorySource{}}, "multiple source types set: Helm, Directory"},
		"kustomize and plugin":   {Source{Path: "a", Kustomize: &KustomizeSource{}, Plugin: &PluginSource{}}, "multiple source types set: Kustomize, Plugin"},
		"chart with directory":   {Source{Chart: "web", Directory: &DirectorySource{}}, "chart is set on a Directory source"},
		"chart under ref":        {Source{Chart: "web", Ref: "values"}, "lent under ref"},
		"ref with a space":       {Source{Ref: "my values"}, "may only contain"},
		"nothing to render":      {Source{RepoURL: "https://github.com/example/gitops"}, "none of path, chart and ref"},
		"helm block and nothing": {Source{Helm: &HelmSource{}}, "none of path, chart and ref"},
	}
	for name, tt := range invalid {
		t.Run(name, func(t *testing.T) {
			err := tt.source.Validate()
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("Validate() = %v, want an error containing %q", err, tt.want)
			}
		})
	}
}

func TestReleaseNameFallsBackToApplication(t *testing.T) {
	s := Source{Chart: "web"}
	if got := s.ReleaseName("app"); got != "app" {
		t.Errorf("ReleaseName() = %q, want app", got)
	}
	s.Helm = &HelmSource{ReleaseName: "storefront"}
	if got := s.ReleaseName("app"); got != "storefront" {
		t.Errorf("ReleaseName() = %q, want storefront", got)
	}
}
