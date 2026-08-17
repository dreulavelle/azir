import { useEffect, useState } from "react";
import { api, chat, Perm, type Actor, type Registry } from "../api";
import type { Route } from "../router";
import { cn } from "@/lib/cn";
import { Icon, Label } from "../ui";

/**
 * What is left to do before Azir is useful.
 *
 * A self-hosted product is judged in the five minutes after someone first signs
 * in, and at that moment every screen is empty and correct — which reads as
 * broken. This turns that moment into a list, and each step is derived from
 * what is actually configured rather than from a flag someone has to remember
 * to set, so it cannot claim a step is done when it is not.
 *
 * It disappears entirely once there is nothing left to say.
 */

type Step = {
  title: string;
  detail: string;
  done: boolean;
  action?: { label: string; go: Route };
  /** Only an administrator can do something about it. */
  admin?: boolean;
};

export function GettingStarted({ actor, go }: { actor: Actor; go: (to: Route) => void }) {
  const [registry, setRegistry] = useState<Registry | null>(null);
  const [assistantReady, setAssistantReady] = useState<boolean | null>(null);

  useEffect(() => {
    let cancelled = false;
    (async () => {
      const [reg, status] = await Promise.allSettled([api.registry(), chat.status()]);
      if (cancelled) return;
      if (reg.status === "fulfilled") setRegistry(reg.value);
      setAssistantReady(status.status === "fulfilled" ? status.value.ready : false);
    })();
    return () => {
      cancelled = true;
    };
  }, []);

  if (!registry || assistantReady === null) return null;

  // A connection that only reports on Azir's own plumbing is not one somebody
  // set up, so it must not tick the box that says they did.
  const real = registry.plugins.filter(
    (p) => !p.tools.every((t) => (t.provides ?? []).every((c) => c === "diagnostic")),
  );
  const connected = real.filter((p) => p.ready);
  const anyAllowed = real.some((p) => p.tools.some((t) => t.status === "approved" && !t.mutates));
  const canConfigure = actor.permissions.includes(Perm.pluginConfigure);

  const steps: Step[] = [
    {
      title: "Create your account",
      detail: "Done — you are signed in as an administrator.",
      done: true,
    },
    {
      title: "Connect your helpdesk",
      detail: connected.length
        ? `${connected.map((p) => p.name).join(", ")} connected.`
        : "Add the address and API key for the system your tickets live in.",
      done: connected.length > 0,
      action: { label: "Set up a plugin", go: { name: "settings", tab: "plugins" } },
      admin: true,
    },
    {
      title: "Choose what Azir may read",
      detail: anyAllowed
        ? "Azir can read what you have allowed."
        : "Nothing is used until you allow it, so start by turning on what you want.",
      done: anyAllowed,
      action: { label: "Review what is allowed", go: { name: "settings", tab: "plugins" } },
      admin: true,
    },
    {
      title: "Turn on the assistant",
      detail: assistantReady
        ? "Ready — open it beside any ticket."
        : "Optional. Add a key for your model provider and it can read tickets and draft replies.",
      done: assistantReady,
      action: { label: "Set up the assistant", go: { name: "settings", tab: "assistant" } },
      admin: true,
    },
  ];

  const remaining = steps.filter((s) => !s.done);
  if (remaining.length === 0) return null;

  // Somebody who cannot change any of it should not be shown a list of things
  // to do; they need to know who to ask.
  if (!canConfigure) {
    return (
      <div className="rounded-lg border border-edge bg-panel p-5 shadow-e1">
        <h2 className="text-base font-semibold">Azir is not set up yet</h2>
        <p className="mt-1.5 max-w-[60ch] text-sm text-ink-dim">
          An administrator needs to connect your helpdesk before there is
          anything here. Nothing you do will be lost in the meantime.
        </p>
      </div>
    );
  }

  const next = steps.findIndex((s) => !s.done);

  return (
    <div className="mb-7 rounded-lg border border-edge bg-panel p-5 shadow-e1">
      <div className="mb-4 flex items-baseline justify-between gap-4">
        <h2 className="text-base font-semibold">Getting started</h2>
        <Label>
          {steps.length - remaining.length} of {steps.length} done
        </Label>
      </div>

      <ol className="flex flex-col">
          {steps.map((step, i) => (
            <li
              key={step.title}
              className="flex items-center gap-3 border-b border-edge/60 py-2.5 last:border-b-0"
            >
              {/* Done reads as a tick rather than a struck-through line: the
                  point is that it is behind you, not that it was cancelled. */}
              <span
                className={cn(
                  "grid size-6 shrink-0 place-items-center rounded-full border font-mono text-2xs",
                  step.done
                    ? "border-steady/40 bg-steady/10 text-steady"
                    : "border-edge text-ink-faint",
                )}
                aria-hidden="true"
              >
                {step.done ? <Icon.tick /> : i + 1}
              </span>

              <span className="flex min-w-0 flex-1 flex-col">
                <span className={cn("text-sm font-medium", step.done && "text-ink-dim")}>
                  {step.title}
                </span>
                <span className="text-xs text-ink-faint">{step.detail}</span>
              </span>

              {/* Only the next thing gets a button. Four buttons is a menu; one
                  is a direction. */}
              {!step.done && i === next && step.action && (
                <button
                  className="h-7 shrink-0 rounded-md bg-azir px-3 text-xs font-medium text-azir-ink transition-opacity hover:opacity-90"
                  onClick={() => go(step.action!.go)}
                >
                  {step.action.label}
                </button>
              )}
            </li>
          ))}
      </ol>
    </div>
  );
}
