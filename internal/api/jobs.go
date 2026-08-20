package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/dreulavelle/azir/internal/audit"
	"github.com/dreulavelle/azir/internal/identity"
	"github.com/dreulavelle/azir/internal/registry"
	"github.com/dreulavelle/azir/internal/store"
	"github.com/dreulavelle/azir/pkg/plugin"
)

/*
Work somebody asked for, to happen later.

Generic on purpose. A job is a tool call with a time on it, so anything Azir
can do it can do later — a department's hours, a routing device swap at two in
the morning, whatever a plugin registers next year. Nothing here knows what any
of them mean.

The approval is inherited. A person with the permission said do this and named
the moment; that is the approval, and there is nobody present at the moment
itself to give a second one. What is checked again when it fires is every
standing gate — the account, the role, whether writes are enabled for the
plugin, whether the tool is still approved — so revoking any of them stops
scheduled work. See RunJob.
*/

// Scheduler is the timer half. An interface so the API package does not depend
// on JetStream, and so tests can arm nothing.
type Scheduler interface {
	Arm(ctx context.Context, job store.Job) error
	Disarm(ctx context.Context, id uuid.UUID) error
}

// listJobs answers the screen: what is armed, and what has already happened.
func (s *Server) listJobs(w http.ResponseWriter, r *http.Request, actor identity.Actor) {
	var customer *uuid.UUID
	if raw := strings.TrimSpace(r.URL.Query().Get("customer_id")); raw != "" {
		id, err := uuid.Parse(raw)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, errBody("that is not a customer"))
			return
		}
		customer = &id
	}

	jobs, err := s.DB.ListJobs(r.Context(), customer, 100)
	if err != nil {
		s.fail(w, err, "could not read what is scheduled")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"jobs": jobs})
}

/*
createJob arms one.

The tool is resolved here rather than when it fires. Naming a capability and
resolving it later would mean the thing that runs is whatever happens to
provide that capability in a month's time, which is not what anybody approved.
Resolving now also fails fast: an unapproved tool, a missing permission or a
disabled plugin is a refusal on the screen, in front of the person who can do
something about it.
*/
func (s *Server) createJob(w http.ResponseWriter, r *http.Request, actor identity.Actor) {
	if s.Jobs == nil {
		writeJSON(w, http.StatusServiceUnavailable, errBody("scheduling is not running"))
		return
	}

	var body struct {
		Capability string          `json:"capability"`
		Plugin     string          `json:"plugin"`
		Tool       string          `json:"tool"`
		Args       json.RawMessage `json:"args"`
		Title      string          `json:"title"`
		CustomerID string          `json:"customer_id"`
		RunAt      time.Time       `json:"run_at"`
		Repeats    string          `json:"repeats"`
		TimeZone   string          `json:"time_zone"`
	}
	if err := decode(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, errBody("invalid json body"))
		return
	}

	tool, err := s.jobTool(r.Context(), body.Capability, body.Plugin, body.Tool)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errBody(err.Error()))
		return
	}

	// The gate, at the moment of approval. Everything it checks is checked
	// again when the job fires; passing it here is what makes the refusal
	// visible to somebody rather than a line in a log tomorrow.
	if err := s.mayUse(r.Context(), actor, tool); err != nil {
		var gate *gateError
		if errors.As(err, &gate) {
			writeJSON(w, gate.status, gate.body)
			return
		}
		s.fail(w, err, "could not check whether that is allowed")
		return
	}

	job := store.Job{
		Plugin:      tool.Plugin,
		Tool:        tool.Name,
		Args:        body.Args,
		Title:       strings.TrimSpace(body.Title),
		RunAt:       body.RunAt.UTC(),
		Repeats:     strings.TrimSpace(body.Repeats),
		TimeZone:    strings.TrimSpace(body.TimeZone),
		CreatedBy:   actor.Email,
		CreatedByID: &actor.UserID,
	}
	if job.Title == "" {
		job.Title = tool.Plugin + "." + tool.Name
	}
	if raw := strings.TrimSpace(body.CustomerID); raw != "" {
		id, err := uuid.Parse(raw)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, errBody("that is not a customer"))
			return
		}
		if _, err := s.DB.GetCustomer(r.Context(), id); err != nil {
			writeJSON(w, http.StatusNotFound, errBody("no such customer"))
			return
		}
		job.CustomerID = &id
	}
	if err := checkWhen(job); err != nil {
		writeJSON(w, http.StatusBadRequest, errBody(err.Error()))
		return
	}

	created, err := s.DB.CreateJob(r.Context(), job)
	if err != nil {
		s.fail(w, err, "could not schedule that")
		return
	}
	/*
		Arming can fail — a cron expression the server rejects, a zone it does
		not know — and a row nothing will ever fire is worse than no row: it is
		a promise on a screen. So the row goes away with it.
	*/
	if err := s.Jobs.Arm(r.Context(), created); err != nil {
		if err := s.DB.DeleteJob(r.Context(), created.ID); err != nil {
			s.Log.Error("could not remove a job that was never armed",
				"job", created.ID, "error", err)
		}
		writeJSON(w, http.StatusBadRequest, errBody("could not schedule that: "+err.Error()))
		return
	}

	s.Audit.Record(r.Context(), audit.Event{
		ActorUserID: actor.Email,
		Action:      "job.schedule",
		Plugin:      created.Plugin,
		Tool:        created.Tool,
		CustomerID:  created.CustomerID,
		Outcome:     audit.OutcomeOK,
		Detail:      created.Title + ", for " + whenText(created),
	})

	writeJSON(w, http.StatusCreated, created)
}

