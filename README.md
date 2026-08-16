# Azir

A read-only AI troubleshooting assistant for MSP work, and a platform for the
systems that work touches. Syncro and 3CX are the first two plugins, not the
product.

The job Azir replaces is opening ChatGPT, pasting in a ticket thread,
explaining the customer's setup, and asking for help. It does that job by
already knowing all of it.

## Status

**Phase 1.** No business logic yet — no Syncro, no 3CX, no model. What exists
is the foundation everything else needs: a credential vault, the customer
spine, the capability approval gate, an audit trail, and redaction that is
tested rather than asserted.

## Two containers

Azir is a single binary — it embeds a NATS server with JetStream, the HTTP API,
the built frontend, and a supervisor that runs bundled plugins as child
processes. Postgres is the one external service.

Plugins are still separate processes speaking NATS, so crash isolation is
unchanged and a third-party plugin can run as its own container against the
same server. Setting `NATS_URL` points everything at an external cluster.

Three choices worth knowing about:

**Postgres with pgvector, not SQLite.** Semantic recall over tickets,
conversations and memory is the feature a general chat tool cannot match, and
it needs an ANN index. `sqlite-vec` is brute-force only and degrades past
roughly a million vectors; pgvector's HNSW answers in 5–20ms at 95%+ recall well
past ten million. Call records alone add on the order of a million rows a year.
Postgres also gives real write concurrency for ingest that runs while backfill
does, `tsvector` alongside vectors for hybrid retrieval in one query, and
partitioning as the high-churn tables grow.

Turso was evaluated and rejected: the Rust rewrite is in beta and its own
maintainers advise caution for mission-critical use, which this is — Azir holds
System Owner credentials for every customer PBX.

**A Go supervisor, not s6.** A child's stdout is piped through the same
redacting log handler core uses, so a plugin that logs carelessly still cannot
put a credential on the container's stdout. An external init system would write
those bytes straight out and silently undo the guarantee the rest of the system
is built around. It also needs no root and no second init.

**No entrypoint script.** The binary validates its own configuration, creates
its own directories, and fails with real errors. A shell wrapper would add a
moving part to a design whose point is having fewer of them.

## Design rules

Three constraints shape every decision here.

**Read-only, system-wide.** No write path to Syncro or 3CX exists in any
component. This is a property of the system, not a restriction on the model.
The SDK refuses to register a tool declaring `Mutates: true` — such a plugin
fails to boot rather than failing quietly. Azir writes only its own data:
conversations, drafts, memory, audit.

**The model gets facts, never keys.** It names what and where; it never learns
how. Customers are addressed by opaque ID. Credentials are resolved inside the
plugin, server-side, and every handler return value passes the SDK redactor
before reaching the wire.

**Discovery proposes; an administrator approves.** A plugin appearing in `$SRV`
becomes a candidate capability, not a granted one — it lands as `pending` and
is unusable until someone activates it. Without this, anyone able to start a
container could extend what Azir can do. Rediscovery never overwrites a
decision, so restarting a plugin cannot launder a rejection back into pending.

## Layout

```
cmd/azir-core/        the whole application
cmd/plugin-echo/      diagnostic plugin proving the transport
internal/api/         HTTP surface and SPA serving
internal/audit/       append-only trail, JetStream to SQLite
internal/logging/     the redacting slog handler
internal/natsd/       embedded NATS server
internal/registry/    service discovery and the capability index
internal/store/       SQLite: spine, credentials, capabilities, audit
internal/supervisor/  bundled plugins as supervised children
internal/vault/       envelope encryption and key rotation
pkg/plugin/           the SDK — importable from outside this module
web/                  React + TypeScript, embedded into the binary
```

`pkg/` rather than `internal/` for the SDK is deliberate: it is the one package
that must be importable from a plugin living in another repository.

## Running it

```sh
make keygen              # generate a master key
export AZIR_MASTER_KEY=…
make up                  # build and start azir + postgres
make smoke               # twelve checks against the running stack
make psql                # a session against the running database
make down
```

