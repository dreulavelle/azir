package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/dreulavelle/azir/internal/audit"
	"github.com/dreulavelle/azir/internal/identity"
	"github.com/dreulavelle/azir/internal/store"
	"github.com/dreulavelle/azir/internal/supportinfo"
	"github.com/dreulavelle/azir/pkg/plugin"
)

/*
Diagnostic snapshots.

A 3CX support bundle is what an engineer asks for when a phone problem has
resisted everything else, and it is almost never read: forty megabytes, four
hundred files, and no indication which nine of them matter. This takes the zip,
reads those nine, and keeps the answer.

The bundle itself is never stored. It is enormous and it is the customer's, and
everything worth having out of it exists as a finding by the time the upload
returns.
*/

// maxBundle is the largest upload accepted. The real ones are thirteen to forty
// megabytes; a hundred leaves room for a bigger site without letting an
// accidental upload of something else consume the machine.
const maxBundle = 100 << 20

func (s *Server) uploadSnapshot(w http.ResponseWriter, r *http.Request, actor identity.Actor) {
	// A customer may be named, and usually is not. The bundle knows which
	// phone system it came off, and that is a better answer than a list of
	// every customer with the right one somewhere in it.
	var customerID uuid.UUID
	if asked := strings.TrimSpace(r.URL.Query().Get("customer_id")); asked != "" {
		parsed, err := uuid.Parse(asked)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, errBody("that is not a customer id"))
			return
		}
		if _, err := s.DB.GetCustomer(r.Context(), parsed); err != nil {
			writeJSON(w, http.StatusNotFound, errBody("no such customer"))
			return
		}
		customerID = parsed
	}

	if err := r.ParseMultipartForm(16 << 20); err != nil {
		writeJSON(w, http.StatusBadRequest, errBody("that upload could not be read"))
		return
	}
	file, header, err := r.FormFile("bundle")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errBody("attach the support info zip as 'bundle'"))
		return
	}
	defer file.Close() //nolint:errcheck // read-only

	// Read into memory rather than to disk. A zip needs random access to be
	// read at all, the ceiling is a hundred megabytes, and a temporary file
	// holding a customer's logs is a thing to clean up and eventually fail to.
	raw, err := io.ReadAll(io.LimitReader(file, maxBundle+1))
	if err != nil {
		s.fail(w, err, "that upload could not be read")
		return
	}
	if len(raw) > maxBundle {
		writeJSON(w, http.StatusRequestEntityTooLarge,
			errBody("that file is larger than 100 MB, which is bigger than any support bundle should be"))
		return
	}

	snapshot, err := supportinfo.Read(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errBody(err.Error()))
		return
	}
	// Released before anything else happens. Holding forty megabytes of
	// somebody's logs for longer than the parse is the thing this design is
	// most trying to avoid.
	raw = nil

	report, err := json.Marshal(snapshot)
	if err != nil {
		s.fail(w, err, "that report could not be stored")
		return
	}

	// Nobody said whose it was, so the bundle is asked. Its FQDN is the same
	// address an administrator typed into that customer's 3CX settings, which
	// makes the match exact rather than a guess at a name.
	if customerID == uuid.Nil {
		if found, err := s.DB.CustomerByPluginValue(r.Context(), "3cx", "fqdn", snapshot.System.FQDN); err == nil {
			customerID = found
		}
	}

	stored := store.Snapshot{
		CustomerID: customerID,
		FQDN:       snapshot.System.FQDN,
		Filename:   header.Filename,
		Report:     report,
		Findings:   len(snapshot.Findings),
		Worst:      worstOf(snapshot.Findings),
		UploadedBy: actor.Email,
	}
	if !snapshot.System.CapturedAt.IsZero() {
		at := snapshot.System.CapturedAt
		stored.CapturedAt = &at
	}

	saved, err := s.DB.AddSnapshot(r.Context(), stored)
	if err != nil {
		s.fail(w, err, "that snapshot could not be saved")
		return
	}

	// The filename only. What is inside is the customer's, and the audit log
	// exists to say who did what — not to hold a second copy of the evidence.
	event := audit.Event{
		ActorUserID: actor.Email, Action: "snapshot.upload",
		Outcome: audit.OutcomeOK, Detail: header.Filename,
	}
	if customerID != uuid.Nil {
		event.CustomerID = &customerID
	}
	s.Audit.Record(r.Context(), event)
	s.announce("snapshot")

	writeJSON(w, http.StatusCreated, map[string]any{
		"snapshot": saved,
		"report":   snapshot,
	})
}

