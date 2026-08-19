import type { BulkEdit, BulkPlan, BulkRow } from "../../api";
import { Tooltip } from "../../components";
import { Button, Chip, Panel, PanelHead } from "../../ui";
import { doing, type Counts } from "./words";

/**
 * The before and after.
 *
 * Every row that would do something can be unticked. One approval for the
 * batch, as agreed — but a batch is not all-or-nothing, and the row somebody
 * wants to drop is usually the one a colleague touched since the plan was
 * made. That row is on screen with its current value; unticking it is how you
 * decline it without abandoning the other thirty-nine.
 */
export function Diff({
  edit,
  customer,
  plan,
  skip,
  setSkip,
  counts,
  busy,
  onApply,
  onBack,
  onDone,
  onDiscard,
  onRevert,
}: {
  edit: BulkEdit;
  customer?: string;
  plan: BulkPlan;
  skip: Set<string>;
  setSkip: (next: Set<string>) => void;
  counts: Counts;
  busy: boolean;
  onApply: () => void;
  onBack: () => void;
  onDone: () => void;
  onDiscard: () => void;
  onRevert: () => void;
}) {
  const applied = edit.status === "applied";
  const outcome = new Map((edit.outcome ?? []).map((o) => [o.extension, o]));
  const planRows = plan.rows ?? [];
  const acting = planRows.filter(
    (r) => !r.problem && (r.gone || r.new || (r.changes?.length ?? 0) > 0),
  );
  const total = counts.change + counts.create + counts.remove;

  function tick(extension: string, on: boolean) {
    const next = new Set(skip);
    if (on) next.delete(extension);
    else next.add(extension);
    setSkip(next);
  }

  return (
    <Panel className="mt-5" rail={counts.remove > 0 && !applied ? "critical" : undefined}>
      <PanelHead>
        <div>
          <h2 className="text-sm font-medium">
            {edit.filename}
            {/* Whose phone system, on the screen where getting that wrong is
                the worst thing this feature can do. */}
            {customer && <span className="text-ink-dim"> — {customer}</span>}
          </h2>
          <p className="mt-0.5 text-xs text-ink-dim">
            {plan.changing} changing
            {plan.creating > 0 && <> · {plan.creating} new</>}
            {plan.removing > 0 && <> · {plan.removing} to remove</>} · {plan.unchanged} already{" "}
            {plan.unchanged === 1 ? "matches" : "match"} · {plan.skipped} skipped
          </p>
        </div>
        {applied && <Chip tone="good">applied</Chip>}
      </PanelHead>

      {!applied && acting.length > 1 && (
        <div className="flex items-center justify-between gap-3 border-b border-edge px-4 py-2.5">
          <p className="text-xs text-ink-faint">
            Untick anything you do not want. {total} of {acting.length} still ticked.
          </p>
          <div className="flex gap-2">
            <button
              className="rounded-md px-2 py-1 text-xs text-ink-dim transition-colors hover:bg-sunken hover:text-ink"
              onClick={() => setSkip(new Set())}
            >
              Tick all
            </button>
            <button
              className="rounded-md px-2 py-1 text-xs text-ink-dim transition-colors hover:bg-sunken hover:text-ink"
              onClick={() => setSkip(new Set(acting.map((r) => r.extension)))}
            >
              Untick all
            </button>
          </div>
        </div>
      )}

      <div className="overflow-x-auto">
        <table className="w-full border-collapse text-sm">
          <thead>
            <tr>
              <th className="w-[44px]" />
              <th className="w-[90px]">Ext</th>
              <th className="w-[180px]">Field</th>
              <th>Before</th>
              <th>After</th>
              <th className="w-[110px]" />
            </tr>
          </thead>
          <tbody>
            {planRows.map((row) => (
              <Rows
                key={row.extension + row.name}
                row={row}
                said={outcome.get(row.extension)}
                applied={applied}
                ticked={!skip.has(row.extension)}
                onTick={(on) => tick(row.extension, on)}
              />
            ))}
          </tbody>
        </table>
      </div>

      <div className="flex flex-wrap items-center justify-between gap-3 border-t border-edge px-4 py-3">
        <p className="max-w-[58ch] text-xs text-ink-faint">
          Only ticked rows are touched. Rows that already match, or that name an extension the phone
          system does not have, are left alone either way.
        </p>
        <div className="flex gap-2">
          {applied ? (
            <>
              <Button onClick={onRevert} disabled={busy}>
                {busy ? "Working…" : "Put it back"}
              </Button>
              <Button onClick={onDone}>Done</Button>
            </>
          ) : (
            <>
              <Button onClick={onBack} disabled={busy}>
                Back
              </Button>
              <Button onClick={onDiscard} disabled={busy}>
                Discard
              </Button>
              <Button weight="primary" onClick={onApply} disabled={busy || total === 0}>
                {total === 0 ? "Nothing ticked" : doing(counts)}
              </Button>
            </>
          )}
        </div>
      </div>
    </Panel>
  );
}

