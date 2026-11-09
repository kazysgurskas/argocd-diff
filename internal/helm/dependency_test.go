package helm

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"helm.sh/helm/v3/pkg/chart/loader"
	"helm.sh/helm/v3/pkg/chartutil"
	"helm.sh/helm/v3/pkg/repo"
)

func writeFiles(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, content := range files {
		path := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// writeCommonChart writes the library chart common, version 0.1.0, which
// defines the named template common.name, at <dir>/common.
func writeCommonChart(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "common")
	writeFiles(t, path, map[string]string{
		"Chart.yaml":          "apiVersion: v2\nname: common\nversion: 0.1.0\ntype: library\n",
		"templates/_name.tpl": `{{- define "common.name" -}}from-common{{- end -}}`,
	})
	return path
}

// writeParentChart writes a chart at <dir>/parent that depends on common at
// version from repository, names its ConfigMap with common.name and prints
// the greeting value.
func writeParentChart(t *testing.T, dir, version, repository string) string {
	t.Helper()
	path := filepath.Join(dir, "parent")
	writeFiles(t, path, map[string]string{
		"Chart.yaml": "apiVersion: v2\nname: parent\nversion: 0.1.0\ndependencies:\n" +
			"  - name: common\n    version: " + version + "\n    repository: " + repository + "\n",
		"templates/configmap.yaml": "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: {{ include \"common.name\" . }}\n" +
			"data:\n  greeting: {{ .Values.greeting | default \"none\" }}\n",
	})
	return path
}

// vendorCommon archives the common chart at commonPath into the parent
// chart's charts/ directory, as `helm dependency build` would have.
func vendorCommon(t *testing.T, commonPath, parent string) {
	t.Helper()
	common, err := loader.Load(commonPath)
	if err != nil {
		t.Fatal(err)
	}
	charts := filepath.Join(parent, "charts")
	if err := os.MkdirAll(charts, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := chartutil.Save(common, charts); err != nil {
		t.Fatal(err)
	}
}

func TestBuildDependenciesLeavesChartWithoutDependencies(t *testing.T) {
	path := writeChart(t, `""`)
	got, err := BuildDependencies(path, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if got != path {
		t.Errorf("BuildDependencies() = %q, want the chart itself", got)
	}
	for _, name := range []string{"Chart.lock", "charts"} {
		if _, err := os.Stat(filepath.Join(path, name)); err == nil {
			t.Errorf("%s was written to a chart without dependencies", name)
		}
	}
}

// TestBuildDependenciesLeavesPresentDependency pins that a dependency
// already under charts/ in a version that satisfies Chart.yaml is used as it
// is, without contacting its repository, as ArgoCD only builds when
// templating fails. Once Chart.yaml asks for a version the archive does not
// satisfy, the dependency is missing and the repository is contacted.
func TestBuildDependenciesLeavesPresentDependency(t *testing.T) {
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		http.NotFound(w, nil)
	}))
	defer server.Close()
	dir := t.TempDir()
	parent := writeParentChart(t, dir, "0.1.0", server.URL)
	vendorCommon(t, writeCommonChart(t, dir), parent)

	got, err := BuildDependencies(parent, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if got != parent || hits.Load() != 0 {
		t.Errorf("BuildDependencies() = %q with %d repository requests, want the chart itself and none", got, hits.Load())
	}

	writeParentChart(t, dir, "0.2.0", server.URL)
	if _, err := BuildDependencies(parent, t.TempDir()); err == nil || hits.Load() == 0 {
		t.Errorf("a dependency whose version no longer satisfies Chart.yaml must be fetched; got %v after %d requests", err, hits.Load())
	}
}

// TestBuildDependenciesArchivesLocalDependency pins that a file://
// dependency is archived into a scratch copy of the chart, the chart itself
// untouched, that .git is not copied and that a symbolic link in the chart
// is followed.
func TestBuildDependenciesArchivesLocalDependency(t *testing.T) {
	dir := t.TempDir()
	writeCommonChart(t, dir)
	parent := writeParentChart(t, dir, "0.1.0", "file://../common")
	writeFiles(t, parent, map[string]string{".git/HEAD": "ref: refs/heads/main\n"})
	writeFiles(t, dir, map[string]string{"shared-values.yaml": "greeting: shared\n"})
	if err := os.Symlink(filepath.Join(dir, "shared-values.yaml"), filepath.Join(parent, "values.yaml")); err != nil {
		t.Fatal(err)
	}

	scratch := t.TempDir()
	got, err := BuildDependencies(parent, scratch)
	if err != nil {
		t.Fatal(err)
	}
	if got == parent || !strings.HasPrefix(got, scratch) {
		t.Fatalf("BuildDependencies() = %q, want a copy below %s", got, scratch)
	}
	for _, name := range []string{"charts/common-0.1.0.tgz", "Chart.lock"} {
		if _, err := os.Stat(filepath.Join(got, name)); err != nil {
			t.Errorf("%s is missing from the copy", name)
		}
		if _, err := os.Stat(filepath.Join(parent, name)); err == nil {
			t.Errorf("%s was written to the chart itself", name)
		}
	}
	if _, err := os.Stat(filepath.Join(got, ".git")); err == nil {
		t.Error(".git was copied")
	}
	out, err := Render(got, "rel", "ns", DefaultKubeVersion, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"name: from-common", "greeting: shared"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

// TestBuildDependenciesRebuildsLocalDependency pins the case a stale
// archive would hide: the source of a file:// dependency changes without a
// version bump while an archive of the old one sits under charts/. The
// dependency is rebuilt and the change shows.
func TestBuildDependenciesRebuildsLocalDependency(t *testing.T) {
	dir := t.TempDir()
	common := writeCommonChart(t, dir)
	parent := writeParentChart(t, dir, "0.1.0", "file://../common")
	vendorCommon(t, common, parent)
	writeFiles(t, common, map[string]string{"templates/_name.tpl": `{{- define "common.name" -}}from-edited-common{{- end -}}`})

	got, err := BuildDependencies(parent, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	out, err := Render(got, "rel", "ns", DefaultKubeVersion, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "name: from-edited-common") {
		t.Errorf("the stale archive was used:\n%s", out)
	}
}

// TestBuildDependenciesDownloadsFromRepository serves a chart repository
// over HTTP and pins that a dependency is downloaded from it both without a
// Chart.lock, when the version is resolved and a lock written in the copy,
// and with the lock, when `helm dependency build` downloads only from
// repositories it is configured with. The user's own repositories.yaml is
// left alone.
func TestBuildDependenciesDownloadsFromRepository(t *testing.T) {
	repositoryDir := t.TempDir()
	common, err := loader.Load(writeCommonChart(t, t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := chartutil.Save(common, repositoryDir); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.FileServer(http.Dir(repositoryDir)))
	defer server.Close()
	index, err := repo.IndexDirectory(repositoryDir, server.URL)
	if err != nil {
		t.Fatal(err)
	}
	if err := index.WriteFile(filepath.Join(repositoryDir, "index.yaml"), 0o644); err != nil {
		t.Fatal(err)
	}
	userConfig := filepath.Join(t.TempDir(), "repositories.yaml")
	t.Setenv("HELM_REPOSITORY_CONFIG", userConfig)
	parent := writeParentChart(t, t.TempDir(), "0.1.0", server.URL)

	withoutLock, err := BuildDependencies(parent, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(withoutLock, "charts", "common-0.1.0.tgz")); err != nil {
		t.Error("common was not downloaded into the copy")
	}
	lock, err := os.ReadFile(filepath.Join(withoutLock, "Chart.lock"))
	if err != nil {
		t.Fatal("no Chart.lock in the copy")
	}
	if _, err := os.Stat(filepath.Join(parent, "Chart.lock")); err == nil {
		t.Error("Chart.lock was written to the chart itself")
	}

	if err := os.WriteFile(filepath.Join(parent, "Chart.lock"), lock, 0o644); err != nil {
		t.Fatal(err)
	}
	withLock, err := BuildDependencies(parent, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(withLock, "charts", "common-0.1.0.tgz")); err != nil {
		t.Error("common was not downloaded into the copy from the lock")
	}
	if _, err := os.Stat(userConfig); err == nil {
		t.Error("the user's repositories.yaml was written")
	}
}

func TestBuildDependenciesRejectsMissingChart(t *testing.T) {
	if _, err := BuildDependencies(filepath.Join(t.TempDir(), "missing"), t.TempDir()); err == nil {
		t.Error("expected an error for a path without a chart")
	}
}
