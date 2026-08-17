package plugin

import "fmt"

// Capability is a semantic tag describing what a tool provides, drawn from a
// vocabulary Azir owns. Core never asks whether a plugin "is a PSA"; it asks
// whether anything in the deployment provides a given capability. Features
// declare the capabilities they need and are unavailable — with a reason —
// when nothing supplies them.
//
// The vocabulary is deliberately small. Tags are added when a real integration
// demonstrates genuine overlap, never in anticipation of one.
type Capability string

const (
	CapCustomersList Capability = "customers.list"
	CapCustomersGet  Capability = "customers.get"
	// CapCustomersContacts is the people at a customer: who to actually call.
	CapCustomersContacts Capability = "customers.contacts"
	CapWorkItemsSearch   Capability = "work_items.search"
	CapWorkItemsGet      Capability = "work_items.get"
	CapTimeEntriesList   Capability = "time_entries.list"
	CapAssetsList        Capability = "assets.list"
	CapCallsList         Capability = "calls.list"
	CapDocsSearch        Capability = "documentation.search"
	CapInvoicesList      Capability = "invoices.list"
	CapAccessCheck       Capability = "access.check"
	CapRecordingsGet     Capability = "recordings.get"

	// CapWorkItemsTimeline is one work item's history as a single ordered
	// sequence, with whatever the plugin can compute about its shape.
	//
	// Distinct from CapWorkItemsGet rather than a flag on it, because a caller
	// asking for a capability must get exactly one kind of answer. When several
	// tools answered to one tag, the choice between "fetch this ticket" and
	// "reconstruct its history" fell to whichever sorted first.
	CapWorkItemsTimeline Capability = "work_items.timeline"

	// CapCustomerStanding is a customer's financial position as a summary,
	// which is a different question from listing their invoices.
	CapCustomerStanding Capability = "customers.standing"

	// CapWorkItemsSchema is what a connected system will accept for a work
	// item — its statuses, its people — so an interface can offer real choices
	// instead of asking someone to type a status and hope.
	CapWorkItemsSchema Capability = "work_items.schema"

	// Capabilities that change something.
	//
	// Separate tags from the ones that read, because a caller asking to read a
	// ticket and a caller asking to comment on one want different things and a
	// shared tag makes them indistinguishable. It also matters to the
	// assistant: a write is offered to it as "propose.<capability>", and
	// "propose.work_items.get" is a phrase nobody can act on — it reads as
	// proposing to fetch something. Naming the action makes the offer legible.
	CapWorkItemsComment Capability = "work_items.comment"
	CapWorkItemsUpdate  Capability = "work_items.update"
	// CapPhoneExtensionWrite is creating or changing extensions on a customer's
	// phone system.
	CapPhoneExtensionWrite  Capability = "phone_system.extension_change"
	CapPhoneExtensionCreate Capability = "phone_system.extension_create"
	CapPhoneRingGroups      Capability = "phone_system.ring_groups"
	CapPhoneRingGroupWrite  Capability = "phone_system.ring_group_create"
	CapPhoneExtensionDelete Capability = "phone_system.extension_delete"
	CapPhoneRingGroupDelete Capability = "phone_system.ring_group_delete"
	// CapPhoneEvents is what a phone system has been logging. Distinct from
	// CapPhoneStatus: both once answered to the same name, providers are sorted,
	// and "events.recent" sorts before "system.status" — so every request for a
	// system's health was answered with a list of log lines, and the screen that
	// asked for health crashed on a shape it had no reason to expect.
	CapPhoneEvents Capability = "phone_system.events"
	// The four questions most phone tickets actually turn on.
	CapPhoneCallHistory      Capability = "phone_system.call_history"
	CapPhoneExtensionDetail  Capability = "phone_system.extension_detail"
	CapPhoneDevices          Capability = "phone_system.devices"
	CapPhoneServices         Capability = "phone_system.services"
	CapPhoneLogSearch        Capability = "phone_system.log_search"
	CapPhoneExtensionOptions Capability = "phone_system.extension_options"
	CapPhoneHandsetAction    Capability = "phone_system.handset_action"
	CapPhoneReview           Capability = "phone_system.review"
	// CapPhoneCapture is a diagnostic capture pulled from a live phone system
	// rather than uploaded as a file. Distinct from CapPhoneEvents, which is
	// the last few events for a screen: this is the whole log, paged, for
	// something that is going to analyse it.
	CapPhoneCapture Capability = "phone_system.capture"

	// CapPhoneStatus is whether a customer's phone system is healthy: how many
	// extensions and trunks are registered against how many exist, how many
	// calls are running against what the licence allows, and whether anything
	// has stopped.
	//
	// One capability rather than several because the question a helpdesk asks
	// is "is their phone system all right", and answering it in four calls
	// makes four ways to answer it wrong.
	CapPhoneStatus Capability = "phone_system.status"

	// CapPhoneExtensions is who has an extension and whether their handset is
	// currently registered — the difference between "the phone is broken" and
	// "the phone is unplugged".
	CapPhoneExtensions Capability = "phone_system.extensions"

	// CapWebSearch is the public internet, as search results.
	//
	// The only capability whose answers come from outside the customer's own
	// systems, which makes it the only one that can carry information *out*.
	// Two rules follow from that and are enforced by the plugin rather than
	// left to a prompt: the caller supplies a query, never a destination, so
	// the model cannot choose which host is contacted; and nothing is fetched
	// from a URL the search did not return. Without those, a customer who
	// writes "look up evil.example/?data=" into a ticket has an exfiltration
	// channel, in a product whose whole claim is that their data does not
	// leave.
	CapWebSearch Capability = "web.search"

	// CapDiagnostic is for tools that expose no customer data at all, such as
	// the echo plugin. It exists so that trivial plugins need not misuse a
	// domain tag.
	CapDiagnostic Capability = "diagnostic"
)

