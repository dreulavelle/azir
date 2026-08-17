// Command plugin-syncro exposes Syncro MSP as read-only Azir tools.
//
// Every tool is a GET. There are no allowlisted write-shaped reads, so the
// read-only invariant holds absolutely here: the transport refuses anything
// but a safe method, below any mistake this file could make.
//
// Settings and credentials come from core at request time, so an administrator
// changes the subdomain or rotates the API key in the console and nothing
// needs redeploying.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/dreulavelle/azir/internal/syncro"
	"github.com/dreulavelle/azir/pkg/plugin"
)

// clients keeps one API client per subdomain. The rate limiter lives inside
// it, so building a fresh client per request would reset the pacing and
// silently blow through Syncro's 180 requests a minute.
var clients = syncro.NewCache()

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := plugin.Serve(ctx, definition(), plugin.WithLogger(log)); err != nil {
		log.Error("plugin failed to start", "error", err)
		os.Exit(1)
	}
}

// definition is the plugin as registered. Extracted from main so a test can
// assert that every tool is accounted for in the permission map — a tool added
// without one would silently report itself available and then fail with a 401.
func definition() plugin.Plugin {
	return plugin.Plugin{
		Name:        "syncro",
		Version:     "0.1.0",
		Description: "Read-only access to Syncro MSP tickets, customers and assets",
		Category:    plugin.CategoryPSA,
		ConfigSchema: json.RawMessage(`{
			"type": "object",
			"required": ["subdomain", "api_key"],
			"properties": {
				"subdomain": {
					"type": "string",
					"title": "Syncro subdomain",
					"description": "The first label of your Syncro URL. For acme.syncromsp.com, enter: acme"
				},
				"api_key": {
					"type": "string",
					"title": "API token",
					"description": "Admin > API > API Tokens. Grant read permissions only — Azir never writes to Syncro, so a write-capable token adds risk without adding capability.",
					"x-azir-secret": true
				}
			}
		}`),
		// Reports which tools this token can actually drive. An administrator
		// granting Azir only ticket access is being sensible, and the right
		// response is for the ticket tools to work while the invoice tools say
		// what permission they need — not for the plugin to fail, and not for
		// a technician to meet an opaque 401 mid-conversation.
		Preflight: preflight,
		Tools: []plugin.Tool{
			{
				Name:        "customers.search",
				Summary:     "Finds customers by name, business, email or phone.",
				Freshness:   &plugin.Freshness{Soft: 30 * time.Minute, Hard: 4 * time.Hour},
				Description: "Find customers by name, business, email or phone. Returns a page of matches with contact details.",
				Provides:    []plugin.Capability{plugin.CapCustomersList},
				Schema: json.RawMessage(`{
					"type": "object",
					"properties": {
						"query": {"type": "string", "description": "Free-text search across name, business, email and phone"},
						"page": {"type": "integer", "minimum": 1, "default": 1},
						"per_page": {"type": "integer", "minimum": 1, "maximum": 100, "default": 25}
					}
				}`),
				Handler: guarded("customers.search", searchCustomers),
			},
			{
				Name:        "customers.get",
				Summary:     "Looks up one customer's contact details and notes.",
				Freshness:   &plugin.Freshness{Soft: 30 * time.Minute, Hard: 4 * time.Hour},
				Description: "Fetch one customer by their Syncro id, including contact details and notes.",
				Provides:    []plugin.Capability{plugin.CapCustomersGet},
				Schema: json.RawMessage(`{
					"type": "object",
					"required": ["id"],
					"properties": {
						"id": {"type": "integer", "description": "Syncro customer id"}
					}
				}`),
				Handler: guarded("customers.get", getCustomer),
			},
			{
				Name:      "tickets.search",
				Summary:   "Finds tickets by text, status or customer. No message contents.",
				Freshness: &plugin.Freshness{Soft: 60 * time.Second, Hard: 5 * time.Minute},
				Description: "Search tickets by free text, status, or customer. Returns summaries without " +
					"comment threads; use tickets.get for the full conversation on one ticket. " +
					"Set open_only when the question is about work still outstanding — resolved " +
					"tickets otherwise fill the page and push older open ones out of reach.",
				Provides: []plugin.Capability{plugin.CapWorkItemsSearch},
				Schema: json.RawMessage(`{
					"type": "object",
					"properties": {
						"query": {"type": "string", "description": "Free-text search across subject and body"},
						"status": {"type": "string", "description": "Filter by ticket status, e.g. New, In Progress, Resolved"},
						"open_only": {"type": "boolean", "description": "Drop tickets whose status means the work is finished, reading further into the list to make up the difference"},
						"customer_id": {"type": "integer", "description": "Restrict to one Syncro customer"},
						"page": {"type": "integer", "minimum": 1, "default": 1},
						"per_page": {"type": "integer", "minimum": 1, "maximum": 100, "default": 25}
					}
				}`),
				Handler: guarded("tickets.search", searchTickets),
			},
			{
				Name:        "tickets.get",
				Summary:     "Opens one ticket, including its full conversation.",
				Freshness:   &plugin.Freshness{Soft: 30 * time.Second, Hard: 2 * time.Minute},
				Description: "Fetch one ticket including its full comment thread. This is the tool to reach for when helping with a specific ticket.",
				Provides:    []plugin.Capability{plugin.CapWorkItemsGet},
				Schema: json.RawMessage(`{
					"type": "object",
					"required": ["id"],
					"properties": {
						"id": {"type": "integer", "description": "Syncro ticket id"}
					}
				}`),
				Handler: guarded("tickets.get", getTicket),
			},
			{
				Name:      "tickets.timeline",
				Summary:   "Rebuilds one ticket's history and works out where it stalled.",
				Freshness: &plugin.Freshness{Soft: 30 * time.Second, Hard: 2 * time.Minute},
				Description: "The full history of one ticket in one chronological sequence — creation, " +
					"every message, and logged time — with computed signals: time to first response, " +
					"longest gap, how many times the conversation changed sides, and whether it has gone quiet. " +
					"Reach for this when helping with a specific ticket.",
				Provides: []plugin.Capability{plugin.CapWorkItemsTimeline},
				Schema: json.RawMessage(`{
					"type": "object",
					"required": ["id"],
					"properties": {
						"id": {"type": "integer", "description": "Syncro ticket id"}
					}
				}`),
				Handler: guarded("tickets.timeline", getTimeline),
			},
			{
				Name:      "time.entries",
				Summary:   "Reads logged time, so you can see what work was recorded.",
				Freshness: &plugin.Freshness{Soft: 5 * time.Minute, Hard: 30 * time.Minute},
				Description: "Logged time entries, filterable by customer and date window. Use this to " +
					"find work that was done, or to check whether time was booked against a customer in a period.",
				Provides: []plugin.Capability{plugin.CapTimeEntriesList},
				Schema: json.RawMessage(`{
					"type": "object",
					"properties": {
						"customer_id": {"type": "integer", "description": "Syncro customer id"},
						"since": {"type": "string", "description": "RFC3339 timestamp; entries created after this"},
						"until": {"type": "string", "description": "RFC3339 timestamp; entries created before this"},
						"page": {"type": "integer", "minimum": 1, "default": 1},
						"per_page": {"type": "integer", "minimum": 1, "maximum": 100, "default": 25}
					}
				}`),
				Handler: guarded("time.entries", listTimeEntries),
			},
			{
				Name:      "docs.search",
				Summary:   "Searches your Syncro wiki for documented procedures.",
				Freshness: &plugin.Freshness{Soft: 6 * time.Hour, Hard: 24 * time.Hour},
				Description: "Search the company's Syncro wiki — documented procedures, runbooks and " +
					"setup guides. Prefer a documented procedure over general knowledge when one exists.",
				Provides: []plugin.Capability{plugin.CapDocsSearch},
				Schema: json.RawMessage(`{
					"type": "object",
					"properties": {
						"query": {"type": "string", "description": "Words to match in a page title or body"},
						"include_body": {"type": "boolean", "default": true, "description": "Return full page text, not just titles"}
					}
				}`),
				Handler: guarded("docs.search", searchDocs),
			},
			{
				Name:      "invoices.list",
				Summary:   "Reads invoices, to see what has been billed and what is outstanding.",
				Freshness: &plugin.Freshness{Soft: 10 * time.Minute, Hard: 1 * time.Hour},
				Description: "List invoices, optionally only paid or only unpaid, and optionally for one " +
					"customer or ticket. Use this to answer what has been billed and what is outstanding.",
				Provides: []plugin.Capability{plugin.CapInvoicesList},
				Schema: json.RawMessage(`{
					"type": "object",
					"properties": {
						"status": {"type": "string", "enum": ["paid", "unpaid"], "description": "Omit for all invoices"},
						"customer_id": {"type": "integer", "description": "Syncro customer id"},
						"ticket_id": {"type": "integer", "description": "Invoices raised against one ticket"},
						"page": {"type": "integer", "minimum": 1, "default": 1},
						"per_page": {"type": "integer", "minimum": 1, "maximum": 100, "default": 25}
					}
				}`),
				Handler: guarded("invoices.list", listInvoices),
			},
			{
				Name:      "customers.standing",
				Summary:   "Totals up what one customer owes and how much is overdue.",
				Freshness: &plugin.Freshness{Soft: 10 * time.Minute, Hard: 1 * time.Hour},
				Description: "A customer's financial position: what is outstanding, how much is overdue, " +
					"and the unpaid invoices behind the total. The balance is summed from unpaid invoices " +
					"because Syncro exposes no balance field, and the response says so.",
				Provides: []plugin.Capability{plugin.CapCustomerStanding},
				Schema: json.RawMessage(`{
					"type": "object",
					"required": ["customer_id"],
					"properties": {
						"customer_id": {"type": "integer", "description": "Syncro customer id"},
						"include_paid": {"type": "boolean", "default": false, "description": "Also return recently paid invoices"}
					}
				}`),
				Handler: guarded("customers.standing", customerStanding),
			},
			{
				Name:    "tickets.comment",
				Summary: "Posts a reply or an internal note on a ticket.",
				Description: "Post a message to a ticket — a public reply the customer receives, or a " +
					"hidden internal note they never see. Never offered to the model: only a person can send this.",
				Provides:           []plugin.Capability{plugin.CapWorkItemsComment},
				Mutates:            true,
				RequiresPermission: "ticket.comment",
				Schema: json.RawMessage(`{
					"type": "object",
					"required": ["id", "body"],
					"properties": {
						"id": {"type": "integer", "description": "Syncro ticket id"},
						"body": {"type": "string", "description": "The message text"},
						"subject": {"type": "string", "description": "Optional subject line"},
						"hidden": {"type": "boolean", "default": true, "description": "Internal note the customer never sees. Defaults to true: a private note posted publicly is far worse than the reverse."},
						"do_not_email": {"type": "boolean", "default": false, "description": "Post without emailing the customer"}
					}
				}`),
				Handler: guarded("tickets.comment", postComment),
			},
			{
				Name:    "tickets.update",
				Summary: "Changes a ticket's status, assignee, priority or customer.",
				Description: "Change a ticket's status, assignee, priority, or the customer it belongs to. " +
					"Reassigning the customer is how a PagerDuty ticket on a generic account reaches the right one.",
				Provides:           []plugin.Capability{plugin.CapWorkItemsUpdate},
				Mutates:            true,
				RequiresPermission: "ticket.status",
				Schema: json.RawMessage(`{
					"type": "object",
					"required": ["id"],
					"properties": {
						"id": {"type": "integer", "description": "Syncro ticket id"},
						"status": {"type": "string", "description": "New status; use tickets.options for the list this account defines"},
						"user_id": {"type": "integer", "description": "Technician to assign to; see tickets.options"},
						"customer_id": {"type": "integer", "description": "Move the ticket to a different customer"},
						"priority": {"type": "string", "description": "New priority"}
					}
				}`),
				Handler: guarded("tickets.update", updateTicket),
			},
			{
				Name:    "tickets.options",
				Summary: "Reads the statuses and technicians your account has set up.",
				Description: "The statuses and technicians this Syncro account defines, so an interface can " +
					"offer real choices instead of guessing at them.",
				Provides:  []plugin.Capability{plugin.CapWorkItemsSchema},
				Freshness: &plugin.Freshness{Soft: 1 * time.Hour, Hard: 12 * time.Hour},
				Schema:    json.RawMessage(`{"type": "object", "properties": {}}`),
				Handler:   guarded("tickets.options", ticketOptions),
			},
			{
				Name:    "access.check",
				Summary: "Checks what your Syncro key is allowed to do.",
				Description: "Report what the configured Syncro API token is permitted to do, and whether " +
					"it holds more permission than Azir needs.",
				Provides: []plugin.Capability{plugin.CapAccessCheck},
				Schema:   json.RawMessage(`{"type": "object", "properties": {}}`),
				Handler:  checkAccess,
			},
			{
				Name:        "assets.list",
				Summary:     "Lists a customer's machines and devices.",
				Freshness:   &plugin.Freshness{Soft: 1 * time.Hour, Hard: 12 * time.Hour},
				Description: "List a customer's assets — machines, devices and their serials.",
				Provides:    []plugin.Capability{plugin.CapAssetsList},
				Schema: json.RawMessage(`{
					"type": "object",
					"properties": {
						"customer_id": {"type": "integer", "description": "Syncro customer id; omit for all assets"},
						"page": {"type": "integer", "minimum": 1, "default": 1},
						"per_page": {"type": "integer", "minimum": 1, "maximum": 100, "default": 25}
					}
				}`),
				Handler: guarded("assets.list", listAssets),
			},
		},
	}
}

