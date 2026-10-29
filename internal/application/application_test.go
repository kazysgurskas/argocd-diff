package application

import (
	"os"
	"path/filepath"
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
	if len(apps[1].Spec.Sources) != 2 {
		t.Errorf("docs has %d sources, want 2", len(apps[1].Spec.Sources))
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

func TestValuesRepoURL(t *testing.T) {
	var app Application
	if got := app.ValuesRepoURL(); got != "" {
		t.Errorf("ValuesRepoURL() = %q for an application without sources", got)
	}
	app.Spec.Sources = []*Source{
		{RepoURL: "https://charts.example.com", Chart: "web"},
		{RepoURL: "https://github.com/example/gitops", Ref: "values"},
	}
	if got := app.ValuesRepoURL(); got != "https://github.com/example/gitops" {
		t.Errorf("ValuesRepoURL() = %q, want the values source's repository", got)
	}
	app.Spec.Source = &Source{RepoURL: "https://github.com/example/gitops", Path: "apps/web", Directory: &DirectorySource{}}
	if got := app.ValuesRepoURL(); got != "" {
		t.Errorf("ValuesRepoURL() = %q for a single directory source", got)
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
		name                        string
		source                      Source
		chart, directory, valuesRef bool
	}{
		{"chart", Source{Chart: "web"}, true, false, false},
		{"directory", Source{Path: "apps/web", Directory: &DirectorySource{}}, false, true, false},
		{"values ref", Source{Ref: "values"}, false, false, true},
		{"bare path", Source{Path: "apps/web"}, false, false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.source.IsChart(); got != tt.chart {
				t.Errorf("IsChart() = %v", got)
			}
			if got := tt.source.IsDirectory(); got != tt.directory {
				t.Errorf("IsDirectory() = %v", got)
			}
			if got := tt.source.IsValuesRef(); got != tt.valuesRef {
				t.Errorf("IsValuesRef() = %v", got)
			}
		})
	}
}

func TestReleaseNameFallsBackToApplication(t *testing.T) {
	s := Source{Chart: "web"}
	if got := s.ReleaseName("app"); got != "app" {
		t.Errorf("ReleaseName() = %q, want app", got)
	}
	s.Helm.ReleaseName = "storefront"
	if got := s.ReleaseName("app"); got != "storefront" {
		t.Errorf("ReleaseName() = %q, want storefront", got)
	}
}
