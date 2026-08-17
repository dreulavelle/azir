package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/dreulavelle/azir/internal/assistant"
	"github.com/dreulavelle/azir/internal/audit"
	"github.com/dreulavelle/azir/internal/identity"
	"github.com/dreulavelle/azir/internal/registry"
	"github.com/dreulavelle/azir/internal/store"
	"github.com/dreulavelle/azir/pkg/plugin"
)

// assistantTools returns what the model may look up.
//
// The filter here is the containment argument, so it is written out rather than
// folded into a query: a tool reaches the model only if it reads, is approved,
// is currently working, and provides a capability. Anything that writes is
// excluded before anything else is considered, and there is no flag anywhere
// that turns that off.
// assistantTools is what the model may reach for in this conversation.
//
// onBehalf is the customer being discussed, and may be uuid.Nil — either a
// conversation about nothing in particular, or the settings screen asking what
// the assistant can do at all rather than what it can do here.
func (s *Server) assistantTools(ctx context.Context, onBehalf uuid.UUID) ([]assistant.Tool, error) {
	approved, err := s.DB.ApprovedTools(ctx)
	if err != nil {
		return nil, err
	}

	// Keyed by capability: the model asks for a kind of information, not for a
	// named plugin, so two systems providing the same thing appear once.
	seen := map[string]assistant.Tool{}

	for _, p := range s.Reg.Snapshot().Plugins {
		for _, t := range p.Tools {
			if !t.Available {
				continue
			}
			if _, ok := approved[p.Name+"."+t.Name]; !ok {
				continue
			}
			for _, c := range t.Provides {
				capability := string(c)
				if capability == string(plugin.CapDiagnostic) {
					continue // proves the plumbing works; tells nobody anything
				}

				// A write is offered under a name that says what calling it
				// does. "propose.work_items.comment" cannot be mistaken for
				// doing something, by a reader or by the model — and it keeps
				// reads and writes from colliding on one capability, since a
				// plugin usually provides both against the same tag.
				description := t.Description
				if t.Mutates {
					capability = proposePrefix + capability
					description = "PROPOSES a change — it does not make one. " + t.Description +
						" Calling this writes the change down and shows it to the technician " +
						"with an Apply button. Nothing happens until they press it, so say " +
						"what you have proposed and that they need to approve it. Never say it is done."
				}

				if _, taken := seen[capability]; taken {
					continue
				}
				seen[capability] = assistant.Tool{
					Name:        capability,
					Description: description,
					Schema:      t.Schema,
				}
			}
		}
	}

	out := make([]assistant.Tool, 0, len(seen)+1)
	for _, t := range seen {
		out = append(out, t)
	}

	/*
		Recall is Azir's own, so it comes from here rather than from the
		registry.

		Every other tool is a way of reaching somebody else's system, which is
		why they arrive from plugins and need approving one at a time. This one
		reaches an index Azir built out of tickets an administrator has already
		let it read. Wrapping it in a plugin to preserve the symmetry would be
		ceremony: a process, a subject, a schema and an approval, all to search
		a table in the database core is already holding open.

		It is offered only once something has been indexed. A tool that always
		answers "nothing found" teaches a model to stop calling it, and it would
		have learnt that before the first backfill ever ran.
	*/
	if total, _, err := s.DB.RecallSize(ctx); err == nil && total > 0 {
		out = append(out, assistant.Tool{
			Name: "recall.similar_tickets",
			Description: "Searches finished tickets for work like this — what the problem looked like " +
				"and what was actually done about it. Reach for it before working out an answer from " +
				"first principles: this company has probably met the problem before, and what fixed it " +
				"on their equipment is better than what fixes it in general. Search the way you would " +
				"describe the fault, or paste an error code or a model number.",
			Schema: json.RawMessage(`{
				"type": "object",
				"required": ["query"],
				"properties": {
					"query": {"type": "string", "description": "What the problem looks like — symptoms, an error, a product."},
					"this_customer_only": {"type": "boolean", "description": "Restrict to the customer this conversation is about."}
				}
			}`),
		})
	}

	/*
		Support captures, likewise Azir's own.

		Offered only where one exists, and for the same reason as recall: a tool
		that is always empty teaches a model not to bother. What it returns is
		the findings — sentences about what is wrong — rather than the metrics
		they were read from, because a model handed five thousand readings will
		describe the readings, and what a technician needs is the sentence.
	*/
	if onBehalf != uuid.Nil && s.hasCaptures(ctx, onBehalf) {
		out = append(out, assistant.Tool{
			Name: "diagnostics.read_capture",
			Description: "Reads the 3CX support capture taken from this customer's phone system — " +
				"version, host, extension counts, and what was found wrong in the logs: dead audio, " +
				"failed logins, licence and certificate trouble. Reach for it whenever the problem " +
				"is with their phones and you would otherwise be guessing. Defaults to the most " +
				"recent capture, which is nearly always the one meant.",
			Schema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"snapshot_id": {"type": "string", "description": "A specific capture. Omit for the latest."}
				}
			}`),
		})
	}

	return out, nil
}

// runner performs one lookup for the assistant, through core's ordinary path.
//
// Same approval gate, same availability check, same audit entry as a person
// clicking the same thing — the actor recorded is the technician, because the
// assistant is acting on their behalf and their permissions are the ones that
// apply.
// stage turns an attempted change into a proposal.
//
// Every check that guards a person making this change is deliberately *not*
// applied here — not writes-enabled, not the actor's permission. Those belong
// at the moment of applying, against the person who approves it, because they
// are the one making the change. Refusing to even draft one because the
// current technician lacks the permission would be a worse product: the shift
// lead who does have it never gets to see what was suggested.
func (s *Server) stage(
	ctx context.Context,
	actor identity.Actor,
	onBehalf uuid.UUID,
	conversation uuid.UUID,
	tool registry.Tool,
	args json.RawMessage,
) (json.RawMessage, error) {
	if conversation == uuid.Nil {
		return nil, errors.New("changes can only be proposed inside a conversation")
	}

	var customer *uuid.UUID
	if onBehalf != uuid.Nil {
		customer = &onBehalf
	}

	proposal, err := s.DB.Propose(ctx, store.Proposal{
		ConversationID: conversation,
		ProposedFor:    actor.Email,
		Plugin:         tool.Plugin,
		Tool:           tool.Name,
		Args:           args,
		CustomerID:     customer,
		Summary:        actionTitle(tool.Name),
	})
	if err != nil {
		return nil, errors.New("that change could not be written down")
	}

	s.Audit.Record(ctx, audit.Event{
		ActorUserID: actor.Email, Action: "assistant.proposed",
		Plugin: tool.Plugin, Tool: tool.Name,
		Outcome: audit.OutcomeOK, Detail: proposal.ID.String(),
	})

	// Written for the model. It has to understand that nothing has happened
	// yet, or it will tell the technician the job is done.
	return json.Marshal(map[string]any{
		"proposed": true,
		"id":       proposal.ID,
		"status":   "waiting for a person to approve it",
		"note": "This change has NOT been made. It is now shown to the technician " +
			"with an Apply button. Tell them what you have proposed and why, and that " +
			"they need to approve it. Do not claim it is done.",
	})
}

// actionTitle turns a dotted tool name into something a person reads.
func actionTitle(name string) string {
	readable := strings.ReplaceAll(strings.ReplaceAll(name, ".", " "), "_", " ")
	if readable == "" {
		return readable
	}
	return strings.ToUpper(readable[:1]) + readable[1:]
}

// onBehalfOf works out which customer the assistant is acting for.
//
// Capabilities split into two kinds and the assistant has to satisfy both. Most
// are one per deployment — there is one Syncro. A phone system is not: every
// customer has their own, and a lookup that does not say whose gets refused,
// correctly, because answering with somebody else's would be worse.
//
// The assistant cannot supply that itself. It knows it is looking at a ticket;
// it does not know Azir's internal id for the business that ticket belongs to,
// and it should not — that is plumbing, and telling the model about it would be
// giving it a handle it could be talked into pointing somewhere else. So it is
// resolved here from the conversation's subject and attached to every request.
//
// Returning nothing is a normal outcome: a customer nobody has linked yet, or a
// chat about no one in particular. Deployment-wide lookups carry on working;
// per-customer ones refuse and say why.
func (s *Server) onBehalfOf(ctx context.Context, c store.Conversation) (uuid.UUID, string) {
	var external string
	switch c.SubjectKind {
	case "customer":
		external = c.SubjectID
	case "ticket":
		// The ticket knows its customer. Read through the ordinary path so the
		// same approvals and cache apply as anywhere else.
		raw, err := s.readCapability(ctx, "work_items.get",
			json.RawMessage(fmt.Sprintf(`{"id":%s}`, c.SubjectID)))
		if err != nil {
			return uuid.Nil, ""
		}
		var ticket struct {
			CustomerID int64  `json:"customer_id"`
			Customer   string `json:"customer"`
		}
		if err := json.Unmarshal(raw, &ticket); err != nil || ticket.CustomerID == 0 {
			return uuid.Nil, ""
		}
		external = strconv.FormatInt(ticket.CustomerID, 10)
	default:
		return uuid.Nil, ""
	}
	if external == "" {
		return uuid.Nil, ""
	}

	// The identity is recorded against "psa" by the console when somebody links
	// a customer; older links may name the plugin instead.
	for _, under := range []string{"psa", "syncro"} {
		if found, err := s.DB.ResolveIdentity(ctx, under, external); err == nil {
			return found.ID, found.DisplayName
		}
	}
	return uuid.Nil, ""
}

func (s *Server) runner(actor identity.Actor, onBehalf uuid.UUID, conversation uuid.UUID) assistant.Runner {
	return func(ctx context.Context, capability string, args json.RawMessage) (json.RawMessage, error) {
		// Azir's own index, answered here rather than over NATS.
		if capability == "recall.similar_tickets" {
			return s.recallForModel(ctx, onBehalf, args)
		}
		if capability == "diagnostics.read_capture" {
			return s.snapshotForModel(ctx, onBehalf, args)
		}

		tool, err := s.resolveCapability(ctx, capability)
		if err != nil {
			return nil, err
		}

		// A write is staged, never performed. This is the line the whole
		// design rests on: the model may describe a change in full detail and
		// cannot cause one. What it gets back says so, so it reports the
		// change as proposed rather than telling a technician it is done.
		//
		// Ticket text is written by customers, so anything the model was
		// persuaded to attempt ends up here — as a row a person reads and
		// rejects, not as a request to somebody's phone system.
		if tool.Mutates {
			return s.stage(ctx, actor, onBehalf, conversation, tool, args)
		}

		payload, err := json.Marshal(plugin.Request{
			Actor: plugin.Actor{UserID: actor.Email, Role: actor.Role},
			Args:  args,
			// Attached by Azir, never chosen by the model. A capability that
			// reaches one customer's own system gets the customer this
			// conversation is about and no other.
			CustomerID: customerRef(onBehalf),
		})
		if err != nil {
			return nil, err
		}

		callCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()

		msg, err := s.NC.RequestWithContext(callCtx, tool.Subject, payload)
		if err != nil {
			s.recordInvoke(ctx, actor, tool.Plugin, tool.Name, "", audit.OutcomeFailed, "assistant")
			return nil, fmt.Errorf("%s did not respond", capability)
		}
		if code := msg.Header.Get("Nats-Service-Error-Code"); code != "" {
			s.recordInvoke(ctx, actor, tool.Plugin, tool.Name, "", audit.OutcomeFailed, "assistant")
			return nil, errors.New(msg.Header.Get("Nats-Service-Error"))
		}

		s.recordInvoke(ctx, actor, tool.Plugin, tool.Name, "", audit.OutcomeOK, "assistant")
		return msg.Data, nil
	}
}

// resolveCapability finds the approved, working tool behind a capability.
// readCapability performs one read through the ordinary path, for Azir's own
// use rather than the model's.
func (s *Server) readCapability(ctx context.Context, capability string, args json.RawMessage) (json.RawMessage, error) {
	tool, err := s.resolveCapability(ctx, capability)
	if err != nil {
		return nil, err
	}
	payload, err := json.Marshal(plugin.Request{Args: args})
	if err != nil {
		return nil, err
	}
	callCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	msg, err := s.NC.RequestWithContext(callCtx, tool.Subject, payload)
	if err != nil {
		return nil, err
	}
	if code := msg.Header.Get("Nats-Service-Error-Code"); code != "" {
		return nil, fmt.Errorf("%s: %s", code, msg.Header.Get("Nats-Service-Error"))
	}
	return msg.Data, nil
}

// customerNote tells the model whose systems it can reach.
//
// Said explicitly because the failure it prevents is a confusing one: without
// it, a lookup against a customer's own phone system refuses, the model does
// not know why, and it either tries again or tells the technician something
// vague. Naming the gap turns that into an answer somebody can act on.
func customerNote(onBehalf uuid.UUID, name string) string {
	if onBehalf == uuid.Nil {
		return "\nThis customer does not have a record in Azir yet, so anything held per " +
			"customer — their phone system, for instance — cannot be reached. Say so " +
			"plainly if you are asked about one, and suggest linking them on their " +
			"customer page.\n"
	}
	return fmt.Sprintf("\nYou are working on behalf of %s. Lookups that reach a customer's "+
		"own systems will reach theirs.\n", name)
}

// customerRef is the wire form of a customer handle: empty when there is none.
func customerRef(id uuid.UUID) string {
	if id == uuid.Nil {
		return ""
	}
	return id.String()
}

// proposePrefix marks a tool the model may only stage, never perform.
const proposePrefix = "propose."

func (s *Server) resolveCapability(ctx context.Context, capability string) (registry.Tool, error) {
	// A proposal resolves to the mutating provider of the underlying
	// capability; an ordinary call resolves to a reading one. Keeping them
	// apart here means a model asking to read can never be routed to a tool
	// that writes, whatever a plugin declares.
	wantWrite := strings.HasPrefix(capability, proposePrefix)
	capability = strings.TrimPrefix(capability, proposePrefix)

	// Two indexes, so a request to read can never resolve to a tool that
	// writes. Only the propose path looks in the write one.
	providers := s.Reg.Providers(plugin.Capability(capability))
	if wantWrite {
		providers = s.Reg.WriteProviders(plugin.Capability(capability))
	}
	if len(providers) == 0 {
		if wantWrite {
			return registry.Tool{}, fmt.Errorf("nothing here can change %s", capability)
		}
		return registry.Tool{}, fmt.Errorf("nothing here can look up %s", capability)
	}
	approved, err := s.DB.ApprovedTools(ctx)
	if err != nil {
		return registry.Tool{}, err
	}
	for _, qualified := range providers {
		if _, ok := approved[qualified]; !ok {
			continue
		}
		pluginName, toolName, ok := strings.Cut(qualified, ".")
		if !ok {
			continue
		}
		tool, found := s.Reg.Lookup(pluginName, toolName)
		if !found || !tool.Available {
			continue
		}
		// The read path must never land on a tool that writes, and the propose
		// path must never land on one that does not — a proposal that quietly
		// resolved to a read would report success having done nothing.
		if tool.Mutates != wantWrite {
			continue
		}
		return tool, nil
	}
	if wantWrite {
		return registry.Tool{}, fmt.Errorf("nothing here can change %s", capability)
	}
	return registry.Tool{}, fmt.Errorf("%s is not switched on for this deployment", capability)
}

// engine builds the assistant from stored settings, resolving the API key at
// the moment it is needed rather than holding it in memory.
func (s *Server) engine(ctx context.Context, actor identity.Actor, onBehalf, conversation uuid.UUID) (*assistant.Engine, store.AssistantConfig, error) {
	cfg, err := s.DB.AssistantConfig(ctx)
	if err != nil {
		return nil, cfg, err
	}
	if !cfg.Enabled {
		return nil, cfg, errors.New("the assistant is not switched on yet")
	}

	key, err := s.Creds.Open(ctx, nil, store.AssistantPlugin, store.AssistantSecretKind)
	if err != nil {
		return nil, cfg, errors.New("no API key is stored for the model provider")
	}

	return &assistant.Engine{
		Provider: providerFor(cfg, string(key)),
		Run:      s.runner(actor, onBehalf, conversation),
	}, cfg, nil
}

// providerFor picks how to talk to the configured service.
//
// Anthropic has its own message shape; everything else in practice speaks
// chat/completions — including gateways that sit in front of several vendors,
// which is how a deployment keeps one place to see cost and choose a model.
func providerFor(cfg store.AssistantConfig, key string) assistant.Provider {
	if cfg.Provider == "anthropic" {
		return &assistant.Anthropic{APIKey: key, BaseURL: cfg.BaseURL}
	}
	return &assistant.OpenAICompatible{APIKey: key, BaseURL: cfg.BaseURL, Label: cfg.Provider}
}

// assistantStatus says whether the assistant can answer at all.
//
// Readable by anyone signed in, unlike the settings it summarises: a technician
// needs to know whether to bother asking, and refusing them that turns a clear
// "not set up yet" into a failed question.
func (s *Server) assistantStatus(w http.ResponseWriter, r *http.Request) {
	cfg, err := s.DB.AssistantConfig(r.Context())
	if err != nil {
		s.fail(w, err, "could not check the assistant")
		return
	}

	ready := cfg.Enabled
	if ready {
		// Configured but keyless is the same as off from here: a button that
		// cannot work is worse than one that is honestly absent.
		if _, err := s.Creds.Open(r.Context(), nil, store.AssistantPlugin, store.AssistantSecretKind); err != nil {
			ready = false
		}
	}
	// What this person may choose is part of "can I use this", so it comes
	// back with it: the panel would otherwise need a second request before it
	// could draw its own header.
	choice := cfg.ModelChoice
	if choice == "" {
		choice = store.ModelFixed
	}
	allowed := cfg.AllowedModels
	if allowed == nil {
		allowed = []string{}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"ready": ready,
		// The model in use when nobody picks one. Named rather than hidden so
		// a technician can tell what answered them.
		"default_model":  cfg.Model,
		"model_choice":   choice,
		"allowed_models": allowed,
		"may_choose":     choice != store.ModelFixed,
	})
}

// testAssistant checks the settings actually work, and says what is wrong when
// they do not.
//
// Worth its own endpoint because the alternative is discovering a typo when
// somebody asks a real question and gets a failure they cannot act on.
func (s *Server) testAssistant(w http.ResponseWriter, r *http.Request, actor identity.Actor) {
	engine, cfg, err := s.engine(r.Context(), actor, uuid.Nil, uuid.Nil)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "problem": err.Error()})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()

	// Deliberately tiny, and with no tools: this is checking the key, the
	// address and the model name, not the assistant's judgement.
	_, err = engine.Provider.Complete(ctx, assistant.Request{
		System:          "Reply with the single word: ready.",
		Turns:           []assistant.Turn{{Role: "user", Content: "ready?"}},
		Model:           cfg.Model,
		MaxAnswerTokens: 16,
	})
	if err == nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
		return
	}

	out := map[string]any{"ok": false, "problem": err.Error()}

	// A rejected model name is answerable: ask the service what it does run.
	if lister, canList := engine.Provider.(interface {
		Models(context.Context) ([]string, error)
	}); canList && strings.Contains(err.Error(), "model name") {
		if models, listErr := lister.Models(ctx); listErr == nil {
			out["available_models"] = models
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// listAssistantModels asks the configured service what it can actually run.
//
// The model name is the most commonly wrong setting on this page, because a
// gateway renames whatever it fronts — the name in a vendor's own documentation
// is rarely the name to type here. Asking the service turns a guess into a
// choice, and turns curating an allowed list from typing strings into ticking
// boxes.
//
// A provider that cannot be asked is not an error worth failing on: Anthropic's
// own API has no such endpoint, and the page still works by typing a name.
func (s *Server) listAssistantModels(w http.ResponseWriter, r *http.Request, _ identity.Actor) {
	engine, _, err := s.engine(r.Context(), identity.Actor{}, uuid.Nil, uuid.Nil)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"models": []string{},
			"reason": "Add an API key and save before the list can be fetched.",
		})
		return
	}

	lister, canList := engine.Provider.(interface {
		Models(context.Context) ([]string, error)
	})
	if !canList {
		writeJSON(w, http.StatusOK, map[string]any{
			"models": []string{},
			"reason": "This service does not publish a model list. Type the name instead.",
		})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()

	models, err := lister.Models(ctx)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"models": []string{},
			"reason": err.Error(),
		})
		return
	}
	slices.Sort(models)
	writeJSON(w, http.StatusOK, map[string]any{"models": models})
}

// --- conversations -----------------------------------------------------------

func (s *Server) listConversations(w http.ResponseWriter, r *http.Request, actor identity.Actor) {
	q := r.URL.Query()
	list, err := s.DB.Conversations(r.Context(), actor.UserID,
		q.Get("subject_kind"), q.Get("subject_id"), 50)
	if err != nil {
		s.fail(w, err, "could not load your chats")
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) createConversation(w http.ResponseWriter, r *http.Request, actor identity.Actor) {
	var body struct {
		Title       string `json:"title"`
		SubjectKind string `json:"subject_kind"`
		SubjectID   string `json:"subject_id"`
		Model       string `json:"model"`
	}
	if err := decode(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, errBody("invalid json body"))
		return
	}

	// A model a technician is not allowed to use is refused here rather than
	// quietly swapped for the default: silently answering with something other
	// than what was asked for is how somebody ends up mistrusting the answer.
	cfg, err := s.DB.AssistantConfig(r.Context())
	if err != nil {
		s.fail(w, err, "could not read the assistant settings")
		return
	}
	if !cfg.MayUse(body.Model) {
		writeJSON(w, http.StatusForbidden, errBody("that model is not available to you"))
		return
	}

	c, err := s.DB.CreateConversation(r.Context(), actor.UserID, body.Title, body.SubjectKind, body.SubjectID, body.Model)
	if err != nil {
		s.fail(w, err, "could not start a chat")
		return
	}
	writeJSON(w, http.StatusCreated, c)
}

func (s *Server) getConversation(w http.ResponseWriter, r *http.Request, actor identity.Actor) {
	c, ok := s.ownedConversation(w, r, actor)
	if !ok {
		return
	}
	messages, err := s.DB.Messages(r.Context(), c.ID)
	if err != nil {
		s.fail(w, err, "could not load this chat")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"conversation": c, "messages": messages})
}

func (s *Server) deleteConversation(w http.ResponseWriter, r *http.Request, actor identity.Actor) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errBody("not a valid chat id"))
		return
	}
	if err := s.DB.DeleteConversation(r.Context(), id, actor.UserID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeJSON(w, http.StatusNotFound, errBody("no such chat"))
			return
		}
		s.fail(w, err, "could not delete this chat")
		return
	}
	writeJSON(w, http.StatusNoContent, nil)
}

func (s *Server) ownedConversation(w http.ResponseWriter, r *http.Request, actor identity.Actor) (store.Conversation, bool) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errBody("not a valid chat id"))
		return store.Conversation{}, false
	}
	c, err := s.DB.Conversation(r.Context(), id, actor.UserID)
	switch {
	case errors.Is(err, store.ErrNotFound), errors.Is(err, store.ErrNotOwned):
		// Deliberately the same answer for both: whether somebody else's chat
		// exists is not something to confirm.
		writeJSON(w, http.StatusNotFound, errBody("no such chat"))
		return store.Conversation{}, false
	case err != nil:
		s.fail(w, err, "could not load this chat")
		return store.Conversation{}, false
	}
	return c, true
}

// sendMessage asks the assistant something and returns its answer.
func (s *Server) sendMessage(w http.ResponseWriter, r *http.Request, actor identity.Actor) {
	c, ok := s.ownedConversation(w, r, actor)
	if !ok {
		return
	}

	var body struct {
		Message string `json:"message"`
	}
	if err := decode(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, errBody("invalid json body"))
		return
	}
	body.Message = strings.TrimSpace(body.Message)
	if body.Message == "" {
		writeJSON(w, http.StatusBadRequest, errBody("there is no question here"))
		return
	}

	// Which business this conversation is about, so a capability that reaches
	// one customer's own system reaches the right one.
	onBehalf, onBehalfName := s.onBehalfOf(r.Context(), c)

	engine, cfg, err := s.engine(r.Context(), actor, onBehalf, c.ID)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errBody(err.Error()))
		return
	}

	tools, err := s.assistantTools(r.Context(), onBehalf)
	if err != nil {
		s.fail(w, err, "could not work out what the assistant can look up")
		return
	}

	history, err := s.DB.Messages(r.Context(), c.ID)
	if err != nil {
		s.fail(w, err, "could not load this chat")
		return
	}

	// The question is stored before the model is asked, so a failure leaves a
	// conversation someone can retry rather than one that lost what they typed.
	asked, err := s.DB.AddMessage(r.Context(), c.ID, store.Message{Role: "user", Content: body.Message})
	if err != nil {
		s.fail(w, err, "could not save your message")
		return
	}
	if c.Title == "" {
		if err := s.DB.SetConversationTitle(r.Context(), c.ID, summarise(body.Message)); err != nil {
			s.Log.Warn("could not title a chat", "error", err)
		}
	}

	turns := make([]assistant.Turn, 0, len(history)+1)
	for _, m := range history {
		if m.Role == "user" || m.Role == "assistant" {
			turns = append(turns, assistant.Turn{Role: m.Role, Content: m.Content})
		}
	}
	turns = append(turns, assistant.Turn{Role: "user", Content: body.Message})

	// Only the most recent exchanges: a long conversation costs more and gets
	// worse, because the model loses the thread rather than gaining context.
	if max := cfg.MaxTurns; max > 0 && len(turns) > max {
		turns = turns[len(turns)-max:]
	}

	s.Audit.Record(r.Context(), audit.Event{
		ActorUserID: actor.Email, Action: "assistant.ask", Outcome: audit.OutcomeOK,
	})

	answer, steps, err := engine.Answer(r.Context(), assistant.Request{
		System: prompt(cfg, actor.DisplayName, describeSubject(c)) + customerNote(onBehalf, onBehalfName),
		Turns:  turns,
		Tools:  tools,
		Model:  modelFor(cfg, c),

		MaxAnswerTokens: cfg.MaxAnswer,
	})
	if err != nil {
		s.Log.Warn("the assistant could not answer", "error", err)
		s.Audit.Record(r.Context(), audit.Event{
			ActorUserID: actor.Email, Action: "assistant.ask",
			Outcome: audit.OutcomeFailed, Detail: err.Error(),
		})
		writeJSON(w, http.StatusBadGateway, map[string]any{
			"error": err.Error(),
			"asked": asked,
		})
		return
	}

	reply, err := s.DB.AddMessage(r.Context(), c.ID, store.Message{
		Role: "assistant", Content: answer, Steps: steps,
	})
	if err != nil {
		s.fail(w, err, "could not save the answer")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"asked": asked, "reply": reply})
}

// describeSubject says, in words, what the technician is looking at.
// modelFor decides which model answers this conversation.
//
// A conversation remembers what it was started with, so a chat does not change
// voice halfway through because an administrator edited the default. It is
// re-checked against the policy every time rather than trusted: a model that
// was allowed last week may have been removed from the list since, and the
// stored value must not become a way around that.
func modelFor(cfg store.AssistantConfig, c store.Conversation) string {
	if c.Model != "" && cfg.MayUse(c.Model) {
		return c.Model
	}
	return cfg.Model
}

// prompt is the shipped system prompt plus whatever the MSP added to it.
//
// The order matters and is not configurable. The shipped part states the
// boundary the whole design rests on — no tools that write, ticket text is
// evidence rather than instruction — and it goes first so that an addition
// reads as a house style on top of it rather than as a replacement for it.
func prompt(cfg store.AssistantConfig, actorName, subject string) string {
	base := strings.TrimSpace(cfg.SystemPrompt)
	if base == "" {
		base = assistant.BaseSystemPrompt
	}

	var b strings.Builder
	b.WriteString(base)
	if house := strings.TrimSpace(cfg.HousePrompt); house != "" {
		b.WriteString("\n\nYour organisation has asked you to work this way:\n")
		b.WriteString(house)
		b.WriteString("\n")
	}
	// The facts of this request go last, so nothing an operator wrote can push
	// them out of the model's attention.
	b.WriteString("\n")
	b.WriteString(assistant.Context(time.Now(), actorName, subject))
	return b.String()
}

func describeSubject(c store.Conversation) string {
	switch c.SubjectKind {
	case "ticket":
		return "ticket " + c.SubjectID
	case "customer":
		return "the customer with id " + c.SubjectID
	default:
		return ""
	}
}

// summarise names a chat from its opening line.
func summarise(message string) string {
	line := strings.TrimSpace(strings.SplitN(message, "\n", 2)[0])
	const limit = 60
	if len(line) <= limit {
		return line
	}
	// Cut on a word boundary; a title ending mid-word reads as broken.
	if space := strings.LastIndex(line[:limit], " "); space > 20 {
		return line[:space] + "…"
	}
	return line[:limit] + "…"
}

// sendMessageStreaming is sendMessage with the working-out reported as it
// happens.
//
// Server-sent events rather than a socket: this is one-way, short-lived and
// survives a proxy that knows nothing about it, which a socket does not.
func (s *Server) sendMessageStreaming(w http.ResponseWriter, r *http.Request, actor identity.Actor) {
	c, ok := s.ownedConversation(w, r, actor)
	if !ok {
		return
	}

	var body struct {
		Message string `json:"message"`
	}
	if err := decode(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, errBody("invalid json body"))
		return
	}
	body.Message = strings.TrimSpace(body.Message)
	if body.Message == "" {
		writeJSON(w, http.StatusBadRequest, errBody("there is no question here"))
		return
	}

	flusher, canStream := w.(http.Flusher)
	if !canStream {
		// Nothing downstream can push, so answer the ordinary way rather than
		// buffering an event stream nobody will see until the end.
		s.sendMessage(w, r, actor)
		return
	}

	onBehalf, onBehalfName := s.onBehalfOf(r.Context(), c)

	engine, cfg, err := s.engine(r.Context(), actor, onBehalf, c.ID)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errBody(err.Error()))
		return
	}
	tools, err := s.assistantTools(r.Context(), onBehalf)
	if err != nil {
		s.fail(w, err, "could not work out what the assistant can look up")
		return
	}
	history, err := s.DB.Messages(r.Context(), c.ID)
	if err != nil {
		s.fail(w, err, "could not load this chat")
		return
	}

	asked, err := s.DB.AddMessage(r.Context(), c.ID, store.Message{Role: "user", Content: body.Message})
	if err != nil {
		s.fail(w, err, "could not save your message")
		return
	}
	if c.Title == "" {
		if err := s.DB.SetConversationTitle(r.Context(), c.ID, summarise(body.Message)); err != nil {
			s.Log.Warn("could not title a chat", "error", err)
		}
	}

	// Headers before anything else: once the first byte is written the status
	// code is fixed, so every later failure has to travel as an event.
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	// Proxies that buffer by default would hold the whole stream until the end,
	// which defeats the point.
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	send := func(event string, payload any) {
		raw, err := json.Marshal(payload)
		if err != nil {
			return
		}
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, raw)
		flusher.Flush()
	}

	send("asked", asked)

	turns := make([]assistant.Turn, 0, len(history)+1)
	for _, m := range history {
		if m.Role == "user" || m.Role == "assistant" {
			turns = append(turns, assistant.Turn{Role: m.Role, Content: m.Content})
		}
	}
	turns = append(turns, assistant.Turn{Role: "user", Content: body.Message})
	if max := cfg.MaxTurns; max > 0 && len(turns) > max {
		turns = turns[len(turns)-max:]
	}

	// Guarded because the engine calls this from its own goroutine-free loop
	// but the writer is only safe from one place at a time.
	var mu sync.Mutex
	engine.OnStep = func(step store.Step) {
		mu.Lock()
		defer mu.Unlock()
		send("step", step)
	}
	engine.OnToken = func(fragment string) {
		mu.Lock()
		defer mu.Unlock()
		send("token", map[string]string{"text": fragment})
	}
	engine.OnNarration = func(text string) {
		mu.Lock()
		defer mu.Unlock()
		send("narration", map[string]string{"text": text})
	}

	s.Audit.Record(r.Context(), audit.Event{
		ActorUserID: actor.Email, Action: "assistant.ask", Outcome: audit.OutcomeOK,
	})

	answer, steps, err := engine.Answer(r.Context(), assistant.Request{
		System:          prompt(cfg, actor.DisplayName, describeSubject(c)) + customerNote(onBehalf, onBehalfName),
		Turns:           turns,
		Tools:           tools,
		Model:           modelFor(cfg, c),
		MaxAnswerTokens: cfg.MaxAnswer,
	})
	if err != nil {
		s.Log.Warn("the assistant could not answer", "error", err)
		s.Audit.Record(r.Context(), audit.Event{
			ActorUserID: actor.Email, Action: "assistant.ask",
			Outcome: audit.OutcomeFailed, Detail: err.Error(),
		})
		send("failed", map[string]string{"error": err.Error()})
		return
	}

	reply, err := s.DB.AddMessage(r.Context(), c.ID, store.Message{
		Role: "assistant", Content: answer, Steps: steps,
	})
	if err != nil {
		send("failed", map[string]string{"error": "the answer could not be saved"})
		return
	}
	send("reply", reply)
}

// --- settings ----------------------------------------------------------------

type assistantSettingsView struct {
	store.AssistantConfig
	APIKeySet bool `json:"api_key_set"`
	// What Azir ships, so the page can show it as the starting point and offer
	// a way back to it.
	ShippedPrompt string `json:"shipped_prompt"`
	// The sentence whose removal is worth warning about.
	InjectionDefence string `json:"injection_defence"`
	// What the assistant would currently be able to look up, so an
	// administrator can see the consequence of their approval decisions in one
	// place rather than inferring it.
	Available []string `json:"available_lookups"`
}

func (s *Server) getAssistantSettings(w http.ResponseWriter, r *http.Request) {
	cfg, err := s.DB.AssistantConfig(r.Context())
	if err != nil {
		s.fail(w, err, "could not read the assistant settings")
		return
	}

	view := assistantSettingsView{
		AssistantConfig:  cfg,
		Available:        []string{},
		ShippedPrompt:    strings.TrimSpace(assistant.BaseSystemPrompt),
		InjectionDefence: assistant.InjectionDefence,
	}
	if refs, err := s.Creds.List(r.Context()); err == nil {
		for _, ref := range refs {
			if ref.Plugin == store.AssistantPlugin && ref.Kind == store.AssistantSecretKind {
				view.APIKeySet = true
			}
		}
	}
	// What the assistant can reach, in general. Deliberately not the same
	// question a conversation asks: the capture reader needs a customer to be
	// about, and there is none here, but leaving it off this list would
	// understate what an administrator has actually enabled.
	if tools, err := s.assistantTools(r.Context(), uuid.Nil); err == nil {
		for _, t := range tools {
			view.Available = append(view.Available, t.Name)
		}
	}
	if found, err := s.DB.AllSnapshots(r.Context(), 1); err == nil && len(found) > 0 {
		view.Available = append(view.Available, "diagnostics.read_capture")
	}
	writeJSON(w, http.StatusOK, view)
}

func (s *Server) putAssistantSettings(w http.ResponseWriter, r *http.Request, actor identity.Actor) {
	var body struct {
		store.AssistantConfig
		APIKey string `json:"api_key"`
	}
	if err := decode(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, errBody("invalid json body"))
		return
	}

	cfg := body.AssistantConfig
	cfg.Provider = strings.TrimSpace(cfg.Provider)
	cfg.Model = strings.TrimSpace(cfg.Model)
	cfg.BaseURL = strings.TrimSpace(cfg.BaseURL)
	if cfg.Provider == "" {
		cfg.Provider = "anthropic"
	}
	// A gateway needs somewhere to point at, and a model name it recognises;
	// neither has a sensible default we could invent.
	if cfg.Provider != "anthropic" && cfg.BaseURL == "" {
		writeJSON(w, http.StatusBadRequest, errBody(
			"a service address is required for this provider"))
		return
	}
	if cfg.Model == "" {
		if cfg.Provider != "anthropic" {
			writeJSON(w, http.StatusBadRequest, errBody(
				"a model name is required for this provider"))
			return
		}
		cfg.Model = assistant.DefaultModel
	}
	if cfg.MaxTurns <= 0 {
		cfg.MaxTurns = 12
	}
	if cfg.MaxAnswer <= 0 {
		cfg.MaxAnswer = 16384
	}

	switch cfg.ModelChoice {
	case store.ModelListed, store.ModelFree:
		// Fine as given.
	default:
		cfg.ModelChoice = store.ModelFixed
	}

	// Tidied here rather than trusted from the form: this list is what a
	// permission check reads, and a stray blank or a duplicate in it is the
	// kind of thing that turns into "why can they use that".
	seen := map[string]bool{}
	clean := []string{}
	for _, m := range cfg.AllowedModels {
		m = strings.TrimSpace(m)
		if m == "" || seen[m] {
			continue
		}
		seen[m] = true
		clean = append(clean, m)
	}
	cfg.AllowedModels = clean

	// Offering a list that does not contain the default would let somebody
	// switch away from it and not be able to switch back.
	if cfg.ModelChoice == store.ModelListed && cfg.Model != "" && !seen[cfg.Model] {
		cfg.AllowedModels = append([]string{cfg.Model}, cfg.AllowedModels...)
	}

	cfg.SystemPrompt = strings.TrimSpace(cfg.SystemPrompt)
	// Stored empty rather than as a copy when it matches what we ship, so a
	// future release that improves the wording still reaches this deployment.
	if cfg.SystemPrompt == strings.TrimSpace(assistant.BaseSystemPrompt) {
		cfg.SystemPrompt = ""
	}
	if len(cfg.SystemPrompt) > 20000 {
		writeJSON(w, http.StatusBadRequest, errBody("the instructions are too long"))
		return
	}
	cfg.HousePrompt = strings.TrimSpace(cfg.HousePrompt)
	if len(cfg.HousePrompt) > 4000 {
		writeJSON(w, http.StatusBadRequest, errBody(
			"the added instructions are too long; keep them under 4000 characters"))
		return
	}

	if key := strings.TrimSpace(body.APIKey); key != "" {
		if _, err := s.Creds.Put(r.Context(), nil, store.AssistantPlugin, store.AssistantSecretKind, []byte(key)); err != nil {
			s.fail(w, err, "could not store the API key")
			return
		}
	}

	// Switching it on requires a key, so nobody discovers the omission by
	// asking a question and getting a failure.
	if cfg.Enabled {
		if _, err := s.Creds.Open(r.Context(), nil, store.AssistantPlugin, store.AssistantSecretKind); err != nil {
			writeJSON(w, http.StatusBadRequest, errBody(
				"an API key is required before the assistant can be switched on"))
			return
		}
	}

	if err := s.DB.SetAssistantConfig(r.Context(), cfg, actor.Email); err != nil {
		writeJSON(w, http.StatusBadRequest, errBody(err.Error()))
		return
	}

	s.Audit.Record(r.Context(), audit.Event{
		ActorUserID: actor.Email, Action: "assistant.configure",
		Outcome: audit.OutcomeOK, Detail: enabledWord(cfg.Enabled),
	})
	writeJSON(w, http.StatusOK, map[string]bool{"enabled": cfg.Enabled})
}

// hasCaptures reports whether this customer has a support capture to read.
func (s *Server) hasCaptures(ctx context.Context, customer uuid.UUID) bool {
	found, err := s.DB.Snapshots(ctx, customer)
	return err == nil && len(found) > 0
}
