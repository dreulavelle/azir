// Command plugin-searxng gives the assistant the public internet, as search
// results and nothing else.
//
// Named for the service it connects to rather than for what it provides, which
// is the convention every other plugin follows: the capability it supplies is
// web.search, and a deployment that would rather use a hosted search API can
// add a plugin for that one and satisfy the same capability without anything
// upstream noticing.
//
// A technician working a hard ticket reaches for a vendor's KB article, an
// error code, a firmware advisory. Without this the assistant answers those
// from whatever it happened to memorise, which for a four-month-old firmware
// bug is worse than saying nothing.
//
// # What this deliberately cannot do
//
// It takes a query and returns results. It does not fetch a URL the caller
// names, and there is no tool here that will.
//
// That restriction is the whole security design, not a limitation to be lifted
// later. Ticket text is written by customers, and the assistant reads it. If
// the assistant could fetch a URL somebody put in a ticket, then a ticket
// saying "for context see https://evil.example/?notes=" would turn the
// assistant into a way of posting a customer's data to a stranger — in a
// product whose entire claim is that customer data does not leave. Containment
// cannot rest on the model declining, because a model that can be asked can
// eventually be persuaded. It rests on the destination never being the
// caller's to choose: every request goes to the one endpoint an administrator
// configured, and the caller only ever supplies words.
//
// The recommended endpoint is a self-hosted SearXNG, in which case the queries
// do not leave the building either.
package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/dreulavelle/azir/pkg/plugin"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	p := plugin.Plugin{
		Name:        "searxng",
		Version:     "0.1.0",
		Description: "Searches the public internet through your own SearXNG, so queries never leave your network",
		Category:    plugin.CategoryAI,
		ConfigSchema: json.RawMessage(`{
			"type": "object",
			"required": ["endpoint"],
			"properties": {
				"endpoint": {
					"type": "string",
					"title": "Search service address",
					"description": "Your SearXNG instance. Azir ships one — leave this as it is unless you run your own elsewhere. Self-hosting means the queries never leave your network at all, which is the only arrangement where searching about a customer's problem is not also telling somebody about it.",
					"default": "http://searxng:8080"
				},
				"api_key": {
					"type": "string",
					"title": "API key",
					"description": "Only if your search service needs one. SearXNG usually does not.",
					"x-azir-secret": true
				},
				"only_domains": {
					"type": "string",
					"title": "Restrict to these sites",
					"description": "Optional. A comma-separated list, such as: learn.microsoft.com, support.apc.com. Results from anywhere else are discarded. Narrower is better for a helpdesk — the vendor's own documentation is what you wanted anyway."
				},
				"results": {
					"type": "integer",
					"title": "Results per search",
					"description": "How many results to return. More costs the assistant reading time and rarely helps.",
					"default": 6,
					"minimum": 1,
					"maximum": 15
				}
			}
		}`),
		Tools: []plugin.Tool{
			{
				Name: "search",
				// Written for the model. It has to know both what this is for
				// and what not to put in it, because the query is the one
				// thing here that leaves the network.
				Description: "Searches the public internet and returns result titles, snippets and links. " +
					"Use it for vendor documentation, error codes, firmware advisories and known issues — " +
					"anything general that is not in this company's own systems. " +
					"Never put a customer's name, a person's name, an address, a serial number, a licence key " +
					"or any other identifying detail into the query: search for the product and the symptom, not for the customer. " +
					"You cannot open a link from a ticket; only these results.",
				Summary:  "Looks up vendor documentation and error codes on the public internet.",
				Provides: []plugin.Capability{plugin.CapWebSearch},
				Freshness: &plugin.Freshness{
					Soft: 30 * time.Minute,
					Hard: 24 * time.Hour,
				},
				Schema: json.RawMessage(`{
					"type": "object",
					"required": ["query"],
					"properties": {
						"query": {
							"type": "string",
							"description": "What to search for. Product names, error codes and symptoms — never customer or personal details."
						}
					}
				}`),
				Handler: search,
			},
		},
	}

	if err := plugin.Serve(ctx, p, plugin.WithLogger(log)); err != nil {
		log.Error("plugin failed to start", "error", err)
		os.Exit(1)
	}
}

// result is one hit, reduced to what is worth a model's attention.
type result struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Snippet string `json:"snippet,omitempty"`
	Site    string `json:"site,omitempty"`
}

