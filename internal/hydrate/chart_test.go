package hydrate

import (
	"bytes"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kazysgurskas/argocd-diff/internal/application"
	"github.com/kazysgurskas/argocd-diff/internal/helm"
)

const gitops = "https://github.com/example/gitops"

// webChart is a chart whose ConfigMap prints the greeting and replicas
// values, for tests that check which value files were applied.
var webChart = map[string]string{
	"charts/web/Chart.yaml":  "apiVersion: v2\nname: web\nversion: 0.1.0\n",
	"charts/web/values.yaml": "greeting: hello\nreplicas: 1\n",
	"charts/web/templates/configmap.yaml": `apiVersion: v1
kind: ConfigMap
metadata:
  name: {{ .Release.Name }}
  namespace: {{ .Release.Namespace }}
data:
  greeting: {{ .Values.greeting }}
  replicas: {{ .Values.replicas | quote }}
`,
}

// captureLogs routes slog to a buffer for the rest of the test.
func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return &logs
}

// TestRenderChartFromPath pins that a path source holding a Chart.yaml is
// templated as a Helm chart, with the release name and namespace from the
// Application, and valueFiles resolved against the chart's path or, under
// $values/, against the checkout.
func TestRenderChartFromPath(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, webChart)
	writeTree(t, root, map[string]string{
		"charts/web/values-stage.yaml": "greeting: hi\n",
		"apps/web/values.yaml":         "replicas: 3\n",
	})
	var app application.Application
	app.Metadata.Name = "web"
	app.Spec.Destination.Namespace = "shop"
	app.Spec.Sources = []*application.Source{
		{RepoURL: gitops, Path: "charts/web", Helm: &application.HelmSource{ValueFiles: []string{"values-stage.yaml", "$values/apps/web/values.yaml"}}},
		{RepoURL: gitops, Ref: "values"},
	}
	manifests, err := Render(app, Options{Root: root, KubeVersion: helm.DefaultKubeVersion})
	if err != nil {
		t.Fatal(err)
	}
	if len(manifests) != 1 {
		t.Fatalf("rendered %d manifests, want 1", len(manifests))
	}
	m := manifests[0]
	if m.Kind != "ConfigMap" || m.Name != "web" || m.Namespace != "shop" {
		t.Errorf("manifest = %s %s/%s, want ConfigMap shop/web", m.Kind, m.Namespace, m.Name)
	}
	for _, want := range []string{"greeting: hi", `replicas: "3"`} {
		if !strings.Contains(m.Content, want) {
			t.Errorf("missing %q in:\n%s", want, m.Content)
		}
	}
}

// TestRenderResolvesAnyRefName pins that $<ref>/ works for every ref name,
// not only values, and that the path after it is relative to the checkout.
func TestRenderResolvesAnyRefName(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, webChart)
	writeTree(t, root, map[string]string{"defaults/web.yaml": "greeting: shared\n"})
	var app application.Application
	app.Metadata.Name = "web"
	app.Spec.Sources = []*application.Source{
		{RepoURL: gitops, Path: "charts/web", Helm: &application.HelmSource{ValueFiles: []string{"$shared/defaults/web.yaml"}}},
		{RepoURL: gitops, Ref: "shared"},
	}
	manifests, err := Render(app, Options{Root: root, KubeVersion: helm.DefaultKubeVersion})
	if err != nil {
		t.Fatal(err)
	}
	if len(manifests) != 1 || !strings.Contains(manifests[0].Content, "greeting: shared") {
		t.Errorf("manifests = %+v, want the shared greeting", manifests)
	}
}

// TestRenderRefusesUnknownRef pins that a valueFiles entry naming a ref no
// source sets fails, naming the refs there are, and is skipped only when
// asked.
func TestRenderRefusesUnknownRef(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, webChart)
	var app application.Application
	app.Metadata.Name = "web"
	app.Spec.Sources = []*application.Source{
		{RepoURL: gitops, Path: "charts/web", Helm: &application.HelmSource{ValueFiles: []string{"$missing/web.yaml"}}},
		{RepoURL: gitops, Ref: "values"},
	}
	_, err := Render(app, Options{Root: root, KubeVersion: helm.DefaultKubeVersion})
	for _, want := range []string{`no source has ref "missing"`, "refs: $values", "valueFiles entry $missing/web.yaml"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("error %v lacks %q", err, want)
		}
	}
	logs := captureLogs(t)
	manifests, err := Render(app, Options{Root: root, KubeVersion: helm.DefaultKubeVersion, SkipUnrenderable: true})
	if err != nil || len(manifests) != 0 {
		t.Errorf("with SkipUnrenderable: manifests = %+v, err = %v; want none and nil", manifests, err)
	}
	if !strings.Contains(logs.String(), "level=WARN") || !strings.Contains(logs.String(), "skipping source that cannot be rendered") {
		t.Errorf("the skip was not logged as a warning:\n%s", logs.String())
	}
}