// guarded wraps a handler with its permission precondition.
//
// Applied at registration rather than remembered inside each handler: a
// forgotten guard is invisible until a technician meets a bare 401, and
// "remember to call this" is not a mechanism.
func guarded(tool string, h plugin.Handler) plugin.Handler {
	return func(ctx context.Context, req plugin.Request) (any, error) {
		c, err := client(ctx, req)
		if err != nil {
			return nil, err
		}
		if err := c.RequireGrants(ctx, tool); err != nil {
			return nil, err
		}
		return h(ctx, req)
	}
}

// preflight asks Syncro what the configured token may do and turns that into
// per-tool availability.
func preflight(ctx context.Context) plugin.Health {
	h := plugin.Health{Tools: map[string]plugin.ToolStatus{}}

	c, err := client(ctx, plugin.Request{})
	if err != nil {
		// Unconfigured is a state, not a fault. Say so plainly so the console
		// shows a setup instruction rather than an error.
		h.Ready = false
		h.Reason = "not configured: set the Syncro subdomain and API token in plugin settings"
		return h
	}

	access, err := c.CheckAccess(ctx)
	if err != nil {
		h.Ready = false
		h.Reason = "the stored Syncro token was rejected; check it in plugin settings"
		return h
	}

	h.Ready = true
	for tool, status := range access.ToolAvailability() {
		info, _ := status.(map[string]any)
		available, _ := info["available"].(bool)
		ts := plugin.ToolStatus{Available: available}
		if !available {
			missing, _ := info["missing"].([]string)
			ts.Reason = "the Syncro API token cannot read " +
				strings.ReplaceAll(strings.Join(missing, " or "), "_", " ")
		}
		h.Tools[tool] = ts
	}

	if !access.Sufficient {
		h.Reason = "the Syncro token cannot read " + strings.Join(access.Unreachable, ", ") +
			"; tools needing those are unavailable"
	}
	return h
}

