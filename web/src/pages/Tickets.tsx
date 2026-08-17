import { useEffect, useMemo, useState } from "react";
import { NotProvided, work, type Ticket } from "../api";
import type { Route } from "../router";
import { cn } from "@/lib/cn";
import { Chip, Empty, Icon, Label, Loading, Problem, priorityRank, since, statusTone } from "../ui";

/**
 * Every ticket, searchable.
 *
 * A table here rather than the triage cards, because this screen answers "find
 * me the one about the UPS" rather than "what should I do next" — and for
 * looking something up, density beats emphasis.
 */
export function Tickets({
  query,
  status,
  go,
}: {
  query?: string;
  status?: string;
  go: (to: Route, replace?: boolean) => void;
}) {
  const [tickets, setTickets] = useState<Ticket[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [missing, setMissing] = useState(false);
  const [draft, setDraft] = useState(query ?? "");

  useEffect(() => setDraft(query ?? ""), [query]);

  useEffect(() => {
    let cancelled = false;
    setTickets(null);
    (async () => {
      try {
        const answer = await work.searchTickets({ query, status, per_page: 100 });
        if (!cancelled) {
          setTickets(answer.data.items ?? []);
          setError(null);
          setMissing(false);
        }
      } catch (e) {
        if (cancelled) return;
        if (e instanceof NotProvided) setMissing(true);
        else setError(e instanceof Error ? e.message : "That search could not be run.");
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [query, status]);

  // Whatever statuses this deployment's system actually uses, learned from the
  // results rather than assumed. A PSA that invents its own statuses — Syncro
  // lets an account do exactly that — still gets working filters.
  const statuses = useMemo(() => {
    const seen = new Set<string>();
    (tickets ?? []).forEach((t) => t.status && seen.add(t.status));
    return [...seen].sort();
  }, [tickets]);

  function submit(e: React.FormEvent) {
    e.preventDefault();
    go({ name: "tickets", query: draft.trim() || undefined, status });
  }

  return (
    <div className="mx-auto max-w-[1180px] px-6 py-6">
      <div className="mb-4 flex items-baseline justify-between gap-4">
        <h1 className="text-2xl font-semibold tracking-tight">Tickets</h1>
        {tickets && <Label>{tickets.length} shown</Label>}
      </div>

      <form className="mb-4 flex flex-wrap items-center gap-2" onSubmit={submit}>
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
        <select
          className="h-8 rounded-md border border-edge bg-sunken px-2 text-sm focus-visible:border-azir focus-visible:outline-none"
          value={status ?? ""}
          aria-label="Filter by status"
          onChange={(e) =>
            go({ name: "tickets", query, status: e.target.value || undefined })
          }
        >
          <option value="">All statuses</option>
          {statuses.map((s) => (
            <option key={s} value={s}>
              {s}
            </option>
          ))}
        </select>
        {(query || status) && (
          <button
            type="button"
            className="h-8 rounded-md px-2.5 text-xs text-ink-dim transition-colors hover:bg-sunken hover:text-ink"
            onClick={() => go({ name: "tickets" })}
          >
            Clear
          </button>
        )}
      </form>

      {missing && <Empty headline="No helpdesk is connected yet" />}
      {error && <Problem>{error}</Problem>}
      {!tickets && !error && !missing && <Loading rows={8} />}

      {tickets && tickets.length === 0 && (
        <Empty headline="Nothing matched">
          {query ? `No ticket matches "${query}".` : "There are no tickets to show."}
        </Empty>
      )}

      {tickets && tickets.length > 0 && (
        <div className="overflow-x-auto">
          <table className="w-full border-collapse text-sm">
            <thead>
              <tr className="border-b border-edge">
                {["Ref", "Subject", "Customer", "Status", "Idle"].map((head, i) => (
                  <th
                    key={head}
                    className={cn(
                      "px-2 pb-2 font-mono text-2xs font-medium uppercase tracking-[0.09em] text-ink-faint",
                      i === 4 ? "text-right" : "text-left",
                    )}
                    style={{ width: [72, undefined, 180, 148, 80][i] }}
                  >
                    {head}
                  </th>
                ))}
              </tr>
            </thead>
            <tbody>
              {tickets.map((t) => (
                <tr
                  key={t.id}
                  tabIndex={0}
                  className="cursor-pointer border-b border-edge/60 transition-colors hover:bg-sunken/70 focus-visible:bg-sunken"
                  onClick={() => go({ name: "ticket", id: String(t.id) })}
                  onKeyDown={(e) => e.key === "Enter" && go({ name: "ticket", id: String(t.id) })}
                >
                  <td className="px-2 py-2.5 font-mono text-2xs tabular-nums text-ink-faint">
                    {t.number || t.id}
                  </td>
                  <td className="max-w-0 px-2 py-2.5">
                    <div className="flex items-center gap-2">
                      {/* The one dot on this screen: priority, which is the
                          only thing here that changes what you do next. */}
                      {priorityRank(t.priority) === "urgent" && (
                        <span
                          className="size-1.5 shrink-0 rounded-full bg-critical"
                          title={t.priority}
                        />
                      )}
                      <span className="truncate font-medium">{t.subject}</span>
                    </div>
                  </td>
                  <td className="max-w-0 truncate px-2 py-2.5 text-xs text-ink-dim">
                    {t.customer || "—"}
                  </td>
                  <td className="px-2 py-2.5">
                    {t.status && <Chip tone={statusTone(t.status)}>{t.status}</Chip>}
                  </td>
                  <td className="px-2 py-2.5 text-right font-mono text-xs tabular-nums text-ink-dim">
                    {since(t.updated_at)}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  );
}
