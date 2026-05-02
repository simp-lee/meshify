package assets

import (
	"fmt"
	"io/fs"

	deployembed "meshify"
)

var embeddedFS = mustSubFS(deployembed.Files, "deploy")

func mustSubFS(source fs.FS, dir string) fs.FS {
	subtree, err := fs.Sub(source, dir)
	if err != nil {
		panic(fmt.Sprintf("assets: embed subtree %q: %v", dir, err))
	}
	return subtree
}
