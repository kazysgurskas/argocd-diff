package cmd

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/kazysgurskas/argocd-diff/internal/diff"
	"github.com/kazysgurskas/argocd-diff/internal/helm"
	"github.com/kazysgurskas/argocd-diff/internal/hydrate"
	"github.com/kazysgurskas/argocd-diff/internal/report"
	"github.com/kazysgurskas/argocd-diff/internal/version"
)

type diffOptions struct {
	base, head   string
	applications string
	// noHydrate means base and head are rendered trees, not checkouts.
	noHydrate   bool
	kubeVersion string
	chartsDir   string
	renderedDir string
	report      string
	summary     string
	// baseApplications and headApplications are the deprecated way to name
	// the Application files of rendered trees.
	baseApplications string
	headApplications string
	context          int
	fullTextLimit    int
	meta             report.Meta
}

// addDiffFlags binds the root command's flags in the order help shows them:
// inputs, hydration, outputs, then the report header.
func addDiffFlags(cmd *cobra.Command, o *diffOptions) {
	f := cmd.Flags()
	f.SortFlags = false
	f.StringVar(&o.base, "base", "base", "Base checkout of the GitOps repository: a directory or a .zip of one")
	f.StringVar(&o.head, "head", "head", "Head checkout of the GitOps repository: a directory or a .zip of one")
	f.StringVar(&o.applications, "applications", "applications.yaml",
		"ArgoCD Application file inside each checkout, as \"helm template\" of an app-of-apps chart writes it")
	f.BoolVar(&o.noHydrate, "no-hydrate", false,
		"Base and head are already rendered manifest trees, as \"argocd-diff hydrate\" writes them; diff them as they are")
	f.StringVar(&o.kubeVersion, "kube-version", helm.DefaultKubeVersion, "Kubernetes version to render Helm charts for")
	f.StringVar(&o.chartsDir, "charts-dir", defaultChartsDir, "Directory pulled Helm charts are kept in between runs")
	f.StringVar(&o.renderedDir, "rendered-dir", "",
		"Keep the rendered manifests of both sides here, as base/ and head/, for other tools to scan (default: a temporary directory)")
	f.StringVar(&o.report, "report", "diff.html", "HTML report to write")
	f.StringVar(&o.summary, "summary", "", "Markdown summary to write, for a pull request comment (default: none)")
	f.IntVar(&o.context, "context", diff.DefaultContext, "Unchanged lines the report keeps around each change")
	f.IntVar(&o.fullTextLimit, "full-text-limit", diff.DefaultFullTextLimit,
		"Largest modified file, in lines, whose whole text the report embeds so context can be expanded (-1: never)")
	f.StringVar(&o.meta.Title, "title", "", "Title of the report (default: \"<repo> #<pr-number>\", or the two paths)")
	f.StringVar(&o.meta.Repo, "repo", envOr("GITHUB_REPOSITORY", ""),
		"Repository as owner/name, shown in the report header (default: $GITHUB_REPOSITORY)")
	f.IntVar(&o.meta.PRNumber, "pr-number", prNumberFromEnv(),
		"Pull request number, shown in the report header (default: from $GITHUB_REF or $GITHUB_PR_NUMBER)")
	f.StringVar(&o.meta.PRTitle, "pr-title", "", "Pull request title, shown in the report header")
	f.StringVar(&o.meta.PRURL, "pr-url", "", "Link from the report header to the pull request (default: github.com/<repo>/pull/<pr-number>)")
	f.StringVar(&o.meta.BaseRef, "base-ref", envOr("GITHUB_BASE_REF", ""), "Base branch, shown in the report header (default: $GITHUB_BASE_REF)")
	f.StringVar(&o.meta.HeadRef, "head-ref", envOr("GITHUB_HEAD_REF", ""), "Head branch, shown in the report header (default: $GITHUB_HEAD_REF)")
	f.StringVar(&o.meta.Author, "author", "", "Pull request author, shown in the report header")

	// The 1.0 names. A deprecated flag still works, warns, and is hidden
	// from help.
	f.StringVar(&o.report, "output", "diff.html", "")
	_ = f.MarkDeprecated("output", "use --report")
	f.StringVar(&o.renderedDir, "rendered", "", "")
	_ = f.MarkDeprecated("rendered", "use --rendered-dir")
	f.StringVar(&o.baseApplications, "base-applications", "", "")
	_ = f.MarkDeprecated("base-applications", "put applications.yaml at the root of the base tree")
	f.StringVar(&o.headApplications, "head-applications", "", "")
	_ = f.MarkDeprecated("head-applications", "put applications.yaml at the root of the head tree")
}

