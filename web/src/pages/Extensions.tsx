import { useCallback, useEffect, useMemo, useState } from "react";
import {
  api,
  type Actor,
  type BulkExtension,
  type BulkPlan,
  type BulkSpec,
  type Customer,
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
export function Extensions({ actor, go }: { actor: Actor; go?: (path: string) => void }) {
  const [customerID, setCustomerID] = useState("");
  const [customer, setCustomer] = useState<Customer | null>(null);
  const [all, setAll] = useState<BulkExtension[] | null>(null);
  const [specs, setSpecs] = useState<BulkSpec[]>([]);
  const [find, setFind] = useState("");
  const [picked, setPicked] = useState<Set<string>>(new Set());
  const [mode, setMode] = useState<Mode | null>(null);
  const [plan, setPlan] = useState<{ id: string; plan: BulkPlan } | null>(null);
  const [removing, setRemoving] = useState(false);
  const [busy, setBusy] = useState(false);
  const [problem, setProblem] = useState<string | null>(null);
  const [editorProblem, setEditorProblem] = useState<string | null>(null);
  const toast = useToast();

  const mayManage = actor.permissions.includes("phone.manage");

  const load = useCallback(async (id: string) => {
    if (!id) {
      setAll(null);
      return;
    }
    setProblem(null);
    setAll(null);
    try {
      const got = await api.extensions(id);
      setAll(got.extensions);
      setSpecs(got.fields);
    } catch (e) {
      setProblem(e instanceof Error ? e.message : "Could not read the phone system");
      setAll([]);
    }
  }, []);

  useEffect(() => {
    void load(customerID);
    setPicked(new Set());
  }, [customerID, load]);

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

  const shown = useMemo(() => {
    const needle = find.trim().toLowerCase();
    if (!needle) return all ?? [];
    return (all ?? []).filter(
      (e) =>
        e.extension.includes(needle) ||
        e.name.toLowerCase().includes(needle) ||
        (e.values.EmailAddress ?? "").toLowerCase().includes(needle),
    );
  }, [all, find]);

  const chosen = useMemo(
    () => (all ?? []).filter((e) => picked.has(e.extension)),
    [all, picked],
  );

  /** The next free number, after the highest — never filling a gap. */
  const nextNumber = useMemo(() => {
    const top = (all ?? []).reduce((high, e) => {
      const n = Number(e.extension);
      return Number.isFinite(n) && n > high ? n : high;
    }, 0);
    return top > 0 ? String(top + 1) : "100";
  }, [all]);

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
      await load(customerID);
    } catch (e) {
      setEditorProblem(e instanceof Error ? e.message : "Could not save that");
    } finally {
      setBusy(false);
    }
  }

  /** Editing several goes through the before-and-after, as it always has. */
  async function reviewMany(draft: Record<string, string>) {
    setBusy(true);
    setEditorProblem(null);
    try {
      const wanted: Record<string, Record<string, string>> = {};
      for (const e of chosen) wanted[e.extension] = draft;
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
      await api.createExtension(customerID, number, draft);
      toast(`Extension ${number} created`, { tone: "good" });
      setMode(null);
      await load(customerID);
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
      await load(customerID);
    } catch (e) {
      setProblem(e instanceof Error ? e.message : "Could not apply");
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
      await load(customerID);
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
          {customerID && all && (
            <Button weight="primary" onClick={() => setMode({ kind: "new" })}>
              <Icon.plus />
              New extension
            </Button>
          )}
        </div>

        {picked.size > 0 && (
          <div className="flex flex-wrap items-center justify-between gap-3 border-t border-edge bg-sunken/50 px-4 py-2.5">
            <span className="text-sm font-medium">
              {picked.size} selected
              <button
                className="ml-2 rounded-md px-1.5 py-0.5 text-xs font-normal text-ink-dim transition-colors hover:bg-panel hover:text-ink"
                onClick={() => setPicked(new Set())}
              >
                Clear
              </button>
            </span>
            <div className="flex gap-2">
              <Button onClick={() => setMode({ kind: "together", extensions: chosen })}>
                Edit together
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

        {customerID && !all && <div className="m-4 h-64 animate-pulse rounded-lg bg-sunken" />}

        {customerID && all && (
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
                    <td className="text-ink-dim">{e.values.EmailAddress || "—"}</td>
                    <td>
                      <Marks values={e.values} enabled={e.enabled} />
                    </td>
                    <td className="text-right">
                      <Button onClick={() => setMode({ kind: "one", extension: e })}>Edit</Button>
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

        {customerID && all && shown.length > 0 && (
          <div className="border-t border-edge px-4 py-2.5 text-xs text-ink-faint">
            {shown.length === all.length
              ? `${all.length} extensions`
              : `${shown.length} of ${all.length}`}
          </div>
        )}
      </Panel>

      {mode && (
        <Editor
          mode={mode}
          specs={specs}
          customer={customer?.display_name ?? "this customer"}
          busy={busy}
          problem={editorProblem}
          nextNumber={nextNumber}
          onClose={() => {
            setMode(null);
            setEditorProblem(null);
          }}
          onSave={(draft, number) => {
            if (mode.kind === "one") void saveOne(mode.extension.extension, draft);
            else if (mode.kind === "together") void reviewMany(draft);
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
function Marks({ values, enabled }: { values: Record<string, string>; enabled: boolean }) {
  const marks: { label: string; tone: "good" | "warn" | "urgent" | "accent" | ""; title: string }[] = [];
  if (!enabled) marks.push({ label: "off", tone: "urgent", title: "This extension is disabled" });
  if (values.RecordCalls === "yes")
    marks.push({ label: "rec", tone: "accent", title: "Calls are recorded" });
  if (values.VMEnabled === "yes")
    marks.push({ label: "vm", tone: "", title: "Voicemail is on" });
  if (values.BlockTunnel === "yes")
    marks.push({ label: "tunnel", tone: "warn", title: "Blocking remote non-tunnel connections" });
  if (values.PbxDeliversAudio === "no")
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
