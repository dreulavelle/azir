import { useEffect, useState } from "react";
import { api, type BulkExtension } from "../../api";
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
 */
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
