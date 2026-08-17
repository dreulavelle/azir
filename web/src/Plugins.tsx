import { useCallback, useEffect, useMemo, useState } from "react";
import {
  api,
  Perm,
  type Actor,
  type Plugin,
  type Registry,
  type Settings,
  type Tool,
} from "./api";
import { ChevronDown } from "lucide-react";
import { cn } from "@/lib/cn";
import { Collapsible, Dialog, Explain, Switch, Tooltip } from "./components";
import { SchemaForm } from "./SchemaForm";
import { useToast } from "./Toast";
import { Chip, CopyButton, Empty, Label, Problem, actionTitle, ago } from "./ui";

/**
 * Connected systems.
 *
 * The words here are deliberately not the words in the code. Inside, these are
 * plugins exposing tools that provide capabilities; to the person running an
 * MSP they are the systems Azir is connected to and the things it is allowed to
 * do with each. Naming the machinery would only ask them to learn it.
 *
 * Permission is a switch rather than a pair of buttons because it is a state,
 * not an event: the question is "is this allowed", and a switch answers that
 * at a glance down a column without anyone having to read a verb.
 */

/** "30m0s" reads as 30 minutes. Nobody should meet Go's duration format. */
function refreshPhrase(soft: string): string {
  const m = soft.match(/^(?:(\d+)h)?(?:(\d+)m)?(?:(\d+)s)?/);
  if (!m) return "";
  const [, h, min, s] = m;
  if (h && h !== "0") return `every ${h} hour${h === "1" ? "" : "s"}`;
  if (min && min !== "0") return `every ${min} minutes`;
  if (s && s !== "0") return `every ${s} seconds`;
  return "";
}

export function Plugins({ actor }: { actor: Actor }) {
  const [registry, setRegistry] = useState<Registry | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [configuring, setConfiguring] = useState<string | null>(null);
  const [showDiagnostics, setShowDiagnostics] = useState(false);
  const toast = useToast();

  const mayConfigure = actor.permissions.includes(Perm.pluginConfigure);
  const mayApprove = actor.permissions.includes(Perm.pluginApprove);

  const load = useCallback(async () => {
    try {
      setRegistry(await api.registry());
      setError(null);
    } catch (e) {
      setError(e instanceof Error ? e.message : "Azir is not responding.");
    }
  }, []);

  useEffect(() => {
    void load();
    const id = setInterval(() => void load(), 15000);
    return () => clearInterval(id);
  }, [load]);

  if (error) return <Problem>{error}</Problem>;
  if (!registry) return <div className="h-50 animate-pulse rounded-lg bg-sunken" />;

  if (registry.plugins.length === 0) {
    return (
      <Empty headline="Nothing is connected yet">
        Once a system is connected it appears here, along with everything Azir
        can do with it.
      </Empty>
    );
  }

  // A connection whose every ability is diagnostic holds no customer data and
  // does no work — it exists to prove the plumbing. Listing it beside the
  // helpdesk invites the reasonable question "what is that and why is it here?",
  // so it goes below, folded away, and says what it is for.
  const isDiagnostic = (p: Plugin) =>
    p.tools.length > 0 && p.tools.every((t) => (t.provides ?? []).every((c) => c === "diagnostic"));

  const working = registry.plugins.filter((p) => !isDiagnostic(p));
  const diagnostics = registry.plugins.filter(isDiagnostic);

  const render = (p: Plugin) => (
    <Connection
      key={p.id}
      plugin={p}
      mayConfigure={mayConfigure}
      mayApprove={mayApprove}
      open={configuring === p.name}
      onToggleOpen={() => setConfiguring(configuring === p.name ? null : p.name)}
      onChanged={load}
      notify={toast}
    />
  );

  return (
    <>
      <p className="mb-5 max-w-[70ch] text-sm text-ink-dim">
        Each plugin connects Azir to one of your systems and carries its own
        settings. Nothing a plugin offers is used until you allow it.
      </p>

      {working.length === 0 && (
        <Empty headline="No plugins are connected">
          Connect a helpdesk to see your queue, your customers and their history.
        </Empty>
      )}

      <div className="flex flex-col gap-2">{working.map(render)}</div>

      {diagnostics.length > 0 && (
        <Collapsible
          open={showDiagnostics}
          onOpenChange={setShowDiagnostics}
          trigger={
            <button className="mt-4 rounded-md px-2 py-1.5 text-xs text-ink-dim transition-colors hover:bg-sunken hover:text-ink">
              {showDiagnostics ? "Hide" : "Show"} built-in test tools ({diagnostics.length})
            </button>
          }
        >
          <div className="pt-3">
            <p className="mb-3 max-w-[70ch] text-xs text-ink-faint">
              These ship with Azir and only confirm that it is working correctly.
              They hold no customer data and do nothing on their own.
            </p>
            <div className="flex flex-col gap-2">{diagnostics.map(render)}</div>
          </div>
        </Collapsible>
      )}
    </>
  );
}

