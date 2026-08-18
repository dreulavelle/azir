import { useCallback, useEffect, useState } from "react";
import {
  api,
  type Actor,
  type BulkEdit,
  type BulkField,
  type BulkMapping,
  type BulkPlan,
  type BulkRow,
  type Customer,
} from "../api";
import { Dialog } from "../components";
import { useToast } from "../Toast";
import {
  Button,
  Chip,
  Empty,
  Label,
  Panel,
  PanelHead,
  Picker,
  Problem,
} from "../ui";

/**
 * Changing many extensions from a sheet.
 *
 * Three steps, and the middle one is the point. The file says what somebody
 * wants; the phone system says what is true now; what gets approved is the
 * difference. A sheet exported last Tuesday and edited since describes a system
 * that has moved on, and applying it wholesale would quietly undo whatever
 * changed in between.
 *
 * The file is read by Azir and never by the assistant. A customer's extension
 * list is their data, and the rule everywhere else here is that it does not
 * leave for a model to read — so the columns are mapped in a dropdown rather
 * than worked out by asking one.
 */
export function BulkEdits({ actor }: { actor: Actor }) {
  const [customers, setCustomers] = useState<Customer[] | null>(null);
  const [customerID, setCustomerID] = useState("");
  const [edit, setEdit] = useState<BulkEdit | null>(null);
  const [editable, setEditable] = useState<BulkField[]>([]);
  const [mapping, setMapping] = useState<BulkMapping | null>(null);
  const [plan, setPlan] = useState<BulkPlan | null>(null);
  const [busy, setBusy] = useState(false);
  const [problem, setProblem] = useState<string | null>(null);
  const [asking, setAsking] = useState(false);
  const [recent, setRecent] = useState<BulkEdit[]>([]);
  const toast = useToast();

  // Sheets already uploaded for this customer. The server holds them between
  // the upload and the decision precisely so a refresh does not lose one, and
  // without this list there would be no way back to it.
  const loadRecent = useCallback(async (id: string) => {
    if (!id) return setRecent([]);
    try {
      setRecent((await api.bulkEdits(id)).edits);
    } catch {
      setRecent([]);
    }
  }, []);

  useEffect(() => {
    void loadRecent(customerID);
  }, [customerID, loadRecent, edit]);

  async function reopen(id: string) {
    setBusy(true);
    setProblem(null);
    try {
      const got = await api.bulkEdit(id);
      setEdit(got.edit);
      setEditable(got.editable);
      setMapping(got.edit.mapping ?? null);
      setPlan(got.edit.plan ?? null);
    } catch (e) {
      setProblem(e instanceof Error ? e.message : "Could not open that sheet");
    } finally {
      setBusy(false);
    }
  }

  useEffect(() => {
    void (async () => {
      try {
        const list = await api.customers();
        setCustomers(list);
        if (list.length === 1) setCustomerID(list[0].id);
      } catch (e) {
        setProblem(e instanceof Error ? e.message : "Could not load your customers");
      }
    })();
  }, []);

  const reset = useCallback(() => {
    setEdit(null);
    setMapping(null);
    setPlan(null);
    setProblem(null);
  }, []);

  async function upload(file: File) {
    setBusy(true);
    setProblem(null);
    try {
      const got = await api.uploadSheet(customerID, file);
      setEdit(got.edit);
      setEditable(got.editable);
      setMapping(got.suggests);
      setPlan(null);
    } catch (e) {
      setProblem(e instanceof Error ? e.message : "That sheet could not be read");
    } finally {
      setBusy(false);
    }
  }

  async function compare() {
    if (!edit || !mapping) return;
    setBusy(true);
    setProblem(null);
    try {
      const got = await api.planSheet(edit.id, mapping);
      setEdit(got.edit);
      setPlan(got.plan);
    } catch (e) {
      setProblem(e instanceof Error ? e.message : "Could not compare against the phone system");
    } finally {
      setBusy(false);
    }
  }

  async function revert() {
    if (!edit) return;
    setBusy(true);
    setProblem(null);
    try {
      const got = await api.revertSheet(edit.id);
      setEdit(got.edit);
      setPlan(got.plan);
      if (got.created > 0) {
        toast(`${got.created} created ${got.created === 1 ? "extension is" : "extensions are"} not removed`, {
          detail: "Putting a sheet back does not delete what it made.",
        });
      }
    } catch (e) {
      setProblem(e instanceof Error ? e.message : "Could not build the undo");
    } finally {
      setBusy(false);
    }
  }

  async function apply() {
    if (!edit) return;
    setBusy(true);
    try {
      const got = await api.applySheet(edit.id);
      setAsking(false);
      setEdit(got.edit);
      const said = [
        got.changed > 0 ? `changed ${got.changed}` : "",
        got.created > 0 ? `created ${got.created}` : "",
        got.failed > 0 ? `${got.failed} failed` : "",
      ].filter(Boolean);
      toast(said.join(", ") || "Nothing to do", { tone: got.failed > 0 ? "bad" : "good" });
    } catch (e) {
      setAsking(false);
      setProblem(e instanceof Error ? e.message : "Could not apply");
    } finally {
      setBusy(false);
    }
  }

  if (!actor.permissions.includes("phone.manage")) {
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
      <h1 className="text-2xl font-semibold tracking-tight">Bulk edits</h1>
      <p className="mt-1 max-w-[62ch] text-sm text-ink-dim">
        Change many extensions from a spreadsheet. You see a before and after before anything is
        applied. The file is never sent to the assistant.
      </p>

      {problem && (
        <div className="mt-4">
          <Problem>{problem}</Problem>
        </div>
      )}

      {!edit && (
        <Panel className="mt-5">
          <PanelHead>
            <h2 className="text-sm font-medium">Upload a sheet</h2>
          </PanelHead>
          <div className="flex flex-wrap items-end gap-3 px-4 py-4">
            <div className="flex min-w-[220px] flex-col gap-1.5">
              <Label>Customer</Label>
              <Picker value={customerID} onChange={(e) => setCustomerID(e.target.value)}>
                <option value="">Choose one</option>
                {(customers ?? []).map((c) => (
                  <option key={c.id} value={c.id}>
                    {c.display_name}
                  </option>
                ))}
              </Picker>
            </div>
            <div className="flex min-w-[260px] flex-1 flex-col gap-1.5">
              <Label>Sheet</Label>
              <input
                type="file"
                accept=".csv,.tsv,.txt,text/csv,text/plain"
                disabled={!customerID || busy}
                className="text-sm file:mr-3 file:rounded-md file:border file:border-edge file:bg-panel file:px-2.5 file:py-1 file:text-xs file:font-medium disabled:opacity-50"
                onChange={(e) => {
                  const file = e.target.files?.[0];
                  if (file) void upload(file);
                  e.target.value = "";
                }}
              />
            </div>
          </div>
          <p className="px-4 pb-4 text-xs text-ink-faint">
            CSV or tab-separated, with a header row. One column holds the extension number.
          </p>

          {recent.length > 0 && (
            <div className="border-t border-edge">
              <ul className="divide-y divide-edge">
                {recent.map((r) => (
                  <li key={r.id} className="flex items-center justify-between gap-3 px-4 py-2.5">
                    <div className="min-w-0">
                      <span className="text-sm font-medium">{r.filename}</span>
                      <span className="ml-2 text-xs text-ink-faint">
                        {r.sheet.rows.length} {r.sheet.rows.length === 1 ? "row" : "rows"} ·{" "}
                        {r.uploaded_by}
                      </span>
                    </div>
                    <div className="flex shrink-0 items-center gap-2">
                      <Chip tone={r.status === "applied" ? "good" : r.status === "planned" ? "accent" : ""}>
                        {r.status}
                      </Chip>
                      <Button onClick={() => void reopen(r.id)} disabled={busy}>
                        Open
                      </Button>
                    </div>
                  </li>
                ))}
              </ul>
            </div>
          )}
        </Panel>
      )}

      {edit && !plan && mapping && (
        <Mapper
          edit={edit}
          editable={editable}
          mapping={mapping}
          setMapping={setMapping}
          busy={busy}
          onCompare={() => void compare()}
          onCancel={reset}
        />
      )}

      {edit && plan && (
        <Diff
          edit={edit}
          plan={plan}
          busy={busy}
          onApply={() => setAsking(true)}
          onBack={() => setPlan(null)}
          onDone={reset}
          onRevert={() => void revert()}
        />
      )}

      {asking && plan && (
        <Dialog
          open
          onOpenChange={(next) => {
            if (!next && !busy) setAsking(false);
          }}
          title="Apply these changes"
          description={
            plan.creating > 0
              ? `This changes ${plan.changing} and creates ${plan.creating} on the phone system.`
              : `This changes ${plan.changing} ${
                  plan.changing === 1 ? "extension" : "extensions"
                } on the phone system.`
          }
          footer={
            <>
              <Button onClick={() => setAsking(false)} disabled={busy}>
                Cancel
              </Button>
              <Button weight="primary" onClick={() => void apply()} disabled={busy}>
                {busy ? "Applying…" : "Apply"}
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

/** Which column is which. Pre-filled from the header names. */
function Mapper({
  edit,
  editable,
  mapping,
  setMapping,
  busy,
  onCompare,
  onCancel,
}: {
  edit: BulkEdit;
  editable: BulkField[];
  mapping: BulkMapping;
  setMapping: (m: BulkMapping) => void;
  busy: boolean;
  onCompare: () => void;
  onCancel: () => void;
}) {
  const columns = edit.sheet.columns;
  const ready = mapping.extension >= 0 && Object.keys(mapping.fields).length > 0;

  const choose = (field: string, value: string) => {
    const next = { ...mapping, fields: { ...mapping.fields } };
    if (value === "") delete next.fields[field];
    else next.fields[field] = Number(value);
    setMapping(next);
  };

  return (
    <Panel className="mt-5">
      <PanelHead>
        <div>
          <h2 className="text-sm font-medium">{edit.filename}</h2>
          <p className="mt-0.5 text-xs text-ink-dim">
            {edit.sheet.rows.length} {edit.sheet.rows.length === 1 ? "row" : "rows"}. Check the
            columns, then compare.
          </p>
        </div>
      </PanelHead>

      <div className="flex flex-wrap gap-4 px-4 py-4">
        <div className="flex min-w-[200px] flex-col gap-1.5">
          <Label>Extension number</Label>
          <Picker
            value={String(mapping.extension)}
            onChange={(e) => setMapping({ ...mapping, extension: Number(e.target.value) })}
          >
            <option value="-1">Choose a column</option>
            {columns.map((c, i) => (
              <option key={i} value={i}>
                {c || `Column ${i + 1}`}
              </option>
            ))}
          </Picker>
        </div>

        {editable.map((field) => (
          <div key={field.Field} className="flex min-w-[200px] flex-col gap-1.5">
            <Label>{field.Label}</Label>
            <Picker
              value={mapping.fields[field.Field] === undefined ? "" : String(mapping.fields[field.Field])}
              onChange={(e) => choose(field.Field, e.target.value)}
            >
              <option value="">Leave alone</option>
              {columns.map((c, i) => (
                <option key={i} value={i}>
                  {c || `Column ${i + 1}`}
                </option>
              ))}
            </Picker>
          </div>
        ))}
      </div>

      <div className="border-t border-edge px-4 py-3">
        <label className="flex max-w-[70ch] cursor-pointer items-start gap-2.5">
          <input
            type="checkbox"
            className="mt-0.5 size-3.5 accent-azir"
            checked={mapping.create ?? false}
            onChange={(e) => setMapping({ ...mapping, create: e.target.checked })}
          />
          <span className="text-sm">
            Create extensions this sheet has and the phone system does not
            <span className="mt-0.5 block text-xs text-ink-faint">
              Off, a sheet for the wrong customer matches nothing and does nothing. On, it would
              create every row on it. Rows with no number get the next one after the highest.
            </span>
          </span>
        </label>
      </div>

      <div className="flex items-center justify-between border-t border-edge px-4 py-3">
        <p className="text-xs text-ink-faint">Blank cells leave a field as it is.</p>
        <div className="flex gap-2">
          <Button onClick={onCancel} disabled={busy}>
            Cancel
          </Button>
          <Button weight="primary" onClick={onCompare} disabled={!ready || busy}>
            {busy ? "Comparing…" : "Compare"}
          </Button>
        </div>
      </div>
    </Panel>
  );
}

/** The before and after. */
function Diff({
  edit,
  plan,
  busy,
  onApply,
  onBack,
  onDone,
  onRevert,
}: {
  edit: BulkEdit;
  plan: BulkPlan;
  busy: boolean;
  onApply: () => void;
  onBack: () => void;
  onDone: () => void;
  onRevert: () => void;
}) {
  const applied = edit.status === "applied";
  const outcome = new Map((edit.outcome ?? []).map((o) => [o.extension, o]));

  return (
    <Panel className="mt-5">
      <PanelHead>
        <div>
          <h2 className="text-sm font-medium">{edit.filename}</h2>
          <p className="mt-0.5 text-xs text-ink-dim">
            {plan.changing} changing
            {plan.creating > 0 && <> · {plan.creating} new</>} · {plan.unchanged} already match ·{" "}
            {plan.skipped} skipped
          </p>
        </div>
        {applied && <Chip tone="good">applied</Chip>}
      </PanelHead>

      <div className="overflow-x-auto">
        <table className="w-full border-collapse text-sm">
          <thead>
            <tr>
              <th className="w-[90px]">Ext</th>
              <th className="w-[180px]">Field</th>
              <th>Before</th>
              <th>After</th>
              <th className="w-[110px]" />
            </tr>
          </thead>
          <tbody>
            {plan.rows.map((row) => (
              <Rows key={row.extension + row.name} row={row} said={outcome.get(row.extension)} />
            ))}
          </tbody>
        </table>
      </div>

      <div className="flex items-center justify-between border-t border-edge px-4 py-3">
        <p className="max-w-[60ch] text-xs text-ink-faint">
          Only the rows that differ are changed. Rows that already match, or that name an extension
          the phone system does not have, are left alone.
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
              <Button
                weight="primary"
                onClick={onApply}
                disabled={busy || plan.changing + plan.creating === 0}
              >
                {plan.changing + plan.creating === 0
                  ? "Nothing to change"
                  : plan.creating > 0
                    ? `Change ${plan.changing}, create ${plan.creating}`
                    : `Apply ${plan.changing}`}
              </Button>
            </>
          )}
        </div>
      </div>
    </Panel>
  );
}

/** One extension, as one row per field it changes. */
function Rows({ row, said }: { row: BulkRow; said?: { ok: boolean; problem?: string } }) {
  if (row.problem) {
    return (
      <tr className="text-ink-faint">
        <td className="font-mono text-xs">{row.extension || "—"}</td>
        <td colSpan={4} className="text-xs">
          {row.problem}
        </td>
      </tr>
    );
  }

  if (row.new) {
    return (
      <tr>
        <td className="font-mono text-xs">{row.extension}</td>
        <td>
          <Chip tone="accent">new</Chip>
        </td>
        <td className="text-ink-faint italic">does not exist</td>
        <td className="font-medium">{row.wanted}</td>
        <td className="text-right text-xs">
          {said && !said.ok && <span className="text-critical">{said.problem}</span>}
          {said && said.ok && <span className="text-steady">created</span>}
        </td>
      </tr>
    );
  }

  if (!row.changes || row.changes.length === 0) {
    return (
      <tr className="text-ink-faint">
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
        <tr key={change.field}>
          <td className="font-mono text-xs">{i === 0 ? row.extension : ""}</td>
          <td className="text-ink-dim">{change.label}</td>
          <td className="text-ink-dim line-through decoration-critical/50">
            {change.before || <span className="italic">unset</span>}
          </td>
          <td className="font-medium">{change.after}</td>
          <td className="text-right text-xs">
            {said && !said.ok && <span className="text-critical">{said.problem}</span>}
            {said && said.ok && <span className="text-steady">done</span>}
          </td>
        </tr>
      ))}
    </>
  );
}
