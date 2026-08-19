import { useCallback, useEffect, useState } from "react";
import {
  api,
  type Actor,
  type BulkPlan,
  type BlfKey,
  type BlfKind,
  type BulkSpec,
  type Customer,
  type ExtensionRow,
} from "../api";
import { CustomerSearch } from "../CustomerSearch";
import { Dialog } from "../components";
import { useToast } from "../Toast";
import {
  Button,
  Chip,
  Empty,
  Icon,
  Label,
  Panel,
  Problem,
  TextInput,
} from "../ui";
import { Editor, type Mode } from "./extensions/Editor";

/**
 * Extensions.
 *
 * The list first, and everything else from it. A technician arrives knowing
 * which extension they want, or roughly which ones — so the screen is the list
 * with a search over it, and editing one, editing several, making one and
 * removing one all start from a row rather than from a menu.
 *
 * This replaces a spreadsheet as the way in. The spreadsheet is still here,
 * behind Import and Export, because two hundred extensions is a job a form is
 * bad at — but nobody has to meet a CSV to rename somebody.
 *
 * Every write still goes through the same gate: what changes is shown before
 * it is applied, and nothing here is ever handed to the assistant.
 */
/** How many rows a page holds. Fifty is what fits without scrolling forever. */
const PER_PAGE = 50;

