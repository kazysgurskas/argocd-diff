package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const manifests = `apiVersion: v1
kind: Namespace
metadata:
  name: shop
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: web
  namespace: shop
spec:
  replicas: 1
---
apiVersion: v1
kind: Secret
metadata:
  name: web-env
  namespace: shop
stringData:
  token: hunter2
`

// writeFixture writes a directory-source application and returns the
// applications file path.
func writeFixture(t *testing.T, dir string) string {
	t.Helper()
	source := filepath.Join(dir, "src")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "all.yaml"), []byte(manifests), 0o644); err != nil {
		t.Fatal(err)
	}
	applications := filepath.Join(dir, "applications.yaml")
	content := "apiVersion: argoproj.io/v1alpha1\nkind: Application\nmetadata:\n  name: web.stage.c1\nspec:\n  destination:\n    namespace: shop\n  source:\n    repoURL: https://github.com/example/gitops\n    path: " + source + "\n    directory: {}\n"
	if err := os.WriteFile(applications, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return applications
}

func TestHydrateWritesManifestTree(t *testing.T) {
	dir := t.TempDir()
	applications := writeFixture(t, dir)
	out := filepath.Join(dir, "out")

	root := New()
	root.SetArgs([]string{"hydrate", "--applications", applications, "--rendered-dir", out})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}

	for path, want := range map[string]string{
		"web.stage.c1/Namespace/shop.yaml":      "---\napiVersion: v1\nkind: Namespace\nmetadata:\n  name: shop\n",
		"web.stage.c1/shop/Deployment/web.yaml": "---\napiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: web\n  namespace: shop\nspec:\n  replicas: 1\n",
		"web.stage.c1/shop/Secret/web-env.yaml": "token: '***REDACTED***'",
	} {
		got, err := os.ReadFile(filepath.Join(out, filepath.FromSlash(path)))
		if err != nil {
			t.Errorf("%s: %v", path, err)
			continue
		}
		if !strings.Contains(string(got), want) {
			t.Errorf("%s:\n%s\nwant to contain:\n%s", path, got, want)
		}
	}
	if strings.Contains(readFile(t, filepath.Join(out, "web.stage.c1", "shop", "Secret", "web-env.yaml")), "hunter2") {
		t.Error("secret value written in clear")
	}
}

func TestHydrateMissingApplicationsFileIsAnError(t *testing.T) {
	root := New()
	root.SetArgs([]string{"hydrate", "--applications", filepath.Join(t.TempDir(), "missing.yaml"), "--rendered-dir", t.TempDir()})
	err := root.Execute()
	if err == nil || !strings.HasPrefix(err.Error(), "load applications: ") {
		t.Errorf("want a wrapped load error, got %v", err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(content)
}
