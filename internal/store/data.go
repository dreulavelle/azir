package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

/*
What Azir is holding, and how to put it back to empty.

The screen this feeds is not a database console, and deliberately so. It
answers the one question somebody running an MSP has to be able to answer
about their own tooling — what of my customers' data is in here, and when
does it leave — and gives them a way to clear it without a terminal and a
password.

So everything here is named the way a person would ask about it. "Ticket
memory" rather than ticket_memory and recall_progress; "Connections" rather
than three tables that happen to hold the same idea. The grouping is the
feature: a list of table names would be accurate and would answer nothing.
*/

// Stored is one kind of thing Azir keeps.
type Stored struct {
	Name   string `json:"name"`
	Detail string `json:"detail"`
	Count  int64  `json:"count"`
	Bytes  int64  `json:"bytes"`
	// Cleared reports whether starting fresh removes this. Shown next to
	// everything, because which half a thing falls in is the entire question
	// somebody is asking before they press the button.
	Cleared bool `json:"cleared"`
	// Expires is how this goes away on its own, in words. Empty means it
	// stays until somebody removes it.
	Expires string `json:"expires,omitempty"`
}

/*
The catalogue.

Table names here are compile-time constants written in this file and never
come from a request, which is what makes it safe to build the query text
around them below. Nothing from outside this file may be interpolated into
that SQL.
*/
var stored = []struct {
	name, detail string
	// counted is the table whose row count is worth showing. A conversation
	// is a thing somebody recognises; the messages inside it are not, and
	// reporting eleven thousand of them would be true and useless.
	counted string
	tables  []string
	cleared bool
	expires string
}{
	{
		name:    "Conversations",
		detail:  "Chats with the assistant, and every message in them.",
		counted: "conversations",
		tables:  []string{"conversations", "messages"},
		cleared: true,
	},
	{
		name:    "Diagnostic captures",
		detail:  "3CX support bundles, read down to a report.",
		counted: "snapshots",
		tables:  []string{"snapshots"},
		cleared: true,
		expires: "14 days, unless pinned",
	},
	{
		name:    "Ticket memory",
		detail:  "What finished tickets were about, so past work can be recalled.",
		counted: "ticket_memory",
		tables:  []string{"ticket_memory", "recall_progress"},
		cleared: true,
	},
	{
		name:    "Proposed changes",
		detail:  "Changes the assistant suggested, waiting on a person.",
		counted: "proposed_changes",
		tables:  []string{"proposed_changes"},
		cleared: true,
	},
	{
		name:    "Activity log",
		detail:  "Every tool call and administrative action.",
		counted: "audit_log",
		tables:  []string{"audit_log"},
		cleared: true,
	},
	{
		name:    "Cached answers",
		detail:  "Recent tool results, reused instead of asking a connected system again.",
		counted: "tool_cache",
		tables:  []string{"tool_cache"},
		cleared: true,
		expires: "7 days",
	},
	{
		name:    "Customers",
		detail:  "The spine everything else hangs off, and what each connected system calls them.",
		counted: "customers",
		tables:  []string{"customers", "customer_identities"},
	},
	{
		name:    "People",
		detail:  "Accounts that can sign in, and sessions currently open.",
		counted: "users",
		tables:  []string{"users", "sessions"},
	},
	{
		name:    "Connections",
		detail:  "Stored credentials, plugin settings, and which tools are approved.",
		counted: "credentials",
		tables:  []string{"credentials", "plugin_config", "capabilities"},
	},
}

