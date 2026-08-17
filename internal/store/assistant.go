package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// AssistantPlugin is the pseudo-plugin the model provider's API key is stored
// under, so it shares the vault, rotation and audit trail with every other
// credential rather than getting a private arrangement.
const AssistantPlugin = "azir.assistant"

// AssistantSecretKind is the credential kind for the provider API key.
const AssistantSecretKind = "api_key"

// Model choice policies. Who picks which model answers.
const (
	// ModelFixed uses the configured model for everyone.
	ModelFixed = "fixed"
	// ModelListed lets a technician pick from a list an administrator curated.
	ModelListed = "listed"
	// ModelFree lets a technician name any model the provider accepts.
	ModelFree = "free"
)

// AssistantConfig is which model answers and how far it may go.
type AssistantConfig struct {
	Enabled   bool   `json:"enabled"`
	Provider  string `json:"provider"`
	Model     string `json:"model"`
	BaseURL   string `json:"base_url"`
	MaxTurns  int    `json:"max_turns"`
	MaxAnswer int    `json:"max_answer_tokens"`

	// ModelChoice is one of ModelFixed, ModelListed or ModelFree.
	ModelChoice string `json:"model_choice"`
	// AllowedModels is what ModelListed offers.
	AllowedModels []string `json:"allowed_models"`
	// SystemPrompt replaces the shipped instructions. Empty means use whatever
	// this version of Azir ships, so an improved default still reaches a
	// deployment that never overrode it.
	SystemPrompt string `json:"system_prompt"`
	// HousePrompt is added after the instructions above.
	HousePrompt string `json:"house_prompt"`

	UpdatedAt time.Time `json:"updated_at"`
	UpdatedBy string    `json:"updated_by"`
}

// MayUse reports whether a technician is allowed to ask for this model.
//
// Asked here rather than at the edge so every path into the assistant — the
// streaming endpoint, the plain one, and anything added later — is checked by
// the same function. An empty request means "whatever the default is", which is
// always allowed.
func (c AssistantConfig) MayUse(model string) bool {
	model = strings.TrimSpace(model)
	if model == "" || model == c.Model {
		return true
	}
	switch c.ModelChoice {
	case ModelFree:
		return true
	case ModelListed:
		return slices.Contains(c.AllowedModels, model)
	default:
		return false
	}
}

func (db *DB) AssistantConfig(ctx context.Context) (AssistantConfig, error) {
	var c AssistantConfig
	err := db.pool.QueryRow(ctx, `
		SELECT enabled, provider, model, base_url, max_turns,
		       max_answer_tokens, model_choice, allowed_models, system_prompt,
		       house_prompt, updated_at, updated_by
		FROM assistant_config WHERE id`).Scan(
		&c.Enabled, &c.Provider, &c.Model, &c.BaseURL, &c.MaxTurns,
		&c.MaxAnswer, &c.ModelChoice, &c.AllowedModels, &c.SystemPrompt,
		&c.HousePrompt, &c.UpdatedAt, &c.UpdatedBy)
	if err != nil {
		return AssistantConfig{}, fmt.Errorf("store: read assistant config: %w", err)
	}
	return c, nil
}

func (db *DB) SetAssistantConfig(ctx context.Context, c AssistantConfig, actor string) error {
	_, err := db.pool.Exec(ctx, `
		UPDATE assistant_config SET
			enabled = $1, provider = $2, model = $3, base_url = $4,
			max_turns = $5, max_answer_tokens = $6, model_choice = $7,
			allowed_models = $8, system_prompt = $9, house_prompt = $10,
			updated_at = now(), updated_by = $11
		WHERE id`,
		c.Enabled, c.Provider, c.Model, c.BaseURL, c.MaxTurns,
		c.MaxAnswer, c.ModelChoice, c.AllowedModels, c.SystemPrompt,
		c.HousePrompt, actor)
	if err != nil {
		return fmt.Errorf("store: write assistant config: %w", err)
	}
	return nil
}

// --- conversations -----------------------------------------------------------

