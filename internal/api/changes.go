package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"github.com/dreulavelle/azir/internal/identity"
	"github.com/dreulavelle/azir/pkg/plugin"
)

/*
Changing something in a connected system, because a person said so.

This is deliberately a different door from /api/do, which resolves through the
read index and therefore cannot reach a write at all. The separation is the
whole containment argument: no path that answers "read me this" can ever return
a way to change it.

It is also a different door from the proposal queue. A proposal exists because
a *model* wanted the change, and a model's suggestion has to be looked at by a
person before it touches somebody's helpdesk. A technician pressing a button is
already that person — asking them to approve their own click would teach them to
approve without reading, which is exactly the habit the queue exists to prevent.

Both doors go through the same mayUse gate: the caller holds the tool's declared
permission, an administrator has enabled writes for that plugin, and the tool is
approved. Nothing here is a shortcut around any of that.
*/

type changeRequest struct {
	Args json.RawMessage `json:"args"`
	// CustomerID scopes the call for a per-customer plugin.
	CustomerID string `json:"customer_id,omitempty"`
}

func (s *Server) applyChange(w http.ResponseWriter, r *http.Request, actor identity.Actor) {
	capability := r.PathValue("capability")

	providers := s.Reg.WriteProviders(plugin.Capability(capability))
	if len(providers) == 0 {
		writeJSON(w, http.StatusNotFound, map[string]string{
			"error":      "nothing in this deployment can make that change",
			"capability": capability,
		})
		return
	}

	var body changeRequest
	if err := decode(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, errBody("invalid json body"))
		return
	}

	var customer *uuid.UUID
	if body.CustomerID != "" {
		id, err := uuid.Parse(body.CustomerID)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, errBody("that is not a customer id"))
			return
		}
		customer = &id
	}

	// Providers are sorted, so the same call routes the same way until an
	// administrator changes something.
	for _, qualified := range providers {
		pluginName, toolName, ok := strings.Cut(qualified, ".")
		if !ok {
			continue
		}
		tool, found := s.Reg.Lookup(pluginName, toolName)
		if !found || !tool.Available {
			continue
		}

		result, err := s.performTool(r.Context(), actor, tool, body.Args, customer, byHand)
		if err != nil {
			var gate *gateError
			if errors.As(err, &gate) {
				writeJSON(w, gate.status, gate.body)
				return
			}
			writeJSON(w, http.StatusBadGateway, errBody(err.Error()))
			return
		}

		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(result)
		return
	}

	writeJSON(w, http.StatusServiceUnavailable, map[string]string{
		"error":      "the tool that makes that change is not available right now",
		"capability": capability,
	})
}
