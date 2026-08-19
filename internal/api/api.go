// Package api serves core's HTTP surface: the administrator's control plane
// and the tool invocation path.
//
// Every route declares the permission it requires, and the actor is resolved
// from the session server-side rather than taken from the request. A caller
// cannot name itself, and a handler cannot forget to check — the wrapper it is
// registered with has already done so.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"

	"github.com/dreulavelle/azir/internal/audit"
	"github.com/dreulavelle/azir/internal/identity"
	"github.com/dreulavelle/azir/internal/oidc"
	"github.com/dreulavelle/azir/internal/registry"
	"github.com/dreulavelle/azir/internal/store"
	"github.com/dreulavelle/azir/pkg/plugin"
)

// Server holds the dependencies of the HTTP surface.
type Server struct {
	NC    *nats.Conn
	Reg   *registry.Registry
	DB    *store.DB
	Creds *store.Credentials
	Audit *audit.Recorder
	Log   *slog.Logger

	// Web is the built frontend, embedded in the binary. Nil serves the API
	// alone, which is what tests want.
	Web fs.FS

	// Cache serves tool results within the freshness budget each plugin
	// declares. Nil disables caching entirely.
	Cache *ToolCache

	// OIDC holds the discovered identity provider between sign-ins.
	OIDC oidc.Cache
}

