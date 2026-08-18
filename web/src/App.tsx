import { lazy, Suspense, useCallback, useEffect, useState } from "react";
import {
  api,
  onSessionExpired,
  Perm,
  Unauthorized,
  work,
  type Actor,
  type AuthState,
} from "./api";
import { Gate } from "./Auth";
import { Dialog, Tooltip, TooltipLayer } from "./components";
import { useShortcuts, type Chord } from "./keyboard";
import { ToastHost } from "./Toast";
import { CommandPalette } from "./CommandPalette";
import { href, useRoute, type Route } from "./router";
import { useFallbackPoll, useLiveChanges } from "./live";
import { useTicketWatch } from "./watch";
import { cn } from "@/lib/cn";
import { BrandingProvider, Mark, useBranding } from "./branding";
import { Icon, initials, roleLabel, statusTone, type Signal } from "./ui";
import { Triage } from "./pages/Triage";
import { Tickets } from "./pages/Tickets";
import { TicketDetail } from "./pages/TicketDetail";
import { Customers, CustomerDetail } from "./pages/CustomerPages";
import { Chats } from "./pages/Chats";
import { Diagnostics } from "./pages/Diagnostics";
import { settingsTabsFor } from "./pages/settingsTabs";

/**
 * The administrative screens, fetched when somebody opens them.
 *
 * Plugins, assistant configuration, people, the customer spine and the audit
 * log are one screen a technician visits rarely and a large amount of code to
 * carry through a shift in the queue.
 */
const Settings = lazy(() => import("./pages/Settings").then((m) => ({ default: m.Settings })));
const BulkEdits = lazy(() => import("./pages/BulkEdits").then((m) => ({ default: m.BulkEdits })));

/**
 * The assistant, fetched the first time somebody opens it.
 *
 * It brings the whole markdown stack with it — remark, micromark, the mdast
 * and hast trees — which is the largest thing in the application and useless
 * until there is an answer to render. A technician who spends the morning in
 * the queue should not pay for it on the first paint.
 */
const AssistantPanel = lazy(() =>
  import("./Assistant").then((m) => ({ default: m.AssistantPanel })),
);


type Session =
  | { state: "loading" }
  | { state: "gate"; auth: AuthState }
  | { state: "in"; actor: Actor };

const NO_SSO: AuthState = { needs_setup: false, oidc_enabled: false, oidc_label: "" };

export function App() {
  return (
    <BrandingProvider>
      <TooltipLayer>
        <ToastHost>
          <Console />
        </ToastHost>
      </TooltipLayer>
    </BrandingProvider>
  );
}