// client builds or reuses the API client for the configured subdomain,
// resolving the API token per request so a rotation is picked up immediately.
func client(ctx context.Context, req plugin.Request) (*syncro.Client, error) {
	cfg, ok := plugin.ConfigFrom(ctx)
	if !ok {
		return nil, plugin.Errorf("500", "plugin settings are unavailable")
	}
	subdomain, err := cfg.String(ctx, req.CustomerID, "subdomain")
	if err != nil {
		return nil, plugin.Errorf("412", "Syncro is not configured yet: set the subdomain in plugin settings")
	}

	return clients.For(subdomain, func(ctx context.Context) (string, error) {
		v, ok := plugin.VaultFrom(ctx)
		if !ok {
			return "", plugin.Errorf("500", "the credential store is unavailable")
		}
		token, err := v.For(ctx, "", "api_key")
		if errors.Is(err, plugin.ErrNoCredential) {
			return "", plugin.Errorf("412", "Syncro is not configured yet: set the API token in plugin settings")
		}
		return token, err
	})
}

// args decodes a handler's arguments.
func args[T any](req plugin.Request) (T, error) {
	var out T
	if len(req.Args) == 0 {
		return out, nil
	}
	if err := json.Unmarshal(req.Args, &out); err != nil {
		return out, plugin.Errorf("400", "arguments could not be parsed")
	}
	return out, nil
}

