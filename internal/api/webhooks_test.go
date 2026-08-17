package api

import (
	"encoding/json"
	"testing"
)

// The subject decides which caches are forgotten. Getting it wrong costs a
// wasted request, so the test is about the common shapes rather than about
// exhaustiveness — and about the default being the useful one.
func TestSubjectOf(t *testing.T) {
	cases := []struct {
		name string
		body any
		want string
	}{
		{"a named ticket event", map[string]any{"event": "ticket.created"}, "ticket"},
		{"a customer event", map[string]any{"type": "customer_updated"}, "customer"},
		{"a contact counts as a customer", map[string]any{"event": "contact.created"}, "customer"},
		{"an invoice event", map[string]any{"event": "invoice.paid"}, "invoice"},
		{"a payment is its own subject", map[string]any{"topic": "payment_received"}, "payment"},
		{"a device is an asset", map[string]any{"event": "device.online"}, "asset"},
		{"a timer", map[string]any{"event": "timer.stopped"}, "timer"},
		{"the subject in the shape", map[string]any{"asset": map[string]any{"id": 1}}, "asset"},
		{"something unrecognised falls back to tickets", map[string]any{"event": "wat"}, "ticket"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body, err := json.Marshal(tc.body)
			if err != nil {
				t.Fatal(err)
			}
			if got, _ := subjectOf(body); got != tc.want {
				t.Errorf("subjectOf(%s) = %q, want %q", body, got, tc.want)
			}
		})
	}

	// Syncro's own event names, taken from the notification list an operator
	// actually enables. These are the strings that decide what goes stale, so
	// they are worth pinning: the vendor's names are undocumented and will
	// change, and this test is where that gets noticed.
	syncro := map[string]string{
		"Ticket - A customer replied to any Ticket":        "ticket",
		"Ticket - created from email":                      "ticket",
		"Ticket - Status was changed on any Ticket":        "ticket",
		"Ticket - was resolved":                            "ticket",
		"Ticket - A hidden comment was added to my Ticket": "ticket",
		"SLA - Breached on any Ticket":                     "sla",
		"SLA - Breaching soon on my Ticket":                "sla",
		"RMM Alert - was created":                          "alert",
		"RMM Alert - was auto-resolved":                    "alert",
		"Script - failed":                                  "alert",
		"Payments - A payment was made":                    "payment",
		"Payments - A payout failed":                       "payment",
		"Payments - A stored card is expiring in 30 days":  "payment",
		"Invoice - was created (5 minute delay)":           "invoice",
		"Purchase Order - was created":                     "order",
		"RMA - was created":                                "order",
		"Parts/Logistics - order was created":              "order",
		"Appointment - was created":                        "appointment",
		"Reminder - is due within the next hour":           "appointment",
		"RMM Asset - application installed":                "asset",
		"Network Discovery - Device Discovered":            "asset",
		"Customer - Was created":                           "customer",
		"Contact - Was created":                            "customer",
	}
	for name, want := range syncro {
		body, err := json.Marshal(map[string]string{"event": name})
		if err != nil {
			t.Fatal(err)
		}
		if got, _ := subjectOf(body); got != want {
			t.Errorf("%q read as %q, want %q", name, got, want)
		}
	}

	// The event's own name comes back for the log, so an operator can see what
	// a connected system is actually sending rather than guessing.
	body, _ := json.Marshal(map[string]string{"event": "SLA - Breached on any Ticket"})
	if _, event := subjectOf(body); event != "sla - breached on any ticket" {
		t.Errorf("the event name was not reported: %q", event)
	}

	// A body that is not JSON at all must not panic or return something that
	// invalidates nothing: an unreadable delivery still means "go and look".
	if got, _ := subjectOf([]byte("not json")); got != "ticket" {
		t.Errorf("unreadable body gave %q", got)
	}
}

// The rate limit exists so a discovered URL cannot be used to make Azir hammer
// a customer's PSA. It has to actually stop.
func TestDeliveryRateLimit(t *testing.T) {
	w := &rateWindow{}
	for i := range deliveryLimit {
		if !w.allow("syncro") {
			t.Fatalf("refused delivery %d, which is within the limit", i+1)
		}
	}
	if w.allow("syncro") {
		t.Error("allowed a delivery past the limit")
	}
	// Limits are per endpoint: one noisy sender must not silence another.
	if !w.allow("3cx") {
		t.Error("one endpoint's limit blocked a different endpoint")
	}
}
