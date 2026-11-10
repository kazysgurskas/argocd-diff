package kustomize

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"sigs.k8s.io/kustomize/api/types"

	"github.com/kazysgurskas/argocd-diff/internal/application"
)

func writeTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for name, content := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// writeExample writes a base with a Deployment and a Service, an overlay
// that prefixes them, and a component that annotates them, and returns the
// root.
func writeExample(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"base/kustomization.yaml": "resources:\n  - deployment.yaml\n  - service.yaml\n",
		"base/deployment.yaml": `apiVersion: apps/v1
kind: Deployment
metadata:
  name: web
spec:
  replicas: 1
  selector:
    matchLabels:
      app: web
  template:
    metadata:
      labels:
        app: web
    spec:
      containers:
        - name: web
          image: ghcr.io/example/web:1.0.0
`,
		"base/service.yaml": `apiVersion: v1
kind: Service
metadata:
  name: web
spec:
  selector:
    app: web
  ports:
    - port: 80
`,
		"overlay/kustomization.yaml":   "resources:\n  - ../base\nnamePrefix: dev-\n",
		"component/kustomization.yaml": "apiVersion: kustomize.config.k8s.io/v1alpha1\nkind: Component\ncommonAnnotations:\n  from: component\n",
	})
	return root
}

func TestRenderBuildsOverlay(t *testing.T) {
	root := writeExample(t)
	overlay := filepath.Join(root, "overlay")
	before, err := os.ReadFile(filepath.Join(overlay, "kustomization.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	out, err := Render(overlay, nil, BuildOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"kind: Deployment", "kind: Service", "name: dev-web"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Count(out, "name: dev-web") != 2 {
		t.Errorf("want both resources prefixed:\n%s", out)
	}
	after, err := os.ReadFile(filepath.Join(overlay, "kustomization.yaml"))
	if err != nil || string(after) != string(before) {
		t.Error("the kustomization on disk changed")
	}
}

// TestRenderAppliesSettings pins that every kustomize setting ArgoCD
// applies with `kustomize edit` shows in the build, and that the
// kustomization on disk is left as it is.
func TestRenderAppliesSettings(t *testing.T) {
	root := writeExample(t)
	base := filepath.Join(root, "base")
	before, err := os.ReadFile(filepath.Join(base, "kustomization.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	settings := &application.KustomizeSource{
		NamePrefix:        "stage-",
		NameSuffix:        "-v2",
		Namespace:         "shop",
		Images:            []string{"ghcr.io/example/web=ghcr.io/example/app:2.0.0"},
		CommonLabels:      map[string]string{"team": "web"},
		CommonAnnotations: map[string]string{"owner": "platform"},
		Replicas:          []application.KustomizeReplica{{Name: "web", Count: 3}},
		Patches: []application.KustomizePatch{{
			Patch:  "apiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: web\nspec:\n  revisionHistoryLimit: 5\n",
			Target: &application.KustomizeSelector{Kind: "Deployment", Name: "web"},
		}},
		Components: []string{"../component"},
		Version:    "v5.0.0",
	}
	out, err := Render(base, settings, BuildOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"name: stage-web-v2", "namespace: shop", "image: ghcr.io/example/app:2.0.0",
		"team: web", "owner: platform", "replicas: 3", "revisionHistoryLimit: 5", "from: component",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	// Labels reach the selectors too: Deployment metadata, selector and
	// template, Service metadata and selector.
	if got := strings.Count(out, "team: web"); got != 5 {
		t.Errorf("team: web appears %d times, want 5 (selectors included):\n%s", got, out)
	}
	after, err := os.ReadFile(filepath.Join(base, "kustomization.yaml"))
	if err != nil || string(after) != string(before) {
		t.Error("the kustomization on disk changed")
	}
}

func TestRenderLabelWithoutSelector(t *testing.T) {
	root := writeExample(t)
	out, err := Render(filepath.Join(root, "base"), &application.KustomizeSource{
		CommonLabels:         map[string]string{"team": "web"},
		LabelWithoutSelector: true,
	}, BuildOptions{})
	if err != nil {
		t.Fatal(err)
	}
	// Only the two metadata blocks, not the selectors or the pod template.
	if got := strings.Count(out, "team: web"); got != 2 {
		t.Errorf("team: web appears %d times, want 2 (metadata only):\n%s", got, out)
	}
}

func TestRenderMissingKustomization(t *testing.T) {
	if _, err := Render(t.TempDir(), nil, BuildOptions{}); err == nil {
		t.Error("expected an error for a directory without a kustomization")
	}
	if _, err := Render(t.TempDir(), &application.KustomizeSource{NamePrefix: "x-"}, BuildOptions{}); err == nil {
		t.Error("expected an error for a directory without a kustomization, settings or not")
	}
}

func TestParseImage(t *testing.T) {
	tests := map[string]types.Image{
		"nginx":                            {Name: "nginx"},
		"nginx:1.25":                       {Name: "nginx", NewTag: "1.25"},
		"nginx@sha256:abc":                 {Name: "nginx", Digest: "sha256:abc"},
		"nginx=ghcr.io/x/nginx":            {Name: "nginx", NewName: "ghcr.io/x/nginx"},
		"nginx=ghcr.io/x/nginx:1.25":       {Name: "nginx", NewName: "ghcr.io/x/nginx", NewTag: "1.25"},
		"nginx=registry:5000/nginx":        {Name: "nginx", NewName: "registry:5000/nginx"},
		"nginx=registry:5000/nginx:1.25":   {Name: "nginx", NewName: "registry:5000/nginx", NewTag: "1.25"},
		"nginx=ghcr.io/x/nginx@sha256:abc": {Name: "nginx", NewName: "ghcr.io/x/nginx", Digest: "sha256:abc"},
		"registry:5000/nginx:1.25":         {Name: "registry:5000/nginx", NewTag: "1.25"},
	}
	for arg, want := range tests {
		t.Run(arg, func(t *testing.T) {
			got, err := parseImage(arg)
			if err != nil {
				t.Fatal(err)
			}
			if got != want {
				t.Errorf("parseImage(%q) = %+v, want %+v", arg, got, want)
			}
		})
	}
	if _, err := parseImage("=nginx"); err == nil {
		t.Error("an override without a name must be refused")
	}
}

// TestRenderRefusesConflictsUnlessForced pins `kustomize edit add` without
// --force: a label or annotation the kustomization already sets is refused,
// and the force setting lets the Application's value win.
func TestRenderRefusesConflictsUnlessForced(t *testing.T) {
	root := writeExample(t)
	base := filepath.Join(root, "base")
	writeTree(t, root, map[string]string{
		"base/kustomization.yaml": "resources:\n  - deployment.yaml\n  - service.yaml\ncommonLabels:\n  team: base\ncommonAnnotations:\n  owner: base\n",
	})
	labels := &application.KustomizeSource{CommonLabels: map[string]string{"team": "web"}}
	if _, err := Render(base, labels, BuildOptions{}); err == nil || !strings.Contains(err.Error(), "forceCommonLabels") {
		t.Errorf("want a label conflict naming forceCommonLabels, got %v", err)
	}
	labels.ForceCommonLabels = true
	out, err := Render(base, labels, BuildOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "team: web") || strings.Contains(out, "team: base") {
		t.Errorf("the forced label did not win:\n%s", out)
	}

	annotations := &application.KustomizeSource{CommonAnnotations: map[string]string{"owner": "web"}}
	if _, err := Render(base, annotations, BuildOptions{}); err == nil || !strings.Contains(err.Error(), "forceCommonAnnotations") {
		t.Errorf("want an annotation conflict naming forceCommonAnnotations, got %v", err)
	}
	annotations.ForceCommonAnnotations = true
	if out, err := Render(base, annotations, BuildOptions{}); err != nil || !strings.Contains(out, "owner: web") || strings.Contains(out, "owner: base") {
		t.Errorf("the forced annotation did not win: %v\n%s", err, out)
	}

	// A label without selectors conflicts only with such an entry.
	without := &application.KustomizeSource{CommonLabels: map[string]string{"team": "web"}, LabelWithoutSelector: true}
	if _, err := Render(base, without, BuildOptions{}); err != nil {
		t.Errorf("a label without selectors must not conflict with commonLabels: %v", err)
	}
}

// TestRenderIgnoresMissingComponentsWhenAsked pins ArgoCD's
// ignoreMissingComponents: a component path that does not exist fails the
// build unless the setting drops it, and the setting keeps a path that
// exists without a kustomization, which fails the build as in ArgoCD.
func TestRenderIgnoresMissingComponentsWhenAsked(t *testing.T) {
	root := writeExample(t)
	base := filepath.Join(root, "base")
	settings := &application.KustomizeSource{Components: []string{"../component", "../missing"}}
	if _, err := Render(base, settings, BuildOptions{}); err == nil {
		t.Error("a missing component must fail the build")
	}
	settings.IgnoreMissingComponents = true
	out, err := Render(base, settings, BuildOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "from: component") {
		t.Errorf("the component that exists was not applied:\n%s", out)
	}

	writeTree(t, root, map[string]string{"empty/README.md": "no kustomization here\n"})
	settings.Components = []string{"../component", "../empty"}
	if _, err := Render(base, settings, BuildOptions{}); err == nil {
		t.Error("a component directory without a kustomization must be kept, and fail the build")
	}
}

// TestRenderConflictIsErrConflict pins that a refused label or annotation
// wraps ErrConflict, so that callers can tell it from a failed build.
func TestRenderConflictIsErrConflict(t *testing.T) {
	root := writeExample(t)
	base := filepath.Join(root, "base")
	writeTree(t, root, map[string]string{
		"base/kustomization.yaml": "resources:\n  - deployment.yaml\n  - service.yaml\ncommonLabels:\n  team: base\n",
	})
	_, err := Render(base, &application.KustomizeSource{CommonLabels: map[string]string{"team": "web"}}, BuildOptions{})
	if !errors.Is(err, ErrConflict) {
		t.Errorf("want ErrConflict, got %v", err)
	}
}

// TestRenderInflatesChartsForKubeVersion pins that with --enable-helm a
// helmCharts entry inflates for the build's Kubernetes version when the
// kustomize block sets none, and for the block's when it does.
func TestRenderInflatesChartsForKubeVersion(t *testing.T) {
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("helm is not installed")
	}
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"app/kustomization.yaml":                  "helmCharts:\n  - name: web\n    releaseName: web\n",
		"app/charts/web/Chart.yaml":               "apiVersion: v2\nname: web\nversion: 0.1.0\n",
		"app/charts/web/values.yaml":              "{}\n",
		"app/charts/web/templates/configmap.yaml": "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: web\ndata:\n  kube: {{ .Capabilities.KubeVersion.Version }}\n",
	})
	app := filepath.Join(root, "app")
	out, err := Render(app, nil, BuildOptions{EnableHelm: true, KubeVersion: "1.30.0"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "kube: v1.30.0") {
		t.Errorf("the build's Kubernetes version was not used:\n%s", out)
	}
	out, err = Render(app, &application.KustomizeSource{KubeVersion: "1.28.0"}, BuildOptions{EnableHelm: true, KubeVersion: "1.30.0"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "kube: v1.28.0") {
		t.Errorf("the kustomize block's Kubernetes version did not win:\n%s", out)
	}
}

// TestRenderLoadRestrictions pins that a file outside the kustomization's
// directory is refused by default and allowed with LoadRestrictionsNone, as
// `--load-restrictor` decides it.
func TestRenderLoadRestrictions(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"app/kustomization.yaml": "resources:\n  - ../shared/configmap.yaml\n",
		"shared/configmap.yaml":  "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: shared\n",
	})
	app := filepath.Join(root, "app")
	if _, err := Render(app, nil, BuildOptions{}); err == nil {
		t.Error("a file outside the kustomization must be refused by default")
	}
	out, err := Render(app, nil, BuildOptions{LoadRestrictions: types.LoadRestrictionsNone})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "name: shared") {
		t.Errorf("the file outside the kustomization was not loaded:\n%s", out)
	}
}