export function Extensions({ actor, go }: { actor: Actor; go?: (path: string) => void }) {
  const [customerID, setCustomerID] = useState("");
  const [customer, setCustomer] = useState<Customer | null>(null);
  const [rows, setRows] = useState<ExtensionRow[] | null>(null);
  const [total, setTotal] = useState(0);
  const [everything, setEverything] = useState(0);
  const [complete, setComplete] = useState(true);
  const [offset, setOffset] = useState(0);
  const [specs, setSpecs] = useState<BulkSpec[]>([]);
  const [kinds, setKinds] = useState<BlfKind[]>([]);
  const [find, setFind] = useState("");
  const [picked, setPicked] = useState<Set<string>>(new Set());
  const [opening, setOpening] = useState(false);
  const [pick, setPick] = useState("");
  const [picking, setPicking] = useState(false);
  const [missed, setMissed] = useState<string[]>([]);
  const [mode, setMode] = useState<Mode | null>(null);
  const [plan, setPlan] = useState<{ id: string; plan: BulkPlan } | null>(null);
  const [removing, setRemoving] = useState(false);
  const [busy, setBusy] = useState(false);
  const [problem, setProblem] = useState<string | null>(null);
  const [editorProblem, setEditorProblem] = useState<string | null>(null);
  const toast = useToast();

  const mayManage = actor.permissions.includes("phone.manage");

  const load = useCallback(async (id: string, q: string, at: number) => {
    if (!id) {
      setRows(null);
      return;
    }
    setProblem(null);
    try {
      const got = await api.extensionPage(id, q, PER_PAGE, at);
      setRows(got.extensions);
      setTotal(got.total);
      setEverything(got.all);
      setComplete(got.complete);
      setSpecs(got.fields);
    } catch (e) {
      setProblem(e instanceof Error ? e.message : "Could not read the phone system");
      setRows([]);
    }
  }, []);

  // Debounced, because the search runs on the server now — it has to, since
  // the browser is no longer holding the rows that do not match.
  useEffect(() => {
    if (!customerID) return;
    const timer = setTimeout(() => void load(customerID, find.trim(), offset), 200);
    return () => clearTimeout(timer);
  }, [customerID, find, offset, load]);

  useEffect(() => {
    setPicked(new Set());
    setOffset(0);
  }, [customerID]);

  // A new search starts at the beginning; page three of the old results is not
  // page three of the new ones.
  useEffect(() => setOffset(0), [find]);

  // One customer is not a choice.
  useEffect(() => {
    void (async () => {
      try {
        const first = await api.findCustomers("", 2);
        if (first.length === 1) {
          setCustomerID(first[0].id);
          setCustomer(first[0]);
        }
      } catch {
        // The search is how a customer is chosen and reports its own failures.
      }
    })();
  }, []);

  const shown = rows ?? [];

  /*
   * The next free number, after the highest.
   *
   * Worked out from the whole list rather than the page on screen: page one of
   * a thousand extensions has no idea what the highest number is, and a new
   * extension numbered 151 because that is what page three ended on would
   * collide with whatever is already there.
   */
  const [nextFree, setNextFree] = useState(100);
  useEffect(() => {
    if (!customerID) return;
    void (async () => {
      try {
        const last = await api.extensionPage(customerID, "", 1, Math.max(0, everything - 1));
        const top = Number(last.extensions[0]?.extension);
        setNextFree(Number.isFinite(top) && top > 0 ? top + 1 : 100);
      } catch {
        // The form still asks for a number; it just starts somewhere useful.
      }
    })();
  }, [customerID, everything]);

  /**
   * Adds whatever somebody wrote to the selection.
   *
   * "102-110, 119" is how a technician describes a floor of phones, and typing
   * eleven numbers to change eleven of them is work a computer should do. It
   * adds rather than replaces, so a selection can be built up out of several
   * goes — and unticking a row still takes one back out, which is the "except
   * 108" half of the same job.
   */
  async function addToSelection() {
    const asked = pick.trim();
    if (!asked) return;
    setPicking(true);
    setProblem(null);
    try {
      const got = await api.selectExtensions(customerID, asked);
      setPicked((was) => new Set([...was, ...got.selected]));
      setMissed(got.missing);
      setPick("");
    } catch (e) {
      setProblem(e instanceof Error ? e.message : "Could not read that selection");
    } finally {
      setPicking(false);
    }
  }

  /**
   * Opens the editor, fetching what it needs first.
   *
   * The table holds eight fields per row; the form holds all of them. Asking
   * for the rest of one extension when somebody opens it is what keeps a
   * thousand-row list from being a thousand rows of everything.
   */
  async function open(numbers: string[]) {
    setOpening(true);
    setProblem(null);
    try {
      const got = await api.extensionValues(customerID, numbers);
      if (got.extensions.length === 0) {
        setProblem("Those extensions are no longer on the phone system.");
        return;
      }
      setSpecs(got.fields);
      setKinds(got.kinds ?? []);
      setMode(
        got.extensions.length === 1
          ? { kind: "one", extension: got.extensions[0] }
          : { kind: "together", extensions: got.extensions },
      );
    } catch (e) {
      setProblem(e instanceof Error ? e.message : "Could not read those extensions");
    } finally {
      setOpening(false);
    }
  }

  function tick(extension: string, on: boolean) {
    setPicked((was) => {
      const next = new Set(was);
      if (on) next.add(extension);
      else next.delete(extension);
      return next;
    });
  }

  /** Editing one is applied straight away; it is one extension and one look. */
  async function saveOne(number: string, draft: Record<string, string>) {
    setBusy(true);
    setEditorProblem(null);
    try {
      await api.setExtension(customerID, number, draft);
      toast(`Extension ${number} saved`, { tone: "good" });
      setMode(null);
      await load(customerID, find.trim(), offset);
    } catch (e) {
      setEditorProblem(e instanceof Error ? e.message : "Could not save that");
    } finally {
      setBusy(false);
    }
  }

  /** Editing several goes through the before-and-after, as it always has. */
  async function reviewMany(draft: Record<string, string>, numbers: string[]) {
    setBusy(true);
    setEditorProblem(null);
    try {
      const wanted: Record<string, Record<string, string>> = {};
      for (const number of numbers) wanted[number] = draft;
      const got = await api.planChosen(customerID, wanted);
      setMode(null);
      setPlan({ id: got.edit.id, plan: got.plan });
    } catch (e) {
      setEditorProblem(e instanceof Error ? e.message : "Could not work out what would change");
    } finally {
      setBusy(false);
    }
  }

  async function create(draft: Record<string, string>, number: string) {
    setBusy(true);
    setEditorProblem(null);
    try {
      const got = await api.createExtension(customerID, number, draft);
      // The extension is made either way. Its settings are a second step, and
      // saying "created" when half of them were refused sends somebody looking
      // for the wrong problem.
      if (got.settings_problem) {
        setEditorProblem(
          `Extension ${number} was created, but its settings were not applied: ${got.settings_problem}`,
        );
        await load(customerID, find.trim(), offset);
        return;
      }
      toast(`Extension ${number} created`, { tone: "good" });
      setMode(null);
      await load(customerID, find.trim(), offset);
    } catch (e) {
      setEditorProblem(e instanceof Error ? e.message : "Could not create that");
    } finally {
      setBusy(false);
    }
  }

  async function applyPlan() {
    if (!plan) return;
    setBusy(true);
    try {
      const got = await api.applySheet(plan.id);
      toast(
        got.failed > 0 ? `${got.changed} changed, ${got.failed} failed` : `${got.changed} changed`,
        { tone: got.failed > 0 ? "bad" : "good" },
      );
      setPlan(null);
      setPicked(new Set());
      await load(customerID, find.trim(), offset);
    } catch (e) {
      setProblem(e instanceof Error ? e.message : "Could not apply");
    } finally {
      setBusy(false);
    }
  }

  /**
   * Saves a phone's buttons.
   *
   * On its own, not with the fields: the phone system keeps a key layout as
   * one value and writes it whole, so it is one request with its own approval
   * rather than another entry in a bag of changes.
   */
  async function saveKeys(numbers: string[], layout: { keys?: BlfKey[]; from?: string }) {
    setBusy(true);
    setEditorProblem(null);
    try {
      const got = await api.setExtensionKeys(customerID, numbers, layout);
      const failed = Object.entries(got.problems ?? {});
      if (failed.length > 0) {
        setEditorProblem(
          failed.map(([number, why]) => `${number}: ${why}`).join("; "),
        );
      } else {
        toast(`Buttons set on ${got.changed} ${got.changed === 1 ? "phone" : "phones"}`, {
          tone: "good",
        });
        setMode(null);
      }
      await load(customerID, find.trim(), offset);
    } catch (e) {
      setEditorProblem(e instanceof Error ? e.message : "Could not set the buttons");
    } finally {
      setBusy(false);
    }
  }

  async function remove() {
    setBusy(true);
    try {
      await api.removeExtensions(customerID, [...picked]);
      toast(`Removed ${picked.size}`, { tone: "good" });
      setRemoving(false);
      setPicked(new Set());
      await load(customerID, find.trim(), offset);
    } catch (e) {
      setProblem(e instanceof Error ? e.message : "Could not remove those");
      setRemoving(false);
    } finally {
      setBusy(false);
    }
  }

  if (!mayManage) {
    return (
      <div className="mx-auto max-w-[1180px] px-6 py-6">
        <Empty headline="You cannot change phone systems">
          Ask an admin for the phone permission.
        </Empty>
      </div>
    );
  }

  return (
    <div className="mx-auto max-w-[1180px] px-6 py-6">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <h1 className="text-2xl font-semibold tracking-tight">Extensions</h1>
          <p className="mt-1 max-w-[62ch] text-sm text-ink-dim">
            Everything on a customer's phone system, and everything you can change about it.
          </p>
        </div>
        {go && (
          <Button onClick={() => go("/bulk")}>
            <Icon.download />
            Import or export
          </Button>
        )}
      </div>

      {problem && (
        <div className="mt-4">
          <Problem>{problem}</Problem>
        </div>
      )}

      {!complete && (
        <p className="mt-4 rounded-lg border border-attention/30 bg-attention/10 px-3.5 py-2.5 text-sm text-attention">
          This phone system has more extensions than Azir will read in one go. What is below is not
          all of them — search for the one you want rather than working from the list.
        </p>
      )}

      <Panel className="mt-5">
        <div className="flex flex-wrap items-end gap-3 px-4 py-4">
          <div className="flex min-w-[260px] flex-col gap-1.5">
            <Label>Whose phone system</Label>
            <CustomerSearch
              value={customerID}
              ariaLabel="Whose extensions to show"
              placeholder="Type a customer's name"
              onChange={(id, got) => {
                setCustomerID(id);
                setCustomer(got ?? null);
              }}
            />
          </div>
          {customerID && (
            <div className="flex min-w-[220px] flex-1 flex-col gap-1.5">
              <Label>Find</Label>
              <TextInput
                value={find}
                placeholder="Number, name or email"
                onChange={(e) => setFind(e.target.value)}
              />
            </div>
          )}
          {customerID && (
            <div className="flex min-w-[240px] flex-col gap-1.5">
              <Label>Select for a bulk edit</Label>
              <div className="flex gap-2">
                <TextInput
                  value={pick}
                  placeholder="102-110, 119"
                  aria-label="Extensions to select, as numbers and ranges"
                  onChange={(e) => setPick(e.target.value)}
                  onKeyDown={(e) => {
                    if (e.key === "Enter") {
                      e.preventDefault();
                      void addToSelection();
                    }
                  }}
                />
                <Button disabled={picking || !pick.trim()} onClick={() => void addToSelection()}>
                  {picking ? "…" : "Add"}
                </Button>
              </div>
            </div>
          )}
          {customerID && rows && (
            <Button weight="primary" onClick={() => setMode({ kind: "new" })}>
              <Icon.plus />
              New extension
            </Button>
          )}
        </div>

        {missed.length > 0 && (
          <p className="border-t border-edge bg-attention/10 px-4 py-2 text-xs text-attention">
            Not on this phone system, so not selected: {missed.join(", ")}
            <button
              className="ml-2 rounded px-1.5 py-0.5 text-2xs underline"
              onClick={() => setMissed([])}
            >
              dismiss
            </button>
          </p>
        )}

        {picked.size > 0 && (
          <div className="flex flex-wrap items-center justify-between gap-3 border-t border-edge bg-sunken/50 px-4 py-2.5">
            <div className="min-w-0">
              <span className="text-sm font-medium">
                {picked.size} selected
                <button
                  className="ml-2 rounded-md px-1.5 py-0.5 text-xs font-normal text-ink-dim transition-colors hover:bg-panel hover:text-ink"
                  onClick={() => {
                    setPicked(new Set());
                    setMissed([]);
                  }}
                >
                  Clear
                </button>
              </span>
              {/* The selection is not the page. Something chosen by a range
                  can sit forty rows further down, and unticking its row is
                  only possible if you can find its row. */}
              <Chosen picked={picked} onDrop={(n) => tick(n, false)} />
            </div>
            <div className="flex gap-2">
              <Button disabled={opening} onClick={() => void open([...picked])}>
                {opening ? "Opening…" : "Edit together"}
              </Button>
              <Button onClick={() => setRemoving(true)}>Remove</Button>
            </div>
          </div>
        )}

        {!customerID && (
          <div className="px-4 pb-4">
            <Empty headline="Choose a customer to start" />
          </div>
        )}

        {customerID && !rows && <div className="m-4 h-64 animate-pulse rounded-lg bg-sunken" />}

        {customerID && rows && (
          <div className="overflow-x-auto border-t border-edge">
            <table className="w-full border-collapse text-sm">
              <thead>
                <tr>
                  <th className="w-[44px]">
                    <input
                      type="checkbox"
                      className="size-3.5 cursor-pointer align-middle accent-azir"
                      aria-label="Select every extension shown"
                      checked={shown.length > 0 && shown.every((e) => picked.has(e.extension))}
                      onChange={(e) =>
                        setPicked(
                          e.target.checked ? new Set(shown.map((x) => x.extension)) : new Set(),
                        )
                      }
                    />
                  </th>
                  <th className="w-[90px]">Ext</th>
                  <th>Name</th>
                  <th className="w-[220px]">Email</th>
                  <th className="w-[200px]">Set up</th>
                  <th className="w-[80px]" />
                </tr>
              </thead>
              <tbody>
                {shown.map((e) => (
                  <tr key={e.extension} className={picked.has(e.extension) ? "bg-sunken/40" : ""}>
                    <td>
                      <input
                        type="checkbox"
                        className="size-3.5 cursor-pointer align-middle accent-azir"
                        checked={picked.has(e.extension)}
                        aria-label={`Select extension ${e.extension}`}
                        onChange={(ev) => tick(e.extension, ev.target.checked)}
                      />
                    </td>
                    <td className="font-mono text-xs">{e.extension}</td>
                    <td className="font-medium">
                      {e.name || <span className="italic text-ink-faint">unnamed</span>}
                    </td>
                    <td className="text-ink-dim">{e.email || "—"}</td>
                    <td>
                      <Marks row={e} />
                    </td>
                    <td className="text-right">
                      <Button disabled={opening} onClick={() => void open([e.extension])}>
                        Edit
                      </Button>
                    </td>
                  </tr>
                ))}
                {shown.length === 0 && (
                  <tr>
                    <td colSpan={6} className="py-8 text-center text-sm text-ink-faint">
                      {find.trim()
                        ? `Nothing matches "${find.trim()}".`
                        : "This phone system has no extensions."}
                    </td>
                  </tr>
                )}
              </tbody>
            </table>
          </div>
        )}

        {customerID && rows && (
          <Pages
            offset={offset}
            shown={shown.length}
            total={total}
            everything={everything}
            searching={find.trim() !== ""}
            onGo={setOffset}
          />
        )}
      </Panel>

      {mode && (
        <Editor
          // Keyed so opening a different extension starts a fresh form rather
          // than carrying the last one's draft into it.
          key={
            mode.kind === "one"
              ? mode.extension.extension
              : mode.kind === "together"
                ? mode.extensions.map((e) => e.extension).join(",")
                : "new"
          }
          mode={mode}
          specs={specs}
          kinds={kinds}
          customer={customer?.display_name ?? "this customer"}
          busy={busy}
          problem={editorProblem}
          nextNumber={String(nextFree)}
          onClose={() => {
            setMode(null);
            setEditorProblem(null);
          }}
          onSaveKeys={(layout) =>
            void saveKeys(
              mode.kind === "one"
                ? [mode.extension.extension]
                : mode.kind === "together"
                  ? mode.extensions.map((e) => e.extension)
                  : [],
              layout,
            )
          }
          onSave={(draft, number) => {
            if (mode.kind === "one") void saveOne(mode.extension.extension, draft);
            else if (mode.kind === "together")
              void reviewMany(draft, mode.extensions.map((e) => e.extension));
            else void create(draft, number);
          }}
        />
      )}

      {plan && (
        <Dialog
          open
          onOpenChange={(next) => {
            if (!next && !busy) setPlan(null);
          }}
          title="Before and after"
          description={`${plan.plan.changing} of the ${picked.size} selected would change on ${customer?.display_name ?? "this customer"}'s phone system.`}
          footer={
            <>
              <Button onClick={() => setPlan(null)} disabled={busy}>
                Cancel
              </Button>
              <Button
                weight="primary"
                disabled={busy || plan.plan.changing === 0}
                onClick={() => void applyPlan()}
              >
                {busy ? "Applying…" : `Change ${plan.plan.changing}`}
              </Button>
            </>
          }
        >
          <div className="mt-4 max-h-[48vh] overflow-y-auto">
            <table className="w-full border-collapse text-sm">
              <tbody>
                {plan.plan.rows.map((row) =>
                  (row.changes ?? []).map((change, i) => (
                    <tr key={row.extension + change.field}>
                      <td className="w-[70px] font-mono text-xs">{i === 0 ? row.extension : ""}</td>
                      <td className="text-ink-dim">{change.label}</td>
                      <td className="text-ink-dim line-through decoration-critical/50">
                        {change.before || <span className="italic">unset</span>}
                      </td>
                      <td className="font-medium">{change.after}</td>
                    </tr>
                  )),
                )}
                {plan.plan.changing === 0 && (
                  <tr>
                    <td className="py-4 text-center text-sm text-ink-faint">
                      Every one of them already says that.
                    </td>
                  </tr>
                )}
              </tbody>
            </table>
          </div>
        </Dialog>
      )}

      {removing && (
        <Dialog
          open
          onOpenChange={(next) => {
            if (!next && !busy) setRemoving(false);
          }}
          title={`Remove ${picked.size} ${picked.size === 1 ? "extension" : "extensions"}`}
          description={
            <>
              This deletes {[...picked].join(", ")} from{" "}
              <strong>{customer?.display_name ?? "this customer"}</strong>'s phone system.
              <span className="mt-2 block text-critical">
                Removing an extension cannot be undone from here.
              </span>
            </>
          }
          footer={
            <>
              <Button onClick={() => setRemoving(false)} disabled={busy}>
                Cancel
              </Button>
              <Button weight="primary" disabled={busy} onClick={() => void remove()}>
                {busy ? "Removing…" : "Remove"}
              </Button>
            </>
          }
        >
          <span />
        </Dialog>
      )}
    </div>
  );
}

