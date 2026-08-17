import { useCallback, useEffect, useState } from "react";
import {
  chat,
  NotProvided,
  work,
  type CustomerRecord,
  type Ticket,
  type Timeline,
  type TimelineEntry,
} from "../api";
import { canGoBack, type Route } from "../router";
import { useLiveChanges } from "../live";
import { cn } from "@/lib/cn";
import { Chip, Empty, Icon, Label, Loading, Panel, PanelHead, Problem, absolute, ago, duration, isDone, prioritySignal, since, statusTone } from "../ui";

/**
 * One ticket, as a history rather than as a form.
 *
 * A PSA shows fields and a comment list. What a technician picking up someone
 * else's ticket actually needs is the shape of what happened: how long the
 * customer waited, where it stalled, how many times it went back and forth.
 * Azir computes those, so they lead rather than being left as an exercise.
 *
 * The page is ordered by the questions asked in the order they get asked. What
 * is this and who is it for, at the top. How is it doing, in the strip beneath.
 * What did they actually ask for, pinned so a long thread can never fold it
 * away. Then the conversation, and only then the reference material.
 */

/** A silence worth drawing rather than leaving the reader to subtract dates. */
const GAP_HOURS = 24;

/** Whether an entry came from the customer's side of the conversation. */
function fromCustomer(entry: TimelineEntry): boolean {
  return /customer|client|inbound/.test(entry.kind.toLowerCase());
}

export function TicketDetail({
  id,
  go,
  ask,
}: {
  id: string;
  go: (to: Route) => void;
  /** Hands a question to the assistant panel and opens it. */
  ask: (question: string) => void;
}) {
  const [timeline, setTimeline] = useState<Timeline | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [missing, setMissing] = useState(false);
  const [busy, setBusy] = useState(false);

  async function load(refresh = false) {
    setBusy(true);
    try {
      const answer = await work.timeline(Number(id), refresh);
      setTimeline(answer.data);
      setError(null);
      setMissing(false);
    } catch (e) {
      if (e instanceof NotProvided) setMissing(true);
      else setError(e instanceof Error ? e.message : "This ticket could not be loaded.");
    } finally {
      setBusy(false);
    }
  }

  useEffect(() => {
    setTimeline(null);
    void load();
    // Refetching when the id changes is the whole point of the dependency.
  }, [id]);

  // The ticket you are reading is the one most likely to change while you read
  // it. Refreshed in place rather than cleared, so a reply appears at the
  // bottom of the thread instead of the page blanking and rebuilding under you.
  useLiveChanges(
    ["ticket"],
    useCallback(() => {
      void load(true);
      // load closes over nothing that changes between renders.
      // eslint-disable-next-line react-hooks/exhaustive-deps
    }, [id]),
  );

  if (missing) {
    return (
      <div className="mx-auto max-w-[1180px] px-6 py-6">
        <Empty headline="Ticket history is not available" />
      </div>
    );
  }
  if (error) {
    return (
      <div className="mx-auto max-w-[1180px] px-6 py-6">
        <div className="mt-4">
          <Problem>{error}</Problem>
        </div>
      </div>
    );
  }
  if (!timeline) {
    return (
      <div className="mx-auto max-w-[1180px] px-6 py-6">
        <Loading rows={5} />
      </div>
    );
  }

  const { ticket, entries } = timeline;

  // The opening request, lifted out of the thread. On a long ticket the thread
  // folds to the recent end, which used to hide the one message the whole
  // ticket is about behind a "show earlier" button.
  const opening =
    entries.find((e) => fromCustomer(e) && e.body?.trim()) ??
    entries.find((e) => e.kind !== "created" && !/time|logged/.test(e.kind) && e.body?.trim());
  const thread = entries.filter((e) => e !== opening);

  return (
    <div className="mx-auto max-w-[1180px] px-6 py-6">
      <Header ticket={ticket} busy={busy} onRefresh={() => void load(true)} go={go} />

      <Vitals timeline={timeline} />

      <div className="mt-8 grid gap-8 lg:grid-cols-[minmax(0,1fr)_300px]">
        <div className="min-w-0">
          {opening && <Opening entry={opening} subject={ticket.subject} />}
          <Thread entries={thread} />
          <AskAzir timeline={timeline} ask={ask} />
        </div>

        <aside className="flex flex-col gap-3">
          <Facts ticket={ticket} />

          {ticket.customer_id && (
            <CustomerCard id={ticket.customer_id} fallback={ticket.customer} go={go} />
          )}

          {ticket.customer_id && (
            <SameCustomer
              customerId={ticket.customer_id}
              exceptId={ticket.id}
              customer={ticket.customer}
              go={go}
            />
          )}

          {/* What this history cannot show, in the plugin's own words.
              A footnote about the limits of the source, not a problem with the
              ticket — so it is quiet and sits under the facts it qualifies,
              rather than shouting above the thread it does not describe. */}
          {timeline.notes && timeline.notes.length > 0 && (
            <div className="rounded-lg border border-dashed border-edge px-4 py-3">
              <Label className="mb-2 block">Worth knowing</Label>
              <div className="flex flex-col gap-1.5">
                {timeline.notes.map((note) => (
                  <p key={note} className="text-xs leading-relaxed text-ink-faint">
                    {note}
                  </p>
                ))}
              </div>
            </div>
          )}
        </aside>
      </div>
    </div>
  );
}

