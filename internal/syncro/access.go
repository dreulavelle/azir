package syncro

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/dreulavelle/azir/pkg/plugin"
)

// Access is what an API token can actually do, determined by trying.
//
// Syncro's /me endpoint reports the *user's* permissions, not the *token's*:
// an API token restricted to a handful of scopes still reports the full admin
// matrix. Verified against two tokens on one account — identical /me output,
// materially different behaviour. Asking is therefore worthless and only
// probing is honest.
//
// The cost is a handful of cheap requests, cached for minutes. That is a small
// price for not telling an administrator their token can do something it
// cannot.
type Access struct {
	User      string `json:"user"`
	Email     string `json:"email"`
	Subdomain string `json:"subdomain"`

	// Reachable maps a resource to whether this token can read it.
	Reachable map[string]bool `json:"reachable"`

	// Required lists what Azir's core function needs and whether it is present.
	Required map[string]bool `json:"required"`
	// Sufficient is true when every required resource is reachable.
	Sufficient bool `json:"sufficient"`
	// Unreachable names what this token cannot read, so an administrator can
	// see the gap without reading a table of booleans.
	Unreachable []string `json:"unreachable"`

	// Method records how this was determined, because a reader should never
	// have to guess whether a capability report was measured or claimed.
	Method string `json:"method"`
}

// probe is a cheap request that proves whether a resource is readable.
type probe struct {
	resource string
	path     string
}

// probes are the resources Azir's tools depend on. Each is a single-item read,
// which is the cheapest question that still gets a truthful answer.
var probes = []probe{
	{"tickets", "/tickets"},
	{"customers", "/customers"},
	{"assets", "/customer_assets"},
	{"invoices", "/invoices"},
	{"documentation", "/wiki_pages"},
	{"time_entries", "/ticket_timers"},
}

// requiredResources is what Azir needs to be useful at all. Everything else
// degrades a feature rather than the product.
var requiredResources = []string{"tickets", "customers"}

// toolResources maps each tool to the resource it reads. A tool absent here is
// assumed always available.
var toolResources = map[string]string{
	"customers.search":   "customers",
	"customers.get":      "customers",
	"tickets.search":     "tickets",
	"tickets.get":        "tickets",
	"tickets.timeline":   "tickets",
	"assets.list":        "assets",
	"invoices.list":      "invoices",
	"customers.standing": "invoices",
	"docs.search":        "documentation",
	"time.entries":       "time_entries",
	"tickets.comment":    "tickets",
	"tickets.update":     "tickets",
	"tickets.options":    "tickets",
	"access.check":       "",
}

// KnownTools returns every tool the resource map accounts for, so a test can
// assert the two never drift apart.
func KnownTools() []string {
	out := make([]string, 0, len(toolResources))
	for tool := range toolResources {
		out = append(out, tool)
	}
	sort.Strings(out)
	return out
}

// CheckAccess determines what this token can read by probing each resource.
func (c *Client) CheckAccess(ctx context.Context) (Access, error) {
	a := Access{
		Reachable: map[string]bool{},
		Required:  map[string]bool{},
		Method:    "probed: each resource was read, because Syncro's /me reports the user's permissions rather than the token's",
	}

	// Identity is still worth reporting even though its permissions are not.
	var who struct {
		UserName  string `json:"user_name"`
		UserEmail string `json:"user_email"`
		Subdomain string `json:"subdomain"`
	}
	if err := c.get(ctx, "/me", nil, &who); err == nil {
		a.User, a.Email, a.Subdomain = who.UserName, who.UserEmail, who.Subdomain
	}

	// Probes run concurrently: the rate limiter still paces them, and doing
	// them in series would make preflight noticeably slow.
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, p := range probes {
		wg.Add(1)
		go func(p probe) {
			defer wg.Done()
			ok := c.canRead(ctx, p.path)
			mu.Lock()
			a.Reachable[p.resource] = ok
			mu.Unlock()
		}(p)
	}
	wg.Wait()

	a.Sufficient = true
	for _, need := range requiredResources {
		a.Required[need] = a.Reachable[need]
		if !a.Reachable[need] {
			a.Sufficient = false
		}
	}
	for resource, ok := range a.Reachable {
		if !ok {
			a.Unreachable = append(a.Unreachable, resource)
		}
	}
	sort.Strings(a.Unreachable)

	return a, nil
}

// canRead reports whether one resource answers.
//
// Syncro returns 401 for a permission denial, not 403, so a refusal is
// indistinguishable from a bad credential by status alone. Since the token has
// already proven itself on other endpoints by this point, a 401 here means
// "not permitted" rather than "not authenticated".
func (c *Client) canRead(ctx context.Context, path string) bool {
	q := url.Values{}
	q.Set("per_page", "1")

	_, err := c.http.Do(ctx, http.MethodGet, path, q, nil)
	if err == nil {
		return true
	}
	var perr *plugin.Error
	if errors.As(err, &perr) {
		// 401 and 403 both mean this token may not read here. Anything else —
		// a timeout, a 500 — is a fault rather than a permission boundary, and
		// disabling a tool over a transient failure would be worse than
		// letting it try.
		return perr.Code != "401" && perr.Code != "403"
	}
	return true
}

// ToolAvailability reports which tools this token can drive.
func (a Access) ToolAvailability() map[string]any {
	out := map[string]any{}
	for tool, resource := range toolResources {
		if resource == "" {
			out[tool] = map[string]any{"available": true}
			continue
		}
		if a.Reachable[resource] {
			out[tool] = map[string]any{"available": true}
			continue
		}
		out[tool] = map[string]any{"available": false, "missing": []string{resource}}
	}
	return out
}

// cachedAccess probes at most once every few minutes.
//
// Longer than a typical cache because the answer costs several vendor requests
// and changes only when an administrator edits a token — which is rare, and
// never urgent enough to justify spending the rate limit this protects.
func (c *Client) cachedAccess(ctx context.Context) (Access, error) {
	c.accessMu.Lock()
	defer c.accessMu.Unlock()

	if c.accessOK && time.Since(c.accessAt) < 5*time.Minute {
		return c.access, nil
	}
	access, err := c.CheckAccess(ctx)
	if err != nil {
		return Access{}, err
	}
	c.access, c.accessAt, c.accessOK = access, time.Now(), true
	return access, nil
}

// RequireGrants returns an actionable error when the token cannot support a
// tool, naming the resource instead of surfacing a bare 401.
func (c *Client) RequireGrants(ctx context.Context, tool string) error {
	resource := toolResources[tool]
	if resource == "" {
		return nil
	}
	access, err := c.cachedAccess(ctx)
	if err != nil {
		// If access cannot be determined, let the call proceed: a working tool
		// blocked by a failed precondition is worse than a clear vendor error.
		return nil
	}
	if !access.Reachable[resource] {
		return plugin.Errorf("403",
			"the Syncro API token cannot read %s; grant it in Syncro under Admin > API > API Tokens",
			strings.ReplaceAll(resource, "_", " "))
	}
	return nil
}
