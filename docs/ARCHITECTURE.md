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

Two supports under that, both added after an audit found the rule was narrower
in practice than on paper. Unsealing a credential registers it with the log
redactor at the one place plaintext is produced — `store.Credentials.Open` —
rather than asking each caller to remember, which is what the code used to do
and what none of its callers did. And core's log handler and the SDK's payload
redactor now consult one list of sensitive field names, because they were two
lists and had drifted: the telephony names covered a plugin's tool output while
the same value in the same plugin's log line went out unredacted.

Sealed credentials are also bound to the scope they were stored for. A
ciphertext carries its (plugin, kind, customer) as additional authenticated
data, so a secret copied into another customer's row does not open — see
**The vault** below.

**Reads are always allowed. Writes are never unattended.** Azir began
read-only, and the SDK refused to register a mutating tool at all. What
replaced that refusal is narrower rather than looser: a tool declaring
`Mutates` must name the permission it requires, or the plugin does not start —
a write nobody has to be allowed to make is not a write anyone should make.
Beyond that, every write has a person in it. A technician acts on a screen, or
the assistant proposes and a technician approves the change itself. Bulk edits
are compared against the live system and shown as a before-and-after; what is
approved is the difference, never the intention.

A change with a time on it is the one case where "a person is in it" needs
spelling out. Arming the job is the approval: someone with the permission said
do this, and named the moment. Nothing asks again when it fires, because nobody
is there to ask. What *is* re-checked at that moment is every standing gate —
the account still exists and is enabled, the role still carries the permission,
writes are still on for the plugin, and the tool is still approved — so
withdrawing any of them stops work armed before anyone thought to worry. See
`internal/scheduler`.

**Discovery proposes; an administrator approves.** A plugin appearing in `$SRV`
becomes a candidate capability, not a granted one — it lands as `pending` and
is unusable until someone activates it. Without this, anyone able to start a
container could extend what Azir can do. Rediscovery never overwrites a
decision, so restarting a plugin cannot launder a rejection back into pending.

## Capability tags

Tools declare what they provide from a vocabulary Azir owns
(`pkg/plugin/capability.go` — 43 tags today). Core never asks whether a plugin
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

## The vault

Envelope encryption. Each secret is sealed with a freshly generated data key,
and that data key is wrapped with a versioned master key. Rotation re-wraps
data keys and never touches ciphertext, so it is cheap and the plaintext never
materialises; a compromised data key exposes exactly one secret.

Each ciphertext is additionally bound to the scope it was stored for — its
(plugin, kind, customer) — as AES-GCM additional authenticated data. Without
that binding a sealed secret is a portable blob, and write access to the
database is enough to copy one customer's PBX password into another customer's
row: Azir would open it and connect with it, because every field involved is
one the attacker just wrote. With it, a moved ciphertext fails to authenticate,
and producing one that would succeed needs the master key — which is precisely
what an attacker holding only a database dump does not have.

The binding is on the payload, not on the wrapped data key, so rotation still
needs to know nothing about scope.

Keys arrive by environment variable rather than by file, so a Docker secret or
a platform secret store can supply them without landing on disk. There is no
default master key: a default would be a working encryption key published in
this repository, and every deployment that never changed it would be holding
customer credentials sealed with a key anyone can read.

## Who a request is from

X-Forwarded-For and X-Forwarded-Proto are ordinary request headers, so they are
believed only when the connection carrying them came from an address named in
`AZIR_TRUSTED_PROXIES`. Unset — the default — believes neither, which is right
for a deployment reached directly on loopback.

This is not only about audit accuracy, though writing an attacker's chosen
address into their own audit trail is bad enough. Sign-in is rate limited per
source address, and a limit keyed on a forgeable value is not a limit. The two
had to be fixed together.

Behind a trusted proxy the client is the rightmost hop in the forwarded list
that is not itself a trusted proxy. Rightmost rather than leftmost: a client
that sends its own X-Forwarded-For has that value preserved at the front by
every proxy it passes through, so reading from the left returns whatever it
chose to claim.