/**
 * Who this ticket is, and the three things you can do to it from here.
 *
 * Everything identifying sits on one line under the subject rather than as a
 * row of chips: status, priority, customer and owner are read together — "an
 * urgent Acme ticket nobody owns" is one thought, not four.
 */
function Header({
  ticket,
  busy,
  onRefresh,
  go,
}: {
  ticket: Ticket;
  busy: boolean;
  onRefresh: () => void;
  go: (to: Route) => void;
}) {
  const priority = prioritySignal(ticket.priority);
  const finished = isDone(ticket.status);

  return (
    <header className="border-b border-edge pb-5">
      <div className="mb-3 flex items-center justify-between gap-3">
        <div className="flex items-center gap-2">
          {/* Back rather than a link to the list, so the filters and the scroll
              position somebody spent effort on survive opening one ticket. It
              falls back to the list when this page was opened directly, which
              is when there is nothing behind it to return to. */}
          <button
            className="flex items-center gap-1 rounded-md px-1.5 py-1 text-xs text-ink-dim transition-colors hover:bg-sunken hover:text-ink"
            onClick={() => (canGoBack() ? window.history.back() : go({ name: "tickets" }))}
          >
            <Icon.back /> Back
          </button>
          <Label>#{ticket.number || ticket.id}</Label>
        </div>

        <div className="flex shrink-0 items-center gap-2">
          {/* Azir deliberately cannot do everything to a ticket that Syncro
              can. The alternative to this link is retyping a number into
              another tab, which is the trip this product exists to remove. */}
          {ticket.url && (
            <a
              className="flex h-8 items-center gap-1.5 rounded-md px-2.5 text-xs text-ink-dim transition-colors hover:bg-sunken hover:text-ink"
              href={ticket.url}
              target="_blank"
              rel="noreferrer"
            >
              <Icon.external /> Open in Syncro
            </a>
          )}
          <button
            className="flex h-8 shrink-0 items-center gap-1.5 rounded-md border border-edge bg-panel px-3 text-sm font-medium transition-colors hover:bg-sunken disabled:opacity-50"
            onClick={onRefresh}
            disabled={busy}
            title="Read this ticket again from the helpdesk"
          >
            <span className={busy ? "animate-spin" : ""}>
              <Icon.refresh />
            </span>
            Refresh
          </button>
        </div>
      </div>

      <h1
        className={cn(
          "max-w-[48ch] text-2xl font-semibold tracking-tight text-balance",
          finished && "text-ink-dim",
        )}
      >
        {ticket.subject}
      </h1>

      <div className="mt-2.5 flex flex-wrap items-center gap-x-3 gap-y-2 text-xs">
        {ticket.status && <Chip tone={statusTone(ticket.status)}>{ticket.status}</Chip>}

        {ticket.priority && (
          <span
            className={cn(
              "inline-flex items-center gap-1.5 font-mono text-2xs uppercase tracking-[0.09em]",
              priority === "critical"
                ? "text-critical"
                : priority === "attention"
                  ? "text-attention"
                  : "text-ink-faint",
            )}
          >
            <span
              className={cn(
                "size-1.5 rounded-full",
                priority === "critical"
                  ? "bg-critical"
                  : priority === "attention"
                    ? "bg-attention"
                    : "bg-edge-strong",
              )}
            />
            {ticket.priority}
          </span>
        )}

        <span className="text-edge-strong" aria-hidden="true">
          |
        </span>

        {ticket.customer && (
          <button
            className="inline-flex items-center gap-1.5 rounded-md px-1 py-0.5 text-ink-dim underline-offset-4 transition-colors hover:bg-sunken hover:text-ink hover:underline"
            onClick={() =>
              ticket.customer_id && go({ name: "customer", id: String(ticket.customer_id) })
            }
          >
            <Icon.business />
            {ticket.customer}
          </button>
        )}

        {/* An unowned ticket is the most common reason one goes stale, so it
            says so rather than leaving a blank where a name would be. */}
        <span
          className={cn(
            "inline-flex items-center gap-1.5",
            ticket.assigned_to?.trim() ? "text-ink-dim" : "text-attention",
          )}
        >
          <Icon.person />
          {ticket.assigned_to?.trim() || "Unassigned"}
        </span>
      </div>
    </header>
  );
}

