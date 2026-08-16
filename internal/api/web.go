package api

import (
	"io/fs"
	"net/http"
	"strings"
)

// SPA serves the embedded frontend. Unknown paths fall back to index.html so
// client-side routes survive a refresh, but /api and /healthz are never
// shadowed — a mistyped API path must 404 rather than silently return HTML,
// which is a genuinely confusing failure to debug.
func SPA(assets fs.FS) http.Handler {
	fileServer := http.FileServer(http.FS(assets))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/")
		if path == "" {
			path = "index.html"
		}

		if f, err := assets.Open(path); err == nil {
			f.Close()
			// Hashed build assets are immutable; index.html must not be, or a
			// deploy leaves browsers on a stale shell.
			if strings.HasPrefix(path, "assets/") {
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			} else {
				w.Header().Set("Cache-Control", "no-cache")
			}
			fileServer.ServeHTTP(w, r)
			return
		}

		index, err := fs.ReadFile(assets, "index.html")
		if err != nil {
			http.Error(w, "frontend assets are not built into this binary", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = w.Write(index)
	})
}
