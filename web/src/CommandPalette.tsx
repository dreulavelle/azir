import { useEffect, useRef, useState } from "react";
import { work, type Ticket } from "./api";
import type { Route } from "./router";
import { Icon, statusTone, Chip } from "./ui";

/**
 * Cmd+K.
 *
 * The research on keyboard-first tools is unanimous about why this earns its
 * place: it removes navigation depth entirely. Someone who knows the ticket
 * they want should never have to go to a list to find it.
 *
 * Ticket search runs against the connected system, so this is a real search
 * rather than a filter over what happens to be loaded.
 */

type Item = {
  id: string;
  label: string;
  hint?: string;
  tone?: "good" | "warn" | "urgent" | "accent" | "";
  group: string;
  run: () => void;
};

export function CommandPalette({
  open,
  onClose,
  go,
  seed = "",
}: {
  open: boolean;
  onClose: () => void;
  go: (to: Route) => void;
  /** What was already typed in the top bar, carried in so nothing is lost. */
  seed?: string;
}) {
  const [term, setTerm] = useState("");
  const [hits, setHits] = useState<Ticket[]>([]);
  const [cursor, setCursor] = useState(0);
  const [searching, setSearching] = useState(false);
  const inputRef = useRef<HTMLInputElement>(null);

  useEffect(() => {
    if (open) {
      setTerm(seed);
      setHits([]);
      setCursor(0);
    }
    // seed is deliberately not a dependency: it is the value at the moment of
    // opening, and reacting to it later would fight whatever is being typed.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open]);

  // Debounced, because a request per keystroke would hammer the connected
  // system for answers nobody reads.
  useEffect(() => {
    const q = term.trim();
    if (q.length < 2) {
      setHits([]);
      return;
    }
    setSearching(true);
    const timer = setTimeout(async () => {
      try {
        const answer = await work.searchTickets({ query: q, per_page: 8 });
        setHits(answer.data.items ?? []);
      } catch {
        setHits([]); // a search that cannot run simply offers nothing
      } finally {
        setSearching(false);
      }
    }, 220);
    return () => clearTimeout(timer);
  }, [term]);

  const go_ = (to: Route) => {
    onClose();
    go(to);
  };

  const navigation: Item[] = [
    { id: "nav-triage", label: "Triage", hint: "the queue", group: "Go to", run: () => go_({ name: "triage" }) },
    { id: "nav-tickets", label: "Tickets", group: "Go to", run: () => go_({ name: "tickets" }) },
    { id: "nav-customers", label: "Customers", group: "Go to", run: () => go_({ name: "customers" }) },
    { id: "nav-settings", label: "Settings", group: "Go to", run: () => go_({ name: "settings" }) },
  ].filter((i) => !term || i.label.toLowerCase().includes(term.toLowerCase()));

  const ticketItems: Item[] = hits.map((t) => ({
    id: `t-${t.id}`,
    label: t.subject,
    hint: t.customer,
    tone: statusTone(t.status),
    group: "Tickets",
    run: () => go_({ name: "ticket", id: String(t.id) }),
  }));

  const searchAll: Item[] = term.trim()
    ? [
        {
          id: "search-all",
          label: `Search all tickets for "${term.trim()}"`,
          group: "Tickets",
          run: () => go_({ name: "tickets", query: term.trim() }),
        },
      ]
    : [];

  const items = [...navigation, ...ticketItems, ...searchAll];
  const active = items[Math.min(cursor, items.length - 1)];

  useEffect(() => setCursor(0), [term]);

  if (!open) return null;

  function onKeyDown(e: React.KeyboardEvent) {
    if (e.key === "Escape") {
      e.preventDefault();
      onClose();
    } else if (e.key === "ArrowDown") {
      e.preventDefault();
      setCursor((c) => Math.min(c + 1, items.length - 1));
    } else if (e.key === "ArrowUp") {
      e.preventDefault();
      setCursor((c) => Math.max(c - 1, 0));
    } else if (e.key === "Enter") {
      e.preventDefault();
      active?.run();
    }
  }

  let lastGroup = "";

  return (
    <div
      className="fixed inset-0 z-50 flex items-start justify-center bg-black/50 px-4 pt-[12vh] backdrop-blur-[2px]"
      role="presentation"
      onMouseDown={(e) => e.target === e.currentTarget && onClose()}
    >
      <div className="w-full max-w-xl overflow-hidden rounded-xl border border-edge bg-raised shadow-e3" role="dialog" aria-modal="true" aria-label="Command palette">
        {/* autoFocus rather than focusing on the next frame: the palette is
            opened by someone who is already typing, and a frame of delay drops
            their first characters. It mounts fresh each time it opens, so this
            fires on every open. */}
        <input
          ref={inputRef}
          autoFocus
          className="w-full border-b border-edge bg-transparent px-4 py-3.5 text-base placeholder:text-ink-faint focus:outline-none"
          placeholder="Search tickets, or jump to a page…"
          value={term}
          aria-label="Search tickets or jump to a page"
          onChange={(e) => setTerm(e.target.value)}
          onKeyDown={onKeyDown}
        />
        <div className="max-h-[52vh] overflow-y-auto p-1.5" role="listbox">
          {items.length === 0 && (
            <p className="px-3 py-3.5 text-xs text-ink-faint">
              {searching ? "Searching…" : "Nothing matches."}
            </p>
          )}
          {items.map((item, i) => {
            const header = item.group !== lastGroup ? item.group : null;
            lastGroup = item.group;
            return (
              <div key={item.id}>
                {header && <div className="px-3 pb-1 pt-3 font-mono text-2xs font-medium uppercase tracking-[0.09em] text-ink-faint">{header}</div>}
                <button
                  className="flex w-full items-center gap-2.5 rounded-md px-3 py-2 text-left text-sm transition-colors aria-selected:bg-sunken hover:bg-sunken"
                  role="option"
                  aria-selected={item === active}
                  onMouseEnter={() => setCursor(i)}
                  onClick={item.run}
                >
                  <span className="grid place-items-center opacity-60">
                    {item.group === "Go to" ? <Icon.triage /> : <Icon.ticket />}
                  </span>
                  <span className="min-w-0 flex-1 truncate">{item.label}</span>
                  {item.tone && <Chip tone={item.tone}>&nbsp;</Chip>}
                  {item.hint && <span className="shrink-0 truncate text-xs text-ink-faint">{item.hint}</span>}
                </button>
              </div>
            );
          })}
        </div>
      </div>
    </div>
  );
}