// Routes builds the mux.
//
// Every route carries the permission it needs. Enforcement is a wrapper rather
// than a line inside each handler, because a check that must be remembered is a
// check that will eventually be forgotten — and the forgetting is invisible
// until someone reaches something they should not have.
func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", s.health)

	// Open by necessity: you cannot require a session to find out whether an
	// account exists yet, or to create one.
	mux.HandleFunc("GET /api/setup", s.authState)
	mux.HandleFunc("POST /api/setup", s.setup)
	mux.HandleFunc("POST /api/login", s.login)
	mux.HandleFunc("POST /api/logout", s.logout)
	mux.HandleFunc("GET /api/me", s.whoami)

	// The single sign-on round trip. Both ends are necessarily open: the first
	// is reached by someone who is not signed in, and the second by a browser
	// the identity provider redirected. Neither trusts its input — the state,
	// nonce and PKCE verifier were recorded here before the browser left.
	mux.HandleFunc("GET /api/auth/oidc/start", s.oidcStart)
	mux.HandleFunc("GET "+callbackPath, s.oidcCallback)

	mux.HandleFunc("GET /api/auth/settings",
		s.require(identity.PermPluginConfigure, ignoreActor(s.getAuthSettings)))
	mux.HandleFunc("PUT /api/auth/settings",
		s.require(identity.PermPluginConfigure, s.putAuthSettings))

	p := identity.PermToolRead
	mux.HandleFunc("GET /api/registry", s.require(p, ignoreActor(s.getRegistry)))
	mux.HandleFunc("GET /api/capabilities", s.require(p, ignoreActor(s.listCapabilities)))
	mux.HandleFunc("POST /api/capabilities/{plugin}/{tool}/decide",
		s.require(identity.PermPluginApprove, s.decideCapability))

	mux.HandleFunc("GET /api/data",
		s.require(identity.PermDataManage, ignoreActor(s.getDataUsage)))
	mux.HandleFunc("POST /api/data/clear",
		s.require(identity.PermDataManage, s.clearWork))
	mux.HandleFunc("POST /api/data/reset",
		s.require(identity.PermDataManage, s.resetAll))
	mux.HandleFunc("PUT /api/data/retention",
		s.require(identity.PermDataManage, s.putRetention))

	mux.HandleFunc("GET /api/customers", s.require(p, ignoreActor(s.listCustomers)))
	mux.HandleFunc("GET /api/customers/{id}", s.require(p, ignoreActor(s.getCustomer)))
	mux.HandleFunc("POST /api/customers",
		s.require(identity.PermCustomerManage, s.createCustomer))
	mux.HandleFunc("POST /api/customers/{id}/identities",
		s.require(identity.PermCustomerManage, s.linkIdentity))

	mux.HandleFunc("GET /api/credentials",
		s.require(identity.PermCredentialManage, ignoreActor(s.listCredentials)))
	mux.HandleFunc("PUT /api/credentials",
		s.require(identity.PermCredentialManage, s.putCredential))
	mux.HandleFunc("DELETE /api/credentials/{id}",
		s.require(identity.PermCredentialManage, s.deleteCredential))
	mux.HandleFunc("POST /api/credentials/rotate",
		s.require(identity.PermCredentialManage, s.rotateCredentials))

	// Plugin settings. One panel per plugin, rendered from the schema the
	// plugin publishes — no per-integration frontend code. Administrators
	// only: configuring a plugin is how someone would widen their own reach.
	mux.HandleFunc("GET /api/plugins/{plugin}/settings",
		s.require(identity.PermPluginConfigure, ignoreActor(s.getSettings)))
	mux.HandleFunc("PUT /api/plugins/{plugin}/settings",
		s.require(identity.PermPluginConfigure, s.putSettings))
	mux.HandleFunc("DELETE /api/plugins/{plugin}/settings/{field}",
		s.require(identity.PermPluginConfigure, s.deleteSettingSecret))
	mux.HandleFunc("PUT /api/plugins/{plugin}/writes",
		s.require(identity.PermPluginConfigure, s.putWrites))

	mux.HandleFunc("GET /api/users", s.require(identity.PermUserManage, ignoreActor(s.listUsers)))
	mux.HandleFunc("POST /api/users", s.require(identity.PermUserManage, s.createUser))
	mux.HandleFunc("PATCH /api/users/{id}", s.require(identity.PermUserManage, s.updateUser))
	mux.HandleFunc("POST /api/users/{id}/password", s.require(identity.PermUserManage, s.setPassword))
	mux.HandleFunc("DELETE /api/users/{id}", s.require(identity.PermUserManage, s.deleteUser))
	// Reading the roles comes with managing accounts: you cannot sensibly
	// choose somebody's role without seeing what each one means. Changing them
	// is its own permission — deciding who works here and deciding what a
	// technician is trusted with are different decisions.
	mux.HandleFunc("GET /api/roles", s.require(identity.PermUserManage, ignoreActor(s.listRoles)))
	mux.HandleFunc("GET /api/sessions", s.require(identity.PermUserManage, ignoreActor(s.listSessions)))
	mux.HandleFunc("DELETE /api/sessions/{id}", s.require(identity.PermUserManage, s.endSession))
	mux.HandleFunc("POST /api/roles", s.require(identity.PermRoleManage, s.createRole))
	mux.HandleFunc("PATCH /api/roles/{name}", s.require(identity.PermRoleManage, s.updateRole))
	mux.HandleFunc("DELETE /api/roles/{name}", s.require(identity.PermRoleManage, s.deleteRole))

	mux.HandleFunc("GET /api/audit", s.require(identity.PermAuditRead, ignoreActor(s.listAudit)))

	// The assistant. Chats belong to the person who started them, so every
	// route here resolves ownership from the session rather than trusting an id.
	mux.HandleFunc("GET /api/assistant/status", s.require(p, ignoreActor(s.assistantStatus)))
	mux.HandleFunc("GET /api/chats", s.require(p, s.listConversations))
	mux.HandleFunc("POST /api/chats", s.require(p, s.createConversation))
	mux.HandleFunc("GET /api/chats/{id}", s.require(p, s.getConversation))
	mux.HandleFunc("DELETE /api/chats/{id}", s.require(p, s.deleteConversation))
	mux.HandleFunc("POST /api/chats/{id}/messages", s.require(p, s.sendMessage))
	mux.HandleFunc("POST /api/chats/{id}/stream", s.require(p, s.sendMessageStreaming))
	mux.HandleFunc("GET /api/chats/{id}/changes", s.require(p, s.listProposals))
	// Applying is guarded by the tool's own permission rather than by a blanket
	// one: approving a ticket comment and approving a change to a customer's
	// phone system are different decisions and should need different rights.
	mux.HandleFunc("POST /api/changes/{id}", s.require(p, s.decideProposal))

	mux.HandleFunc("GET /api/assistant/settings",
		s.require(identity.PermPluginConfigure, ignoreActor(s.getAssistantSettings)))
	mux.HandleFunc("PUT /api/assistant/settings",
		s.require(identity.PermPluginConfigure, s.putAssistantSettings))
	mux.HandleFunc("POST /api/assistant/test",
		s.require(identity.PermPluginConfigure, s.testAssistant))
	// Readable before signing in, because the sign-in page has to know what
	// this deployment calls itself before it knows who is looking.
	mux.HandleFunc("GET /api/branding", s.getBranding)
	mux.HandleFunc("GET /api/branding/logo", s.getLogo)
	mux.HandleFunc("PUT /api/branding",
		s.require(identity.PermPluginConfigure, s.putBranding))
	mux.HandleFunc("PUT /api/branding/logo",
		s.require(identity.PermPluginConfigure, s.putLogo))

	// Unauthenticated as well, and safe because nothing it is sent is
	// believed: see internal/api/webhooks.go.
	mux.HandleFunc("POST /api/hooks/{secret}", s.receiveWebhook)

	mux.HandleFunc("GET /api/events", s.require(p, s.streamChanges))

	mux.HandleFunc("GET /api/webhooks/{plugin}",
		s.require(identity.PermPluginConfigure, ignoreActor(s.getWebhook)))
	mux.HandleFunc("POST /api/webhooks/{plugin}/rotate",
		s.require(identity.PermPluginConfigure, s.rotateWebhook))

	mux.HandleFunc("GET /api/assistant/models",
		s.require(identity.PermPluginConfigure, s.listAssistantModels))

	mux.HandleFunc("POST /api/invoke/{plugin}/{tool}", s.require(p, s.invoke))
	// By capability rather than by name, so the interface never learns which
	// plugin is behind a screen.
	mux.HandleFunc("POST /api/do/{capability}", s.require(p, s.invokeCapability))
	// Changing something, as opposed to reading it. A separate route because it
	// resolves through a separate index: no way of reading a thing can ever
	// return a way of writing it. The gate inside is the same one the assistant's
	// proposals go through.
	mux.HandleFunc("POST /api/change/{capability}", s.require(p, s.applyChange))

	// Bulk edits from a sheet. Under phone.manage rather than a read
	// permission, including the reading: somebody who cannot change one
	// extension has no business staging forty of them.
	mux.HandleFunc("GET /api/bulk",
		s.require(identity.PermPhoneManage, ignoreActor(s.listBulk)))
	mux.HandleFunc("GET /api/bulk/{id}",
		s.require(identity.PermPhoneManage, ignoreActor(s.getBulk)))
	mux.HandleFunc("GET /api/bulk/extensions",
		s.require(identity.PermPhoneManage, s.listExtensions))
	mux.HandleFunc("GET /api/bulk/starting-sheet",
		s.require(identity.PermPhoneManage, s.startingSheet))
	mux.HandleFunc("POST /api/bulk", s.require(identity.PermPhoneManage, s.uploadBulk))
	mux.HandleFunc("POST /api/bulk/chosen", s.require(identity.PermPhoneManage, s.planChosen))
	mux.HandleFunc("POST /api/bulk/{id}/plan", s.require(identity.PermPhoneManage, s.planBulk))
	mux.HandleFunc("POST /api/bulk/{id}/apply", s.require(identity.PermPhoneManage, s.applyBulk))
	mux.HandleFunc("POST /api/bulk/{id}/cancel", s.require(identity.PermPhoneManage, s.cancelBulk))
	mux.HandleFunc("POST /api/bulk/{id}/revert", s.require(identity.PermPhoneManage, s.revertBulk))

	// Diagnostic snapshots: a phone system's support bundle, read.
	mux.HandleFunc("GET /api/snapshots", s.require(p, ignoreActor(s.listSnapshots)))
	mux.HandleFunc("GET /api/snapshots/{id}", s.require(p, ignoreActor(s.getSnapshot)))
	mux.HandleFunc("POST /api/snapshots", s.require(p, s.uploadSnapshot))
	mux.HandleFunc("POST /api/snapshots/pull", s.require(p, s.pullSnapshot))
	mux.HandleFunc("POST /api/snapshots/{id}/attach", s.require(p, s.attachSnapshot))
	mux.HandleFunc("POST /api/snapshots/{id}/keep", s.require(p, s.keepSnapshot))
	mux.HandleFunc("DELETE /api/snapshots/{id}", s.require(p, s.deleteSnapshot))

	// Recall over finished work. Reading it needs no more than reading a
	// ticket; filling it is an administrator's decision, because it costs
	// thousands of requests against somebody else's API.
	mux.HandleFunc("GET /api/recall", s.require(p, ignoreActor(s.searchRecall)))
	mux.HandleFunc("GET /api/recall/status", s.require(p, ignoreActor(s.getRecallStatus)))
	mux.HandleFunc("POST /api/recall/backfill",
		s.require(identity.PermPluginConfigure, s.startBackfill))

	// The frontend is served by the same binary on the same port, so there is
	// no proxy to configure and no second origin to authorise.
	if s.Web != nil {
		// A mistyped API path must 404 rather than quietly returning the HTML
		// shell, which is a genuinely confusing thing to debug. This pattern
		// is more specific than "/" and less specific than the routes above,
		// so it catches exactly the unmatched /api paths.
		mux.HandleFunc("/api/", func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(w, http.StatusNotFound, errBody("no such endpoint"))
		})
		mux.Handle("/", SPA(s.Web))
	}

	return secured(mux)
}

