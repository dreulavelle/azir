import { useCallback, useEffect, useState } from "react";
import {
  api,
  type Actor,
  type Customer,
  type DIDImported,
  type DIDMode,
  type DIDPreview,
  type DIDTrunk,
} from "../api";
import { CustomerSearch } from "../CustomerSearch";
import { useToast } from "../Toast";
import { Button, Chip, Empty, Label, Panel, PanelHead, Problem } from "../ui";
import { saveAs } from "./bulk/words";

/**
 * Putting a carrier's list of numbers onto a trunk.
 *
 * A carrier sells a block of DIDs and sends a spreadsheet of them. Typing two
 * hundred numbers into a phone system's console is the job this replaces, and
 * the shape is the same one the sheet of extensions uses next door: the file
 * says what somebody has, the phone system says what is true now, and what
 * gets approved is the difference.
 *
 * The difference matters more here than it looks. Half of a carrier's file is
 * usually already on the trunk, and 3CX keeps a trunk's numbers as one array
 * that is written whole — so an import that did not compare first would either
 * write duplicates or, done carelessly, take every number already there off
 * the air. Comparing means the screen can say "forty of these are new" and
 * mean it.
 *
 * Two things the screen has to be honest about, because both are invisible in
 * the phone system's own console until somebody rings the number:
 *
 *   - A number on the trunk is answered. A number with an inbound rule is
 *     routed. They are different objects, and a file with no second column
 *     produces the first without the second.
 *   - A number that already routes somewhere is left exactly as it is. Nobody
 *     importing new numbers is asking to repoint a DID that has been ringing a
 *     desk for two years, so the sheet's destination is shown as declined
 *     rather than quietly applied.
 *
 * Nothing here reaches the assistant. A customer's DID list is their data.
 */
