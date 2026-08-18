import { useCallback, useEffect, useRef, useSyncExternalStore } from "react";
import { work, type Ticket } from "./api";
import { useFallbackPoll, useLiveChanges } from "./live";
import { useToast } from "./Toast";
import type { Route } from "./router";
import { statusTone } from "./ui";

/**
 * Noticing what changed, and saying so.
 *
 * A technician should not have to keep opening a ticket to find out whether the
 * customer has written back. When the helpdesk says something changed, Azir
 * refetches and compares against what it was already showing — so the
 * notification is derived from what Azir read with its own credentials, never
 * from what the webhook claimed. A forged delivery can at most cause a refetch
 * that finds nothing.
 *
 * What the ticket list can and cannot say is worth being honest about: it
 * carries no comments, so "the customer replied" is not visible in it. When a
 * ticket's timestamp moves, the one changed ticket's history is read to find
 * out who moved it. That is one extra call per changed ticket — far less than
 * the polling this replaced, and it is the difference between "something
 * happened" and something worth interrupting somebody for.
 */

/** At most this many histories are read in one burst. */
const LOOKUP_BUDGET = 5;

/**
 * Which tickets have moved since this session started looking.
 *
 * Module scope rather than component state because two screens want the same
 * answer — the queue marks the rows, and the watcher decides what to announce —
 * and threading it through both would mean the mark disappearing whenever a
 * screen unmounted, which is exactly when somebody was away and needs it most.
 */
const changed = new Set<number>();

// Subscribers, so a screen re-renders when the set changes underneath it.
// A plain module-level Set is invisible to React: the marks were computed
// correctly and simply never drawn, because nothing told the queue to look
// again.
const marked = new Set<() => void>();
let version = 0;

function markChanged(id: number) {
  if (changed.has(id)) return;
  changed.add(id);
  version++;
  marked.forEach((fn) => fn());
}

/** Tickets that have moved since you started looking. */
export function hasChanged(id: number): boolean {
  return changed.has(id);
}

/** Forgets a mark, once somebody has actually looked at it. */
export function seenChange(id: number) {
  if (!changed.delete(id)) return;
  version++;
  marked.forEach((fn) => fn());
}

/**
 * Re-renders the caller whenever the set of changed tickets moves.
 *
 * The returned number is not useful in itself; subscribing is the point.
 */
export function useChangedTickets(): number {
  return useSyncExternalStore(
    (notify) => {
      marked.add(notify);
      return () => marked.delete(notify);
    },
    () => version,
    () => version,
  );
}

type Seen = { updated?: string; status?: string; subject: string; customer?: string };

export function useTicketWatch(
  go: (to: Route) => void,
  enabled: boolean,
  /** What is on screen, so we do not announce what is already visible. */
  looking?: { name: string; id?: string },
) {
  const seen = useRef<Map<number, Seen> | null>(null);
  const toast = useToast();

  // Read through a ref so a change of screen does not re-run the whole watch —
  // it only affects what gets announced next time.
  const onScreen = useRef(looking);
  onScreen.current = looking;

  const check = useCallback(async () => {
    if (!enabled) return;
    let tickets: Ticket[];
    try {
      tickets = (await work.searchTickets({ per_page: 100 }, true)).data.items ?? [];
    } catch {
      return; // No helpdesk, no permission, or it is briefly unreachable.
    }

    const now = new Map<number, Seen>();
    for (const t of tickets) {
      now.set(t.id, {
        updated: t.updated_at,
        status: t.status,
        subject: t.subject,
        customer: t.customer,
      });
    }

    const before = seen.current;
    seen.current = now;
    // The first pass is only a baseline. Announcing everything that exists the
    // moment somebody signs in would be forty notifications about nothing.
    if (!before) {
      changed.clear();
      return;
    }

    // A reply to the ticket you are reading needs no toast: that page refreshes
    // in place, so the message is already in front of you and a notification
    // about it is just something else to dismiss.
    const watching = onScreen.current;
    const showing = (id: number) =>
      watching?.name === "ticket" && watching.id === String(id);

    const arrived: Ticket[] = [];
    const moved: Ticket[] = [];
    for (const t of tickets) {
      const was = before.get(t.id);
      if (!was) {
        arrived.push(t);
        markChanged(t.id);
      } else if (was.updated !== t.updated_at) {
        moved.push(t);
        markChanged(t.id);
      }
    }

    for (const t of arrived.slice(0, LOOKUP_BUDGET)) {
      if (showing(t.id)) continue;
      toast(`New ticket from ${t.customer ?? "a customer"}`, {

        detail: t.subject,
        onOpen: () => go({ name: "ticket", id: String(t.id) }),
      });
    }

    for (const t of moved.slice(0, LOOKUP_BUDGET)) {
      if (showing(t.id)) continue;
      const was = before.get(t.id);
      // Resolved is worth saying plainly rather than as "updated".
      if (was?.status !== t.status && statusTone(t.status) === "good") {
        toast(`${t.subject} was resolved`, {
          tone: "good",
          detail: t.customer,
          onOpen: () => go({ name: "ticket", id: String(t.id) }),
        });
        continue;
      }

      const who = await whoMovedIt(t.id);
      if (who === "customer") {
        toast(`${t.customer ?? "The customer"} replied`, {

          detail: t.subject,
          onOpen: () => go({ name: "ticket", id: String(t.id) }),
        });
      } else if (was?.status !== t.status && t.status) {
        toast(`${t.subject} is now ${t.status}`, {

          detail: t.customer,
          onOpen: () => go({ name: "ticket", id: String(t.id) }),
        });
      }
      // A ticket one of your own colleagues just touched is not news.
    }
  }, [enabled, go, toast]);

  useEffect(() => {
    void check();
  }, [check]);

  useLiveChanges(["ticket", "sla"], check);

  // An SLA breach is the one event in a helpdesk that is worth interrupting
  // somebody for regardless of what they are doing, so it is announced on
  // arrival rather than waiting to be worked out from a diff. Which ticket
  // breached is not in the message — nothing a webhook says is believed — so
  // the refetch above finds it, and this says plainly that one has.
  useLiveChanges(
    ["sla"],
    useCallback(() => {
      toast("An SLA has breached", {
        tone: "bad",
        detail: "Your helpdesk reported it. The queue below is refreshing.",
      });
    }, [toast]),
  );
  // Works whether or not anybody has configured a webhook yet.
  useFallbackPoll(check);
}

/**
 * Who made the most recent thing happen on a ticket.
 *
 * Read from the history rather than guessed from the list, because the list
 * cannot say. A failure here is not worth a notification of its own — the
 * caller simply falls back to the quieter wording.
 */
async function whoMovedIt(id: number): Promise<"customer" | "us" | "unknown"> {
  try {
    const timeline = (await work.timeline(id, true)).data;
    const entries = timeline.entries ?? [];
    if (entries.length === 0) return "unknown";
    const last = entries[entries.length - 1];
    return /customer|client|inbound/.test(last.kind.toLowerCase()) ? "customer" : "us";
  } catch {
    return "unknown";
  }
}
