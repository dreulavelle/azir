# Operations

## Running the stack

```sh
make init          # generate any missing secrets into .env, never rewriting one
make up            # build and start
make smoke         # sixteen checks against the running stack
make psql          # a session against the running database
make logs
make down
make down-hard     # also delete the volumes, returning it to first run
```

`compose.yaml` and `.env` are both at the repository root, so plain
`docker compose up -d` works without flags.

`make init` is safe to re-run: it only adds variables that are missing, so a
new service added later picks up its secret on the next `make up` rather than
failing to start on a variable that did not exist when the `.env` was written.

**Two things to back up:** the Postgres volume, and `/var/lib/azir` for
JetStream.

Postgres publishes on **5433** by default so it does not collide with one
already running on the host. Override with `AZIR_PG_PORT`.

## What it listens on

Azir publishes on **loopback** by default, not on every interface. Docker's
default would publish on the LAN address, every container bridge and IPv6 — on
the host this was noticed on, fourteen addresses — and this API is the front
door to a credential vault.

Loopback is right for anything terminating TLS on the same machine. A tunnel or
proxy running elsewhere on the network needs a reachable address: set
`AZIR_BIND` to the one interface it arrives on, never back to `0.0.0.0`, and
firewall that port to the proxy's address.

## Configuration

| Variable | Default | Purpose |
|---|---|---|
| `AZIR_MASTER_KEY` | — | base64 32-byte key sealing the vault. Required. |
| `AZIR_MASTER_KEYS` | — | `1:<b64>,2:<b64>` when more than one key version is loaded |
| `AZIR_KEY_VERSION` | — | which key version new secrets are sealed with |
| `DATABASE_URL` | — | Postgres connection string. Required. |
| `AZIR_DATA_DIR` | `/var/lib/azir` | JetStream storage |
| `AZIR_HTTP_ADDR` | `:8080` | API and UI listener |
| `NATS_URL` | embedded | set to use an external NATS instead |
| `AZIR_NATS_HOST` | loopback | the address the embedded server binds |
| `AZIR_PLUGIN_DIR` | `/usr/local/lib/azir/plugins` | bundled plugins to supervise |
| `AZIR_LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` |

Compose reads a few more from `.env`: `AZIR_BIND`, `AZIR_PORT`,
`AZIR_PG_PORT`, `POSTGRES_PASSWORD`, `SEARXNG_SECRET`, and the
`AZIR_*_CONTAINER_NAME` overrides for running a second instance beside the
first.

Lose `AZIR_MASTER_KEY` and the vault cannot be read. There is deliberately no
default: a default would be a working encryption key published in the
repository, and every deployment that never changed it would be holding
customer credentials sealed with a key anyone can read.

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