/*
cancelJob disarms one.

Gated on what the job's own tool needs, so cancelling a change to a phone
system asks for the same permission scheduling it did. Deliberately not gated
on the tool still being approved: an administrator who has just withdrawn
approval for a tool is exactly the person who then wants the work already armed
with it taken down.
*/
func (s *Server) cancelJob(w http.ResponseWriter, r *http.Request, actor identity.Actor) {
	if s.Jobs == nil {
		writeJSON(w, http.StatusServiceUnavailable, errBody("scheduling is not running"))
		return
	}
	id, err := uuid.Parse(strings.TrimSpace(r.PathValue("id")))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errBody("say which job to cancel"))
		return
	}

	job, err := s.DB.GetJob(r.Context(), id)
	if errors.Is(err, store.ErrNoJob) {
		writeJSON(w, http.StatusNotFound, errBody("no such scheduled job"))
		return
	}
	if err != nil {
		s.fail(w, err, "could not read that job")
		return
	}

	needed := identity.PermToolWrite
	if tool, found := s.Reg.Lookup(job.Plugin, job.Tool); found && tool.RequiresPermission != "" {
		needed = tool.RequiresPermission
	}
	if err := actor.Require(needed); err != nil {
		writeJSON(w, http.StatusForbidden, map[string]string{
			"error":               "your role does not permit cancelling that",
			"required_permission": needed,
		})
		return
	}

	// The timer goes first. A row that says cancelled while the stream still
	// holds a live schedule is the one state that could still surprise
	// somebody; the reverse is merely untidy and is repaired at startup.
	if err := s.Jobs.Disarm(r.Context(), id); err != nil {
		s.fail(w, err, "could not cancel that")
		return
	}
	cancelled, err := s.DB.CancelJob(r.Context(), id, actor.Email)
	if errors.Is(err, store.ErrNoJob) {
		writeJSON(w, http.StatusConflict, errBody("that job has already run or been cancelled"))
		return
	}
	if err != nil {
		s.fail(w, err, "could not cancel that")
		return
	}

	s.Audit.Record(r.Context(), audit.Event{
		ActorUserID: actor.Email,
		Action:      "job.cancel",
		Plugin:      cancelled.Plugin,
		Tool:        cancelled.Tool,
		CustomerID:  cancelled.CustomerID,
		Outcome:     audit.OutcomeOK,
		Detail:      cancelled.Title,
	})
	writeJSON(w, http.StatusOK, cancelled)
}