func runDiff(cmd *cobra.Command, o *diffOptions) error {
	baseDir, cleanupBase, err := diff.OpenTree(o.base)
	if err != nil {
		return fmt.Errorf("base: %w", err)
	}
	defer cleanupBase()
	headDir, cleanupHead, err := diff.OpenTree(o.head)
	if err != nil {
		return fmt.Errorf("head: %w", err)
	}
	defer cleanupHead()

	var apps diff.Applications
	if o.noHydrate {
		apps, err = applicationsOf(baseDir, o.baseApplications, headDir, o.headApplications)
		if err != nil {
			return err
		}
	} else {
		if o.baseApplications != "" || o.headApplications != "" {
			return errors.New("--base-applications and --head-applications apply only with --no-hydrate; hydrated trees are labelled from the Applications they are rendered from")
		}
		rendered := o.renderedDir
		if rendered == "" {
			if rendered, err = os.MkdirTemp("", "argocd-diff-"); err != nil {
				return err
			}
			defer func() { _ = os.RemoveAll(rendered) }()
		}
		opts := hydrate.Options{ChartsDir: o.chartsDir, KubeVersion: o.kubeVersion}
		var baseApps, headApps diff.Applications
		if baseDir, baseApps, err = hydrateSide(baseDir, filepath.Join(rendered, "base"), o.applications, opts); err != nil {
			return fmt.Errorf("base: %w", err)
		}
		if headDir, headApps, err = hydrateSide(headDir, filepath.Join(rendered, "head"), o.applications, opts); err != nil {
			return fmt.Errorf("head: %w", err)
		}
		apps = baseApps.Merge(headApps)
	}
	result, err := diff.Compare(baseDir, headDir, diff.Options{Context: o.context, FullTextLimit: o.fullTextLimit, Applications: apps})
	if err != nil {
		return err
	}

	o.meta.GeneratedAt = time.Now().UTC()
	o.meta.Tool = "argocd-diff " + version.Version
	if o.meta.Title == "" {
		switch {
		case o.meta.Repo != "" && o.meta.PRNumber > 0:
			o.meta.Title = fmt.Sprintf("%s #%d", o.meta.Repo, o.meta.PRNumber)
		default:
			o.meta.Title = fmt.Sprintf("%s → %s", o.base, o.head)
		}
	}
	if o.meta.PRURL == "" && o.meta.PRNumber > 0 && strings.Count(o.meta.Repo, "/") == 1 {
		o.meta.PRURL = fmt.Sprintf("https://github.com/%s/pull/%d", o.meta.Repo, o.meta.PRNumber)
	}

	out, err := os.Create(o.report)
	if err != nil {
		return fmt.Errorf("create %s: %w", o.report, err)
	}
	defer func() { _ = out.Close() }()
	if err := report.Render(out, result, o.meta); err != nil {
		return fmt.Errorf("write report: %w", err)
	}
	if o.summary != "" {
		if err := writeSummary(o.summary, result); err != nil {
			return err
		}
	}

	_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Wrote %s: %d files changed (%d added, %d modified, %d deleted), +%d -%d\n",
		o.report, len(result.Files), result.Added, result.Modified, result.Deleted, result.Additions, result.Deletions)
	return nil
}

func writeSummary(path string, result *diff.Result) error {
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("create %s: %w", path, err)
	}
	if err := report.WriteMarkdown(f, result, report.MarkdownOptions{}); err != nil {
		_ = f.Close()
		return fmt.Errorf("write summary: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close %s: %w", path, err)
	}
	return nil
}

// hydrateSide renders the Applications in the file at applications, relative
// to the checkout at root, into out, replacing whatever an earlier run left
// there, and returns out with the index of what it rendered.
func hydrateSide(root, out, applications string, opts hydrate.Options) (string, diff.Applications, error) {
	file := filepath.Join(root, applications)
	if _, err := os.Stat(file); errors.Is(err, os.ErrNotExist) {
		return "", nil, fmt.Errorf("%s has no %s; if it is a rendered tree rather than a checkout, pass --no-hydrate", root, applications)
	}
	if err := os.RemoveAll(out); err != nil {
		return "", nil, err
	}
	opts.Root = root
	apps, err := hydrateTree(file, out, opts)
	if err != nil {
		return "", nil, err
	}
	return out, diff.IndexApplications(apps), nil
}

// applicationsOf reads the Application resources of both rendered trees: the
// given files, or applications.yaml at each tree root when it exists. Head
// wins where both trees define the same Application.
func applicationsOf(baseDir, basePath, headDir, headPath string) (diff.Applications, error) {
	apps := diff.Applications{}
	for _, tree := range []struct{ dir, path string }{{baseDir, basePath}, {headDir, headPath}} {
		path := tree.path
		if path == "" {
			path = filepath.Join(tree.dir, "applications.yaml")
		}
		loaded, err := diff.LoadApplications(path)
		if err != nil {
			if tree.path == "" && errors.Is(err, os.ErrNotExist) {
				continue
			}
			return nil, fmt.Errorf("applications: %w", err)
		}
		apps = apps.Merge(loaded)
	}
	return apps, nil
}

func envOr(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}

// prNumberFromEnv is the pull request number GitHub Actions implies:
// GITHUB_PR_NUMBER when a workflow exports it, otherwise the number in a
// GITHUB_REF of the form refs/pull/<n>/merge. Zero when neither applies.
func prNumberFromEnv() int {
	if n, err := strconv.Atoi(os.Getenv("GITHUB_PR_NUMBER")); err == nil && n > 0 {
		return n
	}
	return prNumberFromRef(os.Getenv("GITHUB_REF"))
}

func prNumberFromRef(ref string) int {
	rest, ok := strings.CutPrefix(ref, "refs/pull/")
	if !ok {
		return 0
	}
	num, _, ok := strings.Cut(rest, "/")
	if !ok {
		return 0
	}
	n, err := strconv.Atoi(num)
	if err != nil || n <= 0 {
		return 0
	}
	return n
}