/**
 * What is switched on, at a glance.
 *
 * A row of chips rather than eight columns of yes and no: the question somebody
 * scans a list for is "which of these is set up differently", and a mark that
 * is only there when it is true answers that without being counted.
 */
/**
 * What is selected, as removable chips.
 *
 * Runs are written as ranges, because "102–110, 119" is what somebody typed
 * and reading ten separate chips back is worse than reading what they meant.
 * Long selections are cut off with a count rather than filling the screen.
 */
function Chosen({ picked, onDrop }: { picked: Set<string>; onDrop: (extension: string) => void }) {
  const numbers = [...picked].sort((a, b) => Number(a) - Number(b) || a.localeCompare(b));
  const shown = numbers.slice(0, 24);
  return (
    <span className="mt-1 flex flex-wrap items-center gap-1">
      {shown.map((n) => (
        <button
          key={n}
          className="inline-flex items-center gap-1 rounded border border-edge bg-panel px-1.5 py-px font-mono text-2xs text-ink-dim transition-colors hover:border-critical/40 hover:text-critical"
          title={`Take ${n} out of the selection`}
          onClick={() => onDrop(n)}
        >
          {n}
          <span aria-hidden="true">×</span>
        </button>
      ))}
      {numbers.length > shown.length && (
        <span className="text-2xs text-ink-faint">and {numbers.length - shown.length} more</span>
      )}
    </span>
  );
}