/*
RunJob performs the work when its moment arrives. This is the Runner the
scheduler calls, and the whole of what "inherited approval" means.

Nothing re-asks a human: there is none. What is asked again is everything that
can be taken away. The account is resolved afresh, so a disabled or deleted one
stops here. Its permissions come from the role it holds now, so a demotion
stops here. mayUse then applies the same three gates a button press does —
role, writes-enabled, tool-approved — so an administrator who withdraws any of
them has stopped the scheduled work too, without having to know it existed.
*/
func (s *Server) RunJob(ctx context.Context, job store.Job) (string, error) {
	if job.CreatedByID == nil {
		return "", errors.New("this job has no owner to run as")
	}
	actor, err := s.DB.ActorByID(ctx, *job.CreatedByID)
	if err != nil {
		return "", fmt.Errorf("the person who scheduled this can no longer do it: %w", err)
	}

	tool, found := s.Reg.Lookup(job.Plugin, job.Tool)
	if !found {
		return "", fmt.Errorf("%s.%s is not registered any more", job.Plugin, job.Tool)
	}
	if !tool.Available {
		return "", fmt.Errorf("%s.%s is not available", job.Plugin, job.Tool)
	}
	if err := s.mayUse(ctx, actor, tool); err != nil {
		var gate *gateError
		if errors.As(err, &gate) {
			return "", errors.New(gate.body["error"])
		}
		return "", err
	}

	if _, err := s.performTool(ctx, actor, tool, job.Args, job.CustomerID, onSchedule); err != nil {
		return "", err
	}
	return "", nil
}

/*
jobTool resolves what will run.

Either by capability, which is how a screen asks for a thing without knowing
which plugin provides it, or by name, which is how an administrator schedules
something specific. Both end at one tool, recorded on the job.
*/
func (s *Server) jobTool(ctx context.Context, capability, pluginName, toolName string) (registry.Tool, error) {
	capability = strings.TrimSpace(capability)
	pluginName, toolName = strings.TrimSpace(pluginName), strings.TrimSpace(toolName)

	if capability != "" {
		cap := plugin.Capability(capability)
		if !cap.Valid() {
			return registry.Tool{}, fmt.Errorf("%q is not a capability Azir knows", capability)
		}
		// Writes only. Scheduling a read would be a way to make a phone system
		// answer questions at three in the morning to nobody.
		tool, err := s.approvedTool(ctx, cap, true)
		if err != nil {
			return registry.Tool{}, fmt.Errorf("nothing approved can do that: %w", err)
		}
		return tool, nil
	}

	if pluginName == "" || toolName == "" {
		return registry.Tool{}, errors.New("say what to run, by capability or by name")
	}
	tool, found := s.Reg.Lookup(pluginName, toolName)
	if !found {
		return registry.Tool{}, errors.New("no such tool in the current registry snapshot")
	}
	if !tool.Mutates {
		return registry.Tool{}, errors.New("that tool only reads; there is nothing to schedule")
	}
	return tool, nil
}

// checkWhen refuses times that cannot mean what they say.
func checkWhen(job store.Job) error {
	if job.Repeats != "" {
		if strings.TrimSpace(job.TimeZone) == "" {
			return errors.New("repeating work needs a time zone, or it happens on the server's clock")
		}
		if _, err := time.LoadLocation(job.TimeZone); err != nil {
			return fmt.Errorf("%q is not a time zone", job.TimeZone)
		}
		return nil
	}
	if job.RunAt.IsZero() {
		return errors.New("say when this should happen")
	}
	// A minute of slack, because a browser's clock and this one are never
	// quite the same and "now" is a reasonable thing to ask for.
	if time.Until(job.RunAt) < -time.Minute {
		return errors.New("that time has already passed")
	}
	return nil
}

// whenText says when, for the activity log.
func whenText(job store.Job) string {
	if job.Repeats != "" {
		return job.Repeats + " (" + job.TimeZone + ")"
	}
	return job.RunAt.Format(time.RFC3339)
}
