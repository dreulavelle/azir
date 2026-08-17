package syncro

import (
	"context"
	"html"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// TimelineEntry is one dated thing that happened to a ticket.
type TimelineEntry struct {
	At     string `json:"at"`
	Kind   string `json:"kind"`
	Actor  string `json:"actor,omitempty"`
	Body   string `json:"body,omitempty"`
	Hidden bool   `json:"hidden,omitempty"`
	// Minutes is set on time entries.
	Minutes int `json:"minutes,omitempty"`
}

// Timeline is a ticket's full history in one chronological sequence.
//
// Syncro exposes the pieces separately — the ticket, its comments, its timers
// — and reconstructing the order by hand across them is exactly the work a
// technician currently does mentally. Merging them is most of the value.
type Timeline struct {
	Ticket  Ticket          `json:"ticket"`
	Entries []TimelineEntry `json:"entries"`

	// Computed signals. These are arithmetic rather than judgement, so they
	// belong here and not in a model call.
	FirstResponseMinutes int `json:"first_response_minutes,omitempty"`
	LongestGapHours      int `json:"longest_gap_hours,omitempty"`
	RoundTrips           int `json:"round_trips"`
	// RoundTripsUnknown means the sides could not be told apart, so RoundTrips
	// is absence of evidence rather than evidence of absence.
	RoundTripsUnknown  bool `json:"round_trips_unknown,omitempty"`
	TotalLoggedMinutes int  `json:"total_logged_minutes"`
	Stale              bool `json:"stale"`

	// Notes explains anything the timeline could not include, so a caller is
	// never left assuming completeness it does not have.
	Notes []string `json:"notes,omitempty"`
}

// GetTimeline assembles a ticket's history.
func (c *Client) GetTimeline(ctx context.Context, id int64) (Timeline, error) {
	ticket, err := c.GetTicket(ctx, id)
	if err != nil {
		return Timeline{}, err
	}

	tl := Timeline{Ticket: ticket}
	if ticket.CreatedAt != "" {
		tl.Entries = append(tl.Entries, TimelineEntry{
			At: ticket.CreatedAt, Kind: "created", Actor: ticket.Customer,
			Body: ticket.Subject,
		})
	}

	customerMessages := 0
	for _, cm := range ticket.Comments {
		actor := cm.TechName
		kind := "comment"
		if fromCustomer(cm) {
			actor, kind = ticket.Customer, "customer_message"
			customerMessages++
		}
		tl.Entries = append(tl.Entries, TimelineEntry{
			At: cm.CreatedAt, Kind: kind, Actor: actor,
			Body: cm.Body, Hidden: cm.Hidden,
		})
	}

	// Time entries live on a separate endpoint and may be unavailable if the
	// token lacks the permission; a timeline without them is still useful, so
	// this degrades rather than fails.
	timers, err := c.TicketTimers(ctx, TimerSearch{TicketID: id, PerPage: 100})
	if err != nil {
		tl.Notes = append(tl.Notes, "Logged time could not be read, so it is missing from this history. Everything else is here.")
	} else {
		for _, t := range timers.Items {
			tl.TotalLoggedMinutes += t.Minutes
			tl.Entries = append(tl.Entries, TimelineEntry{
				At: t.StartedAt, Kind: "time_logged", Actor: t.User,
				Body: t.Notes, Minutes: t.Minutes,
			})
		}
	}

	sort.SliceStable(tl.Entries, func(i, j int) bool {
		return tl.Entries[i].At < tl.Entries[j].At
	})

	tl.computeSignals()

	// Which side a message came from is what makes "back and forth" mean
	// anything. Syncro stamps a technician name on every comment created
	// through its API — including ones marked as coming from the customer — so
	// a thread can arrive with no distinguishable customer side at all.
	// Reporting zero round trips there would be a finding rather than a gap,
	// so the gap is named instead.
	if customerMessages == 0 && len(ticket.Comments) > 0 {
		tl.RoundTripsUnknown = true
		tl.Notes = append(tl.Notes,
			"Every message on this ticket is filed under a technician, so the customer's replies cannot be told apart from ours.")
	}

	// Syncro exposes no per-change history, so status transitions cannot be
	// placed on the timeline. Saying so is better than letting a reader assume
	// the sequence is complete.
	tl.Notes = append(tl.Notes,
		"Status changes are not shown here. Syncro does not keep a record of them that we can read.")

	return tl, nil
}

// computeSignals derives the arithmetic a reader would otherwise do by eye.
func (tl *Timeline) computeSignals() {
	var created, firstTechReply string
	var previous string
	var lastKind string

	for _, e := range tl.Entries {
		switch e.Kind {
		case "created":
			created = e.At
		case "comment":
			if firstTechReply == "" {
				firstTechReply = e.At
			}
		}

		// A round trip is the conversation changing sides. Frequent switching
		// is a reliable signal that something is being miscommunicated.
		if (e.Kind == "comment" || e.Kind == "customer_message") &&
			lastKind != "" && lastKind != e.Kind {
			tl.RoundTrips++
		}
		if e.Kind == "comment" || e.Kind == "customer_message" {
			lastKind = e.Kind
		}

		if previous != "" {
			if gap := hoursBetween(previous, e.At); gap > tl.LongestGapHours {
				tl.LongestGapHours = gap
			}
		}
		previous = e.At
	}

	if created != "" && firstTechReply != "" {
		tl.FirstResponseMinutes = minutesBetween(created, firstTechReply)
	}
	// A ticket whose last activity is far in the past has gone quiet, which is
	// the thing worth surfacing rather than the raw timestamp.
	if len(tl.Entries) > 0 {
		tl.Stale = hoursBetween(tl.Entries[len(tl.Entries)-1].At, nowRFC3339()) > 24*7
	}
}

// Timer is one logged block of work on a ticket.
//
// Shape verified against a live entry. Durations arrive in seconds under
// active_duration and billable_time, and are converted to minutes here because
// a technician thinks in minutes and a model reading "900" will guess wrongly.
//
// A timer carries no customer id, so filtering by customer cannot be done
// client-side — Syncro's documented customer_id query parameter is the only
// way, and that is what the search below relies on.
type Timer struct {
	ID       int64  `json:"id"`
	TicketID int64  `json:"ticket_id"`
	UserID   int64  `json:"user_id,omitempty"`
	User     string `json:"user,omitempty"`
	Notes    string `json:"notes,omitempty"`
	// Minutes is the time actually worked; BillableMinutes may differ when a
	// technician has overridden what the customer is charged for.
	Minutes         int    `json:"minutes"`
	BillableMinutes int    `json:"billable_minutes"`
	Billable        bool   `json:"billable"`
	Recorded        bool   `json:"recorded"`
	Status          string `json:"status,omitempty"`
	StartedAt       string `json:"started_at,omitempty"`
	EndedAt         string `json:"ended_at,omitempty"`
}

// TimerSearch filters ticket timers. The time window and customer filters are
// what an unlogged-work report needs: find the calls, then ask whether any
// time was booked against that customer in the same window.
type TimerSearch struct {
	TicketID   int64
	CustomerID int64
	Since      string // RFC3339
	Until      string // RFC3339
	Page       int
	PerPage    int
}

// TicketTimers returns logged time entries.
func (c *Client) TicketTimers(ctx context.Context, opts TimerSearch) (Result[Timer], error) {
	q := paging(opts.Page, opts.PerPage)
	if opts.CustomerID > 0 {
		q.Set("customer_id", strconv.FormatInt(opts.CustomerID, 10))
	}
	if opts.Since != "" {
		q.Set("created_at_gt", opts.Since)
	}
	if opts.Until != "" {
		q.Set("created_at_lt", opts.Until)
	}

	var body struct {
		Timers []wireTimer `json:"ticket_timers"`
		Meta   wireMeta    `json:"meta"`
	}
	if err := c.get(ctx, "/ticket_timers", q, &body); err != nil {
		return Result[Timer]{}, err
	}

	out := Result[Timer]{Items: make([]Timer, 0, len(body.Timers))}
	for _, w := range body.Timers {
		t := w.trim()
		// Syncro has no ticket_id filter on this endpoint, so a single
		// ticket's timers are selected here rather than in the query.
		if opts.TicketID > 0 && t.TicketID != opts.TicketID {
			continue
		}
		out.Items = append(out.Items, t)
	}
	out.Page = pageOf(body.Meta, opts.Page, opts.PerPage)
	return out, nil
}

// WikiPage is one documentation article.
type WikiPage struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	Slug string `json:"slug"`
	Body string `json:"body,omitempty"`
}

