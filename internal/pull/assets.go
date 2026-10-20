package pull

import (
	"embed"
	"fmt"
)

// assets holds the kelper-fetch binaries, one per node architecture, which
// `make` builds into assets/ before compiling kelper. The directory always
// contains README.md so the package compiles from a fresh clone; a binary
// built that way reports the missing fetcher at runtime.
//
//go:embed assets
var assets embed.FS

// fetcherBinary returns the fetcher for a linux node architecture.
func fetcherBinary(arch string) ([]byte, error) {
	b, err := assets.ReadFile("assets/kelper-fetch-linux-" + arch)
	if err != nil {
		return nil, fmt.Errorf("this kelper binary has no fetcher for linux/%s nodes (build kelper with `make build` to embed it)", arch)
	}
	return b, nil
}
