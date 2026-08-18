package audit

import (
	"regexp"
	"strings"
)

/*
What a detail line is allowed to carry.

The log records what happened, and quite a lot of what happened is somebody
else's error message repeated verbatim — a helpdesk's rejection, a phone
system's complaint, a model provider's billing refusal. Those messages are
written by people who have no idea their text is being stored, and they are
generous with what they put in one.

The row that prompted this was a provider saying a request could not be
afforded, with a link to the key's management page in it. The hex on the end of
that link is not the key, but the shape of the problem is plain: a third party's
text went into an audit log at full length with nothing looking at it. A
provider that echoed part of a credential, or a fragment of a customer's data,
into an error string would have put it here.

So details are capped and anything shaped like a secret is taken out. This is
not a substitute for not logging secrets in the first place — Azir does not
pass them to the recorder — it is the net under text that came from somewhere
else.
*/

// detailLimit is as much of a message as is worth keeping.
//
// Long enough for a sentence of explanation and the identifiers around it;
// short enough that a provider returning a page of prose does not put a page of
// prose in an audit row.
const detailLimit = 300

// secrets are the shapes worth removing wherever they appear.
var secrets = []*regexp.Regexp{
	// Bearer and API-key prefixes used by the services Azir talks to, plus the
	// generic "sk-" family.
	regexp.MustCompile(`(?i)\b(bearer\s+)[A-Za-z0-9._\-]{16,}`),
	regexp.MustCompile(`\bsk-[A-Za-z0-9._\-]{16,}`),
	regexp.MustCompile(`(?i)\b(x?api[_-]?key|secret|token|password)\s*[=:]\s*\S+`),
	// A long unbroken run of hex or base64 is not something a person wrote.
	// Keys, hashes and opaque identifiers all look like this; ordinary English
	// does not.
	regexp.MustCompile(`\b[A-Fa-f0-9]{32,}\b`),
	regexp.MustCompile(`\b[A-Za-z0-9+/]{40,}={0,2}\b`),
}

// scrub trims a detail line to what is safe and useful to keep.
func scrub(detail string) string {
	if detail == "" {
		return ""
	}
	for _, shape := range secrets {
		detail = shape.ReplaceAllStringFunc(detail, func(match string) string {
			// Keep the label, drop the value: "api_key=…" says more than "…"
			// and still says nothing anybody can use.
			if i := strings.IndexAny(match, "=:"); i > 0 {
				return match[:i+1] + " [removed]"
			}
			if lower := strings.ToLower(match); strings.HasPrefix(lower, "bearer") {
				return "Bearer [removed]"
			}
			return "[removed]"
		})
	}
	if len(detail) > detailLimit {
		// Cut on a rune boundary so a truncated message is still text.
		cut := detail[:detailLimit]
		for len(cut) > 0 && !isBoundary(detail[len(cut)]) && len(detail) > len(cut) {
			cut = cut[:len(cut)-1]
			if len(detail)-len(cut) > 8 {
				cut = detail[:detailLimit]
				break
			}
		}
		detail = strings.TrimSpace(cut) + "…"
	}
	return detail
}

// isBoundary reports whether a byte starts a new UTF-8 rune.
func isBoundary(b byte) bool { return b&0xC0 != 0x80 }