// SearchWiki finds documentation by title or content.
//
// Syncro has no wiki search endpoint, so this fetches pages and filters
// locally. That is acceptable because a wiki is small — tens of pages, not
// thousands — and it is the difference between an assistant that knows your
// documented procedures and one that guesses at them.
func (c *Client) SearchWiki(ctx context.Context, query string, includeBody bool) ([]WikiPage, error) {
	var body struct {
		Pages []struct {
			ID   int64  `json:"id"`
			Name string `json:"name"`
			Slug string `json:"slug"`
			Body string `json:"body"`
		} `json:"wiki_pages"`
	}
	q := url.Values{}
	q.Set("per_page", "100")
	if err := c.get(ctx, "/wiki_pages", q, &body); err != nil {
		return nil, err
	}

	needle := strings.ToLower(strings.TrimSpace(query))
	out := []WikiPage{}
	for _, p := range body.Pages {
		// Bodies are HTML. Markup is noise that costs context and occasionally
		// confuses a summariser, so it is stripped before matching or return.
		text := stripHTML(p.Body)
		if needle != "" &&
			!strings.Contains(strings.ToLower(p.Name), needle) &&
			!strings.Contains(strings.ToLower(text), needle) {
			continue
		}
		page := WikiPage{ID: p.ID, Name: p.Name, Slug: p.Slug}
		if includeBody {
			page.Body = truncate(text, 8000)
		}
		out = append(out, page)
	}
	return out, nil
}

