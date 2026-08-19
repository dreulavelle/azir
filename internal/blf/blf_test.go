package blf

import (
	"strings"
	"testing"
)

/*
A real layout, read off a real phone system.

Every kind of key 3CX offers, set up by hand on extension 100 and fetched back
verbatim. This is the fixture the whole package is built against, because the
format is not documented anywhere and everything here was worked out from it.
*/
const real = `<PhoneDevice><BLFS><BLF ID="-1" BLFNo="2" BLFType="Line" BLFTypeID="6" />` +
	`<BLF ID="32" BLFNo="3" BLFType="BLF" BLFTypeID="0">101</BLF>` +
	`<BLF ID="36" BLFNo="4" BLFType="SpeedDial" BLFTypeID="1">103</BLF>` +
	`<BLF ID="-1" BLFNo="5" BLFType="CustomSpeedDial" BLFTypeID="2">*9104` + "\n" + `Page` + "\n" + `All</BLF>` +
	`<BLF ID="6" BLFNo="6" BLFType="SharedParking" BLFTypeID="3">SP1</BLF>` +
	`<BLF ID="7" BLFNo="7" BLFType="SharedParking" BLFTypeID="3">SP2</BLF>` +
	`<BLF ID="LOGGEDOUTQUEUE" BLFNo="8" BLFType="QueueLogin" BLFTypeID="4">DEFINED BY ID</BLF>` +
	`<BLF ID="LOGGEDINQUEUE" BLFNo="9" BLFType="QueueLogin" BLFTypeID="4">DEFINED BY ID</BLF>` +
	`<BLF ID="-1" BLFNo="10" BLFType="ProfileStatus" BLFTypeID="5">2</BLF>` +
	`<BLF ID="-1" BLFNo="11" BLFType="ProfileStatus" BLFTypeID="5">3</BLF>` +
	`<BLF ID="-1" BLFNo="12" BLFType="ProfileStatus" BLFTypeID="5">0</BLF>` +
	`<BLF ID="-1" BLFNo="13" BLFType="ProfileStatus" BLFTypeID="5">1</BLF>` +
	`</BLFS></PhoneDevice>`

/*
What the phone system wrote is what goes back.

Changing one key means writing the whole layout back, so the layout that comes
out for an untouched one has to be the layout that went in — byte for byte, not
merely equivalent. Anything less and every save would be a small unreviewed
change to somebody's desk phone.
*/
func TestARealLayoutSurvivesTheRoundTrip(t *testing.T) {
	keys, err := Parse(real)
	if err != nil {
		t.Fatalf("parsing a layout the phone system wrote: %v", err)
	}
	if len(keys) != 12 {
		t.Fatalf("read %d keys, want 12", len(keys))
	}
	if got := Render(keys); got != real {
		t.Errorf("the round trip changed the layout.\n got: %s\nwant: %s", got, real)
	}
}

// The keys are read as what they are, not merely counted.
func TestEveryKindIsRead(t *testing.T) {
	keys, err := Parse(real)
	if err != nil {
		t.Fatal(err)
	}
	want := []Key{
		{No: 2, Kind: KindLine, ID: "-1"},
		{No: 3, Kind: KindBLF, ID: "32", Value: "101"},
		{No: 4, Kind: KindSpeedDial, ID: "36", Value: "103"},
		{No: 5, Kind: KindCustomSpeedDial, ID: "-1", Value: "*9104\nPage\nAll"},
		{No: 6, Kind: KindSharedParking, ID: "6", Value: "SP1"},
		{No: 7, Kind: KindSharedParking, ID: "7", Value: "SP2"},
		{No: 8, Kind: KindQueueLogin, ID: LoggedOut, Value: ByID},
		{No: 9, Kind: KindQueueLogin, ID: LoggedIn, Value: ByID},
		{No: 10, Kind: KindProfileStatus, ID: "-1", Value: "2"},
		{No: 11, Kind: KindProfileStatus, ID: "-1", Value: "3"},
		{No: 12, Kind: KindProfileStatus, ID: "-1", Value: "0"},
		{No: 13, Kind: KindProfileStatus, ID: "-1", Value: "1"},
	}
	for i, w := range want {
		if keys[i] != w {
			t.Errorf("key %d read as %+v, want %+v", i, keys[i], w)
		}
	}
}

/*
The layout starts at key two, and stays there.

Key one was left at its default on the extension this was read from, and the
phone system does not write out a key left at its default — so a layout that
renumbered from one would silently take over a button somebody had set.
*/
func TestRenumberingKeepsTheFirstButton(t *testing.T) {
	keys, err := Parse(real)
	if err != nil {
		t.Fatal(err)
	}
	// Drop the speed dial from the middle, as removing a key would.
	shorter := append(append([]Key{}, keys[:2]...), keys[3:]...)
	after := Renumber(shorter, StartsAt(keys))

	if StartsAt(keys) != 2 {
		t.Fatalf("the fixture starts at key %d, not 2", StartsAt(keys))
	}
	if after[0].No != 2 {
		t.Errorf("the layout now starts at key %d; it started at 2", after[0].No)
	}
	for i := 1; i < len(after); i++ {
		if after[i].No != after[i-1].No+1 {
			t.Errorf("a gap was left at key %d", after[i].No)
		}
	}
	if after[2].Kind != KindCustomSpeedDial {
		t.Errorf("the key after the removed one is %s, want the custom speed dial", after[2].Kind)
	}
}

// An extension nobody has set up has no keys, and gets the empty layout the
// phone system itself writes.
func TestAnEmptyLayout(t *testing.T) {
	empty := "<PhoneDevice><BLFS /></PhoneDevice>"
	keys, err := Parse(empty)
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 0 {
		t.Errorf("read %d keys from an empty layout", len(keys))
	}
	if got := Render(keys); got != empty {
		t.Errorf("Render(nothing) = %q, want %q", got, empty)
	}
	if keys, err := Parse(""); err != nil || len(keys) != 0 {
		t.Errorf("Parse(\"\") = %v, %v; want no keys and no error", keys, err)
	}
}

// A name or a label with a character that would end the element early.
func TestCharactersThatWouldBreakTheLayout(t *testing.T) {
	keys := []Key{{No: 1, Kind: KindCustomSpeedDial, ID: "-1", Value: `*1 & "R&D" <all>`}}
	rendered := Render(keys)
	if strings.Contains(rendered, `"R&D"`) {
		t.Error("a quote and an ampersand went in unescaped")
	}
	back, err := Parse(rendered)
	if err != nil {
		t.Fatalf("what this package wrote, it cannot read: %v", err)
	}
	if back[0].Value != keys[0].Value {
		t.Errorf("round trip changed %q to %q", keys[0].Value, back[0].Value)
	}
}

// The kind names and the numbers beside them are two lists that have to agree;
// a kind offered but not numbered would be written as type zero, which is a
// different key entirely.
func TestEveryOfferedKindHasANumber(t *testing.T) {
	for _, k := range Kinds {
		if !Known(k.Kind) {
			t.Errorf("%s is offered but has no number, so it would be written as a %s",
				k.Kind, KindBLF)
		}
	}
	if len(Kinds) != len(kindID) {
		t.Errorf("%d kinds are offered and %d are numbered", len(Kinds), len(kindID))
	}
}
