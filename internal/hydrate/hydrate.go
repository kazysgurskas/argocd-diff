// Package hydrate renders ArgoCD Applications into the Kubernetes manifests
// they would deploy: Helm charts are pulled from their repository or read
// from the checkout and templated, directory sources are read as they are,
// and Secret values are redacted.
package hydrate

import (
	"fmt"
	"log/slog"
	"path/filepath"

	"github.com/kazysgurskas/argocd-diff/internal/application"
)

// Options tune a render.
type Options struct {
	// Root is the directory that source paths and value files are relative
	// to: the checkout of the GitOps repository. Empty means the working
	// directory.
	Root string
	// ChartsDir is where pulled charts are kept between runs.
	ChartsDir string
	// KubeVersion is the Kubernetes version charts are rendered for.
	KubeVersion string
}

// Render renders every source of app and returns the manifests in source
// order. A source that only lends values to the others (ref: values)
// renders nothing, and a chart named by chart is pulled from its Helm
// repository. Every other source is a path in the checkout at opts.Root,
// which holds the values source's repository: a path in any other
// repository is not there, so the source is skipped with a warning. An
// Application without a values source has every path read from the
// checkout. A path is read as a directory of manifests when the source
// sets directory, and templated as a Helm chart when it sets helm or holds
// a Chart.yaml, as ArgoCD decides; a source that sets both is refused, as
// ArgoCD refuses it.
func Render(app application.Application, opts Options) ([]Manifest, error) {
	namespace := app.Namespace()
	valuesRepoURL := app.ValuesRepoURL()
	var manifests []Manifest
	for _, source := range app.Sources() {
		if err := source.Validate(); err != nil {
			return nil, fmt.Errorf("source %s path %q: %w", source.RepoURL, source.Path, err)
		}
		var stream string
		var err error
		switch {
		case source.IsValuesRef():
			continue
		case source.IsChart():
			stream, err = renderChart(source, source.ReleaseName(app.Metadata.Name), namespace, opts)
		case valuesRepoURL != "" && !application.SameRepoURL(source.RepoURL, valuesRepoURL):
			slog.Warn("skipping source from another repository", "application", app.Metadata.Name, "repo", source.RepoURL, "path", source.Path)
			continue
		case source.IsDirectory():
			stream, err = renderDirectory(source, opts)
		case source.Path != "" && (source.IsHelm() || isChartDirectory(filepath.Join(opts.Root, source.Path))):
			stream, err = renderChart(source, source.ReleaseName(app.Metadata.Name), namespace, opts)
		default:
			return nil, fmt.Errorf("source %s path %q has no Chart.yaml and sets neither chart, helm nor directory", source.RepoURL, source.Path)
		}
		if err != nil {
			return nil, err
		}
		parsed, err := parseManifests(stream)
		if err != nil {
			return nil, err
		}
		manifests = append(manifests, parsed...)
	}
	return manifests, nil
}
