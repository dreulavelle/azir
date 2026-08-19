import { useCallback, useEffect, useMemo, useState } from "react";
import {
  api,
  type Actor,
  type BulkEdit,
  type BulkExtension,
  type BulkField,
  type BulkMapping,
  type BulkPlan,
  type BulkRow,
  type Customer,
} from "../api";
import { Dialog, Tooltip } from "../components";
import { useToast } from "../Toast";
import {
  Button,
  Chip,
  Empty,
  Icon,
  Label,
  Panel,
  PanelHead,
  Picker,
  Problem,
  TextInput,
} from "../ui";

/**
 * Changing many extensions at once.
 *
 * Two ways in, and they meet in the middle. Ticking extensions off the phone
 * system's own list is the right shape for renaming four people; a spreadsheet
 * is the right shape for two hundred, or for creating extensions that do not
 * exist yet. Both become the same plan, get the same before-and-after, the
 * same approval, the same line in the activity log and the same undo.
 *
 * The comparison is the point. The file, or the form, says what somebody
 * wants; the phone system says what is true now; what gets approved is the
 * difference. A sheet exported last Tuesday and edited since describes a
 * system that has moved on, and applying it wholesale would quietly undo
 * whatever changed in between.
 *
 * Nothing here reaches the assistant. A customer's extension list is their
 * data, and the rule everywhere else in Azir is that it does not leave for a
 * model to read — so the columns are mapped in a dropdown rather than worked
 * out by asking one.
 */
