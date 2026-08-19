package main

import (
	"os"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

/*
Every property name this plugin uses, checked against 3CX's own description of
its API.

Three bugs in one day came from writing these from memory. Two of the eight
role names were invented. A role was written to GroupRights, which a UserGroup
carries alongside Rights, and the phone system accepted it and changed nothing.
Forwarding rules went to AvailableRoute on profiles that carry AwayRoute, and
were refused. Each was found by trying it against a customer's phone system,
which is the worst place to find out.

The schema was sitting in a download the whole time. It is in the repository
now — see xapi/README.md — and this reads it.

This catches two of those three. Reintroducing the invented property and the
misrouted rule both fail here; the invented role names do not, because a role
is a value and 3CX documents RoleName as a plain string with no enum to check
against. Nothing in the schema could have caught them — which is why the roles
are now asked of the phone system at Groups({id})/Rights rather than written
down anywhere.

So: a floor, not a guarantee. It knows every name a property can have and
almost nothing about what may go in one.
*/

// schema is 3CX's OpenAPI document, loaded once for the package.
type openAPI struct {
	Components struct {
		Schemas map[string]yaml.Node `yaml:"schemas"`
	} `yaml:"components"`
}

func load(t *testing.T) *openAPI {
	t.Helper()
	raw, err := os.ReadFile("xapi/openapi.yaml")
	if err != nil {
		t.Fatalf("the vendored 3CX schema is missing, so nothing here is checked: %v", err)
	}
	var doc openAPI
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("the vendored 3CX schema could not be read: %v", err)
	}
	if len(doc.Components.Schemas) == 0 {
		t.Fatal("the vendored 3CX schema describes nothing")
	}
	return &doc
}

/*
propertiesOf collects the property names a schema defines, following allOf.

Pbx.User is an allOf over Pbx.ClickToCall and its own object, so reading only
the top level would find none of the fields this plugin writes.
*/
func propertiesOf(t *testing.T, doc *openAPI, name string) map[string]bool {
	t.Helper()
	node, ok := doc.Components.Schemas[name]
	if !ok {
		t.Fatalf("the schema has no %s, so this test is checking against nothing", name)
	}
	out := map[string]bool{}
	collect(t, doc, &node, out, 0)
	if len(out) == 0 {
		t.Fatalf("%s defines no properties", name)
	}
	return out
}

func collect(t *testing.T, doc *openAPI, node *yaml.Node, into map[string]bool, depth int) {
	t.Helper()
	if depth > 6 || node == nil || node.Kind != yaml.MappingNode {
		return
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		key, value := node.Content[i].Value, node.Content[i+1]
		switch key {
		case "properties":
			for j := 0; j+1 < len(value.Content); j += 2 {
				into[value.Content[j].Value] = true
			}
		case "allOf":
			for _, one := range value.Content {
				collect(t, doc, one, into, depth+1)
			}
		case "$ref":
			// "#/components/schemas/Pbx.ClickToCall"
			if at := strings.LastIndex(value.Value, "/"); at >= 0 {
				if referenced, ok := doc.Components.Schemas[value.Value[at+1:]]; ok {
					collect(t, doc, &referenced, into, depth+1)
				}
			}
		}
	}
	// A $ref sits beside its siblings in an allOf entry, so the loop above
	// handles both without needing to know which shape it met.
}

// Every field the bulk editor writes is a property of Pbx.User.
func TestEveryEditableFieldExists(t *testing.T) {
	doc := load(t)
	user := propertiesOf(t, doc, "Pbx.User")

	for _, o := range editable {
		if !user[o.Field] {
			t.Errorf("%s (%q) is not a property of Pbx.User", o.Field, o.Label)
		}
	}
	for _, name := range []string{fieldTunnel, fieldLanOnly} {
		if !user[name] {
			t.Errorf("%s is not a property of Pbx.User", name)
		}
	}
}

