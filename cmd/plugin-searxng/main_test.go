package main

import "testing"

// The allowed-domain check is a security boundary, not a filter: a host that
// merely contains an allowed domain must not pass, because
// "microsoft.com.evil.example" is somebody else's server.
func TestPermitted(t *testing.T) {
	allow := []string{"microsoft.com", "support.apc.com"}

	cases := map[string]bool{
		"microsoft.com":           true,
		"learn.microsoft.com":     true,
		"support.apc.com":         true,
		"microsoft.com.evil.test": false,
		"notmicrosoft.com":        false,
		"evil.test":               false,
		"apc.com":                 false,
		"":                        false,
		"xsupport.apc.com":        false,
	}

	for host, want := range cases {
		if got := permitted(host, allow); got != want {
			t.Errorf("permitted(%q) = %v, want %v", host, got, want)
		}
	}
}

func TestDomainListIgnoresBlanks(t *testing.T) {
	got := domainList(" Microsoft.com , ,support.apc.com ,")
	if len(got) != 2 || got[0] != "microsoft.com" || got[1] != "support.apc.com" {
		t.Errorf("domainList produced %q", got)
	}
}
