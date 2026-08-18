package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/dreulavelle/azir/internal/identity"
	"github.com/dreulavelle/azir/internal/store"
	"github.com/dreulavelle/azir/pkg/plugin"
)

/*
Answering "have we seen this before".

The index is filled from the helpdesk rather than from conversations, for the
reason the store file gives: the ticket is the record and the conversation is
commentary. What that buys operationally is that this can be wrong, or empty, or
deleted, and the only cost is a rebuild.

Nothing here is on a timer. A ticket is indexed when the helpdesk says it
changed, and the backfill runs when somebody asks for it — a phone system and a
PSA are somebody else's infrastructure and this product does not sit on either
of them making requests nobody asked for.
*/

// recallSource names where indexed tickets came from. One helpdesk for now; the
// column exists so a second could be indexed beside it rather than over it.
const recallSource = "work_items"

// backfillPageSize is how many tickets one pass reads. The vendor caps a page
// at a hundred and the rate limiter paces the rest.
const backfillPageSize = 100

type recallResponse struct {
	Matches []store.Remembered `json:"matches"`
	// Said out loud, because "nothing matched" and "nothing is indexed" look
	// identical from the outside and lead somewhere completely different.
	Indexed        int  `json:"indexed"`
	WithResolution int  `json:"with_resolution"`
	Empty          bool `json:"index_is_empty"`
}

func (s *Server) searchRecall(w http.ResponseWriter, r *http.Request) {
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	if query == "" {
		writeJSON(w, http.StatusBadRequest, errBody("what should I look for?"))
		return
	}

	var customer *uuid.UUID
	if raw := r.URL.Query().Get("customer_id"); raw != "" {
		id, err := uuid.Parse(raw)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, errBody("that is not a customer id"))
			return
		}
		customer = &id
	}

	matches, err := s.DB.Recall(r.Context(), store.RecallQuery{
		Text:     query,
		Customer: customer,
		Exclude:  r.URL.Query().Get("except"),
		Limit:    8,
	})
	if err != nil {
		s.fail(w, err, "that search could not be run")
		return
	}

	total, resolved, err := s.DB.RecallSize(r.Context())
	if err != nil {
		s.Log.Warn("could not size the recall index", "error", err)
	}

	writeJSON(w, http.StatusOK, recallResponse{
		Matches: matches, Indexed: total, WithResolution: resolved, Empty: total == 0,
	})
}

// getRecallStatus reports how much is indexed and whether a backfill has
// finished, so an administrator is never guessing why recall is quiet.
func (s *Server) getRecallStatus(w http.ResponseWriter, r *http.Request) {
	total, resolved, err := s.DB.RecallSize(r.Context())
	if err != nil {
		s.fail(w, err, "could not read the index")
		return
	}
	progress, err := s.DB.BackfillProgress(r.Context(), recallSource)
	if err != nil {
		s.fail(w, err, "could not read the indexing progress")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"indexed":         total,
		"with_resolution": resolved,
		"backfill":        progress,
	})
}

/*
startBackfill reads finished tickets out of the helpdesk and indexes them.

It runs in the background and records where it got to after every page, so a
restart resumes instead of starting again — an MSP with two years of history is
thousands of vendor requests, and losing that to a container restart would mean
nobody ever completes one.

Deliberately not automatic. Filling this index is a decision with a cost that
lands on somebody else's API, and an administrator should be the one making it.
*/
func (s *Server) startBackfill(w http.ResponseWriter, r *http.Request, actor identity.Actor) {
	progress, err := s.DB.BackfillProgress(r.Context(), recallSource)
	if err != nil {
		s.fail(w, err, "could not read the indexing progress")
		return
	}
	if r.URL.Query().Get("restart") == "true" {
		progress = store.RecallProgress{Source: recallSource}
	}
	if progress.Complete && r.URL.Query().Get("restart") != "true" {
		writeJSON(w, http.StatusOK, map[string]any{
			"already_complete": true, "indexed": progress.Indexed,
		})
		return
	}

	// Detached from the request: the browser gets an answer immediately and the
	// work carries on. Bounded by its own timeout so a stuck vendor cannot
	// leave it running for the life of the process.
	go func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 2*time.Hour)
		defer cancel()
		if err := s.backfill(ctx, progress); err != nil {
			s.Log.Warn("recall backfill stopped", "error", err)
		}
	}()

	s.Log.Info("recall backfill started", "from_page", progress.LastPage+1, "by", actor.Email)
	writeJSON(w, http.StatusAccepted, map[string]any{
		"started": true, "from_page": progress.LastPage + 1,
	})
}

