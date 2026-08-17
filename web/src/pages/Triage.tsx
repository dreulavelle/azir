import { useCallback, useEffect, useMemo, useState } from "react";
import { Split, Trend, type Point } from "../charts";
import { GettingStarted } from "./GettingStarted";
import { NotProvided, work, type Actor, type Provenance, type Ticket } from "../api";
import type { Route } from "../router";
import { useFallbackPoll, useLiveChanges } from "../live";
import { hasChanged, seenChange, useChangedTickets } from "../watch";
import { cn } from "@/lib/cn";
import { Chip, Empty, Icon, Label, Loading, Panel, Problem, Stat, daysSince, isDone, priorityRank, prioritySignal, since, statusTone } from "../ui";

/**
 * The queue, ordered by what needs attention rather than by when it arrived.
 *
 * This is the whole argument for the product. A PSA shows a list sorted by
 * date, which is the order things happened in and not the order they matter in;
 * a technician then reads all of it to find the three that are actually going
 * wrong. Azir does that reading.
 *
 * Every lane below is a claim that can be checked against the ticket, and the
 * lane says which claim it is making. Nothing here is a score nobody can argue
 * with — a technician who disagrees can see exactly why a ticket is where it is.
 */

type Lane = {
  key: string;
  title: string;
  why: string;
  tone: "urgent" | "warn" | "accent" | "";
  tickets: Ticket[];
};

/**
 * How many tickets a lane shows before it stops being a triage list.
 *
 * A busy deployment can put hundreds in one lane. The browser copes — a person
 * does not, and a wall of six hundred cards answers the opposite of the
 * question this screen exists to answer. The rest are one click away, sorted
 * so the ones shown are the ones that deserve to be.
 */
const LANE_LIMIT = 8;

/** How long without an update before a ticket has gone quiet. */
const QUIET_DAYS = 5;

/** How long a new ticket may sit untouched before that is itself the problem. */
const UNANSWERED_HOURS = 8;


/**
 * Sorts tickets into lanes.
 *
 * Order matters: a ticket belongs to the first lane it qualifies for, so the
 * most urgent reading of a ticket is the one that gets shown. A ticket that is
 * both urgent and quiet is an urgent ticket, and saying so twice would only
 * make the queue longer.
 */
export function lanesFor(tickets: Ticket[]): Lane[] {
  const taken = new Set<number>();
  const claim = (predicate: (t: Ticket) => boolean) => {
    const out = tickets.filter((t) => !taken.has(t.id) && predicate(t));
    out.forEach((t) => taken.add(t.id));
    return out;
  };

  const open = (t: Ticket) => !isDone(t.status);

  // Longest untouched first, within every lane. Whatever order the connected
  // system returned is not an order of need, and when only the first few are
  // shown the choice of which few has to mean something.
  const byNeglect = (list: Ticket[]) =>
    [...list].sort(
      (a, b) => new Date(a.updated_at ?? 0).getTime() - new Date(b.updated_at ?? 0).getTime(),
    );

  const lanes: Lane[] = [
    {
      key: "urgent",
      title: "Urgent and open",
      why: "highest priority, not resolved",
      tone: "urgent",
      tickets: byNeglect(claim((t) => open(t) && priorityRank(t.priority) === "urgent")),
    },
    {
      key: "unanswered",
      title: "Never answered",
      why: `new, opened over ${UNANSWERED_HOURS}h ago`,
      tone: "urgent",
      tickets: byNeglect(
        claim(
          (t) =>
            open(t) &&
            statusTone(t.status) === "accent" &&
            daysSince(t.created_at) * 24 > UNANSWERED_HOURS,
        ),
      ),
    },
    {
      key: "quiet",
      title: "Gone quiet",
      why: `no movement in ${QUIET_DAYS} days`,
      tone: "warn",
      tickets: byNeglect(claim((t) => open(t) && daysSince(t.updated_at) > QUIET_DAYS)),
    },
    {
      key: "waiting",
      title: "Waiting on someone else",
      why: "parked, but still ours to chase",
      tone: "",
      tickets: byNeglect(claim((t) => open(t) && statusTone(t.status) === "warn")),
    },
    {
      key: "active",
      title: "In flight",
      why: "moving, nothing to do right now",
      tone: "",
      tickets: byNeglect(claim(open)),
    },
  ];

  return lanes.filter((l) => l.tickets.length > 0);
}