func (s *Server) listSnapshots(w http.ResponseWriter, r *http.Request) {
	if raw := r.URL.Query().Get("customer_id"); raw != "" {
		id, err := uuid.Parse(raw)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, errBody("that is not a customer id"))
			return
		}
		list, err := s.DB.Snapshots(r.Context(), id)
		if err != nil {
			s.fail(w, err, "could not read the snapshots")
			return
		}
		writeJSON(w, http.StatusOK, list)
		return
	}

	list, err := s.DB.AllSnapshots(r.Context(), 50)
	if err != nil {
		s.fail(w, err, "could not read the snapshots")
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) getSnapshot(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errBody("that is not a snapshot id"))
		return
	}
	snap, err := s.DB.GetSnapshot(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		writeJSON(w, http.StatusNotFound, errBody("no such snapshot"))
		return
	}
	if err != nil {
		s.fail(w, err, "could not read that snapshot")
		return
	}
	writeJSON(w, http.StatusOK, snap)
}

func (s *Server) deleteSnapshot(w http.ResponseWriter, r *http.Request, actor identity.Actor) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errBody("that is not a snapshot id"))
		return
	}
	if err := s.DB.DeleteSnapshot(r.Context(), id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeJSON(w, http.StatusNotFound, errBody("no such snapshot"))
			return
		}
		s.fail(w, err, "could not remove that snapshot")
		return
	}
	s.Audit.Record(r.Context(), audit.Event{
		ActorUserID: actor.Email, Action: "snapshot.delete",
		Outcome: audit.OutcomeOK, Detail: id.String(),
	})
	w.WriteHeader(http.StatusNoContent)
}

// worstOf is the highest severity present, for a list that has to say at a
// glance whether a capture is worth opening.
func worstOf(findings []supportinfo.Finding) string {
	worst := ""
	for _, f := range findings {
		switch f.Severity {
		case supportinfo.Critical:
			return string(supportinfo.Critical)
		case supportinfo.Warning:
			worst = string(supportinfo.Warning)
		case supportinfo.Note:
			if worst == "" {
				worst = string(supportinfo.Note)
			}
		}
	}
	return worst
}

// snapshotForModel hands the assistant one capture's findings.
//
// Trimmed to the findings and the facts. The series are thousands of readings
// and a model cannot see a shape in a list of numbers — what it can use is the
// sentence somebody already wrote about what those numbers mean.
//
// The identifier is optional, and usually absent. A model has no way to know a
// snapshot's id: it is a uuid that exists on a screen the model cannot see, and
// a tool that demands one can only be called after a person has pasted it. With
// no id it reads the newest capture for the customer the conversation is about,
// which is what somebody means by "check the support info" almost every time.
func (s *Server) snapshotForModel(ctx context.Context, onBehalf uuid.UUID, raw json.RawMessage) (json.RawMessage, error) {
	var args struct {
		SnapshotID string `json:"snapshot_id"`
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &args); err != nil {
			return nil, errors.New("that request could not be read")
		}
	}

	id, err := uuid.Parse(strings.TrimSpace(args.SnapshotID))
	if err != nil {
		if onBehalf == uuid.Nil {
			return nil, errors.New("which customer's capture? This conversation is not about one")
		}
		captures, err := s.DB.Snapshots(ctx, onBehalf)
		if err != nil || len(captures) == 0 {
			return nil, errors.New("no support capture has been uploaded for this customer")
		}
		id = captures[0].ID
	}

	stored, err := s.DB.GetSnapshot(ctx, id)
	if err != nil {
		return nil, errors.New("no such snapshot")
	}

	// A capture belongs to the customer it was taken from. Answering about
	// another one across a conversation boundary would leak one customer's
	// telephony into another's ticket.
	if onBehalf != uuid.Nil && stored.CustomerID != onBehalf {
		return nil, errors.New("that capture belongs to a different customer")
	}

	var report supportinfo.Snapshot
	if err := json.Unmarshal(stored.Report, &report); err != nil {
		return nil, errors.New("that snapshot could not be read")
	}

	return json.Marshal(map[string]any{
		"captured_at": stored.CapturedAt,
		"filename":    stored.Filename,
		"system":      report.System,
		"health":      report.Health,
		"findings":    report.Findings,
		"note":        "Metrics are summarised in the findings. The raw series are not included; they are thousands of readings and the findings already say what shape they are.",
	})
}

