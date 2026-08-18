import { cn } from "@/lib/cn";
import { useEffect, useMemo, useState } from "react";
import { api, type AuditEvent } from "./api";
import { Explain, Select, Tooltip } from "./components";
import { Chip, Empty, Icon, PanelHead, Problem, absolute, actionTitle, ago, initials } from "./ui";

/**
 * What has happened, in words.
 *
 * The log is written by the parts of Azir that record things, so its raw
 * vocabulary is theirs: tool.invoke, credential.resolve. That is exactly right
 * for a machine and exactly wrong for the person auditing their own deployment,
 * who wants to know whether anyone looked at a customer's data and whether
 * anything was changed.
 */

/** How each recorded action reads to a person. */
const ACTIONS: Record<string, { verb: string; note?: string }> = {
  login: { verb: "Signed in" },
  "login.sso": { verb: "Signed in with single sign-on" },
  "setup.complete": { verb: "Created the first administrator" },
  "user.create": { verb: "Added a person" },
  "access.denied": { verb: "Was refused", note: "Their role does not permit it." },

  "tool.invoke": { verb: "Looked something up" },
  "tool.write": { verb: "Made a change" },
  "capability.decide": { verb: "Changed what Azir is allowed to do" },

  "plugin.configure": { verb: "Changed a connection's settings" },
  "plugin.writes": { verb: "Changed whether Azir may make changes" },

  "credential.resolve": {
    verb: "Used a stored password",
    note: "A connection unlocked its own credential to make a request. The value is never shown or logged.",
  },
  "credential.put": { verb: "Stored a password" },
  "credential.delete": { verb: "Removed a stored password" },

  "auth.configure": { verb: "Changed sign-in settings" },
  "assistant.configure": { verb: "Changed assistant settings" },
  "assistant.ask": { verb: "Asked the assistant" },
  "assistant.blocked": {
    verb: "Stopped the assistant making a change",
    note: "The assistant is never given anything that writes. This is the second check that says so.",
  },
  "assistant.proposed": { verb: "Suggested a change" },
  "assistant.discarded": { verb: "Turned down a suggested change" },

  "user.role": { verb: "Changed someone's role" },
  "user.password": { verb: "Set someone's password" },
  "user.remove": { verb: "Removed a person" },
  "user.enabled": { verb: "Let someone back in" },
  "user.disabled": { verb: "Locked someone out" },
  "session.end": { verb: "Signed someone out" },
  "role.create": { verb: "Added a role" },
  "role.update": { verb: "Changed what a role can do" },
  "role.delete": { verb: "Removed a role" },

  "customer.create": { verb: "Added a customer" },
  "branding.change": { verb: "Changed the branding" },
  "branding.logo": { verb: "Changed the logo" },
  "webhook.rotate": { verb: "Changed a webhook address" },
  "webhook.received": {
    verb: "A connected system reported a change",
    note: "Azir refetched with its own credentials. Nothing a webhook says is stored or shown.",
  },
  "credential.rotate": {
    verb: "Re-encrypted the stored passwords",
    note: "Sealed again under a new key. The values are never shown or logged.",
  },

  "snapshot.upload": { verb: "Uploaded a support bundle" },
  "snapshot.pull": { verb: "Fetched a support bundle from a phone system" },
  "snapshot.attach": { verb: "Attached a capture to a ticket" },
  "snapshot.keep": { verb: "Changed how long a capture is kept" },
  "snapshot.delete": { verb: "Removed a capture" },

  "data.retention": { verb: "Changed how long things are kept" },
  "data.clear": {
    verb: "Started fresh",
    note: "Cleared the conversations, captures, ticket memory and this log. Accounts and connections were kept.",
  },
  "data.reset": {
    verb: "Reset everything",
    note: "Cleared the work and the setup, including customers and stored passwords. Accounts and roles were kept.",
  },
};

function describe(action: string) {
  return ACTIONS[action] ?? { verb: action.replace(/[._]/g, " ") };
}

/** What the answer came from — the internal words mean nothing to a reader. */
const SOURCES: Record<string, string> = {
  live: "asked the system directly",
  cache: "reused a recent answer",
  "cache-refreshing": "reused a recent answer while fetching a fresh one",
  assistant: "on the assistant's behalf",
};

function detailWords(detail?: string): string | undefined {
  if (!detail) return undefined;
  return SOURCES[detail] ?? detail;
}

/** Outcomes are open-ended; this reads intent rather than matching a fixed set. */
function outcomeTone(outcome: string): { tone: "good" | "warn" | "urgent" | ""; word: string } {
  const o = outcome.toLowerCase();
  if (o === "ok" || o === "success") return { tone: "good", word: "done" };
  if (/denied|refused|forbidden/.test(o)) return { tone: "urgent", word: "refused" };
  if (/fail|error/.test(o)) return { tone: "warn", word: "failed" };
  return { tone: "", word: outcome };
}

