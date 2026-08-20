# Azir

**The helpdesk copilot that already knows your customers.**

A ticket comes in: *phones are down at the Ellis office.* Answering it takes
four tabs. The PSA, for the ticket and what happened last time. The PBX, to see
whether anything is actually registered. The documentation, for what was
installed. And somebody's memory, for who to call over there.

Azir answers it in one place, because it is already connected to all four.

---

## The tab you cannot audit

A technician stuck on that ticket opens ChatGPT. They paste the thread, explain
the customer's setup, describe the phone system, ask for help. Then they think
about which parts they should not have pasted.

That paste is the problem. It is your customer's data, sitting in somebody
else's model, with no record that it happened and no way to take it back. Most
MSPs have already decided this is not allowed, and most of them know it happens
anyway.

Azir does the same job without the paste. It reads the ticket because it is
connected to your PSA. It knows the phone system because it holds the
credentials. Nothing is copied out to anyone, and every question and change is
on an audit trail you own.

## What your technicians get

**Answers that cross systems.** A ticket with its full history, the customer
and who to actually call, their extensions and handsets, recent call quality,
where their invoices stand. One question, four systems, one answer.

**An extension screen built for a thousand extensions.** Every 3CX setting
across six tabs, search and range selection, the desk-phone key layout with
copy between phones, ring groups, office hours and holidays. Bulk edits are
staged against the live system and shown as a before-and-after, so the
technician approves the difference rather than the intention.

**Changes with a time on them.** Move a customer to their holiday greeting at
6pm on the 24th. Arming the job is the approval, so nobody has to be at a
keyboard when it fires, and every standing permission is re-checked at that
moment. Withdrawing any of them stops work armed before anyone thought to worry
about it.

**Support bundles read for you.** Point Azir at a customer's 3CX and it pulls
the capture itself, reads the whole thing, and says what is wrong. Nobody
downloads a zip.

**Memory across the whole history.** "Didn't we see this at Ellis last spring?"
becomes a question with an answer, over past tickets and past conversations.

## Why you can point it at customer data

Four properties, each enforced by the software rather than promised by a prompt.

**No credential ever reaches the model.** Not scrubbed from the output; never
in the input. Credentials are resolved server-side, inside the integration, and
never enter the conversation at all. A deliberately hostile test integration
tries every route out — returning a secret, burying it in a structure, hiding
it in prose, wrapping it in an error — and a sentinel value is asserted absent
from every one.

**Nothing writes without a person.** Either a technician acts on a screen, or
the assistant proposes a change and a technician approves that exact change.
There is no configuration in which the model writes to a customer's system on
its own.

**Nothing is capable until you say so.** A new integration arrives as a
candidate, not a capability. It can do nothing until an administrator turns it
on, tool by tool, and restarting it cannot launder a rejection into a fresh
request.

**Every technician has a role.** Commenting on a ticket and reconfiguring a
customer's phone system are different levels of trust, and Azir lets you say
so. Refusals are recorded alongside actions, so an attempt to reach something
out of reach is visible too.

## What stays yours

Azir is self-hosted and single-tenant. Your Postgres, your machine, your
backups. There is no vendor account, no per-seat billing, and no copy of your
customers' data anywhere you did not put it.

Web search runs through your own SearXNG, so looking up a vendor advisory does
not tell a search provider which of your customers is having trouble. Sign-in
goes through your identity provider if you have one, with a local administrator
that still works when the provider is the thing that is broken. The login
screen carries your name and your logo, because your technicians should not
have to look at ours.

## What it connects to

| | |
|---|---|
| **Syncro** | tickets, timelines, customers, contacts, assets, time entries, invoices |
| **3CX** | system health, extensions, handsets, call history and quality, logs, support bundles, ring groups, schedules |
| **SearXNG** | web search that stays on your infrastructure |

Azir asks what a system *provides*, not who makes it, so a second PSA or a
different phone system is an integration rather than a rewrite. Integrations
are ordinary programs: drop one in and it is picked up on the next start, with
nothing to register and nothing to rebuild. It still has to be approved before
it can do anything.

## Try it

```sh
make init          # generate the secrets it needs
make up            # build and start
```

Then open <http://localhost:8080>. The API and the interface are on the same
port. `make smoke` runs fifteen checks against the running stack, including one
that a sentinel credential never reaches the logs.

## For engineers

[docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) covers how it is put together and
why: the rules that shaped it, the credential vault, configuration, writing an
integration, and the Postgres tuning. [CLAUDE.md](CLAUDE.md) is the working
guide for this repository.
