import { useMemo, useState } from "react";
import type { BlfKey, BlfKind, BulkExtension, BulkSpec } from "../../api";
import { Sheet, Tabs } from "../../components";
import { Button, Chip, Problem, TextInput } from "../../ui";
import { Field, LEAVE, MIXED } from "./Fields";
import { Keys } from "./Keys";

/**
 * The extension editor: one form, three jobs.
 *
 * Editing one extension, editing several together, and making a new one are
 * the same fields with the same rules, so they are the same form. Three
 * screens would drift — a field added to one, worded differently in another —
 * and the drift would be invisible until somebody set the wrong thing.
 *
 * The tabs are 3CX's own, in 3CX's order, because a technician arrives already
 * knowing where things live. Making them find Voicemail somewhere new is a
 * cost with nothing on the other side of it.
 *
 * Nothing is sent that was not touched. The form starts from what is true and
 * submits the difference, so a field somebody scrolled past is not a field
 * they set.
 */

/** The tabs, in the order the phone system puts them. */
const TABS = [
  { id: "General", label: "General" },
  { id: "Call forwarding", label: "Call forwarding" },
  { id: "IP phone", label: "IP phone" },
  { id: "BLF", label: "BLF" },
  { id: "Voicemail", label: "Voicemail" },
  { id: "Options", label: "Options" },
  { id: "Apps and sign-in", label: "Apps" },
];

/**
 * What a new extension gets before anybody touches it.
 *
 * Both of these ship the wrong way round for a hosted deployment: 3CX blocks
 * remote non-tunnel connections and leaves audio to the endpoints, and the
 * first thing anybody does to a new extension is turn both around. Doing it
 * here means it is right by default and visible in the form rather than
 * remembered.
 */
const NEW_DEFAULTS: Record<string, string> = {
  PbxDeliversAudio: "yes",
  BlockTunnel: "no",
  // Azir's own field for this, not the phone system's "Enabled" — the two
  // describe one switch and Azir's is the one that survives. Naming the wrong
  // one made the phone system refuse the whole set of defaults, so a new
  // extension came out with none of them.
  enabled: "yes",
  VMEnabled: "yes",
};

export type Mode =
  | { kind: "one"; extension: BulkExtension }
  | { kind: "together"; extensions: BulkExtension[] }
  | { kind: "new" };

