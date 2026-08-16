# Azir

A read-only AI troubleshooting assistant for MSP work, and a platform for the
systems that work touches. Syncro and 3CX are the first two plugins, not the
product.

The job Azir replaces is opening ChatGPT, pasting in a ticket thread,
explaining the customer's setup, and asking for help. It does that job by
already knowing all of it.

## Status

**Phase 0 — skeleton.** No business logic. The plumbing is real and proven:
plugins register themselves with NATS service discovery, core finds them, and
tool calls round-trip. Everything else is scaffolding waiting for phase 1.

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
becomes a candidate capability, not a granted one. (The approval gate lands in
phase 1; today discovery is direct.)

## Layout

```
cmd/azir-core/      hub: discovery, HTTP, tool round-trip
cmd/plugin-echo/    diagnostic plugin proving the transport
internal/registry/  service discovery and the capability index
pkg/plugin/         the SDK — importable from outside this module
web/                React + TypeScript frontend
deploy/             Docker Compose stack
```

`pkg/` rather than `internal/` for the SDK is deliberate: it is the one package
that must be importable from a plugin living in another repository.

## Running it

```sh
make up      # build and start nats, postgres, core, plugin-echo, web
make smoke   # verify discovery, transport and redaction
make down
```

Then open <http://localhost:5173>.

Postgres publishes on **5433** by default so it does not collide with a
Postgres already running on the host. Override with `AZIR_PG_PORT`.

## Development

```sh
make check   # gofmt, go vet, go test -race
make build   # binaries into bin/
```

Tests run an in-process NATS server, so the discovery round-trip is verified
against the real protocol without Docker.

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
