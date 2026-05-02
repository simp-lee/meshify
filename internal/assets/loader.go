package assets

import (
	"fmt"
	"io/fs"
	"path"
	"slices"
	"strings"
)

type Loader struct {
	source fs.FS
}

func NewLoader() Loader {
	return Loader{source: embeddedFS}
}

func (loader Loader) Read(sourcePath string) ([]byte, error) {
	normalizedPath := normalizeSourcePath(sourcePath)
	data, err := fs.ReadFile(loader.source, normalizedPath)
	if err != nil {
		return nil, fmt.Errorf("read embedded asset %q: %w", normalizedPath, err)
	}
	return data, nil
}

func (loader Loader) List() ([]string, error) {
	var paths []string
	err := fs.WalkDir(loader.source, ".", func(currentPath string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		paths = append(paths, normalizeSourcePath(currentPath))
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("list embedded assets: %w", err)
	}

	slices.Sort(paths)
	return paths, nil
}

func normalizeSourcePath(sourcePath string) string {
	normalizedPath := path.Clean(strings.TrimPrefix(sourcePath, "/"))
	if normalizedPath == "." {
		return ""
	}
	return normalizedPath
}