// ignoreActor adapts a handler that does not need to know who is calling.
func ignoreActor(h func(http.ResponseWriter, *http.Request)) func(http.ResponseWriter, *http.Request, identity.Actor) {
	return func(w http.ResponseWriter, r *http.Request, _ identity.Actor) { h(w, r) }
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	out := map[string]string{"status": "ok", "nats": "ok", "database": "ok"}
	code := http.StatusOK

	if !s.NC.IsConnected() {
		out["nats"], out["status"], code = "disconnected", "degraded", http.StatusServiceUnavailable
	}
	if err := s.DB.Ping(r.Context()); err != nil {
		out["database"], out["status"], code = "unreachable", "degraded", http.StatusServiceUnavailable
	}
	writeJSON(w, code, out)
}

// toolView merges what discovery found with what an administrator decided.
type toolView struct {
	registry.Tool
	Status string `json:"status"`
}

type pluginView struct {
	registry.Plugin
	Tools []toolView `json:"tools"`
}

func (s *Server) getRegistry(w http.ResponseWriter, r *http.Request) {
	snap := s.Reg.Snapshot()

	decisions := map[string]string{}
	if records, err := s.DB.Capabilities(r.Context()); err == nil {
		for _, rec := range records {
			decisions[rec.Plugin+"."+rec.Tool] = rec.Status
		}
	} else {
		s.Log.Warn("could not load capability decisions", "error", err)
	}

	views := make([]pluginView, 0, len(snap.Plugins))
	for _, p := range snap.Plugins {
		v := pluginView{Plugin: p, Tools: make([]toolView, 0, len(p.Tools))}
		for _, t := range p.Tools {
			status := decisions[p.Name+"."+t.Name]
			if status == "" {
				status = store.StatusPending
			}
			v.Tools = append(v.Tools, toolView{Tool: t, Status: status})
		}
		views = append(views, v)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"plugins":      views,
		"capabilities": snap.Capability,
		"at":           snap.At,
	})
}

