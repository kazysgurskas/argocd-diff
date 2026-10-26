// Package cmd defines the argocd-diff command line.
package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/kazysgurskas/argocd-diff/internal/version"
)

// New returns the root command, which renders two checkouts and diffs the
// result. hydrate, which renders one checkout, is its subcommand. Errors
// are returned from Execute rather than printed, so the caller reports them
// once.
func New() *cobra.Command {
	o := &diffOptions{}
	root := &cobra.Command{
		Use:   "argocd-diff",
		Short: "Diff the manifests ArgoCD Applications render to, between two checkouts",
		Long: `Render the ArgoCD Applications of two checkouts of a GitOps repository and
show what changed in the Kubernetes manifests they produce, as a
self-contained HTML report and a Markdown summary for a pull request comment.
No ArgoCD instance or cluster is needed.

By default the checkouts are ./base and ./head, each holding its Application
resources as applications.yaml; source paths in the Applications are resolved
against their checkout. Pass --no-hydrate to diff two trees that
"argocd-diff hydrate" rendered earlier.

Inside GitHub Actions the report header is filled from GITHUB_REPOSITORY,
GITHUB_BASE_REF, GITHUB_HEAD_REF and GITHUB_REF; pass --pr-title and --author
from the event.`,
		Example: `  # The pull request's base is checked out in base/, its head in head/, and
  # the Application resources were rendered into applications.yaml in each
  argocd-diff --summary diff.md --pr-title "$PR_TITLE" --author "$AUTHOR"

  # Explicit paths, keeping the rendered manifests for other tools to scan
  argocd-diff --base base-checkout --head head-checkout --applications manifests/applications.yaml \
      --rendered-dir rendered --report manifest-diff.html --summary manifest-diff.md

  # Two trees rendered earlier with "argocd-diff hydrate"
  argocd-diff --no-hydrate --base base-manifests --head head-manifests`,
		Version:       fmt.Sprintf("%s (commit %s, built %s)", version.Version, version.GitCommit, version.BuildDate),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runDiff(cmd, o)
		},
	}
	addDiffFlags(root, o)
	root.AddCommand(newHydrateCommand())
	return root
}