export function DIDs({ actor }: { actor: Actor }) {
  const [customerID, setCustomerID] = useState("");
  const [, setCustomer] = useState<Customer | null>(null);
  const [trunks, setTrunks] = useState<DIDTrunk[]>([]);
  const [trunk, setTrunk] = useState("");
  const [preview, setPreview] = useState<DIDPreview | null>(null);
  const [done, setDone] = useState<DIDImported | null>(null);
  const [mode, setMode] = useState<DIDMode>("append");
  const [busy, setBusy] = useState(false);
  const [problem, setProblem] = useState<string | null>(null);
  const toast = useToast();

  const mayChange = actor.permissions?.includes("phone.manage") ?? true;

  // One customer is not a choice, for the reason the sheet screen gives:
  // somebody running Azir for a single business should not be asked which one
  // every time they open this.
  useEffect(() => {
    void (async () => {
      try {
        const first = await api.findCustomers("", 2);
        if (first.length === 1) {
          setCustomerID(first[0].id);
          setCustomer(first[0]);
        }
      } catch {
        // The search below is how a customer is chosen and reports its own
        // failures.
      }
    })();
  }, []);

  const loadTrunks = useCallback(async (id: string) => {
    if (!id) {
      setTrunks([]);
      return;
    }
    setBusy(true);
    setProblem(null);
    try {
      const got = await api.didTrunks(id);
      setTrunks(got.trunks);
      // One trunk is not a choice either, and it is the common case: most
      // deployments have exactly one.
      setTrunk(got.trunks.length === 1 ? String(got.trunks[0].id) : "");
    } catch (e) {
      setTrunks([]);
      setProblem(e instanceof Error ? e.message : "Could not read the phone system's trunks");
    } finally {
      setBusy(false);
    }
  }, []);

  useEffect(() => {
    void loadTrunks(customerID);
  }, [customerID, loadTrunks]);

  const chosen = trunks.find((t) => String(t.id) === trunk);

  async function upload(file: File) {
    setBusy(true);
    setProblem(null);
    setDone(null);
    try {
      setPreview(await api.previewDIDs(customerID, trunk, file));
    } catch (e) {
      setPreview(null);
      setProblem(e instanceof Error ? e.message : "That file could not be read");
    } finally {
      setBusy(false);
    }
  }

  async function apply() {
    if (!preview) return;
    setBusy(true);
    setProblem(null);
    try {
      /*
        Every number the file named, not only the ones being added.

        A DID already on the trunk can still have no extension against it —
        importing onto a trunk somebody loaded last month and never routed adds
        nothing and assigns everything. Sending only the new ones would silently
        do half the job. The import is idempotent about numbers it already has.
      */
      const all = [...preview.adding, ...preview.already];
      const got = await api.importDIDs(customerID, trunk, all, mode);
      setDone(got);
      setPreview(null);
      toast(got.summary, { tone: "good" });
      await loadTrunks(customerID);
    } catch (e) {
      setProblem(e instanceof Error ? e.message : "That import could not be applied");
    } finally {
      setBusy(false);
    }
  }

  async function download() {
    setProblem(null);
    try {
      await api.startingDIDs(customerID, trunk, saveAs);
    } catch (e) {
      setProblem(e instanceof Error ? e.message : "Could not download that trunk's numbers");
    }
  }

  return (
    <div className="mx-auto flex max-w-[1180px] flex-col gap-4 px-6 py-6">
      <header className="flex flex-col gap-1">
        <h1 className="text-lg font-medium">Import DID numbers</h1>
        <p className="text-sm text-ink-faint">
          Put a carrier&rsquo;s list of numbers onto a trunk, and point each one at an extension.
        </p>
      </header>

      {problem && <Problem>{problem}</Problem>}

      {!mayChange && (
        <Problem>
          Importing numbers changes a customer&rsquo;s phone system, which needs the phone.manage
          permission.
        </Problem>
      )}

      <Panel>
        <PanelHead>
          <h2 className="text-sm font-medium">Which phone system</h2>
        </PanelHead>
        <div className="flex flex-wrap gap-4 px-4 py-4">
          <div className="flex min-w-[260px] flex-1 flex-col gap-1.5">
            <Label htmlFor="did-customer">Customer</Label>
            <CustomerSearch
              id="did-customer"
              value={customerID}
              onChange={(id, found) => {
                setCustomerID(id);
                setCustomer(found ?? null);
                setPreview(null);
                setDone(null);
              }}
            />
          </div>

          <div className="flex min-w-[260px] flex-1 flex-col gap-1.5">
            <Label htmlFor="did-trunk">Trunk</Label>
            <select
              id="did-trunk"
              value={trunk}
              disabled={busy || trunks.length === 0}
              className="rounded-md border border-edge bg-panel px-2.5 py-1.5 text-sm disabled:opacity-50"
              onChange={(e) => {
                setTrunk(e.target.value);
                setPreview(null);
                setDone(null);
              }}
            >
              <option value="">
                {trunks.length === 0 ? "No trunks to choose from" : "Choose a trunk"}
              </option>
              {trunks.map((t) => (
                <option key={t.id} value={String(t.id)}>
                  {trunkLabel(t)}
                </option>
              ))}
            </select>
          </div>
        </div>

        {chosen && (
          <div className="flex flex-wrap items-center gap-2 border-t border-edge px-4 py-3">
            <Chip tone={chosen.online ? "good" : "urgent"}>
              {chosen.online ? "Registered" : "Not registered"}
            </Chip>
            <Chip>{count(chosen.dids.length, "number")}</Chip>
            {/*
              The gap between these two is the thing worth seeing. A trunk with
              forty numbers and eight rules answers thirty-two of them with
              whatever its default destination is.
            */}
            <Chip tone={routedTone(chosen)}>
              {Object.keys(chosen.routes).length} routed
            </Chip>
            {chosen.host && <span className="text-xs text-ink-faint">{chosen.host}</span>}
            <Button className="ml-auto" onClick={() => void download()} disabled={busy}>
              Download what it has
            </Button>
          </div>
        )}
      </Panel>

      {trunk && !preview && !done && (
        <Panel>
          <PanelHead>
            <h2 className="text-sm font-medium">The file</h2>
          </PanelHead>
          <div className="flex flex-col gap-2 px-4 py-4 text-sm">
            <p>
              One number per line. A second column is optional and says which extension that
              number should ring.
            </p>
            <pre className="overflow-x-auto rounded-md bg-sunken px-3 py-2 text-xs">
              {SAMPLE}
            </pre>
            <ul className="list-disc pl-5 text-xs text-ink-faint">
              <li>A header row is optional — a file that is only numbers works as it is.</li>
              <li>
                The second column is just a number. A person, a queue, a ring group and a digital
                receptionist all live in one numbering plan, so the number is enough — which one
                it is gets looked up on the phone system.
              </li>
              <li>Several numbers may ring the same extension. Repeat it on each line.</li>
              <li>
                Write <code>+15551234510..529</code> for a block of consecutive numbers. A dash is
                read as part of a phone number, never as a range.
              </li>
              <li>
                Numbers are taken exactly as written. No country code is added or removed, because
                a DID has to match what the carrier actually sends.
              </li>
            </ul>
          </div>
          <div className="flex flex-wrap items-end gap-3 border-t border-edge px-4 py-4">
            <div className="flex min-w-[280px] flex-1 flex-col gap-1.5">
              <Label>Upload the list</Label>
              <input
                type="file"
                accept=".csv,.tsv,.txt,text/csv,text/plain"
                disabled={busy || !mayChange}
                className="text-sm file:mr-3 file:rounded-md file:border file:border-edge file:bg-panel file:px-2.5 file:py-1 file:text-xs file:font-medium disabled:opacity-50"
                onChange={(e) => {
                  const file = e.target.files?.[0];
                  if (file) void upload(file);
                  e.target.value = "";
                }}
              />
            </div>
          </div>
        </Panel>
      )}

      {preview && (
        <Preview
          preview={preview}
          mode={mode}
          onMode={setMode}
          busy={busy}
          mayChange={mayChange}
          onApply={() => void apply()}
          onCancel={() => setPreview(null)}
        />
      )}

      {done && <Applied done={done} onAgain={() => setDone(null)} />}

      {!customerID && (
        <Empty headline="Choose a customer">
          Their trunks are read from their phone system, so there is nothing to show until one is
          picked.
        </Empty>
      )}
    </div>
  );
}

