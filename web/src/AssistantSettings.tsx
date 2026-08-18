import { cn } from "@/lib/cn";
import { useCallback, useEffect, useState } from "react";
import { chat, type AssistantSettings as Settings } from "./api";
import { Explain, Select, Switch, Tooltip } from "./components";
import { useToast } from "./Toast";
import { Button, Chip, Label, PanelHead } from "./ui";

/**
 * Which model answers, and what it is allowed to see.
 *
 * Bring your own key: Azir never proxies through a service of ours, so a
 * customer's ticket text goes to the provider the administrator chose and
 * nowhere else. Saying so here matters more than any setting on the page.
 */

const PROVIDERS = [
  { value: "anthropic", label: "Anthropic" },
  { value: "gateway", label: "A gateway or self-hosted service" },
];

/**
 * Suggestions, not a closed list — a gateway can offer anything, and this box
 * accepts a typed model name whatever is in here.
 *
 * Ordered by what the job actually asks for rather than by capability. Azir's
 * assistant runs a tool loop: it picks a tool, reads what comes back, and goes
 * again, several times, before it writes a short reply. Choosing the right
 * tool and filling its arguments correctly is where a model succeeds or fails
 * here — not reasoning depth — so the balanced model leads.
 */
const ANTHROPIC_MODELS = [
  { value: "claude-sonnet-5", label: "Claude Sonnet 5 — balanced, and the one to start with" },
  { value: "claude-opus-5", label: "Claude Opus 5 — most capable" },
  { value: "claude-haiku-4-5", label: "Claude Haiku 4.5 — fastest and cheapest" },
];

/** Capability names are not for reading aloud. */
const LOOKUP_WORDS: Record<string, string> = {
  "work_items.search": "Search tickets",
  "work_items.get": "Read a ticket",
  "work_items.timeline": "Read a ticket's history",
  "work_items.schema": "Available statuses",
  "customers.list": "Search customers",
  "customers.get": "Read a customer",
  "customers.standing": "Customer balances",
  "time_entries.list": "Logged time",
  "assets.list": "Equipment",
  "documentation.search": "Your documentation",
  "access.check": "What your key can do",
  "invoices.list": "Invoices",
  "web.search": "The public internet",
};

/**
 * The models a technician may pick from.
 *
 * Ticked rather than typed. The names a gateway uses are not the names in a
 * vendor's documentation — "claude/claude-opus-5" rather than "claude-opus-5" —
 * so asking somebody to type them from memory is asking them to get it wrong,
 * and a typo here does not fail loudly: it produces a model nobody can select.
 *
 * The service is asked what it runs. When it will not say, the field falls back
 * to typing, because a gateway that publishes no list is still a usable one.
 */
