# Azir

**The helpdesk copilot that already knows your customers.**

Your technician opens a ticket that says "phones are down at the Ellis office."
Answering it means four tabs: the PSA for the ticket and its history, the PBX
for whether anything is actually registered, the documentation for what was
installed, and someone's memory for who to call. Azir answers it in one place,
because it is already connected to all four.

---

## The job it replaces

Open ChatGPT. Paste the ticket thread. Explain the customer's setup, their
phone system, what was tried last time. Ask for help. Redact the bits you
should not have pasted — after you have already pasted them.

Azir does that job by knowing all of it already, and by never sending the parts
that should not leave.

## What it does today

Working, on real tickets, against real customer systems.

**Answers questions across systems.** A ticket and its full history, the
customer and who to actually call, their extensions and handsets, call quality,
what their invoices look like. One question, several systems, one answer.

**Reads a 3CX support bundle without anyone unzipping it.** Point it at a
customer's phone system and it pulls the capture itself, reads the whole thing,
and tells you what is wrong with it.

**Manages extensions properly.** The screen a 3CX technician actually needs:
every setting across six tabs, search and range selection, the desk-phone key
layout with copy between phones, and bulk edits that show you a
before-and-after before anything is written. Built for the customers with a
thousand extensions, not the ones with six.

**Remembers.** Semantic recall over tickets and past conversations, so "didn't
we see this at Ellis last spring?" is a question with an answer.

## Why it is safe to point at customer data

Three properties, each enforced by the system rather than promised by a prompt.

**No credential reaches a model.** Not redacted from the output — never in the
input. Credentials are resolved inside the plugin, server-side, and every
value a handler returns passes a redactor before it reaches the wire. A
deliberately hostile test plugin tries every route out — returning a secret,
burying it in a nested structure, hiding it in prose, using it as an object
key, wrapping it in an error — and a sentinel is asserted absent from every
one. It found two real holes the first time it ran.

**Reads are free; writes are not.** Every write names the permission it
requires, or the plugin does not start. Nothing writes without a person: either
a technician acts on a screen, or the assistant proposes and a technician
approves the exact change. Bulk edits are staged and diffed first — you approve
a before-and-after, not an intention.

**Nothing is capable until someone says so.** A plugin appearing on the bus
becomes a *candidate*, not a granted capability: it lands as pending and can do
nothing until an administrator activates it. Restarting a plugin cannot launder
a rejection back into pending.

The data stays yours in the ordinary sense too: self-hosted, single
organisation, your own Postgres. Web search runs through your own SearXNG, so
looking up a vendor advisory does not tell a search provider what your
customers are having trouble with.

## Run it

```sh
make init          # generate the secrets it needs, into .env
make up            # build and start the stack
```

Then open <http://localhost:8080>. API and UI on the same port.

`docker compose up -d` works on its own too — `compose.yaml` and `.env` are
both at the root.

```sh
make smoke         # sixteen checks against the running stack
make logs
make down
```

## What is in the box

Azir is **one binary**: it embeds a NATS server with JetStream, the HTTP API,
the built frontend, and a supervisor running the bundled plugins as child
processes. Beside it run **Postgres** — because semantic recall needs a real
vector index — and **SearXNG**, for private web search.

Four integrations ship with it:

| | |
|---|---|
| **Syncro** | tickets, timelines, customers, contacts, assets, time entries, invoices |
| **3CX** | system health, extensions, handsets, call history and quality, logs, support bundles, and the full extension editor |
| **SearXNG** | web search that stays on your infrastructure |
| **echo** | a diagnostic plugin that proves the transport and the credential firewall |

Plugins are ordinary processes speaking NATS, so a third-party one runs as its
own container against the same bus. Drop an executable named `plugin-<name>`
into `plugins/` and it is supervised on the next start — nothing to register,
nothing to rebuild. It still has to be approved before it can do anything.

## Documentation

| | |
|---|---|
| [Architecture](docs/architecture.md) | how it is put together, and the three rules that shaped it |
| [Operations](docs/operations.md) | configuration, Postgres tuning, backups |
| [Writing a plugin](docs/plugins.md) | the SDK, credentials, caching, permissions |
| [Development](docs/development.md) | tests, migrations, and the canary suite |
