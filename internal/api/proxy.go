package api

import (
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strings"
)

/*
Whether to believe what a request says about where it came from.

X-Forwarded-For and X-Forwarded-Proto are ordinary request headers. Anyone who
can reach Azir can send them, saying anything. Believing them unconditionally
means the address written into somebody's audit trail is the address they chose
to write there, and — worse — that a per-address limit on sign-in attempts is
one header away from being no limit at all.

So the headers are believed only when the connection they arrived on came from
a proxy the operator named. Unset means believe nothing, which is right for the
default deployment: Azir binds to loopback and is reached directly.

Configured with AZIR_TRUSTED_PROXIES, a comma-separated list of addresses or
CIDR blocks — the proxy, tunnel or load balancer in front. A deployment behind
one that does not set it does not become insecure; it records the proxy's
address for every actor, which is useless but never wrong. That is the right
way round: an operator notices every audit row saying 172.18.0.1 far sooner
than they notice an attacker choosing their own.
*/
type ProxyTrust struct {
	nets []netip.Prefix
}

// TrustedProxies reads the operator's list, as set in AZIR_TRUSTED_PROXIES.
// A bare address is taken as itself — /32 or /128 — so the common case of one
// proxy in front needs no mask. An empty spec trusts nothing, which is the
// zero value and the default.
func TrustedProxies(spec string) (ProxyTrust, error) {
	var t ProxyTrust
	for _, field := range strings.Split(spec, ",") {
		field = strings.TrimSpace(field)
		if field == "" {
			continue
		}
		if prefix, err := netip.ParsePrefix(field); err == nil {
			t.nets = append(t.nets, prefix.Masked())
			continue
		}
		addr, err := netip.ParseAddr(field)
		if err != nil {
			return ProxyTrust{}, fmt.Errorf("api: %q is not an address or CIDR block", field)
		}
		t.nets = append(t.nets, netip.PrefixFrom(addr, addr.BitLen()))
	}
	return t, nil
}

// trusts reports whether a connection arrived from a named proxy.
func (t ProxyTrust) trusts(remoteAddr string) bool {
	if len(t.nets) == 0 {
		return false
	}
	addr, ok := parseHostAddr(remoteAddr)
	if !ok {
		return false
	}
	for _, n := range t.nets {
		if n.Contains(addr) {
			return true
		}
	}
	return false
}

/*
clientIP is the address to attribute a request to.

From a trusted proxy, the rightmost entry in X-Forwarded-For that is not itself
a trusted proxy. Rightmost rather than leftmost, which is the part that is easy
to get wrong: the leftmost entry is whatever the original client claimed, and a
client that sends its own X-Forwarded-For has that value preserved at the front
of the list by every proxy it passes through. Walking from the right stops at
the last hop Azir has a reason to believe.

From anywhere else, the address the connection actually came from.
*/
func (t ProxyTrust) clientIP(r *http.Request) string {
	direct := remoteHost(r.RemoteAddr)
	if !t.trusts(r.RemoteAddr) {
		return direct
	}

	forwarded := r.Header.Get("X-Forwarded-For")
	if forwarded == "" {
		return direct
	}

	hops := strings.Split(forwarded, ",")
	for i := len(hops) - 1; i >= 0; i-- {
		hop := strings.TrimSpace(hops[i])
		addr, ok := parseHostAddr(hop)
		if !ok {
			// An unparseable hop is not something to attribute anything to,
			// and not something to keep walking past either: everything to
			// its left is written by whoever wrote it.
			return direct
		}
		if t.trusts(hop) {
			continue
		}
		return addr.String()
	}

	// Every hop was a trusted proxy. Nothing here identifies a client.
	return direct
}

// overTLS reports whether the browser's side of the connection was encrypted.
// Behind a trusted proxy the request arrives here over plain HTTP no matter how
// it started, so the proxy's account of the original scheme is all there is —
// but only a trusted proxy's account.
func (t ProxyTrust) overTLS(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	if !t.trusts(r.RemoteAddr) {
		return false
	}
	return strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

// remoteHost strips the port from a RemoteAddr, leaving the address as text
// even when it is not one this can parse.
func remoteHost(remoteAddr string) string {
	if host, _, err := net.SplitHostPort(remoteAddr); err == nil {
		return host
	}
	return remoteAddr
}

// parseHostAddr turns a RemoteAddr, a bare address or a bracketed IPv6 literal
// into an address, dropping any zone so that comparisons behave.
func parseHostAddr(s string) (netip.Addr, bool) {
	s = strings.TrimSpace(remoteHost(s))
	s = strings.TrimPrefix(strings.TrimSuffix(s, "]"), "[")
	addr, err := netip.ParseAddr(s)
	if err != nil {
		return netip.Addr{}, false
	}
	return addr.Unmap().WithZone(""), true
}
