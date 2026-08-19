package api

import (
	"strings"
	"testing"
)

/*
Reading a written selection.

"102-110, 119" is how somebody describes a floor of phones, and what it
resolves to decides which extensions a bulk edit touches. An off-by-one here
is not a display bug — it is somebody's phone changed because a range was read
one wider than it was written.
*/
func TestASelectionReadsAsWritten(t *testing.T) {
	cases := []struct {
		name, text string
		want       string
	}{
		{"one number", "104", "104"},
		{"a few", "100,102,104", "100 102 104"},
		{"a range", "102-105", "102 103 104 105"},
		{"a range and a number", "102-104, 119", "102 103 104 119"},
		{"spaces instead of commas", "100 101 102", "100 101 102"},
		{"newlines, as pasted from a ticket", "100\n101\n102", "100 101 102"},
		{"backwards is the same floor", "105-102", "102 103 104 105"},
		{"an en dash, as pasted", "102–104", "102 103 104"},
		{"a two dot range", "102..104", "102 103 104"},
		{"one number twice is still one range", "102-102", "102"},
		// A system numbering from 0100 means 0100, and selecting 100 instead
		// would be a different extension or none at all.
		{"leading zeroes are kept", "0100-0102", "0100 0101 0102"},
		{"ragged spacing", "  102 - 104 ", "102 103 104"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := parseSelection(c.text)
			if err != nil {
				t.Fatalf("parseSelection(%q): %v", c.text, err)
			}
			if strings.Join(got, " ") != c.want {
				t.Errorf("parseSelection(%q) = %v, want %v", c.text, got, c.want)
			}
		})
	}
}

// What cannot be read is refused by name rather than silently selecting
// something else. A selection nobody meant is the failure worth catching.
func TestAnUnreadableSelectionIsRefused(t *testing.T) {
	for _, text := range []string{
		"",
		"   ",
		"reception",
		"10a",
		"102-abc",
		"-",
		"1-999999",
	} {
		if got, err := parseSelection(text); err == nil {
			t.Errorf("parseSelection(%q) = %v; want a refusal", text, got)
		}
	}
}

// A typo in a range must not ask a phone system for a hundred thousand
// extensions.
func TestARangeIsBounded(t *testing.T) {
	if _, err := parseSelection("100-100000"); err == nil {
		t.Error("an unbounded range was accepted")
	}
	// Several ranges that are each small but together are not.
	big := make([]string, 0, 40)
	for i := 0; i < 40; i++ {
		big = append(big, "1000-1099")
	}
	if _, err := parseSelection(strings.Join(big, ",")); err == nil {
		t.Error("a selection over the limit was accepted when spread over many ranges")
	}
}