// DataUsage reports what is stored, and how large the database is altogether.
// The total is bigger than the parts add up to — indexes, the schema itself
// and Postgres's own catalogues are all in it — so it is returned separately
// rather than presented as a sum.
func (db *DB) DataUsage(ctx context.Context) ([]Stored, int64, error) {
	parts := make([]string, len(stored))
	for i, k := range stored {
		sizes := make([]string, len(k.tables))
		for j, t := range k.tables {
			sizes[j] = fmt.Sprintf("pg_total_relation_size('%s')", t)
		}
		parts[i] = fmt.Sprintf(
			`SELECT %d AS ord, (SELECT count(*) FROM %s) AS n, (%s)::bigint AS bytes`,
			i, k.counted, strings.Join(sizes, " + "))
	}

	rows, err := db.pool.Query(ctx, strings.Join(parts, " UNION ALL ")+" ORDER BY ord")
	if err != nil {
		return nil, 0, fmt.Errorf("store: data usage: %w", err)
	}
	defer rows.Close()

	out := make([]Stored, 0, len(stored))
	for rows.Next() {
		var ord int
		var count, bytes int64
		if err := rows.Scan(&ord, &count, &bytes); err != nil {
			return nil, 0, err
		}
		k := stored[ord]
		out = append(out, Stored{
			Name:    k.name,
			Detail:  k.detail,
			Count:   count,
			Bytes:   bytes,
			Cleared: k.cleared,
			Expires: k.expires,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}

	var total int64
	if err := db.pool.QueryRow(ctx,
		`SELECT pg_database_size(current_database())`).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("store: database size: %w", err)
	}
	return out, total, nil
}

// Removed is what clearing took, for the person who pressed the button and for
// the audit record written afterwards.
type Removed struct {
	Name string `json:"name"`
	Rows int64  `json:"rows"`
}

/*
The order things are deleted in, and which reset each step belongs to.

FK-safe throughout, and children go before parents even where a cascade would
have taken them anyway. Deleting a conversation would silently remove its
messages and its proposed changes, and the counts reported back would then say
zero for both — which reads as "there was nothing there" rather than "something
else deleted it first". Being explicit costs two statements and keeps the
number honest.

work marks the steps that Start fresh runs. The rest run only for the deeper
reset, and they sit between the work steps and the log deliberately: everything
that points at a customer has to go before the customer does, and the log is
last in both because it is where the clearing itself gets recorded.

headline marks the row whose count is the one worth reporting under that name.
*/
var clearing = []struct {
	name     string
	table    string
	headline bool
	work     bool
}{
	{name: "Proposed changes", table: "proposed_changes", headline: true, work: true},
	{name: "Conversations", table: "messages", work: true},
	{name: "Conversations", table: "conversations", headline: true, work: true},
	{name: "Diagnostic captures", table: "snapshots", headline: true, work: true},
	{name: "Ticket memory", table: "ticket_memory", headline: true, work: true},
	{name: "Ticket memory", table: "recall_progress", work: true},
	{name: "Cached answers", table: "tool_cache", headline: true, work: true},

	// Deeper only. Everything above already let go of the customers these
	// point at, so the spine can follow.
	{name: "Connections", table: "credentials", headline: true},
	{name: "Connections", table: "plugin_config"},
	{name: "Connections", table: "capabilities"},
	{name: "Connections", table: "webhook_endpoints"},
	{name: "Customers", table: "customer_identities"},
	{name: "Customers", table: "customers", headline: true},
	{name: "Assistant settings", table: "assistant_config", headline: true},
	{name: "Branding", table: "branding", headline: true},
	{name: "Single sign-on", table: "auth_config", headline: true},
	{name: "Single sign-on", table: "oidc_states"},

	{name: "Activity log", table: "audit_log", headline: true, work: true},
}

/*
SignIn is how the people who already have accounts get in.

Read before the deeper reset, because that reset takes single sign-on with it.
An account with a password can still get in afterwards; one that has only ever
arrived through the provider cannot, and there is nothing it can do about it
from a sign-in page. Whether that is acceptable is a judgement for whoever is
about to press the button, so they are told rather than found out.
*/
type SignIn struct {
	// WithPassword can sign in whatever happens to the provider.
	WithPassword []string `json:"with_password"`
	// SSOOnly would have no way in once the provider is gone.
	SSOOnly []string `json:"sso_only"`
}

// SignInRoutes reports how each enabled account currently gets in.
func (db *DB) SignInRoutes(ctx context.Context) (SignIn, error) {
	rows, err := db.pool.Query(ctx,
		`SELECT email, password_hash IS NOT NULL AND password_hash <> ''
		   FROM users WHERE NOT disabled ORDER BY email`)
	if err != nil {
		return SignIn{}, fmt.Errorf("store: sign-in routes: %w", err)
	}
	defer rows.Close()

	var out SignIn
	for rows.Next() {
		var email string
		var hasPassword bool
		if err := rows.Scan(&email, &hasPassword); err != nil {
			return SignIn{}, err
		}
		if hasPassword {
			out.WithPassword = append(out.WithPassword, email)
		} else {
			out.SSOOnly = append(out.SSOOnly, email)
		}
	}
	return out, rows.Err()
}

// ErrWouldLockEveryoneOut means the deeper reset was refused because nobody
// would have been able to sign in afterwards.
var ErrWouldLockEveryoneOut = errors.New(
	"store: every account signs in through the provider, and the reset would remove it")

/*
ClearWork empties everything Azir has done and keeps everything it was set up
with.

The split is the whole design. Accounts, credentials, plugin settings, tool
approvals and the customer spine all survive, so somebody who clears is still
signed in, still connected, and still knows who their customers are — they
have an empty desk, not a new install.

recall_progress goes with ticket_memory rather than being left behind. It
records how far recall has read; keeping it while removing what it produced
would leave recall convinced it had already done the work, and the memory
would never come back.

The audit log is cleared last and the clearing is recorded after this returns,
so the one entry in an otherwise empty log is the act that emptied it.
*/
func (db *DB) ClearWork(ctx context.Context) ([]Removed, error) {
	return db.clear(ctx, false)
}

/*
ResetAll takes the setup too, and stops short of the people.

What survives is exactly the ability to sign in and the roles that say what
each account may do — so the console comes back empty rather than coming back
asking to be installed. Everything else goes: the customers, the credentials
for the systems they were reached through, which tools were approved, the
assistant's key and model, the branding, and single sign-on.

Refused outright if it would leave nobody able to sign in. Removing the
provider is the point of the reset, but doing it when no account has a password
turns a reset into a lockout that cannot be undone from a browser, and no
confirmation dialog makes that a reasonable thing to allow.
*/
func (db *DB) ResetAll(ctx context.Context) ([]Removed, error) {
	routes, err := db.SignInRoutes(ctx)
	if err != nil {
		return nil, err
	}
	if len(routes.WithPassword) == 0 {
		return nil, ErrWouldLockEveryoneOut
	}
	return db.clear(ctx, true)
}

func (db *DB) clear(ctx context.Context, deep bool) ([]Removed, error) {
	tx, err := db.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("store: clear: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op once committed

	var out []Removed
	for _, step := range clearing {
		if !deep && !step.work {
			continue
		}
		// step.table is a constant from this file, never a request value.
		tag, err := tx.Exec(ctx, fmt.Sprintf(`DELETE FROM %s`, step.table))
		if err != nil {
			return nil, fmt.Errorf("store: clear %s: %w", step.table, err)
		}
		if step.headline {
			out = append(out, Removed{Name: step.name, Rows: tag.RowsAffected()})
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("store: clear: %w", err)
	}
	return out, nil
}
