package api

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strings"
)

// Below this, compression costs more in headers and CPU than it saves.
const worthCompressing = 1024

// squeezed is one asset, held both ways.
type squeezed struct {
	gzipped []byte
	etag    string
	mime    string
}

/*
compress builds gzipped copies of the embedded assets, once, at startup.

The asset set is fixed when the binary is built and it is small — under two
megabytes — so compressing on every request would burn CPU to produce
byte-for-byte the same answer each time. Doing it once at the best available
level costs a few hundred milliseconds of start-up and about two hundred
kilobytes of memory, and it is the difference between shipping 690KB of
JavaScript to every cold browser and shipping 207KB.

It is worth being blunt about why this exists: http.FileServer does not
compress, and nothing in the stack in front of it did either, so the frontend
was going over the wire at three and a half times its necessary size.
*/
func compress(assets fs.FS) map[string]squeezed {
	out := map[string]squeezed{}
	if assets == nil {
		return out
	}

	_ = fs.WalkDir(assets, ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil || info.Size() < worthCompressing {
			return nil
		}
		// Fonts and images arrive compressed. Running them through gzip again
		// spends CPU to make them very slightly larger.
		switch strings.ToLower(path.Ext(name)) {
		case ".js", ".css", ".html", ".svg", ".json", ".map", ".txt", ".xml":
		default:
			return nil
		}

		raw, err := fs.ReadFile(assets, name)
		if err != nil {
			return nil
		}

		var buf bytes.Buffer
		zw, err := gzip.NewWriterLevel(&buf, gzip.BestCompression)
		if err != nil {
			return nil
		}
		if _, err := zw.Write(raw); err != nil {
			return nil
		}
		if err := zw.Close(); err != nil {
			return nil
		}
		// A file that does not shrink is kept uncompressed rather than served
		// larger than it started.
		if buf.Len() >= len(raw) {
			return nil
		}

		sum := sha256.Sum256(raw)
		contentType := mime.TypeByExtension(path.Ext(name))
		if contentType == "" {
			contentType = "application/octet-stream"
		}
		out[name] = squeezed{
			gzipped: buf.Bytes(),
			etag:    `"` + hex.EncodeToString(sum[:16]) + `"`,
			mime:    contentType,
		}
		return nil
	})
	return out
}

// SPA serves the embedded frontend. Unknown paths fall back to index.html so
// client-side routes survive a refresh, but /api and /healthz are never
// shadowed — a mistyped API path must 404 rather than silently return HTML,
// which is a genuinely confusing failure to debug.
func SPA(assets fs.FS) http.Handler {
	fileServer := http.FileServer(http.FS(assets))
	gzipped := compress(assets)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/")
		if name == "" {
			name = "index.html"
		}

		if f, err := assets.Open(name); err == nil {
			f.Close()
			// Hashed build assets are immutable; index.html must not be, or a
			// deploy leaves browsers on a stale shell.
			if strings.HasPrefix(name, "assets/") {
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			} else {
				w.Header().Set("Cache-Control", "no-cache")
			}
			if serveGzip(w, r, gzipped[name]) {
				return
			}
			fileServer.ServeHTTP(w, r)
			return
		}

		// A build asset that does not exist is a mistake, not a route.
		//
		// Falling through to the shell answered a missing stylesheet with HTML
		// and a 200, so the browser tried to parse a page as CSS and reported
		// something unrelated. The same reasoning that keeps /api out of the
		// fallback applies here.
		if strings.HasPrefix(name, "assets/") {
			http.NotFound(w, r)
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

// serveGzip answers with the pre-compressed copy, and reports whether it did.
func serveGzip(w http.ResponseWriter, r *http.Request, asset squeezed) bool {
	if asset.gzipped == nil || !acceptsGzip(r.Header.Get("Accept-Encoding")) {
		return false
	}

	// Vary regardless of what this particular request accepted: a shared cache
	// that stored the compressed answer and replayed it to a client which did
	// not ask for gzip would serve unreadable bytes.
	w.Header().Set("Vary", "Accept-Encoding")
	w.Header().Set("Content-Type", asset.mime)
	w.Header().Set("ETag", asset.etag)

	if match := r.Header.Get("If-None-Match"); match != "" && strings.Contains(match, asset.etag) {
		w.WriteHeader(http.StatusNotModified)
		return true
	}

	w.Header().Set("Content-Encoding", "gzip")
	if r.Method == http.MethodHead {
		w.WriteHeader(http.StatusOK)
		return true
	}
	_, _ = io.Copy(w, bytes.NewReader(asset.gzipped))
	return true
}

// acceptsGzip reports whether the client asked for it, without being fooled by
// a header that lists it only to refuse it.
func acceptsGzip(header string) bool {
	for _, part := range strings.Split(header, ",") {
		name, params, _ := strings.Cut(strings.TrimSpace(part), ";")
		if !strings.EqualFold(strings.TrimSpace(name), "gzip") {
			continue
		}
		// "gzip;q=0" means the client would rather have it uncompressed.
		if strings.Contains(strings.ReplaceAll(params, " ", ""), "q=0") &&
			!strings.Contains(strings.ReplaceAll(params, " ", ""), "q=0.") {
			return false
		}
		return true
	}
	return false
}