// TestRenderRefusesDuplicateRef pins that two sources with one ref fail
// the application, as in ArgoCD, and that SkipUnrenderable skips the
// application rather than guessing which source $<ref> means.
func TestRenderRefusesDuplicateRef(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{"apps/web/a.yaml": "kind: ConfigMap\nmetadata:\n  name: a\n"})
	var app application.Application
	app.Metadata.Name = "web"
	app.Spec.Sources = []*application.Source{
		{RepoURL: gitops, Path: "apps/web", Directory: &application.DirectorySource{}},
		{RepoURL: gitops, Ref: "values"},
		{RepoURL: "https://github.com/example/other", Ref: "values"},
	}
	if _, err := Render(app, Options{Root: root}); err == nil || !strings.Contains(err.Error(), "multiple sources have ref") {
		t.Errorf("want a duplicate ref error, got %v", err)
	}
	logs := captureLogs(t)
	manifests, err := Render(app, Options{Root: root, SkipUnrenderable: true})
	if err != nil || len(manifests) != 0 {
		t.Errorf("with SkipUnrenderable: manifests = %+v, err = %v; want none and nil", manifests, err)
	}
	if !strings.Contains(logs.String(), "skipping application that cannot be rendered") {
		t.Errorf("the skip was not logged:\n%s", logs.String())
	}
}

// TestRenderSkipsUnrenderableSourcesWhenAsked pins that with
// SkipUnrenderable a source ArgoCD would refuse is logged as a warning and
// left out while the other sources still render, and that without it the
// render fails.
func TestRenderSkipsUnrenderableSourcesWhenAsked(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{"apps/web/a.yaml": "kind: ConfigMap\nmetadata:\n  name: a\n"})
	var app application.Application
	app.Metadata.Name = "web"
	app.Spec.Sources = []*application.Source{
		{RepoURL: gitops, Path: "apps/bad", Helm: &application.HelmSource{}, Directory: &application.DirectorySource{}},
		{RepoURL: gitops, Path: "apps/web", Directory: &application.DirectorySource{}},
	}
	if _, err := Render(app, Options{Root: root}); err == nil || !strings.Contains(err.Error(), "multiple source types set: Helm, Directory") {
		t.Errorf("want ArgoCD's refusal, got %v", err)
	}
	logs := captureLogs(t)
	manifests, err := Render(app, Options{Root: root, SkipUnrenderable: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(manifests) != 1 || manifests[0].Name != "a" {
		t.Errorf("manifests = %+v, want the one from the valid source", manifests)
	}
	if !strings.Contains(logs.String(), "level=WARN") || !strings.Contains(logs.String(), "multiple source types set") {
		t.Errorf("the skip was not logged as a warning with the reason:\n%s", logs.String())
	}
}

// TestRenderMissingValuesFileAlwaysFails pins that a value file that does
// not exist is a failure to render, named after its valueFiles entry, and
// is not skipped by SkipUnrenderable.
func TestRenderMissingValuesFileAlwaysFails(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, webChart)
	var app application.Application
	app.Metadata.Name = "web"
	app.Spec.Sources = []*application.Source{
		{RepoURL: gitops, Path: "charts/web", Helm: &application.HelmSource{ValueFiles: []string{"$values/apps/web/missing.yaml"}}},
		{RepoURL: gitops, Ref: "values"},
	}
	for _, skip := range []bool{false, true} {
		_, err := Render(app, Options{Root: root, KubeVersion: helm.DefaultKubeVersion, SkipUnrenderable: skip})
		if err == nil || !strings.Contains(err.Error(), "valueFiles entry $values/apps/web/missing.yaml") {
			t.Errorf("SkipUnrenderable=%v: want an error naming the entry, got %v", skip, err)
		}
	}
}

