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

	/*
		The fields that are deliberately not properties of an extension.

		A handset is a record beside the extension, and a DID is an inbound
		rule pointing at it — both are joined on in extensionSettings rather
		than selected from Users. Asserted as absent because the failure mode
		is silent: adding one of these names to the $select would have 3CX
		refuse the whole page, and adding it to `editable` would have Azir
		offer to write somewhere that does not exist.
	*/
	for _, o := range append(append([]option{}, handsetFields...), didFields...) {
		if user[o.Field] {
			t.Errorf("%s is joined on separately and is now a property of Pbx.User; select it instead", o.Field)
		}
		if o.Kind != "readonly" {
			t.Errorf("%s is not a property of Pbx.User and is offered as %q rather than readonly", o.Field, o.Kind)
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

/*
Every property the trunk and DID import reads or writes.

Written after this test caught the file's first version reading Name and Host
off Pbx.Trunk. Neither is a trunk property — both belong to the Gateway a trunk
carries — and the mistake compiles, unmarshals to empty strings, and shows a
trunk picker of blank rows. Nothing else would have found it before somebody
opened the screen against a customer's phone system.
*/
func TestEveryTrunkFieldExists(t *testing.T) {
	doc := load(t)
	trunk := propertiesOf(t, doc, "Pbx.Trunk")

	for _, name := range []string{"Id", "Number", "ExternalNumber", "Direction", "IsOnline", "DidNumbers", "Gateway"} {
		if !trunk[name] {
			t.Errorf("Pbx.Trunk has no %s", name)
		}
	}
	// And the two that are not on it, so the fix cannot quietly come undone.
	for _, name := range []string{"Name", "Host"} {
		if trunk[name] {
			t.Errorf("Pbx.Trunk now has %s; the Gateway indirection in trunks.go can be simplified", name)
		}
	}

	gateway := propertiesOf(t, doc, "Pbx.Gateway")
	for _, name := range []string{"Name", "Host"} {
		if !gateway[name] {
			t.Errorf("Pbx.Gateway has no %s, which is where a trunk's name comes from", name)
		}
	}
}

// Every property an imported DID's inbound rule is built from.
func TestEveryInboundRuleFieldExists(t *testing.T) {
	doc := load(t)
	rule := propertiesOf(t, doc, "Pbx.InboundRule")

	for _, name := range []string{
		"Id", "RuleName", "Condition", "Data", "TrunkDN", "OfficeHoursDestination",
		"AlterDestinationDuringOutOfOfficeHours", "AlterDestinationDuringHolidays",
	} {
		if !rule[name] {
			t.Errorf("Pbx.InboundRule has no %s", name)
		}
	}

	// The trunk a rule names is a Peer, and it is matched by Id.
	peer := propertiesOf(t, doc, "Pbx.Peer")
	for _, name := range []string{"Id", "Number"} {
		if !peer[name] {
			t.Errorf("Pbx.Peer has no %s", name)
		}
	}
}

// The condition that matches on the dialled number. A rule created with a
// condition 3CX does not know is a rule that never fires.
func TestTheDIDConditionExists(t *testing.T) {
	doc := load(t)
	if !slices.Contains(enumOf(t, doc, "Pbx.RuleConditionType"), conditionDID) {
		t.Errorf("%q is not one of %v", conditionDID, enumOf(t, doc, "Pbx.RuleConditionType"))
	}
}

/*
Every kind of number a DID may be pointed at exists in both enums.

The sheet gives a plain number and the phone system says what it is, which
means a PeerType has to be turned into a DestinationType. They overlap but are
not the same list — Parking and Conference are peers that no call can be sent
to — so the six this plugin treats as interchangeable have to actually be
spelled the same way in both. If 3CX ever renames one on one side, a DID import
would start writing a destination type that does not exist.
*/
func TestEveryRoutableNumberIsInBothEnums(t *testing.T) {
	doc := load(t)
	peers := enumOf(t, doc, "Pbx.PeerType")
	destinations := enumOf(t, doc, "Pbx.DestinationType")

	for _, name := range []string{"Extension", "Queue", "RingGroup", "IVR", "Fax", "RoutePoint"} {
		if !slices.Contains(peers, name) {
			t.Errorf("%q is treated as a routable number and Pbx.PeerType has no such value", name)
		}
		if !slices.Contains(destinations, name) {
			t.Errorf("%q is written as a destination and Pbx.DestinationType has no such value", name)
		}
		if !canTakeACall(name) {
			t.Errorf("%q is in both enums and canTakeACall refuses it", name)
		}
	}

	// And the ones deliberately left out really are peers, or the exclusion is
	// guarding against nothing.
	for _, name := range []string{"Parking", "Conference"} {
		if !slices.Contains(peers, name) {
			t.Errorf("Pbx.PeerType has no %q, so refusing it proves nothing", name)
		}
		if canTakeACall(name) {
			t.Errorf("a DID may be pointed at a %q, which is not somewhere a call can go", name)
		}
	}
}

// The properties a number's kind is read from.
func TestEveryPeerFieldExists(t *testing.T) {
	doc := load(t)
	peer := propertiesOf(t, doc, "Pbx.Peer")

	for _, name := range []string{"Id", "Number", "Name", "Type"} {
		if !peer[name] {
			t.Errorf("Pbx.Peer has no %s", name)
		}
	}
}

// enumOf reads the values an enum schema names.
func enumOf(t *testing.T, doc *openAPI, name string) []string {
	t.Helper()
	node, ok := doc.Components.Schemas[name]
	if !ok {
		t.Fatalf("the schema has no %s", name)
	}
	var out []string
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == "enum" {
			for _, one := range node.Content[i+1].Content {
				out = append(out, one.Value)
			}
		}
	}
	if len(out) == 0 {
		t.Fatalf("%s names no values", name)
	}
	return out
}
