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

// Source is one source of an Application: a Helm chart, named by chart or
// found at path, a directory of manifests, or a repository that only lends
// its files to other sources.
type Source struct {
	RepoURL        string           `yaml:"repoURL"`
	Chart          string           `yaml:"chart,omitempty"`
	TargetRevision string           `yaml:"targetRevision"`
	Path           string           `yaml:"path,omitempty"`
	Ref            string           `yaml:"ref,omitempty"`
	Helm           *HelmSource      `yaml:"helm,omitempty"`
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

// ValuesRepoURL is the repository of the source that lends its files to the
// others (ref: values), which is the GitOps repository the Application is
// rendered from. It is empty when the Application has no such source.
func (app *Application) ValuesRepoURL() string {
	for _, source := range app.Sources() {
		if source.IsValuesRef() {
			return source.RepoURL
		}
	}
	return ""
}

// SameRepoURL reports whether two URLs name the same repository, ignoring
// case, surrounding space, a trailing slash and a .git suffix.
func SameRepoURL(a, b string) bool {
	return repoURLKey(a) == repoURLKey(b)
}

func repoURLKey(url string) string {
	url = strings.ToLower(strings.TrimSpace(url))
	url = strings.TrimSuffix(url, "/")
	return strings.TrimSuffix(url, ".git")
}

// IsValuesRef reports whether the source only lends its files to other
// sources under $values and renders nothing itself.
func (s *Source) IsValuesRef() bool {
	return s.Ref == "values"
}

// IsChart reports whether the source names a chart in a Helm repository. A
// chart at a path in the Git repository is told from a directory by its
// Chart.yaml, which takes the checkout to see, so it is not reported here.
func (s *Source) IsChart() bool {
	return s.Chart != ""
}

// IsDirectory reports whether the source is a directory of manifests.
func (s *Source) IsDirectory() bool {
	return s.Directory != nil
}

// IsHelm reports whether the source sets helm, which makes it a Helm chart
// whatever its path holds, as in ArgoCD.
func (s *Source) IsHelm() bool {
	return s.Helm != nil
}

// Validate refuses a source that sets both helm and directory, which ArgoCD
// rejects as having two source types.
func (s *Source) Validate() error {
	if s.Helm != nil && s.Directory != nil {
		return errors.New("helm and directory are both set, which ArgoCD allows only one of")
	}
	return nil
}

// ValueFiles is the source's Helm value files, in order; none when the
// source sets no helm.
func (s *Source) ValueFiles() []string {
	if s.Helm == nil {
		return nil
	}
	return s.Helm.ValueFiles
}

// ReleaseName is the Helm release name, which defaults to fallback when the
// source sets none.
func (s *Source) ReleaseName(fallback string) string {
	if s.Helm != nil && s.Helm.ReleaseName != "" {
		return s.Helm.ReleaseName
	}
	return fallback
}