/**
 * The ticket's condition, in four numbers.
 *
 * Full width and directly under the header, because these describe the ticket
 * as a whole and were previously squeezed into a sidebar two at a time. Each is
 * coloured by whether it is a problem, since a number a reader has to evaluate
 * for themselves is a number they will skip. Thresholds are stated in one place
 * so they can be argued with.
 */
function Vitals({ timeline }: { timeline: Timeline }) {
  const first = timeline.first_response_minutes ?? 0;
  const gap = timeline.longest_gap_hours ?? 0;

  const cells: { label: string; value: string; note?: string; tone: string }[] = [
    {
      label: "to first reply",
      value: first ? duration(first) : "none yet",
      note: first ? undefined : "nobody has answered",
      tone: !first ? "bad" : first > 240 ? "warn" : "good",
    },
    {
      label: "longest silence",
      value: gap ? `${gap}h` : "—",
      tone: gap >= 72 ? "bad" : gap >= 24 ? "warn" : "",
    },
    {
      label: "back and forth",
      // Zero because nobody replied and zero because we cannot tell who did
      // are different facts, and only one of them is about the ticket.
      value: timeline.round_trips_unknown ? "unclear" : String(timeline.round_trips),
      note: timeline.round_trips_unknown ? "sides cannot be told apart" : undefined,
      // Many round trips is not failure, but it is a signal that the thread is
      // not converging and might be worth a call instead.
      tone: timeline.round_trips_unknown ? "" : timeline.round_trips >= 6 ? "warn" : "",
    },
    {
      label: "time logged",
      value: duration(timeline.total_logged_minutes),
      tone: "",
    },
  ];

  const colour = (tone: string) =>
    tone === "bad"
      ? "text-critical"
      : tone === "warn"
        ? "text-attention"
        : tone === "good"
          ? "text-steady"
          : "text-ink";

  return (
    <>
      {timeline.stale && (
        <p className="mt-5 rounded-lg border border-attention/30 bg-attention/10 px-3.5 py-2.5 text-sm text-attention">
          This ticket has gone quiet.
        </p>
      )}
      <dl className="mt-5 grid grid-cols-2 gap-px overflow-hidden rounded-lg border border-edge bg-edge sm:grid-cols-4">
        {cells.map((c) => (
          <div key={c.label} className="bg-panel px-4 py-3">
            <dd
              className={cn(
                "font-mono text-xl font-medium tabular-nums tracking-tight",
                colour(c.tone),
              )}
            >
              {c.value}
            </dd>
            <dt className="mt-0.5 font-mono text-2xs uppercase tracking-[0.09em] text-ink-faint">
              {c.label}
            </dt>
            {c.note && <p className="mt-1 text-2xs text-ink-faint">{c.note}</p>}
          </div>
        ))}
      </dl>
    </>
  );
}

