import { useEffect, useMemo, useState } from "react";
import { api, type BulkExtension } from "../../api";
import { Button, Panel, PanelHead, Picker, Problem, TextInput } from "../../ui";

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
export function Chooser({
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