/**
 * What the queue has been doing lately, derived from the tickets already on
 * screen rather than from a new endpoint.
 *
 * The counts alone say what is true now; the shape says whether now is unusual,
 * which is the thing a lead actually wants to know before they read anything.
 */
function useInsights(tickets: Ticket[] | null, openWork: Ticket[] | null) {
  return useMemo(() => {
    if (!tickets || tickets.length === 0) return null;

    const DAYS = 14;
    const opened: Point[] = [];
    const closed: Point[] = [];
    const today = new Date();
    today.setHours(0, 0, 0, 0);

    for (let back = DAYS - 1; back >= 0; back--) {
      const day = new Date(today);
      day.setDate(day.getDate() - back);
      const next = new Date(day);
      next.setDate(next.getDate() + 1);
      const within = (iso?: string) => {
        const at = new Date(iso ?? "").getTime();
        return at >= day.getTime() && at < next.getTime();
      };

      const label = day.toLocaleDateString(undefined, { weekday: "short", day: "numeric" });
      opened.push({ label, value: tickets.filter((t) => within(t.created_at)).length });
      // When a ticket was finished is approximated by when it was last touched,
      // because Syncro records no resolution time we can read. It is right for
      // almost every ticket — closing one is usually the last thing done to it —
      // and wrong for any that was edited afterwards. Said out loud beside the
      // chart rather than left for somebody to discover.
      closed.push({
        label,
        value: tickets.filter((t) => isDone(t.status) && within(t.updated_at)).length,
      });
    }

    // The open-work figures come from the open-work request, which has read far
    // enough back to include the old neglected tickets. Deriving them from the
    // recent slice instead would quietly under-report every one of them.
    const open = openWork ?? tickets.filter((t) => !isDone(t.status));
    const byAge = { fresh: 0, week: 0, older: 0 };
    for (const t of open) {
      const age = daysSince(t.created_at);
      if (age <= 2) byAge.fresh++;
      else if (age <= 7) byAge.week++;
      else byAge.older++;
    }

    // Four numbers a lead actually acts on, all derived from what is already
    // loaded — no extra request buys any of them.
    const unassigned = open.filter((t) => !t.assigned_to?.trim()).length;

    // The longest anything has sat untouched. One number that answers "how bad
    // is the worst of it", which a count of open tickets never does.
    const stalest = open.reduce((worst, t) => Math.max(worst, daysSince(t.updated_at)), 0);

    // Closed in the last seven days, as a counterweight: a queue of forty is a
    // different situation depending on whether thirty went out this week.
    const closedThisWeek = tickets.filter(
      (t) => isDone(t.status) && daysSince(t.updated_at) <= 7,
    ).length;
    const openedThisWeek = tickets.filter((t) => daysSince(t.created_at) <= 7).length;

    // Who is generating the work. Useful when one customer is having a bad
    // week and nobody has noticed it is all the same customer.
    const perCustomer = new Map<string, number>();
    for (const t of open) {
      if (!t.customer) continue;
      perCustomer.set(t.customer, (perCustomer.get(t.customer) ?? 0) + 1);
    }
    const busiest = [...perCustomer.entries()].sort((a, b) => b[1] - a[1])[0];

    // Whether the queue is keeping up over the whole window, which a single
    // week's pair of numbers cannot say.
    const arrived = opened.reduce((n, p) => n + p.value, 0);
    const finished = closed.reduce((n, p) => n + p.value, 0);

    return {
      opened,
      closed,
      arrived,
      finished,
      byAge,
      openCount: open.length,
      unassigned,
      stalest,
      closedThisWeek,
      openedThisWeek,
      busiest: busiest && busiest[1] > 1 ? { name: busiest[0], count: busiest[1] } : null,
    };
  }, [tickets]);
}