export function Editor({
  mode,
  specs,
  kinds,
  customer,
  busy,
  problem,
  nextNumber,
  onSave,
  onSaveKeys,
  onClose,
}: {
  mode: Mode;
  specs: BulkSpec[];
  kinds: BlfKind[];
  customer: string;
  busy: boolean;
  problem: string | null;
  /** The number a new extension would get. */
  nextNumber: string;
  onSave: (draft: Record<string, string>, number: string) => void;
  /** The buttons, saved on their own: the phone system writes them whole. */
  onSaveKeys: (layout: { keys?: BlfKey[]; from?: string }) => void;
  onClose: () => void;
}) {
  const together = mode.kind === "together";
  const making = mode.kind === "new";

  // What each field says now. For several at once, a field they do not agree
  // on reads as mixed rather than as whichever one happened to be first.
  const now = useMemo(() => {
    if (mode.kind === "one") return mode.extension.values;
    if (mode.kind === "new") return NEW_DEFAULTS;
    const [first, ...rest] = mode.extensions;
    const shared: Record<string, string> = {};
    for (const [field, value] of Object.entries(first?.values ?? {})) {
      shared[field] = rest.every((e) => e.values[field] === value) ? value : MIXED;
    }
    return shared;
  }, [mode]);

  /*
   * What is being set, as against what is merely on screen.
   *
   * Empty for an extension that exists, so what goes out is the difference and
   * a field somebody scrolled past is not a field they set.
   *
   * Seeded for a new one, because the defaults are the point. They were shown
   * as the starting values and never sent — a new extension came out with the
   * phone system's own defaults, which are the two this is here to turn round.
   */
  const [draft, setDraft] = useState<Record<string, string>>(() => {
    if (!making) return {};
    // Only defaults this phone system actually has. One that names a field it
    // does not know is refused, and refused with the rest of them.
    const known = new Set(specs.map((s) => s.field));
    return Object.fromEntries(
      Object.entries(NEW_DEFAULTS).filter(([field]) => known.has(field)),
    );
  });
  const [number, setNumber] = useState(nextNumber);
  const [tab, setTab] = useState("General");

  // The buttons are their own thing, kept and saved apart from the fields.
  // The phone system holds a layout as one value and writes it whole, so it
  // does not belong in a bag of field changes that go out one at a time.
  const startingKeys = mode.kind === "one" ? (mode.extension.keys ?? []) : [];
  const [keys, setKeys] = useState<BlfKey[]>(startingKeys);
  const [copyFrom, setCopyFrom] = useState("");
  const keysMoved =
    copyFrom.trim() !== "" || JSON.stringify(keys) !== JSON.stringify(startingKeys);

  const valueOf = (spec: BulkSpec) => {
    if (draft[spec.field] !== undefined) return draft[spec.field];
    if (together) return now[spec.field] === MIXED ? MIXED : LEAVE;
    return now[spec.field] ?? "";
  };

  const set = (field: string, next: string) =>
    setDraft((was) => ({ ...was, [field]: next }));

  /*
   * The fields that would actually go out.
   *
   * Three modes and three answers, spelled out rather than folded into one
   * condition — the folded version was wrong about new extensions and unclear
   * about the rest.
   */
  const setting = Object.entries(draft).filter(([field, value]) => {
    // Never: these are the absence of an answer, not an answer.
    if (value === MIXED || value === LEAVE) return false;
    // Editing several, or making one: anything explicitly set is set.
    if (together || making) return true;
    // Editing one: only what differs from what it says now.
    return value !== (now[field] ?? "");
  });

  const byTab = useMemo(() => {
    const groups = new Map<string, BulkSpec[]>();
    // A field the server marked as belonging in a spreadsheet rather than a
    // form — the display name, where the phone system also offers its parts.
    for (const spec of specs) {
      if (spec.sheet_only) continue;
      const list = groups.get(spec.group) ?? [];
      list.push(spec);
      groups.set(spec.group, list);
    }
    return groups;
  }, [specs]);

  // How many changes sit on each tab, so a tab somebody edited and scrolled
  // away from is not lost behind another one.
  const counts = useMemo(() => {
    const per: Record<string, number> = {};
    for (const [field] of setting) {
      const group = specs.find((s) => s.field === field)?.group;
      if (group) per[group] = (per[group] ?? 0) + 1;
    }
    return per;
  }, [setting, specs]);

  const shown = byTab.get(tab) ?? [];
  const title = making
    ? "New extension"
    : together
      ? `${(mode as { extensions: BulkExtension[] }).extensions.length} extensions`
      : `Extension ${(mode as { extension: BulkExtension }).extension.extension}`;

  const subtitle = making
    ? `On ${customer}. It will be created as ${number || "—"}.`
    : together
      ? `On ${customer}. Anything left alone stays as it is.`
      : `${(mode as { extension: BulkExtension }).extension.name || "unnamed"} · ${customer}`;

  return (
    <Sheet
      open
      wide
      onOpenChange={(next) => {
        if (!next && !busy) onClose();
      }}
      title={title}
      description={subtitle}
      footer={
        <>
          <span className="text-xs text-ink-faint">
            {setting.length === 0
              ? making
                ? "Give it a name to continue."
                : "Nothing changed yet."
              : `${setting.length} ${setting.length === 1 ? "change" : "changes"}`}
          </span>
          <div className="flex gap-2">
            <Button onClick={onClose} disabled={busy}>
              Cancel
            </Button>
            <Button
              weight="primary"
              disabled={busy || (making ? !draft.FirstName && !draft.LastName : setting.length === 0)}
              onClick={() => onSave(Object.fromEntries(setting), number)}
            >
              {busy
                ? "Saving…"
                : making
                  ? "Create"
                  : setting.length === 0
                    ? "Save"
                    : together
                      ? `Review ${setting.length}`
                      : `Save ${setting.length}`}
            </Button>
          </div>
        </>
      }
    >
      {problem && (
        <div className="mb-4">
          <Problem>{problem}</Problem>
        </div>
      )}

      {making && (
        <div className="mb-4 flex items-start justify-between gap-4 rounded-lg border border-edge bg-sunken/60 px-3 py-3">
          <label htmlFor="new-number" className="pt-1.5 text-sm">
            Extension number
            <span className="mt-0.5 block text-2xs text-ink-faint">
              The next one free after the highest. Numbers of deleted extensions are not reused —
              their old routing can still point at them.
            </span>
          </label>
          <div className="w-[190px] shrink-0">
            <TextInput
              id="new-number"
              value={number}
              onChange={(e) => setNumber(e.target.value)}
            />
          </div>
        </div>
      )}

      <Tabs
        value={tab}
        onChange={setTab}
        tabs={TABS.filter((t) => byTab.has(t.id) || t.id === "IP phone" || (t.id === "BLF" && !making)).map((t) => ({
          ...t,
          count: counts[t.id],
        }))}
      />

      {tab === "BLF" && !making ? (
        <div className="flex flex-col gap-4">
          <Keys
            keys={keys}
            kinds={kinds}
            onChange={setKeys}
            copyFrom={copyFrom}
            onCopyFrom={setCopyFrom}
            together={together}
            disabled={busy}
          />
          {keysMoved && (
            <div className="flex items-center justify-between gap-3 rounded-lg border border-edge bg-sunken/60 px-3 py-2.5">
              <span className="text-xs text-ink-dim">
                {copyFrom.trim()
                  ? `Every button replaced with extension ${copyFrom.trim()}'s.`
                  : "The buttons have changed."}
              </span>
              <Button
                weight="primary"
                disabled={busy}
                onClick={() =>
                  onSaveKeys(copyFrom.trim() ? { from: copyFrom.trim() } : { keys })
                }
              >
                {busy ? "Saving…" : "Save the buttons"}
              </Button>
            </div>
          )}
        </div>
      ) : shown.length > 0 ? (
        <div className="divide-y divide-edge/60">
          {shown.map((spec) => (
            <Field
              key={spec.field}
              spec={spec}
              value={valueOf(spec)}
              was={together || making ? undefined : (now[spec.field] ?? "")}
              together={together}
              disabled={busy || (together && spec.kind === "secret")}
              onChange={(next) => set(spec.field, next)}
            />
          ))}
        </div>
      ) : (
        <NotYet tab={tab} />
      )}
    </Sheet>
  );
}

/**
 * A tab whose fields Azir cannot set yet, said plainly.
 *
 * These are on the phone system's page and not on this one, and a tab that
 * silently showed nothing would read as a bug. What each one is waiting on is
 * specific, because "coming soon" is not information.
 */
function NotYet({ tab }: { tab: string }) {
  const why: Record<string, string> = {
    "IP phone": "No handset has been provisioned on this extension.",
  };
  return (
    <div className="rounded-lg border border-dashed border-edge px-5 py-8 text-center">
      <div className="text-sm font-medium">Not editable from Azir yet</div>
      <p className="mx-auto mt-1.5 max-w-[52ch] text-xs text-ink-dim">
        {why[tab] ?? "Nothing on this tab can be set yet."}
      </p>
      <p className="mt-2 text-xs text-ink-faint">
        Change it on the phone system for now. <Chip>{tab}</Chip>
      </p>
    </div>
  );
}