/**
 * The message the ticket is actually about.
 *
 * Pinned above the thread rather than left in it. A long ticket opens at the
 * recent end with the older part folded, which was hiding the original request
 * behind a button — and the original request is the one thing on the page that
 * nobody reading this ticket can do without.
 */
function Opening({ entry, subject }: { entry: TimelineEntry; subject: string }) {
  const [full, setFull] = useState(false);
  const body = entry.body ?? "";
  // Long enough that it stops being a summary and starts being the thread.
  const long = body.length > 900;

  return (
    <section className="mb-8">
      <div className="mb-2 flex items-baseline justify-between gap-3">
        <Label>What they reported</Label>
        <Label title={absolute(entry.at)}>{ago(entry.at)}</Label>
      </div>
      <blockquote className="rounded-lg border border-edge bg-sunken/60 p-4">
        <p
          className={cn(
            "whitespace-pre-wrap break-words text-sm leading-relaxed",
            long && !full && "line-clamp-[12]",
          )}
        >
          {body || subject}
        </p>
        {long && (
          <button
            className="mt-2 text-xs text-ink-dim underline-offset-4 transition-colors hover:text-ink hover:underline"
            onClick={() => setFull((v) => !v)}
          >
            {full ? "Show less" : "Read the whole thing"}
          </button>
        )}
      </blockquote>
    </section>
  );
}

/** Beyond this, a thread is long enough that reading it top to bottom is work. */
const LONG_THREAD = 12;

function Thread({ entries }: { entries: TimelineEntry[] }) {
  // A long history opens at the recent end, with the older part folded. What
  // happened last week is context; what happened yesterday is the reason
  // somebody opened the ticket.
  const [expanded, setExpanded] = useState(entries.length <= LONG_THREAD);

  if (entries.length === 0) {
    return (
      <Empty headline="Nothing else has happened yet">
        Nobody has replied, and no time has been logged against it.
      </Empty>
    );
  }

  const hidden = expanded ? 0 : entries.length - LONG_THREAD;
  const shown = expanded ? entries : entries.slice(-LONG_THREAD);

  return (
    <div className="flex flex-col">
      <Label className="mb-3 block">Since then</Label>
      {hidden > 0 && (
        <button
          className="mb-4 self-start rounded-md border border-edge px-2.5 py-1 text-xs text-ink-dim transition-colors hover:bg-sunken hover:text-ink"
          onClick={() => setExpanded(true)}
        >
          Show {hidden} earlier {hidden === 1 ? "message" : "messages"}
        </button>
      )}
      {shown.map((entry, i) => {
        const previous = shown[i - 1];
        const gapHours = previous
          ? (new Date(entry.at).getTime() - new Date(previous.at).getTime()) / 3_600_000
          : 0;

        return (
          <div key={`${entry.at}-${i}`}>
            {/* A silence long enough to matter is drawn, because the absence of
                activity is the thing being looked for and nothing else shows it. */}
            {gapHours >= GAP_HOURS && (
              <div className="mb-5 ml-9 flex items-center gap-2.5">
                <span className="h-px w-4 bg-attention/40" />
                <Label className="text-attention">
                  {Math.round(gapHours / 24)} days with no activity
                </Label>
                <span className="h-px flex-1 bg-attention/25" />
              </div>
            )}
            <Event entry={entry} />
          </div>
        );
      })}
    </div>
  );
}

/**
 * Whose message this was.
 *
 * The connected system's author field is filled in by whoever configured it, and
 * it is regularly not a name: blank, or a single letter somebody typed once and
 * never corrected. Printing that verbatim reads as a rendering fault, and the
 * reader still does not learn the one thing the line is for — whether this came
 * from the customer or from us. So an unusable name falls back to the side it
 * came from, which the timeline does know.
 */
function whoSaidIt(entry: TimelineEntry, customer: boolean, isTime: boolean): string {
  const actor = (entry.actor ?? "").trim();
  if (actor.length > 1) return actor;
  if (customer) return "The customer";
  return isTime ? "Time logged" : "Your team";
}

