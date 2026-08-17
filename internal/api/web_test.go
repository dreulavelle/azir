package api

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

// A build's worth of assets, big enough to be worth compressing.
func builtAssets() fstest.MapFS {
	return fstest.MapFS{
		"index.html": &fstest.MapFile{Data: []byte("<!doctype html><div id=root></div>")},
		"assets/index-abc123.js": &fstest.MapFile{
			Data: []byte(strings.Repeat("export const thing = 1;\n", 400)),
		},
		"assets/index-abc123.css": &fstest.MapFile{
			Data: []byte(strings.Repeat(".a{color:red}\n", 400)),
		},
		"assets/font-abc123.woff2": &fstest.MapFile{
			Data: bytes.Repeat([]byte{0x77, 0x4f, 0x46, 0x32}, 500),
		},
	}
}

func get(t *testing.T, handler http.Handler, path, encoding string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, path, nil)
	if encoding != "" {
		r.Header.Set("Accept-Encoding", encoding)
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	return w
}

// The whole point. Serving the frontend uncompressed was costing three and a
// half times the bytes on every cold load.
func TestAssetsAreCompressed(t *testing.T) {
	assets := builtAssets()
	handler := SPA(assets)

	w := get(t, handler, "/assets/index-abc123.js", "gzip, deflate, br")
	if got := w.Header().Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("Content-Encoding = %q, want gzip", got)
	}
	if w.Header().Get("Vary") != "Accept-Encoding" {
		t.Error("no Vary header; a shared cache could serve these bytes to a client that cannot read them")
	}

	raw := assets["assets/index-abc123.js"].Data
	if w.Body.Len() >= len(raw) {
		t.Errorf("compressed to %d bytes from %d — no saving", w.Body.Len(), len(raw))
	}

	// And it has to actually decompress back to the file.
	zr, err := gzip.NewReader(bytes.NewReader(w.Body.Bytes()))
	if err != nil {
		t.Fatalf("body is not valid gzip: %v", err)
	}
	back, err := io.ReadAll(zr)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(back, raw) {
		t.Error("decompressed body does not match the file it came from")
	}
}

// A client that cannot read gzip must still get a working file.
func TestUncompressedClientStillWorks(t *testing.T) {
	assets := builtAssets()
	w := get(t, SPA(assets), "/assets/index-abc123.js", "")

	if got := w.Header().Get("Content-Encoding"); got != "" {
		t.Errorf("Content-Encoding = %q for a client that did not ask for it", got)
	}
	if !bytes.Equal(w.Body.Bytes(), assets["assets/index-abc123.js"].Data) {
		t.Error("body does not match the file")
	}
}

// "gzip;q=0" is a client saying it would rather not. Reading it as acceptance
// sends bytes the client has told you it cannot use.
func TestGzipRefusalIsHonoured(t *testing.T) {
	handler := SPA(builtAssets())

	if w := get(t, handler, "/assets/index-abc123.js", "gzip;q=0"); w.Header().Get("Content-Encoding") == "gzip" {
		t.Error("compressed for a client that refused gzip")
	}
	// But a weighted preference is still acceptance.
	if w := get(t, handler, "/assets/index-abc123.js", "gzip;q=0.5"); w.Header().Get("Content-Encoding") != "gzip" {
		t.Error("did not compress for a client that accepts gzip at q=0.5")
	}
}

// Fonts arrive compressed. Running them through gzip spends CPU to make them
// marginally bigger.
func TestAlreadyCompressedFormatsAreLeftAlone(t *testing.T) {
	w := get(t, SPA(builtAssets()), "/assets/font-abc123.woff2", "gzip")
	if got := w.Header().Get("Content-Encoding"); got == "gzip" {
		t.Error("re-compressed a woff2")
	}
}

// A second visit should cost a 304 and no body.
func TestUnchangedAssetRevalidates(t *testing.T) {
	handler := SPA(builtAssets())

	first := get(t, handler, "/assets/index-abc123.css", "gzip")
	etag := first.Header().Get("ETag")
	if etag == "" {
		t.Fatal("no ETag, so nothing can be revalidated")
	}

	r := httptest.NewRequest(http.MethodGet, "/assets/index-abc123.css", nil)
	r.Header.Set("Accept-Encoding", "gzip")
	r.Header.Set("If-None-Match", etag)
	second := httptest.NewRecorder()
	handler.ServeHTTP(second, r)

	if second.Code != http.StatusNotModified {
		t.Errorf("status = %d, want 304", second.Code)
	}
	if second.Body.Len() != 0 {
		t.Errorf("304 carried %d bytes of body", second.Body.Len())
	}
}

/*
A missing build asset must be a 404.

Falling through to the shell answered a missing stylesheet with HTML and a 200,
so the browser tried to parse a page as CSS and reported something unrelated to
the actual problem. The same reasoning that keeps /api out of the SPA fallback
applies to /assets.
*/
func TestMissingAssetIsNotFound(t *testing.T) {
	w := get(t, SPA(builtAssets()), "/assets/index-DOESNOTEXIST.css", "gzip")

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
	if strings.Contains(w.Body.String(), "<div id=root>") {
		t.Error("answered a missing asset with the application shell")
	}
}

// A client-side route still has to survive a refresh.
func TestUnknownRouteServesTheShell(t *testing.T) {
	w := get(t, SPA(builtAssets()), "/tickets/4233", "gzip")

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
	if !strings.Contains(w.Body.String(), "<div id=root>") {
		t.Error("a client-side route did not get the shell")
	}
}

// index.html must stay revalidated or a deploy leaves browsers on a stale
// shell pointing at assets that no longer exist.
func TestShellIsNotCachedForever(t *testing.T) {
	for _, path := range []string{"/", "/tickets/4233"} {
		w := get(t, SPA(builtAssets()), path, "gzip")
		if got := w.Header().Get("Cache-Control"); !strings.Contains(got, "no-cache") {
			t.Errorf("%s Cache-Control = %q, want no-cache", path, got)
		}
	}
}

func TestHashedAssetsAreImmutable(t *testing.T) {
	w := get(t, SPA(builtAssets()), "/assets/index-abc123.js", "gzip")
	if got := w.Header().Get("Cache-Control"); !strings.Contains(got, "immutable") {
		t.Errorf("Cache-Control = %q, want immutable", got)
	}
}
