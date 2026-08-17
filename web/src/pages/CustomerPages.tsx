import { useCallback, useEffect, useMemo, useState } from "react";
import {
  api,
  customerName,
  NotProvided,
  Perm,
  work,
  type Actor,
  type Customer,
  type PhoneStatus,
  type Plugin,
  type Asset,
  type CustomerRecord,
  type Standing,
  type Ticket,
  type TimeEntry,
} from "../api";
import { ChevronDown } from "lucide-react";
import { cn } from "@/lib/cn";
import { PluginSettings } from "../Plugins";
import { Spark } from "../charts";
import { Explain, Tooltip } from "../components";
import { useToast } from "../Toast";
import type { Route } from "../router";
import { TicketRow } from "./Triage";
import {
  Empty,
  Icon,
  Loading,
  Panel,
  Problem,
  Stat,
  Label,
  ago,
  duration,
  initials,
  statusTone,
} from "../ui";

/** Everyone the connected systems know about. */
export function Customers({
  query,
  go,
}: {
  query?: string;
  go: (to: Route, replace?: boolean) => void;
}) {
  const [customers, setCustomers] = useState<CustomerRecord[] | null>(null);
  const [tickets, setTickets] = useState<Ticket[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [missing, setMissing] = useState(false);
  const [draft, setDraft] = useState(query ?? "");

  // Every ticket once, so each row can show its customer's recent shape without
  // a request per row. A list of forty customers should not be forty calls.
  useEffect(() => {
    (async () => {
      try {
        setTickets((await work.searchTickets({ per_page: 100 })).data.items ?? []);
      } catch {
        setTickets([]);
      }
    })();
  }, []);

  const activity = useMemo(() => {
    const byCustomer = new Map<number, number[]>();
    if (!tickets) return byCustomer;

    const WEEKS = 8;
    const now = Date.now();
    for (const t of tickets) {
      if (!t.customer_id) continue;
      const opened = new Date(t.created_at ?? "").getTime();
      if (Number.isNaN(opened)) continue;

      const weeksAgo = Math.floor((now - opened) / (7 * 86_400_000));
      if (weeksAgo < 0 || weeksAgo >= WEEKS) continue;

      const series = byCustomer.get(t.customer_id) ?? Array.from({ length: WEEKS }, () => 0);
      // Oldest week first, so the line reads left to right like everything else.
      series[WEEKS - 1 - weeksAgo]++;
      byCustomer.set(t.customer_id, series);
    }
    return byCustomer;
  }, [tickets]);

  useEffect(() => setDraft(query ?? ""), [query]);

  useEffect(() => {
    let cancelled = false;
    setCustomers(null);
    (async () => {
      try {
        const answer = await work.searchCustomers({ query, per_page: 100 });
        if (!cancelled) {
          setCustomers(answer.data.items ?? []);
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
  }, [query]);

  return (
    <div className="mx-auto max-w-[1180px] px-6 py-6">
      <div className="mb-4 flex items-center justify-between gap-3">
        <h1 className="text-2xl font-semibold tracking-tight">Customers</h1>
        {customers && (
          <span className="text-xs text-ink-faint">
            {customers.length} {customers.length === 1 ? "customer" : "customers"}
          </span>
        )}
      </div>

      <form
        className="mb-4 flex items-center gap-3"
        onSubmit={(e) => {
          e.preventDefault();
          go({ name: "customers", query: draft.trim() || undefined });
        }}
      >
        <div className="relative flex max-w-md items-center">
          <span className="pointer-events-none absolute left-3 text-ink-faint">
            <Icon.search />
          </span>
          <input
            className="h-8 w-full rounded-md border border-edge bg-sunken pl-9 pr-3 text-sm placeholder:text-ink-faint focus-visible:border-azir focus-visible:outline-none"
            value={draft}
            placeholder="Search by business, contact, email or phone…"
            aria-label="Search customers"
            onChange={(e) => setDraft(e.target.value)}
          />
        </div>
        {query && (
          <button type="button" className="rounded-md px-2 py-1 text-xs text-ink-dim transition-colors hover:bg-sunken hover:text-ink" onClick={() => go({ name: "customers" })}>
            Clear
          </button>
        )}
      </form>

      {missing && (
        <Empty headline="No customer list is available">
          Connect a system that knows your customers and they appear here.
        </Empty>
      )}
      {error && <Problem>{error}</Problem>}
      {!customers && !error && !missing && <Loading rows={8} />}

      {customers && customers.length === 0 && (
        <Empty headline="No customers matched">
          {query ? `Nothing matches "${query}".` : "There is nobody to show."}
        </Empty>
      )}

      {customers && customers.length > 0 && (
        <table className="w-full border-collapse text-sm">
          <thead>
            <tr>
              <th>Customer</th>
              <th style={{ width: 230 }}>Main contact</th>
              <th style={{ width: 130 }}>Phone</th>
              <th style={{ width: 110 }}>
                <span className="flex items-center gap-2">
                  Tickets
                  <Explain side="left">
                    Tickets opened week by week over the last two months. A line
                    climbing on the right is a customer who has started needing
                    more from you.
                  </Explain>
                </span>
              </th>
            </tr>
          </thead>
          <tbody>
            {customers.map((c) => (
              <tr
                key={c.id}
                tabIndex={0}
                onClick={() => go({ name: "customer", id: String(c.id) })}
                onKeyDown={(e) => e.key === "Enter" && go({ name: "customer", id: String(c.id) })}
              >
                <td>
                  <div className="flex items-center gap-3">
                    <span className="grid size-7 shrink-0 place-items-center rounded-md bg-sunken font-mono text-2xs font-semibold text-ink-dim">{initials(customerName(c))}</span>
                    <span className="font-medium">{customerName(c)}</span>
                  </div>
                </td>
                <td className="max-w-0 truncate text-xs text-ink-dim">
                  {c.business_name && c.name ? (
                    <>
                      {c.name}
                      {c.email && <span className="text-ink-faint"> · {c.email}</span>}
                    </>
                  ) : (
                    c.email || "—"
                  )}
                </td>
                <td className="text-ink-dim">{c.phone || "—"}</td>
                <td>
                  {activity.has(c.id) ? (
                    <Spark values={activity.get(c.id)!} />
                  ) : (
                    <span className="text-xs text-ink-faint">—</span>
                  )}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </div>
  );
}

/**
 * One customer, with everything about them in one place.
 *
 * The point of this page is to answer "how are we doing with these people"
 * before anyone reads a single ticket — so it leads with the figures and puts
 * the list underneath. Each panel loads independently and reports its own
 * absence, because a deployment whose key can read tickets but not invoices is
 * a normal configuration and should cost the invoice panel, not the page.
 */
export function CustomerDetail({
  id,
  go,
  actor,
}: {
  id: string;
  go: (to: Route) => void;
  actor: Actor;
}) {
  const numericId = Number(id);
  const [customer, setCustomer] = useState<CustomerRecord | null>(null);
  const [tickets, setTickets] = useState<Ticket[] | null>(null);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    setCustomer(null);
    setTickets(null);
    (async () => {
      try {
        setCustomer((await work.getCustomer(numericId)).data);
        setError(null);
      } catch (e) {
        setError(e instanceof Error ? e.message : "This customer could not be loaded.");
      }
    })();
    (async () => {
      try {
        setTickets((await work.searchTickets({ customer_id: numericId, per_page: 100 })).data.items ?? []);
      } catch {
        setTickets([]);
      }
    })();
  }, [numericId]);

  const figures = useMemo(() => {
    if (!tickets) return null;
    const open = tickets.filter((t) => statusTone(t.status) !== "good");
    const quiet = open.filter((t) => {
      const ms = Date.now() - new Date(t.updated_at ?? "").getTime();
      return !Number.isNaN(ms) && ms > 5 * 86_400_000;
    });
    return { total: tickets.length, open: open.length, quiet: quiet.length };
  }, [tickets]);

  return (
    <div className="mx-auto max-w-[1180px] px-6 py-6">
      <button
        className="mb-4 flex items-center gap-1 rounded-md px-1.5 py-1 text-xs text-ink-dim transition-colors hover:bg-sunken hover:text-ink"
        onClick={() => go({ name: "customers" })}
      >
        <Icon.back /> Customers
      </button>

      {error && <Problem>{error}</Problem>}
      {!customer && !error && <Loading rows={3} />}

      {customer && (
        <>
          <div className="mb-1 flex items-center gap-3">
            <span className="grid size-10 shrink-0 place-items-center rounded-lg bg-sunken font-mono text-sm font-semibold text-ink-dim">
              {initials(customerName(customer))}
            </span>
            <h1 className="text-2xl font-semibold tracking-tight">{customerName(customer)}</h1>
          </div>

          <div className="mb-6 ml-13 flex flex-wrap items-center gap-x-4 gap-y-1">
            {customer.business_name && customer.name && (
              <span className="text-xs text-ink-dim">{customer.name}</span>
            )}
            {customer.email && (
              <a className="text-xs text-ink-dim underline-offset-4 hover:text-ink hover:underline" href={`mailto:${customer.email}`}>
                {customer.email}
              </a>
            )}
            {customer.phone && (
              <a className="text-xs text-ink-dim" href={`tel:${customer.phone}`}>
                {customer.phone}
              </a>
            )}
            {customer.address && <span className="text-xs text-ink-faint">{customer.address}</span>}
          </div>

          {figures && (
            <div className="mb-6 grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
              <Panel className="p-4">
                <Stat value={figures.open} label="open tickets" />
              </Panel>
              <Panel className="p-4" rail={figures.quiet > 0 ? "attention" : undefined}>
                <Stat
                  value={figures.quiet}
                  label="gone quiet"
                  tone={figures.quiet > 0 ? "attention" : undefined}
                />
              </Panel>
              <Panel className="p-4">
                <Stat value={figures.total} label="tickets all time" />
              </Panel>
              <StandingMetric customerId={numericId} />
            </div>
          )}

          <LinkedPanels externalId={id} displayName={customerName(customer)} actor={actor} />

          <div className="grid gap-8 lg:grid-cols-[minmax(0,1fr)_320px]">
            <CustomerTickets tickets={tickets} go={go} />
            <aside className="flex flex-col gap-3">
              <CustomerEquipment customerId={numericId} />
              <RecentTime customerId={numericId} />
            </aside>
          </div>
        </>
      )}
    </div>
  );
}

/**
 * How this customer's phone system is doing.
 *
 * Loaded once when the page opens and then only when asked. Every refresh is a
 * real request against somebody's PBX, and a panel that polls turns one
 * customer page left open on a second monitor into a steady drip of traffic
 * against their phone system all day. The age of what is on screen is shown
 * instead, so nobody has to guess whether it is current.
 *
 * Absent entirely when this customer has no phone system connected — most
 * customers of most MSPs will not.
 */
function PhonePanel({ customerId }: { customerId: string }) {
  const [status, setStatus] = useState<PhoneStatus | null>(null);
  const [absent, setAbsent] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [readAt, setReadAt] = useState<string | null>(null);

  const load = useCallback(
    async (refresh: boolean) => {
      setBusy(true);
      try {
        const answer = await work.phoneStatus(customerId, refresh);
        setStatus(answer.data);
        setReadAt(new Date().toISOString());
        setError(null);
      } catch (e) {
        if (e instanceof NotProvided) setAbsent(true);
        // A customer with no PBX configured is the normal case, not a fault.
        else if (e instanceof Error && /which customer|no phone system address/i.test(e.message)) {
          setAbsent(true);
        } else setError(e instanceof Error ? e.message : "The phone system could not be read.");
      } finally {
        setBusy(false);
      }
    },
    [customerId],
  );

  useEffect(() => {
    void load(false);
  }, [load]);

  if (absent) return null;
  if (error) {
    return (
      <div className="mb-6">
        <Problem>{error}</Problem>
      </div>
    );
  }
  if (!status) return <div className="mb-6 h-28 animate-pulse rounded-lg bg-sunken" />;

  const licence = status.licence;
  const expires = licence?.expires ? new Date(licence.expires) : null;
  const days = expires ? Math.round((expires.getTime() - Date.now()) / 86_400_000) : null;

  const tiles: { value: string; label: string; tone?: "critical" | "attention" | "steady" }[] = [
    {
      value: `${status.extensions_registered}/${status.extensions_total}`,
      label: "handsets registered",
      tone:
        status.extensions_total > 0 && status.extensions_registered === 0
          ? "critical"
          : status.extensions_registered < status.extensions_total
            ? "attention"
            : "steady",
    },
    {
      value: `${status.trunks_registered}/${status.trunks_total}`,
      label: "trunks up",
      tone: status.trunks_registered < status.trunks_total ? "critical" : "steady",
    },
    {
      value: `${status.calls_in_progress}/${status.max_simultaneous_calls}`,
      label: "calls in progress",
    },
    {
      value: days === null ? "—" : days < 0 ? "expired" : `${days}d`,
      label: "licence left",
      tone: days === null ? undefined : days < 0 ? "critical" : days <= 14 ? "attention" : undefined,
    },
  ];

  return (
    <div className="mb-6">
      <div className="mb-2 flex items-baseline justify-between gap-3 border-b border-edge pb-2">
        <h2 className="text-sm font-semibold">Phone system</h2>
        <span
          className={cn(
            "inline-flex items-center gap-1.5 font-mono text-2xs uppercase tracking-[0.09em]",
            status.healthy ? "text-steady" : "text-critical",
          )}
        >
          <span className={cn("size-1.5 rounded-full", status.healthy ? "bg-steady" : "bg-critical")} />
          {status.healthy ? "healthy" : "needs attention"}
        </span>
        <span className="ml-auto flex items-center gap-2">
          {readAt && <Label>read {ago(readAt)}</Label>}
          <button
            className="flex h-7 items-center gap-1.5 rounded-md border border-edge bg-panel px-2.5 text-xs font-medium transition-colors hover:bg-sunken disabled:opacity-50"
            onClick={() => void load(true)}
            disabled={busy}
          >
            <span className={busy ? "animate-spin" : ""}>
              <Icon.refresh />
            </span>
            Refresh
          </button>
        </span>
      </div>

      <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
        {tiles.map((t) => (
          <Panel key={t.label} className="p-4" rail={t.tone === "steady" ? undefined : t.tone}>
            <Stat value={t.value} label={t.label} tone={t.tone} />
          </Panel>
        ))}
      </div>

      {status.concerns.length > 0 && (
        <ul className="mt-3 flex flex-col gap-1.5">
          {status.concerns.map((c: string) => (
            <li key={c} className="flex items-start gap-2 text-xs text-attention">
              <span className="mt-1.5 size-1.5 shrink-0 rounded-full bg-attention" />
              {c}
            </li>
          ))}
        </ul>
      )}

      <p className="mt-3 text-2xs text-ink-faint">
        {status.address} · 3CX {status.version}
        {licence?.product ? ` · ${licence.product}` : ""}
        {status.trunks_offline.length > 0 ? ` · offline: ${status.trunks_offline.join(", ")}` : ""}
      </p>
    </div>
  );
}

/**
 * The systems that belong to this customer rather than to the MSP.
 *
 * Most connections are one per deployment — an MSP has one Syncro tenant. A
 * phone system is not: every customer has their own, at their own address, with
 * their own credentials. Those settings therefore live on the customer.
 *
 * Which plugins appear here is decided by the plugins themselves, not by this
 * file: anything declaring a per-customer scope shows up, and its form is
 * rendered from the schema it publishes. Connecting a second per-customer
 * system — a different phone platform, a per-site RMM — is a plugin and no
 * frontend work at all.
 */
function ConnectedSystems({
  externalId,
  displayName,
  actor,
  linked,
  onLinked,
}: {
  /** The id the PSA knows this customer by, which is what the URL carries. */
  externalId: string;
  displayName: string;
  actor: Actor;
  /** Azir's own record for them: undefined while looking, null when none. */
  linked: Customer | null | undefined;
  onLinked: () => Promise<void>;
}) {
  const [plugins, setPlugins] = useState<Plugin[] | null>(null);
  const [open, setOpen] = useState<string | null>(null);
  const [linking, setLinking] = useState(false);
  const toast = useToast();

  useEffect(() => {
    let cancelled = false;
    api
      .registry()
      .then((r) => !cancelled && setPlugins(r.plugins.filter((p) => p.config_scope === "customer")))
      .catch(() => !cancelled && setPlugins([]));
    return () => {
      cancelled = true;
    };
  }, []);

  /**
   * Gives this customer a record of their own.
   *
   * Per-customer credentials have to hang off something, and the PSA's id is
   * not it: a deployment can change helpdesk, and the whole design is that
   * nothing upstream notices when it does. So Azir keeps its own customer and
   * remembers which id each connected system knows them by.
   */
  async function link() {
    setLinking(true);
    try {
      const created = await api.createCustomer(displayName);
      await api.linkIdentity(created.id, "psa", externalId);
      await onLinked();
      toast(`${displayName} now has a record in Azir`);
    } catch (e) {
      toast("Could not link this customer", {
        tone: "bad",
        detail: e instanceof Error ? e.message : undefined,
      });
    } finally {
      setLinking(false);
    }
  }

  if (!plugins || plugins.length === 0) return null;
  if (!actor.permissions.includes(Perm.pluginConfigure)) return null;

  return (
    <div className="mb-6">
      <div className="mb-2 flex items-baseline justify-between gap-3 border-b border-edge pb-2">
        <h2 className="text-sm font-semibold">Connected systems</h2>
        <Label>this customer only</Label>
      </div>

      {linked === null ? (
        // Said plainly rather than shown as a broken form. Holding credentials
        // for somebody requires a record of who they are.
        <div className="flex flex-wrap items-center justify-between gap-3 rounded-lg border border-dashed border-edge px-4 py-3.5">
          <p className="max-w-[62ch] text-xs text-ink-dim">
            {displayName} does not have a record in Azir yet. They need one before
            their own {plugins.map((p) => p.name).join(" and ")} details can be
            stored against them.
          </p>
          <button
            className="h-8 shrink-0 rounded-md bg-azir px-3 text-sm font-medium text-azir-ink transition-opacity hover:opacity-90 disabled:opacity-50"
            disabled={linking}
            onClick={() => void link()}
          >
            {linking ? "Linking…" : "Link this customer"}
          </button>
        </div>
      ) : linked === undefined ? (
        <div className="h-16 animate-pulse rounded-lg bg-sunken" />
      ) : (
        <div className="flex flex-col gap-2">
          {plugins.map((p) => (
            <section
              key={p.id}
              className={cn(
                "overflow-hidden rounded-lg border bg-panel shadow-e1 transition-colors",
                open === p.name ? "border-edge-strong" : "border-edge",
              )}
            >
              <button
                className="flex w-full items-center gap-3 px-4 py-3 text-left transition-colors hover:bg-sunken/60"
                onClick={() => setOpen(open === p.name ? null : p.name)}
                aria-expanded={open === p.name}
              >
                <span className="grid size-8 shrink-0 place-items-center rounded-md bg-sunken font-mono text-2xs font-semibold text-ink-dim">
                  {p.name.slice(0, 2).toUpperCase()}
                </span>
                <span className="flex min-w-0 flex-1 flex-col">
                  <span className="text-sm font-semibold">{p.name}</span>
                  <span className="truncate text-xs text-ink-faint">{p.description}</span>
                </span>
                <ChevronDown
                  className={cn(
                    "size-4 shrink-0 text-ink-faint transition-transform",
                    open === p.name && "rotate-180",
                  )}
                  aria-hidden="true"
                />
              </button>

              {open === p.name && (
                <div className="border-t border-edge px-4 py-4">
                  <PluginSettings plugin={p} customerId={linked.id} />
                </div>
              )}
            </section>
          ))}
        </div>
      )}
    </div>
  );
}

/**
 * Everything that depends on this customer having a record in Azir.
 *
 * Resolved once and shared, so the phone panel and the settings forms do not
 * each go looking for the same link.
 */
function LinkedPanels({
  externalId,
  displayName,
  actor,
}: {
  externalId: string;
  displayName: string;
  actor: Actor;
}) {
  const [linked, setLinked] = useState<Customer | null | undefined>(undefined);

  const findLink = useCallback(async () => {
    try {
      const all = await api.customers();
      setLinked(all.find((c) => c.identities.some((i) => i.external_id === externalId)) ?? null);
    } catch {
      setLinked(null);
    }
  }, [externalId]);

  useEffect(() => {
    void findLink();
  }, [findLink]);

  return (
    <>
      {linked && <PhonePanel customerId={linked.id} />}
      <ConnectedSystems
        externalId={externalId}
        displayName={displayName}
        actor={actor}
        linked={linked}
        onLinked={findLink}
      />
    </>
  );
}

/** What they owe. Its own component so a key without invoice access costs one tile. */
function StandingMetric({ customerId }: { customerId: number }) {
  const [standing, setStanding] = useState<Standing | null>(null);
  const [absent, setAbsent] = useState(false);

  useEffect(() => {
    (async () => {
      try {
        setStanding((await work.standing(customerId)).data);
      } catch {
        setAbsent(true);
      }
    })();
  }, [customerId]);

  if (absent) return null;
  if (!standing) return <div className="h-[74px] animate-pulse rounded-lg bg-sunken" />;

  const owed = (standing.balance_cents ?? 0) / 100;
  const overdue = (standing.overdue_cents ?? 0) / 100;
  const money = (n: number) =>
    n.toLocaleString(undefined, { style: "currency", currency: "USD", maximumFractionDigits: 0 });

  return (
    <Tooltip content={standing.note || "Summed from unpaid invoices."}>
      <Panel className="p-4" rail={overdue > 0 ? "critical" : undefined}>
        <Stat
          value={money(owed)}
          label={`outstanding${overdue > 0 ? ` · ${money(overdue)} overdue` : ""}`}
          tone={overdue > 0 ? "critical" : undefined}
        />
      </Panel>
    </Tooltip>
  );
}

function CustomerTickets({
  tickets,
  go,
}: {
  tickets: Ticket[] | null;
  go: (to: Route) => void;
}) {
  const [showResolved, setShowResolved] = useState(false);

  if (!tickets) {
    return (
      <section>
        <div className="mb-2 flex items-baseline justify-between gap-3 border-b border-edge pb-2">
          <h2 className="text-sm font-semibold">Tickets</h2>
        </div>
        <Loading rows={3} />
      </section>
    );
  }

  const open = tickets.filter((t) => statusTone(t.status) !== "good");
  const closed = tickets.filter((t) => statusTone(t.status) === "good");
  const shown = showResolved ? tickets : open;

  return (
    <section>
      <div className="mb-2 flex items-baseline justify-between gap-3 border-b border-edge pb-2">
        <h2 className="text-sm font-semibold">Tickets</h2>
        <span className="text-xs text-ink-faint">{open.length} open</span>
        {closed.length > 0 && (
          <button
            className="rounded-md px-2 py-1 text-xs text-ink-dim transition-colors hover:bg-sunken hover:text-ink"
            style={{ marginLeft: "auto" }}
            onClick={() => setShowResolved((v) => !v)}
          >
            {showResolved ? "Hide" : "Show"} {closed.length} resolved
          </button>
        )}
      </div>

      {shown.length === 0 ? (
        <Empty headline="Nothing open for this customer">
          {closed.length > 0
            ? `All ${closed.length} of their tickets are resolved.`
            : "They have never raised a ticket."}
        </Empty>
      ) : (
        /* The same row the queue uses: a ticket should not look like a
           different kind of thing depending on which screen found it. */
        <div className="flex flex-col">
          {shown.map((t) => (
            <TicketRow
              key={t.id}
              ticket={t}
              onOpen={() => go({ name: "ticket", id: String(t.id) })}
            />
          ))}
        </div>
      )}
    </section>
  );
}

function CustomerEquipment({ customerId }: { customerId: number }) {
  const [assets, setAssets] = useState<Asset[] | null>(null);
  const [absent, setAbsent] = useState<string | null>(null);
  const [showAll, setShowAll] = useState(false);

  useEffect(() => {
    (async () => {
      try {
        setAssets((await work.assets(customerId)).data.items ?? []);
      } catch (e) {
        setAbsent(
          e instanceof NotProvided
            ? "No connected system tracks equipment."
            : e instanceof Error
              ? e.message
              : "unavailable",
        );
      }
    })();
  }, [customerId]);

  if (absent) return null;

  const shown = assets && (showAll ? assets : assets.slice(0, 8));

  return (
    <div className="rounded-lg border border-edge bg-panel shadow-e1">
      <div className="flex items-baseline justify-between gap-3 border-b border-edge px-4 py-3">
        <h3 className="text-sm font-medium">Equipment</h3>
        {assets && <span className="text-xs text-ink-faint">{assets.length}</span>}
      </div>
      <div className="p-4">
        {!assets && <div className="h-14 animate-pulse rounded-lg bg-sunken" />}
        {assets && assets.length === 0 && (
          <p className="text-xs text-ink-faint">
            Nothing recorded for this customer.
          </p>
        )}
        {shown && shown.length > 0 && (
          <div className="flex flex-col gap-2.5">
            {shown.map((a) => (
              <div key={a.id} className="flex flex-col">
                <span className="truncate text-sm font-medium">
                  {a.name}
                </span>
                <span className="text-xs text-ink-faint">
                  {[a.type, a.serial].filter(Boolean).join(" · ") || "no details"}
                </span>
              </div>
            ))}
            {assets && assets.length > 8 && (
              <button className="rounded-md px-2 py-1 text-xs text-ink-dim transition-colors hover:bg-sunken hover:text-ink" onClick={() => setShowAll((v) => !v)}>
                {showAll ? "Show fewer" : `Show all ${assets.length}`}
              </button>
            )}
          </div>
        )}
      </div>
    </div>
  );
}

/** Work recorded against this customer, so "what have we done for them" has an answer. */
function RecentTime({ customerId }: { customerId: number }) {
  const [entries, setEntries] = useState<TimeEntry[] | null>(null);
  const [absent, setAbsent] = useState(false);

  useEffect(() => {
    (async () => {
      try {
        setEntries((await work.timeEntries({ customer_id: customerId, per_page: 50 })).data.items ?? []);
      } catch {
        setAbsent(true);
      }
    })();
  }, [customerId]);

  if (absent) return null;

  const total = (entries ?? []).reduce((n, e) => n + (e.minutes ?? 0), 0);

  return (
    <div className="rounded-lg border border-edge bg-panel shadow-e1">
      <div className="flex items-baseline justify-between gap-3 border-b border-edge px-4 py-3">
        <h3 className="text-sm font-medium">Recorded work</h3>
        {entries && entries.length > 0 && <span className="text-xs text-ink-faint">{duration(total)}</span>}
      </div>
      <div className="p-4">
        {!entries && <div className="animate-pulse rounded-lg bg-sunken" style={{ height: 44 }} />}
        {entries && entries.length === 0 && (
          <p className="text-xs text-ink-faint">
            No time has been logged against this customer.
          </p>
        )}
        {entries && entries.length > 0 && (
          <div className="flex flex-col gap-2" style={{ gap: 9 }}>
            {entries.slice(0, 6).map((e) => (
              <div key={e.id} className="flex items-center justify-between gap-3" style={{ gap: 10, alignItems: "baseline" }}>
                <span className="truncate text-xs">{e.notes || e.user || "Work logged"}</span>
                <span className="whitespace-nowrap text-xs text-ink-faint">{duration(e.minutes)}</span>
              </div>
            ))}
          </div>
        )}
      </div>
    </div>
  );
}