// TestRenderChartFromPathBuildsDependencies pins that a chart in the
// checkout gets the dependencies its Chart.yaml declares before it is
// templated, and that the checkout is left as it was.
func TestRenderChartFromPathBuildsDependencies(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"charts/common/Chart.yaml":          "apiVersion: v2\nname: common\nversion: 0.1.0\ntype: library\n",
		"charts/common/templates/_name.tpl": `{{- define "common.name" -}}from-common{{- end -}}`,
		"charts/web/Chart.yaml": "apiVersion: v2\nname: web\nversion: 0.1.0\ndependencies:\n" +
			"  - name: common\n    version: 0.1.0\n    repository: file://../common\n",
		"charts/web/templates/configmap.yaml": "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: {{ include \"common.name\" . }}\n",
	})
	var app application.Application
	app.Metadata.Name = "web"
	app.Spec.Source = &application.Source{RepoURL: gitops, Path: "charts/web"}
	manifests, err := Render(app, Options{Root: root, KubeVersion: helm.DefaultKubeVersion})
	if err != nil {
		t.Fatal(err)
	}
	if len(manifests) != 1 || manifests[0].Name != "from-common" {
		t.Errorf("manifests = %+v, want one ConfigMap named by the dependency's template", manifests)
	}
	for _, name := range []string{"charts/web/Chart.lock", "charts/web/charts"} {
		if _, err := os.Stat(filepath.Join(root, name)); err == nil {
			t.Errorf("%s was written into the checkout", name)
		}
	}
}

// TestRenderHelmSourceIsAChart pins ArgoCD's precedence the other way: a
// source that sets helm is a Helm chart whatever its path holds, so one
// without a Chart.yaml fails as a chart rather than being read as a
// directory.
func TestRenderHelmSourceIsAChart(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{"apps/web/configmap.yaml": "kind: ConfigMap\nmetadata:\n  name: raw\n"})
	var app application.Application
	app.Metadata.Name = "web"
	app.Spec.Source = &application.Source{RepoURL: gitops, Path: "apps/web", Helm: &application.HelmSource{}}
	if _, err := Render(app, Options{Root: root, KubeVersion: helm.DefaultKubeVersion}); err == nil || !strings.Contains(err.Error(), "Chart.yaml") {
		t.Errorf("want a missing Chart.yaml error, got %v", err)
	}
}

// TestRenderRejectsSourceWithoutPath pins that a source with none of path,
// chart and ref is refused rather than read as a chart at the root of the
// checkout.
func TestRenderRejectsSourceWithoutPath(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{"Chart.yaml": "apiVersion: v2\nname: root\nversion: 0.1.0\n"})
	var app application.Application
	app.Metadata.Name = "web"
	app.Spec.Source = &application.Source{RepoURL: gitops}
	if _, err := Render(app, Options{Root: root}); err == nil || !strings.Contains(err.Error(), "none of path, chart and ref") {
		t.Errorf("want a refusal, got %v", err)
	}
}

// TestRenderRefusesRefFromAnotherRepository pins the fix for refs that
// name more than one repository: a $<ref>/ file lent by a source whose
// repository is not the checkout's cannot be read from the checkout, so it
// is refused. With the checkout's repository given, the other ref works.
func TestRenderRefusesRefFromAnotherRepository(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, webChart)
	writeTree(t, root, map[string]string{"defaults/web.yaml": "greeting: shared\n"})
	shared := "https://github.com/example/shared"
	var app application.Application
	app.Metadata.Name = "web"
	app.Spec.Sources = []*application.Source{
		{RepoURL: gitops, Path: "charts/web", Helm: &application.HelmSource{ValueFiles: []string{"$shared/defaults/web.yaml"}}},
		{RepoURL: gitops, Ref: "values"},
		{RepoURL: shared, Ref: "shared"},
	}
	_, err := Render(app, Options{Root: root, KubeVersion: helm.DefaultKubeVersion})
	if err == nil || !errors.Is(err, errUnrenderable) || !strings.Contains(err.Error(), "not the checkout") {
		t.Errorf("want a refusal of the foreign ref, got %v", err)
	}

	// Told that the checkout is the shared repository, the shared ref
	// resolves and the chart, now foreign, is skipped.
	manifests, err := Render(app, Options{Root: root, KubeVersion: helm.DefaultKubeVersion, RepoURL: shared})
	if err != nil || len(manifests) != 0 {
		t.Errorf("with the shared repository as checkout: manifests = %+v, err = %v; want the chart skipped as foreign", manifests, err)
	}
	app.Spec.Sources[0].RepoURL = shared
	manifests, err = Render(app, Options{Root: root, KubeVersion: helm.DefaultKubeVersion, RepoURL: shared})
	if err != nil || len(manifests) != 1 || !strings.Contains(manifests[0].Content, "greeting: shared") {
		t.Errorf("with the chart in the shared repository: manifests = %+v, err = %v; want the shared greeting", manifests, err)
	}
}

