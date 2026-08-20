package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/dreulavelle/azir/internal/audit"
	"github.com/dreulavelle/azir/internal/identity"
	"github.com/dreulavelle/azir/internal/registry"
	"github.com/dreulavelle/azir/internal/store"
	"github.com/dreulavelle/azir/pkg/plugin"
	"github.com/google/uuid"
)

// Applying what the assistant proposed.
//
// This is the half of the design that carries the weight. The assistant may
// describe any change in full detail and cannot make one; a change happens here,
// when a named person asks for it, and every guard that protects a human doing
// it by hand applies to them rather than to the model:
//
//   - the plugin's writes-enabled switch
//   - the approver's own permission for that tool
//   - the ordinary invoke path, so there is one implementation of the gate
//   - an audit entry naming who approved it, separately from who was talking
//     to the assistant when it was suggested
//
// The model's involvement ends at the proposal. Nothing it said is re-read
// here: the arguments were fixed when the row was written, so a conversation
// that continues afterwards cannot change what Apply does.

func (s *Server) listProposals(w http.ResponseWriter, r *http.Request, actor identity.Actor) {
	c, ok := s.ownedConversation(w, r, actor)
	if !ok {
		return
	}
	proposals, err := s.DB.Proposals(r.Context(), c.ID)
	if err != nil {
		s.fail(w, err, "could not read what was proposed")
		return
	}
	writeJSON(w, http.StatusOK, proposals)
}

func (s *Server) decideProposal(w http.ResponseWriter, r *http.Request, actor identity.Actor) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errBody("that is not a change id"))
		return
	}

	var body struct {
		Apply bool `json:"apply"`
	}
	if err := decode(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, errBody("invalid json body"))
		return
	}

	proposal, err := s.DB.Proposal(r.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeJSON(w, http.StatusNotFound, errBody("no such change"))
			return
		}
		s.fail(w, err, "could not read that change")
		return
	}

	// The conversation it came from belongs to somebody. Checked so a change
	// cannot be applied by someone who was never shown it.
	if _, err := s.DB.Conversation(r.Context(), proposal.ConversationID, actor.UserID); err != nil {
		writeJSON(w, http.StatusNotFound, errBody("no such change"))
		return
	}

	if proposal.Status != store.ProposalPending {
		writeJSON(w, http.StatusConflict, map[string]string{
			"error":  "that change has already been decided",
			"status": proposal.Status,
		})
		return
	}

	if !body.Apply {
		if err := s.DB.DecideProposal(r.Context(), id, store.ProposalDiscarded, "", actor.Email); err != nil {
			s.fail(w, err, "could not discard that change")
			return
		}
		s.Audit.Record(r.Context(), audit.Event{
			ActorUserID: actor.Email, Action: "assistant.discarded",
			Plugin: proposal.Plugin, Tool: proposal.Tool, Outcome: audit.OutcomeOK,
		})
		writeJSON(w, http.StatusOK, map[string]string{"status": store.ProposalDiscarded})
		return
	}

	tool, found := s.Reg.Lookup(proposal.Plugin, proposal.Tool)
	if !found || !tool.Available {
		_ = s.DB.DecideProposal(r.Context(), id, store.ProposalFailed,
			"that connection is no longer available", actor.Email)
		writeJSON(w, http.StatusConflict, errBody("that connection is no longer available"))
		return
	}

	// Whether this person may do it at all is settled before the row is
	// claimed. Checking after would leave a refused change recorded as applied
	// and unretryable — which is exactly what it did until a test pressed
	// Apply with writes switched off and then switched them on.
	if err := s.mayUse(r.Context(), actor, tool); err != nil {
		var gate *gateError
		if errors.As(err, &gate) {
			writeJSON(w, gate.status, gate.body)
			return
		}
		s.fail(w, err, "could not check whether that is allowed")
		return
	}

	// Claimed before it runs, so two people pressing Apply produce one change.
	// The database decides the race; whoever loses is told plainly.
	if err := s.DB.DecideProposal(r.Context(), id, store.ProposalApplied, "", actor.Email); err != nil {
		if errors.Is(err, store.ErrAlreadyDecided) {
			writeJSON(w, http.StatusConflict, errBody("somebody else has already decided that one"))
			return
		}
		s.fail(w, err, "could not apply that change")
		return
	}

	// Performed through the ordinary path, so the write gate, the approver's
	// permission and the audit trail are the same code that runs when somebody
	// presses a button on a screen. There is no second way in.
	result, failure := s.performTool(r.Context(), actor, tool, proposal.Args, proposal.CustomerID, fromAssistant)
	if failure != nil {
		// Put it back to failed, not left as applied. A row that says a change
		// happened when it did not is worse than no row at all.
		if err := s.DB.FailProposal(r.Context(), id, failure.Error(), actor.Email); err != nil {
			s.Log.Warn("could not record a failed change", "id", id, "error", err)
		}
		// Recorded as failed rather than left applied: a change that did not
		// happen must not read as one that did.
		writeJSON(w, http.StatusBadGateway, map[string]string{
			"error":  failure.Error(),
			"status": store.ProposalFailed,
		})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"status": store.ProposalApplied,
		"result": result,
	})
}

