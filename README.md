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

**Postgres 18 with pgvector, not SQLite.** Semantic recall over tickets,
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
cmd/plugin-syncro/    Syncro MSP: tickets, customers, assets (read-only)
internal/api/         HTTP surface and SPA serving
internal/audit/       append-only trail, JetStream to SQLite
internal/logging/     the redacting slog handler
internal/natsd/       embedded NATS server
internal/registry/    service discovery and the capability index
internal/store/       Postgres: spine, credentials, capabilities, audit
internal/syncro/      Syncro API client
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

`deploy/postgres/postgresql.conf` targets PostgreSQL 18 and is a commented,
checked-in config rather than an autotuner — a generated config makes behaviour depend on the machine a
container landed on, which turns "the query got slow" into archaeology. The
baseline assumes ~4GB for the container; scale the memory settings with the
limit. `jit = off` is deliberate: JIT regularly costs more than it saves on
short pgvector queries and is a known source of latency spikes.

`effective_io_concurrency` now counts I/Os the executor keeps in flight rather
than being a device-parallelism hint, so the pre-18 advice to set it in the
hundreds no longer applies.

#### On asynchronous I/O

Azir already runs PostgreSQL 18's async I/O. `io_method = worker` is the async
implementation using worker processes; it is not the old synchronous path. In
published cold-cache benchmarks the large jump is sync → worker, with io_uring
adding a further increment — and for high-bandwidth sequential scans worker can
beat io_uring outright, because it spreads CPU load across processes.

io_uring stays off for two reasons.

It **bypasses seccomp filtering** rather than merely needing a wider profile:
operations are submitted through the ring instead of as syscalls, so a filter
cannot see them. Docker blocks it by default for exactly this reason, and
Google attributed a majority of the kernel exploits in one bug-bounty year to
it. The container in question holds envelope-encrypted System Owner credentials
for every customer PBX.

And it would buy little today. Async I/O accelerates reads that reach the disk;
HNSW traversal against an index resident in `shared_buffers` does not reach the
disk at all. The threshold worth watching is when the working set outgrows
shared memory — at 1536 dimensions a float32 embedding is ~6KB, so 1GB holds
roughly 170k vectors. At that point **raise `shared_buffers` first**:
eliminating the I/O beats making it faster. io_uring becomes interesting only
once the working set exceeds the RAM you are willing to buy for it, and it
should be enabled against a measurement rather than a hunch.

#### Autovacuum

The global settings are a floor; each table tightens further in
`0002_autovacuum.sql`, because a setting that suits `audit_log` is wasteful on
`customers`. `audit_log` is append-only and churns constantly, so it vacuums
aggressively and freezes early — un-frozen pages otherwise accumulate until an
anti-wraparound vacuum has to read the whole table in one stall. `capabilities`
is tiny but rewritten every discovery sweep, which produces dead tuples out of
all proportion to its size.

Two global values matter more than they look. `autovacuum_vacuum_cost_limit` is
raised well above the default throttle, which was calibrated for spinning disks
and is the usual reason autovacuum cannot keep up. And `autovacuum_work_mem` is
set **explicitly**: its `-1` default inherits `maintenance_work_mem`, which is
1GB here for HNSW builds — so each of several autovacuum workers could claim
that, on a container sized for 4GB total. A test asserts it is not `-1`.

