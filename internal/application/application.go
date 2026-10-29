// Package application reads ArgoCD Application resources and exposes the
// parts of them that hydration needs.
package application

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// Application is the subset of an ArgoCD Application that hydration reads.
type Application struct {
	APIVersion string `yaml:"apiVersion"`
	Kind       string `yaml:"kind"`
	Metadata   struct {
		Name string `yaml:"name"`
	} `yaml:"metadata"`
	Spec struct {
		Project     string `yaml:"project"`
		Destination struct {
			Name      string `yaml:"name"`
			Namespace string `yaml:"namespace"`
		} `yaml:"destination"`
		Source  *Source   `yaml:"source,omitempty"`
		Sources []*Source `yaml:"sources,omitempty"`
	} `yaml:"spec"`
}

// Source is one source of an Application: a Helm chart, a directory of
// manifests, or a repository that only lends its files to other sources.
type Source struct {
	RepoURL        string           `yaml:"repoURL"`
	Chart          string           `yaml:"chart,omitempty"`
	TargetRevision string           `yaml:"targetRevision"`
	Path           string           `yaml:"path,omitempty"`
	Ref            string           `yaml:"ref,omitempty"`
	Helm           HelmSource       `yaml:"helm,omitempty"`
	Directory      *DirectorySource `yaml:"directory,omitempty"`
}

// HelmSource holds the Helm settings of a source.
type HelmSource struct {
	ReleaseName string   `yaml:"releaseName,omitempty"`
	ValueFiles  []string `yaml:"valueFiles,omitempty"`
	Values      string   `yaml:"values,omitempty"`
}

// DirectorySource holds the directory settings of a source.
type DirectorySource struct {
	Recurse bool `yaml:"recurse,omitempty"`
}

// Load reads every ArgoCD Application in the YAML stream at path. Other
// documents in the stream are ignored.
func Load(path string) ([]Application, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()

	var applications []Application
	decoder := yaml.NewDecoder(f)
	for {
		var app Application
		err := decoder.Decode(&app)
		if errors.Is(err, io.EOF) {
			return applications, nil
		}
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", path, err)
		}
		if app.Kind == "Application" && strings.HasPrefix(app.APIVersion, "argoproj.io/") {
			applications = append(applications, app)
		}
	}
}

// Namespace is the destination namespace, which defaults to the
// application's name when the Application sets none.
func (app *Application) Namespace() string {
	if app.Spec.Destination.Namespace != "" {
		return app.Spec.Destination.Namespace
	}
	return app.Metadata.Name
}

// Sources returns the application's sources, whether it declares one or
// many.
func (app *Application) Sources() []*Source {
	if app.Spec.Source != nil {
		return []*Source{app.Spec.Source}
	}
	return app.Spec.Sources
}

type SourceKind string

const (
	SourceKindUnknown   SourceKind = "unknown"
	SourceKindChart     SourceKind = "chart"
	SourceKindDirectory SourceKind = "directory"
)

// Kind determines the type of the source
func (s *Source) Kind() (SourceKind, error) {
	var kinds []SourceKind

	if s.Chart != "" {
		kinds = append(kinds, SourceKindChart)
	}
	if s.Directory != nil {
		kinds = append(kinds, SourceKindDirectory)
	}

	switch len(kinds) {
	case 0:
		return SourceKindUnknown, nil
	case 1:
		return kinds[0], nil
	default:
		return SourceKindUnknown, fmt.Errorf("multiple kinds detected for source %s: %v", s.RepoURL, kinds)
	}
}

// ReleaseName is the Helm release name, which defaults to fallback when the
// source sets none.
func (s *Source) ReleaseName(fallback string) string {
	if s.Helm.ReleaseName != "" {
		return s.Helm.ReleaseName
	}
	return fallback
}
