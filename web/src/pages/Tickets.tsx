import { useCallback, useEffect, useMemo, useState } from "react";
import { NotProvided, work, type Actor, type Ticket } from "../api";
import type { Route, TicketOwner, TicketSort } from "../router";
import { useFallbackPoll, useLiveChanges } from "../live";
import { hasChanged, seenChange, useChangedTickets } from "../watch";
import { matcher, useHelpdeskSchema } from "../whoami";
import { Life } from "../charts";
import { cn } from "@/lib/cn";
import { Chip, Empty, Failure, Icon, Label, Loading, daysSince, isDone, priorityRank, prioritySignal, since, statusTone } from "../ui";

/**
 * Every ticket, searchable.
 *
 * A table rather than the triage cards, because this screen answers "find me
 * the one about the UPS" rather than "what should I do next" — and for looking
 * something up, density beats emphasis.
 *
 * It opens on open work. Resolved tickets used to arrive by default and they
 * are the one thing on this screen nobody can act on; worse, they were filling
 * the page budget, so the open ticket from three months ago never arrived at
 * all. The helpdesk now does that filtering, reading further into its own list
 * to make up the difference — which is why the count at the bottom talks about
 * how much was read rather than pretending to know a total.
 */

/** How long a ticket sits untouched before the wait is the story. */
const QUIET_DAYS = 5;
const ABANDONED_DAYS = 14;

/** One screenful. More arrives on request rather than on scroll. */
const PER_PAGE = 100;


