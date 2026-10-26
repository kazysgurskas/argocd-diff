#!/usr/bin/env bash
# Hydrates the example's base and head checkouts and diffs them into out/diff.html.
#
#   ./test/smoke/run.sh              # the committed example
#   ./test/smoke/run.sh local/mine   # your own scenario under test/smoke/local/ (gitignored)
#
# A scenario directory holds base/ and head/, each a checkout with an
# applications.yaml whose source paths are relative to it.
set -euo pipefail
cd "$(dirname "$0")"

example=${1:-example}
bin=${ARGOCD_DIFF_BIN:-../../bin/argocd-diff}
if [ ! -x "$bin" ]; then
  echo "building bin/argocd-diff" >&2
  (cd ../.. && make build >/dev/null)
fi

out=out/$(basename "$example")
rm -rf "$out" && mkdir -p "$out"
"$bin" --base "$example/base" --head "$example/head" --applications applications.yaml \
  --charts-dir cache --rendered-dir "$out" --report "$out/diff.html" --summary "$out/diff.md" \
  --repo example/gitops --pr-number 1 --pr-title "Ingress to Gateway API migration" \
  --base-ref main --head-ref feature/gateway-api --author smoke
echo "open $(pwd)/$out/diff.html"
