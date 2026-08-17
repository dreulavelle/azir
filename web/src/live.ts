import { useEffect } from "react";

/**
 * Being told when something changed, instead of asking.
 *
 * Every screen used to poll. Triage asked for the whole queue once a minute
 * whether anything had happened or not — a minute out of date, and a request a
 * minute against somebody's PSA forever. A connected system now tells Azir when
 * something changes and Azir tells the browser, so a queue is current within a
 * second and costs nothing while nothing happens.
 *
 * One connection is shared by every screen. A page with a queue, a rail count
 * and a ticket open should not hold three.
 */

type Listener = (subject: string) => void;

const listeners = new Set<Listener>();
let stream: EventSource | null = null;
let backoff = 1000;

/**
 * When a change last arrived by push.
 *
 * Push only works once somebody has pasted Azir's address into the connected
 * system, and some never will — a PSA behind a firewall, a customer who does
 * not allow outbound webhooks. A product that only refreshes when correctly
 * configured is broken out of the box and gives no sign of it, which is exactly
 * what happened here: the polling came out before the webhook went in, and the
 * screen quietly stopped updating.
 *
 * So a slow poll stays as a floor. It is twelve times cheaper than the
 * per-minute one it replaced, and it stands down entirely while push is
 * working — a deployment with webhooks wired up pays almost nothing for it.
 */
let lastPush = 0;

/** How often to check anyway, when nothing has been pushed. */
export const FALLBACK_MS = 5 * 60_000;

/** Whether push has proved itself recently enough to skip a poll. */
export function pushIsWorking(): boolean {
  return Date.now() - lastPush < FALLBACK_MS;
}

function connect() {
  if (stream || listeners.size === 0) return;

  stream = new EventSource("/api/events");

  stream.addEventListener("changed", (e) => {
    backoff = 1000;
    lastPush = Date.now();
    try {
      const { subject } = JSON.parse((e as MessageEvent).data);
      listeners.forEach((fn) => fn(subject));
    } catch {
      // A message we cannot read is not worth a broken screen.
    }
  });

  stream.onopen = () => {
    backoff = 1000;
  };

  stream.onerror = () => {
    // EventSource reconnects on its own, but it does so immediately and
    // forever — which turns a restarting server into a stampede. Closing and
    // backing off means an outage costs one connection attempt every few
    // seconds rather than thousands.
    stream?.close();
    stream = null;
    const wait = backoff;
    backoff = Math.min(backoff * 2, 30_000);
    setTimeout(connect, wait);
  };
}

/**
 * Runs `onChange` when something of the given kind changes elsewhere.
 *
 * The subject is deliberately coarse — "tickets", "customers" — because the
 * message is a nudge to go and look, not the change itself. What actually
 * changed is read back through the ordinary authenticated path.
 */
/**
 * Runs `onChange` on a slow timer, but only while push is not working.
 *
 * Paired with useLiveChanges rather than replacing it: push is the fast path
 * and this is the floor underneath it.
 */
export function useFallbackPoll(onChange: () => void) {
  useEffect(() => {
    const id = setInterval(() => {
      if (!pushIsWorking()) onChange();
    }, FALLBACK_MS);
    return () => clearInterval(id);
  }, [onChange]);
}

export function useLiveChanges(subjects: string[], onChange: () => void) {
  useEffect(() => {
    const wanted = new Set(subjects);
    const listener: Listener = (subject) => {
      if (wanted.has(subject)) onChange();
    };

    listeners.add(listener);
    connect();

    return () => {
      listeners.delete(listener);
      if (listeners.size === 0) {
        stream?.close();
        stream = null;
      }
    };
    // The subjects are constant per screen; re-subscribing on every render of
    // a changing callback would close and reopen the stream continuously.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [subjects.join(","), onChange]);
}
