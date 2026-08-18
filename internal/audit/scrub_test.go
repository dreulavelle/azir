package audit

import "testing"

/*
Somebody else's error message does not get to put a secret in the log.

The case this was written for is real and came out of an audit of a running
deployment: a model provider refused a request on billing and included a link
to the key's management page, and the whole sentence went into an audit row at
full length. The hex on the end of that link was not the key — but nothing had
looked, and nothing would have looked if it had been.
*/
func TestDetailsAreScrubbedAndCapped(t *testing.T) {
	// Values are synthetic. The scrubber matches on shape, and a real
	// identifier in a fixture is a real identifier in the history forever.
	cases := []struct {
		name, in string
		gone     string
		kept     string
	}{
		{
			name: "the row that prompted this",
			in:   "assistant: the model provider refused on billing: [402]: This request requires more credits. To increase, visit https://openrouter.ai/workspaces/default/keys/a1b2c3d4e5f60718293a4b5c6d7e8f90a1b2c3d4e5f60718293a4b5c6d7e8f90 and adjust the key's weekly limit",
			gone: "a1b2c3d4e5f60718293a4b5c6d7e8f90a1b2c3d4e5f60718293a4b5c6d7e8f90",
			kept: "refused on billing",
		},
		{name: "bearer token", in: "upstream said: Authorization: Bearer sk-or-v1-abcdefghijklmnopqrstuvwxyz012345", gone: "abcdefghijklmnopqrstuvwxyz012345"},
		{name: "labelled key", in: "connection failed, api_key=9f8e7d6c5b4a39281706fedcba098765", gone: "9f8e7d6c5b4a39281706fedcba098765"},
		{name: "base64 blob", in: "rejected: dGhpcyBpcyBhIHZlcnkgbG9uZyBzZWNyZXQgdmFsdWUgaW5kZWVk=", gone: "dGhpcyBpcyBhIHZlcnkgbG9uZyBzZWNyZXQgdmFsdWUgaW5kZWVk"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := scrub(c.in)
			if c.gone != "" && contains(got, c.gone) {
				t.Errorf("kept something that should not be there:\n%s", got)
			}
			if c.kept != "" && !contains(got, c.kept) {
				t.Errorf("threw away the part worth reading:\n%s", got)
			}
			if len(got) > detailLimit+8 {
				t.Errorf("still %d bytes long", len(got))
			}
		})
	}
}

// The ordinary case must survive untouched, or every row becomes unreadable in
// exchange for a problem that was only in a few of them.
func TestOrdinaryDetailsAreLeftAlone(t *testing.T) {
	for _, plain := range []string{
		"captures 14 days, cached answers 7 days",
		"senior-tech: tool.read, ticket.comment, phone.manage",
		"Conversations, Diagnostic captures, Ticket memory",
		"syncro work_items.search",
		"someone@example.com",
		"",
	} {
		if got := scrub(plain); got != plain {
			t.Errorf("changed an ordinary detail:\n  in:  %q\n  out: %q", plain, got)
		}
	}
}

func contains(haystack, needle string) bool {
	return len(needle) > 0 && len(haystack) >= len(needle) &&
		func() bool {
			for i := 0; i+len(needle) <= len(haystack); i++ {
				if haystack[i:i+len(needle)] == needle {
					return true
				}
			}
			return false
		}()
}