function ModelList({
  chosen,
  fallback,
  onChange,
}: {
  chosen: string[];
  fallback: string;
  onChange: (next: string[]) => void;
}) {
  const [offered, setOffered] = useState<string[] | null>(null);
  const [reason, setReason] = useState("");
  const [filter, setFilter] = useState("");

  useEffect(() => {
    let cancelled = false;
    chat
      .models()
      .then((r) => {
        if (cancelled) return;
        setOffered(r.models);
        setReason(r.reason ?? "");
      })
      .catch(() => !cancelled && setOffered([]));
    return () => {
      cancelled = true;
    };
  }, []);

  const toggle = (model: string) =>
    onChange(chosen.includes(model) ? chosen.filter((m) => m !== model) : [...chosen, model]);

  if (offered === null) {
    return <div className="h-32 animate-pulse rounded-lg bg-sunken" />;
  }

  // Anything already ticked stays visible even if the service has since
  // stopped offering it — otherwise a model would silently vanish from the
  // list a technician is still allowed to use.
  const all = [...new Set([...offered, ...chosen])].filter(Boolean).sort();
  const matching = filter
    ? all.filter((m) => m.toLowerCase().includes(filter.toLowerCase()))
    : all;

  if (all.length === 0) {
    return (
      <div className="flex flex-col gap-1.5">
        <label htmlFor="assistant-allowed" className="flex items-center gap-2 text-sm font-medium">
          Models they may choose
        </label>
        <p className="max-w-[70ch] text-xs text-ink-faint">
          {reason || "The service did not return a list."} One name per line.
        </p>
        <textarea
          id="assistant-allowed"
          className="w-full resize-y rounded-md border border-edge bg-sunken px-2.5 py-2 font-mono text-xs placeholder:text-ink-faint focus-visible:border-azir focus-visible:outline-none"
          rows={4}
          spellCheck={false}
          value={chosen.join("\n")}
          // Trimmed on the way out rather than per keystroke: normalising what
          // is already on screen fights whoever is typing into it.
          onChange={(e) => onChange(e.target.value.split("\n"))}
          onBlur={(e) => onChange(e.target.value.split("\n").map((m) => m.trim()).filter(Boolean))}
        />
      </div>
    );
  }

  return (
    <div className="flex flex-col gap-1.5">
      <div className="flex items-baseline justify-between gap-3">
        <span className="flex items-center gap-2 text-sm font-medium">
          Models they may choose
          <Explain>
            Straight from your service, named the way it names them. The default
            above is always available whether or not you tick it.
          </Explain>
        </span>
        <Label>
          {chosen.length} of {all.length}
        </Label>
      </div>

      {all.length > 8 && (
        <input
          className="h-8 w-full rounded-md border border-edge bg-sunken px-2.5 text-sm placeholder:text-ink-faint focus-visible:border-azir focus-visible:outline-none"
          placeholder={`Filter ${all.length} models…`}
          value={filter}
          aria-label="Filter models"
          onChange={(e) => setFilter(e.target.value)}
        />
      )}

      <div className="max-h-72 overflow-y-auto rounded-lg border border-edge">
        {matching.map((m) => (
          <label
            key={m}
            className="flex cursor-pointer items-center gap-3 border-b border-edge/60 px-3 py-2 last:border-b-0 hover:bg-sunken"
          >
            <Switch
              checked={chosen.includes(m) || m === fallback}
              disabled={m === fallback}
              onChange={() => toggle(m)}
              label={`Allow ${m}`}
            />
            <span className="min-w-0 flex-1 truncate font-mono text-xs">{m}</span>
            {m === fallback && <Label>default</Label>}
          </label>
        ))}
        {matching.length === 0 && (
          <p className="px-3 py-4 text-xs text-ink-faint">Nothing matches “{filter}”.</p>
        )}
      </div>
    </div>
  );
}

