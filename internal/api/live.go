package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/dreulavelle/azir/internal/identity"
	"github.com/nats-io/nats.go"
)

// Live updates: how a screen learns that something changed without asking.
//
// Before this, every screen polled. Triage asked for the whole queue once a
// minute whether anything had happened or not, which is both a minute out of
// date and a request a minute against somebody's PSA forever. Now a webhook
// arrives, Azir forgets what it cached, and every open browser is told — so the
// queue is current within a second and costs nothing while nothing happens.
//
// Nothing about the change itself travels. A message says "tickets changed",
// never what changed or for whom: the browser then asks through the ordinary
// authenticated path, which applies the same permission checks as any other
// read. A subscriber therefore learns nothing they could not already see, which
// is what makes it safe to send to every session at once.

// ChangedSubject is where a change is announced inside Azir.
const ChangedSubject = "azir.changed"

// livePulse is how often a keep-alive comment goes out.
//
// Proxies close a connection that has been silent for a while, and a dead
// EventSource reconnects noisily rather than failing loudly. A comment costs
// two bytes and prevents the whole class of problem.
const livePulse = 25 * time.Second

// watchers is every browser currently listening.
type watchers struct {
	mu   sync.RWMutex
	next int
	to   map[int]chan string
}

func (w *watchers) add() (int, chan string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.to == nil {
		w.to = map[int]chan string{}
	}
	w.next++
	// Buffered, so a slow reader delays itself rather than the sender.
	ch := make(chan string, 8)
	w.to[w.next] = ch
	return w.next, ch
}

func (w *watchers) remove(id int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	delete(w.to, id)
}

func (w *watchers) tell(subject string) {
	w.mu.RLock()
	defer w.mu.RUnlock()
	for _, ch := range w.to {
		select {
		case ch <- subject:
		default:
			// A browser that cannot keep up with one word per event is a
			// browser that has gone away. Dropping is right: the next event
			// says the same thing, and a screen that missed one refresh is
			// wrong for a second rather than forever.
		}
	}
}

var live = &watchers{}

// WatchChanges relays announcements from NATS to every connected browser.
//
// Routed through NATS rather than called directly so that a deployment running
// more than one Azir still tells every browser, not only the ones attached to
// whichever instance took the webhook.
func (s *Server) WatchChanges() (func(), error) {
	sub, err := s.NC.Subscribe(ChangedSubject, func(m *nats.Msg) {
		live.tell(string(m.Data))
	})
	if err != nil {
		return nil, fmt.Errorf("api: subscribe to changes: %w", err)
	}
	return func() { _ = sub.Unsubscribe() }, nil
}

// coalesceWindow is how long announcements of the same kind are merged.
//
// A helpdesk with every notification enabled sends a burst: resolving a ticket
// can fire the status change, the resolution and an SLA event within a second
// of each other. Announcing each one separately would make every open browser
// refetch the queue three times for one action. Merging costs a second of
// latency on a screen that was a minute stale before any of this existed.
const coalesceWindow = 1500 * time.Millisecond

type pending struct {
	mu   sync.Mutex
	when map[string]time.Time
}

func (p *pending) shouldSend(subject string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.when == nil {
		p.when = map[string]time.Time{}
	}
	if last, ok := p.when[subject]; ok && time.Since(last) < coalesceWindow {
		return false
	}
	p.when[subject] = time.Now()
	return true
}

var recent = &pending{}

// announce says that something changed, to this instance and any other.
func (s *Server) announce(subject string) {
	if !recent.shouldSend(subject) {
		return
	}
	if s.NC == nil {
		live.tell(subject)
		return
	}
	if err := s.NC.Publish(ChangedSubject, []byte(subject)); err != nil {
		s.Log.Warn("could not announce a change", "subject", subject, "error", err)
	}
}

// streamChanges is the browser's end: one long-lived connection per session.
func (s *Server) streamChanges(w http.ResponseWriter, r *http.Request, _ identity.Actor) {
	flusher, canStream := w.(http.Flusher)
	if !canStream {
		http.Error(w, "streaming is not supported here", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	// Nginx and friends buffer event streams into uselessness otherwise.
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	id, ch := live.add()
	defer live.remove(id)

	pulse := time.NewTicker(livePulse)
	defer pulse.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-pulse.C:
			fmt.Fprint(w, ": still here\n\n")
			flusher.Flush()
		case subject := <-ch:
			payload, err := json.Marshal(map[string]string{"subject": subject})
			if err != nil {
				continue
			}
			fmt.Fprintf(w, "event: changed\ndata: %s\n\n", payload)
			flusher.Flush()
		}
	}
}