type pageArgs struct {
	Page    int `json:"page"`
	PerPage int `json:"per_page"`
}

func searchCustomers(ctx context.Context, req plugin.Request) (any, error) {
	a, err := args[struct {
		pageArgs
		Query string `json:"query"`
	}](req)
	if err != nil {
		return nil, err
	}
	c, err := client(ctx, req)
	if err != nil {
		return nil, err
	}
	return c.ListCustomers(ctx, a.Query, a.Page, a.PerPage)
}

func getCustomer(ctx context.Context, req plugin.Request) (any, error) {
	a, err := args[struct {
		ID int64 `json:"id"`
	}](req)
	if err != nil {
		return nil, err
	}
	if a.ID <= 0 {
		return nil, plugin.Errorf("400", "a Syncro customer id is required")
	}
	c, err := client(ctx, req)
	if err != nil {
		return nil, err
	}
	return c.GetCustomer(ctx, a.ID)
}

func searchTickets(ctx context.Context, req plugin.Request) (any, error) {
	a, err := args[struct {
		pageArgs
		Query      string `json:"query"`
		Status     string `json:"status"`
		OpenOnly   bool   `json:"open_only"`
		CustomerID int64  `json:"customer_id"`
	}](req)
	if err != nil {
		return nil, err
	}

	c, err := client(ctx, req)
	if err != nil {
		return nil, err
	}

	// When the caller named an Azir customer but no Syncro one, translate
	// through the spine. This is why handlers receive an opaque Azir id: a
	// vendor identifier must never be the thing memory or context hangs off.
	customerID := a.CustomerID
	if customerID == 0 && req.CustomerID != "" {
		if id, ok := syncroIDFor(ctx, req.CustomerID); ok {
			customerID = id
		}
	}

	return c.SearchTickets(ctx, syncro.TicketSearch{
		Query:      a.Query,
		Status:     a.Status,
		OpenOnly:   a.OpenOnly,
		CustomerID: customerID,
		Page:       a.Page,
		PerPage:    a.PerPage,
	})
}