/*
attachSnapshot links a capture to a customer after the fact.

The other half of letting a bundle in without one. Most attach themselves by
FQDN; the rest are ones from a phone system Azir has never been pointed at, and
somebody has to say whose they are. Passing no customer unlinks it again, so a
capture attached to the wrong one is not stuck there.
*/
func (s *Server) attachSnapshot(w http.ResponseWriter, r *http.Request, actor identity.Actor) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errBody("that is not a snapshot id"))
		return
	}
	var body struct {
		CustomerID string `json:"customer_id"`
	}
	if err := decode(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, errBody("invalid json body"))
		return
	}

	var customerID uuid.UUID
	if asked := strings.TrimSpace(body.CustomerID); asked != "" {
		parsed, err := uuid.Parse(asked)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, errBody("that is not a customer id"))
			return
		}
		if _, err := s.DB.GetCustomer(r.Context(), parsed); err != nil {
			writeJSON(w, http.StatusNotFound, errBody("no such customer"))
			return
		}
		customerID = parsed
	}

	if err := s.DB.AttachSnapshot(r.Context(), id, customerID); err != nil {
		s.notFoundOr(w, err, "that capture could not be attached")
		return
	}
	s.Audit.Record(r.Context(), audit.Event{
		ActorUserID: actor.Email, Action: "snapshot.attach",
		Outcome: audit.OutcomeOK, Detail: id.String(),
	})
	s.announce("snapshot")
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// keepSnapshot pins a capture past its expiry, or lets it go again.
func (s *Server) keepSnapshot(w http.ResponseWriter, r *http.Request, actor identity.Actor) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errBody("that is not a snapshot id"))
		return
	}
	var body struct {
		Keep bool `json:"keep"`
	}
	if err := decode(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, errBody("invalid json body"))
		return
	}
	if err := s.DB.KeepSnapshot(r.Context(), id, body.Keep); err != nil {
		s.notFoundOr(w, err, "that capture could not be updated")
		return
	}
	s.Audit.Record(r.Context(), audit.Event{
		ActorUserID: actor.Email, Action: "snapshot.keep",
		Outcome: audit.OutcomeOK, Detail: id.String(),
	})
	s.announce("snapshot")
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// notFoundOr answers a missing row as 404 and anything else as a failure.
func (s *Server) notFoundOr(w http.ResponseWriter, err error, message string) {
	if errors.Is(err, store.ErrNotFound) {
		writeJSON(w, http.StatusNotFound, errBody("no such capture"))
		return
	}
	s.fail(w, err, message)
}