func (s *Server) listCapabilities(w http.ResponseWriter, r *http.Request) {
	records, err := s.DB.Capabilities(r.Context())
	if err != nil {
		s.fail(w, err, "could not list capabilities")
		return
	}
	writeJSON(w, http.StatusOK, records)
}

func (s *Server) decideCapability(w http.ResponseWriter, r *http.Request, actor identity.Actor) {
	var body struct {
		Status string `json:"status"`
	}
	if err := decode(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, errBody("invalid json body"))
		return
	}

	pluginName, toolName := r.PathValue("plugin"), r.PathValue("tool")
	err := s.DB.Decide(r.Context(), pluginName, toolName, body.Status, actor.Email)
	if errors.Is(err, store.ErrNotFound) {
		writeJSON(w, http.StatusNotFound, errBody("capability not found"))
		return
	}
	if err != nil {
		s.fail(w, err, "could not record decision")
		return
	}

	s.Audit.Record(r.Context(), audit.Event{
		ActorUserID: actor.Email,
		Action:      "capability.decide",
		Plugin:      pluginName,
		Tool:        toolName,
		Outcome:     audit.OutcomeOK,
		Detail:      body.Status,
	})
	writeJSON(w, http.StatusOK, map[string]string{"status": body.Status})
}

func (s *Server) listCustomers(w http.ResponseWriter, r *http.Request) {
	// ?q= runs the trigram-backed fuzzy search, so a name read off a ticket
	// resolves to a customer without anyone knowing an identifier.
	if q := r.URL.Query().Get("q"); q != "" {
		customers, err := s.DB.SearchCustomers(r.Context(), q, 20)
		if err != nil {
			s.fail(w, err, "could not search customers")
			return
		}
		writeJSON(w, http.StatusOK, customers)
		return
	}

	customers, err := s.DB.ListCustomers(r.Context())
	if err != nil {
		s.fail(w, err, "could not list customers")
		return
	}
	writeJSON(w, http.StatusOK, customers)
}