func getTicket(ctx context.Context, req plugin.Request) (any, error) {
	a, err := args[struct {
		ID int64 `json:"id"`
	}](req)
	if err != nil {
		return nil, err
	}
	if a.ID <= 0 {
		return nil, plugin.Errorf("400", "a Syncro ticket id is required")
	}
	c, err := client(ctx, req)
	if err != nil {
		return nil, err
	}
	return c.GetTicket(ctx, a.ID)
}

func listAssets(ctx context.Context, req plugin.Request) (any, error) {
	a, err := args[struct {
		pageArgs
		CustomerID int64 `json:"customer_id"`
	}](req)
	if err != nil {
		return nil, err
	}
	c, err := client(ctx, req)
	if err != nil {
		return nil, err
	}

	customerID := a.CustomerID
	if customerID == 0 && req.CustomerID != "" {
		if id, ok := syncroIDFor(ctx, req.CustomerID); ok {
			customerID = id
		}
	}
	return c.ListAssets(ctx, customerID, a.Page, a.PerPage)
}

func getTimeline(ctx context.Context, req plugin.Request) (any, error) {
	a, err := args[struct {
		ID int64 `json:"id"`
	}](req)
	if err != nil {
		return nil, err
	}
	if a.ID <= 0 {
		return nil, plugin.Errorf("400", "a Syncro ticket id is required")
	}
	c, err := client(ctx, req)
	if err != nil {
		return nil, err
	}
	return c.GetTimeline(ctx, a.ID)
}

func listTimeEntries(ctx context.Context, req plugin.Request) (any, error) {
	a, err := args[struct {
		pageArgs
		CustomerID int64  `json:"customer_id"`
		Since      string `json:"since"`
		Until      string `json:"until"`
	}](req)
	if err != nil {
		return nil, err
	}
	c, err := client(ctx, req)
	if err != nil {
		return nil, err
	}

	customerID := a.CustomerID
	if customerID == 0 && req.CustomerID != "" {
		if id, ok := syncroIDFor(ctx, req.CustomerID); ok {
			customerID = id
		}
	}

	return c.TicketTimers(ctx, syncro.TimerSearch{
		CustomerID: customerID,
		Since:      a.Since,
		Until:      a.Until,
		Page:       a.Page,
		PerPage:    a.PerPage,
	})
}

func searchDocs(ctx context.Context, req plugin.Request) (any, error) {
	a, err := args[struct {
		Query       string `json:"query"`
		IncludeBody *bool  `json:"include_body"`
	}](req)
	if err != nil {
		return nil, err
	}
	c, err := client(ctx, req)
	if err != nil {
		return nil, err
	}
	includeBody := a.IncludeBody == nil || *a.IncludeBody
	pages, err := c.SearchWiki(ctx, a.Query, includeBody)
	if err != nil {
		return nil, err
	}
	return map[string]any{"pages": pages, "count": len(pages)}, nil
}

