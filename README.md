<p align="center">
  <img src="docs/logo.jpg" alt="argocd-diff: an octopus in a diving helmet comparing two manifests" width="260">
</p>

<p align="center">
  <a href="https://github.com/kazysgurskas/argocd-diff/actions/workflows/ci.yaml"><img src="https://github.com/kazysgurskas/argocd-diff/actions/workflows/ci.yaml/badge.svg" alt="CI"></a>
  <a href="https://goreportcard.com/report/github.com/kazysgurskas/argocd-diff"><img src="https://goreportcard.com/badge/github.com/kazysgurskas/argocd-diff" alt="Go Report Card"></a>
  <a href="https://pkg.go.dev/github.com/kazysgurskas/argocd-diff"><img src="https://pkg.go.dev/badge/github.com/kazysgurskas/argocd-diff.svg" alt="Go Reference"></a>
  <a href="https://github.com/kazysgurskas/argocd-diff/releases/latest"><img src="https://img.shields.io/github/v/release/kazysgurskas/argocd-diff" alt="Latest release"></a>
  <img src="https://img.shields.io/github/go-mod/go-version/kazysgurskas/argocd-diff" alt="Go version">
  <a href="LICENSE"><img src="https://img.shields.io/github/license/kazysgurskas/argocd-diff" alt="License"></a>
</p>

# argocd-diff
 
See exactly how a pull request changes the Kubernetes manifests your ArgoCD
Applications produce — no ArgoCD server, no cluster, no kubeconfig.
 
argocd-diff reads your Application resources from a checkout of your GitOps
repo and renders them the same way ArgoCD's repo server would. Helm charts are
pulled and templated with the Helm SDK, directory sources are used as-is, and
Secret values are redacted. The only network calls go to the chart
repositories your Applications point at, and downloaded charts are cached
between runs.
 
Give it the base and head of a pull request, and it renders both, compares
them, and produces:
 
- a self-contained **HTML report** for reviewers
- a **Markdown summary** to post as a PR comment
![The HTML report: changed manifests grouped by application, with filters by status, application and kind, and a unified diff with intra-line highlighting](docs/report.png)
 
### What's supported
 
- Helm charts from HTTP and OCI repositories
- Directories of plain manifests
- Single-source and multi-source Applications
Private chart registries work the same way they do with `helm pull`: log in
first with `helm registry login` or a Docker credential helper.
 
## Install
 
