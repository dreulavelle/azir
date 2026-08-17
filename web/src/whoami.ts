import { useEffect, useState } from "react";
import { work, type Actor, type Technician, type Ticket } from "./api";

/**
 * What the helpdesk will accept on a ticket: its statuses, and who tickets can
 * be assigned to.
 *
 * One request for both, because both come from one capability and two screens
 * asking the same question twice is two round trips for one answer.
 */
export function useHelpdeskSchema(): {
  statuses: string[] | null;
  technicians: Technician[] | null;
} {
  const [state, setState] = useState<{
    statuses: string[] | null;
    technicians: Technician[] | null;
  }>({ statuses: null, technicians: null });

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
      .catch(() => {});
    return () => {
      cancelled = true;
    };
  }, []);

  return state;
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
 * So the match is attempted three ways, best evidence first, and the last is a
 * comparison of plain strings. Nothing about which one succeeded reaches the
 * screen: a technician has no way to act on it and no reason to care which of
 * two systems has a field filled in. Unassigned tickets are always listed
 * beneath, and Everyone shows the whole queue, so an incomplete match costs a
 * click rather than hiding work behind a warning nobody asked for.
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
  /** The technician this resolved to, when the helpdesk had a record for one. */
  technician?: Technician;
};

/**
 * Works out which helpdesk technician the signed-in person is.
 *
 * An address match is the only reliable one, so it is tried first. A display
 * name match is accepted next because plenty of directories never populate an
 * address on the helpdesk side. Failing both, tickets are matched by comparing
 * the assignee string against everything we know the person by.
 */
export function matcher(actor: Actor, technicians: Technician[] | null): Match {
  const email = key(actor.email);
  const name = key(actor.display_name);

  const byEmail = technicians?.find((t) => t.email && key(t.email) === email);
  if (byEmail) {
    return { isMine: assignedTo(byEmail), technician: byEmail };
  }

  const byName = technicians?.find((t) => key(t.name) === name);
  if (byName) {
    return { isMine: assignedTo(byName), technician: byName };
  }

  // No technician record to anchor on. Compare what the ticket says against
  // everything we know the person by.
  const known = new Set([name, email, localPart(actor.email)].filter(Boolean));
  return {
    isMine: (ticket) => {
      const assignee = key(ticket.assigned_to);
      return assignee !== "" && known.has(assignee);
    },
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