export function Tickets({
  actor,
  query,
  status,
  includeDone,
  owner = "yours",
  sort = "idle",
  go,
}: {
  actor: Actor;
  query?: string;
  status?: string;
  includeDone?: boolean;
  owner?: TicketOwner;
  sort?: TicketSort;
  go: (to: Route, replace?: boolean) => void;
}) {
  const [tickets, setTickets] = useState<Ticket[] | null>(null);
  // The caught value rather than its message: whether this was an outage or
  // a refusal decides how it is shown, and a string throws that away.
  const [error, setError] = useState<unknown>(null);
  const [missing, setMissing] = useState(false);
  const [draft, setDraft] = useState(query ?? "");
  const [busy, setBusy] = useState(false);
  // How far into the helpdesk's own list this has read, and whether there is
  // any more of it. Both come from the helpdesk rather than being counted here.
  const [readTo, setReadTo] = useState(1);
  const [more, setMore] = useState(false);
  // Subscribing is what makes a row redraw when it is marked as changed.
  useChangedTickets();

  useEffect(() => setDraft(query ?? ""), [query]);

  // Asking for one status by name means you want that status, including a
  // finished one. The open-work default is for when you have not said.
  const openOnly = !includeDone && !status;

  const fetchPage = useCallback(
    async (page: number, refresh: boolean) => {
      return work.searchTickets(
        { query, status, open_only: openOnly || undefined, page, per_page: PER_PAGE },
        refresh,
      );
    },
    [query, status, openOnly],
  );

  const load = useCallback(
    async (refresh = false) => {
      setBusy(true);
      try {
        const answer = await fetchPage(1, refresh);
        setTickets(answer.data.items ?? []);
        setReadTo(answer.data.page?.page ?? 1);
        setMore((answer.data.page?.page ?? 1) < (answer.data.page?.total_pages ?? 1));
        setError(null);
        setMissing(false);
      } catch (e) {
        if (e instanceof NotProvided) setMissing(true);
        else setError(e ?? "That search could not be run.");
      } finally {
        setBusy(false);
      }
    },
    [fetchPage],
  );

  useEffect(() => {
    setTickets(null);
    void load();
  }, [load]);

  // The list is worth keeping current for the same reason the queue is: a
  // technician leaves it open. Refreshed when the helpdesk says something
  // moved, and on a slow timer underneath in case nobody wired the webhook up.
  const refresh = useCallback(() => void load(true), [load]);
  useLiveChanges(["ticket"], refresh);
  useFallbackPoll(refresh);

  async function loadMore() {
    setBusy(true);
    try {
      const answer = await fetchPage(readTo + 1, false);
      const next = answer.data.items ?? [];
      // Merged by id rather than concatenated: a ticket can move between pages
      // while somebody is reading, and the same row twice is a bug report.
      setTickets((held) => {
        const seen = new Map((held ?? []).map((t) => [t.id, t]));
        next.forEach((t) => seen.set(t.id, t));
        return [...seen.values()];
      });
      setReadTo(answer.data.page?.page ?? readTo + 1);
      setMore((answer.data.page?.page ?? 0) < (answer.data.page?.total_pages ?? 0));
    } catch (e) {
      setError(e ?? "More tickets could not be loaded.");
    } finally {
      setBusy(false);
    }
  }

  // The statuses this deployment actually defines, and the technicians it knows
  // about. Reading statuses off the visible rows meant a status that happened
  // not to be on screen could not be filtered for — including, once the
  // finished ones are hidden by default, every finished status there is.
  const { statuses: defined, technicians } = useHelpdeskSchema();

  const statuses = useMemo(() => {
    if (defined && defined.length > 0) return defined;
    const seen = new Set<string>();
    (tickets ?? []).forEach((t) => t.status && seen.add(t.status));
    if (status) seen.add(status);
    return [...seen].sort();
  }, [defined, tickets, status]);

  const me = useMemo(() => matcher(actor, technicians), [actor, technicians]);

  const order = useMemo(
    () =>
      ({
        // Longest untouched first. Whatever order the helpdesk returned is the
        // order things happened in, which is not the order they matter in.
        idle: (a: Ticket, b: Ticket) =>
          new Date(a.updated_at ?? 0).getTime() - new Date(b.updated_at ?? 0).getTime(),
        newest: (a: Ticket, b: Ticket) =>
          new Date(b.created_at ?? 0).getTime() - new Date(a.created_at ?? 0).getTime(),
        priority: (a: Ticket, b: Ticket) => {
          const rank = { urgent: 0, high: 1, normal: 2, low: 3 };
          const gap = rank[priorityRank(a.priority)] - rank[priorityRank(b.priority)];
          return gap !== 0
            ? gap
            : new Date(a.updated_at ?? 0).getTime() - new Date(b.updated_at ?? 0).getTime();
        },
      })[sort],
    [sort],
  );

  /**
   * The list, in the order it is read.
   *
   * Yours first, then nobody's. Somebody else's ticket is not work you can pick
   * up, and a queue that mixes the three is a queue you scan past instead of
   * working. The whole helpdesk is one control away when the question changes.
   */
  const groups = useMemo(() => {
    const rows = [...(tickets ?? [])].sort(order);
    if (owner === "everyone") {
      return [{ key: "all", title: "Every ticket", tickets: rows, hint: "" }];
    }
    const mine = rows.filter(me.isMine);
    const nobody = rows.filter((t) => !t.assigned_to?.trim());
    return [
      { key: "mine", title: "Assigned to you", tickets: mine, hint: "" },
      {
        key: "unassigned",
        title: "Unassigned",
        tickets: nobody,
        hint: "nobody has picked these up",
      },
    ];
  }, [tickets, owner, order, me]);

  const count = groups.reduce((n, g) => n + g.tickets.length, 0);
  const hidden = (tickets?.length ?? 0) - count;

  // In the grouped view every row is either yours or nobody's, and the group
  // heading already says which. The column would be 130px of the same two
  // answers repeated down the page.
  const showAssignee = owner === "everyone";
  const columns = showAssignee ? 7 : 6;

  // One scale for every life bar on screen, so their lengths mean something
  // relative to each other rather than each being drawn against itself.
  const longestLife = useMemo(
    () => Math.max(1, ...(tickets ?? []).map((t) => daysSince(t.created_at))),
    [tickets],
  );

  function submit(e: React.FormEvent) {
    e.preventDefault();
    go({ name: "tickets", query: draft.trim() || undefined, status, includeDone, owner, sort });
  }

  /** Changes one part of the filter, leaving the rest of it alone. */
  const set = (patch: Partial<Extract<Route, { name: "tickets" }>>) =>
    go({ name: "tickets", query, status, includeDone, owner, sort, ...patch });

  return (
    <div className="mx-auto max-w-[1180px] px-6 py-6">
      <div className="mb-4 flex items-baseline justify-between gap-4">
        <h1 className="text-2xl font-semibold tracking-tight">Tickets</h1>
        {tickets && (
          <Label>
            {count} {openOnly ? "open" : "shown"}
            {hidden > 0 && ` · ${hidden} on other people`}
          </Label>
        )}
      </div>

      <div className="mb-4 flex flex-col gap-2">
        <form className="flex flex-wrap items-center gap-2" onSubmit={submit}>
          <div className="relative flex min-w-64 flex-1 items-center">
            <span className="pointer-events-none absolute left-3 text-ink-faint">
              <Icon.search />
            </span>
            <input
              className="h-8 w-full rounded-md border border-edge bg-sunken pl-9 pr-3 text-sm placeholder:text-ink-faint focus-visible:border-azir focus-visible:outline-none"
              value={draft}
              placeholder="Search tickets by subject or message…"
              aria-label="Search tickets"
              onChange={(e) => setDraft(e.target.value)}
            />
          </div>

          {/* Open work or everything. A segmented pair rather than a checkbox
              because it is the single most consequential thing on this bar, and
              because both halves should be readable without operating them. */}
          <Segmented
            label="Which tickets"
            value={includeDone ? "all" : "open"}
            options={[
              { value: "open", label: "Open" },
              { value: "all", label: "All" },
            ]}
            onChange={(v) => set({ includeDone: v === "all" || undefined, status: undefined })}
          />

          <Segmented
            label="Whose queue"
            value={owner}
            options={[
              { value: "yours", label: "Yours" },
              { value: "everyone", label: "Everyone" },
            ]}
            onChange={(v) => set({ owner: v as TicketOwner })}
          />

          {/* Picking a status by name means you want that status, finished ones
              included — so the Open/All control follows rather than fighting it. */}
          <select
            className="h-8 w-auto rounded-md border border-edge bg-sunken px-2 text-sm focus-visible:border-azir focus-visible:outline-none"
            value={status ?? ""}
            aria-label="Filter by status"
            onChange={(e) =>
              set({
                status: e.target.value || undefined,
                includeDone: !!e.target.value || undefined,
              })
            }
          >
            <option value="">Any status</option>
            {statuses.map((s) => (
              <option key={s} value={s}>
                {s}
              </option>
            ))}
          </select>

          <select
            className="h-8 w-auto rounded-md border border-edge bg-sunken px-2 text-sm focus-visible:border-azir focus-visible:outline-none"
            value={sort}
            aria-label="Order"
            onChange={(e) => set({ sort: e.target.value as TicketSort })}
          >
            <option value="idle">Longest untouched</option>
            <option value="newest">Newest first</option>
            <option value="priority">Highest priority</option>
          </select>

          {(query || status || includeDone || owner !== "yours" || sort !== "idle") && (
            <button
              type="button"
              className="h-8 rounded-md px-2.5 text-xs text-ink-dim transition-colors hover:bg-sunken hover:text-ink"
              onClick={() => go({ name: "tickets" })}
            >
              Reset
            </button>
          )}
        </form>

        {/* What the filter above actually did, in the case that is easy to
            mistake for an empty helpdesk. */}
        {status && (
          <p className="text-xs text-ink-faint">
            Showing only tickets marked <strong className="font-medium text-ink-dim">{status}</strong>
            {isDone(status) && " — finished work, which the list otherwise leaves out"}.
          </p>
        )}
      </div>

      {missing && <Empty headline="No helpdesk is connected yet" />}
      <Failure error={error} onRetry={() => void load(true)} busy={busy} />
      {!tickets && !error && !missing && <Loading rows={8} />}

      {tickets && count === 0 && (
        <Empty headline={query ? "Nothing matched" : "Nothing to pick up"}>
          {query
            ? `No ${openOnly ? "open " : ""}ticket matches "${query}".`
            : owner === "yours"
              ? "Nothing is assigned to you and nothing is waiting to be picked up. Switch to Everyone to see the rest of the queue."
              : openOnly
                ? "There is no open work. Switch to All to see finished tickets."
                : "There are no tickets to show."}
        </Empty>
      )}

      {tickets && count > 0 && (
        <>
          <div className="overflow-x-auto">
            <table className="w-full border-collapse text-sm">
              <colgroup>
                <col style={{ width: 4 }} />
                <col style={{ width: 68 }} />
                <col />
                <col style={{ width: 170 }} />
                {showAssignee && <col style={{ width: 130 }} />}
                <col style={{ width: 140 }} />
                <col style={{ width: 148 }} />
              </colgroup>
              <thead>
                <tr className="border-b border-edge">
                  <th className="sr-only">Priority</th>
                  <th className="px-2 pb-2 text-left font-mono text-2xs font-medium uppercase tracking-[0.09em] text-ink-faint">
                    Ref
                  </th>
                  {["Subject", "Customer", ...(showAssignee ? ["Assigned"] : []), "Status"].map(
                    (head) => (
                      <th
                        key={head}
                        className="px-2 pb-2 text-left font-mono text-2xs font-medium uppercase tracking-[0.09em] text-ink-faint"
                      >
                        {head}
                      </th>
                    ),
                  )}
                  <th className="px-2 pb-2 text-right font-mono text-2xs font-medium uppercase tracking-[0.09em] text-ink-faint">
                    Idle
                  </th>
                </tr>
              </thead>
              {groups.map((group) => (
                <tbody key={group.key}>
                  {/* An empty group still says its name, because "nothing is
                      assigned to you" is an answer somebody came here for. */}
                  {groups.length > 1 && (
                    <tr>
                      <td colSpan={columns} className="pb-1.5 pt-5 first:pt-2">
                        <div className="flex items-baseline gap-2">
                          <Label className="text-ink-dim">{group.title}</Label>
                          <Label>{group.tickets.length}</Label>
                          {group.hint && group.tickets.length > 0 && (
                            <Label className="font-sans normal-case tracking-normal">
                              {group.hint}
                            </Label>
                          )}
                        </div>
                      </td>
                    </tr>
                  )}
                  {group.tickets.length === 0 && (
                    <tr>
                      <td colSpan={columns} className="px-2 pb-2 text-xs text-ink-faint">
                        {group.key === "mine" ? "Nothing is on you." : "Everything has an owner."}
                      </td>
                    </tr>
                  )}
                  {group.tickets.map((t) => (
                    <Row
                      key={t.id}
                      ticket={t}
                      showAssignee={showAssignee}
                      longestLife={longestLife}
                      go={go}
                    />
                  ))}
                </tbody>
              ))}
            </table>
          </div>

          {/* How much of the helpdesk's list this actually read. The screen
              cannot know how many open tickets exist without reading all of
              them, so it says what it did rather than inventing a total. */}
          <div className="mt-4 flex items-center justify-between gap-4 border-t border-edge pt-3">
            <Label>
              {readTo === 1
                ? `From the first page of the helpdesk's list`
                : `Read ${readTo} pages of the helpdesk's list`}
            </Label>
            {more ? (
              <button
                className="h-8 rounded-md border border-edge bg-panel px-3 text-xs font-medium transition-colors hover:bg-sunken disabled:opacity-50"
                onClick={() => void loadMore()}
                disabled={busy}
              >
                {busy ? "Reading…" : "Read further back"}
              </button>
            ) : (
              <Label>That is all of it</Label>
            )}
          </div>
        </>
      )}
    </div>
  );
}

