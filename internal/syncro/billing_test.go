package syncro_test

import (
	"testing"

	"github.com/dreulavelle/azir/internal/syncro"
)

// A token granted only ticket access is a sensible thing for an administrator
// to issue. The ticket tools must work and the rest must explain themselves —
// the plugin does not fail, and no tool waits until call time to discover it
// cannot work.
func TestPartialPermissionsDegradeGracefully(t *testing.T) {
	ticketsOnly := syncro.Access{
		Reachable: map[string]bool{
			"tickets":       true,
			"customers":     false,
			"assets":        false,
			"invoices":      false,
			"documentation": false,
			"time_entries":  false,
		},
	}

	availability := ticketsOnly.ToolAvailability()

	mustWork := []string{"tickets.search", "tickets.get", "tickets.timeline"}
	for _, tool := range mustWork {
		st, ok := availability[tool].(map[string]any)
		if !ok || st["available"] != true {
			t.Errorf("%s should work with ticket.read alone: %v", tool, availability[tool])
		}
	}

	mustExplain := map[string]string{
		"customers.search":   "customers",
		"assets.list":        "assets",
		"invoices.list":      "invoices",
		"customers.standing": "invoices",
		"docs.search":        "documentation",
		"time.entries":       "time_entries",
	}
	for tool, need := range mustExplain {
		st, ok := availability[tool].(map[string]any)
		if !ok || st["available"] != false {
			t.Errorf("%s should be unavailable without %s: %v", tool, need, availability[tool])
			continue
		}
		missing, _ := st["missing"].([]string)
		if len(missing) == 0 {
			t.Errorf("%s is unavailable but does not say what is missing", tool)
		}
	}
}

// A fully permissioned token must show everything available, so the mechanism
// cannot be passing by disabling things indiscriminately.
func TestFullPermissionsEnableEverything(t *testing.T) {
	full := syncro.Access{
		Reachable: map[string]bool{
			"tickets": true, "customers": true, "assets": true, "contacts": true,
			"invoices": true, "documentation": true, "time_entries": true,
		},
	}
	for tool, st := range full.ToolAvailability() {
		info, _ := st.(map[string]any)
		if info["available"] != true {
			t.Errorf("%s unavailable with full read access: %v", tool, st)
		}
	}
}
