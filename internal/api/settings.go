package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/google/uuid"

	"github.com/dreulavelle/azir/internal/audit"
	"github.com/dreulavelle/azir/internal/identity"
	"github.com/dreulavelle/azir/internal/registry"
	"github.com/dreulavelle/azir/internal/store"
	"github.com/dreulavelle/azir/pkg/plugin"
)

// settingsView is what the admin console renders. The schema comes from the
// plugin itself, so the console needs no knowledge of any particular
// integration — adding a plugin requires no frontend change.
type settingsView struct {
	Plugin       string          `json:"plugin"`
	Description  string          `json:"description"`
	Category     string          `json:"category"`
	ConfigSchema json.RawMessage `json:"config_schema,omitempty"`
	// Values holds non-secret settings only.
	Values map[string]any `json:"values"`
	// SecretsSet reports which secret-marked fields have a stored value,
	// without revealing any of them. The console shows "configured" and an
	// empty input, which is the only honest way to render a write-only field.
	SecretsSet map[string]bool `json:"secrets_set"`
	CustomerID *uuid.UUID      `json:"customer_id,omitempty"`
}

// secretFields returns the property names a plugin marked as credential
// material in its config schema.
func secretFields(schema json.RawMessage) []string {
	if len(schema) == 0 {
		return nil
	}
	var doc struct {
		Properties map[string]map[string]any `json:"properties"`
	}
	if err := json.Unmarshal(schema, &doc); err != nil {
		return nil
	}
	var out []string
	for name, prop := range doc.Properties {
		if marked, ok := prop[plugin.SecretMarker].(bool); ok && marked {
			out = append(out, name)
		}
	}
	return out
}

func (s *Server) findPlugin(name string) (registry.Plugin, bool) {
	for _, p := range s.Reg.Snapshot().Plugins {
		if p.Name == name {
			return p, true
		}
	}
	return registry.Plugin{}, false
}

func customerParam(r *http.Request) (*uuid.UUID, error) {
	raw := r.URL.Query().Get("customer_id")
	if raw == "" {
		return nil, nil
	}
	id, err := uuid.Parse(raw)
	if err != nil {
		return nil, err
	}
	return &id, nil
}

// getSettings returns a plugin's schema and current values for a scope.
func (s *Server) getSettings(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("plugin")
	p, ok := s.findPlugin(name)
	if !ok {
		writeJSON(w, http.StatusNotFound, errBody("no such plugin is running"))
		return
	}

	customerID, err := customerParam(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errBody("invalid customer id"))
		return
	}

	settings, err := s.DB.GetPluginConfig(r.Context(), name, customerID)
	if err != nil {
		s.fail(w, err, "could not load settings")
		return
	}

	view := settingsView{
		Plugin:       p.Name,
		Description:  p.Description,
		Category:     string(p.Category),
		ConfigSchema: p.ConfigSchema,
		Values:       settings.Values,
		SecretsSet:   map[string]bool{},
		CustomerID:   customerID,
	}

	// Report only whether each secret exists. There is deliberately no path
	// that returns a stored secret to a browser.
	refs, err := s.Creds.List(r.Context())
	if err != nil {
		s.fail(w, err, "could not check stored credentials")
		return
	}
	for _, field := range secretFields(p.ConfigSchema) {
		view.SecretsSet[field] = false
		for _, ref := range refs {
			if ref.Plugin != name || ref.Kind != field {
				continue
			}
			if sameScope(ref.CustomerID, customerID) {
				view.SecretsSet[field] = true
			}
		}
	}

	writeJSON(w, http.StatusOK, view)
}

func sameScope(a, b *uuid.UUID) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

// putSettings saves a scope's settings, routing each field by what the
// plugin's schema says it is.
//
// One form, two destinations: secret-marked fields are sealed into the vault,
// everything else lands in plugin_config. An administrator types a password
// and a subdomain into the same panel and does not need to know that they are
// stored entirely differently — which is the point.
func (s *Server) putSettings(w http.ResponseWriter, r *http.Request, actor identity.Actor) {
	name := r.PathValue("plugin")
	p, ok := s.findPlugin(name)
	if !ok {
		writeJSON(w, http.StatusNotFound, errBody("no such plugin is running"))
		return
	}

	customerID, err := customerParam(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errBody("invalid customer id"))
		return
	}

	var body struct {
		Values map[string]any `json:"values"`
	}
	if err := decode(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, errBody("invalid json body"))
		return
	}
	if body.Values == nil {
		body.Values = map[string]any{}
	}

	secrets := secretFields(p.ConfigSchema)
	isSecret := make(map[string]bool, len(secrets))
	for _, f := range secrets {
		isSecret[f] = true
	}

	plain := map[string]any{}
	stored := 0
	for key, value := range body.Values {
		if !isSecret[key] {
			plain[key] = value
			continue
		}
		text, _ := value.(string)
		if text == "" {
			// An empty secret field means "leave it alone". Treating it as a
			// deletion would wipe a working credential every time someone
			// edited an unrelated field on the same form.
			continue
		}
		if _, err := s.Creds.Put(r.Context(), customerID, name, key, []byte(text)); err != nil {
			s.fail(w, err, "could not store credential")
			return
		}
		stored++
	}

	if err := s.DB.SetPluginConfig(r.Context(), name, customerID, plain, actor.Email); err != nil {
		s.fail(w, err, "could not save settings")
		return
	}

	s.Audit.Record(r.Context(), audit.Event{
		ActorUserID: actor.Email,
		Action:      "plugin.configure",
		Plugin:      name,
		CustomerID:  customerID,
		Outcome:     audit.OutcomeOK,
	})
	if stored > 0 {
		s.Audit.Record(r.Context(), audit.Event{
			ActorUserID: actor.Email,
			Action:      "credential.put",
			Plugin:      name,
			CustomerID:  customerID,
			Outcome:     audit.OutcomeOK,
		})
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"saved":             len(plain),
		"credentials_saved": stored,
	})
}

// deleteSettingSecret clears one stored credential.
func (s *Server) deleteSettingSecret(w http.ResponseWriter, r *http.Request, actor identity.Actor) {
	name, field := r.PathValue("plugin"), r.PathValue("field")

	customerID, err := customerParam(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errBody("invalid customer id"))
		return
	}

	refs, err := s.Creds.List(r.Context())
	if err != nil {
		s.fail(w, err, "could not list credentials")
		return
	}
	for _, ref := range refs {
		if ref.Plugin == name && ref.Kind == field && sameScope(ref.CustomerID, customerID) {
			if err := s.Creds.Delete(r.Context(), ref.ID); err != nil && !errors.Is(err, store.ErrNotFound) {
				s.fail(w, err, "could not delete credential")
				return
			}
			s.Audit.Record(r.Context(), audit.Event{
				ActorUserID: actor.Email, Action: "credential.delete",
				Plugin: name, CustomerID: customerID, Outcome: audit.OutcomeOK,
			})
			writeJSON(w, http.StatusNoContent, nil)
			return
		}
	}
	writeJSON(w, http.StatusNotFound, errBody("no such credential"))
}
