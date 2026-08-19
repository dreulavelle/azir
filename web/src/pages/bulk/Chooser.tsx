import { useEffect, useMemo, useState } from "react";
import { api, type BulkExtension, type BulkSpec } from "../../api";
import { Dialog } from "../../components";
import { Button, Chip, Panel, PanelHead, Picker, Problem, TextInput } from "../../ui";

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
 *
 * Settings are set in a form rather than as thirty more columns in the table.
 * "Turn call recording on for these twelve" is one decision about twelve
 * extensions, not twelve decisions — but it is still compared against each of
 * them afterwards, because a form that applied itself without a before and
 * after would be the blind bulk write this whole feature exists to prevent.
 */

/** What each ticked extension should say. A field left out is left alone. */
type Draft = Map<string, Record<string, string>>;

export function Chooser({
  customerID,
  busy,
  onCompare,
  onBack,
}: {
  customerID: string;
  busy: boolean;
  onCompare: (wanted: Record<string, Record<string, string>>) => void;
  onBack: () => void;
}) {
  const [all, setAll] = useState<BulkExtension[] | null>(null);
  const [specs, setSpecs] = useState<BulkSpec[]>([]);
  const [failed, setFailed] = useState<string | null>(null);
  const [find, setFind] = useState("");
  const [draft, setDraft] = useState<Draft>(new Map());
  const [settingOptions, setSettingOptions] = useState(false);

  useEffect(() => {
    void (async () => {
      setAll(null);
      setFailed(null);
      try {
        const got = await api.extensions(customerID);
        setAll(got.extensions);
        setSpecs(got.fields);
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

  // Settings are everything except the two that have their own column in the
  // table, which are the two people came to change most of the time.
  const options = useMemo(() => specs.filter((s) => s.field !== "name" && s.field !== "enabled"), [specs]);

  /** Whether one extension's draft says anything different to what is true. */
  const moved = (extension: string, want: Record<string, string>) => {
    const was = now.get(extension);
    if (!was) return false;
    return Object.entries(want).some(
      ([field, value]) => value.trim() !== "" && value.trim() !== (was.values[field] ?? ""),
    );
  };

  const differing = useMemo(
    () => [...draft].filter(([extension, want]) => moved(extension, want)).length,
    // moved reads `now`, which changes only when the list is reloaded.
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [draft, now],
  );

  // How many settings the form has set, which is the same for every chosen
  // extension and so worth saying once.
  const optionsSet = useMemo(() => {
    const first = [...draft.values()][0];
    if (!first) return 0;
    return options.filter((s) => (first[s.field] ?? "") !== "").length;
  }, [draft, options]);

  function tick(e: BulkExtension, on: boolean) {
    setDraft((was) => {
      const next = new Map(was);
      if (!on) {
        next.delete(e.extension);
        return next;
      }
      // Seeded with what is true now, so the two fields in the table have
      // something to show, and carrying whatever the form has already set.
      const carried: Record<string, string> = {};
      const first = [...was.values()][0];
      if (first) {
        for (const spec of options) {
          if ((first[spec.field] ?? "") !== "") carried[spec.field] = first[spec.field];
        }
      }
      next.set(e.extension, { name: e.name, enabled: e.enabled ? "yes" : "no", ...carried });
      return next;
    });
  }

  function edit(extension: string, patch: Record<string, string>) {
    setDraft((was) => {
      const had = was.get(extension);
      if (!had) return was;
      const next = new Map(was);
      next.set(extension, { ...had, ...patch });
      return next;
    });
  }

  /** Writes the form's settings into every chosen extension. */
  function applyOptions(values: Record<string, string>) {
    setDraft((was) => {
      const next = new Map(was);
      for (const [extension, want] of next) {
        const merged = { ...want };
        for (const spec of options) {
          if (values[spec.field] === undefined || values[spec.field] === "") delete merged[spec.field];
          else merged[spec.field] = values[spec.field];
        }
        next.set(extension, merged);
      }
      return next;
    });
    setSettingOptions(false);
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
        <div className="min-w-[180px] flex-1">
          <TextInput
            value={find}
            placeholder="Find a number or a name"
            onChange={(e) => setFind(e.target.value)}
          />
        </div>
        {draft.size > 0 && (
          <>
            <Button onClick={() => setSettingOptions(true)} disabled={options.length === 0}>
              Set settings for all {draft.size}
              {optionsSet > 0 && <Chip tone="accent">{optionsSet}</Chip>}
            </Button>
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
                    {/* Ticked but identical is a row somebody meant to change
                        and has not yet. Marking it is the difference between
                        "I have done twelve" and "I have opened twelve". */}
                    {chosen && moved(e.extension, want) && (
                      <span
                        className="ml-1.5 inline-block size-1.5 rounded-full bg-azir align-middle"
                        title="Edited"
                      />
                    )}
                  </td>
                  <td>
                    {chosen ? (
                      <TextInput
                        value={want.name ?? ""}
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
                        value={want.enabled ?? "yes"}
                        aria-label={`Enabled for ${e.extension}`}
                        onChange={(ev) => edit(e.extension, { enabled: ev.target.value })}
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
            onClick={() => onCompare(Object.fromEntries(draft))}
          >
            {busy ? "Comparing…" : "Compare"}
          </Button>
        </div>
      </div>

      {settingOptions && (
        <Settings
          specs={options}
          chosen={draft.size}
          already={[...draft.values()][0] ?? {}}
          onApply={applyOptions}
          onClose={() => setSettingOptions(false)}
        />
      )}
    </Panel>
  );
}

/**
 * The settings form: one decision applied to every chosen extension.
 *
 * Everything starts at "leave alone", and a setting left there is not sent —
 * which is what makes a form of thirty fields safe to open when somebody came
 * to change one of them.
 */
function Settings({
  specs,
  chosen,
  already,
  onApply,
  onClose,
}: {
  specs: BulkSpec[];
  chosen: number;
  already: Record<string, string>;
  onApply: (values: Record<string, string>) => void;
  onClose: () => void;
}) {
  const [values, setValues] = useState<Record<string, string>>(() => {
    const start: Record<string, string> = {};
    for (const spec of specs) if (already[spec.field]) start[spec.field] = already[spec.field];
    return start;
  });

  const groups = useMemo(() => {
    const byGroup = new Map<string, BulkSpec[]>();
    for (const spec of specs) {
      const list = byGroup.get(spec.group) ?? [];
      list.push(spec);
      byGroup.set(spec.group, list);
    }
    return [...byGroup];
  }, [specs]);

  const setting = Object.values(values).filter((v) => v !== "").length;

  const set = (field: string, value: string) =>
    setValues((was) => {
      const next = { ...was };
      if (value === "") delete next[field];
      else next[field] = value;
      return next;
    });

  return (
    <Dialog
      open
      onOpenChange={(next) => {
        if (!next) onClose();
      }}
      title={`Settings for ${chosen} ${chosen === 1 ? "extension" : "extensions"}`}
      description="Anything left alone is not changed. You will see a before and after for each extension before anything is applied."
      footer={
        <>
          <Button onClick={onClose}>Cancel</Button>
          <Button weight="primary" onClick={() => onApply(values)}>
            {setting === 0 ? "Leave everything alone" : `Set ${setting}`}
          </Button>
        </>
      }
    >
      <div className="mt-4 flex max-h-[52vh] flex-col gap-4 overflow-y-auto pr-1">
        {groups.map(([group, fields]) => (
          <div key={group} className="flex flex-col gap-2">
            <span className="font-mono text-2xs font-medium uppercase tracking-[0.09em] text-ink-faint">
              {group}
            </span>
            {fields.map((spec) => (
              <div key={spec.field} className="flex items-center justify-between gap-3">
                <label htmlFor={`opt-${spec.field}`} className="min-w-0 flex-1 text-sm">
                  {spec.label}
                </label>
                <div className="w-[150px] shrink-0">
                  {spec.kind === "bool" || spec.kind === "choice" ? (
                    <Picker
                      id={`opt-${spec.field}`}
                      value={values[spec.field] ?? ""}
                      onChange={(e) => set(spec.field, e.target.value)}
                    >
                      <option value="">Leave alone</option>
                      {spec.kind === "bool" ? (
                        <>
                          <option value="yes">Yes</option>
                          <option value="no">No</option>
                        </>
                      ) : (
                        (spec.choices ?? []).map((choice) => (
                          <option key={choice} value={choice}>
                            {choice}
                          </option>
                        ))
                      )}
                    </Picker>
                  ) : (
                    <TextInput
                      id={`opt-${spec.field}`}
                      value={values[spec.field] ?? ""}
                      placeholder="Leave alone"
                      onChange={(e) => set(spec.field, e.target.value)}
                    />
                  )}
                </div>
              </div>
            ))}
          </div>
        ))}
      </div>
    </Dialog>
  );
}
