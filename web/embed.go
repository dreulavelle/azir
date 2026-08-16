// Package web carries the built frontend so the binary serves its own UI.
//
// The dist directory is produced by `npm run build` and is not committed. A
// developer build without it still compiles: Assets reports that the frontend
// is missing and core serves the API alone, which is what `go run` should do
// rather than failing to build at all.
package web

import (
	"embed"
	"errors"
	"io/fs"
)

//go:embed all:dist
var dist embed.FS

// ErrNotBuilt means the frontend was never built into this binary.
var ErrNotBuilt = errors.New("web: frontend not built; run `npm --prefix web run build`")

// Assets returns the built frontend rooted at dist.
func Assets() (fs.FS, error) {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		return nil, ErrNotBuilt
	}
	// A dist directory holding only the placeholder is not a real build.
	if _, err := fs.Stat(sub, "index.html"); err != nil {
		return nil, ErrNotBuilt
	}
	return sub, nil
}
