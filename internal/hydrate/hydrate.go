// Package hydrate renders ArgoCD Applications into the Kubernetes manifests
// they would deploy. Helm charts are pulled from their repository, or read
// from the checkout, and templated. Directories of manifests are read as
// they are. Kustomizations are built. Plugin sources are skipped with a
// warning. Secret values are redacted.
package hydrate

import (
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"path/filepath"
	"slices"

	"github.com/kazysgurskas/argocd-diff/internal/application"
	"github.com/kazysgurskas/argocd-diff/internal/kustomize"
)

// Options tune a render.
type Options struct {
	// Root is the checkout of the GitOps repository. Source paths are
	// relative to it, and so are value files, except the $<ref>/ ones read
	// from RefRepos. Empty means the working directory.
	Root string
	// RepoURL is the repository the checkout is of. It tells which $<ref>/
	// value files and which paths are in the checkout, and which belong to
	// another repository. Empty means the repository named by the
	// Application's ref sources that are not from RefRepos.
	RepoURL string
	// RefRepos maps the URL of another repository to a directory holding a
	// checkout of it. The $<ref>/ value files that a ref source from that
	// repository lends are read from the directory. RepoURL must not be
	// among the keys, and no two keys may name the same repository, as
	// application.SameRepoURL compares them.
	RefRepos map[string]string
	// ChartsDir is where pulled charts are kept between runs.
	ChartsDir string
	// KubeVersion is the Kubernetes version charts are rendered for.
	KubeVersion string
	// Kustomize carries the `kustomize build` flags for Kustomize sources.
	Kustomize kustomize.BuildOptions
	// SkipUnrenderable logs and skips a source instead of failing the render
	// when ArgoCD would refuse the source, when its $<ref>/ value files
	// cannot be placed, or when nothing says whether its path is in the
	// checkout. A failure to render a source still fails.
	SkipUnrenderable bool
}

// errUnrenderable marks an error in how a source is defined, as opposed to
// a failure to render it. Options.SkipUnrenderable skips the former.
var errUnrenderable = errors.New("cannot be rendered")

// checkoutRepo is what is known about the repository the checkout at
// Options.Root is of.
type checkoutRepo struct {
	// url is the repository, or empty when nothing says which it is: the
	// Application has no ref source outside Options.RefRepos. Every path
	// source is then read from the checkout.
	url string
	// ambiguous means the Application's ref sources outside
	// Options.RefRepos name different repositories and none of them is
	// values, so the checkout could be any of them. Options.RepoURL settles
	// it.
	ambiguous bool
}

// Render renders every source of app and returns the manifests in source
// order.
//
// A source that only lends its files under a ref renders nothing. A chart
// named by chart is pulled from its Helm repository. Every other source is
// a path in the checkout at opts.Root. The checkout belongs to one
// repository: opts.RepoURL when set, otherwise the repository the
// Application's ref sources name. A ref source from a repository in
// opts.RefRepos is not counted there, since that repository is not the
// checkout's. A path in another repository is not in the checkout, so that
// source is skipped with a warning. When the counted ref sources name
// different repositories and none of them is values, nothing says which
// one the checkout is, so a path source cannot be rendered until
// opts.RepoURL says.
//
// The kind of a path source follows ArgoCD. A helm block or a Chart.yaml
// makes it a Helm chart. A kustomize block or a kustomization file makes it
// a kustomization. A directory block, or nothing special, makes it a
// directory of manifests. A plugin block makes it a plugin source, which
// nothing renders here, so it is skipped with a warning.
//
// A source ArgoCD would refuse, or whose $<ref>/ value files cannot be
// placed, fails the render. With opts.SkipUnrenderable it is logged and
// skipped instead. A failure to render a source always fails.
func Render(app application.Application, opts Options) ([]Manifest, error) {
	name := app.Metadata.Name
	refs, err := app.RefSources()
	if err != nil {
		if !opts.SkipUnrenderable {
			return nil, err
		}
		slog.Warn("skipping application that cannot be rendered", "application", name, "error", err)
		return nil, nil
	}
	checkout := checkoutRepo{url: opts.RepoURL}
	if checkout.url == "" {
		url, err := app.CheckoutRepoURL(slices.Collect(maps.Keys(opts.RefRepos)))
		checkout = checkoutRepo{url: url, ambiguous: errors.Is(err, application.ErrAmbiguousCheckout)}
	}
	var manifests []Manifest
	for _, source := range app.Sources() {
		stream, err := renderSource(source, app, refs, checkout, opts)
		if err != nil {
			if opts.SkipUnrenderable && errors.Is(err, errUnrenderable) {
				slog.Warn("skipping source that cannot be rendered", "application", name, "error", err)
				continue
			}
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

// renderSource renders one source of app. It returns an empty stream for a
// source that renders nothing: a ref-only source, a source from another
// repository or a plugin source. The last two are logged as warnings. An
// error that wraps errUnrenderable means ArgoCD would refuse the source as
// it is defined, that its $<ref>/ value files cannot be placed, or that
// nothing says whether its path is in the checkout. Any other error is a
// failure to render.
func renderSource(source *application.Source, app application.Application, refs map[string]*application.Source, checkout checkoutRepo, opts Options) (string, error) {
	name := app.Metadata.Name
	if err := source.Validate(); err != nil {
		return "", fmt.Errorf("source %s path %q %w: %w", source.RepoURL, source.Path, errUnrenderable, err)
	}
	if source.IsRefOnly() {
		return "", nil
	}
	if !source.IsChart() {
		switch {
		case checkout.ambiguous:
			return "", fmt.Errorf("source %s path %q %w: %w; pass --repo-url", source.RepoURL, source.Path, errUnrenderable, application.ErrAmbiguousCheckout)
		case checkout.url != "" && !application.SameRepoURL(source.RepoURL, checkout.url):
			slog.Warn("skipping source from another repository", "application", name, "repo", source.RepoURL, "path", source.Path)
			return "", nil
		}
	}
	switch kind := sourceKind(source, opts.Root); kind {
	case KindHelm:
		return renderChart(source, refs, checkout, source.ReleaseName(name), app.Namespace(), opts)
	case KindDirectory:
		return renderDirectory(source, opts)
	case KindKustomize:
		build := opts.Kustomize
		if build.KubeVersion == "" {
			build.KubeVersion = opts.KubeVersion
		}
		stream, err := kustomize.Render(filepath.Join(opts.Root, source.Path), source.Kustomize, build)
		if errors.Is(err, kustomize.ErrConflict) {
			return "", fmt.Errorf("source %s path %q %w: %w", source.RepoURL, source.Path, errUnrenderable, err)
		}
		return stream, err
	default:
		slog.Warn("skipping source of a kind not rendered", "application", name, "kind", kind.String(), "repo", source.RepoURL, "path", source.Path)
		return "", nil
	}
}
