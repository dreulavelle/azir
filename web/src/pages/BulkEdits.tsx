import { useCallback, useEffect, useState } from "react";
import {
  api,
  type Actor,
  type BulkEdit,
  type BulkField,
  type BulkMapping,
  type BulkPlan,
  type Customer,
} from "../api";
import { Dialog } from "../components";
import { useToast } from "../Toast";
import { Button, Chip, Empty, Label, Panel, PanelHead, Problem } from "../ui";
import { CustomerSearch } from "../CustomerSearch";
import { Chooser } from "./bulk/Chooser";
import { Diff } from "./bulk/Diff";
import { Mapper } from "./bulk/Mapper";
import { SheetWay } from "./bulk/SheetWay";
import { Way } from "./bulk/Way";
import { rowCount, saying, statusWord, upperFirst } from "./bulk/words";
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
  const [customer, setCustomer] = useState<Customer | null>(null);
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

  // One customer is not a choice. Somebody running Azir for a single business
  // should not be asked which one every time they open this.
  useEffect(() => {
    void (async () => {
      try {
        const first = await api.findCustomers("", 2);
        if (first.length === 1) {
          setCustomerID(first[0].id);
          setCustomer(first[0]);
        }
      } catch {
        // Not worth reporting: the search below is how a customer is chosen,
        // and it reports its own failures.
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

  async function compareChosen(wanted: Record<string, Record<string, string>>) {
    setBusy(true);
    setProblem(null);
    try {
      const got = await api.planChosen(customerID, wanted);
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
              <div className="flex min-w-[260px] flex-col gap-1.5">
                <Label>Whose phone system</Label>
                <CustomerSearch
                  value={customerID}
                  ariaLabel="Whose phone system to change"
                  placeholder="Type a customer's name"
                  onChange={(id, picked) => {
                    setCustomerID(id);
                    setCustomer(picked ?? null);
                    setWay("");
                    setProblem(null);
                  }}
                />
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
              onCompare={(wanted) => void compareChosen(wanted)}
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