/** One row. Split out so the mark-as-seen effect belongs to the row it is about. */
function Row({
  ticket: t,
  showAssignee,
  longestLife,
  go,
}: {
  ticket: Ticket;
  showAssignee: boolean;
  longestLife: number;
  go: (to: Route) => void;
}) {
  const idle = daysSince(t.updated_at);
  const age = daysSince(t.created_at);
  const moved = hasChanged(t.id);
  const priority = prioritySignal(t.priority);
  const unassigned = !t.assigned_to?.trim();

  const open = () => {
    seenChange(t.id);
    go({ name: "ticket", id: String(t.id) });
  };

  return (
    <tr
      tabIndex={0}
      // A row that moved while you were reading something else warms once and
      // settles. Keyed on the ticket so a refresh that changes nothing does not
      // re-run it, which would turn a signal into wallpaper.
      className={cn(
        "cursor-pointer border-b border-edge/60 transition-colors hover:bg-sunken/70 focus-visible:bg-sunken focus-visible:outline-none",
        moved && "just-changed",
      )}
      onClick={open}
      onKeyDown={(e) => e.key === "Enter" && open()}
    >
      {/* Priority as a rail rather than as a word: a column being scanned
          vertically is read by shape, and only the two ranks that change what
          you do next get one. */}
      <td className="p-0">
        <span
          className={cn(
            "block h-8 w-[3px] rounded-full",
            priority === "critical"
              ? "bg-critical"
              : priority === "attention"
                ? "bg-attention"
                : "bg-transparent",
          )}
          title={t.priority ? `${t.priority} priority` : undefined}
        />
      </td>
      <td className="px-2 py-2.5 font-mono text-2xs tabular-nums text-ink-faint">
        {t.number || t.id}
      </td>
      <td className="max-w-0 px-2 py-2.5">
        <div className="flex items-center gap-2">
          {moved && (
            <span
              className="size-1.5 shrink-0 rounded-full bg-azir"
              title="Something moved on this ticket since you started looking"
            />
          )}
          <span className={cn("truncate", moved ? "font-semibold" : "font-medium")}>
            {t.subject}
          </span>
        </div>
      </td>
      <td className="max-w-0 truncate px-2 py-2.5 text-xs text-ink-dim">{t.customer || "—"}</td>
      {showAssignee && (
        <td className="max-w-0 truncate px-2 py-2.5 text-xs">
          {unassigned ? (
            <span className="text-attention">Unassigned</span>
          ) : (
            <span className="text-ink-dim">{t.assigned_to}</span>
          )}
        </td>
      )}
      <td className="px-2 py-2.5">
        {t.status && <Chip tone={statusTone(t.status)}>{t.status}</Chip>}
      </td>
      {/* Idle time is coloured because a number the reader has to evaluate for
          themselves is a number they skip. */}
      <td
        className="px-2 py-2.5 text-right"
        title={t.updated_at ? `Last activity ${new Date(t.updated_at).toLocaleString()}` : undefined}
      >
        <span className="flex items-center justify-end gap-2">
          {/* How long it has been alive, and how much of that was silence. The
              number alone cannot tell a three-day ticket answered yesterday
              from a three-day ticket nobody has touched. */}
          <Life ageDays={age} silentDays={idle} longest={longestLife} />
          <span
            className={cn(
              "font-mono text-xs tabular-nums",
              idle >= ABANDONED_DAYS
                ? "text-critical"
                : idle >= QUIET_DAYS
                  ? "text-attention"
                  : "text-ink-dim",
            )}
          >
            {since(t.updated_at)}
          </span>
        </span>
      </td>
    </tr>
  );
}

/** A small set of mutually exclusive choices, all of them readable at once. */
function Segmented({
  label,
  value,
  options,
  onChange,
}: {
  label: string;
  value: string;
  options: { value: string; label: string }[];
  onChange: (value: string) => void;
}) {
  return (
    <div
      role="group"
      aria-label={label}
      className="flex h-8 items-center gap-px rounded-md border border-edge bg-sunken p-px"
    >
      {options.map((o) => (
        <button
          key={o.value}
          type="button"
          aria-pressed={value === o.value}
          className={cn(
            "h-full rounded-[5px] px-2.5 text-xs font-medium transition-colors",
            value === o.value
              ? "bg-panel text-ink shadow-e1"
              : "text-ink-dim hover:text-ink",
          )}
          onClick={() => onChange(o.value)}
        >
          {o.label}
        </button>
      ))}
    </div>
  );
}
