import type { BulkSpec } from "../../api";
import { Picker, TextInput } from "../../ui";
import { LongChoice, TOO_MANY, type Option } from "./LongChoice";

/**
 * One field, drawn the way its kind wants to be drawn.
 *
 * Three screens set extension fields — editing one, editing many together, and
 * making a new one — and they are the same fields with the same rules. One
 * control, three modes, so a choice that gains an option or a field that
 * changes its wording changes in one place.
 *
 * "Leave alone" is a real value, not the absence of one. Editing forty
 * extensions together means most fields are not being set, and a form that
 * expressed that as an empty box would blank forty voicemail settings the
 * first time somebody tabbed through it.
 */

/**
 * Where a call can go, in words.
 *
 * The phone system's own names read as identifiers — RingGroup, VoiceMail —
 * and this is a form somebody uses rather than a field they debug.
 */
const WHERE: Record<string, string> = {
  None: "Nowhere",
  VoiceMail: "Voicemail",
  Extension: "An extension",
  External: "An outside number",
  Queue: "A queue",
  RingGroup: "A ring group",
  IVR: "A digital receptionist",
  Fax: "Fax",
};

/** The places that mean nothing without saying which one. */
const NEEDS_NUMBER = new Set(["Extension", "External", "Queue", "RingGroup", "IVR", "Fax"]);

/** Splits "Extension:101" into where and which. */
function split(value: string): [string, string] {
  const at = value.indexOf(":");
  if (at < 0) return [value, ""];
  return [value.slice(0, at), value.slice(at + 1)];
}

/**
 * The dot in front of a choice that can be up or down.
 *
 * A character rather than an element, because a native <option> renders text
 * and nothing else — no span, no colour, no CSS that reaches inside it. This
 * is the one control in the interface that cannot draw its own status, and a
 * routing device somebody has not finished setting up is exactly the thing
 * worth seeing before picking it rather than after.
 *
 * Beside the closed control the same fact is a real element; see Reachable.
 */
const DOT: Record<string, string> = {
  up: "\u{1F7E2} ",
  down: "\u{1F534} ",
};

/** Whether the thing a field points at is answering, in words and in colour. */
function Reachable({ state }: { state: string }) {
  const down = state === "down";
  return (
    <span className="flex items-center gap-1.5 text-2xs text-ink-faint">
      <span
        aria-hidden
        className={`size-1.5 shrink-0 rounded-full ${down ? "bg-critical" : "bg-steady"}`}
      />
      {down ? "Not connected" : "Connected"}
    </span>
  );
}

/** Not being set. */
export const LEAVE = "";
/** Set to different things across what is being edited. Never a real value. */
export const MIXED = "\u0000mixed";

export function Field({
  spec,
  value,
  onChange,
  together,
  was,
  disabled,
}: {
  spec: BulkSpec;
  value: string;
  onChange: (next: string) => void;
  /** Editing more than one, so every field can be left alone. */
  together?: boolean;
  /** What this field says now, for marking what is about to change. */
  was?: string;
  disabled?: boolean;
}) {
  const mixed = value === MIXED;
  const changed = !together && was !== undefined && value !== was;
  // A field no two extensions may share cannot be set across several at once.
  // Typing one address for five is not an edit that half works: the first
  // takes it and the rest are refused, after the first has been written.
  const onePerExtension = Boolean(together && spec.unique);

  return (
    <div className="flex items-start justify-between gap-4 py-1.5">
      <label htmlFor={`f-${spec.field}`} className="min-w-0 flex-1 pt-1.5 text-sm">
        {spec.label}
        {changed && (
          <span
            className="ml-1.5 inline-block size-1.5 rounded-full bg-azir align-middle"
            title="Changed"
          />
        )}
        {onePerExtension ? (
          <span className="mt-0.5 block text-2xs text-ink-faint">
            Belongs to one extension, so it is set one at a time.
          </span>
        ) : (
          mixed && (
            <span className="mt-0.5 block text-2xs text-ink-faint">
              These differ. Setting it changes all of them.
            </span>
          )
        )}
      </label>

      <div className="w-[190px] shrink-0">
        <Control
          spec={spec}
          value={onePerExtension ? LEAVE : value}
          onChange={onChange}
          together={together}
          disabled={disabled || onePerExtension}
        />
      </div>
    </div>
  );
}