## Rate limiting sign-in

Verifying a password costs 64 MiB by design — argon2id is deliberately
expensive, which is what makes a stolen hash hard to crack. That makes
unauthenticated sign-in attempts a memory-exhaustion lever as much as a
guessing one, since the cost is paid before the password is known to be wrong.
The limit is therefore a resource control and a credential control at once, and
it is generous per source: what it stops is the thousandth attempt, not the
fourth.

First-run setup is limited too, and serialises on a Postgres advisory lock.
Checking that no account exists and then creating one are two statements with a
gap between them, and two requests could both pass through it — leaving a
deployment with two administrators, one of whom nobody chose, during the exact
window when the route answers to anyone.

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

Three rules that are enforced rather than documented:

- **Return `plugin.Errorf` for failures.** Any other error is logged locally
  and reported to the caller as a generic failure, because wrapped vendor
  errors routinely carry request URLs and auth headers.
- **Register every resolved credential via `WithSecrets`.** The redactor scrubs
  those literals wherever they appear, including inside prose that key-name
  rules would never inspect.
- **A tool declaring `Mutates` must name a `RequiresPermission`.** The plugin
  does not start otherwise. A write nobody has to be allowed to make is not a
  write anyone should make.

Capability tags come from the vocabulary in `pkg/plugin/capability.go`. An
unknown tag is refused at startup — letting plugins invent tags freely is how
the mechanism stops meaning anything.

### Settings and credentials

A plugin publishes a JSON Schema for its own settings and the console renders
the form from it, so there is no per-integration frontend code. Fields marked
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

### Publishing what a system will accept

Where a plugin can change fields on something, it should publish the field list
as data rather than have Azir keep its own copy: name, label, kind, group, and
what each will accept. Azir builds the spreadsheet columns, the form controls,
the comparison and the write-side allowlist from that one list.

The 3CX extension editor works this way — fifty-two fields across six tabs, and
Azir knows none of them by name. Keeping a second copy is how a sheet ended up
carrying two fields out of thirty-two while everything looked correct.

| flag | meaning |
|---|---|
| `kind: secret` | write-only. Never read back, never staged, never compared. |
| `kind: readonly` | worth showing, not something Azir will change. |
| `unique` | no two records may share a value. A bulk edit setting one value across several is refused before anything is written. |
| `labels` | what to show for each choice, where the stored value is not something to put in front of a person. |

### Freshness, not mirroring

Azir caches tool results; it does not mirror the vendor. Nothing is stored that
nobody asked for, and the vendor stays the source of truth.

Each tool declares a two-level staleness budget, because only the plugin knows
how volatile its own data is — a ticket changes while you are reading it, a
customer's phone number does not:

| tool | serve instantly | must refresh |
|---|---|---|
| `tickets.get`, `tickets.timeline` | 30s | 2m |
| `tickets.search` | 60s | 5m |
| `time.entries` | 5m | 30m |
| `invoices.list`, `customers.standing` | 10m | 1h |
| `customers.*` | 30m | 4h |
| `assets.list` | 1h | 12h |
| `docs.search` | 6h | 24h |
| `access.check` | never cached | |

Below the first threshold a cached answer is returned as-is. Between the two it
is still returned immediately while a refresh runs behind it, so this caller is
fast and the next is current. Beyond the second, the caller waits. Concurrent
refreshes of the same entry collapse into one, because twenty callers arriving
at an expired entry must not become twenty vendor requests.

Two things make this honest rather than merely fast. Every response carries
`X-Azir-Source` and `X-Azir-Age-Seconds`, so a reader — person or model — knows
whether a figure is live or four minutes old. And `{"refresh": true}` bypasses
the cache entirely, which is what the UI sends when a technician opens a ticket
they are about to act on.

When the vendor is unreachable, a stale entry is served with
`X-Azir-Source: cache-stale-vendor-unavailable` rather than an error. Something
old and labelled beats nothing.