function Connection({
  plugin,
  mayConfigure,
  mayApprove,
  open,
  onToggleOpen,
  onChanged,
  notify,
}: {
  plugin: Plugin;
  mayConfigure: boolean;
  mayApprove: boolean;
  open: boolean;
  onToggleOpen: () => void;
  onChanged: () => Promise<void>;
  notify: ReturnType<typeof useToast>;
}) {
  // Optimistic, so a switch moves the instant it is pressed rather than after a
  // round trip. A failure puts it back and says so.
  const [pending, setPending] = useState<Record<string, boolean>>({});
  const [confirmWrite, setConfirmWrite] = useState<Tool | null>(null);

  // Reading and changing are different kinds of permission and deserve to be
  // read separately: a glance should answer "can Azir change anything here?"
  const [reads, writes] = useMemo(() => {
    const r: Tool[] = [];
    const w: Tool[] = [];
    for (const t of plugin.tools) (t.mutates ? w : r).push(t);
    return [r, w];
  }, [plugin.tools]);

  const allowed = (t: Tool) => pending[t.name] ?? t.status === "approved";
  const onCount = reads.filter(allowed).length;

  async function set(tool: Tool, next: boolean) {
    setPending((p) => ({ ...p, [tool.name]: next }));
    try {
      await api.decide(tool.plugin, tool.name, next ? "approved" : "rejected");
      await onChanged();
      setPending((p) => {
        const { [tool.name]: _drop, ...rest } = p;
        return rest;
      });
    } catch (e) {
      setPending((p) => {
        const { [tool.name]: _drop, ...rest } = p;
        return rest;
      });
      notify("That change did not save", {
        tone: "bad",
        detail: e instanceof Error ? e.message : undefined,
      });
    }
  }

  return (
    // Collapsed by default. A deployment with five connections and forty
    // abilities between them is a page nobody reads; what a reader needs from
    // the closed state is which systems are connected and how much each one is
    // allowed to do, and both of those fit on one line.
    <section
      className={cn(
        "overflow-hidden rounded-lg border bg-panel shadow-e1 transition-colors",
        open ? "border-edge-strong" : "border-edge",
      )}
    >
      <button
        className="flex w-full items-center gap-3 px-4 py-3 text-left transition-colors hover:bg-sunken/60"
        onClick={onToggleOpen}
        aria-expanded={open}
      >
        <span
          className="grid size-8 shrink-0 place-items-center rounded-md bg-sunken font-mono text-2xs font-semibold text-ink-dim"
          aria-hidden="true"
        >
          {plugin.name.slice(0, 2).toUpperCase()}
        </span>

        <span className="flex min-w-0 flex-1 flex-col">
          <span className="flex flex-wrap items-center gap-2">
            <h2 className="text-sm font-semibold">{plugin.name}</h2>
            <Label>{plugin.category === "psa" ? "helpdesk" : plugin.category}</Label>
            {/* A status light, in the same vocabulary the queue uses. */}
            <span
              className={cn(
                "inline-flex items-center gap-1.5 text-2xs",
                plugin.ready ? "text-steady" : "text-critical",
              )}
              title={
                plugin.ready
                  ? "Azir can reach this system right now."
                  : plugin.not_ready_reason || "No reason was given."
              }
            >
              <span
                className={cn(
                  "size-1.5 rounded-full",
                  plugin.ready ? "bg-steady" : "bg-critical",
                )}
              />
              {plugin.ready ? "connected" : "not working"}
            </span>
          </span>
          <span className="mt-0.5 truncate text-xs text-ink-faint">{plugin.description}</span>
        </span>

        <span
          className="shrink-0 font-mono text-2xs tabular-nums text-ink-faint"
          title="How many of the things this system offers Azir is currently allowed to use."
        >
          {onCount}/{reads.length} allowed
        </span>

        <ChevronDown
          className={cn(
            "size-4 shrink-0 text-ink-faint transition-transform",
            open && "rotate-180",
          )}
          aria-hidden="true"
        />
      </button>

      {!plugin.ready && (
        <div className="border-t border-edge px-4 py-3">
          <p className="rounded-lg border border-critical/30 bg-critical/10 px-3 py-2 text-xs text-critical">
            {plugin.not_ready_reason || "This connection is not working."}
          </p>
        </div>
      )}

      <Collapsible open={open} onOpenChange={onToggleOpen} trigger={<span hidden />}>
        {mayConfigure &&
          (plugin.config_scope === "customer" ? (
            // Deliberately not a form. This plugin reaches a different system
            // for every customer, so one set of credentials here could only
            // ever reach one of them — and the one it reached would be
            // whichever was typed last.
            <div className="border-t border-edge px-4 py-4">
              <p className="max-w-[70ch] rounded-lg border border-edge bg-sunken px-3.5 py-3 text-xs text-ink-dim">
                Each customer has their own {plugin.name} to connect to, so the
                address and credentials live on the customer rather than here.
                Open a customer and use their <strong>Connected systems</strong>{" "}
                panel. What {plugin.name} is allowed to do is still decided
                below, once, for everybody.
              </p>
              <div className="mt-3">
                <WritePolicy plugin={plugin.name} />
              </div>
            </div>
          ) : (
            <div className="border-t border-edge px-4 py-4">
              <PluginSettings plugin={plugin} />
            </div>
          ))}

        <div className="border-t border-edge px-4 py-4">
          <div className="mb-2 flex items-center gap-1.5">
            <Label>Information it can read</Label>
            <Explain side="right">
              Azir asks for the kind of information it needs, never for a named
              system. Anything that provides the same kind can stand in for this
              one, so replacing a system later does not mean rebuilding anything.
            </Explain>
          </div>

          <ul className="flex flex-col">
            {reads.map((t) => (
              <li
                key={t.name}
                className="flex items-center gap-4 border-b border-edge/60 py-2.5 last:border-b-0"
              >
                <div className="min-w-0 flex-1">
                  <div className="flex flex-wrap items-center gap-2">
                    <span className="text-sm font-medium">{actionTitle(t.name)}</span>
                    {!t.available && (
                      <Tooltip content={t.unavailable_reason || "No reason was given."}>
                        <Chip tone="urgent">unavailable</Chip>
                      </Tooltip>
                    )}
                  </div>
                  <div className="text-xs text-ink-dim">{t.summary || t.description}</div>
                  {t.freshness && (
                    <Tooltip
                      content="Answers are reused briefly so the connected system is not asked the same question over and over. You can always ask for fresh data."
                      side="bottom"
                    >
                      <Label>Updates {refreshPhrase(t.freshness.soft)}</Label>
                    </Tooltip>
                  )}
                </div>
                <Tooltip content={allowed(t) ? "Allowed. Turn off to stop Azir using this." : "Not allowed. Turn on to let Azir use this."}>
                  <span className="shrink-0">
                    <Switch
                      checked={allowed(t)}
                      disabled={!mayApprove}
                      onChange={(next) => void set(t, next)}
                      label={`Allow ${actionTitle(t.name)}`}
                    />
                  </span>
                </Tooltip>
              </li>
            ))}
          </ul>

          {writes.length > 0 && (
            <>
              <div className="mb-2 mt-6 flex items-center gap-1.5">
                <Label className="text-critical">Changes it can make</Label>
                <Explain side="right">
                  These write back to the connected system. The assistant is never
                  offered them — only a person can do these, and only when your
                  administrator has allowed changes for this connection.
                </Explain>
              </div>

              <ul className="flex flex-col rounded-lg border border-critical/25 bg-critical/[0.04] px-3">
                {writes.map((t) => (
                  <li
                    key={t.name}
                    className="flex items-center gap-4 border-b border-critical/15 py-2.5 last:border-b-0"
                  >
                    <div className="min-w-0 flex-1">
                      <div className="flex flex-wrap items-center gap-2">
                        <span className="text-sm font-medium">{actionTitle(t.name)}</span>
                        <Chip tone="urgent">changes data</Chip>
                      </div>
                      <div className="text-xs text-ink-dim">{t.summary || t.description}</div>
                    </div>
                    <Tooltip content={allowed(t) ? "Allowed for people whose role permits it." : "Not allowed."}>
                      <span className="shrink-0">
                        <Switch
                          tone="urgent"
                          checked={allowed(t)}
                          disabled={!mayApprove}
                          onChange={(next) => (next ? setConfirmWrite(t) : void set(t, false))}
                          label={`Allow ${actionTitle(t.name)}`}
                        />
                      </span>
                    </Tooltip>
                  </li>
                ))}
              </ul>
            </>
          )}
        </div>
      </Collapsible>

      {/* Allowing something that writes gets a deliberate pause. Everything
          else on this screen is reversible by flicking a switch back; this one
          can reach a customer. */}
      <Dialog
        open={confirmWrite !== null}
        onOpenChange={(o) => !o && setConfirmWrite(null)}
        title={confirmWrite ? `Allow "${actionTitle(confirmWrite.name)}"?` : ""}
        description={
          <>
            This lets your team change things in {plugin.name} from inside Azir.
            Every change is recorded against the person who made it, and the
            assistant is never able to do it on its own.
          </>
        }
        footer={
          <>
            <button
              className="h-8 rounded-md border border-edge px-3 text-sm font-medium transition-colors hover:bg-sunken"
              onClick={() => setConfirmWrite(null)}
            >
              Cancel
            </button>
            <button
              className="h-8 rounded-md bg-critical px-3 text-sm font-medium text-white transition-opacity hover:opacity-90"
              onClick={() => {
                if (confirmWrite) void set(confirmWrite, true);
                setConfirmWrite(null);
              }}
            >
              Allow it
            </button>
          </>
        }
      />
    </section>
  );
}

