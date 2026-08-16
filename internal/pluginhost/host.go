// Package pluginhost answers the requests plugins make of core: resolving the
// credentials and settings an administrator entered in the web console.
//
// These subjects sit outside azir.tool.*, so they are never discovered as
// model-facing capabilities. A tool the model can call and a credential a
// plugin can resolve are different privileges and must not share a namespace.
package pluginhost

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"

	"github.com/dreulavelle/azir/internal/audit"
	"github.com/dreulavelle/azir/internal/store"
	"github.com/dreulavelle/azir/pkg/plugin"
)

// Host serves credential and settings resolution.
type Host struct {
	nc    *nats.Conn
	db    *store.DB
	creds *store.Credentials
	rec   *audit.Recorder
	log   *slog.Logger

	subs []*nats.Subscription
}

// New builds a Host.
func New(nc *nats.Conn, db *store.DB, creds *store.Credentials, rec *audit.Recorder, log *slog.Logger) *Host {
	return &Host{nc: nc, db: db, creds: creds, rec: rec, log: log}
}

// Start subscribes to the resolution subjects and serves until ctx is
// cancelled.
func (h *Host) Start(ctx context.Context) error {
	vaultSub, err := h.nc.Subscribe(plugin.VaultSubjectPrefix+".*", func(m *nats.Msg) {
		h.resolveCredential(ctx, m)
	})
	if err != nil {
		return err
	}
	configSub, err := h.nc.Subscribe(plugin.ConfigSubjectPrefix+".*", func(m *nats.Msg) {
		h.resolveConfig(ctx, m)
	})
	if err != nil {
		_ = vaultSub.Unsubscribe()
		return err
	}
	h.subs = []*nats.Subscription{vaultSub, configSub}

	h.log.Info("plugin host ready",
		"vault_subject", plugin.VaultSubjectPrefix+".*",
		"config_subject", plugin.ConfigSubjectPrefix+".*")

	<-ctx.Done()
	for _, s := range h.subs {
		_ = s.Unsubscribe()
	}
	return nil
}

// subjectPlugin extracts the plugin name from the subject rather than trusting
// the request body. The two must agree: a body claiming another plugin's name
// is either a bug or an attempt to reach a credential this plugin should not
// have, and neither deserves an answer.
func subjectPlugin(subject string) string {
	idx := strings.LastIndex(subject, ".")
	if idx < 0 || idx == len(subject)-1 {
		return ""
	}
	return subject[idx+1:]
}

func (h *Host) resolveCredential(ctx context.Context, m *nats.Msg) {
	name := subjectPlugin(m.Subject)
	if name == "" {
		h.respondErr(m, "400", "malformed subject")
		return
	}

	var req struct {
		CustomerID string `json:"customer_id"`
		Plugin     string `json:"plugin"`
		Kind       string `json:"kind"`
	}
	if err := json.Unmarshal(m.Data, &req); err != nil {
		h.respondErr(m, "400", "invalid request")
		return
	}
	if req.Plugin != "" && req.Plugin != name {
		h.log.Warn("plugin asked for another plugin's credential",
			"subject_plugin", name, "claimed_plugin", req.Plugin, "kind", req.Kind)
		h.rec.Record(ctx, audit.Event{
			ActorUserID: "plugin:" + name,
			Action:      "credential.resolve",
			Plugin:      name,
			Outcome:     audit.OutcomeDenied,
			Detail:      "scope mismatch",
		})
		h.respondErr(m, "403", "credential scope mismatch")
		return
	}
	if req.Kind == "" {
		h.respondErr(m, "400", "kind is required")
		return
	}

	var customerID *uuid.UUID
	if req.CustomerID != "" {
		parsed, err := uuid.Parse(req.CustomerID)
		if err != nil {
			h.respondErr(m, "400", "invalid customer id")
			return
		}
		customerID = &parsed
	}

	secret, err := h.creds.Open(ctx, customerID, name, req.Kind)
	if errors.Is(err, store.ErrNotFound) {
		// Not an error worth auditing as a failure: an unconfigured plugin is
		// an ordinary state, and the tool should say so rather than break.
		h.respondErr(m, "404", "not configured")
		return
	}
	if err != nil {
		h.log.Error("credential resolution failed",
			"plugin", name, "kind", req.Kind, "error", err)
		h.rec.Record(ctx, audit.Event{
			ActorUserID: "plugin:" + name,
			Action:      "credential.resolve",
			Plugin:      name,
			CustomerID:  customerID,
			Outcome:     audit.OutcomeFailed,
			Detail:      req.Kind,
		})
		h.respondErr(m, "500", "resolution failed")
		return
	}

	// Every successful resolution is audited. The value is never recorded —
	// only that it was handed out, to whom, and for which customer.
	h.rec.Record(ctx, audit.Event{
		ActorUserID: "plugin:" + name,
		Action:      "credential.resolve",
		Plugin:      name,
		CustomerID:  customerID,
		Outcome:     audit.OutcomeOK,
		Detail:      req.Kind,
	})

	body, err := json.Marshal(map[string]string{"value": string(secret)})
	if err != nil {
		h.respondErr(m, "500", "resolution failed")
		return
	}
	_ = m.Respond(body)
}

func (h *Host) resolveConfig(ctx context.Context, m *nats.Msg) {
	name := subjectPlugin(m.Subject)
	if name == "" {
		h.respondErr(m, "400", "malformed subject")
		return
	}

	var req struct {
		Plugin     string `json:"plugin"`
		CustomerID string `json:"customer_id"`
	}
	if err := json.Unmarshal(m.Data, &req); err != nil {
		h.respondErr(m, "400", "invalid request")
		return
	}
	if req.Plugin != "" && req.Plugin != name {
		h.respondErr(m, "403", "config scope mismatch")
		return
	}

	var customerID *uuid.UUID
	if req.CustomerID != "" {
		parsed, err := uuid.Parse(req.CustomerID)
		if err != nil {
			h.respondErr(m, "400", "invalid customer id")
			return
		}
		customerID = &parsed
	}

	settings, err := h.db.GetPluginConfig(ctx, name, customerID)
	if err != nil {
		h.log.Error("config resolution failed", "plugin", name, "error", err)
		h.respondErr(m, "500", "resolution failed")
		return
	}

	body, err := json.Marshal(map[string]any{"values": settings.Values})
	if err != nil {
		h.respondErr(m, "500", "resolution failed")
		return
	}
	_ = m.Respond(body)
}

// respondErr mirrors the micro error convention so the SDK can read it the
// same way it reads a tool error.
func (h *Host) respondErr(m *nats.Msg, code, description string) {
	reply := nats.NewMsg(m.Reply)
	reply.Header.Set("Nats-Service-Error-Code", code)
	reply.Header.Set("Nats-Service-Error", description)
	_ = h.nc.PublishMsg(reply)
}