func (s *Server) createCustomer(w http.ResponseWriter, r *http.Request, actor identity.Actor) {
	var body struct {
		DisplayName string `json:"display_name"`
	}
	if err := decode(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, errBody("invalid json body"))
		return
	}
	c, err := s.DB.CreateCustomer(r.Context(), body.DisplayName)
	if err != nil {
		s.fail(w, err, "could not create customer")
		return
	}
	s.Audit.Record(r.Context(), audit.Event{
		ActorUserID: actor.Email, Action: "customer.create",
		CustomerID: &c.ID, Outcome: audit.OutcomeOK,
	})
	writeJSON(w, http.StatusCreated, c)
}

func (s *Server) getCustomer(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errBody("invalid customer id"))
		return
	}
	c, err := s.DB.GetCustomer(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		writeJSON(w, http.StatusNotFound, errBody("customer not found"))
		return
	}
	if err != nil {
		s.fail(w, err, "could not load customer")
		return
	}
	writeJSON(w, http.StatusOK, c)
}

func (s *Server) linkIdentity(w http.ResponseWriter, r *http.Request, actor identity.Actor) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errBody("invalid customer id"))
		return
	}
	var body struct {
		Plugin     string `json:"plugin"`
		ExternalID string `json:"external_id"`
	}
	if err := decode(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, errBody("invalid json body"))
		return
	}
	if err := s.DB.LinkIdentity(r.Context(), id, body.Plugin, body.ExternalID); err != nil {
		s.fail(w, err, "could not link identity")
		return
	}
	s.Audit.Record(r.Context(), audit.Event{
		ActorUserID: actor.Email, Action: "customer.link_identity",
		Plugin: body.Plugin, CustomerID: &id, Outcome: audit.OutcomeOK,
	})
	writeJSON(w, http.StatusOK, map[string]string{"status": "linked"})
}

func (s *Server) listCredentials(w http.ResponseWriter, r *http.Request) {
	// References only. There is deliberately no endpoint that returns a
	// secret value: plaintext leaves the vault solely into a plugin handler.
	refs, err := s.Creds.List(r.Context())
	if err != nil {
		s.fail(w, err, "could not list credentials")
		return
	}
	writeJSON(w, http.StatusOK, refs)
}

func (s *Server) putCredential(w http.ResponseWriter, r *http.Request, actor identity.Actor) {
	var body struct {
		CustomerID *uuid.UUID `json:"customer_id"`
		Plugin     string     `json:"plugin"`
		Kind       string     `json:"kind"`
		Secret     string     `json:"secret"`
	}
	if err := decode(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, errBody("invalid json body"))
		return
	}

	ref, err := s.Creds.Put(r.Context(), body.CustomerID, body.Plugin, body.Kind, []byte(body.Secret))
	if err != nil {
		s.fail(w, err, "could not store credential")
		return
	}
	s.Audit.Record(r.Context(), audit.Event{
		ActorUserID: actor.Email, Action: "credential.put",
		Plugin: body.Plugin, CustomerID: body.CustomerID,
		Outcome: audit.OutcomeOK, Detail: body.Kind,
	})
	writeJSON(w, http.StatusOK, ref)
}