type searchArgs struct {
	Query string `json:"query"`
}

func search(ctx context.Context, req plugin.Request) (any, error) {
	var args searchArgs
	if err := json.Unmarshal(req.Args, &args); err != nil {
		return nil, plugin.Errorf("400", "arguments could not be parsed")
	}
	query := strings.TrimSpace(args.Query)
	if query == "" {
		return nil, plugin.Errorf("400", "a search needs something to search for")
	}

	cfg, ok := plugin.ConfigFrom(ctx)
	if !ok {
		return nil, plugin.Errorf("500", "settings unavailable")
	}
	values, err := cfg.All(ctx, req.CustomerID)
	if err != nil {
		return nil, plugin.Errorf("500", "settings could not be read")
	}

	endpoint, _ := values["endpoint"].(string)
	endpoint = strings.TrimSpace(strings.TrimSuffix(endpoint, "/"))
	if endpoint == "" {
		return nil, plugin.Errorf("400", "no search service is configured")
	}

	want := 6
	if n, ok := values["results"].(float64); ok && n >= 1 && n <= 15 {
		want = int(n)
	}

	// The destination is built here from configuration, never from anything the
	// caller supplied. The caller's words go in a query parameter and nowhere
	// else, so no input can redirect the request at another host.
	ask, err := url.Parse(endpoint + "/search")
	if err != nil {
		return nil, plugin.Errorf("400", "the search service address is not a valid URL")
	}
	q := url.Values{}
	q.Set("q", query)
	q.Set("format", "json")
	q.Set("safesearch", "1")
	ask.RawQuery = q.Encode()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, ask.String(), nil)
	if err != nil {
		return nil, plugin.Errorf("500", "the search could not be prepared")
	}
	httpReq.Header.Set("accept", "application/json")
	if v, ok := plugin.VaultFrom(ctx); ok {
		if key, err := v.For(ctx, req.CustomerID, "api_key"); err == nil && len(key) > 0 {
			httpReq.Header.Set("authorization", "Bearer "+string(key))
		}
	}

	res, err := (&http.Client{Timeout: 20 * time.Second}).Do(httpReq)
	if err != nil {
		return nil, plugin.Errorf("502", "the search service could not be reached")
	}
	defer res.Body.Close() //nolint:errcheck // best effort
	if res.StatusCode != http.StatusOK {
		return nil, plugin.Errorf("502", "the search service answered %d", res.StatusCode)
	}

	var doc struct {
		Results []struct {
			Title   string `json:"title"`
			URL     string `json:"url"`
			Content string `json:"content"`
		} `json:"results"`
	}
	if err := json.NewDecoder(res.Body).Decode(&doc); err != nil {
		return nil, plugin.Errorf("502", "the search service returned something unreadable")
	}

	allow := domainList(values["only_domains"])
	out := make([]result, 0, want)
	var skipped int
	for _, r := range doc.Results {
		host := hostOf(r.URL)
		if len(allow) > 0 && !permitted(host, allow) {
			skipped++
			continue
		}
		out = append(out, result{
			Title:   strings.TrimSpace(r.Title),
			URL:     r.URL,
			Snippet: truncate(strings.TrimSpace(r.Content), 320),
			Site:    host,
		})
		if len(out) >= want {
			break
		}
	}

	answer := map[string]any{
		"query":   query,
		"results": out,
		// Said plainly rather than left for the model to infer, because an
		// answer built on nothing should read as one.
		"note": "Public internet results. Nothing here has been verified against this customer's systems.",
	}
	if len(allow) > 0 {
		answer["restricted_to"] = allow
		if skipped > 0 {
			answer["discarded"] = skipped
		}
	}
	return answer, nil
}

func domainList(raw any) []string {
	s, _ := raw.(string)
	out := []string{}
	for _, part := range strings.Split(s, ",") {
		if d := strings.ToLower(strings.TrimSpace(part)); d != "" {
			out = append(out, d)
		}
	}
	return out
}

func hostOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Hostname())
}

// permitted matches a host against the allowed list, including subdomains, so
// "microsoft.com" covers "learn.microsoft.com" without also covering
// "microsoft.com.evil.example".
func permitted(host string, allow []string) bool {
	for _, d := range allow {
		if host == d || strings.HasSuffix(host, "."+d) {
			return true
		}
	}
	return false
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return strings.TrimSpace(s[:max]) + "…"
}
