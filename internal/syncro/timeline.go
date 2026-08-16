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

// Access is what an API token is permitted to do, read back from Syncro rather
// than assumed.
//
// Azir needs read on tickets, customers and assets and nothing else. Reporting
// what a token actually grants lets an administrator verify least privilege
// from inside Azir instead of squinting at checkboxes in another product — and
// makes an over-permissioned token visible rather than invisible.
type Access struct {
	User      string `json:"user"`
	Email     string `json:"email"`
	Subdomain string `json:"subdomain"`
	Admin     bool   `json:"admin"`

	// Grants is the permission matrix, resource to action to allowed.
	Grants map[string]map[string]bool `json:"grants"`

	// Required lists the permissions Azir needs and whether each is present.
	Required map[string]bool `json:"required"`
	// Excessive lists permissions the token holds that Azir never uses. Every
	// one is risk without capability.
	Excessive []string `json:"excessive"`
	// Sufficient reports whether every required permission is present.
	Sufficient bool `json:"sufficient"`
}

// requiredGrants is the complete set Azir uses. Anything beyond this is
// unnecessary for Azir, whatever else it may be needed for.
var requiredGrants = []string{"ticket.read", "customer.read", "asset.read"}

// dangerousGrants are the ones worth naming explicitly when present, because
// their blast radius is much larger than "extra".
var dangerousGrants = map[string]string{
	"script.execute":  "can run scripts on customer machines",
	"ticket.delete":   "can delete tickets",
	"customer.delete": "can delete customers",
	"asset.delete":    "can delete assets",
	"invoice.delete":  "can delete invoices",
	"payment.delete":  "can delete payments",
}

// CheckAccess reports what the configured token can do.
func (c *Client) CheckAccess(ctx context.Context) (Access, error) {
	var body struct {
		UserName    string                     `json:"user_name"`
		UserEmail   string                     `json:"user_email"`
		Subdomain   string                     `json:"subdomain"`
		Admin       bool                       `json:"admin"`
		Permissions map[string]map[string]bool `json:"permissions"`
	}
	if err := c.get(ctx, "/me", nil, &body); err != nil {
		return Access{}, err
	}

	a := Access{
		User:      body.UserName,
		Email:     body.UserEmail,
		Subdomain: body.Subdomain,
		Admin:     body.Admin,
		Grants:    body.Permissions,
		Required:  map[string]bool{},
	}

	held := map[string]bool{}
	for resource, actions := range body.Permissions {
		for action, allowed := range actions {
			if allowed {
				held[resource+"."+action] = true
			}
		}
	}

	a.Sufficient = true
	for _, need := range requiredGrants {
		a.Required[need] = held[need]
		if !held[need] {
			a.Sufficient = false
		}
	}

	needed := map[string]bool{}
	for _, need := range requiredGrants {
		needed[need] = true
	}
	for grant := range held {
		if needed[grant] {
			continue
		}
		if why, dangerous := dangerousGrants[grant]; dangerous {
			a.Excessive = append(a.Excessive, grant+" ("+why+")")
			continue
		}
		a.Excessive = append(a.Excessive, grant)
	}
	sort.Strings(a.Excessive)

	return a, nil
}

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
	FirstResponseMinutes int  `json:"first_response_minutes,omitempty"`
	LongestGapHours      int  `json:"longest_gap_hours,omitempty"`
	RoundTrips           int  `json:"round_trips"`
	TotalLoggedMinutes   int  `json:"total_logged_minutes"`
	Stale                bool `json:"stale"`

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

	for _, cm := range ticket.Comments {
		actor := cm.TechName
		kind := "comment"
		if actor == "" {
			// No tech means it came from the customer side.
			actor, kind = ticket.Customer, "customer_message"
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
		tl.Notes = append(tl.Notes, "time entries could not be read; the timeline is otherwise complete")
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

	// Syncro exposes no per-change history, so status transitions cannot be
	// placed on the timeline. Saying so is better than letting a reader assume
	// the sequence is complete.
	tl.Notes = append(tl.Notes,
		"status changes are not in this timeline: Syncro's API exposes no per-change history")

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
// The shape is inferred from Syncro's documented filters rather than observed:
// the trial account had no timers, so the field names below are a best effort
// and several aliases are decoded for each value.
type Timer struct {
	ID         int64  `json:"id"`
	TicketID   int64  `json:"ticket_id"`
	CustomerID int64  `json:"customer_id,omitempty"`
	User       string `json:"user,omitempty"`
	Notes      string `json:"notes,omitempty"`
	Minutes    int    `json:"minutes"`
	Billable   bool   `json:"billable"`
	StartedAt  string `json:"started_at,omitempty"`
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

type wireTimer struct {
	ID         int64 `json:"id"`
	TicketID   int64 `json:"ticket_id"`
	CustomerID int64 `json:"customer_id"`
	// Several aliases per value: the live shape is unverified, and decoding a
	// few plausible names costs nothing while a wrong single guess costs a
	// silently empty field.
	Notes         string `json:"notes"`
	Comment       string `json:"comment"`
	UserName      string `json:"user_name"`
	Tech          string `json:"tech"`
	Minutes       int    `json:"minutes"`
	DurationMins  int    `json:"duration_minutes"`
	BillingStatus string `json:"billing_status"`
	Billable      *bool  `json:"billable"`
	StartAt       string `json:"start_at"`
	StartedAt     string `json:"started_at"`
	CreatedAt     string `json:"created_at"`
}

func (w wireTimer) trim() Timer {
	t := Timer{
		ID:         w.ID,
		TicketID:   w.TicketID,
		CustomerID: w.CustomerID,
		User:       firstNonEmpty(w.UserName, w.Tech),
		Notes:      truncate(firstNonEmpty(w.Notes, w.Comment), 1000),
		Minutes:    max(w.Minutes, w.DurationMins),
		StartedAt:  firstNonEmpty(w.StartAt, w.StartedAt, w.CreatedAt),
	}
	switch {
	case w.Billable != nil:
		t.Billable = *w.Billable
	default:
		t.Billable = !strings.EqualFold(w.BillingStatus, "Non-Billable")
	}
	return t
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