// TestRenderMatchesSSHAndHTTPSForms pins that the SSH and HTTPS URLs of
// one repository name the same checkout: ref sources that mix them agree,
// and an HTTPS --repo-url matches Applications written with SSH.
func TestRenderMatchesSSHAndHTTPSForms(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, webChart)
	writeTree(t, root, map[string]string{"defaults/web.yaml": "greeting: shared\n"})
	ssh := "git@github.com:example/gitops.git"
	var app application.Application
	app.Metadata.Name = "web"
	app.Spec.Sources = []*application.Source{
		{RepoURL: ssh, Path: "charts/web", Helm: &application.HelmSource{ValueFiles: []string{"$shared/defaults/web.yaml"}}},
		{RepoURL: ssh, Ref: "shared"},
		{RepoURL: "ssh://git@github.com/example/gitops", Ref: "other"},
		{RepoURL: gitops, Ref: "third"},
	}
	for name, opts := range map[string]Options{
		"without --repo-url":       {Root: root, KubeVersion: helm.DefaultKubeVersion},
		"with an HTTPS --repo-url": {Root: root, KubeVersion: helm.DefaultKubeVersion, RepoURL: gitops},
	} {
		manifests, err := Render(app, opts)
		if err != nil || len(manifests) != 1 || !strings.Contains(manifests[0].Content, "greeting: shared") {
			t.Errorf("%s: manifests = %+v, err = %v; want the shared greeting", name, manifests, err)
		}
	}
}

// TestRenderNeedsRepoURLWhenRefsDisagree pins that with ref sources on
// different repositories and none named values, a $<ref>/ file is refused
// with a pointer to --repo-url rather than guessed.
func TestRenderNeedsRepoURLWhenRefsDisagree(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, webChart)
	writeTree(t, root, map[string]string{"defaults/web.yaml": "greeting: shared\n"})
	var app application.Application
	app.Metadata.Name = "web"
	app.Spec.Sources = []*application.Source{
		{RepoURL: gitops, Path: "charts/web", Helm: &application.HelmSource{ValueFiles: []string{"$shared/defaults/web.yaml"}}},
		{RepoURL: gitops, Ref: "shared"},
		{RepoURL: "https://github.com/example/other", Ref: "other"},
	}
	_, err := Render(app, Options{Root: root, KubeVersion: helm.DefaultKubeVersion})
	if err == nil || !errors.Is(err, errUnrenderable) || !strings.Contains(err.Error(), "--repo-url") {
		t.Errorf("want a refusal pointing at --repo-url, got %v", err)
	}
	manifests, err := Render(app, Options{Root: root, KubeVersion: helm.DefaultKubeVersion, RepoURL: gitops})
	if err != nil || len(manifests) != 1 || !strings.Contains(manifests[0].Content, "greeting: shared") {
		t.Errorf("with --repo-url: manifests = %+v, err = %v", manifests, err)
	}
}