// backfill walks the helpdesk's ticket list, page by page, indexing what it
// finds. It stops at the end, on an error, or when the context expires.
func (s *Server) backfill(ctx context.Context, from store.RecallProgress) error {
	page := from.LastPage + 1
	indexed := from.Indexed

	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		tickets, err := s.searchWorkItems(ctx, map[string]any{
			"page": page, "per_page": backfillPageSize,
		})
		if err != nil {
			return err
		}
		if len(tickets) == 0 {
			break
		}

		for _, t := range tickets {
			if err := s.indexOne(ctx, t); err != nil {
				s.Log.Warn("could not index a ticket", "error", err)
				continue
			}
			indexed++
		}

		if err := s.DB.SetBackfillProgress(ctx, store.RecallProgress{
			Source: recallSource, LastPage: page, Indexed: indexed,
			Complete: len(tickets) < backfillPageSize,
		}); err != nil {
			return err
		}
		if len(tickets) < backfillPageSize {
			break
		}
		page++
	}

	s.Log.Info("recall backfill finished", "indexed", indexed)
	s.announce("recall")
	return nil
}

// indexOne turns one ticket into a row, fetching its thread for the resolution.
//
// A ticket summary carries no messages, and the resolution — the single most
// useful thing here — is almost always the last thing a technician wrote. So
// this costs one extra vendor call per ticket, which is why the backfill is
// paced and resumable rather than eager.
func (s *Server) indexOne(ctx context.Context, summary map[string]any) error {
	id := asString(summary["id"])
	if id == "" {
		return errors.New("a ticket with no id")
	}

	remembered := store.Remembered{
		Source:      recallSource,
		ExternalID:  id,
		Subject:     asString(summary["subject"]),
		Status:      asString(summary["status"]),
		ProblemType: asString(summary["problem_type"]),
		Customer:    asString(summary["customer"]),
		OpenedAt:    asTime(summary["created_at"]),
		ClosedAt:    asTime(summary["updated_at"]),
	}
	if raw := asString(summary["customer_id"]); raw != "" {
		if c, err := s.DB.ResolveIdentity(ctx, "psa", raw); err == nil {
			remembered.CustomerID = &c.ID
		}
	}

	// The thread, for what the problem was and what fixed it.
	full, err := s.getWorkItem(ctx, id)
	if err == nil {
		remembered.Problem, remembered.Resolution = problemAndResolution(full)
	}

	return s.DB.RememberTicket(ctx, remembered)
}

/*
problemAndResolution pulls the two halves worth keeping out of a thread.

The problem is what the customer first said. The resolution is the last thing
our side wrote, which is very nearly always the closing note — and when it is
not, it is still the most recent account of where the ticket got to, which is
what a reader wants.

Internal notes count. A note saying "replaced the PSU, all good" is the answer
even though the customer never saw it.
*/
func problemAndResolution(ticket map[string]any) (problem, resolution string) {
	comments, _ := ticket["comments"].([]any)
	for _, raw := range comments {
		c, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		body := strings.TrimSpace(asString(c["body"]))
		if body == "" {
			continue
		}
		if problem == "" && asString(c["from"]) == "customer" {
			problem = body
		}
		if asString(c["from"]) == "technician" {
			resolution = body
		}
	}
	// A ticket nobody replied to, or one raised by a technician: the first
	// thing said is still the problem.
	if problem == "" {
		for _, raw := range comments {
			if c, ok := raw.(map[string]any); ok {
				if body := strings.TrimSpace(asString(c["body"])); body != "" {
					problem = body
					break
				}
			}
		}
	}
	return problem, resolution
}

// --- reaching the helpdesk ----------------------------------------------------