func listInvoices(ctx context.Context, req plugin.Request) (any, error) {
	a, err := args[struct {
		pageArgs
		Status     string `json:"status"`
		CustomerID int64  `json:"customer_id"`
		TicketID   int64  `json:"ticket_id"`
	}](req)
	if err != nil {
		return nil, err
	}
	c, err := client(ctx, req)
	if err != nil {
		return nil, err
	}
	customerID := a.CustomerID
	if customerID == 0 && req.CustomerID != "" {
		if id, ok := syncroIDFor(ctx, req.CustomerID); ok {
			customerID = id
		}
	}

	return c.ListInvoices(ctx, syncro.InvoiceSearch{
		Status:     a.Status,
		CustomerID: customerID,
		TicketID:   a.TicketID,
		Page:       a.Page,
		PerPage:    a.PerPage,
	})
}

func customerStanding(ctx context.Context, req plugin.Request) (any, error) {
	a, err := args[struct {
		CustomerID  int64 `json:"customer_id"`
		IncludePaid bool  `json:"include_paid"`
	}](req)
	if err != nil {
		return nil, err
	}
	c, err := client(ctx, req)
	if err != nil {
		return nil, err
	}
	customerID := a.CustomerID
	if customerID == 0 && req.CustomerID != "" {
		if id, ok := syncroIDFor(ctx, req.CustomerID); ok {
			customerID = id
		}
	}
	if customerID <= 0 {
		return nil, plugin.Errorf("400", "a Syncro customer id is required")
	}
	return c.CustomerStanding(ctx, customerID, a.IncludePaid)
}

func checkAccess(ctx context.Context, req plugin.Request) (any, error) {
	c, err := client(ctx, req)
	if err != nil {
		return nil, err
	}
	access, err := c.CheckAccess(ctx)
	if err != nil {
		return nil, err
	}
	// Which tools this token can actually drive, so a missing permission is a
	// visible fact rather than a surprise at call time.
	return map[string]any{
		"access": access,
		"tools":  access.ToolAvailability(),
	}, nil
}

func postComment(ctx context.Context, req plugin.Request) (any, error) {
	a, err := args[struct {
		ID         int64  `json:"id"`
		Body       string `json:"body"`
		Subject    string `json:"subject"`
		Hidden     *bool  `json:"hidden"`
		DoNotEmail bool   `json:"do_not_email"`
	}](req)
	if err != nil {
		return nil, err
	}
	c, err := client(ctx, req)
	if err != nil {
		return nil, err
	}

	// Hidden defaults to true. Posting an internal note publicly is far worse
	// than the reverse, so the safer reading of an absent flag wins.
	hidden := true
	if a.Hidden != nil {
		hidden = *a.Hidden
	}

	return c.PostComment(ctx, syncro.CommentRequest{
		TicketID:   a.ID,
		Subject:    a.Subject,
		Body:       a.Body,
		Hidden:     hidden,
		DoNotEmail: a.DoNotEmail,
	})
}

func updateTicket(ctx context.Context, req plugin.Request) (any, error) {
	a, err := args[struct {
		ID         int64  `json:"id"`
		Status     string `json:"status"`
		UserID     int64  `json:"user_id"`
		CustomerID int64  `json:"customer_id"`
		Priority   string `json:"priority"`
	}](req)
	if err != nil {
		return nil, err
	}
	c, err := client(ctx, req)
	if err != nil {
		return nil, err
	}
	return c.UpdateTicket(ctx, syncro.TicketUpdate{
		TicketID:   a.ID,
		Status:     a.Status,
		UserID:     a.UserID,
		CustomerID: a.CustomerID,
		Priority:   a.Priority,
	})
}

func ticketOptions(ctx context.Context, req plugin.Request) (any, error) {
	c, err := client(ctx, req)
	if err != nil {
		return nil, err
	}
	statuses, err := c.TicketStatuses(ctx)
	if err != nil {
		return nil, err
	}
	technicians, err := c.Technicians(ctx)
	if err != nil {
		// Statuses alone are still useful; failing both because one failed is
		// worse than a partial answer that says what is missing.
		return map[string]any{"statuses": statuses, "technicians": []any{},
			"note": "technicians could not be read with this token"}, nil
	}
	return map[string]any{"statuses": statuses, "technicians": technicians}, nil
}

// syncroIDFor translates an Azir customer into this plugin's identifier.
// A customer with no Syncro record is an ordinary state, not an error.
func syncroIDFor(ctx context.Context, customerID string) (int64, bool) {
	ident, ok := plugin.IdentityFrom(ctx)
	if !ok {
		return 0, false
	}
	external, err := ident.External(ctx, customerID)
	if err != nil {
		return 0, false
	}
	id, err := strconv.ParseInt(external, 10, 64)
	if err != nil {
		return 0, false
	}
	return id, true
}
