package assets

import (
	"io/fs"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
)

func TestCatalogMatchesDeployTree(t *testing.T) {
	t.Parallel()

	got := catalogSourcePaths(Catalog())
	want := deployTreePaths(t)

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Catalog() paths mismatch\n got: %v\nwant: %v", got, want)
	}
}

func TestRuntimeCatalogCarriesHostMetadata(t *testing.T) {
	t.Parallel()

	runtimeAssets := RuntimeCatalog()
	if len(runtimeAssets) != 4 {
		t.Fatalf("len(RuntimeCatalog()) = %d, want 4", len(runtimeAssets))
	}

	byPath := make(map[string]Asset, len(runtimeAssets))
	for _, asset := range runtimeAssets {
		byPath[asset.SourcePath] = asset
	}

	tests := []struct {
		sourcePath  string
		contentMode ContentMode
		hostPath    string
		mode        fs.FileMode
		activations []Activation
	}{
		{
			sourcePath:  "templates/etc/headscale/config.yaml.tmpl",
			contentMode: ContentModeRender,
			hostPath:    "/etc/headscale/config.yaml",
			mode:        0o600,
			activations: []Activation{ActivationRestartHeadscale},
		},
		{
			sourcePath:  "templates/etc/headscale/policy.hujson",
			contentMode: ContentModeCopy,
			hostPath:    "/etc/headscale/policy.hujson",
			mode:        0o644,
			activations: []Activation{ActivationRestartHeadscale},
		},
		{
			sourcePath:  "templates/etc/nginx/sites-available/headscale.conf.tmpl",
			contentMode: ContentModeRender,
			hostPath:    "/etc/nginx/sites-available/headscale.conf",
			mode:        0o644,
			activations: []Activation{ActivationReloadNginx},
		},
		{
			sourcePath:  "templates/etc/letsencrypt/renewal-hooks/deploy/reload-nginx.sh",
			contentMode: ContentModeCopy,
			hostPath:    "/etc/letsencrypt/renewal-hooks/deploy/reload-nginx.sh",
			mode:        0o755,
			activations: nil,
		},
	}

	for _, tt := range tests {
		testCase := tt
		t.Run(tt.sourcePath, func(t *testing.T) {
			t.Parallel()

			asset, ok := byPath[testCase.sourcePath]
			if !ok {
				t.Fatalf("RuntimeCatalog() missing %q", testCase.sourcePath)
			}

			if asset.ContentMode != testCase.contentMode {
				t.Fatalf("ContentMode = %q, want %q", asset.ContentMode, testCase.contentMode)
			}
			if asset.HostPath != testCase.hostPath {
				t.Fatalf("HostPath = %q, want %q", asset.HostPath, testCase.hostPath)
			}
			if asset.Mode != testCase.mode {
				t.Fatalf("Mode = %v, want %v", asset.Mode, testCase.mode)
			}
			if !slices.Equal(asset.Activations, testCase.activations) {
				t.Fatalf("Activations = %v, want %v", asset.Activations, testCase.activations)
			}
		})
	}
}

func catalogSourcePaths(catalog []Asset) []string {
	paths := make([]string, 0, len(catalog))
	for _, asset := range catalog {
		paths = append(paths, asset.SourcePath)
	}
	slices.Sort(paths)
	return paths
}

func deployTreePaths(t *testing.T) []string {
	t.Helper()

	root := filepath.Join("..", "..", "deploy")
	var paths []string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}

		relativePath, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		paths = append(paths, filepath.ToSlash(relativePath))
		return nil
	})
	if err != nil {
		t.Fatalf("WalkDir() error = %v", err)
	}

	slices.Sort(paths)
	return paths
}
