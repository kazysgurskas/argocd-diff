package hydrate

import (
	"os"
	"path/filepath"

	"github.com/kazysgurskas/argocd-diff/internal/application"
	"github.com/kazysgurskas/argocd-diff/internal/kustomize"
)

// Kind is the type ArgoCD gives a source, which decides how it is
// rendered. Every kind but Plugin is rendered here.
type Kind int

// The kinds, in ArgoCD's names.
const (
	KindHelm Kind = iota + 1
	KindDirectory
	KindKustomize
	KindPlugin
)

// String is the kind's name as ArgoCD prints it.
func (k Kind) String() string {
	switch k {
	case KindHelm:
		return "Helm"
	case KindDirectory:
		return "Directory"
	case KindKustomize:
		return "Kustomize"
	case KindPlugin:
		return "Plugin"
	}
	return "unknown"
}

// sourceKind decides the kind of a valid source the way ArgoCD does. An
// explicit helm, kustomize, directory or plugin block settles it. A source
// that names a chart is Helm. Otherwise the files in the source's directory
// below root decide. A kustomization file makes it Kustomize. A Chart.yaml
// makes it Helm. Anything else makes it a Directory. A directory that holds
// both files is Kustomize, because ArgoCD looks at the kustomization file
// last and lets it win.
func sourceKind(source *application.Source, root string) Kind {
	switch {
	case source.Helm != nil:
		return KindHelm
	case source.Kustomize != nil:
		return KindKustomize
	case source.Directory != nil:
		return KindDirectory
	case source.Plugin != nil:
		return KindPlugin
	case source.Chart != "":
		return KindHelm
	}
	dir := filepath.Join(root, source.Path)
	switch {
	case kustomize.IsKustomization(dir):
		return KindKustomize
	case isChartDirectory(dir):
		return KindHelm
	}
	return KindDirectory
}

// isChartDirectory reports whether dir holds a Chart.yaml, which is how
// ArgoCD detects a Helm chart.
func isChartDirectory(dir string) bool {
	info, err := os.Stat(filepath.Join(dir, "Chart.yaml"))
	return err == nil && !info.IsDir()
}
