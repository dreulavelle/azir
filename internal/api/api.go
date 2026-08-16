// Package api serves core's HTTP surface: the administrator's control plane
// and the tool invocation path.
//
// Authorization is stubbed at this phase — every caller is treated as an
// administrator. Phase 6 supplies the actor from an authenticated session.
// Handlers already take the actor from the server side rather than the request
// body, so that swap does not change any handler.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"

	"github.com/dreulavelle/azir/internal/audit"
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
}

// actor is the stub identity for this phase.
const actor = "admin"

// Routes builds the mux.
func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", s.health)

	mux.HandleFunc("GET /api/registry", s.getRegistry)
	mux.HandleFunc("GET /api/capabilities", s.listCapabilities)
	mux.HandleFunc("POST /api/capabilities/{plugin}/{tool}/decide", s.decideCapability)

	mux.HandleFunc("GET /api/customers", s.listCustomers)
	mux.HandleFunc("POST /api/customers", s.createCustomer)
	mux.HandleFunc("GET /api/customers/{id}", s.getCustomer)
	mux.HandleFunc("POST /api/customers/{id}/identities", s.linkIdentity)

	mux.HandleFunc("GET /api/credentials", s.listCredentials)
	mux.HandleFunc("PUT /api/credentials", s.putCredential)
	mux.HandleFunc("DELETE /api/credentials/{id}", s.deleteCredential)
	mux.HandleFunc("POST /api/credentials/rotate", s.rotateCredentials)

	// Plugin settings. One panel per plugin, rendered from the schema the
	// plugin publishes — no per-integration frontend code.
	mux.HandleFunc("GET /api/plugins/{plugin}/settings", s.getSettings)
	mux.HandleFunc("PUT /api/plugins/{plugin}/settings", s.putSettings)
	mux.HandleFunc("DELETE /api/plugins/{plugin}/settings/{field}", s.deleteSettingSecret)

	mux.HandleFunc("GET /api/audit", s.listAudit)

	mux.HandleFunc("POST /api/invoke/{plugin}/{tool}", s.invoke)

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

	return mux
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

func (s *Server) decideCapability(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Status string `json:"status"`
	}
	if err := decode(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, errBody("invalid json body"))
		return
	}

	pluginName, toolName := r.PathValue("plugin"), r.PathValue("tool")
	err := s.DB.Decide(r.Context(), pluginName, toolName, body.Status, actor)
	if errors.Is(err, store.ErrNotFound) {
		writeJSON(w, http.StatusNotFound, errBody("capability not found"))
		return
	}
	if err != nil {
		s.fail(w, err, "could not record decision")
		return
	}

	s.Audit.Record(r.Context(), audit.Event{
		ActorUserID: actor,
		Action:      "capability.decide",
		Plugin:      pluginName,
		Tool:        toolName,
		Outcome:     audit.OutcomeOK,
		Detail:      body.Status,
	})
	writeJSON(w, http.StatusOK, map[string]string{"status": body.Status})
}

func (s *Server) listCustomers(w http.ResponseWriter, r *http.Request) {
	customers, err := s.DB.ListCustomers(r.Context())
	if err != nil {
		s.fail(w, err, "could not list customers")
		return
	}
	writeJSON(w, http.StatusOK, customers)
}

func (s *Server) createCustomer(w http.ResponseWriter, r *http.Request) {
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
		ActorUserID: actor, Action: "customer.create",
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

func (s *Server) linkIdentity(w http.ResponseWriter, r *http.Request) {
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
		ActorUserID: actor, Action: "customer.link_identity",
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

func (s *Server) putCredential(w http.ResponseWriter, r *http.Request) {
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
		ActorUserID: actor, Action: "credential.put",
		Plugin: body.Plugin, CustomerID: body.CustomerID,
		Outcome: audit.OutcomeOK, Detail: body.Kind,
	})
	writeJSON(w, http.StatusOK, ref)
}

func (s *Server) deleteCredential(w http.ResponseWriter, r *http.Request) {
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
		ActorUserID: actor, Action: "credential.delete", Outcome: audit.OutcomeOK,
	})
	writeJSON(w, http.StatusNoContent, nil)
}

func (s *Server) rotateCredentials(w http.ResponseWriter, r *http.Request) {
	moved, err := s.Creds.Rotate(r.Context())
	if err != nil {
		s.fail(w, err, "rotation failed")
		return
	}
	s.Audit.Record(r.Context(), audit.Event{
		ActorUserID: actor, Action: "credential.rotate", Outcome: audit.OutcomeOK,
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
}

func (s *Server) invoke(w http.ResponseWriter, r *http.Request) {
	pluginName, toolName := r.PathValue("plugin"), r.PathValue("tool")

	tool, ok := s.Reg.Lookup(pluginName, toolName)
	if !ok {
		writeJSON(w, http.StatusNotFound, errBody("no such tool in the current registry snapshot"))
		return
	}

	// The approval gate. Discovery made this tool visible; only an
	// administrator makes it usable.
	approved, err := s.DB.ApprovedTools(r.Context())
	if err != nil {
		s.fail(w, err, "could not check approvals")
		return
	}
	if _, ok := approved[pluginName+"."+toolName]; !ok {
		s.Audit.Record(r.Context(), audit.Event{
			ActorUserID: actor, Action: "tool.invoke",
			Plugin: pluginName, Tool: toolName,
			Outcome: audit.OutcomeDenied, Detail: "not approved",
		})
		writeJSON(w, http.StatusForbidden, errBody("tool is not approved by an administrator"))
		return
	}

	var body invokeRequest
	if err := decode(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, errBody("invalid json body"))
		return
	}

	payload, err := json.Marshal(plugin.Request{
		CustomerID: body.CustomerID,
		Actor:      plugin.Actor{UserID: actor, Role: "admin"},
		Args:       body.Args,
	})
	if err != nil {
		s.fail(w, err, "could not encode request")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	msg, err := s.NC.RequestWithContext(ctx, tool.Subject, payload)
	if err != nil {
		s.Log.Warn("tool request failed", "subject", tool.Subject, "error", err)
		s.recordInvoke(r.Context(), pluginName, toolName, body.CustomerID, audit.OutcomeFailed, "no response")
		writeJSON(w, http.StatusBadGateway, errBody("tool did not respond"))
		return
	}

	if code := msg.Header.Get("Nats-Service-Error-Code"); code != "" {
		s.recordInvoke(r.Context(), pluginName, toolName, body.CustomerID, audit.OutcomeFailed, code)
		writeJSON(w, http.StatusBadGateway, map[string]string{
			"error": msg.Header.Get("Nats-Service-Error"),
			"code":  code,
		})
		return
	}

	s.recordInvoke(r.Context(), pluginName, toolName, body.CustomerID, audit.OutcomeOK, "")
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(msg.Data)
}

func (s *Server) recordInvoke(ctx context.Context, pluginName, toolName, customerID, outcome, detail string) {
	e := audit.Event{
		ActorUserID: actor,
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
