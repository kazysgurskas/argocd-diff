package hydrate

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/kazysgurskas/argocd-diff/internal/application"
	"github.com/kazysgurskas/argocd-diff/internal/helm"
)

// renderChart templates the source's Helm chart as release in namespace. A
// chart named by chart is pulled from its Helm repository; one at path is
// taken from the checkout at opts.Root and, when a dependency is missing,
// built in a scratch copy, so that the checkout is not written to.
func renderChart(source *application.Source, release, namespace string, opts Options) (string, error) {
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
	stream, err := helm.Render(path, release, namespace, opts.KubeVersion, valueFilePaths(source.ValueFiles(), chartDir, opts.Root))
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(stream), nil
}

// valueFilePaths resolves valueFiles entries as ArgoCD does: under $values/
// they are relative to the values source's repository, the checkout at
// root; otherwise they are relative to chartDir, the chart's directory in
// the checkout or, for a chart pulled from a Helm repository, the chart
// itself.
func valueFilePaths(files []string, chartDir, root string) []string {
	paths := make([]string, 0, len(files))
	for _, file := range files {
		if rel, ok := strings.CutPrefix(file, "$values/"); ok {
			paths = append(paths, filepath.Join(root, rel))
			continue
		}
		paths = append(paths, filepath.Join(chartDir, file))
	}
	return paths
}

// isChartDirectory reports whether dir holds a Helm chart, which ArgoCD
// decides by the presence of Chart.yaml.
func isChartDirectory(dir string) bool {
	info, err := os.Stat(filepath.Join(dir, "Chart.yaml"))
	return err == nil && !info.IsDir()
}