function Control({
  spec,
  value,
  onChange,
  together,
  disabled,
}: {
  spec: BulkSpec;
  value: string;
  onChange: (next: string) => void;
  together?: boolean;
  disabled?: boolean;
}) {
  const leave = together ? <option value={LEAVE}>Leave alone</option> : null;
  // Just "Mixed": the sentence explaining it is already under the label, and
  // repeating it inside a 190px control only truncates it.
  const mixed = value === MIXED ? <option value={MIXED}>Mixed</option> : null;

  if (spec.kind === "bool") {
    return (
      <Picker
        id={`f-${spec.field}`}
        value={value}
        disabled={disabled}
        onChange={(e) => onChange(e.target.value)}
      >
        {mixed}
        {leave}
        <option value="yes">Yes</option>
        <option value="no">No</option>
      </Picker>
    );
  }

  if (spec.kind === "choice") {
    // A select whose value matches no option shows the first one instead, and
    // says nothing about it. An extension with no handset has no routing
    // device, and the control read as though it had been pointed at the phone
    // system — a setting nobody chose, displayed as though somebody had.
    // In the several-at-once form an empty value is "leave alone" and already
    // has an option. Editing one, it means the phone system holds nothing here.
    const unset =
      value !== MIXED &&
      !(spec.choices ?? []).includes(value) &&
      !(together && value === LEAVE);

    // Long lists become something to type into. A phone system with a session
    // border controller per site, or with every router-capable handset on it,
    // turns this field into eighty names in a column ordered by nothing.
    if ((spec.choices ?? []).length > TOO_MANY) {
      const options: Option[] = [];
      if (value === MIXED) options.push({ value: MIXED, label: "Mixed" });
      if (together) options.push({ value: LEAVE, label: "Leave alone" });
      if (unset) options.push({ value, label: value || "Not set" });
      for (const choice of spec.choices ?? []) {
        options.push({
          value: choice,
          label: spec.labels?.[choice] ?? choice,
          state: spec.states?.[choice],
        });
      }
      return (
        <div className="flex flex-col gap-1">
          <LongChoice
            id={`f-${spec.field}`}
            ariaLabel={spec.label}
            options={options}
            value={value}
            disabled={disabled}
            onChange={onChange}
          />
          {spec.states?.[value] && <Reachable state={spec.states[value]} />}
        </div>
      );
    }

    return (
      <div className="flex flex-col gap-1">
        <Picker
          id={`f-${spec.field}`}
          value={value}
          disabled={disabled}
          onChange={(e) => onChange(e.target.value)}
        >
          {mixed}
          {leave}
          {unset && <option value={value}>{value || "Not set"}</option>}
          {(spec.choices ?? []).map((choice) => (
            <option key={choice} value={choice}>
              {DOT[spec.states?.[choice] ?? ""] ?? ""}
              {spec.labels?.[choice] ?? choice}
            </option>
          ))}
        </Picker>
        {spec.states?.[value] && <Reachable state={spec.states[value]} />}
      </div>
    );
  }

  // Something worth seeing that Azir will not change. Shown as what it says
  // rather than as a disabled box, which reads as broken.
  if (spec.kind === "readonly") {
    return (
      <span className="block pt-2 text-right text-sm text-ink-dim">
        {value || <span className="italic text-ink-faint">unset</span>}
      </span>
    );
  }

  // Where a call goes: a kind of place, and which one when that means
  // something. Two controls for one field, because "voicemail" is a complete
  // answer and "extension" on its own is not.
  if (spec.kind === "destination") {
    const [where, number] = split(value === MIXED ? "" : value);
    const needsNumber = NEEDS_NUMBER.has(where);
    return (
      <div className="flex flex-col gap-1">
        <Picker
          id={`f-${spec.field}`}
          value={value === MIXED ? MIXED : where}
          disabled={disabled}
          onChange={(e) => {
            const next = e.target.value;
            if (next === LEAVE || next === MIXED) return onChange(next);
            // The number belongs to the place. Keeping it across a change
            // would leave an extension number sitting in an outside line.
            onChange(NEEDS_NUMBER.has(next) ? `${next}:` : next);
          }}
        >
          {mixed}
          {leave}
          {(spec.choices ?? []).map((choice) => (
            <option key={choice} value={choice}>
              {WHERE[choice] ?? choice}
            </option>
          ))}
        </Picker>
        {needsNumber && (
          <TextInput
            value={number}
            disabled={disabled}
            placeholder={where === "External" ? "Outside number" : "Number"}
            aria-label={`${spec.label}: which one`}
            onChange={(e) => onChange(`${where}:${e.target.value.trim()}`)}
          />
        )}
      </div>
    );
  }

  // A secret has no current value to show, because nothing reads it back.
  // Typing one sets it; leaving it empty leaves whatever is there.
  if (spec.kind === "secret") {
    return (
      <TextInput
        id={`f-${spec.field}`}
        type="password"
        autoComplete="new-password"
        value={value === MIXED ? "" : value}
        disabled={disabled}
        placeholder="Unchanged"
        onChange={(e) => onChange(e.target.value)}
      />
    );
  }

  return (
    <TextInput
      id={`f-${spec.field}`}
      value={value === MIXED ? "" : value}
      disabled={disabled}
      placeholder={value === MIXED ? "Mixed" : together ? "Leave alone" : ""}
      onChange={(e) => onChange(e.target.value)}
    />
  );
}
