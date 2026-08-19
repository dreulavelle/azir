# Architecture

## One binary, two services beside it

Azir is a single binary. It embeds a NATS server with JetStream, the HTTP API,
the built frontend, and a supervisor that runs bundled plugins as child
processes. Postgres and SearXNG run alongside it.

Plugins are separate processes speaking NATS, so crash isolation is real and a
third-party plugin can run as its own container against the same server.
Setting `NATS_URL` points everything at an external cluster instead of the
embedded one.

### Postgres with pgvector, not SQLite

Semantic recall over tickets, conversations and memory is the feature a general
chat tool cannot match, and it needs an approximate-nearest-neighbour index.
`sqlite-vec` is brute-force only and degrades past roughly a million vectors;
pgvector's HNSW answers in 5–20ms at 95%+ recall well past ten million. Call
records alone add on the order of a million rows a year.

Postgres also gives real write concurrency for ingest running while backfill
does, `tsvector` alongside vectors for hybrid retrieval in one query, and
partitioning as the high-churn tables grow.

Turso was evaluated and rejected: the Rust rewrite is in beta and its own
maintainers advise caution for mission-critical use, which this is — Azir holds
System Owner credentials for every customer PBX.

### A Go supervisor, not s6

A child's stdout is piped through the same redacting log handler core uses, so
a plugin that logs carelessly still cannot put a credential on the container's
stdout. An external init system would write those bytes straight out and
silently undo the guarantee the rest of the system is built around. It also
needs no root and no second init.

### No entrypoint script

The binary validates its own configuration, creates its own directories, and
fails with real errors. A shell wrapper would add a moving part to a design
whose point is having fewer of them.

## The three rules

**The model gets facts, never keys.** It names what and where; it never learns
how. Customers are addressed by opaque ID. Credentials are resolved inside the
plugin, server-side, and every handler return value passes the SDK redactor
before reaching the wire.

**Reads are always allowed. Writes are never unattended.** Azir began
read-only, and the SDK refused to register a mutating tool at all. What
replaced that refusal is narrower rather than looser: a tool declaring
`Mutates` must name the permission it requires, or the plugin does not start —
a write nobody has to be allowed to make is not a write anyone should make.
Beyond that, every write has a person in it. A technician acts on a screen, or
the assistant proposes and a technician approves the change itself. Bulk edits
are compared against the live system and shown as a before-and-after; what is
approved is the difference, never the intention.

**Discovery proposes; an administrator approves.** A plugin appearing in `$SRV`
becomes a candidate capability, not a granted one — it lands as `pending` and
is unusable until someone activates it. Without this, anyone able to start a
container could extend what Azir can do. Rediscovery never overwrites a
decision, so restarting a plugin cannot launder a rejection back into pending.

## Capability tags

Tools declare what they provide from a vocabulary Azir owns
(`pkg/plugin/capability.go` — 39 tags today). Core never asks whether a plugin
"is a PSA"; it asks whether anything provides `customers.list`. Features
declare the capabilities they need and report themselves unavailable, with a
reason, when nothing supplies them — so a partial integration is still useful.

Reads and writes are separate tags, and the indexes behind them are separate
too. A caller asking to read a ticket and a caller asking to comment on one
want different things, and a shared tag makes them indistinguishable. It also
matters to the assistant, which is offered writes as `propose.<capability>`:
"propose.work_items.get" is a phrase nobody can act on.

The vocabulary is deliberately small. Tags are added when a real integration
shows genuine overlap, never in anticipation of one.

## Permissions across heterogeneous plugins

Azir has no permission table, deliberately. Every vendor models permissions
differently — Syncro has a read/write/delete matrix, 3CX has roles, the next
one will have something else — so a central table would either be Syncro-shaped
and wrong, or abstract enough to mean nothing.

Instead a plugin reports what it can currently do, and core interprets none of
it. `Preflight` returns per-tool availability with a human-readable reason;
core merges that into the registry and drops unavailable tools from the
capability index. The vendor-specific mapping lives entirely inside the plugin,
which is where vendor knowledge belongs.

**Probe, do not ask.** The Syncro plugin originally read `/me` to learn what
its token could do. Testing against three tokens on one account — full admin,
partially restricted, and ticket-only — showed `/me` returns *identical*
permissions for all three: it reports the **user's** permissions, not the
**token's**. The self-report was confidently wrong, so availability is now
determined by issuing a cheap single-item read against each resource and
observing what comes back. Measured beats claimed, and every report says which
one it is.

Two details that only appear under a real restricted token. Syncro answers a
permission denial with **401, not 403**, so a refusal is indistinguishable from
a bad credential by status alone — the SDK's message names both causes rather
than asserting the wrong one. And the permission precondition is applied at
registration by a wrapper, not called inside each handler: a forgotten guard is
invisible until someone meets a bare 401, and "remember to call this" is not a
mechanism.

This mirrors how Airbyte handles the same problem across hundreds of
connectors: `check` that credentials work at all, `discover` what is actually
available with them, and fail at read time for anything else. The platform
receives a catalog, never a permission model.

## On MCP

Syncro publishes an MCP server, and the temptation is to wire MCP into core.
That would be a mistake: every guarantee Azir makes — the credential firewall,
outbound redaction, the approval gate, capability tags — is enforced in
`pkg/plugin`. An MCP server is a third-party tool surface that will have write
tools, returns content we do not shape, and holds its own credentials.

The right shape is an MCP *bridge plugin*, which inherits those guarantees:
declaring any mutating MCP tool as `Mutates` with a required permission so it
lands behind the same gate, drawing server credentials from the vault, and
landing its tools as pending capabilities.

Worth being clear that a bridge is strictly worse than a native plugin where
one exists. The Syncro plugin trims responses, paces to Syncro's documented
limit and curates its tool surface; their MCP would give us their shapes and
their verbosity. MCP earns its place on the long tail — systems that will never
justify a plugin of their own.

## Layout

```
cmd/azir-core/        the whole application
cmd/plugin-3cx/       3CX: health, extensions, handsets, calls, logs, bundles
cmd/plugin-syncro/    Syncro MSP: tickets, customers, assets, invoices
cmd/plugin-searxng/   private web search
cmd/plugin-echo/      diagnostic plugin proving the transport
internal/api/         HTTP surface and SPA serving
internal/assistant/   the chat loop, tool offers and proposals
internal/audit/       append-only trail
internal/blf/         3CX desk-phone key layouts
internal/bulk/        sheets, plans and before-and-after comparison
internal/identity/    sessions, actors and the customer spine
internal/logging/     the redacting slog handler
internal/natsd/       embedded NATS server
internal/oidc/        single sign-on
internal/pluginhost/  vault, config and identity resolution for plugins
internal/registry/    service discovery and the capability index
internal/store/       Postgres: spine, credentials, capabilities, audit
internal/supervisor/  bundled plugins as supervised children
internal/supportinfo/ reading a 3CX support bundle
internal/syncro/      Syncro API client
internal/vault/       envelope encryption and key rotation
pkg/plugin/           the SDK — importable from outside this module
web/                  React + TypeScript, embedded into the binary
```

`pkg/` rather than `internal/` for the SDK is deliberate: it is the one package
that must be importable from a plugin living in another repository.