A write invalidates the whole plugin's cache. Anything narrower is a guess
about what a change touched, and a wrong guess shows somebody stale data
immediately after they changed it.

### Syncro permissions

Azir needs exactly three: `ticket.read`, `customer.read`, `asset.read`. Nothing
else — it never deletes, and never executes scripts.

`syncro.access.check` reports what a configured token actually grants and names
anything beyond that set, so least privilege is verifiable from inside Azir
rather than by squinting at checkboxes in another product. Run against a full
admin token it reports 21 excessive grants, including `script.execute`, which
would let a compromised Azir run code on customer machines.

## Deployment and configuration

| Variable | Default | Purpose |
|---|---|---|
| `AZIR_MASTER_KEY` | — | base64 32-byte key sealing the vault. Required. |
| `AZIR_MASTER_KEYS` | — | `1:<b64>,2:<b64>` when more than one key version is loaded |
| `AZIR_KEY_VERSION` | — | which key version new secrets are sealed with |
| `DATABASE_URL` | — | Postgres connection string. Required. |
| `AZIR_DATA_DIR` | `/var/lib/azir` | JetStream storage |
| `AZIR_HTTP_ADDR` | `:8080` | API and UI listener |
| `AZIR_TRUSTED_PROXIES` | none | addresses or CIDR blocks whose forwarding headers are believed |
| `NATS_URL` | embedded | set to use an external NATS instead |
| `AZIR_NATS_HOST` | loopback | the address the embedded server binds |
| `AZIR_PLUGIN_DIR` | `/usr/local/lib/azir/plugins` | bundled plugins to supervise |
| `AZIR_LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` |

Compose reads a few more from `.env`: `AZIR_BIND`, `AZIR_PORT`, `AZIR_PG_PORT`,
`POSTGRES_PASSWORD`, `SEARXNG_SECRET`, and the `AZIR_*_CONTAINER_NAME`
overrides for running a second instance beside the first.

Azir publishes on **loopback** by default, not on every interface. Docker's
default would publish on the LAN address, every container bridge and IPv6 — on
the host this was noticed on, fourteen addresses — and this API is the front
door to a credential vault. A tunnel or proxy running elsewhere needs a
reachable address: set `AZIR_BIND` to the one interface it arrives on, never
back to `0.0.0.0`, and firewall that port to the proxy's address.

Postgres publishes on **5433** so it does not collide with one already running
on the host; override with `AZIR_PG_PORT`. Two things need backing up: the
Postgres volume, and `/var/lib/azir` for JetStream.

## Postgres tuning

`deploy/postgres/postgresql.conf` targets PostgreSQL 18 and is a commented,
checked-in config rather than an autotuner — a generated config makes behaviour
depend on the machine a container landed on, which turns "the query got slow"
into archaeology. The baseline assumes ~4GB for the container; scale the memory
settings with the limit.

`jit = off` is deliberate: JIT regularly costs more than it saves on short
pgvector queries and is a known source of latency spikes.
`effective_io_concurrency` now counts I/Os the executor keeps in flight rather
than being a device-parallelism hint, so the pre-18 advice to set it in the
hundreds no longer applies.

### On asynchronous I/O

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

### Autovacuum

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

### The volume path

The Postgres volume mounts at `/var/lib/postgresql`, not `.../data`. The 18+
images expect this: the cluster lives in a version-named subdirectory so a
future major upgrade can use `pg_upgrade --link` without straddling a mount
boundary.

## Releases

Versioning runs on [release-please](https://github.com/googleapis/release-please)
from the conventional commits this repository already writes. A pull request
stays open with the next version and the changelog it would produce; merging it
is the release, and only then is a container image built and pushed to GHCR.

Every CI job carries a `timeout-minutes`. A job with no timeout is one that can
run to GitHub's 24-hour ceiling, and a handful of those will empty a month's
minutes without anyone noticing.

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
internal/scheduler/   deferred work: JetStream holds the timers
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