export function Audit() {
  const [events, setEvents] = useState<AuditEvent[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [who, setWho] = useState("");
  const [kind, setKind] = useState("");

  useEffect(() => {
    const load = async () => {
      try {
        setEvents(await api.audit());
        setError(null);
      } catch (e) {
        setError(e instanceof Error ? e.message : "The activity log could not be loaded.");
      }
    };
    void load();
  // A minute rather than fifteen seconds. The log is read while somebody is
  // looking into something, not watched like a monitor.
    const id = setInterval(() => void load(), 60_000);
    return () => clearInterval(id);
  }, []);

  const people = useMemo(() => {
    const seen = new Set((events ?? []).map((e) => e.actor_user_id).filter(Boolean));
    return [...seen].sort();
  }, [events]);

  const shown = useMemo(() => {
    return (events ?? []).filter((e) => {
      if (who && e.actor_user_id !== who) return false;
      if (kind === "changes" && !/write|configure|decide|create|delete|put/.test(e.action)) return false;
      if (kind === "refusals" && outcomeTone(e.outcome).tone !== "urgent") return false;
      if (kind === "assistant" && !e.action.startsWith("assistant")) return false;
      return true;
    });
  }, [events, who, kind]);

  if (error) return <Problem>{error}</Problem>;

  return (
    <section className="rounded-lg border border-edge bg-panel shadow-e1">
      <PanelHead>
        <div className="flex items-center gap-2">
          <h2>Activity</h2>
          <Explain>
            What Azir did and who asked for it. Never ticket contents or
            passwords.
          </Explain>
        </div>
        {events && (
          <span className="text-xs text-ink-faint">
            {shown.length === events.length
              ? `${events.length} recent`
              : `${shown.length} of ${events.length}`}
          </span>
        )}
      </PanelHead>

      <div className="px-4 pt-4">
        <div className="mb-4 flex flex-wrap items-center gap-2">
          {/* Radix treats an empty value as "nothing chosen", so the
              all-inclusive option can never render as the selection. The
              placeholder carries it instead, which also means the closed
              control says what it is filtering rather than "Select…". */}
          <Select
            label="Filter by person"
            placeholder="Everyone"
            value={who}
            onChange={setWho}
            options={[{ value: "", label: "Everyone" }, ...people.map((p) => ({ value: p, label: p }))]}
            width={210}
          />
          <Select
            label="Filter by kind"
            placeholder="Everything"
            value={kind}
            onChange={setKind}
            options={[
              { value: "", label: "Everything" },
              { value: "changes", label: "Only changes" },
              { value: "refusals", label: "Only refusals" },
              { value: "assistant", label: "Only the assistant" },
            ]}
            width={190}
          />
          {(who || kind) && (
            <button
              className="rounded-md px-2 py-1 text-xs text-ink-dim transition-colors hover:bg-sunken hover:text-ink"
              onClick={() => {
                setWho("");
                setKind("");
              }}
            >
              Clear
            </button>
          )}
        </div>
      </div>

      <div className="pt-0 p-4" >
        {!events && <div className="h-50 animate-pulse rounded-lg bg-sunken"  />}
        {events && shown.length === 0 && (
          <Empty headline={events.length === 0 ? "Nothing recorded yet" : "Nothing matches those filters"} />
        )}

        {shown.length > 0 && (
          <ol className="flex flex-col">
            {shown.map((e, i) => {
              const { verb, note } = describe(e.action);
              const outcome = outcomeTone(e.outcome);
              // The connection by name, and what it did in words rather than as a
              // dotted identifier.
              const target = [e.plugin, e.tool ? actionTitle(e.tool).toLowerCase() : ""]
                .filter(Boolean)
                .join(" · ");
              const detail = detailWords(e.detail);

              return (
                <li key={`${e.occurred_at}-${i}`} className="flex items-start gap-3 border-b border-edge/60 py-2 last:border-b-0">
                  <span
                    className={cn(
                      "mt-2 size-1.5 shrink-0 rounded-full",
                      outcome.tone === "urgent"
                        ? "bg-critical"
                        : outcome.tone === "warn"
                          ? "bg-attention"
                          : outcome.tone === "good"
                            ? "bg-steady"
                            : "bg-edge-strong",
                    )}
                    aria-hidden="true"
                  />

                  <span className="mt-0.5 grid size-6 shrink-0 place-items-center rounded-md bg-sunken font-mono text-[9px] font-semibold text-ink-dim">{initials(e.actor_user_id || "?")}</span>

                  <span className="gap-px flex min-w-0 flex-1 flex-col gap-0.5" >
                    <span className="text-sm">
                      <strong>{e.actor_user_id || "someone"}</strong>{" "}
                      {note ? (
                        <Tooltip content={note}>
                          <span className="font-medium">{verb.toLowerCase()}</span>
                        </Tooltip>
                      ) : (
                        <span>{verb.toLowerCase()}</span>
                      )}
                      {target && <span className="text-ink-dim"> — {target}</span>}
                    </span>
                    {detail && <span className="text-xs text-ink-faint">{detail}</span>}
                  </span>

                  {outcome.tone !== "good" && <Chip tone={outcome.tone}>{outcome.word}</Chip>}

                  <span className="whitespace-nowrap text-xs text-ink-faint" title={absolute(e.occurred_at)}>
                    {ago(e.occurred_at)}
                  </span>
                </li>
              );
            })}
          </ol>
        )}
      </div>
    </section>
  );
}

export { Icon };