function Marks({ row }: { row: ExtensionRow }) {
  const marks: { label: string; tone: "good" | "warn" | "urgent" | "accent" | ""; title: string }[] = [];
  if (!row.enabled) marks.push({ label: "off", tone: "urgent", title: "This extension is disabled" });
  if (row.recording) marks.push({ label: "rec", tone: "accent", title: "Calls are recorded" });
  if (row.voicemail) marks.push({ label: "vm", tone: "", title: "Voicemail is on" });
  if (row.tunnel_blocked)
    marks.push({ label: "tunnel", tone: "warn", title: "Blocking remote non-tunnel connections" });
  if (row.no_audio)
    marks.push({ label: "no audio", tone: "warn", title: "The PBX does not deliver audio" });

  if (marks.length === 0) return <span className="text-xs text-ink-faint">—</span>;
  return (
    <span className="flex flex-wrap gap-1">
      {marks.map((m) => (
        <span key={m.label} title={m.title}>
          <Chip tone={m.tone}>{m.label}</Chip>
        </span>
      ))}
    </span>
  );
}

/**
 * Where you are in the list, and how to move.
 *
 * Numbers rather than "load more", because the question on a thousand-
 * extension system is "how many are there" as often as it is "show me the
 * next few", and an endless list never answers the first one.
 */
function Pages({
  offset,
  shown,
  total,
  everything,
  searching,
  onGo,
}: {
  offset: number;
  shown: number;
  total: number;
  everything: number;
  searching: boolean;
  onGo: (next: number) => void;
}) {
  const from = total === 0 ? 0 : offset + 1;
  const to = offset + shown;
  return (
    <div className="flex flex-wrap items-center justify-between gap-3 border-t border-edge px-4 py-2.5">
      <span className="text-xs text-ink-faint">
        {total === 0
          ? "Nothing to show"
          : `${from}–${to} of ${total}${searching ? ` matching, out of ${everything}` : ""}`}
      </span>
      {total > shown && (
        <div className="flex gap-2">
          <Button disabled={offset === 0} onClick={() => onGo(Math.max(0, offset - PER_PAGE))}>
            Back
          </Button>
          <Button disabled={to >= total} onClick={() => onGo(offset + PER_PAGE)}>
            Next
          </Button>
        </div>
      )}
    </div>
  );
}
