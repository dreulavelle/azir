package main

import (
	"encoding/json"
	"testing"

	"github.com/dreulavelle/azir/internal/bulk"
)

/*
What this plugin publishes is what Azir reads.

The two structs are a contract written twice — once here as the fields 3CX will
set, once in internal/bulk as the fields a sheet can carry — and JSON in
between. When the names drifted, every option arrived with an empty identity:
the column headers were right, the mapping dropdowns were right, and not one
setting could be read or written. Nothing failed. The sheet just quietly
carried two fields again.

That is the fourth bug of this exact shape in this codebase. Comparing the two
costs one test.
*/
func TestAzirCanReadWhatThisPublishes(t *testing.T) {
	if len(editable) == 0 {
		t.Fatal("nothing is published, so this test checks nothing")
	}

	encoded, err := json.Marshal(editable)
	if err != nil {
		t.Fatal(err)
	}
	var read []bulk.Spec
	if err := json.Unmarshal(encoded, &read); err != nil {
		t.Fatalf("Azir cannot read what this publishes: %v", err)
	}
	if len(read) != len(editable) {
		t.Fatalf("published %d fields, %d arrived", len(editable), len(read))
	}

	for i, spec := range read {
		was := editable[i]
		if spec.Field == "" {
			t.Errorf("%s arrives with no field name, so nothing can be read or written through it", was.Label)
		}
		if string(spec.Field) != was.Field {
			t.Errorf("%s arrives as %q", was.Field, spec.Field)
		}
		if spec.Label == "" {
			t.Errorf("%s arrives with no words, so it would show as its own field name", was.Field)
		}
		if spec.Group == "" {
			t.Errorf("%s arrives with no group, so it lands under Other", was.Field)
		}
		switch spec.Kind {
		case bulk.KindBool, bulk.KindText:
		case bulk.KindChoice:
			if len(spec.Choices) == 0 {
				t.Errorf("%s is a choice with nothing to choose from", was.Field)
			}
		default:
			t.Errorf("%s arrives as kind %q, which internal/bulk does not know", was.Field, spec.Kind)
		}
	}
}

// Merge drops a published field that means the same thing as one of Azir's
// own. 3CX publishes Enabled, which is the switch bulk.FieldEnabled already
// describes, and two columns fighting over one setting is the bug.
func TestAzirsOwnFieldsWin(t *testing.T) {
	encoded, _ := json.Marshal(editable)
	var read []bulk.Spec
	if err := json.Unmarshal(encoded, &read); err != nil {
		t.Fatal(err)
	}

	merged := bulk.Merge(bulk.Core, read)
	seen := map[bulk.Field]bool{}
	labels := map[string]bool{}
	for _, spec := range merged {
		if seen[spec.Field] {
			t.Errorf("%s is in the sheet twice", spec.Field)
		}
		if labels[spec.Label] {
			t.Errorf("two columns are both called %q", spec.Label)
		}
		seen[spec.Field] = true
		labels[spec.Label] = true
	}
	if !seen[bulk.FieldName] || !seen[bulk.FieldEnabled] {
		t.Error("Azir's own two fields did not survive the merge")
	}
}
