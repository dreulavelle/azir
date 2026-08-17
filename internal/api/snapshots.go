package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"github.com/dreulavelle/azir/internal/audit"
	"github.com/dreulavelle/azir/internal/identity"
	"github.com/dreulavelle/azir/internal/store"
	"github.com/dreulavelle/azir/internal/supportinfo"
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
	customerID, err := uuid.Parse(r.URL.Query().Get("customer_id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errBody("which customer is this from?"))
		return
	}
	if _, err := s.DB.GetCustomer(r.Context(), customerID); err != nil {
		writeJSON(w, http.StatusNotFound, errBody("no such customer"))
		return
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

	stored := store.Snapshot{
		CustomerID: customerID,
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
	s.Audit.Record(r.Context(), audit.Event{
		ActorUserID: actor.Email, Action: "snapshot.upload",
		CustomerID: &customerID, Outcome: audit.OutcomeOK,
		Detail: header.Filename,
	})
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
func (s *Server) snapshotForModel(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
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
		return nil, errors.New("which snapshot? Give its id")
	}

	stored, err := s.DB.GetSnapshot(ctx, id)
	if err != nil {
		return nil, errors.New("no such snapshot")
	}

	var report supportinfo.Snapshot
	if err := json.Unmarshal(stored.Report, &report); err != nil {
		return nil, errors.New("that snapshot could not be read")
	}

	return json.Marshal(map[string]any{
		"system":   report.System,
		"health":   report.Health,
		"findings": report.Findings,
		"note":     "Metrics are summarised in the findings. The raw series are not included; they are thousands of readings and the findings already say what shape they are.",
	})
}
