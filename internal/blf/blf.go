// Package blf reads and writes a 3CX extension's busy-lamp-field keys.
//
// The phone system keeps the whole key layout as one string of XML on the
// extension, rather than as a list of anything:
//
//	<PhoneDevice><BLFS><BLF ID="-1" BLFNo="2" BLFType="Line" BLFTypeID="6" />…</BLFS></PhoneDevice>
//
// So changing one key means writing all of them back, and writing them back
// means producing something the phone system reads the same way it wrote it.
// That is why this renders by hand instead of using encoding/xml: Go writes
// `<BLF></BLF>` where 3CX writes `<BLF />`, orders attributes its own way and
// escapes a newline as a character reference. None of that is wrong XML and
// all of it is a difference, and a difference in a field that decides what
// somebody's desk phone does is not worth being relaxed about.
//
// Reading is lenient and writing is exact, which is the right way round.
package blf

import (
	"encoding/xml"
	"fmt"
	"sort"
	"strings"
)

// The kinds of key a 3CX phone can carry, with the number the phone system
// pairs each name with. Both travel in the XML and both have to agree.
const (
	KindBLF             = "BLF"
	KindSpeedDial       = "SpeedDial"
	KindCustomSpeedDial = "CustomSpeedDial"
	KindSharedParking   = "SharedParking"
	KindQueueLogin      = "QueueLogin"
	KindProfileStatus   = "ProfileStatus"
	KindLine            = "Line"
)

// kindID is the number the phone system writes beside each name.
var kindID = map[string]int{
	KindBLF:             0,
	KindSpeedDial:       1,
	KindCustomSpeedDial: 2,
	KindSharedParking:   3,
	KindQueueLogin:      4,
	KindProfileStatus:   5,
	KindLine:            6,
}

// Kinds lists them in the order a person reads them, with the words to show.
var Kinds = []struct {
	Kind  string `json:"kind"`
	Label string `json:"label"`
	// Takes says what the value on this kind of key means, or is empty when
	// the key carries nothing.
	Takes string `json:"takes,omitempty"`
	Note  string `json:"note,omitempty"`
}{
	{KindBLF, "Watch an extension", "extension", "Lights up when that extension is busy."},
	{KindSpeedDial, "Speed dial an extension", "extension", ""},
	{KindCustomSpeedDial, "Speed dial a number", "number", "Any number, with a label of your own."},
	{KindSharedParking, "Shared parking", "parking spot", "The parking spot, as the phone system names it."},
	{KindQueueLogin, "Queue login", "", "Logs in and out of the queues this extension is in."},
	{KindProfileStatus, "Status profile", "profile", "Switches to one of the extension's status profiles."},
	{KindLine, "Line", "", "A plain line key."},
}

/*
Key is one button on the phone.

No is the button's position, and it is the phone system's numbering rather than
an index: a layout can have gaps, because a key left at its default is not
written out at all. Keeping the original numbers is what makes changing one key
leave the others where they were.
*/
type Key struct {
	No   int    `json:"no"`
	Kind string `json:"kind"`
	// ID is what the key points at, and its meaning depends on the kind: the
	// phone system's own id for an extension it watches, a parking spot's id,
	// one of two constants for queue login, and "-1" for a key that points at
	// nothing.
	ID string `json:"id"`
	// Value is what the phone system writes inside the element. For an
	// extension key it is the number; for a custom speed dial it is the
	// number and its labels, one per line.
	Value string `json:"value"`
}

// Nothing is the id a key carries when it points at nothing in particular.
const Nothing = "-1"

// The two ids a queue-login key can carry. The phone system decides what the
// key does from the id, and writes a fixed phrase as the value.
const (
	LoggedIn  = "LOGGEDINQUEUE"
	LoggedOut = "LOGGEDOUTQUEUE"
	// ByID is the literal the phone system writes inside a queue-login key.
	ByID = "DEFINED BY ID"
)

