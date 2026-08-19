import { useEffect, useMemo, useState } from "react";
import { api, type BulkExtension, type BulkSpec } from "../../api";
import { Button, Icon, Label, Panel, PanelHead } from "../../ui";
import { saveAs } from "./words";

/**
 * The spreadsheet way in, starting with the sheet.
 *
 * The old version of this screen was a file input and a sentence, which left
 * somebody to guess how many columns it wanted and what to call them. Handing
 * them the file answers that by construction: the headers are the ones Azir
 * recognises, the rows are what is true today, and the extension numbers are
 * right because they came from the phone system.
 *
 * Which columns is a question rather than an assumption. A phone system with
 * thirty-two settings makes a thirty-three column sheet, which is correct and
 * unusable — so the common few are ticked to start with and the rest are one
 * click away, grouped the way the phone system groups them.
 */

/** What a sheet starts out carrying: what somebody usually came to change. */
const COMMON = ["name", "enabled"];

export function SheetWay({
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
  const [specs, setSpecs] = useState<BulkSpec[]>([]);
  const [sample, setSample] = useState<BulkExtension[]>([]);
  const [chosen, setChosen] = useState<Set<string>>(new Set(COMMON));
  const [showAll, setShowAll] = useState(false);

  // The fields this phone system will write, and an example built from this
  // customer's own extensions. A made-up example invites the question of
  // whether the real one looks the same.
  useEffect(() => {
    void (async () => {
      try {
        const got = await api.extensions(customerID);
        setSpecs(got.fields);
        setSample(got.extensions.slice(0, 2));
      } catch {
        // The spec below still reads without an example, and the download
        // button reports this properly when it is pressed.
      }
    })();
  }, [customerID]);

  const groups = useMemo(() => {
    const byGroup = new Map<string, BulkSpec[]>();
    for (const spec of specs) {
      const list = byGroup.get(spec.group) ?? [];
      list.push(spec);
      byGroup.set(spec.group, list);
    }
    return [...byGroup];
  }, [specs]);

  const columns = useMemo(
    () => ["Extension", ...specs.filter((s) => chosen.has(s.field)).map((s) => s.label)],
    [specs, chosen],
  );

  function tick(field: string, on: boolean) {
    setChosen((was) => {
      const next = new Set(was);
      if (on) next.add(field);
      else next.delete(field);
      return next;
    });
  }

  async function download() {
    setGetting(true);
    try {
      await api.startingSheet(customerID, [...chosen], saveAs);
    } catch (e) {
      onProblem(e instanceof Error ? e.message : "Could not build that sheet");
    } finally {
      setGetting(false);
    }
  }

  // The example row, in the columns actually chosen.
  const exampleRow = (e: BulkExtension) =>
    [e.extension, ...specs.filter((s) => chosen.has(s.field)).map((s) => e.values[s.field] ?? "")]
      .join(",");

  return (
    <Panel className="mt-4">
      <PanelHead>
        <h2 className="text-sm font-medium">Use a spreadsheet</h2>
        {specs.length > 0 && (
          <span className="text-xs text-ink-faint">
            {chosen.size} of {specs.length} columns
          </span>
        )}
      </PanelHead>

      <div className="border-b border-edge px-4 py-4">
        <Label className="mb-2 block">Columns to include</Label>
        <p className="mb-3 max-w-[74ch] text-xs text-ink-faint">
          Every column you take is one you can edit. Leave out the ones you are not changing —
          a blank cell leaves a setting alone, but a narrower sheet is easier to read.
        </p>

        {specs.length === 0 ? (
          <p className="text-xs text-ink-faint">
            Reading what this phone system will let you change…
          </p>
        ) : (
          <div className="flex flex-col gap-3">
            {groups.slice(0, showAll ? groups.length : 1).map(([group, fields]) => (
              <div key={group}>
                <span className="font-mono text-2xs font-medium uppercase tracking-[0.09em] text-ink-faint">
                  {group}
                </span>
                <div className="mt-1.5 flex flex-wrap gap-x-4 gap-y-1.5">
                  {fields.map((spec) => (
                    <label key={spec.field} className="flex cursor-pointer items-center gap-1.5 text-sm">
                      <input
                        type="checkbox"
                        className="size-3.5 accent-azir"
                        checked={chosen.has(spec.field)}
                        onChange={(e) => tick(spec.field, e.target.checked)}
                      />
                      {spec.label}
                    </label>
                  ))}
                </div>
              </div>
            ))}

            <div className="flex flex-wrap items-center gap-2">
              <button
                className="rounded-md px-2 py-1 text-xs text-ink-dim transition-colors hover:bg-sunken hover:text-ink"
                onClick={() => setShowAll((was) => !was)}
              >
                {showAll
                  ? "Show fewer"
                  : `Show all ${specs.length} settings this phone system will change`}
              </button>
              {showAll && (
                <>
                  <button
                    className="rounded-md px-2 py-1 text-xs text-ink-dim transition-colors hover:bg-sunken hover:text-ink"
                    onClick={() => setChosen(new Set(specs.map((s) => s.field)))}
                  >
                    Take everything
                  </button>
                  <button
                    className="rounded-md px-2 py-1 text-xs text-ink-dim transition-colors hover:bg-sunken hover:text-ink"
                    onClick={() => setChosen(new Set(COMMON))}
                  >
                    Back to the usual few
                  </button>
                </>
              )}
            </div>
          </div>
        )}
      </div>

      <div className="border-b border-edge px-4 py-4">
        <div className="flex flex-wrap items-center gap-3">
          <Button
            weight="primary"
            onClick={() => void download()}
            disabled={getting || chosen.size === 0}
          >
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
            <span className="font-semibold">{columns.join(",")}</span>
            {"\n"}
            {sample.length > 0 ? sample.map(exampleRow).join("\n") : "100,Reception,yes"}
          </pre>
        </div>
        <ul className="mt-3 flex max-w-[74ch] flex-col gap-1 text-xs text-ink-dim">
          <li>
            <strong>{columns.length} columns</strong>, named exactly as above, in a header row.
            Anything else in the file is ignored.
          </li>
          <li>
            <strong>Extension</strong> is how a row is matched. It has to be a number the phone
            system already has, unless you tick "create" on the next screen.
          </li>
          <li>
            <strong>An empty cell leaves that field alone.</strong> It does not blank it.
          </li>
          <li>
            <strong>Yes/no columns</strong> take yes or no. True/false, 1/0 and on/off also work.
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