/** The creation event, which is an event about the ticket rather than a message. */
function openedBy(entry: TimelineEntry): string {
  const actor = (entry.actor ?? "").trim();
  return actor.length > 1 ? `Opened by ${actor}` : "Opened";
}

function Event({ entry }: { entry: TimelineEntry }) {
  // The kinds a plugin can emit are open-ended, so this reads intent from the
  // word rather than switching on a closed set it does not control.
  const kind = entry.kind.toLowerCase();
  const isTime = /time|logged/.test(kind);
  const isCreation = /creat|open/.test(kind);
  const isNote = entry.hidden === true;
  const customer = fromCustomer(entry);

  const glyph = isTime ? (
    <Icon.clock />
  ) : isNote ? (
    <Icon.note />
  ) : isCreation ? (
    <Icon.spark />
  ) : (
    <Icon.reply />
  );

  return (
    <div className="relative flex gap-3 pb-6 last:pb-0">
      {/* The spine, drawn behind the glyphs so the thread reads as one
          conversation rather than as a stack of separate boxes. */}
      <span className="absolute bottom-0 left-[13px] top-8 w-px bg-edge" aria-hidden="true" />

      <div
        className={cn(
          "relative z-10 grid size-[27px] shrink-0 place-items-center rounded-full border",
          customer
            ? "border-edge-strong bg-panel text-ink"
            : "border-edge bg-sunken text-ink-faint",
          isNote && "border-attention/40 bg-attention/10 text-attention",
        )}
      >
        {glyph}
      </div>

      <div className="min-w-0 flex-1 pt-0.5">
        <div className="flex flex-wrap items-center gap-2">
          <span className={cn("text-sm", isCreation ? "text-ink-dim" : "font-medium")}>
            {isCreation ? openedBy(entry) : whoSaidIt(entry, customer, isTime)}
          </span>
          {/* Which side spoke, said outright. It is the fact the whole thread
              hangs on, and a subtly different border was not carrying it. */}
          {!isTime && !isCreation && (
            <Label className={customer ? "text-ink-dim" : "text-ink-faint"}>
              {customer ? "customer" : "us"}
            </Label>
          )}
          {isNote && <Chip tone="warn">internal</Chip>}
          {isTime && entry.minutes ? <Chip>{duration(entry.minutes)}</Chip> : null}
          <Label title={absolute(entry.at)}>{ago(entry.at)}</Label>
        </div>
        {entry.body && !isCreation && (
          <div
            className={cn(
              "mt-1.5 whitespace-pre-wrap break-words text-sm leading-relaxed",
              // A customer's words sit on their own ground, so scanning the
              // thread tells you who was speaking without reading a word of it.
              customer && "rounded-lg border border-edge bg-sunken/60 px-3.5 py-2.5",
              isNote && "rounded-lg border border-dashed border-attention/30 bg-attention/[0.04] px-3.5 py-2.5",
              isTime ? "text-ink-dim" : "text-ink",
            )}
          >
            {entry.body}
          </div>
        )}
      </div>
    </div>
  );
}

/** The reference facts, kept out of the way of the conversation. */
function Facts({ ticket }: { ticket: Ticket }) {
  return (
    <Panel className="p-4">
      <Label className="mb-3 block">Details</Label>
      <dl className="grid grid-cols-[auto_minmax(0,1fr)] items-baseline gap-x-4 gap-y-2 text-xs">
        <dt className="text-ink-faint">Opened</dt>
        <dd className="text-right font-mono tabular-nums" title={absolute(ticket.created_at)}>
          {ago(ticket.created_at)}
        </dd>
        <dt className="text-ink-faint">Last activity</dt>
        <dd className="text-right font-mono tabular-nums" title={absolute(ticket.updated_at)}>
          {ago(ticket.updated_at)}
        </dd>
        {ticket.problem_type && (
          <>
            <dt className="text-ink-faint">Type</dt>
            <dd className="truncate text-right">{ticket.problem_type}</dd>
          </>
        )}
      </dl>
    </Panel>
  );
}

/**
 * Who to call.
 *
 * The next move on a stalled ticket is often a phone call, and the number for
 * it was two pages away. One request buys it, and the panel simply does not
 * appear if the contact details are not readable.
 */
