package api

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"strings"

	"github.com/dreulavelle/azir/internal/audit"
	"github.com/dreulavelle/azir/internal/identity"
	"github.com/dreulavelle/azir/internal/store"
)

// Branding: whose product this is.
//
// Readable without signing in, deliberately. The sign-in page is the first
// thing anybody sees and it has to know what to call itself before it knows
// who is looking — a page that says "Azir" for a second and then renames
// itself has already told the visitor whose software it really is.
//
// Nothing here is sensitive: a name, a tagline, a colour and a picture the
// operator chose to put on a login screen. Serving it publicly leaks the same
// information as looking at the page.

// Defaults, used when a deployment has not overridden them. Kept here rather
// than in the database so an upgrade that improves them still reaches every
// deployment that never had an opinion.
const (
	defaultName    = "Azir"
	defaultMark    = "A"
	defaultTagline = "The assistant that already has the context"
	defaultAccent  = "#7c6bff"
)

// maxLogo is a generous ceiling for something rendered at 40 pixels. It exists
// so a mistaken upload of a print-resolution asset fails clearly rather than
// sitting in the database and on every page load forever.
const maxLogo = 512 << 10

type brandingView struct {
	store.Branding
	// The resolved values, so no caller has to know the fallback rules.
	EffectiveName    string `json:"effective_name"`
	EffectiveMark    string `json:"effective_mark"`
	EffectiveTagline string `json:"effective_tagline"`
	EffectiveAccent  string `json:"effective_accent"`
}

func resolve(b store.Branding) brandingView {
	v := brandingView{Branding: b}
	v.EffectiveName = firstNonBlank(b.Name, defaultName)
	// The mark falls back to the first letter of whatever the product is
	// called, so renaming to "Helios" gives an H without anybody being asked
	// for one.
	fallbackMark := defaultMark
	if name := strings.TrimSpace(v.EffectiveName); name != "" {
		fallbackMark = strings.ToUpper(name[:1])
	}
	v.EffectiveMark = firstNonBlank(b.Mark, fallbackMark)
	v.EffectiveTagline = firstNonBlank(b.Tagline, defaultTagline)
	v.EffectiveAccent = firstNonBlank(b.Accent, defaultAccent)
	return v
}

func firstNonBlank(values ...string) string {
	for _, v := range values {
		if v = strings.TrimSpace(v); v != "" {
			return v
		}
	}
	return ""
}

func (s *Server) getBranding(w http.ResponseWriter, r *http.Request) {
	b, err := s.DB.Branding(r.Context())
	if err != nil {
		// A deployment that cannot read its own branding should still be
		// usable: fall back rather than refusing to draw a sign-in page.
		s.Log.Warn("could not read branding", "error", err)
		writeJSON(w, http.StatusOK, resolve(store.Branding{}))
		return
	}
	writeJSON(w, http.StatusOK, resolve(b))
}

/*
getLogo serves the uploaded mark.

Revalidated rather than re-sent. The logo is the largest single thing this
application serves — a few hundred kilobytes is normal for something exported at
512 pixels square — and it was carrying a sixty second lifetime, so every
browser refetched all of it once a minute forever to be told nothing had
changed. An entity tag over the bytes answers the same question in a 304 with no
body at all, which keeps the "an operator changing their logo expects to see it
change" property that the short lifetime was protecting, without paying for it
on every single load.
*/
func (s *Server) getLogo(w http.ResponseWriter, r *http.Request) {
	image, kind, err := s.DB.Logo(r.Context())
	if err != nil {
		http.NotFound(w, r)
		return
	}

	sum := sha256.Sum256(image)
	etag := `"` + hex.EncodeToString(sum[:16]) + `"`
	w.Header().Set("ETag", etag)
	// must-revalidate rather than a lifetime: the browser always asks, and
	// almost always gets a 304 costing a few hundred bytes instead of hundreds
	// of kilobytes.
	w.Header().Set("Cache-Control", "public, no-cache, must-revalidate")

	if match := r.Header.Get("If-None-Match"); match != "" && strings.Contains(match, etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}

	w.Header().Set("Content-Type", kind)
	_, _ = w.Write(image)
}

func (s *Server) putBranding(w http.ResponseWriter, r *http.Request, actor identity.Actor) {
	var body store.Branding
	if err := decode(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, errBody("invalid json body"))
		return
	}

	body.Name = strings.TrimSpace(body.Name)
	body.Tagline = strings.TrimSpace(body.Tagline)
	body.Mark = strings.TrimSpace(body.Mark)
	body.Accent = strings.TrimSpace(body.Accent)

	// Stored empty when it matches what we ship, so a future release that
	// changes the default still reaches this deployment.
	if body.Name == defaultName {
		body.Name = ""
	}
	if body.Tagline == defaultTagline {
		body.Tagline = ""
	}
	if body.Accent != "" && !isHexColour(body.Accent) {
		writeJSON(w, http.StatusBadRequest, errBody("that is not a colour, such as #7c6bff"))
		return
	}
	if len([]rune(body.Mark)) > 2 {
		writeJSON(w, http.StatusBadRequest, errBody("the mark is one or two characters"))
		return
	}

	if err := s.DB.SetBranding(r.Context(), body, actor.Email); err != nil {
		writeJSON(w, http.StatusBadRequest, errBody(err.Error()))
		return
	}
	s.Audit.Record(r.Context(), audit.Event{
		ActorUserID: actor.Email, Action: "branding.change", Outcome: audit.OutcomeOK,
	})

	b, err := s.DB.Branding(r.Context())
	if err != nil {
		s.fail(w, err, "saved, but could not read it back")
		return
	}
	writeJSON(w, http.StatusOK, resolve(b))
}

// putLogo accepts an image, or clears it when sent nothing.
func (s *Server) putLogo(w http.ResponseWriter, r *http.Request, actor identity.Actor) {
	kind := r.Header.Get("Content-Type")

	image, err := io.ReadAll(io.LimitReader(r.Body, maxLogo+1))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errBody("that image could not be read"))
		return
	}
	if len(image) > maxLogo {
		writeJSON(w, http.StatusRequestEntityTooLarge, errBody(
			"that image is larger than 512KB; it is drawn at about 40 pixels"))
		return
	}

	if len(image) > 0 {
		// An allowlist rather than a sniff: this file is served back to every
		// browser that loads the sign-in page, and the one thing that must not
		// come back out of it is markup. SVG is deliberately absent — it can
		// carry script, and a logo is not worth that.
		switch kind {
		case "image/png", "image/jpeg", "image/webp", "image/gif":
		default:
			writeJSON(w, http.StatusBadRequest, errBody(
				"use a PNG, JPEG, WebP or GIF — SVG can carry code, so it is not accepted"))
			return
		}
	} else {
		kind = ""
	}

	if err := s.DB.SetLogo(r.Context(), image, kind, actor.Email); err != nil {
		s.fail(w, err, "could not store that image")
		return
	}
	s.Audit.Record(r.Context(), audit.Event{
		ActorUserID: actor.Email, Action: "branding.logo", Outcome: audit.OutcomeOK,
		Detail: kindOrCleared(kind),
	})
	writeJSON(w, http.StatusOK, map[string]bool{"has_logo": len(image) > 0})
}

func kindOrCleared(kind string) string {
	if kind == "" {
		return "cleared"
	}
	return kind
}

func isHexColour(s string) bool {
	if len(s) != 7 || s[0] != '#' {
		return false
	}
	for _, c := range s[1:] {
		if !strings.ContainsRune("0123456789abcdefABCDEF", c) {
			return false
		}
	}
	return true
}
