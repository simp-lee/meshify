package deployembed

import "embed"

// Files contains the human-maintained deploy source tree for build-time embedding.
//
//go:embed deploy
var Files embed.FS
