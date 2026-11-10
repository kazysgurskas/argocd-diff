package hydrate

import (
	"testing"

	"github.com/kazysgurskas/argocd-diff/internal/application"
)

// TestSourceKind pins ArgoCD's precedence: an explicit block, then chart,
// then what the path holds, with a plain directory as the fallback.
func TestSourceKind(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"chart/Chart.yaml":                "apiVersion: v2\nname: web\nversion: 0.1.0\n",
		"kustomize/kustomization.yaml":    "resources: []\n",
		"kustomize-yml/kustomization.yml": "resources: []\n",
		"kustomize-cap/Kustomization":     "resources: []\n",
		"plain/a.yaml":                    "kind: A\n",
		"both/Chart.yaml":                 "apiVersion: v2\nname: both\nversion: 0.1.0\n",
		"both/kustomization.yaml":         "resources: []\n",
	})
	tests := []struct {
		name   string
		source application.Source
		want   Kind
	}{
		{"helm block on a plain directory", application.Source{Path: "plain", Helm: &application.HelmSource{}}, KindHelm},
		{"kustomize block on a chart", application.Source{Path: "chart", Kustomize: &application.KustomizeSource{}}, KindKustomize},
		{"directory block on a chart", application.Source{Path: "chart", Directory: &application.DirectorySource{}}, KindDirectory},
		{"plugin block", application.Source{Path: "plain", Plugin: &application.PluginSource{}}, KindPlugin},
		{"chart from a repository", application.Source{Chart: "web"}, KindHelm},
		{"Chart.yaml", application.Source{Path: "chart"}, KindHelm},
		{"kustomization.yaml", application.Source{Path: "kustomize"}, KindKustomize},
		{"kustomization.yml", application.Source{Path: "kustomize-yml"}, KindKustomize},
		{"Kustomization", application.Source{Path: "kustomize-cap"}, KindKustomize},
		{"Chart.yaml and kustomization", application.Source{Path: "both"}, KindKustomize},
		{"plain directory", application.Source{Path: "plain"}, KindDirectory},
		{"missing directory", application.Source{Path: "missing"}, KindDirectory},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sourceKind(&tt.source, root); got != tt.want {
				t.Errorf("sourceKind() = %s, want %s", got, tt.want)
			}
		})
	}
	if got := Kind(0).String(); got != "unknown" {
		t.Errorf("Kind(0).String() = %q", got)
	}
}