export function AssistantSettings() {
  const [settings, setSettings] = useState<Settings | null>(null);
  const [draft, setDraft] = useState<Partial<Settings> & { api_key?: string }>({});
  const [busy, setBusy] = useState(false);
  const toast = useToast();

  const load = useCallback(async () => {
    try {
      const loaded = await chat.settings();
      setSettings(loaded);
      setDraft({
        provider: loaded.provider,
        model: loaded.model || "claude-sonnet-4-5",
        base_url: loaded.base_url,
        max_turns: loaded.max_turns,
        model_choice: loaded.model_choice ?? "fixed",
        allowed_models: loaded.allowed_models ?? [],
        system_prompt: loaded.system_prompt || loaded.shipped_prompt,
        house_prompt: loaded.house_prompt ?? "",
        max_answer_tokens: loaded.max_answer_tokens,
        api_key: "",
      });
    } catch (e) {
      toast("Could not load the assistant settings", {
        tone: "bad",
        detail: e instanceof Error ? e.message : undefined,
      });
    }
  }, [toast]);

  useEffect(() => {
    void load();
  }, [load]);

  async function save(enabled: boolean) {
    setBusy(true);
    try {
      await chat.saveSettings({ ...draft, enabled });
      toast(enabled ? "The assistant is on" : "The assistant is off");
      await load();
    } catch (e) {
      toast("Could not save", { tone: "bad", detail: e instanceof Error ? e.message : undefined });
    } finally {
      setBusy(false);
    }
  }

  if (!settings) return <div className="h-50 animate-pulse rounded-lg bg-sunken"  />;

  const set = (patch: Partial<Settings> & { api_key?: string }) =>
    setDraft((d) => ({ ...d, ...patch }));

  return (
    <section className="rounded-lg border border-edge bg-panel shadow-e1">
      <PanelHead>
        <div className="flex items-center gap-2">
          <h2>Assistant</h2>
          {settings.enabled && <Chip tone="good">on</Chip>}
        </div>
        <Tooltip
          content={
            settings.api_key_set
              ? "Turn the assistant on or off for everyone."
              : "Add an API key first."
          }
        >
          <span>
            <Switch
              checked={settings.enabled}
              disabled={busy || !settings.api_key_set}
              onChange={(next) => void save(next)}
              label="Turn the assistant on"
            />
          </span>
        </Tooltip>
      </PanelHead>

      <div className="p-4">
        <p className="mb-5 max-w-[68ch] text-xs text-ink-dim" >
          The assistant reads your tickets, customers and documentation to answer
          questions and draft replies. It uses your own account with the model
          provider — Azir never sends your customers' data anywhere else, and
          never through us.
        </p>

        <div className="flex max-w-[880px] flex-col gap-5">
          <div className="flex flex-col gap-1.5">
            <label htmlFor="assistant-key" className="flex items-center gap-2">
              API key
              {settings.api_key_set && <Chip tone="accent">stored encrypted</Chip>}
            </label>
            <p className="max-w-[70ch] text-xs text-ink-faint">
              {draft.provider === "anthropic"
                ? "From your Anthropic account. Stored encrypted and never shown again."
                : "The key your gateway expects. Stored encrypted and never shown again."}
            </p>
            <input
              id="assistant-key"
              className="h-8 w-full rounded-md border border-edge bg-sunken px-2.5 text-sm placeholder:text-ink-faint focus-visible:border-azir focus-visible:outline-none disabled:opacity-50"
              type="password"
              autoComplete="new-password"
              placeholder={
                settings.api_key_set
                  ? "•••••••• stored — type to replace"
                  : draft.provider === "anthropic"
                    ? "sk-ant-…"
                    : "the key your gateway expects"
              }
              value={draft.api_key ?? ""}
              onChange={(e) => set({ api_key: e.target.value })}
            />
          </div>

          <div className="grid gap-5 sm:grid-cols-2">
          <div className="flex flex-col gap-1.5">
            <label htmlFor="assistant-provider" className="flex items-center gap-2">
              Service
              <Explain>
                Anthropic directly, or a gateway that sits in front of several
                providers — which keeps one place to see cost and choose a model.
              </Explain>
            </label>
            <Select
              label="Service"
              value={draft.provider ?? "anthropic"}
              onChange={(v) => set({ provider: v })}
              options={PROVIDERS}
              width="100%"
            />
          </div>

          {draft.provider !== "anthropic" && (
            <div className="flex flex-col gap-1.5">
              <label htmlFor="assistant-url" className="flex items-center gap-2">
                Service address
                <Explain>
                  Must be reachable from wherever Azir runs. Inside a container,
                  that usually means a hostname on the same network rather than
                  localhost.
                </Explain>
              </label>
              <input
                id="assistant-url"
                className="h-8 w-full rounded-md border border-edge bg-sunken px-2.5 text-sm placeholder:text-ink-faint focus-visible:border-azir focus-visible:outline-none disabled:opacity-50"
                placeholder="http://gateway.internal:8080/v1"
                value={draft.base_url ?? ""}
                onChange={(e) => set({ base_url: e.target.value })}
              />
            </div>
          )}

          <div className="flex flex-col gap-1.5">
            <label htmlFor="assistant-model">Model</label>
            {draft.provider === "anthropic" ? (
              <Select
                label="Model"
                value={draft.model ?? "claude-sonnet-4-5"}
                onChange={(v) => set({ model: v })}
                options={ANTHROPIC_MODELS}
                width="100%"
              />
            ) : (
              <>
                <input
                  id="assistant-model"
                  className="h-8 w-full rounded-md border border-edge bg-sunken px-2.5 text-sm placeholder:text-ink-faint focus-visible:border-azir focus-visible:outline-none disabled:opacity-50"
                  placeholder="the model name your service expects"
                  value={draft.model ?? ""}
                  onChange={(e) => set({ model: e.target.value })}
                />
                <p className="max-w-[70ch] text-xs text-ink-faint">Exactly as your gateway names it.</p>
              </>
            )}
          </div>

          <div className="flex flex-col gap-1.5">
            <label htmlFor="assistant-answer" className="flex items-center gap-2">
              Longest answer
              <Explain>
                Roughly how much the assistant may write in one reply. The
                default suits a full ticket analysis with a drafted response.
                Some services reserve this whole amount up front and refuse a
                request that asks for more than the account can currently
                afford, so lower it if you hit billing limits.
              </Explain>
            </label>
            <input
              id="assistant-answer"
              className="h-8 w-full rounded-md border border-edge bg-sunken px-2.5 text-sm placeholder:text-ink-faint focus-visible:border-azir focus-visible:outline-none disabled:opacity-50"
              type="number"
              min={256}
              max={200000}
              step={1024}
              value={draft.max_answer_tokens ?? 16384}
              onChange={(e) => set({ max_answer_tokens: Number(e.target.value) })}
            />
          </div>

          {/* Who picks the model.
              Three positions rather than a switch, because the middle one is
              the one most MSPs actually want: a technician can reach for a
              bigger model on a hard ticket, but only from models the business
              has agreed to send customer data to. */}
          </div>

          <div className="flex flex-col gap-1.5">
            <span className="flex items-center gap-2 text-sm font-medium">
              Who chooses the model
              <Explain>
                Everyone answers with the model above unless you allow
                otherwise. Changing this never changes what the assistant is
                allowed to read.
              </Explain>
            </span>

            <div className="flex flex-col gap-1.5">
              {(
                [
                  ["fixed", "Everyone uses the model above", "One model, one bill, one behaviour to support."],
                  ["listed", "Technicians may pick from a list you set", "Useful when a cheaper model handles most tickets and a bigger one is worth it occasionally."],
                  ["free", "Technicians may use any model", "They can type any name the service accepts. Only sensible if you trust the whole team with the bill."],
                ] as const
              ).map(([value, title, why]) => (
                <label
                  key={value}
                  className={cn(
                    "flex cursor-pointer items-start gap-2.5 rounded-lg border px-3 py-2.5 transition-colors",
                    (draft.model_choice ?? "fixed") === value
                      ? "border-azir bg-azir/[0.06]"
                      : "border-edge hover:bg-sunken",
                  )}
                >
                  <input
                    type="radio"
                    name="model-choice"
                    className="mt-1 accent-[var(--azir)]"
                    checked={(draft.model_choice ?? "fixed") === value}
                    onChange={() => set({ model_choice: value })}
                  />
                  <span className="flex flex-col gap-0.5">
                    <span className="text-sm font-medium">{title}</span>
                    <span className="text-xs text-ink-faint">{why}</span>
                  </span>
                </label>
              ))}
            </div>
          </div>

          {draft.model_choice === "listed" && (
            <ModelList
              chosen={draft.allowed_models ?? []}
              fallback={draft.model ?? ""}
              onChange={(allowed_models) => set({ allowed_models })}
            />
          )}

          {/* Everything the assistant is told, in the open.
              Editable because hiding it would buy secrecy rather than safety:
              what stops the assistant writing anywhere is that it is never
              handed a tool that writes, not that a sentence here says so. */}
          <div className="flex flex-col gap-1.5">
            <div className="flex items-baseline justify-between gap-3">
              <label htmlFor="assistant-system" className="flex items-center gap-2 text-sm font-medium">
                Instructions
                <Explain>
                  Exactly what the assistant is told before every question. Yours
                  to change. What the assistant is able to do is decided by the
                  Plugins tab and by Azir itself, not by this text — it can never
                  write anywhere whatever this says.
                </Explain>
              </label>
              {settings.shipped_prompt &&
                draft.system_prompt?.trim() !== settings.shipped_prompt.trim() && (
                  <button
                    className="rounded-md px-1.5 py-0.5 text-xs text-ink-dim transition-colors hover:bg-sunken hover:text-ink"
                    onClick={() => set({ system_prompt: settings.shipped_prompt })}
                  >
                    Reset to Azir's wording
                  </button>
                )}
            </div>
            <textarea
              id="assistant-system"
              className="w-full resize-y rounded-md border border-edge bg-sunken px-2.5 py-2 font-mono text-xs leading-relaxed placeholder:text-ink-faint focus-visible:border-azir focus-visible:outline-none"
              rows={14}
              spellCheck={false}
              value={draft.system_prompt ?? ""}
              onChange={(e) => set({ system_prompt: e.target.value })}
            />

            {/* One specific, actionable warning rather than a refusal. Removing
                this paragraph does not let the assistant do anything new; it
                makes it easier for a customer to talk it into saying something
                misleading, which a technician then reads. */}
            {settings.injection_defence &&
              !(draft.system_prompt ?? "").includes(settings.injection_defence) && (
                <p className="rounded-lg border border-attention/30 bg-attention/10 px-3 py-2 text-xs text-attention">
                  You have removed the paragraph telling the assistant to treat
                  ticket text as evidence rather than as instructions. It still
                  cannot change anything — but a customer who writes orders into
                  a ticket is now likelier to be obeyed in what it tells you.
                </p>
              )}

            <p className="text-2xs text-ink-faint">
              Azir adds who is asking, the time, and which ticket is on screen
              after this, so an answer is about the right thing.
            </p>
          </div>

          <div className="flex flex-col gap-1.5">
            <label htmlFor="assistant-prompt" className="flex items-center gap-2 text-sm font-medium">
              Added instructions
              <Explain>
                Added after the instructions above. Useful for keeping your house
                rules separate from the wording you would rather leave alone —
                tone, escalation, the phrases your customers expect.
              </Explain>
            </label>
            <textarea
              id="assistant-prompt"
              className="w-full resize-y rounded-md border border-edge bg-sunken px-2.5 py-2 text-sm placeholder:text-ink-faint focus-visible:border-azir focus-visible:outline-none"
              rows={5}
              maxLength={4000}
              placeholder="Sign drafted replies off as “The Service Desk”. Escalate anything touching a domain controller to Priya before replying."
              value={draft.house_prompt ?? ""}
              onChange={(e) => set({ house_prompt: e.target.value })}
            />
            <span className="text-2xs text-ink-faint">
              {(draft.house_prompt ?? "").length} of 4000 characters
            </span>
          </div>

          <div>
            <Button weight="primary" disabled={busy} onClick={() => void save(settings.enabled)}>
              {busy ? "Saving…" : "Save"}
            </Button>
          </div>
        </div>

        {/* The consequence of the approval decisions on the Plugins tab,
            in one place, so nobody has to infer it by reading switches. */}
        <div className="mt-6">
          <div className="mb-2 flex items-center gap-2" >
            <span className="font-mono text-2xs font-medium uppercase tracking-[0.09em] text-ink-faint">What it can look up</span>
            <Explain side="right">
              Exactly what you have allowed on the Plugins tab. The assistant
              is never given anything that changes data, whatever else is
              switched on.
            </Explain>
          </div>

          {settings.available_lookups.length === 0 ? (
            <p className="text-xs text-ink-faint" >
              Nothing yet. Allow some information on the Plugins tab and it
              appears here.
            </p>
          ) : (
            <div className="flex-wrap gap-1.5 flex items-center gap-3" >
              {settings.available_lookups.map((c) => (
                <Chip key={c} tone="accent">
                  {LOOKUP_WORDS[c] ?? c}
                </Chip>
              ))}
            </div>
          )}

          <p className="mt-3 max-w-[68ch] text-xs text-ink-faint" >
            It can draft a reply but never send one. Anything that changes a
            ticket stays with a person, so a message written by a customer can
            never cause an action.
          </p>
        </div>
      </div>
    </section>
  );
}