export function PluginSettings({
  plugin,
  customerId,
}: {
  plugin: Plugin;
  /** Which customer these settings belong to. Omitted for deployment-wide ones. */
  customerId?: string;
}) {
  const [settings, setSettings] = useState<Settings | null>(null);
  const [busy, setBusy] = useState(false);
  const toast = useToast();

  const load = useCallback(async () => {
    try {
      setSettings(await api.settings(plugin.name, customerId));
    } catch (e) {
      toast("Could not open these settings", {
        tone: "bad",
        detail: e instanceof Error ? e.message : undefined,
      });
    }
  }, [plugin.name, customerId, toast]);

  useEffect(() => {
    void load();
  }, [load]);

  async function save(values: Record<string, unknown>) {
    setBusy(true);
    try {
      const res = await api.saveSettings(plugin.name, values, customerId);
      toast("Settings saved", {
        detail:
          res.credentials_saved > 0
            ? "Passwords and keys were encrypted before being stored."
            : undefined,
      });
      await load();
    } catch (e) {
      toast("Could not save", { tone: "bad", detail: e instanceof Error ? e.message : undefined });
    } finally {
      setBusy(false);
    }
  }

  async function clearSecret(field: string) {
    await api.clearSecret(plugin.name, field, customerId);
    toast("Removed the stored value");
    await load();
  }

  if (!settings) return <div className="h-32 animate-pulse rounded-lg bg-sunken" />;

  return (
    <div className="flex flex-col gap-4">
      <SchemaForm
        schema={settings.config_schema}
        values={settings.values}
        secretsSet={settings.secrets_set}
        busy={busy}
        onSave={(v) => void save(v)}
        onClearSecret={(f) => void clearSecret(f)}
      />

      <WebhookPanel plugin={plugin.name} />

      {/* Whether Azir may change anything in this system is a deployment
          decision, not a per-customer one: an MSP does not allow writes for
          one customer and refuse them for another. Shown only on the
          deployment form so there is one switch rather than one per customer. */}
      {settings.has_mutating_tools && !customerId && (
        <WriteSwitch
          plugin={plugin.name}
          enabled={settings.writes_enabled}
          onChanged={() => void load()}
        />
      )}
    </div>
  );
}