function Console() {
  const { brand } = useBranding();
  const [session, setSession] = useState<Session>({ state: "loading" });
  const [route, go] = useRoute();
  const [paletteOpen, setPaletteOpen] = useState(false);
  const [openCount, setOpenCount] = useState<number | null>(null);
  const [assistantOpen, setAssistantOpen] = useState(false);
  const [helpOpen, setHelpOpen] = useState(false);
  const [seed, setSeed] = useState("");
  // A question a page handed to the assistant, rather than one typed into it.
  const [ask, setAsk] = useState("");

  const resolve = useCallback(async () => {
    try {
      setSession({ state: "in", actor: await api.me() });
      if (window.location.search.includes("sso_error")) {
        window.history.replaceState({}, "", window.location.pathname);
      }
    } catch (err) {
      if (!(err instanceof Unauthorized)) {
        setSession({ state: "gate", auth: NO_SSO });
        return;
      }
      try {
        setSession({ state: "gate", auth: await api.authState() });
      } catch {
        setSession({ state: "gate", auth: NO_SSO });
      }
    }
  }, []);

  useEffect(() => {
    void resolve();
  }, [resolve]);

  useEffect(() => {
    onSessionExpired(() => setSession({ state: "gate", auth: NO_SSO }));
  }, []);

  // The queue's size, shown in the rail so the pressure is visible from every
  // screen rather than only from the one that lists it.
  //
  // Read once and then only when the helpdesk says something changed. The
  // sixty-second timer this replaced was a request a minute against a
  // customer's PSA for every open tab, forever, whether anything had happened
  // or not — and still left the number up to a minute stale.
  const countOpen = useCallback(async () => {
    try {
      const answer = await work.searchTickets({ per_page: 100 });
      setOpenCount((answer.data.items ?? []).filter((t) => statusTone(t.status) !== "good").length);
    } catch {
      setOpenCount(null); // no PSA connected, or no permission
    }
  }, []);

  useEffect(() => {
    if (session.state !== "in") return;
    void countOpen();
  }, [session.state, countOpen]);

  useLiveChanges(["ticket"], countOpen);
  useFallbackPoll(countOpen);

  // Watches for what changed and says so, from any screen — the point is not
  // having to keep opening a ticket to find out whether anybody wrote back.
  useTicketWatch(go, session.state === "in", route);

  const chords: Chord[] = [
    { keys: ["g", "t"], label: "Go to Triage", group: "Navigate", run: () => go({ name: "triage" }) },
    { keys: ["g", "k"], label: "Go to Tickets", group: "Navigate", run: () => go({ name: "tickets" }) },
    { keys: ["g", "c"], label: "Go to Customers", group: "Navigate", run: () => go({ name: "customers" }) },
    { keys: ["g", "h"], label: "Go to your chats", group: "Navigate", run: () => go({ name: "chats" }) },
    { keys: ["g", "s"], label: "Go to Settings", group: "Navigate", run: () => go({ name: "settings" }) },
    { keys: "/", label: "Search", group: "Do", run: () => { setSeed(""); setPaletteOpen(true); } },
    { keys: "a", label: "Ask the assistant", group: "Do", run: () => setAssistantOpen((v) => !v) },
    { keys: "?", label: "Show this list", group: "Do", run: () => setHelpOpen(true) },
    { keys: "escape", run: () => { setHelpOpen(false); setPaletteOpen(false); } },
  ];

  useShortcuts(chords, session.state === "in");

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === "k") {
        e.preventDefault();
        setPaletteOpen((v) => !v);
      }
      if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === "j") {
        e.preventDefault();
        setAssistantOpen((v) => !v);
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, []);

  async function signOut() {
    await api.logout();
    try {
      setSession({ state: "gate", auth: await api.authState() });
    } catch {
      setSession({ state: "gate", auth: NO_SSO });
    }
  }

  if (session.state === "loading") {
    return (
      <div className="grid min-h-screen place-items-center">
        <div className="h-3 w-45 animate-pulse rounded bg-sunken" />
      </div>
    );
  }
  if (session.state === "gate") {
    return <Gate state={session.auth} onDone={() => void resolve()} />;
  }

  const { actor } = session;
  // Offered when there is anything behind it. A technician can see which
  // systems are connected and what Azir is allowed to read, which is a fair
  // question to be able to answer without asking an administrator.
  const canAdmin = settingsTabsFor(actor).length > 0;

  return (
    <div className="flex min-h-screen">
      <nav className="fixed inset-y-0 left-0 flex w-[232px] flex-col border-r border-edge bg-panel px-3 py-4">
        <a
          className="mb-6 flex items-center gap-2.5 px-2"
          href={href({ name: "triage" })}
          onClick={link(go, { name: "triage" })}
        >
          {/* The mark is the one place the brand colour appears without the
              assistant being involved — it is the product signing its name. */}
          <Mark size={28} />
          <span className="text-base font-semibold tracking-tight">{brand.effective_name}</span>
        </a>

        {/* Grouped, because Chats is a different kind of thing from a queue and
            an undifferentiated list of four says they are all the same. */}
        <NavGroup label="Queue" />
        <NavItem
          icon={<Icon.triage />}
          label="Triage"
          chord="G T"
          active={route.name === "triage"}
          count={openCount ?? undefined}
          onClick={() => go({ name: "triage" })}
        />
        <NavItem
          icon={<Icon.ticket />}
          label="Tickets"
          chord="G K"
          active={route.name === "tickets" || route.name === "ticket"}
          onClick={() => go({ name: "tickets" })}
        />
        <NavItem
          icon={<Icon.people />}
          label="Customers"
          chord="G C"
          active={route.name === "customers" || route.name === "customer"}
          onClick={() => go({ name: "customers" })}
        />

        {/* Tools are the things a technician opens to do a job, as opposed to
            the queue they work through. A section of their own because they are
            a different kind of thing, and because there will be more of them. */}
        <NavGroup label="Tools" />
        <NavItem
          icon={<Icon.diagnostics />}
          label="Diagnostics"
          active={route.name === "diagnostics" || route.name === "snapshot"}
          onClick={() => go({ name: "diagnostics" })}
        />
        {actor.permissions.includes(Perm.phoneManage) && (
          <NavItem
            icon={<Icon.people />}
            label="Bulk edits"
            active={route.name === "bulk"}
            onClick={() => go({ name: "bulk" })}
          />
        )}

        <NavGroup label="Assistant" />
        <NavItem
          icon={<Icon.spark />}
          label="Chats"
          chord="G H"
          active={route.name === "chats"}
          onClick={() => go({ name: "chats" })}
        />

        <div className="mt-auto flex flex-col gap-1 pt-3">
          {canAdmin && (
            <NavItem
              icon={<Icon.settings />}
              label="Settings"
              chord="G S"
              active={route.name === "settings"}
              onClick={() => go({ name: "settings" })}
            />
          )}

          {/* Who you are, and separately a way out. The whole block used to be
              one sign-out button, so reaching for your own name logged you out. */}
          <div className="mt-1 flex items-center gap-2.5 rounded-lg border border-edge bg-sunken/60 px-2.5 py-2">
            <span className="grid size-7 shrink-0 place-items-center rounded-md bg-azir/12 font-mono text-2xs font-semibold text-azir">
              {initials(actor.display_name || actor.email)}
            </span>
            <span className="flex min-w-0 flex-1 flex-col">
              <span className="truncate text-xs font-medium">
                {actor.display_name || actor.email}
              </span>
              <span className="text-2xs text-ink-faint">{roleLabel(actor.role)}</span>
            </span>
            <button
              className="shrink-0 rounded-md p-1 text-ink-faint transition-colors hover:bg-panel hover:text-ink"
              onClick={() => void signOut()}
              title="Sign out"
              aria-label="Sign out"
            >
              <Icon.out />
            </button>
          </div>
        </div>
      </nav>

      <div className="ml-[232px] flex min-w-0 flex-1 flex-col">
        <header className="sticky top-0 z-20 flex items-center gap-3 border-b border-edge bg-ground/85 px-6 py-2.5 backdrop-blur">
          {/* A real search field, not a button dressed as one. The old version
              was a <button> styled like an input: it never took a caret, never
              took a paste, and the mismatch between how it looked and how it
              behaved is exactly what read as unfinished. Typing here opens the
              palette and carries the keystrokes with it. */}
          <div className="relative flex max-w-lg flex-1 items-center">
            <span className="pointer-events-none absolute left-3 text-ink-faint">
              <Icon.search />
            </span>
            <input
              className="h-8 w-full rounded-md border border-edge bg-sunken pl-9 pr-16 text-sm placeholder:text-ink-faint focus-visible:border-azir focus-visible:outline-none"
              placeholder="Search tickets, customers, or jump to a page"
              aria-label="Search"
              value=""
              onFocus={() => {
                setSeed("");
                setPaletteOpen(true);
              }}
              onChange={(e) => {
                setSeed(e.target.value);
                setPaletteOpen(true);
              }}
            />
            <kbd className="pointer-events-none absolute right-2.5 rounded border border-edge bg-panel px-1.5 py-px font-mono text-2xs text-ink-faint">
              ⌘K
            </kbd>
          </div>

          <div className="ml-auto flex items-center gap-2">
            <Tooltip content="Keyboard shortcuts">
              <button
                className="grid size-8 place-items-center rounded-md text-ink-dim transition-colors hover:bg-sunken hover:text-ink"
                onClick={() => setHelpOpen(true)}
                aria-label="Keyboard shortcuts"
              >
                <kbd className="font-mono text-xs">?</kbd>
              </button>
            </Tooltip>

            {/* Violet, and the only violet in the chrome: this is the control
                that hands the screen over to the assistant. */}
            <Tooltip
              content={
                assistantOpen
                  ? "Hide the assistant"
                  : "Ask about what you are looking at"
              }
            >
              <button
                className={cn(
                  "flex h-8 items-center gap-1.5 rounded-md border px-3 text-sm font-medium transition-colors",
                  assistantOpen
                    ? "border-azir bg-azir text-azir-ink"
                    : "border-edge bg-panel text-ink hover:border-azir/50 hover:text-azir",
                )}
                onClick={() => setAssistantOpen((v) => !v)}
              >
                <Icon.spark />
                Assistant
              </button>
            </Tooltip>
          </div>
        </header>

        <div className={cn("flex min-h-0 flex-1", assistantOpen && "gap-0")}>
          <div className="min-w-0 flex-1">
        {route.name === "triage" && <Triage actor={actor} go={go} />}
        {route.name === "tickets" && (
          <Tickets
            actor={actor}
            query={route.query}
            status={route.status}
            includeDone={route.includeDone}
            owner={route.owner}
            sort={route.sort}
            go={go}
          />
        )}
        {route.name === "ticket" && (
          <TicketDetail
            id={route.id}
            actor={actor}
            go={go}
            ask={(question) => {
              setAsk(question);
              setAssistantOpen(true);
            }}
          />
        )}
        {route.name === "customers" && <Customers query={route.query} go={go} />}
        {route.name === "customer" && <CustomerDetail id={route.id} go={go} actor={actor} />}
        {route.name === "chats" && <Chats go={go} />}
        {route.name === "bulk" && (
          <Suspense fallback={<div className="mx-auto max-w-[1180px] px-6 py-6"><div className="h-40 animate-pulse rounded-lg bg-sunken" /></div>}>
            <BulkEdits actor={actor} />
          </Suspense>
        )}
        {(route.name === "diagnostics" || route.name === "snapshot") && (
          <Diagnostics openId={route.name === "snapshot" ? route.id : undefined} go={go} actor={actor} />
        )}
        {route.name === "settings" && (
          <Suspense fallback={<div className="mx-auto max-w-[1180px] px-6 py-6"><div className="h-40 animate-pulse rounded-lg bg-sunken" /></div>}>
            <Settings actor={actor} tab={route.tab} go={go} />
          </Suspense>
        )}
          </div>
          {assistantOpen && (
            // The fallback is the panel's own shape rather than a spinner, so
            // opening it does not move the layout twice.
            <Suspense
              fallback={
                <aside className="w-[420px] shrink-0 border-l border-edge bg-panel">
                  <div className="m-4 h-8 animate-pulse rounded-md bg-sunken" />
                </aside>
              }
            >
              <AssistantPanel
                key={subjectKey(route)}
                subject={subjectOf(route)}
                subjectLabel={subjectLabel(route)}
                seed={ask}
                who={initials(actor.display_name || actor.email)}
                onSeedUsed={() => setAsk("")}
                onClose={() => setAssistantOpen(false)}
              />
            </Suspense>
          )}
        </div>
      </div>

      <CommandPalette
        open={paletteOpen}
        seed={seed}
        onClose={() => {
          setPaletteOpen(false);
          setSeed("");
        }}
        go={go}
      />

      <Dialog
        open={helpOpen}
        onOpenChange={setHelpOpen}
        title="Keyboard shortcuts"
        returnFocusTo='[aria-label="Keyboard shortcuts"]'
        description="Everything here is also a click away — these just save the trip."
      >
        <div className="flex flex-col">
          {["Navigate", "Do"].map((group) => (
            <div key={group}>
              <div className="mb-2 mt-4 font-mono text-2xs font-medium uppercase tracking-[0.09em] text-ink-faint">
                {group}
              </div>
              {chords
                .filter((c) => c.group === group && c.label)
                .map((c) => (
                  <div key={String(c.keys)} className="flex items-center justify-between gap-4 border-b border-edge/60 py-1.5 last:border-b-0">
                    <span className="text-sm text-ink-dim">{c.label}</span>
                    <span className="flex items-center gap-1">
                      {(Array.isArray(c.keys) ? c.keys : [c.keys]).map((k) => (
                        <kbd key={k} className="rounded border border-edge bg-sunken px-1.5 py-px font-mono text-2xs">{k === "escape" ? "Esc" : k}</kbd>
                      ))}
                    </span>
                  </div>
                ))}
            </div>
          ))}
          <div>
            <div className="mb-2 mt-4 font-mono text-2xs font-medium uppercase tracking-[0.09em] text-ink-faint">
              Anywhere
            </div>
            <div className="flex items-center justify-between gap-4 border-b border-edge/60 py-1.5 last:border-b-0">
              <span className="text-sm text-ink-dim">Search or jump to</span>
              <span className="flex items-center gap-1">
                <kbd className="rounded border border-edge bg-sunken px-1.5 py-px font-mono text-2xs">⌘</kbd>
                <kbd className="rounded border border-edge bg-sunken px-1.5 py-px font-mono text-2xs">K</kbd>
              </span>
            </div>
            <div className="flex items-center justify-between gap-4 border-b border-edge/60 py-1.5 last:border-b-0">
              <span className="text-sm text-ink-dim">Open the assistant</span>
              <span className="flex items-center gap-1">
                <kbd className="rounded border border-edge bg-sunken px-1.5 py-px font-mono text-2xs">⌘</kbd>
                <kbd className="rounded border border-edge bg-sunken px-1.5 py-px font-mono text-2xs">J</kbd>
              </span>
            </div>
          </div>
        </div>
      </Dialog>
    </div>
  );
}

