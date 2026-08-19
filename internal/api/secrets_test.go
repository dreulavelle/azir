package api

import (
	"strings"
	"testing"

	"github.com/dreulavelle/azir/internal/bulk"
)

/*
The write-only fields a bulk edit carries, and what may travel that way.

A voicemail PIN cannot go in a plan: a plan is written to the database, drawn
on a screen, kept in the activity log and handed back as an undo, which is four
places a credential would be at rest for a value nothing ever reads back. So it
arrives with the apply instead — and that makes this a second way into the same
write, one that skips the comparison and the diff the ordinary route goes
through.

Which is why it is gated by kind. Anything at all could be sent this way by
calling it a secret.
*/
var fields = map[bulk.Field]bulk.Spec{
	"VMPIN":        {Field: "VMPIN", Label: "Voicemail PIN", Kind: bulk.KindSecret},
	"RecordCalls":  {Field: "RecordCalls", Label: "Record calls", Kind: bulk.KindBool},
	"PhoneMac":     {Field: "PhoneMac", Label: "MAC address", Kind: bulk.KindReadOnly},
	"EmailAddress": {Field: "EmailAddress", Label: "Email", Kind: bulk.KindText},
}

func TestASecretGoesThrough(t *testing.T) {
	got, err := secretsFor(map[string]string{"VMPIN": "4821"}, fields)
	if err != nil {
		t.Fatalf("a voicemail PIN was refused: %v", err)
	}
	if got["VMPIN"] != "4821" {
		t.Errorf("the PIN arrived as %v", got["VMPIN"])
	}
}

// The gate. A field that is not write-only has a before and an after, and
// belongs in the plan somebody approved rather than in a bag beside it.
func TestOnlyWriteOnlyFieldsMayTravelThisWay(t *testing.T) {
	for _, field := range []string{"RecordCalls", "EmailAddress", "PhoneMac"} {
		_, err := secretsFor(map[string]string{field: "anything"}, fields)
		if err == nil {
			t.Errorf("%s was accepted as a secret, which skips the before-and-after", field)
			continue
		}
		if !strings.Contains(err.Error(), "before-and-after") {
			t.Errorf("%s was refused with %q, which does not say why", field, err)
		}
	}
}

func TestAnUnknownFieldIsRefused(t *testing.T) {
	if _, err := secretsFor(map[string]string{"AuthPassword": "hunter2"}, fields); err == nil {
		t.Error("a field this phone system does not have was accepted")
	}
}

// An empty one is somebody who tabbed through the box, not somebody clearing a
// PIN. There is no such thing as clearing it to nothing.
func TestAnEmptySecretIsNotSent(t *testing.T) {
	got, err := secretsFor(map[string]string{"VMPIN": "   "}, fields)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("an untouched PIN box would have been written: %v", got)
	}
}

// The activity log names what was set and never what it was set to.
func TestTheLogNamesTheFieldAndNotTheValue(t *testing.T) {
	line := alsoSet(map[string]string{"VMPIN": "4821"})
	if !strings.Contains(line, "VMPIN") {
		t.Errorf("%q does not say what was set", line)
	}
	if strings.Contains(line, "4821") {
		t.Errorf("the PIN is in the activity log: %q", line)
	}
	if alsoSet(map[string]string{"VMPIN": ""}) != "" {
		t.Error("an untouched box was recorded as a change")
	}
}
