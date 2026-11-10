// Package application reads ArgoCD Application resources and exposes the
// parts of them that hydration needs.
package application

import (
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"regexp"
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
// found at path, a directory of manifests, a kustomization, a plugin source,
// or a repository that lends its files to other sources under ref.
type Source struct {
	RepoURL        string           `yaml:"repoURL"`
	Chart          string           `yaml:"chart,omitempty"`
	TargetRevision string           `yaml:"targetRevision"`
	Path           string           `yaml:"path,omitempty"`
	Ref            string           `yaml:"ref,omitempty"`
	Helm           *HelmSource      `yaml:"helm,omitempty"`
	Directory      *DirectorySource `yaml:"directory,omitempty"`
	Kustomize      *KustomizeSource `yaml:"kustomize,omitempty"`
	Plugin         *PluginSource    `yaml:"plugin,omitempty"`
}

// HelmSource holds the Helm settings of a source.
type HelmSource struct {
	ReleaseName             string   `yaml:"releaseName,omitempty"`
	ValueFiles              []string `yaml:"valueFiles,omitempty"`
	IgnoreMissingValueFiles bool     `yaml:"ignoreMissingValueFiles,omitempty"`
	Values                  string   `yaml:"values,omitempty"`
}

// DirectorySource holds the directory settings of a source.
type DirectorySource struct {
	Recurse bool `yaml:"recurse,omitempty"`
}

// KustomizeSource holds the Kustomize settings of a source, which are
// applied to its kustomization before the build, as ArgoCD applies them.
type KustomizeSource struct {
	NamePrefix                string             `yaml:"namePrefix,omitempty"`
	NameSuffix                string             `yaml:"nameSuffix,omitempty"`
	Namespace                 string             `yaml:"namespace,omitempty"`
	Images                    []string           `yaml:"images,omitempty"`
	CommonLabels              map[string]string  `yaml:"commonLabels,omitempty"`
	ForceCommonLabels         bool               `yaml:"forceCommonLabels,omitempty"`
	LabelWithoutSelector      bool               `yaml:"labelWithoutSelector,omitempty"`
	LabelIncludeTemplates     bool               `yaml:"labelIncludeTemplates,omitempty"`
	CommonAnnotations         map[string]string  `yaml:"commonAnnotations,omitempty"`
	ForceCommonAnnotations    bool               `yaml:"forceCommonAnnotations,omitempty"`
	CommonAnnotationsEnvsubst bool               `yaml:"commonAnnotationsEnvsubst,omitempty"`
	Replicas                  []KustomizeReplica `yaml:"replicas,omitempty"`
	Patches                   []KustomizePatch   `yaml:"patches,omitempty"`
	Components                []string           `yaml:"components,omitempty"`
	IgnoreMissingComponents   bool               `yaml:"ignoreMissingComponents,omitempty"`
	Version                   string             `yaml:"version,omitempty"`
	KubeVersion               string             `yaml:"kubeVersion,omitempty"`
	APIVersions               []string           `yaml:"apiVersions,omitempty"`
}

// KustomizeReplica sets the replica count of the workload named.
type KustomizeReplica struct {
	Name  string `yaml:"name"`
	Count int64  `yaml:"count"`
}

// KustomizePatch is a patch, inline or in a file at path relative to the
// source, applied to the resources its target picks.
type KustomizePatch struct {
	Path    string             `yaml:"path,omitempty"`
	Patch   string             `yaml:"patch,omitempty"`
	Target  *KustomizeSelector `yaml:"target,omitempty"`
	Options map[string]bool    `yaml:"options,omitempty"`
}

// KustomizeSelector picks the resources a patch applies to.
type KustomizeSelector struct {
	Group              string `yaml:"group,omitempty"`
	Version            string `yaml:"version,omitempty"`
	Kind               string `yaml:"kind,omitempty"`
	Name               string `yaml:"name,omitempty"`
	Namespace          string `yaml:"namespace,omitempty"`
	LabelSelector      string `yaml:"labelSelector,omitempty"`
	AnnotationSelector string `yaml:"annotationSelector,omitempty"`
}

// PluginSource marks a source as rendered by a config management plugin;
// its settings are not read.
type PluginSource struct{}

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

// RefSources maps $<ref> to the source lending its files under that ref, as
// ArgoCD keys them for valueFiles. Two sources with the same ref are an
// error, as in ArgoCD.
func (app *Application) RefSources() (map[string]*Source, error) {
	refs := map[string]*Source{}
	for _, source := range app.Sources() {
		if source.Ref == "" {
			continue
		}
		key := "$" + source.Ref
		if _, taken := refs[key]; taken {
			return nil, fmt.Errorf("multiple sources have ref %q", source.Ref)
		}
		refs[key] = source
	}
	return refs, nil
}

// ErrAmbiguousCheckout says that the Application's ref sources name
// different repositories and none of them is values, so nothing tells
// which repository the checkout is of.
var ErrAmbiguousCheckout = errors.New("the ref sources name different repositories and none of them is values, so which one the checkout is cannot be told")

// CheckoutRepoURL is the repository the Application is rendered from, as
// far as its ref sources tell. It is the repository they all name. When
// they disagree, it is the one the source with ref values names, since
// that is the convention for the GitOps repository itself. It is empty
// when the Application has no ref source: then nothing says, and nothing
// needs to. When the ref sources disagree and none is values, the error is
// ErrAmbiguousCheckout.
func (app *Application) CheckoutRepoURL() (string, error) {
	url, values, conflict := "", "", false
	for _, source := range app.Sources() {
		if source.Ref == "" {
			continue
		}
		if source.Ref == "values" {
			values = source.RepoURL
		}
		switch {
		case url == "":
			url = source.RepoURL
		case !SameRepoURL(url, source.RepoURL):
			conflict = true
		}
	}
	switch {
	case !conflict:
		return url, nil
	case values != "":
		return values, nil
	}
	return "", ErrAmbiguousCheckout
}

// SameRepoURL reports whether two URLs name the same repository. It
// compares host and path only, so the HTTPS, ssh:// and git@host:path
// forms of one repository match. Case, surrounding space, the user, the
// port, a trailing slash and a .git suffix are ignored.
func SameRepoURL(a, b string) bool {
	return repoURLKey(a) == repoURLKey(b)
}

// repoURLKey reduces a repository URL to host/path. A string that is
// neither a URL nor in the git@host:path form, such as a local path, is
// kept as it is.
func repoURLKey(raw string) string {
	key := strings.ToLower(strings.TrimSpace(raw))
	if strings.Contains(key, "://") {
		if u, err := url.Parse(key); err == nil && u.Host != "" {
			key = u.Hostname() + "/" + strings.TrimPrefix(u.Path, "/")
		}
	} else if host, path, ok := strings.Cut(key, ":"); ok && !strings.Contains(host, "/") {
		// The scp-like form git accepts: [user@]host:path, with no slash
		// before the colon.
		if at := strings.LastIndex(host, "@"); at >= 0 {
			host = host[at+1:]
		}
		key = host + "/" + strings.TrimPrefix(path, "/")
	}
	key = strings.TrimSuffix(key, "/")
	return strings.TrimSuffix(key, ".git")
}

// refName matches the ref names ArgoCD accepts.
var refName = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

// Validate refuses a source ArgoCD refuses: one that sets more than one of
// helm, kustomize, directory and plugin, that names a chart on a source of
// another kind, that lends a chart under ref, that sets a ref ArgoCD would
// not accept, or that sets none of path, chart and ref.
func (s *Source) Validate() error {
	var kinds []string
	if s.Helm != nil {
		kinds = append(kinds, "Helm")
	}
	if s.Kustomize != nil {
		kinds = append(kinds, "Kustomize")
	}
	if s.Directory != nil {
		kinds = append(kinds, "Directory")
	}
	if s.Plugin != nil {
		kinds = append(kinds, "Plugin")
	}
	switch {
	case len(kinds) > 1:
		return fmt.Errorf("multiple source types set: %s", strings.Join(kinds, ", "))
	case s.Chart != "" && len(kinds) == 1 && kinds[0] != "Helm":
		return fmt.Errorf("chart is set on a %s source", kinds[0])
	case s.Chart != "" && s.Ref != "":
		return errors.New("a chart cannot be lent under ref")
	case s.Ref != "" && !refName.MatchString(s.Ref):
		return fmt.Errorf("ref %q may only contain letters, digits, _ and -", s.Ref)
	case s.Path == "" && s.Chart == "" && s.Ref == "":
		return errors.New("none of path, chart and ref is set")
	}
	return nil
}

// IsRef reports whether the source lends its files to other sources, which
// name them $<ref>/<file> in valueFiles.
func (s *Source) IsRef() bool {
	return s.Ref != ""
}

// IsRefOnly reports whether the source only lends its files and renders
// nothing itself, having neither path nor chart.
func (s *Source) IsRefOnly() bool {
	return s.Ref != "" && s.Path == "" && s.Chart == ""
}

// IsChart reports whether the source names a chart in a Helm repository. A
// chart at a path in the Git repository is told from a directory by its
// Chart.yaml, which takes the checkout to see, so it is not reported here.
func (s *Source) IsChart() bool {
	return s.Chart != ""
}

// IsDirectory reports whether the source sets directory.
func (s *Source) IsDirectory() bool {
	return s.Directory != nil
}

// IsHelm reports whether the source sets helm, which makes it a Helm chart
// whatever its path holds, as in ArgoCD.
func (s *Source) IsHelm() bool {
	return s.Helm != nil
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