function CustomerCard({
  id,
  fallback,
  go,
}: {
  id: number;
  fallback?: string;
  go: (to: Route) => void;
}) {
  const [customer, setCustomer] = useState<CustomerRecord | null>(null);

  useEffect(() => {
    let cancelled = false;
    setCustomer(null);
    work
      .getCustomer(id)
      .then((answer) => !cancelled && setCustomer(answer.data))
      .catch(() => {});
    return () => {
      cancelled = true;
    };
  }, [id]);

  const name = customer?.business_name || customer?.name || fallback;
  if (!customer || (!customer.email && !customer.phone)) return null;

  return (
    <Panel className="p-4">
      <Label className="mb-2 block">Contact</Label>
      <button
        className="block max-w-full truncate text-left text-sm font-medium underline-offset-4 transition-colors hover:text-azir hover:underline"
        onClick={() => go({ name: "customer", id: String(id) })}
      >
        {name}
      </button>
      <div className="mt-2.5 flex flex-col gap-1.5 text-xs">
        {customer.phone && (
          <a
            className="flex items-center gap-2 text-ink-dim transition-colors hover:text-ink"
            href={`tel:${customer.phone.replace(/[^\d+]/g, "")}`}
          >
            <Icon.phone />
            <span className="truncate font-mono tabular-nums">{customer.phone}</span>
          </a>
        )}
        {customer.email && (
          <a
            className="flex items-center gap-2 text-ink-dim transition-colors hover:text-ink"
            href={`mailto:${customer.email}`}
          >
            <Icon.mail />
            <span className="truncate">{customer.email}</span>
          </a>
        )}
      </div>
    </Panel>
  );
}

/**
 * The end of the thread, and the thing you would otherwise open Syncro to do.
 *
 * Reading a history is only ever half of picking a ticket up; the other half is
 * writing back. This sits where the reading finishes and offers the three moves
 * that follow it, each phrased as the sentence it will actually send — a button
 * that quietly rewrites what you asked for is a button you stop trusting.
 *
 * Which drafting move is offered depends on the ticket. A thread nobody has
 * answered needs a first reply; one that has gone quiet needs chasing; one in
 * mid-conversation needs the next message. Offering all three every time would
 * make the reader do the choosing, which is the work this is meant to save.
 */
function AskAzir({ timeline, ask }: { timeline: Timeline; ask: (question: string) => void }) {
  const [ready, setReady] = useState<boolean | null>(null);

  useEffect(() => {
    let cancelled = false;
    chat
      .status()
      .then((s) => !cancelled && setReady(s.ready))
      .catch(() => !cancelled && setReady(false));
    return () => {
      cancelled = true;
    };
  }, []);

  // Nothing is offered until there is something behind it. A button that
  // explains it cannot work is worse than no button.
  if (!ready) return null;

  const finished = isDone(timeline.ticket.status);
  const unanswered = !timeline.first_response_minutes;

  const draft = unanswered
    ? {
        label: "Draft the first reply",
        question:
          "Nobody has replied to this ticket yet. Draft a first reply to the customer: acknowledge what they reported, say what we are doing about it, and ask for anything you need from them.",
      }
    : timeline.stale
      ? {
          label: "Draft a chase-up",
          question:
            "This ticket has gone quiet. Draft a short, friendly message to the customer that picks the conversation back up and asks for whatever we are waiting on.",
        }
      : {
          label: "Draft the next reply",
          question:
            "Draft the next reply to the customer on this ticket, in the same tone as the messages already in the thread.",
        };

  // A finished ticket is read to learn from, not to answer. Offering to draft
  // a reply to it invites sending one to somebody whose problem is solved.
  const asks = finished
    ? [
        {
          label: "What was this?",
          question:
            "Summarise this closed ticket: what the customer reported, what fixed it, and how long it took.",
        },
        {
          label: "Would this recur?",
          question:
            "Look at how this ticket was resolved and tell me whether the underlying cause was actually fixed or worked around, and what would stop it happening again.",
        },
      ]
    : [
        {
          label: "Catch me up",
          question:
            "Catch me up on this ticket in a few sentences: what the customer reported, what has been done, and what it is waiting on right now.",
        },
        draft,
        {
          label: "What should I check next?",
          question:
            "Based on this ticket and anything similar you can find, what should I check next? List the specific steps in the order you would try them.",
        },
      ];

  return (
    // The one panel on the page that acts rather than reports, and the only one
    // wearing the brand — because this is where the assistant takes over.
    <div className="mt-8 rounded-lg border border-azir/25 bg-azir/[0.04] p-4">
      <div className="flex items-start gap-3">
        <span
          className="grid size-7 shrink-0 place-items-center rounded-md bg-azir/15 text-azir"
          aria-hidden="true"
        >
          <Icon.spark />
        </span>
        <div>
          <strong className="text-sm font-semibold">Ask Azir about this ticket</strong>
          <p className="mt-0.5 text-xs text-ink-dim">
            It has read the history above, this customer's other tickets, and your documentation.
          </p>
        </div>
      </div>

      <div className="mt-3.5 flex flex-wrap gap-2 pl-10">
        {asks.map((a) => (
          <button
            key={a.label}
            className="rounded-full border border-edge bg-panel px-3 py-1.5 text-xs font-medium transition-colors hover:border-azir hover:bg-azir hover:text-azir-ink"
            onClick={() => ask(a.question)}
          >
            {a.label}
          </button>
        ))}
      </div>

      <p className="mt-3 pl-10 text-2xs text-ink-faint">
        Answers are drafts for you to check. Nothing is sent to the customer, and any change to the
        ticket waits for you to approve it.
      </p>
    </div>
  );
}

