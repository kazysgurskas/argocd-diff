// Package hydrate renders ArgoCD Applications into the Kubernetes manifests
// they would deploy: Helm chart sources are pulled and templated, directory
// sources are read as they are, and Secret values are redacted.
package hydrate

import (
	"fmt"
	"log/slog"

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
// renders nothing. Directory sources are read from the checkout at
// opts.Root, which holds the values source's repository: a directory source
// from any other repository is not there, so it is skipped with a warning.
// An Application without a values source has every directory source read
// from the checkout.
func Render(app application.Application, opts Options) ([]Manifest, error) {
	namespace := app.Namespace()
	valuesRepoURL := app.ValuesRepoURL()
	var manifests []Manifest
	for _, source := range app.Sources() {
		var stream string
		var err error
		switch {
		case source.IsValuesRef():
			continue
		case source.IsChart():
			stream, err = renderChart(source, source.ReleaseName(app.Metadata.Name), namespace, opts)
		case source.IsDirectory():
			if valuesRepoURL != "" && !application.SameRepoURL(source.RepoURL, valuesRepoURL) {
				slog.Warn("skipping directory source from another repository", "application", app.Metadata.Name, "repo", source.RepoURL, "path", source.Path)
				continue
			}
			stream, err = renderDirectory(source, opts)
		default:
			return nil, fmt.Errorf("source %s sets neither chart nor directory", source.RepoURL)
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
