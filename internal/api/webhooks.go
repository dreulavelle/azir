package api

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/dreulavelle/azir/internal/audit"
	"github.com/dreulavelle/azir/internal/identity"
	"github.com/dreulavelle/azir/internal/store"
	"github.com/dreulavelle/azir/pkg/plugin"
)

// Webhooks: how a connected system says something changed.
//
// This is the only route in Azir that answers to somebody who is not signed
// in, so it is worth being precise about what it does and does not do.
//
// It does not believe anything it is sent. A delivery means "something about
// this changed"; it never means "here is the new state". Azir responds by
// forgetting what it cached and asking the source again with its own
// credentials. That is what makes an unsigned endpoint safe: Syncro does not
// sign its webhooks — there is no HMAC to check — so a forged delivery has to
// be harmless by construction rather than by verification. The worst a
// stranger who guesses the URL can achieve is making Azir refetch a ticket it
// was entitled to read anyway.
//
// That matters more here than in most products. The assistant reads ticket
// text, so a webhook that could inject a ticket would be a prompt-injection
// channel pointed straight at it. It cannot, because nothing it says is ever
// stored or shown.

// maxWebhookBody is all we will read. The body is only used to spot which
// capability went stale, so a large one is either a mistake or an attack.
const maxWebhookBody = 64 << 10

// deliveryLimit is how many deliveries one endpoint may make per minute.
//
// Not about load: each delivery causes Azir to refetch from the connected
// system, so an unthrottled endpoint is a way to make Azir hammer a customer's
// PSA and burn their rate limit. The number is far above any real ticket
// volume and far below anything that would matter.
const deliveryLimit = 120

type rateWindow struct {
	mu     sync.Mutex
	counts map[string]int
	minute int64
}

func (r *rateWindow) allow(key string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now().Unix() / 60
	if now != r.minute {
		r.minute = now
		r.counts = map[string]int{}
	}
	if r.counts == nil {
		r.counts = map[string]int{}
	}
	r.counts[key]++
	return r.counts[key] <= deliveryLimit
}

var deliveries = &rateWindow{}

// staleAfter maps a webhook's subject to the capabilities it invalidates.
//
// Deliberately coarse. Working out that ticket 4210 specifically changed would
// let us invalidate one cache entry instead of a handful, and would mean
// trusting an id out of an unauthenticated body to decide what to refetch. The
// coarse version is a few more requests and nothing to get wrong.
var staleAfter = map[string][]string{
	"ticket":   {"work_items.search", "work_items.get", "work_items.timeline"},
	"customer": {"customers.list", "customers.get"},
	"invoice":  {"invoices.list", "customers.standing"},
	"payment":  {"invoices.list", "customers.standing"},
	"asset":    {"assets.list"},
	"timer":    {"time_entries.list"},
	// An SLA breach is about a ticket, so the ticket caches are what go stale.
	// It is separated from "ticket" because what happens next is different:
	// this one is worth interrupting somebody for.
	"sla": {"work_items.search", "work_items.get", "work_items.timeline"},
	// A device alert is about equipment, and often about a ticket that was
	// raised from it.
	"alert": {"assets.list", "work_items.search"},
	// Stock, purchase orders, returns. Nothing in Azir reads these yet, so
	// nothing goes stale — recognised so they are not miscounted as tickets
	// and do not cause a pointless refetch of the queue.
	"order":       {},
	"appointment": {},
}

// byCategory maps the leading word of a vendor's event name.
//
// Populated from what an operator actually enables rather than from
// documentation, because these names are undocumented and the list is what a
// deployment really receives.
var byCategory = map[string]string{
	"ticket":              "ticket",
	"sla":                 "sla",
	"rmm alert":           "alert",
	"script":              "alert",
	"payments":            "payment",
	"invoice":             "invoice",
	"purchase order":      "order",
	"rma":                 "order",
	"return":              "order",
	"products":            "order",
	"parts/logistics":     "order",
	"appointment":         "appointment",
	"appointment(for me)": "appointment",
	"appointment booking": "appointment",
	"reminder":            "appointment",
	"rmm asset":           "asset",
	"network discovery":   "asset",
	"customer":            "customer",
	"customer email":      "ticket",
	"contact":             "customer",
}

