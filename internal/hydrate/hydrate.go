// Package hydrate renders ArgoCD Applications into the Kubernetes manifests
// they would deploy: Helm chart sources are pulled and templated, directory
// sources are read as they are, and Secret values are redacted.
package hydrate

import (
	"fmt"

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
// renders nothing.
func Render(app application.Application, opts Options) ([]Manifest, error) {
	namespace := app.Namespace()
	var manifests []Manifest
	for _, source := range app.Sources() {
		var stream string
		var err error
		kind, err := source.Kind()
		if err != nil {
			return nil, err
		}
		switch kind {
		case application.SourceKindChart:
			stream, err = renderChart(source, source.ReleaseName(app.Metadata.Name), namespace, opts)
		case application.SourceKindDirectory:
			stream, err = renderDirectory(source, opts)
		default:
			// TODO: print a warning that this source is skipped
			continue
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