func (s *Server) deleteCredential(w http.ResponseWriter, r *http.Request, actor identity.Actor) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errBody("invalid credential id"))
		return
	}
	err = s.Creds.Delete(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		writeJSON(w, http.StatusNotFound, errBody("credential not found"))
		return
	}
	if err != nil {
		s.fail(w, err, "could not delete credential")
		return
	}
	s.Audit.Record(r.Context(), audit.Event{
		ActorUserID: actor.Email, Action: "credential.delete", Outcome: audit.OutcomeOK,
	})
	writeJSON(w, http.StatusNoContent, nil)
}

func (s *Server) rotateCredentials(w http.ResponseWriter, r *http.Request, actor identity.Actor) {
	moved, err := s.Creds.Rotate(r.Context())
	if err != nil {
		s.fail(w, err, "rotation failed")
		return
	}
	s.Audit.Record(r.Context(), audit.Event{
		ActorUserID: actor.Email, Action: "credential.rotate", Outcome: audit.OutcomeOK,
	})
	writeJSON(w, http.StatusOK, map[string]int{"rewrapped": moved})
}

func (s *Server) listAudit(w http.ResponseWriter, r *http.Request) {
	events, err := audit.Recent(r.Context(), s.DB, 100)
	if err != nil {
		s.fail(w, err, "could not read audit log")
		return
	}
	writeJSON(w, http.StatusOK, events)
}

type invokeRequest struct {
	CustomerID string          `json:"customer_id"`
	Args       json.RawMessage `json:"args,omitempty"`
	// Refresh bypasses the cache. The UI sets it when a technician opens a
	// ticket they are about to act on, where a minute of staleness is a
	// minute too much.
	Refresh bool `json:"refresh,omitempty"`
}

// invokeCapability calls whichever approved tool provides a capability.
//
// This is what lets the interface be a helpdesk rather than a Syncro client.
// A screen asks for "work_items.search" and never learns which plugin answered,
// so swapping the PSA is a configuration change rather than a rewrite — the
// same promise the capability index makes to the agent, kept to the UI too.
func (s *Server) invokeCapability(w http.ResponseWriter, r *http.Request, actor identity.Actor) {
	capability := r.PathValue("capability")

	providers := s.Reg.Providers(plugin.Capability(capability))
	if len(providers) == 0 {
		writeJSON(w, http.StatusNotFound, map[string]string{
			"error":      "nothing in this deployment provides that capability",
			"capability": capability,
		})
		return
	}

	approved, err := s.DB.ApprovedTools(r.Context())
	if err != nil {
		s.fail(w, err, "could not check approvals")
		return
	}

	// Approval is per tool, so a capability with several providers may have
	// only some of them usable. Taking the first approved one keeps the choice
	// deterministic: providers are sorted, so the same call routes the same way
	// until an administrator changes something.
	for _, qualified := range providers {
		if _, ok := approved[qualified]; !ok {
			continue
		}
		pluginName, toolName, ok := strings.Cut(qualified, ".")
		if !ok {
			continue
		}
		if tool, found := s.Reg.Lookup(pluginName, toolName); found && tool.Available {
			s.invokeTool(w, r, actor, tool)
			return
		}
	}

	writeJSON(w, http.StatusForbidden, map[string]any{
		"error":      "no approved and available tool provides that capability",
		"capability": capability,
		"candidates": providers,
	})
}

func (s *Server) invoke(w http.ResponseWriter, r *http.Request, actor identity.Actor) {
	pluginName, toolName := r.PathValue("plugin"), r.PathValue("tool")

	tool, ok := s.Reg.Lookup(pluginName, toolName)
	if !ok {
		writeJSON(w, http.StatusNotFound, errBody("no such tool in the current registry snapshot"))
		return
	}
	s.invokeTool(w, r, actor, tool)
}