// subjectOf works out what a delivery was about, from whatever the sender
// happened to call it.
//
// Every PSA names its events differently and none of them promise not to
// change. Reading intent from the words costs nothing when it is wrong: an
// unrecognised event invalidates the ticket caches, which is the common case
// and is merely a wasted request if it was something else.
func subjectOf(body []byte) (subject, event string) {
	var envelope map[string]any
	if err := json.Unmarshal(body, &envelope); err != nil {
		return "ticket", ""
	}

	// The event's own name, if it gave one. Kept for the log so an operator
	// can see what a connected system is actually sending — the names are
	// vendor-specific, undocumented and change, and guessing at them from the
	// outside is how this mapping silently rots.
	for _, key := range []string{"event", "event_type", "type", "topic", "action", "name"} {
		if v, ok := envelope[key].(string); ok && v != "" {
			event = strings.ToLower(v)
			break
		}
	}

	var words strings.Builder
	words.WriteString(event)
	words.WriteString(" ")
	for _, key := range []string{"type", "event", "event_type", "name", "topic", "action", "subject", "message"} {
		if v, ok := envelope[key].(string); ok {
			words.WriteString(strings.ToLower(v))
			words.WriteString(" ")
		}
	}
	// Some senders put the subject in the shape rather than in a field.
	for key := range envelope {
		words.WriteString(strings.ToLower(key))
		words.WriteString(" ")
	}

	// Syncro names an event "Category - what happened", and the category is
	// the reliable half: "Ticket - A customer replied to any Ticket" is about
	// a ticket, not about a customer, and keyword matching on the whole string
	// gets that exactly backwards. Read the category first when there is one.
	if category, _, found := strings.Cut(event, " - "); found {
		if subject, known := byCategory[strings.TrimSpace(category)]; known {
			return subject, event
		}
	}

	// Order matters: the first match wins, so the more specific and more urgent
	// readings are checked before the general ones. An SLA breach mentions a
	// ticket, and reading it as an ordinary ticket change would lose the only
	// part that mattered.
	said := words.String()
	switch {
	case strings.Contains(said, "sla"):
		return "sla", event
	case strings.Contains(said, "rmm alert"), strings.Contains(said, "alert"),
		strings.Contains(said, "script"):
		return "alert", event
	case strings.Contains(said, "payment"), strings.Contains(said, "payout"),
		strings.Contains(said, "card"), strings.Contains(said, "dispute"):
		return "payment", event
	case strings.Contains(said, "invoice"):
		return "invoice", event
	case strings.Contains(said, "purchase order"), strings.Contains(said, "rma"),
		strings.Contains(said, "return"), strings.Contains(said, "product"),
		strings.Contains(said, "parts"), strings.Contains(said, "logistics"):
		return "order", event
	case strings.Contains(said, "appointment"), strings.Contains(said, "reminder"):
		return "appointment", event
	case strings.Contains(said, "asset"), strings.Contains(said, "device"),
		strings.Contains(said, "discovery"):
		return "asset", event
	case strings.Contains(said, "timer"), strings.Contains(said, "time_entry"):
		return "timer", event
	case strings.Contains(said, "customer"), strings.Contains(said, "contact"):
		return "customer", event
	default:
		return "ticket", event
	}
}