export function BulkEdits({ actor }: { actor: Actor }) {
  const [customers, setCustomers] = useState<Customer[] | null>(null);
  const [customerID, setCustomerID] = useState("");
  const [way, setWay] = useState<"" | "choose" | "sheet">("");
  const [edit, setEdit] = useState<BulkEdit | null>(null);
  const [editable, setEditable] = useState<BulkField[]>([]);
  const [mapping, setMapping] = useState<BulkMapping | null>(null);
  const [plan, setPlan] = useState<BulkPlan | null>(null);
  // Rows unticked in the before-and-after. A plan is what was compared; this
  // is what somebody decided to do about it, which is not always all of it.
  const [skip, setSkip] = useState<Set<string>>(new Set());
  const [busy, setBusy] = useState(false);
  const [problem, setProblem] = useState<string | null>(null);
  const [asking, setAsking] = useState(false);
  const [recent, setRecent] = useState<BulkEdit[]>([]);
  const toast = useToast();

  const customer = (customers ?? []).find((c) => c.id === customerID);

  // Sheets already staged for this customer. The server holds them between the
  // upload and the decision precisely so a refresh does not lose one, and
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

  const start = useCallback(() => {
    setEdit(null);
    setMapping(null);
    setPlan(null);
    setSkip(new Set());
    setProblem(null);
    setWay("");
  }, []);

  async function reopen(id: string) {
    setBusy(true);
    setProblem(null);
    try {
      const got = await api.bulkEdit(id);
      setEdit(got.edit);
      setEditable(got.editable);
      setMapping(got.edit.mapping ?? null);
      setPlan(got.edit.plan ?? null);
      setSkip(new Set());
    } catch (e) {
      setProblem(e instanceof Error ? e.message : "Could not open that");
    } finally {
      setBusy(false);
    }
  }

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
      setSkip(new Set());
    } catch (e) {
      setProblem(e instanceof Error ? e.message : "Could not compare against the phone system");
    } finally {
      setBusy(false);
    }
  }

  async function compareChosen(rows: { extension: string; name: string; enabled: string }[]) {
    setBusy(true);
    setProblem(null);
    try {
      const got = await api.planChosen(customerID, rows);
      setEdit(got.edit);
      setPlan(got.plan);
      setSkip(new Set());
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
      setSkip(new Set());
    } catch (e) {
      setProblem(e instanceof Error ? e.message : "Could not build the undo");
    } finally {
      setBusy(false);
    }
  }

  async function discard() {
    if (!edit) return;
    setBusy(true);
    try {
      await api.cancelSheet(edit.id);
      start();
      toast("Discarded", { detail: "Nothing was changed." });
    } catch (e) {
      setProblem(e instanceof Error ? e.message : "Could not discard that");
    } finally {
      setBusy(false);
    }
  }

  async function apply() {
    if (!edit) return;
    setBusy(true);
    try {
      const got = await api.applySheet(edit.id, [...skip]);
      setAsking(false);
      setEdit(got.edit);
      const said = [
        got.changed > 0 ? `changed ${got.changed}` : "",
        got.created > 0 ? `created ${got.created}` : "",
        got.removed > 0 ? `removed ${got.removed}` : "",
        got.left > 0 ? `left ${got.left} alone` : "",
        got.failed > 0 ? `${got.failed} failed` : "",
      ].filter(Boolean);
      toast(upperFirst(said.join(", ")) || "Nothing to do", {
        tone: got.failed > 0 ? "bad" : "good",
      });
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

  // What Apply would actually do, once the unticked rows are taken out.
  const doing = (plan?.rows ?? []).filter(
    (r) => !r.problem && !skip.has(r.extension) && (r.gone || r.new || (r.changes?.length ?? 0) > 0),
  );
  const counts = {
    change: doing.filter((r) => !r.new && !r.gone).length,
    create: doing.filter((r) => r.new).length,
    remove: doing.filter((r) => r.gone).length,
  };

  return (
    <div className="mx-auto max-w-[1180px] px-6 py-6">
      <h1 className="text-2xl font-semibold tracking-tight">Bulk edits</h1>
      <p className="mt-1 max-w-[64ch] text-sm text-ink-dim">
        Change many extensions at once. You always see a before and after first, and nothing is sent
        to the assistant.
      </p>

      {problem && (
        <div className="mt-4">
          <Problem>{problem}</Problem>
        </div>
      )}

      {!edit && (
        <>
          <Panel className="mt-5">
            <div className="flex flex-wrap items-end gap-3 px-4 py-4">
              <div className="flex min-w-[240px] flex-col gap-1.5">
                <Label>Whose phone system</Label>
                <Picker
                  value={customerID}
                  onChange={(e) => {
                    setCustomerID(e.target.value);
                    setWay("");
                    setProblem(null);
                  }}
                >
                  <option value="">Choose a customer</option>
                  {(customers ?? []).map((c) => (
                    <option key={c.id} value={c.id}>
                      {c.display_name}
                    </option>
                  ))}
                </Picker>
              </div>
              {customer && (
                <p className="pb-2 text-xs text-ink-faint">
                  Everything below reads and writes {customer.display_name}'s phone system.
                </p>
              )}
            </div>
          </Panel>

          {!customerID && (
            <div className="mt-4">
              <Empty headline="Choose a customer to start" />
            </div>
          )}

          {customerID && way === "" && (
            <div className="mt-4 grid gap-4 md:grid-cols-2">
              <Way
                title="Choose extensions"
                what="Pick them off the phone system's own list and edit them here."
                best="Best for a handful of changes, or turning a few extensions on and off."
                action="Show the extensions"
                onPick={() => setWay("choose")}
              />
              <Way
                title="Use a spreadsheet"
                what="Download the current list, edit it in Excel, upload it back."
                best="Best for a lot of changes at once, or creating extensions that do not exist yet."
                action="Set up a sheet"
                onPick={() => setWay("sheet")}
              />
            </div>
          )}

          {customerID && way === "choose" && (
            <Chooser
              customerID={customerID}
              busy={busy}
              onCompare={(rows) => void compareChosen(rows)}
              onBack={() => setWay("")}
            />
          )}

          {customerID && way === "sheet" && (
            <SheetWay
              customerID={customerID}
              busy={busy}
              onUpload={(file) => void upload(file)}
              onProblem={setProblem}
              onBack={() => setWay("")}
            />
          )}

          {customerID && way === "" && recent.length > 0 && (
            <Panel className="mt-4">
              <PanelHead>
                <h2 className="text-sm font-medium">Earlier</h2>
                <span className="text-xs text-ink-faint">{recent.length}</span>
              </PanelHead>
              <ul className="divide-y divide-edge">
                {recent.map((r) => (
                  <li key={r.id} className="flex items-center justify-between gap-3 px-4 py-2.5">
                    <div className="min-w-0">
                      <span className="text-sm font-medium">{r.filename}</span>
                      <span className="ml-2 text-xs text-ink-faint">
                        {/* Sheets stored before the server stopped encoding an
                            empty list as null are still in the database. */}
                        {rowCount(r)} {rowCount(r) === 1 ? "row" : "rows"} · {r.uploaded_by}
                      </span>
                    </div>
                    <div className="flex shrink-0 items-center gap-2">
                      <Chip
                        tone={r.status === "applied" ? "good" : r.status === "planned" ? "accent" : ""}
                      >
                        {statusWord(r.status)}
                      </Chip>
                      <Button onClick={() => void reopen(r.id)} disabled={busy}>
                        Open
                      </Button>
                    </div>
                  </li>
                ))}
              </ul>
            </Panel>
          )}
        </>
      )}

      {edit && !plan && mapping && (
        <Mapper
          edit={edit}
          editable={editable}
          mapping={mapping}
          setMapping={setMapping}
          busy={busy}
          onCompare={() => void compare()}
          onCancel={start}
        />
      )}

      {edit && plan && (
        <Diff
          edit={edit}
          customer={customer?.display_name}
          plan={plan}
          skip={skip}
          setSkip={setSkip}
          counts={counts}
          busy={busy}
          onApply={() => setAsking(true)}
          onBack={() => setPlan(null)}
          onDone={start}
          onDiscard={() => void discard()}
          onRevert={() => void revert()}
        />
      )}

      {asking && plan && (
        <Dialog
          open
          onOpenChange={(next) => {
            if (!next && !busy) setAsking(false);
          }}
          title={counts.remove > 0 ? "Apply, including the removals" : "Apply these changes"}
          description={
            <>
              On <strong>{customer?.display_name ?? "this customer"}</strong>'s phone system, this{" "}
              {saying(counts)}.
              {counts.remove > 0 && (
                <span className="mt-2 block text-critical">
                  Removing an extension cannot be undone from here.
                </span>
              )}
            </>
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

/** One of the two ways in. */
function Way({
  title,
  what,
  best,
  action,
  onPick,
}: {
  title: string;
  what: string;
  best: string;
  action: string;
  onPick: () => void;
}) {
  return (
    <Panel className="flex flex-col">
      <PanelHead>
        <h2 className="text-sm font-medium">{title}</h2>
      </PanelHead>
      <div className="flex flex-1 flex-col gap-1.5 px-4 py-4">
        <p className="text-sm">{what}</p>
        <p className="text-xs text-ink-faint">{best}</p>
      </div>
      <div className="border-t border-edge px-4 py-3">
        <Button weight="primary" onClick={onPick}>
          {action}
        </Button>
      </div>
    </Panel>
  );
}

/**
 * Picking extensions off the phone system's own list.
 *
 * The list is what the comparison will read, so an extension cannot be
 * mistyped, cannot fail to exist, and cannot belong to a different customer —
 * three of the four ways this could go wrong, gone before it has started.
 *
 * Only ticked rows are editable. Everything on the screen would otherwise be a
 * field somebody could change by leaning on a keyboard, and the tick is the
 * difference between looking at an extension and meaning to change it.
 */
function Chooser({
  customerID,
  busy,
  onCompare,
  onBack,
}: {
  customerID: string;
  busy: boolean;
  onCompare: (rows: { extension: string; name: string; enabled: string }[]) => void;
  onBack: () => void;
}) {
  const [all, setAll] = useState<BulkExtension[] | null>(null);
  const [failed, setFailed] = useState<string | null>(null);
  const [find, setFind] = useState("");
  const [draft, setDraft] = useState<Map<string, { name: string; enabled: boolean }>>(new Map());

  useEffect(() => {
    void (async () => {
      setAll(null);
      setFailed(null);
      try {
        setAll((await api.extensions(customerID)).extensions);
      } catch (e) {
        setFailed(e instanceof Error ? e.message : "Could not read the phone system");
      }
    })();
  }, [customerID]);

  const shown = useMemo(() => {
    const needle = find.trim().toLowerCase();
    if (!needle) return all ?? [];
    return (all ?? []).filter(
      (e) => e.extension.includes(needle) || e.name.toLowerCase().includes(needle),
    );
  }, [all, find]);

  const now = useMemo(() => new Map((all ?? []).map((e) => [e.extension, e])), [all]);

  // How many chosen rows actually say something different. A screen that lets
  // somebody tick twelve extensions and change none of them should say so
  // before they press Compare, not after.
  const differing = useMemo(() => {
    let n = 0;
    for (const [extension, want] of draft) {
      const was = now.get(extension);
      if (!was) continue;
      if (want.name.trim() !== was.name || want.enabled !== was.enabled) n++;
    }
    return n;
  }, [draft, now]);

  function tick(e: BulkExtension, on: boolean) {
    setDraft((was) => {
      const next = new Map(was);
      if (on) next.set(e.extension, { name: e.name, enabled: e.enabled });
      else next.delete(e.extension);
      return next;
    });
  }

  function edit(extension: string, patch: Partial<{ name: string; enabled: boolean }>) {
    setDraft((was) => {
      const had = was.get(extension);
      if (!had) return was;
      const next = new Map(was);
      next.set(extension, { ...had, ...patch });
      return next;
    });
  }

  /** Turns every chosen extension on, or every one off, in one go. */
  function setAllEnabled(enabled: boolean) {
    setDraft((was) => {
      const next = new Map(was);
      for (const [extension, want] of next) next.set(extension, { ...want, enabled });
      return next;
    });
  }

  if (failed) {
    return (
      <Panel className="mt-4">
        <div className="p-4">
          <Problem>{failed}</Problem>
          <div className="mt-3">
            <Button onClick={onBack}>Back</Button>
          </div>
        </div>
      </Panel>
    );
  }

  if (!all) return <div className="mt-4 h-64 animate-pulse rounded-lg bg-sunken" />;

  return (
    <Panel className="mt-4">
      <PanelHead>
        <div>
          <h2 className="text-sm font-medium">Choose extensions</h2>
          <p className="mt-0.5 text-xs text-ink-dim">
            Tick the ones to change, then edit them. {all.length} on this phone system.
          </p>
        </div>
        {draft.size > 0 && (
          <span className="text-xs text-ink-faint">
            {draft.size} chosen · {differing} {differing === 1 ? "differs" : "differ"}
          </span>
        )}
      </PanelHead>

      <div className="flex flex-wrap items-center gap-2 border-b border-edge px-4 py-3">
        <div className="min-w-[200px] flex-1">
          <TextInput
            value={find}
            placeholder="Find a number or a name"
            onChange={(e) => setFind(e.target.value)}
          />
        </div>
        {draft.size > 0 && (
          <>
            <span className="text-xs text-ink-faint">Set all chosen to</span>
            <Button onClick={() => setAllEnabled(true)}>Enabled</Button>
            <Button onClick={() => setAllEnabled(false)}>Disabled</Button>
            <button
              className="rounded-md px-2 py-1 text-xs text-ink-dim transition-colors hover:bg-sunken hover:text-ink"
              onClick={() => setDraft(new Map())}
            >
              Clear
            </button>
          </>
        )}
      </div>

      <div className="max-h-[26rem] overflow-auto">
        <table className="w-full border-collapse text-sm">
          <thead className="sticky top-0 z-10 bg-panel">
            <tr>
              <th className="w-[44px]" />
              <th className="w-[90px]">Ext</th>
              <th>Display name</th>
              <th className="w-[130px]">Enabled</th>
            </tr>
          </thead>
          <tbody>
            {shown.map((e) => {
              const want = draft.get(e.extension);
              const chosen = want !== undefined;
              // Ticked but identical is a row somebody meant to change and has
              // not yet. Marking it is the difference between "I have done
              // twelve" and "I have opened twelve".
              const moved =
                chosen && (want.name.trim() !== e.name || want.enabled !== e.enabled);
              return (
                <tr key={e.extension} className={chosen ? "" : "text-ink-dim"}>
                  <td>
                    <input
                      type="checkbox"
                      className="size-3.5 cursor-pointer align-middle accent-azir"
                      checked={chosen}
                      aria-label={`Change extension ${e.extension}`}
                      onChange={(ev) => tick(e, ev.target.checked)}
                    />
                  </td>
                  <td className="font-mono text-xs">
                    {e.extension}
                    {moved && (
                      <span
                        className="ml-1.5 inline-block size-1.5 rounded-full bg-azir align-middle"
                        title="Edited"
                      />
                    )}
                  </td>
                  <td>
                    {chosen ? (
                      <TextInput
                        value={want.name}
                        aria-label={`Display name for ${e.extension}`}
                        onChange={(ev) => edit(e.extension, { name: ev.target.value })}
                      />
                    ) : (
                      <span>{e.name || <span className="italic text-ink-faint">unset</span>}</span>
                    )}
                  </td>
                  <td>
                    {chosen ? (
                      <Picker
                        value={want.enabled ? "yes" : "no"}
                        aria-label={`Enabled for ${e.extension}`}
                        onChange={(ev) => edit(e.extension, { enabled: ev.target.value === "yes" })}
                      >
                        <option value="yes">Enabled</option>
                        <option value="no">Disabled</option>
                      </Picker>
                    ) : (
                      <span className="text-xs">{e.enabled ? "yes" : "no"}</span>
                    )}
                  </td>
                </tr>
              );
            })}
            {shown.length === 0 && (
              <tr>
                <td colSpan={4} className="py-6 text-center text-sm text-ink-faint">
                  Nothing matches "{find}".
                </td>
              </tr>
            )}
          </tbody>
        </table>
      </div>

      <div className="flex items-center justify-between gap-3 border-t border-edge px-4 py-3">
        <p className="text-xs text-ink-faint">
          {draft.size === 0
            ? "Nothing chosen yet."
            : differing === 0
              ? "Nothing chosen has been changed yet."
              : `${differing} of the ${draft.size} chosen would change.`}
        </p>
        <div className="flex gap-2">
          <Button onClick={onBack} disabled={busy}>
            Back
          </Button>
          <Button
            weight="primary"
            disabled={busy || differing === 0}
            onClick={() =>
              onCompare(
                [...draft].map(([extension, want]) => ({
                  extension,
                  name: want.name,
                  enabled: want.enabled ? "yes" : "no",
                })),
              )
            }
          >
            {busy ? "Comparing…" : "Compare"}
          </Button>
        </div>
      </div>
    </Panel>
  );
}

/**
 * The spreadsheet way in, starting with the sheet.
 *
 * The old version of this screen was a file input and a sentence, which left
 * somebody to guess how many columns it wanted and what to call them. Handing
 * them the file answers that by construction: the headers are the ones Azir
 * recognises, the rows are what is true today, and the extension numbers are
 * right because they came from the phone system.
 */
function SheetWay({
  customerID,
  busy,
  onUpload,
  onProblem,
  onBack,
}: {
  customerID: string;
  busy: boolean;
  onUpload: (file: File) => void;
  onProblem: (message: string) => void;
  onBack: () => void;
}) {
  const [getting, setGetting] = useState(false);
  const [columns, setColumns] = useState<string[]>([]);
  const [sample, setSample] = useState<BulkExtension[]>([]);

  // The column names, and an example built from this customer's own
  // extensions. A made-up example invites the question of whether the real one
  // looks the same.
  useEffect(() => {
    void (async () => {
      try {
        const got = await api.extensions(customerID);
        setColumns(got.columns);
        setSample(got.extensions.slice(0, 2));
      } catch {
        // The spec below still reads fine without an example, and the download
        // button reports this properly when it is pressed.
      }
    })();
  }, [customerID]);

  async function download() {
    setGetting(true);
    try {
      await api.startingSheet(customerID, saveAs);
    } catch (e) {
      onProblem(e instanceof Error ? e.message : "Could not build that sheet");
    } finally {
      setGetting(false);
    }
  }

  const header = columns.length > 0 ? columns : ["Extension", "Display name", "Enabled"];

  return (
    <Panel className="mt-4">
      <PanelHead>
        <h2 className="text-sm font-medium">Use a spreadsheet</h2>
      </PanelHead>

      <div className="border-b border-edge px-4 py-4">
        <div className="flex flex-wrap items-center gap-3">
          <Button weight="primary" onClick={() => void download()} disabled={getting}>
            <Icon.download />
            {getting ? "Building…" : "Download this customer's extensions"}
          </Button>
          <p className="text-xs text-ink-faint">
            Edit it in Excel and upload it back. The columns will map themselves.
          </p>
        </div>
      </div>

      <div className="border-b border-edge px-4 py-4">
        <Label className="mb-2 block">What the file has to look like</Label>
        <div className="overflow-x-auto rounded-md border border-edge bg-sunken">
          <pre className="px-3 py-2.5 font-mono text-xs leading-relaxed">
            <span className="font-semibold">{header.join(",")}</span>
            {"\n"}
            {sample.length > 0
              ? sample
                  .map((e) => [e.extension, e.name, e.enabled ? "yes" : "no"].join(","))
                  .join("\n")
              : "100,Reception,yes\n101,Sales,no"}
          </pre>
        </div>
        <ul className="mt-3 flex max-w-[74ch] flex-col gap-1 text-xs text-ink-dim">
          <li>
            <strong>{header.length} columns</strong>, named exactly as above, in a header row.
            Anything else in the file is ignored.
          </li>
          <li>
            <strong>{header[0]}</strong> is how a row is matched. It has to be a number the phone
            system already has, unless you tick "create" on the next screen.
          </li>
          <li>
            <strong>An empty cell leaves that field alone.</strong> It does not blank it.
          </li>
          <li>
            <strong>Enabled</strong> takes yes or no. True/false, 1/0 and on/off also work.
          </li>
          <li>Commas, semicolons or tabs — whatever your spreadsheet exported.</li>
        </ul>
      </div>

      <div className="flex flex-wrap items-end justify-between gap-3 px-4 py-4">
        <div className="flex min-w-[280px] flex-1 flex-col gap-1.5">
          <Label>Upload the edited sheet</Label>
          <input
            type="file"
            accept=".csv,.tsv,.txt,text/csv,text/plain"
            disabled={busy}
            className="text-sm file:mr-3 file:rounded-md file:border file:border-edge file:bg-panel file:px-2.5 file:py-1 file:text-xs file:font-medium disabled:opacity-50"
            onChange={(e) => {
              const file = e.target.files?.[0];
              if (file) onUpload(file);
              e.target.value = "";
            }}
          />
        </div>
        <Button onClick={onBack} disabled={busy}>
          Back
        </Button>
      </div>
    </Panel>
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
  const rows = edit.sheet?.rows ?? [];
  const preview = rows.slice(0, 5);

  const choose = (field: string, value: string) => {
    const next = { ...mapping, fields: { ...mapping.fields } };
    if (value === "") delete next.fields[field];
    else next.fields[field] = Number(value);
    setMapping(next);
  };

  // Which columns are spoken for, so the preview can show what each one is
  // being read as rather than leaving somebody to count across.
  const readAs = new Map<number, string>();
  if (mapping.extension >= 0) readAs.set(mapping.extension, "Extension");
  for (const field of editable) {
    const col = mapping.fields[field.Field];
    if (col !== undefined) readAs.set(col, field.Label);
  }

  return (
    <Panel className="mt-5">
      <PanelHead>
        <div>
          <h2 className="text-sm font-medium">{edit.filename}</h2>
          <p className="mt-0.5 text-xs text-ink-dim">
            {rows.length} {rows.length === 1 ? "row" : "rows"}. Check the
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

      {/* The first rows as Azir read them. A column shifted by one is obvious
          here and invisible in a dropdown. */}
      {preview.length > 0 && (
        <div className="border-t border-edge px-4 py-4">
          <Label className="mb-2 block">The first rows, as Azir read them</Label>
          <div className="overflow-x-auto rounded-md border border-edge">
            <table className="w-full border-collapse text-xs">
              <thead>
                <tr>
                  {columns.map((c, i) => (
                    <th key={i} className="whitespace-nowrap">
                      <span className="block">{c || `Column ${i + 1}`}</span>
                      <span
                        className={
                          readAs.has(i)
                            ? "block font-normal normal-case tracking-normal text-azir"
                            : "block font-normal normal-case tracking-normal text-ink-faint"
                        }
                      >
                        {readAs.get(i) ?? "not used"}
                      </span>
                    </th>
                  ))}
                </tr>
              </thead>
              <tbody>
                {preview.map((row, i) => (
                  <tr key={i}>
                    {columns.map((_, col) => (
                      <td key={col} className={readAs.has(col) ? "" : "text-ink-faint"}>
                        {row[col] ?? ""}
                      </td>
                    ))}
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
          {rows.length > preview.length && (
            <p className="mt-2 text-xs text-ink-faint">and {rows.length - preview.length} more.</p>
          )}
        </div>
      )}

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

/**
 * The before and after.
 *
 * Every row that would do something can be unticked. One approval for the
 * batch, as agreed — but a batch is not all-or-nothing, and the row somebody
 * wants to drop is usually the one a colleague touched since the plan was
 * made. That row is on screen with its current value; unticking it is how you
 * decline it without abandoning the other thirty-nine.
 */
function Diff({
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
  counts: { change: number; create: number; remove: number };
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

/**
 * "Change 3, remove 20" — what the button does, said as an instruction.
 *
 * Separate from saying() below, which is the same facts in the third person
 * for the dialog. The button used to borrow that one and read "Changes 1",
 * which scans as a count of changes rather than as a thing it will do.
 */
function doing(counts: { change: number; create: number; remove: number }): string {
  return [
    counts.change > 0 ? `Change ${counts.change}` : "",
    counts.create > 0 ? `Create ${counts.create}` : "",
    counts.remove > 0 ? `Remove ${counts.remove}` : "",
  ]
    .filter(Boolean)
    .join(", ");
}

/** "changes 3 and removes 20", for the dialog. */
function saying(counts: { change: number; create: number; remove: number }): string {
  const parts = [
    counts.change > 0 ? `changes ${counts.change}` : "",
    counts.create > 0 ? `creates ${counts.create}` : "",
    counts.remove > 0 ? `removes ${counts.remove}` : "",
  ].filter(Boolean);
  if (parts.length === 0) return "changes nothing";
  if (parts.length === 1) return parts[0];
  return `${parts.slice(0, -1).join(", ")} and ${parts[parts.length - 1]}`;
}

function upperFirst(text: string): string {
  return text.charAt(0).toUpperCase() + text.slice(1);
}

/** How a staged sheet reads in a list. "planned" is not a word for this. */
function statusWord(status: BulkEdit["status"]): string {
  switch (status) {
    case "draft":
      return "not compared";
    case "planned":
      return "waiting on you";
    case "applied":
      return "applied";
    case "cancelled":
      return "discarded";
  }
}

/** How many rows a stored sheet has, tolerating the ones written as null. */
function rowCount(edit: BulkEdit): number {
  return edit.sheet?.rows?.length ?? 0;
}

/** Hands a generated file to the browser. */
function saveAs(blob: Blob, name: string) {
  const url = URL.createObjectURL(blob);
  const link = document.createElement("a");
  link.href = url;
  link.download = name;
  document.body.appendChild(link);
  link.click();
  link.remove();
  URL.revokeObjectURL(url);
}