// invokeTool is the one path a tool call takes, whether it was reached by name
// or by capability. Sharing it is what keeps the write gate, the approval gate
// and the audit trail from having a second implementation to forget about.
// gateError is a refusal with the status a caller should be told.
type gateError struct {
	status int
	body   map[string]string
}

func (e *gateError) Error() string { return e.body["error"] }

// mayUse applies every condition that stands between somebody and a change.
//
// One implementation, used by the button on a screen and by approving something
// the assistant proposed, because two copies of a permission check is two
// places to forget one. A write needs three independent conditions, because
// this is the one place where being wrong is irreversible: the caller holds the
// tool's declared permission, an administrator has enabled writes for this
// plugin, and the tool was approved like any other.
func (s *Server) mayUse(ctx context.Context, actor identity.Actor, tool registry.Tool) error {
	pluginName, toolName := tool.Plugin, tool.Name

	if tool.Mutates {
		if err := actor.Require(tool.RequiresPermission); err != nil {
			s.Audit.Record(ctx, audit.Event{
				ActorUserID: actor.Email, Action: "tool.write",
				Plugin: pluginName, Tool: toolName,
				Outcome: audit.OutcomeDenied, Detail: "missing " + tool.RequiresPermission,
			})
			return &gateError{status: http.StatusForbidden, body: map[string]string{
				"error":               "your role does not permit this action",
				"required_permission": tool.RequiresPermission,
			}}
		}
		enabled, err := s.writesEnabled(ctx, pluginName)
		if err != nil {
			return &gateError{status: http.StatusInternalServerError,
				body: map[string]string{"error": "could not check whether writes are enabled"}}
		}
		if !enabled {
			s.Audit.Record(ctx, audit.Event{
				ActorUserID: actor.Email, Action: "tool.write",
				Plugin: pluginName, Tool: toolName,
				Outcome: audit.OutcomeRefused, Detail: "writes disabled for this plugin",
			})
			return &gateError{status: http.StatusForbidden, body: map[string]string{
				"error": "writes are disabled for this plugin; an administrator can enable them in plugin settings"}}
		}
	}

	// The approval gate. Discovery made this tool visible; only an
	// administrator makes it usable.
	approved, err := s.DB.ApprovedTools(ctx)
	if err != nil {
		return &gateError{status: http.StatusInternalServerError,
			body: map[string]string{"error": "could not check approvals"}}
	}
	if _, ok := approved[pluginName+"."+toolName]; !ok {
		s.Audit.Record(ctx, audit.Event{
			ActorUserID: actor.Email, Action: "tool.invoke",
			Plugin: pluginName, Tool: toolName,
			Outcome: audit.OutcomeDenied, Detail: "not approved",
		})
		return &gateError{status: http.StatusForbidden,
			body: map[string]string{"error": "tool is not approved by an administrator"}}
	}
	return nil
}