/*
pullSnapshot collects a capture from a customer's phone system.

The other way in. Uploading a bundle means asking somebody to press a button in
3CX, wait, find the zip and send it; this asks the phone system directly and
comes back in seconds. What it gets is the event log rather than a bundle, and
the report says so — see supportinfo.FromEvents — so nobody mistakes a live
capture for the full thing.

It goes through the ordinary read path: the same capability, the same approval
gate, the same audit entry as a technician clicking anything else.
*/
func (s *Server) pullSnapshot(w http.ResponseWriter, r *http.Request, actor identity.Actor) {
	customerID, err := uuid.Parse(r.URL.Query().Get("customer_id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errBody("which customer's phone system?"))
		return
	}
	customer, err := s.DB.GetCustomer(r.Context(), customerID)
	if err != nil {
		writeJSON(w, http.StatusNotFound, errBody("no such customer"))
		return
	}

	days := 7
	if asked := strings.TrimSpace(r.URL.Query().Get("days")); asked != "" {
		if n, err := strconv.Atoi(asked); err == nil && n > 0 && n <= 30 {
			days = n
		}
	}

	args, err := json.Marshal(map[string]any{"days": days})
	if err != nil {
		s.fail(w, err, "could not ask for a capture")
		return
	}
	raw, err := s.readForCustomer(r.Context(), actor, string(plugin.CapPhoneCapture), customerID, args)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, errBody(err.Error()))
		return
	}

	var collected struct {
		Events []supportinfo.LiveEvent `json:"events"`
		Host   string                  `json:"host"`
	}
	if err := json.Unmarshal(raw, &collected); err != nil {
		s.fail(w, err, "that capture could not be read")
		return
	}
	if len(collected.Events) == 0 {
		writeJSON(w, http.StatusOK, map[string]any{
			"empty": true,
			"note": "The phone system answered but had nothing logged in that period, which usually " +
				"means it is quiet rather than that something went wrong.",
		})
		return
	}

	snapshot := supportinfo.FromEvents(collected.Host, collected.Events)
	report, err := json.Marshal(snapshot)
	if err != nil {
		s.fail(w, err, "that report could not be stored")
		return
	}

	captured := snapshot.System.CapturedAt
	saved, err := s.DB.AddSnapshot(r.Context(), store.Snapshot{
		CustomerID: customerID,
		FQDN:       snapshot.System.FQDN,
		Kind:       "3cx-live",
		Filename:   fmt.Sprintf("%s — last %d days", customer.DisplayName, days),
		CapturedAt: &captured,
		Report:     report,
		Findings:   len(snapshot.Findings),
		Worst:      worstOf(snapshot.Findings),
		UploadedBy: actor.Email,
	})
	if err != nil {
		s.fail(w, err, "that capture could not be saved")
		return
	}

	s.Audit.Record(r.Context(), audit.Event{
		ActorUserID: actor.Email, Action: "snapshot.pull",
		CustomerID: &customerID, Outcome: audit.OutcomeOK,
		Detail: fmt.Sprintf("%d events over %d days", len(collected.Events), days),
	})
	s.announce("snapshot")

	writeJSON(w, http.StatusCreated, map[string]any{
		"snapshot": saved,
		"report":   snapshot,
	})
}

/*
readForCustomer performs one read against a named customer's system.

readCapability, which the assistant uses, carries no customer — it answers
questions about the deployment. A phone system is per customer and the plugin
refuses a request without one, deliberately, so that "no extensions are
registered" can never be about a business nobody named.

The approval gate still applies. A read is allowed by default, but a capability
an administrator has not approved is not available to anybody, and the check
belongs here rather than being assumed from the fact that this only reads.
*/
func (s *Server) readForCustomer(
	ctx context.Context,
	actor identity.Actor,
	capability string,
	customer uuid.UUID,
	args json.RawMessage,
) (json.RawMessage, error) {
	tool, err := s.resolveCapability(ctx, capability)
	if err != nil {
		return nil, err
	}
	if err := s.mayUse(ctx, actor, tool); err != nil {
		var gate *gateError
		if errors.As(err, &gate) {
			return nil, errors.New("that is not approved for use yet")
		}
		return nil, err
	}

	payload, err := json.Marshal(plugin.Request{
		CustomerID: customer.String(),
		Actor:      plugin.Actor{UserID: actor.Email, Role: actor.Role},
		Args:       args,
	})
	if err != nil {
		return nil, errors.New("that request could not be encoded")
	}

	// Longer than an ordinary read: this pages a whole event log rather than
	// fetching one screen of it.
	callCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()

	msg, err := s.NC.RequestWithContext(callCtx, tool.Subject, payload)
	if err != nil {
		s.recordInvoke(ctx, actor, tool.Plugin, tool.Name, "", audit.OutcomeFailed, "no response")
		return nil, errors.New("the phone system did not respond")
	}
	if code := msg.Header.Get("Nats-Service-Error-Code"); code != "" {
		reason := msg.Header.Get("Nats-Service-Error")
		s.recordInvoke(ctx, actor, tool.Plugin, tool.Name, "", audit.OutcomeFailed, reason)
		return nil, errors.New(reason)
	}
	s.recordInvoke(ctx, actor, tool.Plugin, tool.Name, "", audit.OutcomeOK, "")
	return msg.Data, nil
}