// searchWorkItems asks whatever provides work item search for one page.
func (s *Server) searchWorkItems(ctx context.Context, args map[string]any) ([]map[string]any, error) {
	raw, err := s.callCapability(ctx, plugin.CapWorkItemsSearch, args)
	if err != nil {
		return nil, err
	}
	var page struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(raw, &page); err != nil {
		return nil, errors.New("the helpdesk returned a ticket list that could not be read")
	}
	return page.Items, nil
}

// getWorkItem fetches one ticket including its thread.
func (s *Server) getWorkItem(ctx context.Context, id string) (map[string]any, error) {
	raw, err := s.callCapability(ctx, plugin.CapWorkItemsGet, map[string]any{"id": asNumber(id)})
	if err != nil {
		return nil, err
	}
	var ticket map[string]any
	if err := json.Unmarshal(raw, &ticket); err != nil {
		return nil, errors.New("the helpdesk returned a ticket that could not be read")
	}
	return ticket, nil
}

/*
callCapability invokes a read capability from inside core.

Distinct from the routes a browser or the model reaches: there is no actor here
and no approval to check, because this is not somebody asking for something —
it is Azir maintaining its own index of work that has already happened, using
capabilities an administrator has already approved for reading.

It still refuses to resolve a write. The read index is the only one consulted,
which is the same guarantee every other path in this system has.
*/
func (s *Server) callCapability(ctx context.Context, capability plugin.Capability, args map[string]any) (json.RawMessage, error) {
	approved, err := s.DB.ApprovedTools(ctx)
	if err != nil {
		return nil, err
	}

	for _, qualified := range s.Reg.Providers(capability) {
		if _, ok := approved[qualified]; !ok {
			continue
		}
		pluginName, toolName, ok := strings.Cut(qualified, ".")
		if !ok {
			continue
		}
		tool, found := s.Reg.Lookup(pluginName, toolName)
		if !found || !tool.Available || tool.Mutates {
			continue
		}

		encoded, err := json.Marshal(args)
		if err != nil {
			return nil, err
		}
		payload, err := json.Marshal(plugin.Request{
			Actor: plugin.Actor{UserID: "azir", Role: "system"},
			Args:  encoded,
		})
		if err != nil {
			return nil, err
		}

		callCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()

		msg, err := s.NC.RequestWithContext(callCtx, tool.Subject, payload)
		if err != nil {
			return nil, err
		}
		if code := msg.Header.Get("Nats-Service-Error-Code"); code != "" {
			return nil, errors.New(msg.Header.Get("Nats-Service-Error"))
		}
		return msg.Data, nil
	}
	return nil, errors.New("nothing approved provides " + string(capability))
}

// --- reading whatever a plugin sent -------------------------------------------
//
// A capability's answer is shaped by whichever plugin served it, so these read
// defensively rather than asserting a type. A wrong guess here would panic the
// backfill on one odd ticket and lose the whole run.

func asString(v any) string {
	switch value := v.(type) {
	case string:
		return value
	case float64:
		// Whole numbers, because an id printed as 1.15794206e+08 matches nothing.
		return strconv.FormatInt(int64(value), 10)
	case bool:
		return strconv.FormatBool(value)
	case nil:
		return ""
	default:
		return ""
	}
}

func asNumber(s string) any {
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		return n
	}
	return s
}

func asTime(v any) *time.Time {
	s := asString(v)
	if s == "" {
		return nil
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05.999999999Z0700", "2006-01-02 15:04:05"} {
		if t, err := time.Parse(layout, s); err == nil {
			return &t
		}
	}
	return nil
}

