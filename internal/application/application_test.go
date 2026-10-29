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
	if apps[0].Spec.Source == nil {
		t.Fatal("web should have a single source")
	}
	expectedKind := SourceKindChart
	if kind, err := apps[0].Spec.Source.Kind(); err != nil || kind != expectedKind {
		t.Errorf("web source kind should be %s but got: kind = %q, error = %v", expectedKind, kind, err)
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

func TestSourceKinds(t *testing.T) {
	// tests that are expected to produce a SourceKind
	tests := []struct {
		name     string
		source   Source
		expected SourceKind
	}{
		{"chart", Source{Chart: "web"}, SourceKindChart},
		{"directory", Source{Path: "apps/web", Directory: &DirectorySource{}}, SourceKindDirectory},
		{"values ref", Source{Ref: "values"}, SourceKindUnknown},
		{"path without directory", Source{Path: "apps/web"}, SourceKindUnknown},
	}

	// tests that are expected to produce an error
	errorTests := []struct {
		name string
		source Source
	}{
		{"multiple kinds", Source{Chart: "web", Path: "apps/web", Directory: &DirectorySource{}}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.source.Kind()
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.expected {
				t.Errorf("kind = %v, but expected %v", got, tt.expected)
			}
		})
	}

	for _, tt := range errorTests {
		t.Run(tt.name, func(t *testing.T) {
			if got, err := tt.source.Kind(); err == nil {
				t.Errorf("kind = %v, but expected an error", got)
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