The Postgres volume mounts at `/var/lib/postgresql`, not `.../data`. The 18+
images expect this: the cluster lives in a version-named subdirectory so a
future major upgrade can use `pg_upgrade --link` without straddling a mount
boundary.

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
make check     # gofmt, go vet, go test -race
make build     # binaries into bin/
make test-db   # optional: reuse the running Postgres instead of a container
```

`go test ./...` needs no setup. Two different tools, for two different reasons:

**NATS runs in-process.** `nats-server` is a Go library, so tests get the real
protocol in milliseconds with no Docker. A container here would be strictly
worse — slower, and no more faithful.

**Postgres runs in a container**, started by `testcontainers` from the same
pgvector image the deployment uses, with `deploy/postgres/postgresql.conf`
mounted. Mocking a store proves nothing about the SQL, which is the part that
actually breaks; and mounting the real config means a typo in our tuning fails
the suite rather than surfacing later as mysterious production behaviour.

Set `AZIR_TEST_DATABASE_URL` to point at an existing database instead — faster
for a repeated local loop. Tuning assertions skip in that mode, since an
externally supplied database has whatever configuration its operator gave it.

The suite previously skipped store tests when no database was configured, which
was a mistake worth naming: a skipped test looks exactly like a passing one.

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

One fixture earns special mention. `pkg/plugin/adversarial_test.go` is a
deliberately hostile plugin that attempts every route a careless or malicious
author might use to get a credential out: returning it, burying it in a nested
structure, embedding it in prose, using it as an object key, wrapping it in an
error, and reaching for another plugin's. It exists because the credential
firewall was otherwise tested one component at a time, and a firewall is only
meaningful end to end.

It earned its place immediately by finding two real holes: the redactor walked
map values but never map keys, so a secret used as an object key escaped
whole; and `plugin.Errorf` messages went to the caller unredacted, because the
contract said they were caller-safe and nothing enforced it.

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

### Configuring a plugin

A plugin publishes a JSON Schema for its own settings, and the console renders
the form from it — there is no per-integration frontend code. Fields marked
`x-azir-secret` are sealed into the vault; everything else lands in
`plugin_config`. An administrator types a subdomain and an API token into the
same panel without needing to know they are stored entirely differently.

Handlers read both back at request time, so a rotated key or a changed
subdomain takes effect without restarting anything:

```go
cfg, _ := plugin.ConfigFrom(ctx)
subdomain, err := cfg.String(ctx, req.CustomerID, "subdomain")

v, _ := plugin.VaultFrom(ctx)
token, err := v.For(ctx, "", "api_key")

// Azir customer id -> this plugin's identifier, through the spine.
ident, _ := plugin.IdentityFrom(ctx)
external, err := ident.External(ctx, req.CustomerID)
```

That last one is why handlers receive an opaque Azir customer id rather than a
vendor one: memory and context hang off the spine, so switching PSA later does
not orphan them.

### Syncro permissions

Azir needs exactly three: `ticket.read`, `customer.read`, `asset.read`. Nothing
else — it never writes, never deletes, and never executes scripts.

`syncro.access.check` reports what a configured token actually grants and names
anything beyond that set, so least privilege is verifiable from inside Azir
rather than by squinting at checkboxes in another product. Run against a full
admin token it reports 21 excessive grants, including `script.execute`, which
would let a compromised Azir run code on customer machines.

### On MCP

Syncro publishes an MCP server, and the temptation is to wire MCP into core.
That would be a mistake: every guarantee Azir makes — the read-only invariant,
the credential firewall, outbound redaction, capability tags — is enforced in
`pkg/plugin`. An MCP server is a third-party tool surface that will have write
tools, returns content we do not shape, and holds its own credentials.

The right shape is an MCP *bridge plugin*, which inherits those guarantees:
refusing to register any MCP tool that advertises mutation, drawing server
credentials from the vault, and landing its tools as pending capabilities.

Worth being clear that a bridge is strictly worse than a native plugin where
one exists. This plugin trims responses, paces to Syncro's documented limit and
curates its tool surface; their MCP would give us their shapes and their
verbosity. MCP earns its place on the long tail — systems that will never
justify a plugin of their own.

### Capability tags

Tools declare what they provide from a vocabulary Azir owns
(`pkg/plugin/capability.go`). Core never asks whether a plugin "is a PSA"; it
asks whether anything provides `customers.list`. Features declare the
capabilities they need and report themselves unavailable, with a reason, when
nothing supplies them — so a partial integration is still useful.

The vocabulary is deliberately small. Tags are added when a real integration
shows genuine overlap, never in anticipation of one.