// wireTimer mirrors a live ticket_timers entry.
type wireTimer struct {
	ID             int64  `json:"id"`
	TicketID       int64  `json:"ticket_id"`
	UserID         int64  `json:"user_id"`
	Notes          string `json:"notes"`
	Billable       bool   `json:"billable"`
	Recorded       bool   `json:"recorded"`
	Status         string `json:"status"`
	StartTime      string `json:"start_time"`
	EndTime        string `json:"end_time"`
	CreatedAt      string `json:"created_at"`
	ActiveDuration int    `json:"active_duration"`
	BillableTime   int    `json:"billable_time"`
	ElapsedSeconds int    `json:"elapsed_seconds"`
}

func (w wireTimer) trim() Timer {
	return Timer{
		ID:       w.ID,
		TicketID: w.TicketID,
		UserID:   w.UserID,
		Notes:    truncate(w.Notes, 1000),
		// Seconds to minutes: a technician thinks in minutes, and a model
		// reading a bare 900 will guess the unit wrongly.
		Minutes:         secondsToMinutes(max(w.ActiveDuration, w.ElapsedSeconds)),
		BillableMinutes: secondsToMinutes(w.BillableTime),
		Billable:        w.Billable,
		Recorded:        w.Recorded,
		Status:          w.Status,
		StartedAt:       firstNonEmpty(w.StartTime, w.CreatedAt),
		EndedAt:         w.EndTime,
	}
}

// secondsToMinutes rounds to the nearest minute, so a 90-second entry reads as
// 2 rather than disappearing.
func secondsToMinutes(seconds int) int {
	if seconds <= 0 {
		return 0
	}
	return (seconds + 30) / 60
}

// --- text helpers -----------------------------------------------------------

var (
	tagPattern        = regexp.MustCompile(`(?s)<[^>]*>`)
	whitespacePattern = regexp.MustCompile(`[ \t]*\n[ \t]*(\n[ \t]*)+`)
)

// stripHTML renders markup as readable text. Block-level tags become newlines
// so structure survives, which matters when the content is a procedure.
func stripHTML(s string) string {
	if s == "" || !strings.Contains(s, "<") {
		return s
	}
	replacer := strings.NewReplacer(
		"</p>", "\n\n", "<br>", "\n", "<br/>", "\n", "<br />", "\n",
		"</div>", "\n", "</li>", "\n", "<li>", "• ",
		"</h1>", "\n\n", "</h2>", "\n\n", "</h3>", "\n\n", "</tr>", "\n",
	)
	s = replacer.Replace(s)
	s = tagPattern.ReplaceAllString(s, "")
	s = html.UnescapeString(s)
	s = whitespacePattern.ReplaceAllString(s, "\n\n")
	return strings.TrimSpace(s)
}

// Syncro timestamps arrive in several shapes across endpoints, so parsing is
// tolerant: a signal that cannot be computed is better than one computed from
// a misread date.
func parseTS(s string) (time.Time, bool) {
	for _, layout := range []string{
		time.RFC3339Nano, time.RFC3339,
		"2006-01-02T15:04:05.999-07:00",
		"2006-01-02 15:04:05 -0700",
		"2006-01-02",
	} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

func minutesBetween(from, to string) int {
	a, ok1 := parseTS(from)
	b, ok2 := parseTS(to)
	if !ok1 || !ok2 || b.Before(a) {
		return 0
	}
	return int(b.Sub(a).Minutes())
}

func hoursBetween(from, to string) int {
	return minutesBetween(from, to) / 60
}

func nowRFC3339() string { return time.Now().UTC().Format(time.RFC3339) }

// fromCustomer decides which side a comment came from.
//
// Syncro's own convention is the subject line: its portal writes "Customer
// Reply" on anything the customer sends and "Reply" on a technician's. That is
// more reliable than the tech field, which is filled in with the API key's
// owner for everything created through the API — so a thread built by an
// integration would otherwise look entirely one-sided.
func fromCustomer(cm Comment) bool {
	if strings.Contains(strings.ToLower(cm.Subject), "customer") {
		return true
	}
	// No technician attributed at all is the other clear case.
	return strings.TrimSpace(cm.TechName) == ""
}
