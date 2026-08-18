import { useCallback, useEffect, useState } from "react";
import { api, type DataUsage, type Stored } from "./api";
import { Dialog } from "./components";
import { useToast } from "./Toast";
import { Button, Chip, Label, Loading, Panel, PanelHead, Problem, TextInput } from "./ui";

/**
 * What Azir is holding, and how to put it back to empty.
 *
 * Not a database console, and the difference matters. Somebody running an MSP
 * has to be able to answer what of their customers' data is sitting in their
 * own tooling and when it leaves — to a client, to an auditor, or to
 * themselves at the end of a job. This page answers that in the words they
 * would use to ask it, and gives them the one action that follows from the
 * answer.
 *
 * The organising idea is the line between what Azir was set up with and what
 * Azir has done. Everything is on one side or the other, said plainly next to
 * each row, because which side a thing falls on is the entire question anybody
 * is asking before they press the button.
 */
export function DataSettings() {
  const [usage, setUsage] = useState<DataUsage | null>(null);
  const [problem, setProblem] = useState<string | null>(null);
  const [asking, setAsking] = useState(false);
  const [resetting, setResetting] = useState(false);
  const toast = useToast();

  // Says what went, in the same words both buttons report in.
  const said = (removed: { name: string; rows: number }[], done: string, empty: string) => {
    const parts = removed.filter((r) => r.rows > 0);
    toast(parts.length > 0 ? done : empty, {
      detail: parts.map((r) => `${r.name} ${r.rows.toLocaleString()}`).join(" · ") || undefined,
    });
  };

  const load = useCallback(async () => {
    try {
      setUsage(await api.data());
      setProblem(null);
    } catch (e) {
      setProblem(e instanceof Error ? e.message : "Could not read what is stored");
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  if (problem) return <Problem>{problem}</Problem>;
  if (!usage) return <Loading rows={6} />;

  const work = usage.stored.filter((s) => s.cleared);
  const setup = usage.stored.filter((s) => !s.cleared);
  const rows = work.reduce((n, s) => n + s.count, 0);
  // The reset reaches the setup too, minus the accounts it deliberately keeps.
  const all = usage.stored
    .filter((s) => s.name !== "People")
    .reduce((n, s) => n + s.count, 0);
  const stranded = usage.sign_in?.sso_only ?? [];
  const canPassword = usage.sign_in?.with_password ?? [];

  return (
    <div className="space-y-5">
      <Panel>
        <PanelHead>
          <div>
            <h2 className="text-sm font-medium">What Azir has done</h2>
            <p className="mt-0.5 text-xs text-ink-dim">
              Everything below is produced by using Azir, and starting fresh removes all of it.
            </p>
          </div>
          <span className="font-mono text-xs text-ink-faint">{bytes(sum(work))}</span>
        </PanelHead>
        <Kinds of={work} />
      </Panel>

      <Panel>
        <PanelHead>
          <div>
            <h2 className="text-sm font-medium">What Azir was set up with</h2>
            <p className="mt-0.5 text-xs text-ink-dim">
              Kept when you start fresh, so you stay signed in and still connected.
            </p>
          </div>
          <span className="font-mono text-xs text-ink-faint">{bytes(sum(setup))}</span>
        </PanelHead>
        <Kinds of={setup} />
      </Panel>

      <Panel>
        <PanelHead>
          <h2 className="text-sm font-medium">Start fresh</h2>
          <span className="font-mono text-xs text-ink-faint">
            {bytes(usage.total)} on disk altogether
          </span>
        </PanelHead>
        <div className="flex flex-wrap items-center justify-between gap-4 px-4 py-4">
          <p className="max-w-[62ch] text-sm text-ink-dim">
            Clears every conversation, diagnostic capture, remembered ticket, proposed change and
            the activity log — {rows.toLocaleString()}{" "}
            {rows === 1 ? "record" : "records"} in all. Your account, your connections and your
            customer list stay exactly as they are. This cannot be undone.
          </p>
          <Button weight="primary" onClick={() => setAsking(true)} disabled={rows === 0}>
            {rows === 0 ? "Nothing to clear" : "Start fresh"}
          </Button>
        </div>
      </Panel>

      {/* The larger of the two, and marked as such. Everything about this
          panel is one step louder than the one above it, because the two sit
          on the same screen and the only thing separating them is reading. */}
      <section className="rounded-lg border border-critical/40 bg-panel shadow-e1">
        <div className="flex items-center justify-between border-b border-critical/25 bg-critical/[0.06] px-4 py-2.5">
          <h2 className="text-sm font-medium text-critical">Reset everything</h2>
          <span className="font-mono text-xs text-critical/70">
            {all.toLocaleString()} {all === 1 ? "record" : "records"}
          </span>
        </div>
        <div className="px-4 py-4">
          <p className="max-w-[62ch] text-sm text-ink-dim">
            Everything above, and the setup with it: your customers, the stored passwords for the
            systems they were reached through, which tools were approved, the assistant's key and
            model, your branding, and single sign-on. Azir comes back the way it arrived.
          </p>
          <p className="mt-2 max-w-[62ch] text-sm text-ink-dim">
            Accounts and roles are kept, so you stay signed in and set it up again from an empty
            console rather than installing it again.
          </p>

          {/* Said before the button, not after it. Removing single sign-on
              strands anybody who has never had a password, and that is not
              something to find out by being locked out. */}
          {stranded.length > 0 && (
            <div className="mt-3 rounded-md border border-attention/30 bg-attention/10 px-3 py-2.5">
              <p className="text-xs text-attention">
                {stranded.length === 1 ? "This account signs in" : "These accounts sign in"} with
                your identity provider and {stranded.length === 1 ? "has" : "have"} no password, so{" "}
                {stranded.length === 1 ? "it" : "they"} will not be able to sign in afterwards:
              </p>
              <p className="mt-1 font-mono text-xs text-attention">{stranded.join(", ")}</p>
              {canPassword.length === 0 && (
                <p className="mt-2 text-xs text-attention">
                  Nobody has a password, so this is refused until at least one administrator does.
                </p>
              )}
            </div>
          )}

          <div className="mt-4 flex justify-end">
            <Button
              weight="primary"
              onClick={() => setResetting(true)}
              disabled={all === 0 || canPassword.length === 0}
            >
              {all === 0 ? "Nothing to reset" : "Reset everything"}
            </Button>
          </div>
        </div>
      </section>

      {asking && (
        <Confirm
          word="clear"
          title="Start fresh"
          description={`This removes ${rows.toLocaleString()} ${
            rows === 1 ? "record" : "records"
          } and cannot be undone. Your account, your connections and your customer list are not touched.`}
          run={api.clearData}
          onClose={() => setAsking(false)}
          onDone={async (removed) => {
            setAsking(false);
            await load();
            said(removed, "Cleared", "There was nothing to clear");
          }}
        />
      )}

      {resetting && (
        <Confirm
          word="reset"
          title="Reset everything"
          description={`This removes ${all.toLocaleString()} ${
            all === 1 ? "record" : "records"
          } — every customer, every stored password, every approval, your branding and single sign-on. Accounts and roles are kept. It cannot be undone.`}
          run={api.resetData}
          onClose={() => setResetting(false)}
          onDone={async (removed) => {
            setResetting(false);
            await load();
            said(removed, "Reset", "There was nothing to reset");
          }}
        />
      )}
    </div>
  );
}

/** One side of the line, as rows. */
function Kinds({ of }: { of: Stored[] }) {
  return (
    <ul className="divide-y divide-edge">
      {of.map((s) => (
        <li key={s.name} className="flex items-baseline gap-4 px-4 py-3">
          <div className="min-w-0 flex-1">
            <div className="flex items-center gap-2">
              <span className="text-sm font-medium">{s.name}</span>
              {s.expires && <Chip>goes after {s.expires}</Chip>}
            </div>
            <p className="mt-0.5 text-xs text-ink-dim">{s.detail}</p>
          </div>
          <span className="shrink-0 text-right font-mono text-sm tabular-nums">
            {s.count.toLocaleString()}
          </span>
          <span className="w-20 shrink-0 text-right font-mono text-xs tabular-nums text-ink-faint">
            {bytes(s.bytes)}
          </span>
        </li>
      ))}
    </ul>
  );
}

/**
 * The confirmation.
 *
 * A word has to be typed rather than a second button pressed. Anything that
 * needs only one more click is something people learn to click, and what is
 * behind these includes the log that would have said what happened.
 *
 * The word differs between the two, deliberately. They sit on one screen and
 * the smaller of them is the one somebody will have pressed before, so sharing
 * a word would make the habit of the safe action into the habit of the other
 * one. The server checks it too, and refuses a request without it — this
 * dialog is not the only thing that can call either endpoint.
 */
function Confirm({
  word,
  title,
  description,
  run,
  onClose,
  onDone,
}: {
  word: string;
  title: string;
  description: string;
  run: () => Promise<{ removed: { name: string; rows: number }[] }>;
  onClose: () => void;
  onDone: (removed: { name: string; rows: number }[]) => void;
}) {
  const [typed, setTyped] = useState("");
  const [busy, setBusy] = useState(false);
  const [problem, setProblem] = useState<string | null>(null);
  const ready = typed.trim().toLowerCase() === word;

  async function go() {
    setBusy(true);
    try {
      const { removed } = await run();
      onDone(removed ?? []);
    } catch (e) {
      setProblem(e instanceof Error ? e.message : "Could not finish");
      setBusy(false);
    }
  }

  return (
    <Dialog
      open
      onOpenChange={(next) => {
        if (!next && !busy) onClose();
      }}
      title={title}
      description={description}
      footer={
        <>
          <Button onClick={onClose} disabled={busy}>
            Cancel
          </Button>
          <Button weight="primary" onClick={() => void go()} disabled={!ready || busy}>
            {busy ? "Working…" : title}
          </Button>
        </>
      }
    >
      <div className="mt-4 space-y-3">
        <label htmlFor="confirm-word" className="block">
          <Label>
            Type <span className="font-mono normal-case tracking-normal text-ink">{word}</span> to
            confirm
          </Label>
        </label>
        <TextInput
          id="confirm-word"
          value={typed}
          autoFocus
          autoComplete="off"
          spellCheck={false}
          onChange={(e) => setTyped(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Enter" && ready && !busy) void go();
          }}
        />
        {problem && <Problem>{problem}</Problem>}
      </div>
    </Dialog>
  );
}

function sum(of: Stored[]): number {
  return of.reduce((n, s) => n + s.bytes, 0);
}

/**
 * Bytes at the precision somebody actually reads.
 *
 * Whole numbers once past a megabyte: the difference between 47 MB and 47.3 MB
 * changes no decision anybody makes on this page, and the extra digit is one
 * more thing to scan past.
 */
function bytes(n: number): string {
  if (n < 1024) return `${n} B`;
  const units = ["KB", "MB", "GB", "TB"];
  let size = n / 1024;
  let unit = 0;
  while (size >= 1024 && unit < units.length - 1) {
    size /= 1024;
    unit += 1;
  }
  return `${size < 10 ? size.toFixed(1) : Math.round(size)} ${units[unit]}`;
}
