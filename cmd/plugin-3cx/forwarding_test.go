package main

import (
	"slices"
	"testing"
)

/*
Forwarding belongs to a status profile, and there are several.

This read and wrote only "Available", so changing a rule while looking at any
other profile in the console did nothing anybody could see — and every test
passed, because the read agreed with the write and both were looking at the
same wrong place.
*/
func TestAFieldNamesItsProfile(t *testing.T) {
	for _, c := range []struct{ field, profile, rule string }{
		{"Available/BusyInternal", "Available", "BusyInternal"},
		{"Away/BusyInternal", "Away", "BusyInternal"},
		{"Out of office/Internal", "Out of office", "Internal"},
		// A name with a space is ordinary; only the first slash separates.
		{"Custom 1/NoAnswerTimeout", "Custom 1", "NoAnswerTimeout"},
	} {
		profile, rule := profileOf(c.field)
		if profile != c.profile || rule != c.rule {
			t.Errorf("%q reads as %q/%q, want %q/%q", c.field, profile, rule, c.profile, c.rule)
		}
		if back := inProfile(c.profile, c.rule); back != c.field {
			t.Errorf("%q/%q writes as %q, want %q", c.profile, c.rule, back, c.field)
		}
	}

	// A sheet written before profiles existed named the default's rules bare,
	// and still maps to the default.
	if profile, rule := profileOf("BusyInternal"); profile != defaultProfile || rule != "BusyInternal" {
		t.Errorf("an unprefixed rule reads as %q/%q", profile, rule)
	}
}

// The allowlist has to admit a rule whichever profile it belongs to. It used to
// refuse every one that was not the default's, by name, as though the phone
// system had never heard of it.
func TestEveryProfilesRulesAreSettable(t *testing.T) {
	for _, field := range []string{
		"BusyInternal", "Away/BusyInternal", "Custom 2/Internal", "Out of office/AllHoursExternal",
	} {
		if _, ok := settingNamed(field); !ok {
			t.Errorf("%s is refused, so that profile cannot be changed at all", field)
		}
	}
	// Still an allowlist. A profile prefix is not a way in.
	for _, field := range []string{"Away/AuthPassword", "Away/Nonsense", "Nonsense"} {
		if _, ok := settingNamed(field); ok {
			t.Errorf("%s was accepted; the prefix is not a skeleton key", field)
		}
	}
}

/*
Which route a profile carries is a property of the profile, not of its name.

On the phone system this was built against, Available and Custom 1 hold
AvailableRoute while Away, Out of office and Custom 2 hold AwayRoute — so
reading the name and assuming would be wrong on exactly one of the five, and
wrong silently.
*/
func TestAProfilesShapeComesFromTheProfile(t *testing.T) {
	rows := []map[string]any{{
		"ForwardingProfiles": []any{
			map[string]any{"Name": "Available", atDesk: map[string]any{}},
			map[string]any{"Name": "Away", awayFrom: map[string]any{}},
			map[string]any{"Name": "Custom 1", atDesk: map[string]any{}},
			map[string]any{"Name": "Custom 2", awayFrom: map[string]any{}},
		},
	}}

	got := profilesInUse(rows)
	if len(got) != 4 {
		t.Fatalf("found %d profiles, want 4: %+v", len(got), got)
	}
	if got[0].Name != defaultProfile {
		t.Errorf("the picker would open on %q rather than the default", got[0].Name)
	}
	shape := map[string]bool{}
	for _, p := range got {
		shape[p.Name] = p.AtDesk
	}
	if !shape["Custom 1"] {
		t.Error("Custom 1 holds AvailableRoute and was read as an away profile")
	}
	if shape["Custom 2"] {
		t.Error("Custom 2 holds AwayRoute and was read as a desk profile")
	}
}

// Each kind of profile is offered only the rules it can hold. A busy rule on a
// profile that sends every call to one place is a control that writes nowhere.
func TestAProfileIsOfferedOnlyItsOwnRules(t *testing.T) {
	published := settable(fieldLists{
		Profiles: []statusProfile{
			{Name: "Available", AtDesk: true},
			{Name: "Away", AtDesk: false},
		},
	})

	var fields []string
	for _, p := range published {
		fields = append(fields, p.Field)
	}
	for _, want := range []string{
		"Available/BusyInternal", "Away/Internal", "Away/AllHoursExternal", "Away/RingMyMobile",
	} {
		if !slices.Contains(fields, want) {
			t.Errorf("%s is not offered", want)
		}
	}
	for _, unwanted := range []string{"Away/BusyInternal", "Available/Internal", "AllHoursExternal"} {
		if slices.Contains(fields, unwanted) {
			t.Errorf("%s is offered and that profile cannot hold it", unwanted)
		}
	}

	/*
		"Internal" is an extension field — may only call internally — and it is
		also the away rule for where internal calls go. It must still be the
		extension field, because that is the one whose name is bare.
	*/
	if !slices.Contains(fields, "Internal") {
		t.Error("the extension's own Internal field was displaced by a forwarding rule")
	}
	for _, p := range published {
		if p.Field == "Internal" && p.Group != "General" {
			t.Errorf("Internal is published as a %s field: %q", p.Group, p.Label)
		}
	}
}

// Rules for several profiles arrive as one flat set and have to be written to
// one object each.
func TestRulesAreGroupedByProfile(t *testing.T) {
	grouped := byProfile(map[string]any{
		"Available/BusyInternal": "VoiceMail",
		"Away/Internal":          "Extension:100",
		"Away/RingMyMobile":      true,
	})

	if len(grouped) != 2 {
		t.Fatalf("grouped into %d profiles, want 2: %+v", len(grouped), grouped)
	}
	if grouped["Available"]["BusyInternal"] != "VoiceMail" {
		t.Errorf("the default profile got %+v", grouped["Available"])
	}
	if len(grouped["Away"]) != 2 || grouped["Away"]["Internal"] != "Extension:100" {
		t.Errorf("Away got %+v", grouped["Away"])
	}
	if _, leaked := grouped["Away"]["BusyInternal"]; leaked {
		t.Error("a rule landed on a profile it was not meant for")
	}
}