func (s *Server) invokeTool(w http.ResponseWriter, r *http.Request, actor identity.Actor, tool registry.Tool) {
	pluginName, toolName := tool.Plugin, tool.Name

	if err := s.mayUse(r.Context(), actor, tool); err != nil {
		var gate *gateError
		if errors.As(err, &gate) {
			writeJSON(w, gate.status, gate.body)
			return
		}
		s.fail(w, err, "could not check whether that is allowed")
		return
	}

	var body invokeRequest
	if err := decode(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, errBody("invalid json body"))
		return
	}

	payload, err := json.Marshal(plugin.Request{
		CustomerID: body.CustomerID,
		Actor:      plugin.Actor{UserID: actor.Email, Role: actor.Role},
		Args:       body.Args,
	})
	if err != nil {
		s.fail(w, err, "could not encode request")
		return
	}

	// The vendor call, wrapped so the cache can decide whether to make it.
	var toolErr *toolFailure
	fetch := func(ctx context.Context) (json.RawMessage, error) {
		callCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()

		msg, err := s.NC.RequestWithContext(callCtx, tool.Subject, payload)
		if err != nil {
			s.Log.Warn("tool request failed", "subject", tool.Subject, "error", err)
			toolErr = &toolFailure{status: http.StatusBadGateway, body: errBody("tool did not respond")}
			return nil, err
		}
		if code := msg.Header.Get("Nats-Service-Error-Code"); code != "" {
			toolErr = &toolFailure{
				status: http.StatusBadGateway,
				body: map[string]string{
					"error": msg.Header.Get("Nats-Service-Error"),
					"code":  code,
				},
				detail: code,
			}
			// A 4xx from a plugin is "you asked wrongly", not "the vendor is
			// down". Answering it from cache would hide the mistake behind a
			// stale but plausible answer — which is how somebody ends up
			// reading one customer's phone system while looking at another.
			// Only an outage earns the stale fallback.
			if strings.HasPrefix(code, "4") {
				return nil, callerMistake{code: code}
			}
			return nil, errors.New("tool returned an error")
		}
		return msg.Data, nil
	}

	var customerUUID *uuid.UUID
	if id, err := uuid.Parse(body.CustomerID); err == nil {
		customerUUID = &id
	}

	var result Result
	if s.Cache != nil && !tool.Mutates {
		result, err = s.Cache.Do(r.Context(), tool, customerUUID, body.Args, body.Refresh, fetch)
	} else {
		var raw json.RawMessage
		raw, err = fetch(r.Context())
		result = Result{Payload: raw, Source: "live"}
	}

	if err != nil {
		detail := "no response"
		status, failBody := http.StatusBadGateway, errBody("tool did not respond")
		if toolErr != nil {
			status, failBody, detail = toolErr.status, toolErr.body, toolErr.detail
		}
		s.recordInvoke(r.Context(), actor, pluginName, toolName, body.CustomerID, audit.OutcomeFailed, detail)
		writeJSON(w, status, failBody)
		return
	}

	s.recordInvoke(r.Context(), actor, pluginName, toolName, body.CustomerID, audit.OutcomeOK, result.Source)

	// How the answer was obtained travels with it. A caller — a person or a
	// model — reasons differently about a number that is four minutes old than
	// about one that is live, and it can only do that if it is told.
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Azir-Source", result.Source)
	w.Header().Set("X-Azir-Age-Seconds", strconv.Itoa(result.AgeSeconds))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(result.Payload)
}

// writesEnabled reports whether an administrator has turned on writes for a
// plugin. Off unless deliberately switched on: read-only stays the default,
// and a deployment that never enables it behaves exactly as before.
func (s *Server) writesEnabled(ctx context.Context, pluginName string) (bool, error) {
	settings, err := s.DB.GetPluginConfig(ctx, pluginName, nil)
	if err != nil {
		return false, err
	}
	enabled, _ := settings.Values["writes_enabled"].(bool)
	return enabled, nil
}

// toolFailure carries an error shape from inside the fetch closure.
type toolFailure struct {
	status int
	body   map[string]string
	detail string
}

func (s *Server) recordInvoke(ctx context.Context, actor identity.Actor, pluginName, toolName, customerID, outcome, detail string) {
	e := audit.Event{
		ActorUserID: actor.Email,
		Action:      "tool.invoke",
		Plugin:      pluginName,
		Tool:        toolName,
		Outcome:     outcome,
		Detail:      detail,
	}
	if id, err := uuid.Parse(customerID); err == nil {
		e.CustomerID = &id
	}
	s.Audit.Record(ctx, e)
}

// fail logs the real error and returns a generic message. Database errors can
// carry connection strings and query text; neither belongs in a response.
func (s *Server) fail(w http.ResponseWriter, err error, msg string) {
	s.Log.Error(msg, "error", err)
	writeJSON(w, http.StatusInternalServerError, errBody(msg))
}

func decode(r *http.Request, v any) error {
	if r.ContentLength == 0 {
		return nil
	}
	return json.NewDecoder(r.Body).Decode(v)
}

func errBody(msg string) map[string]string { return map[string]string{"error": msg} }

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	if v != nil {
		_ = json.NewEncoder(w).Encode(v)
	}
}