/** Lets a nav item be a real link — middle-click and copy-link behave. */
function link(go: (to: Route) => void, to: Route) {
  return (e: React.MouseEvent) => {
    if (e.metaKey || e.ctrlKey || e.shiftKey || e.button !== 0) return;
    e.preventDefault();
    go(to);
  };
}

/** A section heading in the rail, in the structural voice. */
function NavGroup({ label }: { label: string }) {
  return (
    <span className="mb-1 mt-4 px-2.5 font-mono text-2xs font-medium uppercase tracking-[0.09em] text-ink-faint first:mt-0">
      {label}
    </span>
  );
}

/**
 * How many open tickets is a problem.
 *
 * Any number above zero used to be drawn in the critical colour, which meant
 * the rail was permanently red on a healthy queue — and a warning that is
 * always on is a warning nobody sees. These say something: a couple of dozen is
 * a busy morning, fifty is a backlog.
 */
function pressure(count: number): Signal {
  if (count > 50) return "critical";
  if (count > 25) return "attention";
  return "idle";
}

function NavItem({
  icon,
  label,
  active,
  count,
  chord,
  onClick,
}: {
  icon: React.ReactNode;
  label: string;
  active: boolean;
  count?: number;
  /** The keystroke that also gets here, shown on hover so it can be learned. */
  chord?: string;
  onClick: () => void;
}) {
  const tone = count !== undefined ? pressure(count) : "idle";
  return (
    <button
      className={cn(
        "group relative flex w-full items-center gap-2.5 rounded-md py-2 pl-3.5 pr-2.5 text-sm font-medium transition-colors",
        active ? "bg-azir/10 text-ink" : "text-ink-dim hover:bg-sunken hover:text-ink",
      )}
      onClick={onClick}
    >
      {/* The brand runs down the edge of wherever you are. A filled grey box
          said "hovered" as much as it said "here". */}
      {active && (
        <span className="absolute inset-y-1.5 left-0 w-[3px] rounded-full bg-azir" aria-hidden="true" />
      )}
      <span className={cn("shrink-0", active ? "text-azir" : "text-ink-faint")}>{icon}</span>
      {label}

      <span className="ml-auto flex items-center gap-2">
        {/* The shortcut exists either way; showing it on hover is how anybody
            finds out. It gives way to the count when there is one. */}
        {chord && count === undefined && (
          <span className="hidden font-mono text-2xs tracking-[0.09em] text-ink-faint opacity-0 transition-opacity group-hover:opacity-100 lg:inline">
            {chord}
          </span>
        )}
        {count !== undefined && count > 0 && (
          <span className="flex items-center gap-1.5">
            <span className={cn("size-1.5 rounded-full", RAIL_TONE[tone])} />
            <span className="font-mono text-2xs tabular-nums text-ink-dim">{count}</span>
          </span>
        )}
      </span>
    </button>
  );
}

const RAIL_TONE: Record<Signal, string> = {
  critical: "bg-critical",
  attention: "bg-attention",
  steady: "bg-steady",
  idle: "bg-edge-strong",
};

/**
 * What the assistant should already know about, taken from the route.
 *
 * This is the product's premise made concrete: a chat opened while reading a
 * ticket does not need to be told which ticket.
 */
function subjectOf(route: Route): { kind: string; id: string } | undefined {
  if (route.name === "ticket") return { kind: "ticket", id: route.id };
  if (route.name === "customer") return { kind: "customer", id: route.id };
  return undefined;
}

function subjectLabel(route: Route): string | undefined {
  if (route.name === "ticket") return `ticket #${route.id}`;
  if (route.name === "customer") return "this customer";
  return undefined;
}

/** Changing what you are looking at starts a fresh conversation about it. */
function subjectKey(route: Route): string {
  const s = subjectOf(route);
  return s ? `${s.kind}:${s.id}` : "general";
}
