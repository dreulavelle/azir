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
  const toast = useToast();

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

      {asking && (
        <Confirm
          rows={rows}
          onClose={() => setAsking(false)}
          onDone={async (removed) => {
            setAsking(false);
            await load();
            const said = removed.filter((r) => r.rows > 0);
            toast(said.length > 0 ? "Cleared" : "There was nothing to clear", {
              detail: said.map((r) => `${r.name} ${r.rows.toLocaleString()}`).join(" · ") || undefined,
            });
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
 * behind this one includes the log that would have said what happened.
 */
function Confirm({
  rows,
  onClose,
  onDone,
}: {
  rows: number;
  onClose: () => void;
  onDone: (removed: { name: string; rows: number }[]) => void;
}) {
  const [typed, setTyped] = useState("");
  const [busy, setBusy] = useState(false);
  const [problem, setProblem] = useState<string | null>(null);
  const ready = typed.trim().toLowerCase() === "clear";

  async function go() {
    setBusy(true);
    try {
      const { removed } = await api.clearData();
      onDone(removed ?? []);
    } catch (e) {
      setProblem(e instanceof Error ? e.message : "Could not clear");
      setBusy(false);
    }
  }

  return (
    <Dialog
      open
      onOpenChange={(next) => {
        if (!next && !busy) onClose();
      }}
      title="Start fresh"
      description={`This removes ${rows.toLocaleString()} ${
        rows === 1 ? "record" : "records"
      } and cannot be undone. Your account, your connections and your customer list are not touched.`}
      footer={
        <>
          <Button onClick={onClose} disabled={busy}>
            Cancel
          </Button>
          <Button weight="primary" onClick={() => void go()} disabled={!ready || busy}>
            {busy ? "Clearing…" : "Clear"}
          </Button>
        </>
      }
    >
      <div className="mt-4 space-y-3">
        <label htmlFor="clear-confirm" className="block">
          <Label>
            Type <span className="font-mono normal-case tracking-normal text-ink">clear</span> to
            confirm
          </Label>
        </label>
        <TextInput
          id="clear-confirm"
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