/*
The two rights properties a membership carries, and the one that is the
membership's own.

Writing GroupRights alone was accepted with a 200 and changed nothing. Both
names have to exist for the fix to mean anything.
*/
func TestAMembershipCarriesBothRights(t *testing.T) {
	doc := load(t)
	membership := propertiesOf(t, doc, "Pbx.UserGroup")

	for _, name := range []string{"Rights", "GroupRights", "GroupId"} {
		if !membership[name] {
			t.Errorf("Pbx.UserGroup has no %s", name)
		}
	}
	if !propertiesOf(t, doc, "Pbx.Rights")["RoleName"] {
		t.Error("Pbx.Rights has no RoleName, which is where a role lives")
	}
}

/*
The two shapes a forwarding profile can carry, and the rules in each.

Rules were written to AvailableRoute on every profile; half of them carry
AwayRoute and refused. The rule names differ between the two, which is the
whole reason the mistake was possible.
*/
func TestForwardingRulesMatchTheirRoute(t *testing.T) {
	doc := load(t)

	profile := propertiesOf(t, doc, "Pbx.ForwardingProfile")
	for _, name := range []string{atDesk, awayFrom, "AcceptMultipleCalls", "NoAnswerTimeout", "RingMyMobile"} {
		if !profile[name] {
			t.Errorf("Pbx.ForwardingProfile has no %s", name)
		}
	}

	desk := propertiesOf(t, doc, "Pbx.AvailableRouting")
	for _, rule := range deskRules {
		if !desk[rule.Field] {
			t.Errorf("%s is offered on an at-desk profile and Pbx.AvailableRouting has no such property", rule.Field)
		}
	}

	away := propertiesOf(t, doc, "Pbx.AwayRouting")
	for _, rule := range awayRules {
		if !away[rule.Field] {
			t.Errorf("%s is offered on an away profile and Pbx.AwayRouting has no such property", rule.Field)
		}
	}

	// And the two sets really are different, or the bug this guards against
	// could not have happened and the test is checking nothing.
	shared := 0
	for _, rule := range deskRules {
		if away[rule.Field] {
			shared++
		}
	}
	if shared == len(deskRules) {
		t.Error("both routes carry the same rules, so this test proves nothing")
	}
}

// Every destination offered is one the phone system's own enum names.
func TestEveryDestinationExists(t *testing.T) {
	doc := load(t)
	node, ok := doc.Components.Schemas["Pbx.DestinationType"]
	if !ok {
		t.Fatal("the schema has no Pbx.DestinationType")
	}
	var known []string
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == "enum" {
			for _, one := range node.Content[i+1].Content {
				known = append(known, one.Value)
			}
		}
	}
	if len(known) == 0 {
		t.Fatal("Pbx.DestinationType names no destinations")
	}
	for _, choice := range destinations {
		if !slices.Contains(known, choice) {
			t.Errorf("%q is offered as a destination and is not one of %v", choice, known)
		}
	}
}

// Every field a closure is built from is a property of Pbx.Holiday.
func TestEveryClosureFieldExists(t *testing.T) {
	doc := load(t)
	holiday := propertiesOf(t, doc, "Pbx.Holiday")

	for _, name := range []string{
		"Id", "Name", "Day", "Month", "Year", "DayEnd", "MonthEnd", "YearEnd",
		"TimeOfStartDate", "TimeOfEndDate", "IsRecurrent", "Group", "HolidayPrompt",
	} {
		if !holiday[name] {
			t.Errorf("Pbx.Holiday has no %s", name)
		}
	}
}

// The handset properties the IP phone tab reads and the one it writes.
func TestEveryHandsetFieldExists(t *testing.T) {
	doc := load(t)
	phone := propertiesOf(t, doc, "Pbx.Phone")

	for _, name := range []string{"Interface", "MacAddress", "Name", "TemplateName"} {
		if !phone[name] {
			t.Errorf("Pbx.Phone has no %s", name)
		}
	}
}
