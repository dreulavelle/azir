package api

import (
	"net/http"
	"strings"
)

/*
The headers every response carries.

Azir is reached on a public name and everything of value is behind the sign-in,
so the browser is the last place a mistake can still be contained. These are set
in one wrapper around the whole mux rather than per-route, for the same reason
permissions are: a header that has to be remembered is a header that will
eventually be forgotten, and the forgetting is invisible until it matters.

Nothing here replaces a check on the server. They narrow what a bug elsewhere
could be turned into.
*/

// contentSecurityPolicy is what the page is allowed to load and where it may
// send anything.
//
// The frontend is served from the same binary on the same origin as the API, so
// almost everything can be 'self' — there is no CDN to allow and no second
// origin to authorise.
var contentSecurityPolicy = strings.Join([]string{
	// Everything not named below comes from this origin or not at all.
	"default-src 'self'",

	// Scripts are the directive that matters, and it has no 'unsafe-inline'
	// and no 'unsafe-eval'. Vite emits a linked module and nothing else, so
	// injected script has nowhere to run even if something managed to write
	// it into the page.
	"script-src 'self'",

	// Styles need 'unsafe-inline' and there is no way around it: Radix
	// positions every dialog, select and tooltip by writing a style attribute
	// at runtime, and the app sets a few of its own. Inline style is a far
	// smaller weapon than inline script, and it is the trade the component
	// library imposes.
	"style-src 'self' 'unsafe-inline'",

	// Images from here and from data: URIs — the favicon is drawn as an SVG
	// data URI from the branding accent.
	//
	// Remote hosts are deliberately absent. The assistant renders its answers
	// as markdown, and an image is the classic way to make a page reach out:
	// a model that emitted ![](https://elsewhere/?q=<something it had read>)
	// would be asking the browser to carry it there. It cannot, because the
	// browser is not allowed to fetch it.
	"img-src 'self' data:",

	// Typefaces are bundled into the build.
	"font-src 'self'",

	// The API and the event stream, both of which are this origin.
	"connect-src 'self'",

	// No plugins, no framing of anything, and no <base> to quietly repoint
	// every relative URL on the page.
	"object-src 'none'",
	"frame-src 'none'",
	"base-uri 'none'",

	// Every form in the console is handled in JavaScript and submits nowhere.
	// The single sign-on button is a link, not a form, so this does not touch
	// the trip to the identity provider.
	"form-action 'self'",

	// Not embeddable. X-Frame-Options below says the same thing for browsers
	// that predate this directive.
	"frame-ancestors 'none'",
}, "; ")

// secured wraps the mux with the headers above.
func secured(next http.Handler, proxies ProxyTrust) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", contentSecurityPolicy)

		// Answer the declared type or nothing. Without this a browser is free
		// to sniff an uploaded logo as HTML and run what it finds inside.
		h.Set("X-Content-Type-Options", "nosniff")

		// Clickjacking: nobody frames a console that can approve a change to
		// somebody's phone system.
		h.Set("X-Frame-Options", "DENY")

		// A ticket id or a customer name is in the path often enough that no
		// URL from here should leave on an outbound request.
		h.Set("Referrer-Policy", "same-origin")

		// None of this is a helpdesk's business.
		h.Set("Permissions-Policy",
			"camera=(), microphone=(), geolocation=(), payment=(), usb=()")

		// A window opened from here does not get a handle back to this one.
		h.Set("Cross-Origin-Opener-Policy", "same-origin")

		// Only over TLS, and only for this host.
		//
		// The header is ignored on a plain-HTTP response by specification, so
		// sending it always would be harmless but dishonest. includeSubDomains
		// is deliberately absent: it would pin every other name under the same
		// domain to HTTPS as well, and Azir has no idea what else is there.
		if proxies.overTLS(r) {
			h.Set("Strict-Transport-Security", "max-age=31536000")
		}

		next.ServeHTTP(w, r)
	})
}
