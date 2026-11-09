package helm

import (
	"crypto/sha256"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/Masterminds/semver/v3"
	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/chart/loader"
	"helm.sh/helm/v3/pkg/cli"
	"helm.sh/helm/v3/pkg/downloader"
	"helm.sh/helm/v3/pkg/getter"
	"helm.sh/helm/v3/pkg/registry"
	"helm.sh/helm/v3/pkg/repo"
)

// BuildDependencies returns the directory to template the chart at
// chartPath from, with every dependency its Chart.yaml declares under
// charts/. A chart whose dependencies are all there, in versions that
// satisfy it, is returned as it is. Otherwise the chart, and the local
// charts its file:// dependencies name, are copied below scratchDir with
// their layout kept, and the dependencies are built in the copy as `helm
// dependency build` would: Chart.lock pins the versions when the chart has
// one, else the version ranges are resolved and a Chart.lock is written in
// the copy. Nothing is written to the chart itself. A file:// dependency
// never counts as there, since its source changes without a version bump
// and an archive of it may be stale.
func BuildDependencies(chartPath, scratchDir string) (string, error) {
	c, err := loader.Load(chartPath)
	if err != nil {
		return "", fmt.Errorf("load chart %s: %w", chartPath, err)
	}
	if !hasMissingDependencies(c) {
		return chartPath, nil
	}

	absPath, err := filepath.Abs(chartPath)
	if err != nil {
		return "", err
	}
	for _, dir := range localChartDirectories(absPath, c) {
		if err := copyDirectory(dir, scratchPath(scratchDir, dir)); err != nil {
			return "", fmt.Errorf("copy %s: %w", dir, err)
		}
	}
	copyPath := scratchPath(scratchDir, absPath)

	// With a Chart.lock, the manager downloads only from repositories named
	// in a repositories.yaml, so the chart's repositories get one of their
	// own rather than being added to the user's.
	repositoryDir, err := os.MkdirTemp("", "argocd-diff-repositories-")
	if err != nil {
		return "", err
	}
	defer func() { _ = os.RemoveAll(repositoryDir) }()
	settings := cli.New()
	settings.RepositoryConfig = filepath.Join(repositoryDir, "repositories.yaml")
	settings.RepositoryCache = repositoryDir
	var repositories []string
	for _, dependency := range c.Metadata.Dependencies {
		if url := dependency.Repository; strings.HasPrefix(url, "https://") || strings.HasPrefix(url, "http://") {
			repositories = append(repositories, url)
		}
	}
	if err := writeRepositories(settings.RepositoryConfig, repositories); err != nil {
		return "", err
	}
	registryClient, err := registry.NewClient(
		registry.ClientOptEnableCache(true),
		registry.ClientOptWriter(io.Discard),
		registry.ClientOptCredentialsFile(settings.RegistryConfig),
	)
	if err != nil {
		return "", err
	}

	slog.Info("building chart dependencies", "chart", c.Name(), "path", chartPath, "dependencies", len(c.Metadata.Dependencies))
	manager := &downloader.Manager{
		Out:              io.Discard,
		ChartPath:        copyPath,
		Getters:          getter.All(settings),
		RegistryClient:   registryClient,
		RepositoryConfig: settings.RepositoryConfig,
		RepositoryCache:  settings.RepositoryCache,
	}
	if err := manager.Build(); err != nil {
		return "", fmt.Errorf("build dependencies of chart %s: %w", c.Name(), err)
	}
	return copyPath, nil
}

// hasMissingDependencies reports whether a dependency the chart declares is
// not under charts/ in a version that satisfies it, or comes from a file://
// path.
func hasMissingDependencies(c *chart.Chart) bool {
	for _, declared := range c.Metadata.Dependencies {
		if strings.HasPrefix(declared.Repository, "file://") || !hasDependency(c, declared) {
			return true
		}
	}
	return false
}

// hasDependency reports whether a chart named as declared, in a version
// that satisfies it, is loaded from charts/.
func hasDependency(c *chart.Chart, declared *chart.Dependency) bool {
	for _, loaded := range c.Dependencies() {
		if loaded.Name() == declared.Name && satisfies(declared.Version, loaded.Metadata.Version) {
			return true
		}
	}
	return false
}

// satisfies reports whether version meets constraint, an empty constraint
// being met by any version.
func satisfies(constraint, version string) bool {
	if constraint == "" {
		return true
	}
	want, err := semver.NewConstraint(constraint)
	if err != nil {
		return false
	}
	got, err := semver.NewVersion(version)
	return err == nil && want.Check(got)
}

// localChartDirectories returns the chart's directory dir and the
// directories its file:// dependencies name, which Helm resolves relative
// to the chart unless absolute.
func localChartDirectories(dir string, c *chart.Chart) []string {
	dirs := []string{dir}
	for _, dependency := range c.Metadata.Dependencies {
		path, ok := strings.CutPrefix(dependency.Repository, "file://")
		if !ok {
			continue
		}
		if !filepath.IsAbs(path) {
			path = filepath.Join(dir, path)
		}
		dirs = append(dirs, filepath.Clean(path))
	}
	return dirs
}

// scratchPath mirrors the absolute path dir below scratchDir, so that the
// relative paths between copied charts still hold.
func scratchPath(scratchDir, dir string) string {
	return filepath.Join(scratchDir, strings.TrimPrefix(dir, filepath.VolumeName(dir)))
}

// copyDirectory copies the tree at src to dst, following symbolic links so
// that the copy stands on its own, and leaving .git directories out.
func copyDirectory(src, dst string) error {
	return filepath.WalkDir(src, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if entry.Type()&fs.ModeSymlink != 0 {
			resolved, err := filepath.EvalSymlinks(path)
			if err != nil {
				return err
			}
			info, err := os.Stat(resolved)
			if err != nil {
				return err
			}
			if info.IsDir() {
				return copyDirectory(resolved, target)
			}
			return copyFile(resolved, target, info.Mode().Perm())
		}
		if entry.IsDir() {
			if entry.Name() == ".git" && path != src {
				return filepath.SkipDir
			}
			return os.MkdirAll(target, 0o755)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		return copyFile(path, target, info.Mode().Perm())
	})
}

// copyFile copies the regular file at src to dst with perm.
func copyFile(src, dst string, perm fs.FileMode) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, perm)
}

// writeRepositories writes a Helm repositories.yaml at path with an entry
// per URL, named after a hash of the URL as `helm dependency update` names
// the repositories it adds for itself.
func writeRepositories(path string, urls []string) error {
	file := repo.NewFile()
	for _, url := range urls {
		file.Update(&repo.Entry{Name: fmt.Sprintf("%x", sha256.Sum256([]byte(url))), URL: url})
	}
	return file.WriteFile(path, 0o644)
}
