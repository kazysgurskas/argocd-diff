package hydrate

import (
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/kazysgurskas/argocd-diff/internal/application"
)

// renderDirectory joins the .yaml files under source.Path, relative to
// opts.Root, into one stream, each file as its own document, in path order.
// Subdirectories count only when the source's directory block asks to
// recurse.
func renderDirectory(source *application.Source, opts Options) (string, error) {
	root := filepath.Join(opts.Root, source.Path)
	info, err := os.Stat(root)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%s is not a directory", root)
	}
	recurse := source.Directory != nil && source.Directory.Recurse

	var paths []string
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != root && !recurse {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(d.Name(), ".yaml") {
			paths = append(paths, path)
		}
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("walk %s: %w", root, err)
	}
	sort.Strings(paths)
	slog.Info("rendering directory", "path", root, "files", len(paths), "recurse", recurse)

	documents := make([]string, 0, len(paths))
	for _, path := range paths {
		content, err := os.ReadFile(path)
		if err != nil {
			return "", err
		}
		doc := strings.TrimSpace(string(content))
		if !strings.HasPrefix(doc, "---") {
			doc = "---\n" + doc
		}
		documents = append(documents, doc)
	}
	return strings.Join(documents, "\n"), nil
}