export function Triage({ actor, go }: { actor: Actor; go: (to: Route) => void }) {
  // Open work, for the lanes; and the recent slice including finished tickets,
  // for the chart. Kept apart because they answer different questions.
  const [tickets, setTickets] = useState<Ticket[] | null>(null);
  const [recent, setRecent] = useState<Ticket[] | null>(null);
  const [provenance, setProvenance] = useState<Provenance | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [missing, setMissing] = useState(false);
  const [busy, setBusy] = useState(false);
  const insights = useInsights(recent, tickets);
  // Subscribing is what makes a row redraw when it is marked as changed.
  useChangedTickets();

  /**
   * Two questions, so two requests.
   *
   * The lanes are about open work, including the ticket from three months ago
   * that everybody has forgotten — and that one is only reachable by asking the
   * helpdesk to skip past the finished tickets, because otherwise a page of the
   * hundred most recent never reaches it. The chart is the opposite: it needs
   * finished tickets, because "are we keeping up" is a question about both
   * sides. One request cannot answer both without lying to one of them.
   *
   * Both are cached by the same freshness rules as everything else, so the
   * second one is nearly free.
   */
  async function load(refresh = false) {
    setBusy(true);
    try {
      const [open, recent] = await Promise.all([
        work.searchTickets({ open_only: true, per_page: 100 }, refresh),
        work.searchTickets({ per_page: 100 }, refresh),
      ]);
      setTickets(open.data.items ?? []);
      setRecent(recent.data.items ?? []);
      setProvenance(open.provenance);
      setError(null);
      setMissing(false);
    } catch (e) {
      if (e instanceof NotProvided) setMissing(true);
      else setError(e instanceof Error ? e.message : "The queue could not be loaded.");
    } finally {
      setBusy(false);
    }
  }

  useEffect(() => {
    void load();
  }, []);

  // Refreshed when the helpdesk says something changed, and on a slow timer
  // underneath that in case nobody has wired the webhook up.
  const refresh = useCallback(() => {
    void load(true);
    // load closes over nothing that changes between renders.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  useLiveChanges(["ticket"], refresh);
  useFallbackPoll(refresh);

  if (missing) {
    return (
      <div className="mx-auto max-w-[1180px] px-6 py-6">
        <GettingStarted actor={actor} go={go} />
      </div>
    );
  }

  if (error) {
    return (
      <div className="mx-auto max-w-[1180px] px-6 py-6">
        <Problem>{error}</Problem>
      </div>
    );
  }

  if (!tickets) {
    return (
      <div className="mx-auto max-w-[1180px] px-6 py-6">
        <Loading rows={6} />
      </div>
    );
  }

  const lanes = lanesFor(tickets);
  // `tickets` is already the open-work request, so this is a belt-and-braces
  // count rather than a filter doing real work.
  const openCount = tickets.filter((t) => !isDone(t.status)).length;
  const needsAttention = lanes
    .filter((l) => l.tone === "urgent" || l.tone === "warn")
    .reduce((n, l) => n + l.tickets.length, 0);

  return (
    <div className="mx-auto max-w-[1180px] px-6 py-6">
      <div className="mb-6 flex items-start justify-between gap-4">
        <div>
          <h1 className="text-2xl font-semibold tracking-tight">Triage</h1>
          <p className="mt-1 text-sm text-ink-dim">
            {needsAttention === 0
              ? `Nothing is overdue or stalled across ${openCount} open tickets.`
              : `${needsAttention} of ${openCount} open tickets need a decision from someone.`}
          </p>
        </div>
        <div className="flex shrink-0 items-center gap-2">
          {provenance && provenance.source !== "live" && (
            <Label>
              {provenance.ageSeconds < 60
                ? "just refreshed"
                : `${Math.round(provenance.ageSeconds / 60)}m old`}
            </Label>
          )}
          <button
            className="flex h-8 items-center gap-1.5 rounded-md border border-edge bg-panel px-3 text-sm font-medium transition-colors hover:bg-sunken disabled:opacity-50"
            onClick={() => void load(true)}
            disabled={busy}
          >
            <span className={busy ? "animate-spin" : ""}>
              <Icon.refresh />
            </span>
            Refresh
          </button>
        </div>
      </div>

      <GettingStarted actor={actor} go={go} />

      {insights && insights.openCount > 0 && (
        <>
          {/* The four figures worth glancing at before reading anything. */}
          <div className="mb-3 grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
            <Panel className="p-4" rail={insights.unassigned > 0 ? "attention" : undefined}>
              <Stat
                value={insights.unassigned}
                label="nobody assigned"
                tone={insights.unassigned > 0 ? "attention" : undefined}
              />
            </Panel>
            <Panel className="p-4" rail={insights.stalest > QUIET_DAYS ? "critical" : undefined}>
              <Stat
                value={insights.stalest >= 1 ? `${Math.round(insights.stalest)}d` : "<1d"}
                label="longest untouched"
                tone={insights.stalest > QUIET_DAYS ? "critical" : undefined}
              />
            </Panel>
            <Panel className="p-4">
              <Stat
                value={`${insights.openedThisWeek}/${insights.closedThisWeek}`}
                label="opened / closed this week"
                tone={insights.closedThisWeek >= insights.openedThisWeek ? "steady" : undefined}
              />
            </Panel>
            <Panel className="p-4">
              <Stat
                value={insights.busiest ? String(insights.busiest.count) : "—"}
                label={insights.busiest ? `open for ${insights.busiest.name}` : "no busiest customer"}
              />
            </Panel>
          </div>

        <div className="mb-7 grid items-start gap-3 md:grid-cols-2">
          {/* Is the queue keeping up. The one question this screen exists to
              answer before anybody reads a single ticket. */}
          <Panel className="p-4">
            <div className="mb-3 flex items-baseline justify-between gap-3">
              <h2 className="text-sm font-medium">Arriving and finishing</h2>
              <Label
                className={
                  insights.finished >= insights.arrived ? "text-steady" : "text-attention"
                }
              >
                {insights.finished >= insights.arrived
                  ? `keeping up · ${insights.finished - insights.arrived} ahead`
                  : `${insights.arrived - insights.finished} behind over 14 days`}
              </Label>
            </div>
            <Trend
              points={insights.opened}
              against={insights.closed}
              legend={{ points: "arrived", against: "finished" }}
            />
            <p className="mt-2 text-2xs text-ink-faint">
              Finished is counted from when a ticket was last touched — the helpdesk keeps no
              record of when one was resolved.
            </p>
          </Panel>

          <Panel className="p-4">
            <div className="mb-3 flex items-baseline justify-between">
              <h2 className="text-sm font-medium">How old the open ones are</h2>
              <Label>{insights.openCount} open</Label>
            </div>
            <Split
              parts={[
                { label: "under 2 days", value: insights.byAge.fresh, tone: "good" },
                { label: "this week", value: insights.byAge.week, tone: "warn" },
                { label: "over a week", value: insights.byAge.older, tone: "urgent" },
              ]}
            />
          </Panel>
        </div>
        </>
      )}

      {lanes.length === 0 ? (
        <Empty headline="The queue is clear">Nothing is open. Enjoy it while it lasts.</Empty>
      ) : (
        <div className="flex flex-col gap-7">
          {lanes.map((lane) => (
            <section key={lane.key}>
              {/* The lane header is a rule across the width, like the label
                  strip above a row of ports: the name, the count, and the
                  claim the lane is making, in that order. */}
              <div className="mb-2 flex items-baseline gap-2.5 border-b border-edge pb-2">
                <h2 className="text-sm font-semibold">{lane.title}</h2>
                <span
                  className={cn(
                    "font-mono text-xs tabular-nums",
                    lane.tone === "urgent"
                      ? "text-critical"
                      : lane.tone === "warn"
                        ? "text-attention"
                        : "text-ink-faint",
                  )}
                >
                  {lane.tickets.length}
                </span>
                <Label className="ml-auto">{lane.why}</Label>
              </div>

              <div className="flex flex-col">
                {lane.tickets.slice(0, LANE_LIMIT).map((t) => (
                  <TicketRow
                    key={t.id}
                    ticket={t}
                    onOpen={() => go({ name: "ticket", id: String(t.id) })}
                  />
                ))}
                {lane.tickets.length > LANE_LIMIT && (
                  <button
                    className="mt-1 self-start rounded-md px-2 py-1.5 text-xs text-ink-dim transition-colors hover:bg-sunken hover:text-ink"
                    onClick={() => go({ name: "tickets" })}
                  >
                    {lane.tickets.length - LANE_LIMIT} more like this — see them all
                  </button>
                )}
              </div>
            </section>
          ))}
        </div>
      )}
    </div>
  );
}

/**
 * One ticket, as a row rather than as a card.
 *
 * A card asks to be read; a row asks to be scanned, and forty of them in a
 * column is a thing the eye can run down because every value sits in the same
 * place every time. The rail at the left edge carries priority as colour
 * alone — it is the only saturated thing in the row, so a queue with one
 * urgent ticket in it looks like a queue with one urgent ticket in it from
 * across the desk.
 */
export function TicketRow({ ticket, onOpen }: { ticket: Ticket; onOpen: () => void }) {
  const idle = daysSince(ticket.updated_at);
  const idleTone = idle > QUIET_DAYS ? "critical" : idle > 2 ? "attention" : "idle";
  // Moved since this session started looking. Marked rather than reordered:
  // a queue that rearranges itself under somebody mid-scan is worse than one
  // that is slightly out of date.
  const moved = hasChanged(ticket.id);

  return (
    <button
      className={cn(
        "group relative flex w-full items-center gap-4 border-b border-edge/60 py-2.5 pl-4 pr-2 text-left transition-colors last:border-b-0 hover:bg-sunken/70",
        // The tint says "this moved" for as long as it is unread; the warm-and-
        // settle says "it moved just now". Both, because a technician who was
        // away needs the first and one who is watching needs the second.
        moved && "bg-azir/[0.05] just-changed",
      )}
      onClick={() => {
        seenChange(ticket.id);
        onOpen();
      }}
    >
      <span
        className={cn(
          "absolute inset-y-1 left-0 w-[3px] rounded-full",
          RAIL_FOR[prioritySignal(ticket.priority)],
        )}
      />

      <span className="min-w-0 flex-1">
        <span className="flex items-center gap-2">
          {moved && (
            <span
              className="size-1.5 shrink-0 rounded-full bg-azir"
              title="Changed since you started looking"
              aria-label="Changed since you started looking"
            />
          )}
          <span className="truncate text-sm font-medium">{ticket.subject}</span>
        </span>
        <span className="mt-0.5 flex items-center gap-2 text-xs text-ink-faint">
          {ticket.customer && <span className="truncate">{ticket.customer}</span>}
          {ticket.problem_type && (
            <>
              <span aria-hidden="true">·</span>
              <span className="truncate">{ticket.problem_type}</span>
            </>
          )}
          {ticket.assigned_to && (
            <>
              <span aria-hidden="true">·</span>
              <span className="truncate">{ticket.assigned_to}</span>
            </>
          )}
        </span>
      </span>

      <span className="hidden w-40 shrink-0 sm:block">
        {ticket.status && <Chip tone={statusTone(ticket.status)}>{ticket.status}</Chip>}
      </span>

      {/* Two numbers, because they answer different questions: how long has
          this existed, and how long since anyone touched it. The second is
          what a stalled ticket looks like. */}
      <span className="flex shrink-0 items-baseline gap-4 font-mono text-xs tabular-nums">
        <span className="w-14 text-right text-ink-faint">
          {since(ticket.created_at)}
          <span className="ml-1 text-ink-faint/70">old</span>
        </span>
        <span
          className={cn(
            "w-14 text-right",
            idleTone === "critical"
              ? "text-critical"
              : idleTone === "attention"
                ? "text-attention"
                : "text-ink-faint",
          )}
        >
          {since(ticket.updated_at)}
          <span className="ml-1 opacity-70">idle</span>
        </span>
      </span>
    </button>
  );
}

/** The rail colour for each signal, kept beside the only component that draws it. */
const RAIL_FOR = {
  critical: "bg-critical",
  attention: "bg-attention",
  steady: "bg-steady/50",
  idle: "bg-edge-strong",
} as const;

export { daysSince };