var vocabulary = map[Capability]struct{}{
	CapCustomersList:         {},
	CapCustomersGet:          {},
	CapCustomersContacts:     {},
	CapWorkItemsSearch:       {},
	CapWorkItemsGet:          {},
	CapWorkItemsTimeline:     {},
	CapWorkItemsSchema:       {},
	CapTimeEntriesList:       {},
	CapAssetsList:            {},
	CapWebSearch:             {},
	CapPhoneStatus:           {},
	CapPhoneExtensions:       {},
	CapWorkItemsComment:      {},
	CapWorkItemsUpdate:       {},
	CapPhoneExtensionWrite:   {},
	CapPhoneExtensionCreate:  {},
	CapPhoneRingGroups:       {},
	CapPhoneRingGroupWrite:   {},
	CapPhoneExtensionDelete:  {},
	CapPhoneRingGroupDelete:  {},
	CapPhoneEvents:           {},
	CapPhoneCallHistory:      {},
	CapPhoneExtensionDetail:  {},
	CapPhoneDevices:          {},
	CapPhoneServices:         {},
	CapPhoneLogSearch:        {},
	CapPhoneExtensionOptions: {},
	CapPhoneHandsetAction:    {},
	CapPhoneReview:           {},
	CapPhoneCapture:          {},
	CapCallsList:             {},
	CapDocsSearch:            {},
	CapInvoicesList:          {},
	CapCustomerStanding:      {},
	CapAccessCheck:           {},
	CapRecordingsGet:         {},
	CapDiagnostic:            {},
}

// Valid reports whether c belongs to Azir's vocabulary.
func (c Capability) Valid() bool {
	_, ok := vocabulary[c]
	return ok
}

func (c Capability) String() string { return string(c) }

// validateCapabilities rejects tags outside the vocabulary. Letting plugins
// invent tags freely is how the mechanism stops meaning anything.
func validateCapabilities(tool string, caps []Capability) error {
	if len(caps) == 0 {
		return fmt.Errorf("tool %q declares no capabilities", tool)
	}
	for _, c := range caps {
		if !c.Valid() {
			return fmt.Errorf("tool %q declares unknown capability %q", tool, c)
		}
	}
	return nil
}

// Category is a label used only for grouping in the admin console. It carries
// no functional meaning, so being wrong about it costs nothing.
type Category string

const (
	CategoryPSA           Category = "psa"
	CategoryTelephony     Category = "telephony"
	CategoryRMM           Category = "rmm"
	CategoryDocumentation Category = "documentation"
	// CategoryAI is for connections that exist to make the assistant better
	// rather than to reach a customer's system — a search backend, a knowledge
	// source. Worth its own group because the question an administrator asks
	// about them is different: not "can we reach the customer" but "what is
	// the assistant allowed to consult".
	CategoryAI Category = "ai"
	// CategoryInternal is for connections that report on Azir itself and hold
	// no customer data at all.
	CategoryInternal Category = "internal"
	CategoryOther    Category = "other"
)