/**
 * Where a connected system should tell Azir that something changed.
 *
 * Worth its own panel because the alternative is polling, and polling is both
 * slower and ruder: a queue that refreshes every minute is a minute out of date
 * and is also a request a minute against somebody's PSA forever. A webhook is
 * current and costs nothing until something actually happens.
 *
 * The address contains a secret, so it is treated as one: shown to somebody who
 * can already configure connections, copied rather than read aloud, and
 * replaceable in one click if it ends up somewhere it should not be.
 */
function WebhookPanel({ plugin }: { plugin: string }) {
  const [hook, setHook] = useState<{
    path: string;
    deliveries: number;
    rejected: number;
    last_seen?: string;
  } | null>(null);
  const [busy, setBusy] = useState(false);
  const toast = useToast();

  const load = useCallback(async () => {
    try {
      setHook(await api.webhook(plugin));
    } catch {
      setHook(null);
    }
  }, [plugin]);

  useEffect(() => {
    void load();
  }, [load]);

  if (!hook) return null;
  const url = window.location.origin + hook.path;

  async function rotate() {
    setBusy(true);
    try {
      await api.rotateWebhook(plugin);
      await load();
      toast("A new address was issued", {
        detail: "Paste it into the connected system; the old one no longer works.",
      });
    } catch (e) {
      toast("Could not issue a new address", {
        tone: "bad",
        detail: e instanceof Error ? e.message : undefined,
      });
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="mt-4 rounded-lg border border-edge bg-sunken/40 p-3.5">
      <div className="mb-2 flex items-center gap-2">
        <Label>Tell Azir when something changes</Label>
        <Explain side="right">
          Paste this into the connected system so it posts here when a ticket or
          customer changes. Azir does not trust what it is sent — a message only
          means "go and look", and Azir then reads the change with its own
          credentials. That is why this address being guessed would be a
          nuisance rather than a breach.
        </Explain>
      </div>

      <div className="flex flex-wrap items-center gap-2">
        <code className="min-w-0 flex-1 truncate rounded-md border border-edge bg-panel px-2.5 py-1.5 font-mono text-2xs text-ink-dim">
          {url}
        </code>
        <CopyButton text={url} label="Copy" className="shrink-0" />
        <button
          className="shrink-0 rounded-md px-2 py-1 text-xs text-ink-dim transition-colors hover:bg-critical/10 hover:text-critical disabled:opacity-50"
          disabled={busy}
          onClick={() => void rotate()}
        >
          New address
        </button>
      </div>

      <p className="mt-2 text-2xs text-ink-faint">
        {hook.last_seen
          ? `${hook.deliveries} received, last ${ago(hook.last_seen)}`
          : "Nothing received yet — paste the address into the connected system to switch it on."}
        {hook.rejected > 0 ? ` · ${hook.rejected} refused` : ""}
      </p>
    </div>
  );
}

/**
 * The write switch on its own, for a plugin whose credentials live elsewhere.
 *
 * Whether Azir may change anything is a deployment decision — an MSP does not
 * allow writes for one customer and refuse them for another — so it stays on
 * this page even when the address and password do not.
 */
function WritePolicy({ plugin }: { plugin: string }) {
  const [settings, setSettings] = useState<Settings | null>(null);
  const load = useCallback(async () => {
    try {
      setSettings(await api.settings(plugin));
    } catch {
      setSettings(null);
    }
  }, [plugin]);

  useEffect(() => {
    void load();
  }, [load]);

  if (!settings?.has_mutating_tools) return null;
  return (
    <WriteSwitch
      plugin={plugin}
      enabled={settings.writes_enabled}
      onChanged={() => void load()}
    />
  );
}

/**
 * The one switch that lets Azir change anything in a connected system.
 *
 * Separate from the settings form on purpose. Everything else there describes
 * how to reach a system; this decides whether Azir may change it, and it should
 * not be possible to flip while thinking about a web address.
 */
function WriteSwitch({
  plugin,
  enabled,
  onChanged,
}: {
  plugin: string;
  enabled: boolean;
  onChanged: () => void;
}) {
  const [busy, setBusy] = useState(false);
  const [confirming, setConfirming] = useState(false);
  const toast = useToast();

  async function set(next: boolean) {
    setBusy(true);
    try {
      await api.setWrites(plugin, next);
      toast(
        next
          ? `Azir can now make changes in ${plugin}`
          : `Azir is read-only for ${plugin} again`,
      );
      setConfirming(false);
      onChanged();
    } catch (e) {
      toast("Could not change this", {
        tone: "bad",
        detail: e instanceof Error ? e.message : undefined,
      });
    } finally {
      setBusy(false);
    }
  }

  return (
    <>
      <div className={enabled ? "write-gate is-on" : "write-gate"}>
        <div className="min-w-0 flex-1">
          <div className="mb-1 flex items-center gap-2">
            <strong style={{ fontSize: 13.5 }}>Let Azir make changes in {plugin}</strong>
            {/* Coloured for attention, not approval: this is the one setting
                where the permissive state is the notable one. */}
            {enabled && <Chip tone="urgent">on</Chip>}
          </div>
          <p className="max-w-[60ch] text-xs text-ink-dim">
            {enabled
              ? "Your team can reply to tickets and change them from inside Azir. The assistant still cannot."
              : "Azir only reads. Replies and changes are refused, even for someone whose role would allow them."}
          </p>
        </div>
        <Switch
          tone="urgent"
          checked={enabled}
          disabled={busy}
          onChange={(next) => (next ? setConfirming(true) : void set(false))}
          label={`Let Azir make changes in ${plugin}`}
        />
      </div>

      <Dialog
        open={confirming}
        onOpenChange={setConfirming}
        title={`Let Azir make changes in ${plugin}?`}
        description={
          <>
            Your team will be able to reply to tickets and change them without
            leaving Azir. Each change is recorded against the person who made it,
            and only roles that already permit it can do so.
            <br />
            <br />
            The assistant is never given these. Turning this on lets a person act
            through Azir; it does not let the assistant act on its own.
          </>
        }
        footer={
          <>
            <button className="h-8 rounded-md border border-edge px-3 text-sm font-medium transition-colors hover:bg-sunken" onClick={() => setConfirming(false)}>
              Cancel
            </button>
            <button className="h-8 rounded-md bg-critical px-3 text-sm font-medium text-white transition-opacity hover:opacity-90 disabled:opacity-50" disabled={busy} onClick={() => void set(true)}>
              {busy ? "Turning on…" : "Allow changes"}
            </button>
          </>
        }
      />
    </>
  );
}
