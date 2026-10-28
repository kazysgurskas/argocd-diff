# A pull request workflow for a GitOps monorepo

1. works out which applications changed, so only those are rendered;
2. renders the ArgoCD Application resources of the base and the head of the
   pull request with the repository's app-of-apps chart;
3. runs `argocd-diff`, which hydrates both sides and writes the HTML report
   and the Markdown summary;
4. validates and scans the rendered head manifests with kubeconform,
   kube-linter and checkov;
5. uploads the report to object storage and signs a link to it;
6. posts the summary and the link as a pull request comment, updating the
   same comment on later pushes.

The repository it comes from keeps one directory per application under
`apps/` and `infra/`, each with an `app.yaml` that an app-of-apps Helm chart
at the root turns into ArgoCD Application resources. The chart takes the glob
of `app.yaml` files to render as a value, which is how step 2 renders only
the changed applications. Adapt that step to however your repository
produces its Applications; argocd-diff only needs the resulting YAML.
Full reference gitops repo setup can be found at [argocd-gitops-example](https://github.com/kazysgurskas/argocd-gitops-example)

```yaml
name: validate

on:
  pull_request:
    types: [opened, synchronize, reopened, ready_for_review]
    paths:
      - 'apps/**'
      - 'infra/**'

jobs:
  validate:
    if: ${{ !contains(github.event.pull_request.labels.*.name, 'skip-ci') }}
    runs-on: ubuntu-latest

    permissions:
      id-token: write      # authenticate to the cloud with OIDC
      pull-requests: write # post the comment
      contents: read

    env:
      KUBE_VERSION: '1.35.6'

    steps:
      - name: Checkout
        uses: actions/checkout@v4
        with:
          fetch-depth: 0

      # Only the applications the pull request touches are rendered. The
      # changed files are reduced to their application directories.
      - name: Get changed files
        id: changed-files
        uses: tj-actions/changed-files@v47
        with:
          base_sha: ${{ github.event.pull_request.base.sha }}
          dir_names: "true"
          dir_names_max_depth: '2'
          files: |
            apps/**
            infra/**

      - name: Get changed apps
        id: changed-apps
        env:
          ALL_CHANGED_FILES: ${{ steps.changed-files.outputs.all_changed_files }}
        run: |
          set -euo pipefail
          set -f  # no glob expansion while word-splitting $ALL_CHANGED_FILES
          apps=""
          for f in $ALL_CHANGED_FILES; do
            case "$f" in
              apps/*|infra/*) ;;
              *) echo "Refusing unexpected path: $f" >&2; exit 1 ;;
            esac
            case "$f" in
              *[!a-zA-Z0-9._/-]*) echo "Refusing unsafe path: $f" >&2; exit 1 ;;
            esac
            apps="${apps:+$apps,}$f/app.yaml"
          done
          echo "apps=$apps" >> "$GITHUB_OUTPUT"

      # Pulled charts are cached between runs. The key changes whenever any
      # app.yaml changes, which is where chart versions live.
      - name: Generate cache key from all app.yaml files
        id: cache-key
        run: |
          hash=$(find apps infra -name app.yaml -type f -exec sha256sum {} \; | sort | sha256sum | cut -d' ' -f1)
          echo "key=charts-${hash}" >> "$GITHUB_OUTPUT"

      - name: Cache Helm charts
        uses: actions/cache@v4
        with:
          path: charts
          key: ${{ github.repository }}/${{ steps.cache-key.outputs.key }}
          restore-keys: |
            ${{ github.repository }}/charts-

      # Private charts live in a cloud registry. Authenticate with OIDC and
      # register the credential helper, so argocd-diff can pull them the way
      # helm pull would. The same credentials upload the report further down;
      # drop only the credential helper if all your charts are public.
      - name: Set up Cloud SDK
        uses: google-github-actions/setup-gcloud@v3

      - name: Authenticate to Google Cloud
        uses: google-github-actions/auth@v3
        with:
          workload_identity_provider: projects/000000000000/locations/global/workloadIdentityPools/github/providers/example
          project_id: example-project

      - name: Register gcloud as a Docker credential helper
        run: gcloud auth configure-docker REGION-docker.pkg.dev

      - name: Setup Helm
        uses: azure/setup-helm@v5
        with:
          version: '3.17.0'

      - name: Setup argocd-diff
        uses: kazysgurskas/argocd-diff@v1.1.0

      # The base and the head of the pull request, each in its own
      # directory, each with its Application resources rendered into
      # applications.yaml at its root, which is where argocd-diff looks.
      - name: Checkout base
        uses: actions/checkout@v4
        with:
          ref: ${{ github.event.pull_request.base.sha }}
          path: base

      - name: Checkout head
        uses: actions/checkout@v4
        with:
          ref: ${{ github.sha }}
          path: head

      - name: Generate ArgoCD Application manifests
        env:
          APPS: ${{ steps.changed-apps.outputs.apps }}
        run: |
          for side in base head; do
            printf 'appsGlob: "{%s}"\n' "$APPS" | helm template -f - "$side" > "$side/applications.yaml"
          done

      # Hydrates base/ and head/ without an ArgoCD instance and diffs the
      # rendered manifests. The rendered trees are kept under rendered/ for
      # the scanners below.
      - name: Render and diff manifests
        env:
          PR_TITLE: ${{ github.event.pull_request.title }}
          AUTHOR: ${{ github.event.pull_request.user.login }}
        run: |
          argocd-diff \
            --kube-version "$KUBE_VERSION" \
            --charts-dir charts \
            --rendered-dir rendered \
            --report manifest-diff.html \
            --summary manifest-diff.md \
            --pr-title "$PR_TITLE" \
            --author "$AUTHOR"

      # The rendered head is plain YAML, so any manifest scanner can read it.
      - name: Setup kubeconform
        uses: bmuschko/setup-kubeconform@v1
        with:
          kubeconform-version: '0.6.7'

      - name: Validate manifests with kubeconform
        run: |
          kubeconform \
            -skip CustomResourceDefinition \
            -ignore-missing-schemas \
            -schema-location default \
            -schema-location 'https://raw.githubusercontent.com/datreeio/CRDs-catalog/main/{{.Group}}/{{.ResourceKind}}_{{.ResourceAPIVersion}}.json' \
            -summary \
            rendered/head

      - name: Run kube-linter
        uses: stackrox/kube-linter-action@v1
        with:
          config: .github/kubelinter-config.yaml
          directory: rendered/head/

      - name: Run checkov
        uses: bridgecrewio/checkov-action@v12
        with:
          directory: rendered/head/
          config_file: .github/checkov-config.yaml
          output_format: cli
          skip_results_upload: true

      # The report is one self-contained HTML file, so any static host will
      # do. Here it goes to a private bucket and the comment links to it with
      # a signed URL that expires. The OIDC credentials from above cannot
      # sign, so the URL is signed by a service account through the IAM
      # signBlob API: no key to store, and the workflow's identity only needs
      # roles/iam.serviceAccountTokenCreator on that account.
      - name: Upload the report and sign a link to it
        id: upload
        env:
          PR_NUMBER: ${{ github.event.pull_request.number }}
          COMMIT_SHA: ${{ github.event.pull_request.head.sha }}
        run: |
          name="pr-${PR_NUMBER}-${COMMIT_SHA:0:7}.html"
          gcloud storage cp manifest-diff.html "gs://example-manifest-diffs/${name}"
          url=$(gcloud storage sign-url "gs://example-manifest-diffs/${name}" \
            --region=REGION \
            --duration=7d \
            --impersonate-service-account=report-signer@example-project.iam.gserviceaccount.com \
            --format="value(signed_url)")
          echo "url=${url}" >> "$GITHUB_OUTPUT"
          echo "expires=$(date -u -d '+7 days' +%Y-%m-%d)" >> "$GITHUB_OUTPUT"

      # One comment per pull request, found by its marker and replaced on
      # every push. The same text goes to the job summary.
      - name: Compose the comment
        env:
          DIFF_URL: ${{ steps.upload.outputs.url }}
          EXPIRES: ${{ steps.upload.outputs.expires }}
          COMMIT_SHA: ${{ github.event.pull_request.head.sha }}
          COMMIT_URL: ${{ github.server_url }}/${{ github.repository }}/commit/${{ github.event.pull_request.head.sha }}
          RUN_URL: ${{ github.server_url }}/${{ github.repository }}/actions/runs/${{ github.run_id }}
        run: |
          {
            echo '<!-- manifest-diff-comment -->'
            echo '### Rendered manifest diff'
            echo
            cat manifest-diff.md
            echo
            echo "**[View the full diff](${DIFF_URL})**"
            echo
            echo "<sub>Commit [\`${COMMIT_SHA:0:7}\`](${COMMIT_URL}) · link expires ${EXPIRES} · [workflow run](${RUN_URL})</sub>"
          } > comment.md
          cat comment.md >> "$GITHUB_STEP_SUMMARY"

      - name: Find the previous comment
        id: find-comment
        uses: peter-evans/find-comment@v4
        with:
          issue-number: ${{ github.event.pull_request.number }}
          body-includes: '<!-- manifest-diff-comment -->'

      - name: Comment on the pull request
        uses: peter-evans/create-or-update-comment@v5
        with:
          comment-id: ${{ steps.find-comment.outputs.comment-id }}
          issue-number: ${{ github.event.pull_request.number }}
          edit-mode: replace
          body-path: comment.md
```

## Notes

**Pin actions by commit SHA** in a real repository. The tags above are for
readability. The argocd-diff action then needs `with: version: v1.1.0`, since
a SHA says nothing about the release to install.

**Rendering only the changed applications** keeps a run short on a
repository with hundreds of applications, and makes the summary table speak
about what the pull request touched. The price is the "Get changed apps"
step, which also refuses paths with unexpected characters because its output
is spliced into a Helm value. If your repository is small, render everything
and let argocd-diff report no changes for the rest.

**The head checkout is the merge commit.** `github.sha` on a `pull_request`
event is the pull request merged into its base as GitHub would merge it, so
the diff shows what merging would change, not what the branch differs by.

**Private registries** are the only thing argocd-diff needs credentials for.
It pulls charts through the Helm SDK, so whatever works for `helm pull` works
here: a Docker credential helper, as above, or `helm registry login`. Nothing
talks to ArgoCD or to a cluster.

**The scanners read `rendered/head`**, the tree `--rendered-dir` keeps. Without
that flag the trees go to a temporary directory and are gone when argocd-diff
exits.

**Hosting the report.** The HTML is self-contained, so a workflow artifact
works too and needs no cloud account: upload `manifest-diff.html` with
`actions/upload-artifact` and link `steps.<id>.outputs.artifact-url` in the
comment. The bucket and signed URL give reviewers a page that opens in the
browser rather than a zip to download. Signing by impersonation means no
service account key sits in the repository secrets. The workflow in
[argocd-gitops-example](https://github.com/kazysgurskas/argocd-gitops-example)
takes the artifact route.

**The comment marker** `<!-- manifest-diff-comment -->` is how later pushes
find and replace the comment instead of adding one each time.
