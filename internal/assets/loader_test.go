package assets

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestLoaderListMatchesDeployTree(t *testing.T) {
	t.Parallel()

	loader := NewLoader()
	got, err := loader.List()
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}

	want := deployTreePaths(t)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("List() mismatch\n got: %v\nwant: %v", got, want)
	}
}

func TestLoaderReadReturnsEmbeddedContent(t *testing.T) {
	t.Parallel()

	loader := NewLoader()
	got, err := loader.Read("templates/etc/headscale/policy.hujson")
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}

	want, err := os.ReadFile(filepath.Join("..", "..", "deploy", "templates", "etc", "headscale", "policy.hujson"))
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}

	if !bytes.Equal(got, want) {
		t.Fatal("Read() content mismatch")
	}
}
