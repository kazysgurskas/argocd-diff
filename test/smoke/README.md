# Smoke test

A place to run `argocd-diff hydrate` and `argocd-diff` end to end without a
cluster or a real GitOps repository.

```console
$ make smoke          # or ./test/smoke/run.sh
open .../test/smoke/out/example/diff.html
```

`example/` is committed: `base/` and `head/` are two checkouts of a GitOps
repository, each with an `applications.yaml` of four applications, and the
script runs the one-command form that hydrates both and diffs them. The
scenario is an Ingress to Gateway API migration in `myapp` (an `Ingress`
deleted, an `HTTPRoute`, `ServiceAccount`, `PodDisruptionBudget` and
`NetworkPolicy` added, a `Deployment`, `Service`, `ConfigMap`, `Secret` and
`HorizontalPodAutoscaler` modified), an image bump in `otherapp`, and in
`chartapp` an image and replica bump through `values-stage.yaml` and a memory
limit bump through `overrides/chartapp.yaml`, a file lent by the `ref: values`
source (a `Deployment` modified), plus a `ServiceAccount` added. `chartapp`
depends on the `common` library chart next to it through `file://`, so each
run builds that dependency in a temporary copy of the chart; the example
itself is left as it is. `kustomizeapp` holds a `kustomization.yaml`, so it is
detected as Kustomize and built with the kustomize library; its image bump is
one more `Deployment` modified.

```console
$ ./test/smoke/run.sh fleet
open .../test/smoke/out/fleet/diff.html
```

`fleet/` is a second committed scenario, made to show the report on a change
that reaches several clusters. One `web` chart is deployed to a stage cluster
and three prod clusters, each with its own values file. The head checkout
bumps the image, adds zone spreading and a `PodDisruptionBudget` in the
chart, and raises the replicas of one prod cluster. So most files repeat a
change another file already shows, which the report folds, and one
`Deployment` and one `HorizontalPodAutoscaler` differ from the rest. Around
it are a `ClusterRole` and a `Namespace`, which are cluster-scoped, an
application that only base has (two files deleted), and one that only head
has: a `PrometheusRule` of more than 400 lines, which the report cuts off
until asked. A `ConfigMap` line is long enough to need wrapping. The
`pr-title` file in a scenario directory sets the title in the report header.

Everything the run produces goes under `out/` (the rendered trees as `base/`
and `head/`, `diff.html` and `diff.md`) and `cache/`, which are gitignored, as
is `local/`: put your own scenarios there, for example two checkouts of a real
repository with their `applications.yaml`, and run `./test/smoke/run.sh local/<name>`.