**Prebuilt binary:** download the archive for your platform from the
[releases page](https://github.com/kazysgurskas/argocd-diff/releases), check
it against `checksums.txt`, and put `argocd-diff` on your `PATH`.
 
**With Go:**
 
```bash
go install github.com/kazysgurskas/argocd-diff/cmd/argocd-diff@latest
```
 
**Container image:** every release is published to GitHub Container Registry
as `ghcr.io/kazysgurskas/argocd-diff:<version>-amd64` and `-arm64`.
 
```bash
docker run --rm ghcr.io/kazysgurskas/argocd-diff:1.0.0-amd64 --version
```
 
**GitHub Actions:** use this repo as an action. See
[GitHub Action](#github-action).
 
## Usage
 
1. Check out the PR's base into `base/` and its head into `head/`.
2. In each one, render your ArgoCD Application resources into
   `applications.yaml` (for example, `helm template` on an app-of-apps chart).
3. Run:
```bash
argocd-diff --summary diff.md --pr-title "$PR_TITLE" --author "$AUTHOR"
```
 
This writes the report to `diff.html` and the summary to `diff.md`.
 
A few things worth knowing:
 
- **Paths:** source paths in your Applications (`path:`, `$values/…`) are
  resolved relative to the checkout they come from.
- **Changing inputs and outputs:** use `--base`, `--head` and
  `--applications` for the inputs, and `--report` and `--summary` for the
  outputs.
- **Keeping rendered manifests:** `--rendered-dir` saves both sides (as
  `base/` and `head/`) so other tools like kubeconform can scan them.
  Otherwise they go to a temporary directory.
- **Kubernetes version:** there's no cluster to contact, so charts are rendered
  for `--kube-version`. If a chart's `kubeVersion` constraint rules that
  version out, you'll get a clear error.
- **Chart cache:** charts are downloaded once into `--charts-dir` and reused
  afterwards. Cache this directory in CI.
```console
$ argocd-diff --help
Render the ArgoCD Applications of two checkouts of a GitOps repository and
show what changed in the Kubernetes manifests they produce, as a
self-contained HTML report and a Markdown summary for a pull request comment.
No ArgoCD instance or cluster is needed.
 
By default the checkouts are ./base and ./head, each holding its Application
resources as applications.yaml; source paths in the Applications are resolved
against their checkout. Pass --no-hydrate to diff two trees that
"argocd-diff hydrate" rendered earlier.
 
Inside GitHub Actions the report header is filled from GITHUB_REPOSITORY,
GITHUB_BASE_REF, GITHUB_HEAD_REF and GITHUB_REF; pass --pr-title and --author
from the event.
 
Usage:
  argocd-diff [flags]
  argocd-diff [command]
 
Examples:
  # The pull request's base is checked out in base/, its head in head/, and
  # the Application resources were rendered into applications.yaml in each
  argocd-diff --summary diff.md --pr-title "$PR_TITLE" --author "$AUTHOR"
 
  # Explicit paths, keeping the rendered manifests for other tools to scan
  argocd-diff --base base-checkout --head head-checkout --applications manifests/applications.yaml \
      --rendered-dir rendered --report manifest-diff.html --summary manifest-diff.md
 
  # Two trees rendered earlier with "argocd-diff hydrate"
  argocd-diff --no-hydrate --base base-manifests --head head-manifests
 
Available Commands:
  completion  Generate the autocompletion script for the specified shell
  help        Help about any command
  hydrate     Render ArgoCD applications into Kubernetes manifests
 
Flags:
      --base string           Base checkout of the GitOps repository: a directory or a .zip of one (default "base")
      --head string           Head checkout of the GitOps repository: a directory or a .zip of one (default "head")
      --applications string   ArgoCD Application file inside each checkout, as "helm template" of an app-of-apps chart writes it (default "applications.yaml")
      --no-hydrate            Base and head are already rendered manifest trees, as "argocd-diff hydrate" writes them; diff them as they are
      --kube-version string   Kubernetes version to render Helm charts for (default "1.35.6")
      --charts-dir string     Directory pulled Helm charts are kept in between runs (default "cache")
      --rendered-dir string   Keep the rendered manifests of both sides here, as base/ and head/, for other tools to scan (default: a temporary directory)
      --report string         HTML report to write (default "diff.html")
      --summary string        Markdown summary to write, for a pull request comment (default: none)
      --context int           Unchanged lines the report keeps around each change (default 3)
      --full-text-limit int   Largest modified file, in lines, whose whole text the report embeds so context can be expanded (-1: never) (default 2000)
      --title string          Title of the report (default: "<repo> #<pr-number>", or the two paths)
      --repo string           Repository as owner/name, shown in the report header (default: $GITHUB_REPOSITORY)
      --pr-number int         Pull request number, shown in the report header (default: from $GITHUB_REF or $GITHUB_PR_NUMBER)
      --pr-title string       Pull request title, shown in the report header
      --pr-url string         Link from the report header to the pull request (default: github.com/<repo>/pull/<pr-number>)
      --base-ref string       Base branch, shown in the report header (default: $GITHUB_BASE_REF)
      --head-ref string       Head branch, shown in the report header (default: $GITHUB_HEAD_REF)
      --author string         Pull request author, shown in the report header
  -h, --help                  help for argocd-diff
  -v, --version               version for argocd-diff
 
Use "argocd-diff [command] --help" for more information about a command.
```
 
## The report
 
The report groups changed files by application. You get:
 
- unified or side-by-side view
- expandable context around each change
- filters by status, application, project, cluster and kind
- highlighting of changes within a line
- dark mode and keyboard navigation
Only `.yaml` and `.yml` files are included.
 
### Header
 
The header shows the repo, PR, branches and author. In GitHub Actions most of
this is filled in for you:
 
| Flag          | Comes from                                                    |
| ------------- | ------------------------------------------------------------- |
| `--repo`      | `GITHUB_REPOSITORY`                                           |
| `--base-ref`  | `GITHUB_BASE_REF`                                             |
| `--head-ref`  | `GITHUB_HEAD_REF`                                             |
| `--pr-number` | `GITHUB_REF` on `pull_request` events, or `GITHUB_PR_NUMBER`  |
 
So you usually only need to pass `--pr-title` and `--author`. Use `--title`
to override the generated title and `--pr-url` to override the PR link.
 
### Labels
 
Each file is tagged with:
 
- the Application it belongs to (its `metadata.name`, which is also the first
  folder in the file's path)
- that Application's project (`spec.project`)
- its destination cluster (`spec.destination.name`)
### Context
 
`--context` controls how many unchanged lines appear around each change
(default 3). `--full-text-limit` sets the largest file, in lines, whose full
text is embedded so you can expand context in the browser (default 2000).
 
### Size
 
The diff is embedded as JSON and drawn in the browser, and only what's on
screen gets rendered. Reports with hundreds of changed manifests stay small
and fast. Secrets are already redacted before the diff is made.
 
### Markdown summary
 
`--summary <file>` writes a short Markdown summary for a PR comment:
 
- a totals line, e.g. "11 manifests changed: 4 added, 6 modified, 1 deleted"
- a table with one row per application, project and cluster (collapsed if it
  has more than eight rows)
If the PR doesn't change any rendered output, the summary says so in one
line.
 
## Diffing previously rendered trees
 
With `--no-hydrate`, `--base` and `--head` are treated as trees already
written by `argocd-diff hydrate`, and they're compared as-is:
 
```bash
argocd-diff --no-hydrate --base base-manifests --head head-manifests
```
 
Project and cluster labels are read from an `applications.yaml` at the root
of each tree, if there is one. Without it, files are still grouped by
Application; only the project and cluster labels are left blank.
 
## Rendering only
 
`argocd-diff hydrate` renders a single checkout into a tree of manifests.
Use it for the rendered manifests pattern, for review tools that read plain
manifests, or to diff later with `--no-hydrate`.
 
Run it from inside the checkout, since source paths are relative to the
current directory.
 
```console
$ argocd-diff hydrate --help
Render the ArgoCD Applications of one checkout into the Kubernetes manifests
they would deploy, without an ArgoCD instance. Helm chart sources are pulled
and templated, directory sources are read as they are, and Secret values are
redacted. Source paths in the Applications are relative to the working
directory, so run it from the checkout. Each application is written to
<rendered-dir>/<application>/<namespace>/<Kind>/<name>.yaml, or
<rendered-dir>/<application>/<Kind>/<name>.yaml for a cluster-scoped object.
 
Usage:
  argocd-diff hydrate [flags]
 
Examples:
  argocd-diff hydrate
  argocd-diff hydrate --applications manifests/applications.yaml --rendered-dir rendered --kube-version 1.35.6
 
Flags:
      --applications string   ArgoCD Application file to render (default "applications.yaml")
      --rendered-dir string   Directory to write the rendered manifests to (default "rendered")
      --kube-version string   Kubernetes version to render Helm charts for (default "1.35.6")
      --charts-dir string     Directory pulled Helm charts are kept in between runs (default "cache")
  -h, --help                  help for hydrate
```
 
Progress goes to stderr. On error, it prints the error once and exits with
status 1.
 
## GitHub Action
 
This repo also works as a GitHub Action. It installs the release matching the
tag you reference:
 
```yaml
- uses: actions/checkout@v4
  with:
    ref: ${{ github.event.pull_request.base.sha }}
    path: base
- uses: actions/checkout@v4
  with:
    path: head
- run: |
    for side in base head; do
      helm template "$side" > "$side/applications.yaml"
    done
- uses: kazysgurskas/argocd-diff@v1.1.0
- run: argocd-diff --summary diff.md --pr-title "$PR_TITLE" --author "$AUTHOR"
  env:
    PR_TITLE: ${{ github.event.pull_request.title }}
    AUTHOR: ${{ github.event.pull_request.user.login }}
```
 
If you pin the action to a commit SHA, tell it which release to install:
 
```yaml
- uses: kazysgurskas/argocd-diff@<sha> # v1.1.0
  with:
    version: v1.1.0
```
 
Then publish `diff.html` somewhere reviewers can open it (e.g. a workflow artifact
or object storage) and post `diff.md` as a PR comment.
  
For a complete working example, see the workflow in
[argocd-gitops-example](https://github.com/kazysgurskas/argocd-gitops-example).
 
## Smoke test
 
`make smoke` runs argocd-diff against the example in
[`test/smoke/`](test/smoke/) (two checkouts of a small GitOps repo) and
writes `test/smoke/out/example/diff.html`. To try it with your own
scenarios, see [test/smoke/README.md](test/smoke/README.md).
 
## Docker
 
```bash
docker build -t argocd-diff .
docker run -v $(pwd):/workspace -w /workspace argocd-diff --summary diff.md
```
 
## License
 
Licensed under the [Apache License](LICENSE).
 
## Contributing
 
Contributions are welcome! Feel free to open a pull request.