Then open <http://localhost:8080> — API and UI on the same port.

Two things to back up: the Postgres volume, and `/var/lib/azir` for JetStream.

Postgres publishes on **5433** by default so it does not collide with one
already running on the host. Override with `AZIR_PG_PORT`.

### Tuning

`deploy/postgres/postgresql.conf` is a commented, checked-in config rather than
an autotuner — a generated config makes behaviour depend on the machine a
container landed on, which turns "the query got slow" into archaeology. The
baseline assumes ~4GB for the container; scale the memory settings with the
limit. `jit = off` is deliberate: JIT regularly costs more than it saves on
short pgvector queries and is a known source of latency spikes.

### Configuration

| Variable | Default | Purpose |
|---|---|---|
| `AZIR_MASTER_KEY` | — | base64 32-byte key sealing the vault. Required. |
| `AZIR_MASTER_KEYS` | — | `1:<b64>,2:<b64>` when more than one key version is loaded |
| `DATABASE_URL` | — | Postgres connection string. Required. |
| `AZIR_DATA_DIR` | `/var/lib/azir` | JetStream storage |
| `AZIR_HTTP_ADDR` | `:8080` | API and UI listener |
| `NATS_URL` | embedded | set to use an external NATS instead |
| `AZIR_PLUGIN_DIR` | `/usr/local/lib/azir/plugins` | bundled plugins to supervise |
| `AZIR_LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` |

## Development

```sh
make test-db   # start Postgres and create the test database
make check     # gofmt, go vet, go test -race
make build     # binaries into bin/
```

NATS runs in-process for tests, so the discovery round-trip is verified against
the real protocol. Store tests need Postgres — mocking a store proves nothing
about the SQL, which is the part that breaks — and skip without
`AZIR_TEST_DATABASE_URL`.

### Migrations

The migrator is hand-rolled but not naive. Each migration runs in its own
transaction under a Postgres advisory lock, so concurrent replica starts
serialise rather than race. Every file is checksummed: editing an applied
migration is a fatal error, not a silent no-op, because that is precisely how
environments diverge. A database carrying a migration this binary does not know
about is also refused, so an accidental rollback cannot run against a future
schema. A migration needing `CREATE INDEX CONCURRENTLY` opts out of its
transaction with an `-- azir:no-transaction` marker, and must then be written
idempotently.

The canary tests are the ones that matter. A sentinel credential is pushed
through every route that could leak it — messages, attributes, errors, groups,
derived loggers, the database file — and asserted absent. One test deliberately
proves the *detector* works, so a green suite means redaction ran rather than
that the check was vacuous.

## Writing a plugin

A plugin is a NATS micro service. Nothing in the SDK's surface mentions NATS —
`plugin.Serve` is the entire transport boundary.

```go
p := plugin.Plugin{
    Name:     "syncro",
    Version:  "0.1.0",
    Category: plugin.CategoryPSA,
    Tools: []plugin.Tool{{
        Name:        "tickets.search",
        Description: "Search tickets by customer, status or free text.",
        Provides:    []plugin.Capability{plugin.CapWorkItemsSearch},
        Schema:      schema,
        Handler:     searchTickets,
    }},
}
return plugin.Serve(ctx, p, plugin.WithSecrets(apiKey))
```

Two rules that are enforced rather than documented:

- **Return `plugin.Errorf` for failures.** Any other error is logged locally
  and reported to the caller as a generic failure, because wrapped vendor
  errors routinely carry request URLs and auth headers.
- **Register every resolved credential via `WithSecrets`.** The redactor scrubs
  those literals wherever they appear, including inside prose that key-name
  rules would never inspect.

### Capability tags

Tools declare what they provide from a vocabulary Azir owns
(`pkg/plugin/capability.go`). Core never asks whether a plugin "is a PSA"; it
asks whether anything provides `customers.list`. Features declare the
capabilities they need and report themselves unavailable, with a reason, when
nothing supplies them — so a partial integration is still useful.

The vocabulary is deliberately small. Tags are added when a real integration
shows genuine overlap, never in anticipation of one.
