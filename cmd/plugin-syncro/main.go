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
	"syscall"

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

	p := plugin.Plugin{
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
		Tools: []plugin.Tool{
			{
				Name:        "customers.search",
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
				Handler: searchCustomers,
			},
			{
				Name:        "customers.get",
				Description: "Fetch one customer by their Syncro id, including contact details and notes.",
				Provides:    []plugin.Capability{plugin.CapCustomersGet},
				Schema: json.RawMessage(`{
					"type": "object",
					"required": ["id"],
					"properties": {
						"id": {"type": "integer", "description": "Syncro customer id"}
					}
				}`),
				Handler: getCustomer,
			},
			{
				Name: "tickets.search",
				Description: "Search tickets by free text, status, or customer. Returns summaries without " +
					"comment threads; use tickets.get for the full conversation on one ticket.",
				Provides: []plugin.Capability{plugin.CapWorkItemsSearch},
				Schema: json.RawMessage(`{
					"type": "object",
					"properties": {
						"query": {"type": "string", "description": "Free-text search across subject and body"},
						"status": {"type": "string", "description": "Filter by ticket status, e.g. New, In Progress, Resolved"},
						"customer_id": {"type": "integer", "description": "Restrict to one Syncro customer"},
						"page": {"type": "integer", "minimum": 1, "default": 1},
						"per_page": {"type": "integer", "minimum": 1, "maximum": 100, "default": 25}
					}
				}`),
				Handler: searchTickets,
			},
			{
				Name:        "tickets.get",
				Description: "Fetch one ticket including its full comment thread. This is the tool to reach for when helping with a specific ticket.",
				Provides:    []plugin.Capability{plugin.CapWorkItemsGet},
				Schema: json.RawMessage(`{
					"type": "object",
					"required": ["id"],
					"properties": {
						"id": {"type": "integer", "description": "Syncro ticket id"}
					}
				}`),
				Handler: getTicket,
			},
			{
				Name: "tickets.timeline",
				Description: "The full history of one ticket in one chronological sequence — creation, " +
					"every message, and logged time — with computed signals: time to first response, " +
					"longest gap, how many times the conversation changed sides, and whether it has gone quiet. " +
					"Reach for this when helping with a specific ticket.",
				Provides: []plugin.Capability{plugin.CapWorkItemsGet},
				Schema: json.RawMessage(`{
					"type": "object",
					"required": ["id"],
					"properties": {
						"id": {"type": "integer", "description": "Syncro ticket id"}
					}
				}`),
				Handler: getTimeline,
			},
			{
				Name: "time.entries",
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
				Handler: listTimeEntries,
			},
			{
				Name: "docs.search",
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
				Handler: searchDocs,
			},
			{
				Name: "access.check",
				Description: "Report what the configured Syncro API token is permitted to do, and whether " +
					"it holds more permission than Azir needs.",
				Provides: []plugin.Capability{plugin.CapAccessCheck},
				Schema:   json.RawMessage(`{"type": "object", "properties": {}}`),
				Handler:  checkAccess,
			},
			{
				Name:        "assets.list",
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
				Handler: listAssets,
			},
		},
	}

	if err := plugin.Serve(ctx, p, plugin.WithLogger(log)); err != nil {
		log.Error("plugin failed to start", "error", err)
		os.Exit(1)
	}
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

func checkAccess(ctx context.Context, req plugin.Request) (any, error) {
	c, err := client(ctx, req)
	if err != nil {
		return nil, err
	}
	return c.CheckAccess(ctx)
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