// performTool applies one change, having checked that this person may.
//
// Deliberately not the cached path: a change is not a read, and serving one
// from cache or writing its result into the cache would both be wrong. It
// invalidates instead, because the thing it just altered is now stale
// everywhere.
/*
why says where a call came from.

The activity log's whole job is answering who wanted something, and "a person
pressed a button" and "a person approved what a model suggested" are different
answers to that. Passed in rather than guessed, because the one place that
could guess is the one place that cannot see the difference.
*/
type why string

const (
	byHand        why = "a person asked for it"
	fromSheet     why = "from a sheet of changes"
	fromAssistant why = "approved from the assistant"

	/*
		onSchedule is work a person armed earlier.

		Its own reason because the activity log's question is who wanted this,
		and "somebody scheduled it" is a true and different answer from
		"somebody pressed a button". The change is still recorded against the
		person who scheduled it — they are who decided it should happen — but
		reading the log a week later, nobody should have to wonder why a
		technician appeared to be editing a phone system at four in the
		morning.
	*/
	onSchedule why = "scheduled earlier by a person"
)

func (s *Server) performTool(
	ctx context.Context,
	actor identity.Actor,
	tool registry.Tool,
	args json.RawMessage,
	customer *uuid.UUID,
	reason why,
) (json.RawMessage, error) {
	if err := s.mayUse(ctx, actor, tool); err != nil {
		return nil, err
	}

	answer, err := s.askPlugin(ctx, actor, tool, args, customer)
	if err != nil {
		return nil, err
	}

	// Recorded against the person who approved it, which is the answer to
	// "who changed this" — never the assistant that suggested it.
	//
	// A read taken this way is recorded as a read. This used to say
	// "tool.write, approved from the assistant" for every call through here,
	// whatever the tool did and whoever asked — so the activity log reported a
	// change every time somebody opened a screen that reads a phone system.
	// The log exists to answer "did anybody change anything", and a yes when
	// nothing happened is worse than no log at all.
	action := "tool.invoke"
	if tool.Mutates {
		action = "tool.write"
	}
	s.Audit.Record(ctx, audit.Event{
		ActorUserID: actor.Email, Action: action,
		Plugin: tool.Plugin, Tool: tool.Name,
		Outcome: audit.OutcomeOK, Detail: string(reason),
	})

	// What was just changed is stale wherever it was cached, and every open
	// browser should be told the same way a webhook would tell them.
	//
	// The whole plugin, not the tool that did the writing. A write tool has no
	// cache of its own — what has gone stale is the read that would now answer
	// differently, which is a different tool. Invalidating only the writer left
	// the extension list showing what it said before the change.
	if s.Cache != nil && tool.Mutates {
		if _, err := s.DB.InvalidateCache(ctx, tool.Plugin, "", nil); err != nil {
			s.Log.Warn("could not invalidate after a change", "error", err)
		}
	}
	s.announce("ticket")

	return answer, nil
}

/*
askPlugin makes the request and reads the answer. No gate, no audit, no cache —
those belong to whoever is asking and differ between a read and a change.
*/
func (s *Server) askPlugin(
	ctx context.Context,
	actor identity.Actor,
	tool registry.Tool,
	args json.RawMessage,
	customer *uuid.UUID,
) (json.RawMessage, error) {
	payload, err := json.Marshal(plugin.Request{
		CustomerID: customerRef(derefCustomer(customer)),
		Actor:      plugin.Actor{UserID: actor.Email, Role: actor.Role},
		Args:       args,
	})
	if err != nil {
		return nil, errors.New("that request could not be encoded")
	}

	callCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	msg, err := s.NC.RequestWithContext(callCtx, tool.Subject, payload)
	if err != nil {
		s.recordInvoke(ctx, actor, tool.Plugin, tool.Name, "", audit.OutcomeFailed, "approved")
		return nil, errors.New("the connected system did not respond")
	}
	if code := msg.Header.Get("Nats-Service-Error-Code"); code != "" {
		reason := msg.Header.Get("Nats-Service-Error")
		s.recordInvoke(ctx, actor, tool.Plugin, tool.Name, "", audit.OutcomeFailed, reason)
		return nil, errors.New(reason)
	}
	return msg.Data, nil
}

/*
readTool answers a read, from the cache when the tool declares a freshness
budget.

Screens that read a phone system used to go through performTool, which calls
out every single time. Reading every extension's settings off a thousand-
extension system is twenty sequential requests to somebody's PBX, and it was
happening on every navigation — the tool declares two minutes soft and thirty
hard precisely so that it does not have to.

A change invalidates the whole plugin, so what comes back after an edit is
what the edit made.
*/
func (s *Server) readTool(
	ctx context.Context,
	actor identity.Actor,
	tool registry.Tool,
	args json.RawMessage,
	customer *uuid.UUID,
) (json.RawMessage, error) {
	if err := s.mayUse(ctx, actor, tool); err != nil {
		return nil, err
	}
	if s.Cache == nil {
		return s.askPlugin(ctx, actor, tool, args, customer)
	}

	result, err := s.Cache.Do(ctx, tool, customer, args, false,
		func(ctx context.Context) (json.RawMessage, error) {
			return s.askPlugin(ctx, actor, tool, args, customer)
		})
	if err != nil {
		return nil, err
	}
	s.recordInvoke(ctx, actor, tool.Plugin, tool.Name, "", audit.OutcomeOK, result.Source)
	return result.Payload, nil
}

func derefCustomer(id *uuid.UUID) uuid.UUID {
	if id == nil {
		return uuid.Nil
	}
	return *id
}