// Conversation is one chat, owned by the person who started it.
type Conversation struct {
	ID          uuid.UUID `json:"id"`
	Title       string    `json:"title"`
	Model       string    `json:"model,omitempty"`
	SubjectKind string    `json:"subject_kind,omitempty"`
	SubjectID   string    `json:"subject_id,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// Step is one lookup the assistant made while answering.
//
// Names and arguments only. The result is deliberately not kept: storing what
// came back would put a second copy of customer data in the chat log, which is
// another place it can leak from and another place to have to protect.
type Step struct {
	Capability string          `json:"capability"`
	Tool       string          `json:"tool"`
	Args       json.RawMessage `json:"args,omitempty"`
	Failed     bool            `json:"failed,omitempty"`
	Detail     string          `json:"detail,omitempty"`
}

// Message is one turn.
type Message struct {
	ID        uuid.UUID `json:"id"`
	Role      string    `json:"role"`
	Content   string    `json:"content"`
	Steps     []Step    `json:"steps,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// ErrNotOwned means the conversation exists but belongs to somebody else.
var ErrNotOwned = errors.New("store: this conversation belongs to another person")

func (db *DB) CreateConversation(ctx context.Context, userID uuid.UUID, title, subjectKind, subjectID, model string) (Conversation, error) {
	c := Conversation{
		ID:          uuid.New(),
		Title:       strings.TrimSpace(title),
		Model:       strings.TrimSpace(model),
		SubjectKind: subjectKind,
		SubjectID:   subjectID,
	}
	err := db.pool.QueryRow(ctx, `
		INSERT INTO conversations (id, user_id, title, subject_kind, subject_id, model)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING created_at, updated_at`,
		c.ID, userID, c.Title, c.SubjectKind, c.SubjectID, c.Model,
	).Scan(&c.CreatedAt, &c.UpdatedAt)
	if err != nil {
		return Conversation{}, fmt.Errorf("store: create conversation: %w", err)
	}
	return c, nil
}

// Conversations lists one person's chats, most recently used first.
//
// Narrowing by subject is what lets the panel resume rather than restart:
// someone who comes back to a ticket tomorrow should find what they already
// worked out about it, not an empty box.
func (db *DB) Conversations(ctx context.Context, userID uuid.UUID, subjectKind, subjectID string, limit int) ([]Conversation, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := db.pool.Query(ctx, `
		SELECT id, title, model, subject_kind, subject_id, created_at, updated_at
		FROM conversations
		WHERE user_id = $1
		  AND ($2 = '' OR (subject_kind = $2 AND subject_id = $3))
		ORDER BY updated_at DESC LIMIT $4`, userID, subjectKind, subjectID, limit)
	if err != nil {
		return nil, fmt.Errorf("store: list conversations: %w", err)
	}
	defer rows.Close()

	out := []Conversation{}
	for rows.Next() {
		var c Conversation
		if err := rows.Scan(&c.ID, &c.Title, &c.Model, &c.SubjectKind, &c.SubjectID, &c.CreatedAt, &c.UpdatedAt); err != nil {
			return nil, fmt.Errorf("store: scan conversation: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// Conversation returns one chat, refusing it to anyone but its owner.
func (db *DB) Conversation(ctx context.Context, id, userID uuid.UUID) (Conversation, error) {
	var c Conversation
	var owner uuid.UUID
	err := db.pool.QueryRow(ctx, `
		SELECT id, user_id, title, model, subject_kind, subject_id, created_at, updated_at
		FROM conversations WHERE id = $1`, id).Scan(
		&c.ID, &owner, &c.Title, &c.Model, &c.SubjectKind, &c.SubjectID, &c.CreatedAt, &c.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Conversation{}, ErrNotFound
	}
	if err != nil {
		return Conversation{}, fmt.Errorf("store: read conversation: %w", err)
	}
	// Checked here rather than in the WHERE clause so the caller can tell
	// "does not exist" from "not yours" — and so a missing check is a visible
	// omission rather than a silently permissive query.
	if owner != userID {
		return Conversation{}, ErrNotOwned
	}
	return c, nil
}

func (db *DB) Messages(ctx context.Context, conversationID uuid.UUID) ([]Message, error) {
	rows, err := db.pool.Query(ctx, `
		SELECT id, role, content, steps, created_at
		FROM messages WHERE conversation_id = $1 ORDER BY created_at`, conversationID)
	if err != nil {
		return nil, fmt.Errorf("store: list messages: %w", err)
	}
	defer rows.Close()

	out := []Message{}
	for rows.Next() {
		var m Message
		var steps []byte
		if err := rows.Scan(&m.ID, &m.Role, &m.Content, &steps, &m.CreatedAt); err != nil {
			return nil, fmt.Errorf("store: scan message: %w", err)
		}
		if len(steps) > 0 {
			_ = json.Unmarshal(steps, &m.Steps)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// AddMessage appends a turn and marks the conversation as recently used.
func (db *DB) AddMessage(ctx context.Context, conversationID uuid.UUID, m Message) (Message, error) {
	if m.ID == uuid.Nil {
		m.ID = uuid.New()
	}
	steps, err := json.Marshal(m.Steps)
	if err != nil {
		return Message{}, fmt.Errorf("store: encode steps: %w", err)
	}
	if m.Steps == nil {
		steps = []byte("[]")
	}

	tx, err := db.pool.Begin(ctx)
	if err != nil {
		return Message{}, fmt.Errorf("store: add message: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op once committed

	if err := tx.QueryRow(ctx, `
		INSERT INTO messages (id, conversation_id, role, content, steps)
		VALUES ($1, $2, $3, $4, $5) RETURNING created_at`,
		m.ID, conversationID, m.Role, m.Content, steps,
	).Scan(&m.CreatedAt); err != nil {
		return Message{}, fmt.Errorf("store: add message: %w", err)
	}

	if _, err := tx.Exec(ctx,
		`UPDATE conversations SET updated_at = now() WHERE id = $1`, conversationID); err != nil {
		return Message{}, fmt.Errorf("store: touch conversation: %w", err)
	}
	return m, tx.Commit(ctx)
}

// SetConversationModel records which model a chat should use from now on.
func (db *DB) SetConversationModel(ctx context.Context, id uuid.UUID, model string) error {
	_, err := db.pool.Exec(ctx,
		`UPDATE conversations SET model = $1 WHERE id = $2`, strings.TrimSpace(model), id)
	if err != nil {
		return fmt.Errorf("store: set conversation model: %w", err)
	}
	return nil
}

// SetConversationTitle names a chat, usually from its first message.
func (db *DB) SetConversationTitle(ctx context.Context, id uuid.UUID, title string) error {
	_, err := db.pool.Exec(ctx,
		`UPDATE conversations SET title = $1 WHERE id = $2`, strings.TrimSpace(title), id)
	if err != nil {
		return fmt.Errorf("store: title conversation: %w", err)
	}
	return nil
}

func (db *DB) DeleteConversation(ctx context.Context, id, userID uuid.UUID) error {
	tag, err := db.pool.Exec(ctx,
		`DELETE FROM conversations WHERE id = $1 AND user_id = $2`, id, userID)
	if err != nil {
		return fmt.Errorf("store: delete conversation: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
