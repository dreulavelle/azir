package syncro

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/dreulavelle/azir/pkg/plugin"
)

// RateLimit is Syncro's documented ceiling: 180 requests per minute per IP.
// The client paces below it rather than discovering it by being throttled.
const RateLimit = 180

// maxPerPage bounds a single page. Syncro allows more on some endpoints, but a
// larger page mostly buys a bigger blob of model context for the same answer.
const maxPerPage = 100

// Client is a read-only Syncro API client.
//
// Every method is a GET. There are no allowlisted write-shaped reads, so the
// read-only invariant holds absolutely for this plugin: the transport refuses
// anything but GET, HEAD and OPTIONS.
type Client struct {
	http *plugin.HTTPClient

	// site is where a person opens this account in a browser, kept so a result
	// can carry a link back to the record it came from. Blank in tests, which
	// build a client against an arbitrary base URL and have no site to name.
	site string

	// The permission matrix, cached: preflight and every guarded call ask the
	// same question, and an administrator editing a token is rare.
	accessMu sync.Mutex
	access   Access
	accessAt time.Time
	accessOK bool
}

// Credentials are resolved per request by the caller-supplied function, so a
// key rotated in the console is picked up without restarting anything.
type CredentialFunc func(ctx context.Context) (string, error)

// New builds a client for one Syncro subdomain.
//
// The subdomain is validated rather than interpolated blindly: it becomes part
// of a hostname, and a value containing a slash or a dot would otherwise point
// the client at somewhere the administrator never configured.
func New(subdomain string, credential CredentialFunc) (*Client, error) {
	subdomain = strings.TrimSpace(strings.ToLower(subdomain))
	if !validSubdomain(subdomain) {
		return nil, fmt.Errorf("syncro: %q is not a valid subdomain", subdomain)
	}

	httpClient, err := plugin.NewHTTPClient(plugin.HTTPConfig{
		BaseURL:           "https://" + subdomain + ".syncromsp.com/api/v1",
		RequestsPerMinute: RateLimit,
		Burst:             10,
		MaxRetries:        3,
		ExplainError:      explainSyncroError,
		// The write surface, enumerated rather than opened. Everything not
		// listed here is refused by the transport, so the plugin cannot make a
		// write it was not designed to make even if a handler tried.
		AllowedWritePaths: []plugin.MethodPath{
			{Method: http.MethodPost, Prefix: "/tickets", Why: "post a comment to a ticket"},
			{Method: http.MethodPut, Prefix: "/tickets", Why: "change status, assignee or customer"},
		},
	}, func(ctx context.Context, r *http.Request) error {
		token, err := credential(ctx)
		if err != nil {
			return err
		}
		// The Bearer header, never the ?api_key= query parameter Syncro's
		// guide also documents: query strings persist in proxy logs, error
		// strings and access logs long after anyone remembers they held a key.
		r.Header.Set("Authorization", "Bearer "+token)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &Client{http: httpClient, site: "https://" + subdomain + ".syncromsp.com"}, nil
}

// NewForTest builds a client against an arbitrary base URL.
//
// Exported for tests only: New derives the hostname from a validated
// subdomain, which is exactly the property worth keeping, so tests of trimming
// and paging need a way past it that production code never uses.
//
// It carries the same write allowlist as New, deliberately. A test client that
// refused every write could not cover the write path at all — which is how the
// write path came to have no tests.
func NewForTest(baseURL string, credential CredentialFunc) (*Client, error) {
	httpClient, err := plugin.NewHTTPClient(plugin.HTTPConfig{
		BaseURL:           baseURL,
		RequestsPerMinute: 60000,
		Burst:             100,
		MaxRetries:        0,
		AllowedWritePaths: []plugin.MethodPath{
			{Method: http.MethodPost, Prefix: "/tickets", Why: "post a comment to a ticket"},
			{Method: http.MethodPut, Prefix: "/tickets", Why: "change status, assignee or customer"},
		},
	}, func(ctx context.Context, r *http.Request) error {
		token, err := credential(ctx)
		if err != nil {
			return err
		}
		r.Header.Set("Authorization", "Bearer "+token)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &Client{http: httpClient}, nil
}

// explainSyncroError pulls the validation message out of a Syncro rejection.
//
// Syncro answers a bad request with {"success":false,"message":["..."]}, which
// is precisely the actionable part and contains none of the request. Anything
// unrecognised is dropped rather than passed through, because an unknown shape
// is exactly where a credential could be hiding.
func explainSyncroError(status int, body []byte) string {
	if status >= 500 {
		return ""
	}
	var shape struct {
		Message any `json:"message"`
	}
	if err := json.Unmarshal(body, &shape); err != nil {
		return ""
	}
	switch m := shape.Message.(type) {
	case string:
		return truncate(m, 300)
	case []any:
		parts := make([]string, 0, len(m))
		for _, item := range m {
			if s, ok := item.(string); ok {
				parts = append(parts, s)
			}
		}
		return truncate(strings.Join(parts, "; "), 300)
	}
	return ""
}

// validSubdomain accepts only what can safely become a hostname label.
func validSubdomain(s string) bool {
	if s == "" || len(s) > 63 {
		return false
	}
	for i, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
		case r == '-' && i != 0 && i != len(s)-1:
		default:
			return false
		}
	}
	return true
}

// ListCustomers returns a page of customers, optionally filtered by a search
// string.
func (c *Client) ListCustomers(ctx context.Context, query string, page, perPage int) (Result[Customer], error) {
	q := paging(page, perPage)
	if query != "" {
		q.Set("query", query)
	}

	var body struct {
		Customers []wireCustomer `json:"customers"`
		Meta      wireMeta       `json:"meta"`
	}
	if err := c.get(ctx, "/customers", q, &body); err != nil {
		return Result[Customer]{}, err
	}

	out := Result[Customer]{Items: make([]Customer, 0, len(body.Customers))}
	for _, w := range body.Customers {
		out.Items = append(out.Items, w.trim())
	}
	out.Page = pageOf(body.Meta, page, perPage)
	return out, nil
}

// GetCustomer returns one customer.
func (c *Client) GetCustomer(ctx context.Context, id int64) (Customer, error) {
	var body struct {
		Customer wireCustomer `json:"customer"`
	}
	if err := c.get(ctx, "/customers/"+strconv.FormatInt(id, 10), nil, &body); err != nil {
		return Customer{}, err
	}
	return body.Customer.trim(), nil
}

// SearchTickets returns a page of tickets. Comments are omitted: a search
// returning every thread for every hit would swamp the context window, and the
// caller can fetch the one ticket it cares about.
func (c *Client) SearchTickets(ctx context.Context, opts TicketSearch) (Result[Ticket], error) {
	if !opts.OpenOnly {
		return c.ticketPage(ctx, opts, opts.Page)
	}

	want := opts.PerPage
	if want <= 0 || want > maxPerPage {
		want = maxPerPage
	}
	from := opts.Page
	if from <= 0 {
		from = 1
	}

	out := Result[Ticket]{Items: []Ticket{}}
	for read := range maxOpenScan {
		got, err := c.ticketPage(ctx, opts, from+read)
		if err != nil {
			return Result[Ticket]{}, err
		}
		for _, t := range got.Items {
			if !IsDone(t.Status) {
				out.Items = append(out.Items, t)
			}
		}
		// Page reports how far through Syncro's own list this reached, which is
		// what a caller asking for more has to say next. It is deliberately not
		// a count of open tickets: nobody knows that without reading all of
		// them, and inventing the number would be the same lie as before.
		out.Page = got.Page

		done := len(got.Items) == 0 || got.Page.Page >= got.Page.TotalPages
		if len(out.Items) >= want || done {
			break
		}
	}
	return out, nil
}

// ticketPage fetches exactly one page of Syncro's ticket list.
func (c *Client) ticketPage(ctx context.Context, opts TicketSearch, page int) (Result[Ticket], error) {
	q := paging(page, opts.PerPage)
	if opts.Query != "" {
		q.Set("query", opts.Query)
	}
	if opts.CustomerID > 0 {
		q.Set("customer_id", strconv.FormatInt(opts.CustomerID, 10))
	}
	if opts.Status != "" {
		q.Set("status", opts.Status)
	}

	var body struct {
		Tickets []wireTicket `json:"tickets"`
		Meta    wireMeta     `json:"meta"`
	}
	if err := c.get(ctx, "/tickets", q, &body); err != nil {
		return Result[Ticket]{}, err
	}

	out := Result[Ticket]{Items: make([]Ticket, 0, len(body.Tickets))}
	for _, w := range body.Tickets {
		out.Items = append(out.Items, c.withLink(w.trim(false)))
	}
	out.Page = pageOf(body.Meta, page, opts.PerPage)
	return out, nil
}

// maxOpenScan bounds how many of Syncro's pages an open-only search will read.
//
// Filtering belongs here rather than in the browser because the alternative
// spends the whole page budget on tickets nobody will act on: a hundred rows
// fetched, eighty of them resolved, and the open ticket from three months ago —
// the one most worth finding — never reached at all. Five pages fills a screen
// from any realistic queue and stays far inside Syncro's rate limit.
const maxOpenScan = 5

// withLink stamps a ticket with where a person can open it in Syncro.
//
// A technician reading a ticket here regularly needs to do something to it that
// Azir deliberately cannot, and the alternative to a link is retyping a number
// into another tab.
func (c *Client) withLink(t Ticket) Ticket {
	if c.site != "" && t.ID > 0 {
		t.URL = c.site + "/tickets/" + strconv.FormatInt(t.ID, 10)
	}
	return t
}

// TicketSearch are the filters SearchTickets understands.
type TicketSearch struct {
	Query      string
	CustomerID int64
	Status     string
	// OpenOnly drops tickets whose status means the work is finished, reading
	// further into Syncro's list to make up the difference.
	OpenOnly bool
	Page     int
	PerPage  int
}

// GetTicket returns one ticket including its comment thread.
func (c *Client) GetTicket(ctx context.Context, id int64) (Ticket, error) {
	var body struct {
		Ticket wireTicket `json:"ticket"`
	}
	if err := c.get(ctx, "/tickets/"+strconv.FormatInt(id, 10), nil, &body); err != nil {
		return Ticket{}, err
	}
	return c.withLink(body.Ticket.trim(true)), nil
}

// ListAssets returns a page of customer assets.
func (c *Client) ListAssets(ctx context.Context, customerID int64, page, perPage int) (Result[Asset], error) {
	q := paging(page, perPage)
	if customerID > 0 {
		q.Set("customer_id", strconv.FormatInt(customerID, 10))
	}

	var body struct {
		Assets []wireAsset `json:"assets"`
		Meta   wireMeta    `json:"meta"`
	}
	if err := c.get(ctx, "/customer_assets", q, &body); err != nil {
		return Result[Asset]{}, err
	}

	out := Result[Asset]{Items: make([]Asset, 0, len(body.Assets))}
	for _, w := range body.Assets {
		out.Items = append(out.Items, w.trim())
	}
	out.Page = pageOf(body.Meta, page, perPage)
	return out, nil
}

// get performs a request and decodes it.
//
// A decode failure reports the endpoint but never the body: a vendor response
// can contain anything, and this error may be surfaced to a caller.
func (c *Client) get(ctx context.Context, path string, query url.Values, into any) error {
	raw, err := c.http.Do(ctx, http.MethodGet, path, query, nil)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(raw, into); err != nil {
		return plugin.Errorf("502", "syncro returned a response this plugin could not read")
	}
	return nil
}

func paging(page, perPage int) url.Values {
	if page < 1 {
		page = 1
	}
	if perPage < 1 {
		perPage = 25
	}
	if perPage > maxPerPage {
		perPage = maxPerPage
	}
	q := url.Values{}
	q.Set("page", strconv.Itoa(page))
	q.Set("per_page", strconv.Itoa(perPage))
	return q
}

func pageOf(meta wireMeta, page, perPage int) Page {
	if page < 1 {
		page = 1
	}
	if perPage < 1 {
		perPage = 25
	}
	if perPage > maxPerPage {
		perPage = maxPerPage
	}
	return Page{
		Page:       page,
		PerPage:    perPage,
		TotalPages: meta.TotalPages,
		TotalCount: meta.TotalCount,
	}
}

// --- small helpers ----------------------------------------------------------

func truncate(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	return s[:limit] + "… [truncated]"
}

func joinNonEmpty(sep string, parts ...string) string {
	kept := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			kept = append(kept, p)
		}
	}
	return strings.Join(kept, sep)
}

// stringify renders a JSON value that a vendor may send as either a string or
// a number — ticket numbers are the usual offender.
func stringify(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(t)
	default:
		return fmt.Sprint(t)
	}
}

// clientCache keeps one Client per subdomain.
//
// This matters more than it looks: the rate limiter lives inside the HTTP
// client, so building a fresh client per request would reset the limiter and
// silently defeat the pacing that keeps us under Syncro's 180/minute.
type clientCache struct {
	mu      sync.Mutex
	clients map[string]*Client
}

// NewCache returns an empty cache.
func NewCache() *clientCache { //nolint:revive // deliberately unexported type
	return &clientCache{clients: map[string]*Client{}}
}

// For returns the client for a subdomain, building it once.
func (c *clientCache) For(subdomain string, credential CredentialFunc) (*Client, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if existing, ok := c.clients[subdomain]; ok {
		return existing, nil
	}
	client, err := New(subdomain, credential)
	if err != nil {
		return nil, err
	}
	c.clients[subdomain] = client
	return client, nil
}