// Parse reads a layout. An empty or absent one is no keys rather than an error:
// an extension nobody has set up has exactly that.
func Parse(raw string) ([]Key, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}

	var device struct {
		XMLName xml.Name `xml:"PhoneDevice"`
		BLFS    struct {
			Keys []struct {
				ID     string `xml:"ID,attr"`
				No     int    `xml:"BLFNo,attr"`
				Kind   string `xml:"BLFType,attr"`
				KindID int    `xml:"BLFTypeID,attr"`
				Value  string `xml:",chardata"`
			} `xml:"BLF"`
		} `xml:"BLFS"`
	}
	if err := xml.Unmarshal([]byte(raw), &device); err != nil {
		return nil, fmt.Errorf("blf: the phone system's key layout could not be read: %w", err)
	}

	keys := make([]Key, 0, len(device.BLFS.Keys))
	for _, k := range device.BLFS.Keys {
		kind := k.Kind
		if _, known := kindID[kind]; !known {
			// A kind this package has never heard of is carried through
			// untouched rather than dropped. Losing a key because a newer
			// phone system grew one would be the worst thing this could do.
			kind = k.Kind
		}
		keys = append(keys, Key{No: k.No, Kind: kind, ID: k.ID, Value: k.Value})
	}
	sort.SliceStable(keys, func(a, b int) bool { return keys[a].No < keys[b].No })
	return keys, nil
}

/*
Render writes a layout the way the phone system writes one.

Byte for byte, which is what makes reading one and writing it back a safe thing
to do — and what lets a test prove it, against a layout the phone system
produced itself.
*/
func Render(keys []Key) string {
	if len(keys) == 0 {
		return "<PhoneDevice><BLFS /></PhoneDevice>"
	}

	ordered := append([]Key{}, keys...)
	sort.SliceStable(ordered, func(a, b int) bool { return ordered[a].No < ordered[b].No })

	var out strings.Builder
	out.WriteString("<PhoneDevice><BLFS>")
	for _, key := range ordered {
		id := key.ID
		if id == "" {
			id = Nothing
		}
		fmt.Fprintf(&out, `<BLF ID="%s" BLFNo="%d" BLFType="%s" BLFTypeID="%d"`,
			escape(id), key.No, escape(key.Kind), kindID[key.Kind])
		if key.Value == "" {
			out.WriteString(" />")
			continue
		}
		fmt.Fprintf(&out, ">%s</BLF>", escape(key.Value))
	}
	out.WriteString("</BLFS></PhoneDevice>")
	return out.String()
}

/*
escape covers the characters that would otherwise end the element, and nothing
else.

Deliberately not xml.EscapeText, which also turns a newline into a character
reference. A custom speed dial holds its labels one per line, and the phone
system writes those newlines raw — so escaping them would be a different string
for the same layout, and the round trip would stop being provable.
*/
func escape(s string) string {
	return strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
		`"`, "&quot;",
	).Replace(s)
}

/*
Renumber closes the gaps after keys have been added, removed or moved.

The phone system numbers buttons from the top of the physical phone, so a
layout is a sequence and not a set. Starting at the first number the layout
already used keeps whatever sits above it — on the extension this was built
against, key one was left at its default and never written out at all, and
renumbering from one would have taken that key's place.
*/
func Renumber(keys []Key, first int) []Key {
	if len(keys) == 0 {
		return keys
	}
	if first < 1 {
		first = 1
	}
	out := make([]Key, len(keys))
	for i, key := range keys {
		key.No = first + i
		out[i] = key
	}
	return out
}

/*
StartsAt is the first button a layout uses, for renumbering a changed one back
onto the same buttons.

A layout that has never been touched starts at the first button. One that
already exists starts wherever it already started, which is not always the
first: on the extension this was built against the phone system had written
nothing for key one, because it was left at its default.
*/
func StartsAt(keys []Key) int {
	if len(keys) == 0 {
		return 1
	}
	first := keys[0].No
	for _, key := range keys {
		if key.No < first {
			first = key.No
		}
	}
	if first < 1 {
		return 1
	}
	return first
}

// Known reports whether this package understands a kind well enough to offer
// it. An unknown one can still be carried through and shown, but not created.
func Known(kind string) bool {
	_, ok := kindID[kind]
	return ok
}