/**
 * The customer's other open tickets.
 *
 * A ticket is rarely the whole story. Someone asking about a mailbox migration
 * usually wants to know there is also an unanswered licence request from the
 * same people — and finding that out currently means leaving the page, which is
 * exactly the trip this product exists to remove.
 */
function SameCustomer({
  customerId,
  exceptId,
  customer,
  go,
}: {
  customerId: number;
  exceptId: number;
  customer?: string;
  go: (to: Route) => void;
}) {
  const [others, setOthers] = useState<Ticket[] | null>(null);

  useEffect(() => {
    let cancelled = false;
    setOthers(null);
    (async () => {
      try {
        const answer = await work.searchTickets({
          customer_id: customerId,
          open_only: true,
          per_page: 50,
        });
        if (cancelled) return;
        setOthers(
          (answer.data.items ?? [])
            .filter((t) => t.id !== exceptId)
            // Longest untouched first: the one at risk of being forgotten is
            // the one worth surfacing beside whatever is being read.
            .sort(
              (a, b) =>
                new Date(a.updated_at ?? 0).getTime() - new Date(b.updated_at ?? 0).getTime(),
            ),
        );
      } catch {
        if (!cancelled) setOthers([]);
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [customerId, exceptId]);

  // Nothing else open is a fine answer, and not one worth a panel.
  if (others !== null && others.length === 0) return null;

  return (
    <Panel>
      <PanelHead>
        <h3 className="text-sm font-medium">Also open for {customer || "this customer"}</h3>
        {others && <Label>{others.length}</Label>}
      </PanelHead>
      <div className="p-2">
        {!others && <div className="h-12 animate-pulse rounded bg-sunken" />}
        {others && (
          <div className="flex flex-col">
            {others.slice(0, 6).map((t) => (
              <button
                key={t.id}
                className="rounded-md px-2 py-2 text-left transition-colors hover:bg-sunken"
                onClick={() => go({ name: "ticket", id: String(t.id) })}
              >
                <span className="block truncate text-xs font-medium">{t.subject}</span>
                <span className="mt-1 flex items-center gap-2">
                  {t.status && <Chip tone={statusTone(t.status)}>{t.status}</Chip>}
                  <Label>{since(t.updated_at)} idle</Label>
                </span>
              </button>
            ))}
            {others.length > 6 && (
              <button
                className="mt-1 rounded-md px-2 py-1.5 text-left text-xs text-ink-dim transition-colors hover:bg-sunken hover:text-ink"
                onClick={() => go({ name: "customer", id: String(customerId) })}
              >
                See all {others.length}
              </button>
            )}
          </div>
        )}
      </div>
    </Panel>
  );
}
