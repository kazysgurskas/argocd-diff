package cmd

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/kazysgurskas/argocd-diff/internal/application"
	"github.com/kazysgurskas/argocd-diff/internal/helm"
	"github.com/kazysgurskas/argocd-diff/internal/hydrate"
	"github.com/kazysgurskas/argocd-diff/internal/kustomize"
)

// defaultChartsDir is where pulled charts are kept unless --charts-dir says
// otherwise.
const defaultChartsDir = "cache"

// renderFlags are the flags both commands take to render Applications.
type renderFlags struct {
	kubeVersion           string
	chartsDir             string
	repoURL               string
	kustomizeBuildOptions string
	skipUnrenderable      bool
}

// addRenderFlags binds the rendering flags to cmd.
func addRenderFlags(cmd *cobra.Command, r *renderFlags) {
	f := cmd.Flags()
	f.StringVar(&r.kubeVersion, "kube-version", helm.DefaultKubeVersion, "Kubernetes version to render Helm charts for")
	f.StringVar(&r.chartsDir, "charts-dir", defaultChartsDir, "Directory pulled Helm charts are kept in between runs")
	f.StringVar(&r.repoURL, "repo-url", "",
		"URL of the repository the checkout is of, which tells $<ref>/ value files and paths from other repositories apart (default: the repository the Application's ref sources name)")
	f.StringVar(&r.kustomizeBuildOptions, "kustomize-build-options", "",
		"Flags for kustomize build, as argocd-cm's kustomize.buildOptions: --enable-helm (runs the helm binary), --helm-command <binary>, --load-restrictor LoadRestrictionsNone or LoadRestrictionsRootOnly")
	f.BoolVar(&r.skipUnrenderable, "skip-unrenderable", false,
		"Skip sources ArgoCD would reject, or that can't be placed in the checkout, with a warning instead of failing; plugin sources are always skipped")
}

// options turns the rendering flags into hydrate options.
func (r *renderFlags) options() (hydrate.Options, error) {
	build, err := kustomize.ParseBuildOptions(r.kustomizeBuildOptions)
	if err != nil {
		return hydrate.Options{}, fmt.Errorf("--kustomize-build-options: %w", err)
	}
	return hydrate.Options{
		ChartsDir:        r.chartsDir,
		KubeVersion:      r.kubeVersion,
		RepoURL:          r.repoURL,
		Kustomize:        build,
		SkipUnrenderable: r.skipUnrenderable,
	}, nil
}

type hydrateOptions struct {
	applications string
	renderedDir  string
	renderFlags
}

func newHydrateCommand() *cobra.Command {
	o := &hydrateOptions{}
	cmd := &cobra.Command{
		Use:   "hydrate",
		Short: "Render ArgoCD applications into Kubernetes manifests",
		Long: `Render the ArgoCD Applications of one checkout into the Kubernetes manifests
they would deploy. No ArgoCD instance is needed.

Helm charts are pulled from their repository, or read from the checkout,
and templated. Directories of plain manifests are read as they are.
Kustomizations are built. Plugin sources are skipped with a warning. Secret
values are redacted.

Source paths in the Applications, and $<ref>/ paths in valueFiles, are
relative to the working directory, so run it from the checkout. Each
application is written to <rendered-dir>/<application>/<namespace>/<Kind>/<name>.yaml,
or to <rendered-dir>/<application>/<Kind>/<name>.yaml for a cluster-scoped
object.`,
		Example: `  argocd-diff hydrate
  argocd-diff hydrate --applications manifests/applications.yaml --rendered-dir rendered --kube-version 1.35.6`,
		RunE: func(*cobra.Command, []string) error {
			return runHydrate(o)
		},
	}
	addHydrateFlags(cmd, o)
	return cmd
}

func addHydrateFlags(cmd *cobra.Command, o *hydrateOptions) {
	f := cmd.Flags()
	f.SortFlags = false
	f.StringVar(&o.applications, "applications", "applications.yaml", "ArgoCD Application file to render")
	f.StringVar(&o.renderedDir, "rendered-dir", "rendered", "Directory to write the rendered manifests to")
	addRenderFlags(cmd, &o.renderFlags)

	// The 1.0 name; deprecated, so it still works, warns, and is hidden.
	f.StringVar(&o.renderedDir, "output", "rendered", "")
	_ = f.MarkDeprecated("output", "use --rendered-dir")
}

func runHydrate(o *hydrateOptions) error {
	opts, err := o.options()
	if err != nil {
		return err
	}
	_, err = hydrateTree(o.applications, o.renderedDir, opts)
	return err
}

// hydrateTree renders every Application in the file at applications and
// writes each manifest to out/<application>/<manifest path>. It returns the
// Applications it read.
func hydrateTree(applications, out string, opts hydrate.Options) ([]application.Application, error) {
	apps, err := application.Load(applications)
	if err != nil {
		return nil, fmt.Errorf("load applications: %w", err)
	}
	slog.Info("loaded applications", "path", applications, "count", len(apps))
	if err := os.MkdirAll(out, 0o755); err != nil {
		return nil, err
	}

	for _, app := range apps {
		name := app.Metadata.Name
		slog.Info("hydrating application", "application", name)
		dir := filepath.Join(out, name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, err
		}
		manifests, err := hydrate.Render(app, opts)
		if err != nil {
			return nil, fmt.Errorf("hydrate %s: %w", name, err)
		}
		if len(manifests) == 0 {
			slog.Warn("no manifests rendered", "application", name)
			continue
		}
		for _, m := range manifests {
			path := filepath.Join(dir, m.Path())
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				return nil, err
			}
			if err := os.WriteFile(path, []byte(m.Content), 0o644); err != nil {
				return nil, err
			}
		}
		slog.Info("wrote manifests", "application", name, "count", len(manifests))
	}
	return apps, nil
}