// TestRenderNeedsRepoURLForPathWhenRefsDisagree pins that with ref
// sources on different repositories and none named values, a path source
// is refused with a pointer to --repo-url rather than read from the
// checkout, is skipped only when asked, and renders once --repo-url says
// the checkout is its repository.
func TestRenderNeedsRepoURLForPathWhenRefsDisagree(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, webChart)
	other := "https://github.com/example/other"
	var app application.Application
	app.Metadata.Name = "web"
	app.Spec.Sources = []*application.Source{
		{RepoURL: other, Path: "charts/web"},
		{RepoURL: gitops, Ref: "shared"},
		{RepoURL: other, Ref: "other"},
	}
	_, err := Render(app, Options{Root: root, KubeVersion: helm.DefaultKubeVersion})
	if err == nil || !errors.Is(err, errUnrenderable) || !strings.Contains(err.Error(), "--repo-url") {
		t.Errorf("want a refusal pointing at --repo-url, got %v", err)
	}
	logs := captureLogs(t)
	manifests, err := Render(app, Options{Root: root, KubeVersion: helm.DefaultKubeVersion, SkipUnrenderable: true})
	if err != nil || len(manifests) != 0 || !strings.Contains(logs.String(), "skipping source that cannot be rendered") {
		t.Errorf("with --skip-unrenderable: manifests = %+v, err = %v, logs:\n%s", manifests, err, logs)
	}
	manifests, err = Render(app, Options{Root: root, KubeVersion: helm.DefaultKubeVersion, RepoURL: other})
	if err != nil || len(manifests) != 1 {
		t.Errorf("with --repo-url: manifests = %+v, err = %v", manifests, err)
	}
}

func TestRenderRefusesRefWithoutFile(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, webChart)
	var app application.Application
	app.Metadata.Name = "web"
	app.Spec.Sources = []*application.Source{
		{RepoURL: gitops, Path: "charts/web", Helm: &application.HelmSource{ValueFiles: []string{"$values"}}},
		{RepoURL: gitops, Ref: "values"},
	}
	_, err := Render(app, Options{Root: root, KubeVersion: helm.DefaultKubeVersion})
	if err == nil || !errors.Is(err, errUnrenderable) || !strings.Contains(err.Error(), "no file follows the ref") {
		t.Errorf("want a refusal of a bare $values, got %v", err)
	}
}

// TestRenderIgnoresMissingValueFilesWhenAsked pins ArgoCD's
// helm.ignoreMissingValueFiles: a missing file, from the chart or a ref, is
// dropped instead of failing.
func TestRenderIgnoresMissingValueFilesWhenAsked(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, webChart)
	writeTree(t, root, map[string]string{"charts/web/values-stage.yaml": "greeting: hi\n"})
	var app application.Application
	app.Metadata.Name = "web"
	app.Spec.Sources = []*application.Source{
		{RepoURL: gitops, Path: "charts/web", Helm: &application.HelmSource{
			ValueFiles:              []string{"values-stage.yaml", "values-missing.yaml", "$values/apps/web/missing.yaml"},
			IgnoreMissingValueFiles: true,
		}},
		{RepoURL: gitops, Ref: "values"},
	}
	manifests, err := Render(app, Options{Root: root, KubeVersion: helm.DefaultKubeVersion})
	if err != nil {
		t.Fatal(err)
	}
	if len(manifests) != 1 || !strings.Contains(manifests[0].Content, "greeting: hi") {
		t.Errorf("manifests = %+v, want the file that exists applied", manifests)
	}
}

// TestValueFiles pins that $<ref>/ names the checkout and any other entry
// the chart's own directory, which for a pulled chart is the chart, and
// that every file must exist.
func TestValueFiles(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"apps/web/values.yaml":         "a: 1\n",
		"charts/web/values-stage.yaml": "a: 2\n",
		"charts/shared/values.yaml":    "a: 3\n",
	})
	chartDir := filepath.Join(root, "charts", "web")
	refs := map[string]*application.Source{"$values": {RepoURL: gitops, Ref: "values"}}
	source := &application.Source{Helm: &application.HelmSource{ValueFiles: []string{"$values/apps/web/values.yaml", "values-stage.yaml", "../shared/values.yaml"}}}
	got, err := valueFiles(source, chartDir, refs, checkoutRepo{url: gitops}, Options{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{filepath.Join(root, "apps/web/values.yaml"), filepath.Join(chartDir, "values-stage.yaml"), filepath.Join(root, "charts/shared/values.yaml")}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("valueFiles() = %v, want %v", got, want)
	}
	if got, err := valueFiles(&application.Source{}, chartDir, refs, checkoutRepo{url: gitops}, Options{Root: root}); err != nil || len(got) != 0 {
		t.Errorf("valueFiles() without helm = %v, %v", got, err)
	}
	source.Helm.ValueFiles = []string{"missing.yaml"}
	if _, err := valueFiles(source, chartDir, refs, checkoutRepo{url: gitops}, Options{Root: root}); err == nil {
		t.Error("a missing file must be an error")
	}
}