/** One extension, as one row per field it changes. */
function Rows({
  row,
  said,
  applied,
  ticked,
  onTick,
}: {
  row: BulkRow;
  said?: { ok: boolean; left?: boolean; problem?: string };
  applied: boolean;
  ticked: boolean;
  onTick: (on: boolean) => void;
}) {
  const tickBox = (label: string) =>
    applied ? null : (
      <input
        type="checkbox"
        className="size-3.5 cursor-pointer align-middle accent-azir"
        checked={ticked}
        aria-label={label}
        onChange={(e) => onTick(e.target.checked)}
      />
    );

  const outcome = () => {
    if (!said) return null;
    if (said.left) return <span className="text-ink-faint">left alone</span>;
    if (!said.ok) return <span className="text-critical">{said.problem}</span>;
    return <span className="text-steady">done</span>;
  };

  const dim = !applied && !ticked;

  if (row.problem) {
    return (
      <tr className="text-ink-faint">
        <td />
        <td className="font-mono text-xs">{row.extension || "—"}</td>
        <td colSpan={4} className="text-xs">
          {row.problem}
        </td>
      </tr>
    );
  }

  if (row.gone) {
    return (
      <tr className={dim ? "opacity-45" : ""}>
        <td>{tickBox(`Remove extension ${row.extension}`)}</td>
        <td className="font-mono text-xs">{row.extension}</td>
        <td>
          <Tooltip content="This sheet created it, so it did not exist before.">
            <Chip tone="urgent">remove</Chip>
          </Tooltip>
        </td>
        <td className="font-medium">{row.name || <span className="italic">unset</span>}</td>
        <td className="text-ink-faint italic">gone</td>
        <td className="text-right text-xs">{outcome()}</td>
      </tr>
    );
  }

  if (row.new) {
    return (
      <tr className={dim ? "opacity-45" : ""}>
        <td>{tickBox(`Create extension ${row.extension}`)}</td>
        <td className="font-mono text-xs">{row.extension}</td>
        <td>
          <Chip tone="accent">new</Chip>
        </td>
        <td className="text-ink-faint italic">does not exist</td>
        <td className="font-medium">{row.wanted}</td>
        <td className="text-right text-xs">{outcome()}</td>
      </tr>
    );
  }

  if (!row.changes || row.changes.length === 0) {
    return (
      <tr className="text-ink-faint">
        <td />
        <td className="font-mono text-xs">{row.extension}</td>
        <td colSpan={4} className="text-xs">
          already matches
        </td>
      </tr>
    );
  }

  return (
    <>
      {row.changes.map((change, i) => (
        <tr key={change.field} className={dim ? "opacity-45" : ""}>
          <td>{i === 0 ? tickBox(`Change extension ${row.extension}`) : null}</td>
          <td className="font-mono text-xs">{i === 0 ? row.extension : ""}</td>
          <td className="text-ink-dim">{change.label}</td>
          <td className="text-ink-dim line-through decoration-critical/50">
            {change.before || <span className="italic">unset</span>}
          </td>
          <td className="font-medium">{change.after}</td>
          <td className="text-right text-xs">{i === 0 ? outcome() : null}</td>
        </tr>
      ))}
    </>
  );
}