// TestRenderAppliesSettingsToRelativePath pins that the in-memory edit
// reaches the build when the kustomization is named by a relative path, as
// it is when source paths resolve against the working directory.
func TestRenderAppliesSettingsToRelativePath(t *testing.T) {
	root := writeExample(t)
	t.Chdir(root)
	out, err := Render("base", &application.KustomizeSource{NamePrefix: "rel-"}, BuildOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(out, "name: rel-web") != 2 {
		t.Errorf("the prefix was not applied through the relative path:\n%s", out)
	}
}

func TestParseBuildOptions(t *testing.T) {
	tests := map[string]BuildOptions{
		"":                                       {},
		"--enable-helm":                          {EnableHelm: true},
		"--load-restrictor LoadRestrictionsNone": {LoadRestrictions: types.LoadRestrictionsNone},
		"--load-restrictor=LoadRestrictionsRootOnly":                                {LoadRestrictions: types.LoadRestrictionsRootOnly},
		"--enable-helm --helm-command helm3 --load-restrictor LoadRestrictionsNone": {EnableHelm: true, HelmCommand: "helm3", LoadRestrictions: types.LoadRestrictionsNone},
	}
	for arg, want := range tests {
		t.Run(arg, func(t *testing.T) {
			got, err := ParseBuildOptions(arg)
			if err != nil {
				t.Fatal(err)
			}
			if got != want {
				t.Errorf("ParseBuildOptions(%q) = %+v, want %+v", arg, got, want)
			}
		})
	}
	for _, arg := range []string{"--enable-alpha-plugins", "--load-restrictor", "--load-restrictor Loose", "--helm-command"} {
		if _, err := ParseBuildOptions(arg); err == nil {
			t.Errorf("ParseBuildOptions(%q) must fail", arg)
		}
	}
}
