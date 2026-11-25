package helm

import "testing"

func TestIsOCI(t *testing.T) {
	tests := map[string]bool{
		"oci://ghcr.io/stefanprodan/charts":                    true,
		"ghcr.io/stefanprodan/charts":                          true,
		"europe-west2-docker.pkg.dev/exampleco-systems/charts": true,
		"https://charts.jetstack.io":                           false,
		"http://charts.example.com/stable":                     false,
		"podinfo":                                              false,
	}
	for repoURL, want := range tests {
		t.Run(repoURL, func(t *testing.T) {
			if got := isOCI(repoURL); got != want {
				t.Errorf("isOCI(%q) = %v, want %v", repoURL, got, want)
			}
		})
	}
}

func TestPullRefusesUnknownRepository(t *testing.T) {
	if _, err := Pull(t.TempDir(), "ftp://charts", "web", "1.0.0"); err == nil {
		t.Error("expected an error for a repository that is neither http(s) nor OCI")
	}
}
