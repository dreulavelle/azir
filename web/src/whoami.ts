import { useEffect, useState } from "react";
import { work, type Actor, type Technician, type Ticket } from "./api";

/**
 * What the helpdesk will accept on a ticket: its statuses, and who tickets can
 * be assigned to.
 *
 * One request for both, because both come from one capability and two screens
 * asking the same question twice is two round trips for one answer. `ready`
 * separates "we have not looked yet" from "we looked and there is nothing" —
 * they lead to different things being said on screen.
 */
export function useHelpdeskSchema(): {
  statuses: string[] | null;
  technicians: Technician[] | null;
  ready: boolean;
} {
  const [state, setState] = useState<{
    statuses: string[] | null;
    technicians: Technician[] | null;
  }>({ statuses: null, technicians: null });
  const [ready, setReady] = useState(false);

  useEffect(() => {
    let cancelled = false;
    work
      .schema()
      .then((answer) => {
        if (cancelled) return;
        setState({
          statuses: answer.data.statuses ?? [],
          technicians: answer.data.technicians ?? [],
        });
      })
      .catch(() => {})
      .finally(() => !cancelled && setReady(true));
    return () => {
      cancelled = true;
    };
  }, []);

  return { ...state, ready };
}

/**
 * Matching the person signed in to the technician a ticket is assigned to.
 *
 * These are two different systems' idea of the same human. Azir has an account
 * with an address and a display name; the helpdesk has a technician record and
 * writes their name onto tickets. Nothing guarantees the two agree — an account
 * created as "Dreu" against a Syncro user called "Dreu Lavelle" is the normal
 * case, not the edge case.
 *
 * The failure that matters is the quiet one. If this cannot make the match, the
 * queue shows nothing under "Assigned to you" — which reads as good news and is
 * actually a broken screen. So resolution reports whether it succeeded, and the
 * screen says so rather than showing an empty list with no explanation.
 */

/** Lower-cased and trimmed, since neither system is consistent about either. */
function key(value?: string): string {
  return (value ?? "").trim().toLowerCase();
}

/** The part of an address before the @, which is often what a name field holds. */
function localPart(email?: string): string {
  const [local] = key(email).split("@");
  return local ?? "";
}

export type Match = {
  /** Whether a ticket is assigned to the signed-in person. */
  isMine: (ticket: Ticket) => boolean;
  /**
   * How the match was made, so the screen can be honest about it:
   * "email" and "name" mean a technician record was found in the helpdesk;
   * "guess" means we are comparing strings and could be wrong;
   * "none" means the helpdesk could not be asked at all.
   */
  by: "email" | "name" | "guess" | "none";
  /** The technician this resolved to, when one was found. */
  technician?: Technician;
};

/**
 * Works out which helpdesk technician the signed-in person is.
 *
 * An address match is the only reliable one, so it is tried first. A display
 * name match is accepted next because plenty of directories never populate an
 * address on the helpdesk side. Failing both, tickets are matched by comparing
 * the assignee string directly — which is a guess, and is labelled as one.
 */
export function matcher(actor: Actor, technicians: Technician[] | null): Match {
  const email = key(actor.email);
  const name = key(actor.display_name);

  const byEmail = technicians?.find((t) => t.email && key(t.email) === email);
  if (byEmail) {
    return { isMine: assignedTo(byEmail), by: "email", technician: byEmail };
  }

  const byName = technicians?.find((t) => key(t.name) === name);
  if (byName) {
    return { isMine: assignedTo(byName), by: "name", technician: byName };
  }

  // No technician record to anchor on. Compare what the ticket says against
  // everything we know the person by, and say that this is what we are doing.
  const known = new Set([name, email, localPart(actor.email)].filter(Boolean));
  return {
    isMine: (ticket) => {
      const assignee = key(ticket.assigned_to);
      return assignee !== "" && known.has(assignee);
    },
    by: technicians === null ? "none" : "guess",
  };
}

/** Tickets carrying this technician's name, or their address. */
function assignedTo(technician: Technician): (ticket: Ticket) => boolean {
  const names = new Set([key(technician.name), key(technician.email)].filter(Boolean));
  return (ticket) => {
    const assignee = key(ticket.assigned_to);
    return assignee !== "" && names.has(assignee);
  };
}

