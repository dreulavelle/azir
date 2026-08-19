package main

import (
	"strings"
	"testing"
)

/*
What the phone system refused, in words.

The fixture is the real thing: five extensions were given one email address in
a single bulk edit, the first took it and the other four came back with this.
It reached the screen whole, JSON and all, four times over.
*/
const refused = `{"error":{"code":"","message":"EmailAddress:\nWARNINGS.XAPI.ALREADY_IN_USE",` +
	`"details":[{"code":"","message":"WARNINGS.XAPI.ALREADY_IN_USE","target":"EmailAddress"}]}}`

func TestARefusalReadsAsASentence(t *testing.T) {
	got := refusal([]byte(refused))

	if strings.Contains(got, "{") || strings.Contains(got, "WARNINGS") {
		t.Errorf("the envelope reached the screen: %q", got)
	}
	if !strings.Contains(got, "email") {
		t.Errorf("%q does not say which setting was refused", got)
	}
	if !strings.Contains(got, "already uses") {
		t.Errorf("%q does not say what was wrong with it", got)
	}
}

// Something this has never seen is passed through rather than replaced with a
// shrug. A message nobody can read still beats one that says nothing.
func TestAnUnknownRefusalIsNotSwallowed(t *testing.T) {
	got := refusal([]byte(`{"error":{"message":"Mobile:\nSOMETHING_NEW"}}`))
	if !strings.Contains(got, "SOMETHING_NEW") {
		t.Errorf("%q dropped the only fact it had", got)
	}
	if !strings.Contains(strings.ToLower(got), "mobile") {
		t.Errorf("%q dropped which setting it was about", got)
	}
}

// A body that is not the envelope at all — an HTML error page, a proxy — still
// has to come back as something.
func TestARefusalThatIsNotJSON(t *testing.T) {
	got := refusal([]byte("<html>502 Bad Gateway</html>"))
	if got == "" {
		t.Fatal("a refusal came back with nothing said about it")
	}
	if !strings.Contains(got, "502") {
		t.Errorf("%q dropped what the phone system actually said", got)
	}
}
