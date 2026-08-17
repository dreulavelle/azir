import { useCallback, useEffect, useState } from "react";
import {
  api,
  onSessionExpired,
  Unauthorized,
  work,
  type Actor,
  type AuthState,
} from "./api";
import { AssistantPanel } from "./Assistant";
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
import { Icon, initials, statusTone } from "./ui";
import { Triage } from "./pages/Triage";
import { Tickets } from "./pages/Tickets";
import { TicketDetail } from "./pages/TicketDetail";
import { Customers, CustomerDetail } from "./pages/CustomerPages";
import { Chats } from "./pages/Chats";
import { Settings, settingsTabsFor } from "./pages/Settings";

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
      <nav className="fixed inset-y-0 left-0 flex w-[232px] flex-col gap-0.5 border-r border-edge bg-panel px-3 py-4">
        <a
          className="mb-5 flex items-center gap-2.5 px-2"
          href={href({ name: "triage" })}
          onClick={link(go, { name: "triage" })}
        >
          {/* The mark is the one place the brand colour appears without the
              assistant being involved — it is the product signing its name. */}
          <Mark size={28} />
          <span className="text-base font-semibold tracking-tight">{brand.effective_name}</span>
        </a>

        <NavItem
          icon={<Icon.triage />}
          label="Triage"
          active={route.name === "triage"}
          count={openCount ?? undefined}
          alarm={(openCount ?? 0) > 0}
          onClick={() => go({ name: "triage" })}
        />
        <NavItem
          icon={<Icon.ticket />}
          label="Tickets"
          active={route.name === "tickets" || route.name === "ticket"}
          onClick={() => go({ name: "tickets" })}
        />
        <NavItem
          icon={<Icon.people />}
          label="Customers"
          active={route.name === "customers" || route.name === "customer"}
          onClick={() => go({ name: "customers" })}
        />
        <NavItem
          icon={<Icon.spark />}
          label="Chats"
          active={route.name === "chats"}
          onClick={() => go({ name: "chats" })}
        />

        <div className="mt-auto flex flex-col gap-0.5 border-t border-edge pt-3">
          {canAdmin && (
            <NavItem
              icon={<Icon.settings />}
              label="Settings"
              active={route.name === "settings"}
              onClick={() => go({ name: "settings" })}
            />
          )}
          <button
            className="flex w-full items-center gap-2.5 rounded-md px-2.5 py-2 text-left transition-colors hover:bg-sunken"
            onClick={() => void signOut()}
            title="Sign out"
          >
            <span className="grid size-7 shrink-0 place-items-center rounded-md bg-sunken font-mono text-2xs font-semibold text-ink-dim">
              {initials(actor.display_name || actor.email)}
            </span>
            <span className="flex min-w-0 flex-col">
              <span className="truncate text-xs font-medium">
                {actor.display_name || actor.email}
              </span>
              <span className="text-2xs text-ink-faint">{actor.role} · sign out</span>
            </span>
          </button>
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
          <Tickets query={route.query} status={route.status} go={go} />
        )}
        {route.name === "ticket" && (
          <TicketDetail
            id={route.id}
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
        {route.name === "settings" && <Settings actor={actor} tab={route.tab} go={go} />}
          </div>
          {assistantOpen && (
            <AssistantPanel
              key={subjectKey(route)}
              subject={subjectOf(route)}
              subjectLabel={subjectLabel(route)}
              seed={ask}
              who={initials(actor.display_name || actor.email)}
              onSeedUsed={() => setAsk("")}
              onClose={() => setAssistantOpen(false)}
            />
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

function NavItem({
  icon,
  label,
  active,
  count,
  alarm,
  onClick,
}: {
  icon: React.ReactNode;
  label: string;
  active: boolean;
  count?: number;
  alarm?: boolean;
  onClick: () => void;
}) {
  return (
    <button
      className={cn(
        "flex w-full items-center gap-2.5 rounded-md px-2.5 py-2 text-sm font-medium transition-colors",
        active ? "bg-sunken text-ink" : "text-ink-dim hover:bg-sunken/60 hover:text-ink",
      )}
      onClick={onClick}
    >
      <span className={cn("shrink-0", active ? "text-ink" : "text-ink-faint")}>{icon}</span>
      {label}
      {count !== undefined && count > 0 && (
        <span
          className={cn(
            "ml-auto font-mono text-2xs tabular-nums",
            alarm ? "text-critical" : "text-ink-faint",
          )}
        >
          {count}
        </span>
      )}
    </button>
  );
}

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
