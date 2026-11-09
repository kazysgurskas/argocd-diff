package hydrate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kazysgurskas/argocd-diff/internal/application"
	"github.com/kazysgurskas/argocd-diff/internal/helm"
)

// TestRenderChartFromPath pins that a path source holding a Chart.yaml is
// templated as a Helm chart, with the release name and namespace from the
// Application, and valueFiles resolved against the chart's path or, under
// $values/, against the checkout.
func TestRenderChartFromPath(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"charts/web/Chart.yaml":        "apiVersion: v2\nname: web\nversion: 0.1.0\n",
		"charts/web/values.yaml":       "greeting: hello\nreplicas: 1\n",
		"charts/web/values-stage.yaml": "greeting: hi\n",
		"charts/web/templates/configmap.yaml": `apiVersion: v1
kind: ConfigMap
metadata:
  name: {{ .Release.Name }}
  namespace: {{ .Release.Namespace }}
data:
  greeting: {{ .Values.greeting }}
  replicas: {{ .Values.replicas | quote }}
`,
		"apps/web/values.yaml": "replicas: 3\n",
	})
	var app application.Application
	app.Metadata.Name = "web"
	app.Spec.Destination.Namespace = "shop"
	app.Spec.Sources = []*application.Source{
		{
			RepoURL: "https://github.com/example/gitops",
			Path:    "charts/web",
			Helm:    &application.HelmSource{ValueFiles: []string{"values-stage.yaml", "$values/apps/web/values.yaml"}},
		},
		{RepoURL: "https://github.com/example/gitops", Ref: "values"},
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
	app.Spec.Source = &application.Source{RepoURL: "https://github.com/example/gitops", Path: "charts/web"}
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
	app.Spec.Source = &application.Source{RepoURL: "https://github.com/example/gitops", Path: "apps/web", Helm: &application.HelmSource{}}
	if _, err := Render(app, Options{Root: root, KubeVersion: helm.DefaultKubeVersion}); err == nil || !strings.Contains(err.Error(), "Chart.yaml") {
		t.Errorf("want a missing Chart.yaml error, got %v", err)
	}
}

func TestRenderRefusesHelmWithDirectory(t *testing.T) {
	var app application.Application
	app.Metadata.Name = "web"
	app.Spec.Source = &application.Source{RepoURL: "https://github.com/example/gitops", Path: "apps/web", Helm: &application.HelmSource{}, Directory: &application.DirectorySource{}}
	if _, err := Render(app, Options{Root: t.TempDir()}); err == nil || !strings.Contains(err.Error(), "both set") {
		t.Errorf("want a refusal of helm with directory, got %v", err)
	}
}

// TestValueFilePaths pins that $values/ names the checkout and any other
// entry the chart's own directory, which for a pulled chart is the chart.
func TestValueFilePaths(t *testing.T) {
	got := valueFilePaths([]string{"$values/apps/web/values.yaml", "values-stage.yaml", "../shared/values.yaml"}, "/root/charts/web", "/root")
	want := []string{"/root/apps/web/values.yaml", "/root/charts/web/values-stage.yaml", "/root/charts/shared/values.yaml"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("valueFilePaths() = %v, want %v", got, want)
	}
	if got := valueFilePaths(nil, "/cache/1.0.0/web", "/root"); len(got) != 0 {
		t.Errorf("valueFilePaths(nil) = %v", got)
	}
}

// TestRenderDirectoryWinsOverChart pins ArgoCD's precedence: a source that
// sets directory is read as one even when its path holds a Chart.yaml.
func TestRenderDirectoryWinsOverChart(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"apps/web/Chart.yaml":     "apiVersion: v2\nname: web\nversion: 0.1.0\n",
		"apps/web/configmap.yaml": "kind: ConfigMap\nmetadata:\n  name: raw\n",
	})
	var app application.Application
	app.Metadata.Name = "web"
	app.Spec.Source = &application.Source{RepoURL: "https://github.com/example/gitops", Path: "apps/web", Directory: &application.DirectorySource{}}
	manifests, err := Render(app, Options{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	if len(manifests) != 1 || manifests[0].Name != "raw" {
		t.Errorf("manifests = %+v, want the one raw ConfigMap", manifests)
	}
}

// TestRenderRejectsSourceWithoutPath pins that a source with neither path,
// chart nor directory is refused rather than read as a chart at the root of
// the checkout.
func TestRenderRejectsSourceWithoutPath(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{"Chart.yaml": "apiVersion: v2\nname: root\nversion: 0.1.0\n"})
	var app application.Application
	app.Metadata.Name = "web"
	app.Spec.Source = &application.Source{RepoURL: "https://github.com/example/gitops"}
	if _, err := Render(app, Options{Root: root}); err == nil || !strings.Contains(err.Error(), "neither chart, helm nor directory") {
		t.Errorf("want a refusal, got %v", err)
	}
}
