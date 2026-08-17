package api

import (
	"testing"
	"time"
)

// A helpdesk with every notification enabled sends bursts: one action can fire
// three webhooks in a second. Without merging, every open browser refetches the
// queue three times for one thing happening.
func TestAnnouncementsAreMerged(t *testing.T) {
	p := &pending{}

	if !p.shouldSend("ticket") {
		t.Fatal("the first announcement was swallowed")
	}
	for range 5 {
		if p.shouldSend("ticket") {
			t.Error("a repeat within the window was sent")
		}
	}

	// Merging is per subject: a payment arriving must not silence a ticket.
	if !p.shouldSend("payment") {
		t.Error("a different subject was merged with the first")
	}

	// And it is a window, not a mute: the next burst still gets through.
	p.mu.Lock()
	p.when["ticket"] = time.Now().Add(-2 * coalesceWindow)
	p.mu.Unlock()
	if !p.shouldSend("ticket") {
		t.Error("nothing got through after the window passed")
	}
}
