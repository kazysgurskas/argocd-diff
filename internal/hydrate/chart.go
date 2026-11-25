package hydrate

import (
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/kazysgurskas/argocd-diff/internal/application"
	"github.com/kazysgurskas/argocd-diff/internal/helm"
)

// renderChart templates the source's Helm chart as release in namespace. A
// chart named by chart is pulled from its Helm repository. A chart at a
// path is taken from the checkout at opts.Root. When one of its
// dependencies is missing, the chart is built in a scratch copy, so that
// nothing is written into the checkout. Value files are resolved with
// refs, the Application's ref sources, and checkout, what is known about
// the checkout's repository.
func renderChart(source *application.Source, refs map[string]*application.Source, checkout checkoutRepo, release, namespace string, opts Options) (string, error) {
	var chartDir, path string
	var err error
	if source.IsChart() {
		path, err = helm.Pull(opts.ChartsDir, source.RepoURL, source.Chart, source.TargetRevision)
		chartDir = path
	} else {
		chartDir = filepath.Join(opts.Root, source.Path)
		var scratch string
		if scratch, err = os.MkdirTemp("", "argocd-diff-chart-"); err != nil {
			return "", err
		}
		defer func() { _ = os.RemoveAll(scratch) }()
		path, err = helm.BuildDependencies(chartDir, scratch)
	}
	if err != nil {
		return "", err
	}
	// Value files resolve against the chart in the checkout, not a scratch
	// copy of it, since they may reach outside the chart.
	files, err := valueFiles(source, chartDir, refs, checkout, opts)
	if err != nil {
		return "", err
	}
	stream, err := helm.Render(path, release, namespace, opts.KubeVersion, files)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(stream), nil
}

// valueFiles resolves the source's valueFiles entries the way ArgoCD does
// and returns the files to hand to Helm, in order.
//
// An entry without a $ prefix is relative to chartDir: the chart's
// directory in the checkout, or the pulled chart itself for a chart from a
// Helm repository. An entry $<ref>/<file> names the source in refs that
// lends its files under that ref, and <file> is relative to the root of
// that source's repository, which refSourceRoot finds.
//
// Every file must exist, unless the source sets ignoreMissingValueFiles,
// which drops a missing file as ArgoCD does.
func valueFiles(source *application.Source, chartDir string, refs map[string]*application.Source, checkout checkoutRepo, opts Options) ([]string, error) {
	ignoreMissing := source.Helm != nil && source.Helm.IgnoreMissingValueFiles
	var paths []string
	for _, file := range source.ValueFiles() {
		path := filepath.Join(chartDir, file)
		if strings.HasPrefix(file, "$") {
			ref, rest, _ := strings.Cut(file, "/")
			refSource, ok := refs[ref]
			switch {
			case !ok:
				return nil, fmt.Errorf("valueFiles entry %s %w: no source has ref %q (refs: %s)",
					file, errUnrenderable, strings.TrimPrefix(ref, "$"), strings.Join(slices.Sorted(maps.Keys(refs)), ", "))
			case rest == "":
				return nil, fmt.Errorf("valueFiles entry %s %w: no file follows the ref", file, errUnrenderable)
			}
			root, err := refSourceRoot(refSource, checkout, opts)
			if err != nil {
				return nil, fmt.Errorf("valueFiles entry %s %w: %w", file, errUnrenderable, err)
			}
			path = filepath.Join(root, rest)
		}
		if _, err := os.Stat(path); err != nil {
			if ignoreMissing && errors.Is(err, fs.ErrNotExist) {
				slog.Info("skipping missing values file", "entry", file, "path", path)
				continue
			}
			return nil, fmt.Errorf("valueFiles entry %s: %w", file, err)
		}
		paths = append(paths, path)
	}
	return paths, nil
}

// refSourceRoot returns the root of the repository whose files source lends
// under its ref: the directory opts.RefRepos gives for that repository,
// otherwise opts.Root when it is the checkout's, otherwise an error.
func refSourceRoot(source *application.Source, checkout checkoutRepo, opts Options) (string, error) {
	for url, dir := range opts.RefRepos {
		if application.SameRepoURL(url, source.RepoURL) {
			return dir, nil
		}
	}
	switch {
	case checkout.ambiguous:
		return "", fmt.Errorf("%w; pass --repo-url", application.ErrAmbiguousCheckout)
	case !application.SameRepoURL(source.RepoURL, checkout.url):
		return "", fmt.Errorf("ref %q names repository %s, which is not the checkout's %s", source.Ref, source.RepoURL, checkout.url)
	}
	return opts.Root, nil
}
