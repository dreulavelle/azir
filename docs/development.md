# Development

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

## The frontend placeholder

`web/dist/.gitkeep` is committed on purpose. `web/embed.go` embeds `all:dist`,
so without a `dist` directory the entire module fails to compile — not "the
frontend is missing", but `go build ./...` fails. `npm run build` recreates the
placeholder after vite empties the directory.

This is not hypothetical. The file went missing once, and every CI run for the
following month failed twelve seconds in, before gofmt, before vet, and before
a single test — so none of them ran at all.

## Migrations

The migrator is hand-rolled but not naive. Each migration runs in its own
transaction under a Postgres advisory lock, so concurrent replica starts
serialise rather than race. Every file is checksummed: editing an applied
migration is a fatal error, not a silent no-op, because that is precisely how
environments diverge. A database carrying a migration this binary does not know
about is also refused, so an accidental rollback cannot run against a future
schema. A migration needing `CREATE INDEX CONCURRENTLY` opts out of its
transaction with an `-- azir:no-transaction` marker, and must then be written
idempotently.

## The tests that matter

One fixture earns special mention. `pkg/plugin/adversarial_test.go` is a
deliberately hostile plugin that attempts every route a careless or malicious
author might use to get a credential out: returning it, burying it in a nested
structure, embedding it in prose, using it as an object key, wrapping it in an
error, and reaching for another plugin's. It exists because the credential
firewall was otherwise tested one component at a time, and a firewall is only
meaningful end to end.

It earned its place immediately by finding two real holes: the redactor walked
map values but never map keys, so a secret used as an object key escaped whole;
and `plugin.Errorf` messages went to the caller unredacted, because the
contract said they were caller-safe and nothing enforced it.

The canary tests are the ones that matter. A sentinel credential is pushed
through every route that could leak it — messages, attributes, errors, groups,
derived loggers, the database — and asserted absent. One test deliberately
proves the *detector* works, so a green suite means redaction ran rather than
that the check was vacuous. CI additionally greps the built binaries, so a
sentinel cannot ship compiled in.

## Two lists that must agree

A recurring bug shape in this codebase, now bound by a test everywhere it
appears: two lists, often in two languages, that must stay in step with nothing
checking. Each instance failed silently rather than loudly.

- Capability constants against the vocabulary they must belong to — a tag
  declared but not listed crash-looped the plugin at startup.
- What the 3CX plugin publishes against what `internal/bulk` reads — the names
  drifted, every option arrived with an empty identity, and nothing failed. The
  sheet just quietly carried two fields again.
- BLF key kinds against the numbers written beside them — a kind offered but
  not numbered would be written as a different key entirely.
- The fields marked unique against the fields that exist.
- Syncro's permission map against its tool list.

When you add the second list, add the test in the same commit.