const SAMPLE = `did,extension
+15551234500,101
+15551234501,101
+15551234502,800
+15551234510..529,900`;

/** What the before-and-after says, and the one control that applies it. */
function Preview({
  preview,
  mode,
  onMode,
  busy,
  mayChange,
  onApply,
  onCancel,
}: {
  preview: DIDPreview;
  mode: DIDMode;
  onMode: (mode: DIDMode) => void;
  busy: boolean;
  mayChange: boolean;
  onApply: () => void;
  onCancel: () => void;
}) {
  const c = preview.counts;
  // Nothing to do only when there is nothing to add *and* nothing to assign.
  // A trunk that already carries every number in the file can still have none
  // of them pointed anywhere.
  const moving = mode === "replace" ? c.kept_rules : 0;
  const nothing = c.adding === 0 && c.routing === 0 && moving === 0;

  return (
    <Panel>
      <PanelHead>
        <h2 className="text-sm font-medium">
          {preview.filename} onto {preview.trunk.name || preview.trunk.number}
        </h2>
      </PanelHead>

      <div className="flex flex-wrap gap-2 px-4 py-3">
        <Chip tone={c.adding > 0 ? "good" : undefined}>{count(c.adding, "number")} to add</Chip>
        {c.routing > 0 && <Chip tone="good">{count(c.routing, "number")} to assign</Chip>}
        {c.already > 0 && <Chip>{c.already} already on this trunk</Chip>}
        {preview.repeated > 0 && <Chip>{preview.repeated} repeated in the file</Chip>}
        {c.on_other > 0 && <Chip tone="urgent">{c.on_other} on another trunk</Chip>}
        {c.kept_rules > 0 && (
          <Chip tone={mode === "replace" ? "urgent" : "warn"}>
            {c.kept_rules} already assigned elsewhere
          </Chip>
        )}
        {c.refused > 0 && <Chip tone="urgent">{count(c.refused, "line")} unreadable</Chip>}
      </div>

      {/*
        The choice only bites on numbers that already point somewhere, so it is
        drawn where those are counted and says how many it would move.
      */}
      {c.kept_rules > 0 && (
        <fieldset className="border-t border-edge px-4 py-3">
          <legend className="sr-only">What to do with numbers that are already assigned</legend>
          <p className="text-sm font-medium">
            {count(c.kept_rules, "number")} in this file already ring a different extension
          </p>
          <div className="mt-2 flex flex-col gap-1.5">
            <label className="flex items-start gap-2 text-sm">
              <input
                type="radio"
                name="did-mode"
                className="mt-1"
                checked={mode === "append"}
                onChange={() => onMode("append")}
              />
              <span>
                Leave them where they are
                <span className="block text-xs text-ink-faint">
                  Everything else still imports. Use this when the file is a list of numbers you
                  own rather than a description of where each should ring.
                </span>
              </span>
            </label>
            <label className="flex items-start gap-2 text-sm">
              <input
                type="radio"
                name="did-mode"
                className="mt-1"
                checked={mode === "replace"}
                onChange={() => onMode("replace")}
              />
              <span>
                Move them to what the file says
                <span className="block text-xs text-ink-faint">
                  Repoints {count(c.kept_rules, "live number")} at a different extension. Calls to
                  them stop reaching whoever answers them today.
                </span>
              </span>
            </label>
          </div>
        </fieldset>
      )}

      {c.on_other > 0 && (
        <Warning
          heading="Already on another trunk"
          what="The phone system matches whichever trunk it finds first, so the same number on two trunks stops working on one of them. These will still be added — check they are meant to move."
        >
          {preview.on_other.map((d) => (
            <li key={d.number}>
              <code>{d.number}</code> is on {d.existing}
            </li>
          ))}
        </Warning>
      )}

      {c.kept_rules > 0 && (
        <Warning
          heading={
            mode === "replace" ? "These will be moved" : "These will be left as they are"
          }
          what={
            mode === "replace"
              ? "Each of these is a working number today. Check the right-hand column is where its calls should go from now on."
              : "The file names a different extension for each of these. Nothing about them changes."
          }
        >
          {preview.already_route.map((d) => (
            <li key={d.number}>
              <code>{d.number}</code> rings {d.existing}
              {mode === "replace" ? ` → ${d.extension}` : ` — the file said ${d.extension}`}
            </li>
          ))}
        </Warning>
      )}

      {c.refused > 0 && (
        <Warning
          heading="Lines that could not be read"
          what="These are skipped. Everything else in the file still imports."
        >
          {preview.refused.map((r) => (
            <li key={`${r.row}-${r.raw}`}>
              Line {r.row}: <code>{r.raw}</code> — {r.why}
            </li>
          ))}
        </Warning>
      )}

      {c.adding > 0 && (
        <div className="border-t border-edge">
          <div className="max-h-[320px] overflow-y-auto">
            <table className="w-full text-sm">
              <thead className="sticky top-0 bg-panel text-left text-xs text-ink-faint">
                <tr>
                  <th className="px-4 py-2 font-medium">Number</th>
                  <th className="px-4 py-2 font-medium">Rings</th>
                </tr>
              </thead>
              <tbody>
                {preview.adding.map((d) => (
                  <tr key={d.number} className="border-t border-edge">
                    <td className="px-4 py-1.5">
                      <code>{d.number}</code>
                    </td>
                    <td className="px-4 py-1.5">
                      {d.extension ? (
                        d.extension
                      ) : (
                        // Said plainly rather than left blank. A blank cell
                        // reads as "nothing to do" when what it means is that
                        // the number will be answered and not routed.
                        <span className="text-ink-faint">
                          nothing yet — the trunk&rsquo;s default
                        </span>
                      )}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </div>
      )}

      <div className="flex flex-wrap items-center gap-3 border-t border-edge px-4 py-3">
        <Button
          weight="primary"
          onClick={onApply}
          disabled={busy || nothing || !mayChange}
          title={
            nothing ? "This trunk already carries and answers every number in that file" : undefined
          }
        >
          {nothing ? "Nothing to do" : applyWords(c.adding, c.routing, moving)}
        </Button>
        <Button onClick={onCancel} disabled={busy}>
          Choose another file
        </Button>
        {c.adding - c.routing > 0 && (
          <span className="text-xs text-ink-faint">
            {c.adding - c.routing} will be answered but not assigned — the file gave no extension
            for them.
          </span>
        )}
      </div>
    </Panel>
  );
}

/** What the phone system actually did. */
function Applied({ done, onAgain }: { done: DIDImported; onAgain: () => void }) {
  const failed = Object.entries(done.failed ?? {});
  const moved = done.routed.filter((r) => r.was).length;
  const assigned = done.routed.length - moved;

  return (
    <Panel>
      <PanelHead>
        <h2 className="text-sm font-medium">Imported onto {done.trunk}</h2>
      </PanelHead>
      <div className="flex flex-col gap-2 px-4 py-4 text-sm">
        <p>{done.summary}</p>
        <div className="flex flex-wrap gap-2">
          <Chip tone="good">{count(done.added.length, "number")} added</Chip>
          {assigned > 0 && <Chip tone="good">{assigned} assigned</Chip>}
          {moved > 0 && <Chip tone="good">{moved} moved</Chip>}
          {done.unrouted.length > 0 && (
            <Chip tone="warn">{done.unrouted.length} not assigned</Chip>
          )}
          {failed.length > 0 && <Chip tone="urgent">{failed.length} failed</Chip>}
        </div>
      </div>

      {moved > 0 && (
        <Warning
          heading="Moved to a different extension"
          what="These numbers were ringing somewhere else until now."
        >
          {done.routed
            .filter((r) => r.was)
            .map((r) => (
              <li key={r.number}>
                <code>{r.number}</code> rang {r.was}, now rings {r.extension}
              </li>
            ))}
        </Warning>
      )}

      {failed.length > 0 && (
        <Warning
          heading="Could not be assigned"
          what="The numbers are on the trunk. Only the assignment failed, so these can be pointed somewhere from the phone system's own console, or by fixing the file and importing it again."
        >
          {failed.map(([number, why]) => (
            <li key={number}>
              <code>{number}</code> — {why}
            </li>
          ))}
        </Warning>
      )}

      <div className="border-t border-edge px-4 py-3">
        <Button onClick={onAgain}>Import another file</Button>
      </div>
    </Panel>
  );
}

/** A block of things somebody should read before agreeing. */
function Warning({
  heading,
  what,
  children,
}: {
  heading: string;
  what: string;
  children: React.ReactNode;
}) {
  return (
    <div className="border-t border-edge px-4 py-3">
      <h3 className="text-sm font-medium">{heading}</h3>
      <p className="mt-0.5 text-xs text-ink-faint">{what}</p>
      <ul className="mt-2 max-h-[180px] list-disc overflow-y-auto pl-5 text-sm">{children}</ul>
    </div>
  );
}

function trunkLabel(t: DIDTrunk): string {
  const name = t.name || t.number;
  const numbers = count(t.dids.length, "number");
  return t.name && t.name !== t.number ? `${name} (${t.number}) — ${numbers}` : `${name} — ${numbers}`;
}

// How a trunk's routing is doing. Nothing routed at all on a trunk that has
// numbers is worth saying loudly; some routed is ordinary.
function routedTone(t: DIDTrunk): "good" | "warn" | undefined {
  const routed = Object.keys(t.routes).length;
  if (t.dids.length === 0) return undefined;
  if (routed === 0) return "warn";
  return routed === t.dids.length ? "good" : undefined;
}

function count(n: number, thing: string): string {
  return `${n} ${thing}${n === 1 ? "" : "s"}`;
}

/*
What the button says it will do.

Spelled out rather than reduced to a total, because adding a number and moving
a live one are not the same act and a single figure covering both is the one
somebody would press without reading.
*/
function applyWords(adding: number, assigning: number, moving: number): string {
  const parts: string[] = [];
  if (adding > 0) parts.push(`Add ${adding}`);
  if (assigning > 0) parts.push(`assign ${assigning}`);
  if (moving > 0) parts.push(`move ${moving}`);
  if (parts.length === 0) return "Import";
  return parts.join(", ").replace(/^./, (c) => c.toUpperCase());
}