// receiveWebhook accepts a delivery and forgets whatever it makes stale.
func (s *Server) receiveWebhook(w http.ResponseWriter, r *http.Request) {
	secret := r.PathValue("secret")

	endpoint, err := s.DB.WebhookBySecret(r.Context(), secret)
	if err != nil {
		// Deliberately the same answer whether the URL is unknown or malformed,
		// so the endpoint cannot be used to confirm a guess.
		if !errors.Is(err, store.ErrNotFound) {
			s.Log.Warn("could not resolve a webhook", "error", err)
		}
		http.NotFound(w, r)
		return
	}
	// Constant time, because the lookup above already leaked timing on the
	// index and this is the cheap half of not caring.
	if subtle.ConstantTimeCompare([]byte(secret), []byte(endpoint.Secret)) != 1 {
		http.NotFound(w, r)
		return
	}

	if !deliveries.allow(endpoint.Plugin) {
		_ = s.DB.RecordDelivery(r.Context(), endpoint.Plugin, false)
		w.Header().Set("retry-after", "60")
		http.Error(w, "too many deliveries", http.StatusTooManyRequests)
		return
	}

	body, _ := io.ReadAll(io.LimitReader(r.Body, maxWebhookBody))
	subject, event := subjectOf(body)

	var forgotten int64
	for _, capability := range staleAfter[subject] {
		for _, qualified := range s.Reg.Providers(plugin.Capability(capability)) {
			pluginName, toolName, ok := strings.Cut(qualified, ".")
			if !ok {
				continue
			}
			n, err := s.DB.InvalidateCache(r.Context(), pluginName, toolName, nil)
			if err != nil {
				s.Log.Warn("could not invalidate after a webhook",
					"plugin", pluginName, "tool", toolName, "error", err)
				continue
			}
			forgotten += n
		}
	}

	if err := s.DB.RecordDelivery(r.Context(), endpoint.Plugin, true); err != nil {
		s.Log.Warn("could not record a webhook delivery", "error", err)
	}

	// Recorded without a body. What arrived is somebody else's data and Azir
	// has no business keeping a second copy of it.
	s.Audit.Record(r.Context(), audit.Event{
		ActorUserID: "webhook:" + endpoint.Plugin,
		Action:      "webhook.received",
		Plugin:      endpoint.Plugin,
		Outcome:     audit.OutcomeOK,
		Detail:      strings.TrimSpace(subject + " " + event),
	})

	// Every open browser hears about it, so a queue is current within a second
	// instead of within a polling interval.
	s.announce(subject)

	// The event name is logged; the body is not. Knowing that
	// "ticket_customer_reply" arrived is a diagnostic; keeping what the
	// customer wrote would be a second copy of the ticket system.
	s.Log.Info("webhook received", "plugin", endpoint.Plugin,
		"event", event, "subject", subject, "entries_forgotten", forgotten)

	// A sender that gets anything other than a quick 200 will retry, and some
	// will disable the endpoint after enough failures.
	w.WriteHeader(http.StatusNoContent)
}

// getWebhook returns the URL to paste into a connected system's admin console.
func (s *Server) getWebhook(w http.ResponseWriter, r *http.Request) {
	endpoint, err := s.DB.WebhookEndpointFor(r.Context(), r.PathValue("plugin"))
	if err != nil {
		s.fail(w, err, "could not read the webhook")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"path":       "/api/hooks/" + endpoint.Secret,
		"deliveries": endpoint.Deliveries,
		"rejected":   endpoint.Rejected,
		"last_seen":  endpoint.LastSeen,
	})
}

// rotateWebhook issues a new URL, which is how a leaked one is revoked.
func (s *Server) rotateWebhook(w http.ResponseWriter, r *http.Request, actor identity.Actor) {
	name := r.PathValue("plugin")
	endpoint, err := s.DB.RotateWebhookSecret(r.Context(), name)
	if err != nil {
		s.fail(w, err, "could not issue a new webhook address")
		return
	}
	s.Audit.Record(r.Context(), audit.Event{
		ActorUserID: actor.Email, Action: "webhook.rotate",
		Plugin: name, Outcome: audit.OutcomeOK,
	})
	writeJSON(w, http.StatusOK, map[string]any{"path": "/api/hooks/" + endpoint.Secret})
}
