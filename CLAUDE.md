# CLAUDE.md

Azir is a helpdesk copilot for MSPs: one Go binary embedding NATS, the HTTP
API, the React frontend and a supervisor for plugin child processes, with
Postgres and SearXNG beside it. It holds System Owner credentials for customer
phone systems, so the credential handling below is the part to be careful with.

Design rationale lives in `docs/ARCHITECTURE.md`. This file is what you need to
change code here without breaking something quietly.

## Commands

```sh
make check      # gofmt -w, go vet, go test -race    <- run before calling work done
make build      # binaries into bin/
make up         # build and start the stack
make smoke      # fifteen checks against a running stack
make test-db    # print an AZIR_TEST_DATABASE_URL for a faster local loop
```

`helpdesk.dreulavelle.com` is **the** instance: a development deployment, not a
production one, and there is no second environment beside it. Changes are built
and deployed there with `make up`, and that is where they are looked at and
demonstrated from. Do not stand up a parallel dev server.

`go test ./...` needs no setup. Frontend: `cd web && npm run typecheck && npm run build`.

Go 1.26. Format with `gofmt`; `go vet` and `staticcheck` must be clean.

## Layout

```
cmd/azir-core/        the application
cmd/plugin-*/         bundled plugins (3cx, syncro, searxng, echo)
internal/api/         HTTP surface, routes, tool invocation
internal/assistant/   chat loop, tool offers, proposals
internal/identity/    actors, permissions, argon2id, session tokens
internal/store/       Postgres: spine, credentials, capabilities, jobs
internal/vault/       envelope encryption
internal/logging/     the redacting slog handler
internal/registry/    discovery and the capability index
internal/scheduler/   deferred work; JetStream holds the timers
pkg/plugin/           the SDK — public, importable from another repo
web/                  React + TypeScript, embedded into the binary
```

## Invariants

Break one of these and the failure is usually silent. Each is enforced in code;
keep it that way rather than adding a rule someone has to remember.

**Credentials never reach a model or a log.** `store.Credentials.Open` is the
only place plaintext is produced, and it registers every secret with the log
redactor itself. Do not add a second unsealing path, and do not push
registration out to callers — that was the bug: the doc comment asked callers
to do it and not one of them did.

**One list of sensitive field names.** `plugin.IsSensitiveKey` in
`pkg/plugin/redact.go`. `internal/logging` consults it and adds only its own
`dek`/`master_key`. These were two lists once and drifted, and the drift meant
a plugin's stdout was less protected than its tool output.

**Sealed credentials are bound to their scope.** `vault.Seal`/`Open` take AAD;
`store.credentialAAD` builds it from (plugin, kind, customer). A ciphertext
moved to another row does not open. If you add a caller, pass real AAD — `nil`
compiles fine and silently gives up the property.

**Every route names its permission.** `s.require(perm, handler)` in
`internal/api/api.go`. No handler does its own check; no route mutates on GET.

**Forwarding headers are only believed from a named proxy.** `ProxyTrust` in
`internal/api/proxy.go`, configured by `AZIR_TRUSTED_PROXIES`. Use
`s.Proxies.clientIP(r)` / `s.Proxies.overTLS(r)`, never the raw header — the
sign-in throttle keys on that address.

**A tool declaring `Mutates` must name a `RequiresPermission`,** or the plugin
refuses to start. Capability tags must come from the vocabulary in
`pkg/plugin/capability.go`; an unknown tag is refused at startup.

**Handlers return `plugin.Errorf`.** Any other error reaches the caller as a
generic failure, because wrapped vendor errors carry request URLs and auth
headers.

## Testing

Real dependencies, because the parts that break are the real ones.

- **Postgres runs in a container** via testcontainers, from the shipped
  pgvector image with `deploy/postgres/postgresql.conf` mounted — so a typo in
  our tuning fails the suite. Set `AZIR_TEST_DATABASE_URL` to reuse a database
  instead; tuning assertions skip in that mode.
- **NATS runs in-process.** `nats-server` is a library; a container would be
  slower and no more faithful.
- Never skip a test for missing infrastructure. A skipped test looks exactly
  like a passing one — the suite did that once.

Two fixtures carry most of the weight, and new security work belongs beside
them:

- `internal/logging/canary_test.go` and the canary tests in
  `internal/store/store_test.go` push a sentinel credential through every route
  that could leak it. CI greps the built binaries so a sentinel cannot ship.
- `pkg/plugin/adversarial_test.go` is a deliberately hostile plugin trying
  every way out: returning a secret, nesting it, hiding it in prose, using it
  as an object key, wrapping it in an error, reaching for another plugin's. It
  found two real holes the first time it ran.

**A test that cannot fail is worse than no test.** When you add one for a
security property, confirm it fails with the fix removed. `TestCanaryDetectorActuallyDetects`
exists for exactly this reason, and the audit that produced
the current redaction wiring found a canary suite that registered its own
sentinel and therefore proved a property production did not have.

## Migrations

`internal/store/migrations/NNNN_name.sql`, embedded, applied in order under an
advisory lock. Every file is checksummed: **editing an applied migration is a
fatal error**, so write a new one. A database carrying a migration this binary
does not know is refused. `CREATE INDEX CONCURRENTLY` opts out of its
transaction with `-- azir:no-transaction` and must then be idempotent.

## Two lists that must agree

The recurring bug shape here: two lists, often in two languages, that must stay
in step with nothing checking. Every instance failed silently.

Capability constants against their vocabulary; what the 3CX plugin publishes
against what `internal/bulk` reads; BLF key kinds against their numbers; fields
marked unique against fields that exist; Syncro's permission map against its
tool list; sensitive key names across the two redactors.

**When you add the second list, add the test in the same commit.**

## Conventions

- Comments explain *why*, in prose, and often name the bug that caused the
  code to look the way it does. Match that. Do not add comments that restate
  the line beneath them.
- Errors are handled intentionally; no goroutine without clear ownership and
  shutdown.
- Interfaces are small and defined next to their consumer.
- Conventional commits — release-please cuts versions from them. Branches are
  `type/short-description`.
- `web/dist/.gitkeep` is committed on purpose: `web/embed.go` embeds
  `all:dist`, so without it the whole module fails to compile. It went missing
  once and every CI run failed twelve seconds in, before a single test ran.

## Gotchas

- Postgres publishes on **5433** to avoid colliding with a host instance.
- Azir binds **loopback** by default. `AZIR_BIND` takes one interface address,
  never `0.0.0.0` — this API is the front door to a credential vault.
- Lose `AZIR_MASTER_KEY` and the vault cannot be read. There is deliberately no
  default.
- Back up two things: the Postgres volume and `/var/lib/azir` (JetStream).