/*
reindexRecent refreshes the index for whatever has changed lately.

Driven by a webhook rather than a clock. The body of that webhook is never
trusted — this refetches the recent page with Azir's own credentials and
indexes what it actually finds, which is the same rule every other reaction to a
webhook in this system follows.

Only the first page. A helpdesk sorts by recency, so the tickets that could have
changed are at the front; walking further would be doing the backfill's job on
every customer reply.

And only the tickets on it that actually moved. Indexing one ticket means
reading its whole thread, so re-indexing the page cost twenty-six requests
against a customer's helpdesk every time a single ticket changed — with the
caches for exactly those requests having just been dropped by the webhook that
triggered it, so not one of them could be served from memory. Syncro sends a
delivery for every comment and status change, and the rate limiter allows a
hundred and twenty a minute, so the ceiling was a few thousand requests a minute
against somebody else's API to re-read tickets that had not changed.

The page itself says when each ticket last moved, and the index already records
what it saw. Comparing the two costs nothing and is not a matter of trusting
anybody: both halves are things Azir read for itself. What is left is one
request for the page, plus one per ticket that genuinely changed — normally
one.
*/
func (s *Server) reindexRecent(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()

	tickets, err := s.searchWorkItems(ctx, map[string]any{"page": 1, "per_page": 25})
	if err != nil {
		s.Log.Warn("could not reindex after a webhook", "error", err)
		return
	}

	ids := make([]string, 0, len(tickets))
	for _, t := range tickets {
		if id := asString(t["id"]); id != "" {
			ids = append(ids, id)
		}
	}
	seen, err := s.DB.LastSeen(ctx, recallSource, ids)
	if err != nil {
		// Not fatal: without the comparison this does what it used to, which
		// is correct and merely expensive.
		s.Log.Warn("could not read what was already indexed", "error", err)
		seen = nil
	}

	var indexed, skipped int
	for _, t := range tickets {
		id := asString(t["id"])
		moved := asTime(t["updated_at"])
		if was, ok := seen[id]; ok && moved != nil && was.Equal(*moved) {
			skipped++
			continue
		}
		if err := s.indexOne(ctx, t); err != nil {
			s.Log.Warn("could not index a ticket", "error", err)
			continue
		}
		indexed++
	}
	s.Log.Info("reindexed after a webhook", "read", indexed, "unchanged", skipped)
}

/*
recallForModel answers the assistant's recall lookup.

Trimmed harder than the screen's version. A model reading eight full ticket
threads spends most of a context window on narration it will not use, so each
match is cut to the shape of an answer: what it was, what fixed it, when, and
for whom.

The customer filter is applied by Azir when asked for, never chosen freely — the
conversation already knows whose ticket it is, and a model that could search one
customer's history while helping with another's would be a way to carry one
client's details into another client's ticket.
*/
func (s *Server) recallForModel(ctx context.Context, onBehalf uuid.UUID, raw json.RawMessage) (json.RawMessage, error) {
	var args struct {
		Query        string `json:"query"`
		ThisCustomer bool   `json:"this_customer_only"`
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &args); err != nil {
			return nil, errors.New("that recall request could not be read")
		}
	}
	if strings.TrimSpace(args.Query) == "" {
		return nil, errors.New("what should I look for?")
	}

	query := store.RecallQuery{Text: args.Query, Limit: 5}
	if args.ThisCustomer && onBehalf != uuid.Nil {
		query.Customer = &onBehalf
	}

	matches, err := s.DB.Recall(ctx, query)
	if err != nil {
		return nil, err
	}

	type brief struct {
		Ticket     string `json:"ticket"`
		Subject    string `json:"subject"`
		Customer   string `json:"customer,omitempty"`
		Problem    string `json:"problem,omitempty"`
		Resolution string `json:"what_was_done,omitempty"`
		Closed     string `json:"closed,omitempty"`
	}
	out := make([]brief, 0, len(matches))
	for _, m := range matches {
		b := brief{
			Ticket:     m.ExternalID,
			Subject:    m.Subject,
			Customer:   m.Customer,
			Problem:    clip(m.Problem, 600),
			Resolution: clip(m.Resolution, 900),
		}
		if m.ClosedAt != nil {
			b.Closed = m.ClosedAt.Format("2006-01-02")
		}
		out = append(out, b)
	}

	return json.Marshal(map[string]any{
		"matches": out,
		"found":   len(out),
	})
}

// clip shortens a field for a model, on a word boundary so the tail is not a
// severed word that reads as corrupted text.
func clip(s string, max int) string {
	s = strings.TrimSpace(s)
	if len(s) <= max {
		return s
	}
	cut := s[:max]
	if i := strings.LastIndexAny(cut, " \n"); i > max/2 {
		cut = cut[:i]
	}
	return cut + "…"
}
