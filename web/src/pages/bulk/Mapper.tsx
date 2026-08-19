import type { BulkEdit, BulkField, BulkMapping } from "../../api";
import { Button, Label, Panel, PanelHead, Picker } from "../../ui";

/** Which column is which. Pre-filled from the header names. */
export function Mapper({
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
