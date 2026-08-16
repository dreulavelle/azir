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
	CapCustomersList   Capability = "customers.list"
	CapCustomersGet    Capability = "customers.get"
	CapWorkItemsSearch Capability = "work_items.search"
	CapWorkItemsGet    Capability = "work_items.get"
	CapTimeEntriesList Capability = "time_entries.list"
	CapAssetsList      Capability = "assets.list"
	CapCallsList       Capability = "calls.list"
	CapRecordingsGet   Capability = "recordings.get"

	// CapDiagnostic is for tools that expose no customer data at all, such as
	// the echo plugin. It exists so that trivial plugins need not misuse a
	// domain tag.
	CapDiagnostic Capability = "diagnostic"
)

var vocabulary = map[Capability]struct{}{
	CapCustomersList:   {},
	CapCustomersGet:    {},
	CapWorkItemsSearch: {},
	CapWorkItemsGet:    {},
	CapTimeEntriesList: {},
	CapAssetsList:      {},
	CapCallsList:       {},
	CapRecordingsGet:   {},
	CapDiagnostic:      {},
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
	CategoryOther         Category = "other"
)
