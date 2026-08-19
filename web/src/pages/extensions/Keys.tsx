import { useState } from "react";
import type { BlfKey, BlfKind } from "../../api";
import { Button, Chip, Label, Picker, TextInput } from "../../ui";

/**
 * The buttons on a desk phone.
 *
 * A list rather than a form, because that is what it is: the phone system
 * numbers buttons down the side of the handset, so the order on screen is the
 * order on the phone. Adding, removing and moving a key are the three things
 * anybody does here.
 *
 * The whole layout is written at once — the phone system keeps it as one value
 * and there is no such thing as changing one button — so this holds the entire
 * layout and hands it back entire.
 *
 * It starts empty rather than with ten blank keys. A phone system that offers
 * ten placeholders is offering ten chances to leave one half-filled.
 */

/** A key nobody has filled in yet. */
const BLANK: BlfKey = { no: 0, kind: "BLF", id: "-1", value: "" };

export function Keys({
  keys,
  kinds,
  onChange,
  copyFrom,
  onCopyFrom,
  together,
  disabled,
}: {
  keys: BlfKey[];
  kinds: BlfKind[];
  onChange: (next: BlfKey[]) => void;
  /** The extension a layout would be copied from, or "". */
  copyFrom: string;
  onCopyFrom: (extension: string) => void;
  /** Editing several phones, where the only sensible layout change is a copy. */
  together?: boolean;
  disabled?: boolean;
}) {
  const [showCopy, setShowCopy] = useState(together ?? false);

  const takes = (kind: string) => kinds.find((k) => k.kind === kind)?.takes ?? "";
  const note = (kind: string) => kinds.find((k) => k.kind === kind)?.note ?? "";

  function set(at: number, patch: Partial<BlfKey>) {
    onChange(keys.map((key, i) => (i === at ? { ...key, ...patch } : key)));
  }

  function move(at: number, by: number) {
    const to = at + by;
    if (to < 0 || to >= keys.length) return;
    const next = [...keys];
    [next[at], next[to]] = [next[to], next[at]];
    onChange(next);
  }

  if (together) {
    return (
      <div className="flex flex-col gap-3">
        <p className="max-w-[62ch] text-sm text-ink-dim">
          The phone system keeps a key layout as one value, so there is no setting one button
          across several phones. What there is, and what people actually want, is giving these
          phones the layout another one already has.
        </p>
        <CopyFrom value={copyFrom} onChange={onCopyFrom} disabled={disabled} />
      </div>
    );
  }

  return (
    <div className="flex flex-col gap-3">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <span className="text-xs text-ink-faint">
          {keys.length === 0
            ? "No buttons set. The phone shows its defaults."
            : `${keys.length} ${keys.length === 1 ? "button" : "buttons"}, in the order they sit on the phone`}
        </span>
        <button
          className="rounded-md px-2 py-1 text-xs text-ink-dim transition-colors hover:bg-sunken hover:text-ink"
          onClick={() => setShowCopy((was) => !was)}
        >
          {showCopy ? "Set them here instead" : "Copy from another extension"}
        </button>
      </div>

      {showCopy && <CopyFrom value={copyFrom} onChange={onCopyFrom} disabled={disabled} />}

      {!showCopy && (
        <>
          {keys.map((key, i) => (
            <div
              key={i}
              className="flex flex-wrap items-start gap-2 rounded-lg border border-edge bg-sunken/40 px-2.5 py-2"
            >
              <span className="mt-2 w-6 shrink-0 text-center font-mono text-xs text-ink-faint">
                {key.no || i + 1}
              </span>

              <div className="min-w-[170px] flex-1">
                <Picker
                  value={key.kind}
                  disabled={disabled}
                  aria-label={`What button ${key.no || i + 1} does`}
                  onChange={(e) =>
                    // The value means something different for each kind, so it
                    // does not survive the change. Keeping it would leave an
                    // extension number sitting in a parking spot.
                    set(i, { kind: e.target.value, value: "", id: idFor(e.target.value) })
                  }
                >
                  {kinds.map((k) => (
                    <option key={k.kind} value={k.kind}>
                      {k.label}
                    </option>
                  ))}
                </Picker>
              </div>

              <div className="min-w-[150px] flex-1">
                <Value
                  kind={key.kind}
                  takes={takes(key.kind)}
                  value={key.value}
                  id={key.id}
                  disabled={disabled}
                  onValue={(value) => set(i, { value })}
                  onID={(id) => set(i, { id })}
                />
                {note(key.kind) && (
                  <span className="mt-1 block text-2xs text-ink-faint">{note(key.kind)}</span>
                )}
              </div>

              <div className="flex shrink-0 gap-1">
                <IconButton label="Move up" disabled={disabled || i === 0} onClick={() => move(i, -1)}>
                  ↑
                </IconButton>
                <IconButton
                  label="Move down"
                  disabled={disabled || i === keys.length - 1}
                  onClick={() => move(i, 1)}
                >
                  ↓
                </IconButton>
                <IconButton
                  label={`Remove button ${key.no || i + 1}`}
                  disabled={disabled}
                  onClick={() => onChange(keys.filter((_, at) => at !== i))}
                >
                  ✕
                </IconButton>
              </div>
            </div>
          ))}

          <div>
            <Button disabled={disabled} onClick={() => onChange([...keys, { ...BLANK }])}>
              Add a button
            </Button>
          </div>
        </>
      )}
    </div>
  );
}

/** What a key points at, which is a different question for each kind. */
function Value({
  kind,
  takes,
  value,
  id,
  disabled,
  onValue,
  onID,
}: {
  kind: string;
  takes: string;
  value: string;
  id: string;
  disabled?: boolean;
  onValue: (next: string) => void;
  onID: (next: string) => void;
}) {
  // Queue login is decided by which of two it is, not by a value typed in.
  if (kind === "QueueLogin") {
    return (
      <Picker
        value={id === "LOGGEDINQUEUE" ? "LOGGEDINQUEUE" : "LOGGEDOUTQUEUE"}
        disabled={disabled}
        aria-label="Which way the queue key works"
        onChange={(e) => onID(e.target.value)}
      >
        <option value="LOGGEDINQUEUE">Log in to queues</option>
        <option value="LOGGEDOUTQUEUE">Log out of queues</option>
      </Picker>
    );
  }

  if (takes === "") {
    return <span className="block pt-2 text-xs text-ink-faint">Nothing to set</span>;
  }

  // A custom speed dial holds the number and its labels, one per line, which
  // is how the phone system stores it and how the phone reads it.
  if (kind === "CustomSpeedDial") {
    const [number = "", first = "", second = ""] = value.split("\n");
    const join = (n: string, a: string, b: string) =>
      [n, a, b].join("\n").replace(/\n+$/, "");
    return (
      <div className="flex flex-col gap-1">
        <TextInput
          value={number}
          placeholder="Number to dial"
          disabled={disabled}
          aria-label="Number to dial"
          onChange={(e) => onValue(join(e.target.value, first, second))}
        />
        <div className="flex gap-1">
          <TextInput
            value={first}
            placeholder="Label"
            disabled={disabled}
            aria-label="Label"
            onChange={(e) => onValue(join(number, e.target.value, second))}
          />
          <TextInput
            value={second}
            placeholder="Second line"
            disabled={disabled}
            aria-label="Second line of the label"
            onChange={(e) => onValue(join(number, first, e.target.value))}
          />
        </div>
      </div>
    );
  }

  return (
    <TextInput
      value={value}
      disabled={disabled}
      placeholder={takes}
      aria-label={takes}
      onChange={(e) => onValue(e.target.value)}
    />
  );
}

/** Naming the extension to take a layout from. */
function CopyFrom({
  value,
  onChange,
  disabled,
}: {
  value: string;
  onChange: (next: string) => void;
  disabled?: boolean;
}) {
  return (
    <div className="rounded-lg border border-edge bg-sunken/60 px-3 py-3">
      <Label className="mb-1.5 block">Copy the buttons from</Label>
      <div className="flex flex-wrap items-center gap-2">
        <div className="w-[150px]">
          <TextInput
            value={value}
            placeholder="Extension"
            disabled={disabled}
            aria-label="Extension to copy the buttons from"
            onChange={(e) => onChange(e.target.value.trim())}
          />
        </div>
        {value && <Chip tone="accent">replaces every button</Chip>}
      </div>
      <p className="mt-2 max-w-[62ch] text-xs text-ink-faint">
        The whole layout is taken, on the same buttons it sits on there. A key that watches an
        extension keeps watching that extension.
      </p>
    </div>
  );
}

/** The id a fresh key of each kind starts with. */
function idFor(kind: string): string {
  return kind === "QueueLogin" ? "LOGGEDOUTQUEUE" : "-1";
}

function IconButton({
  children,
  label,
  disabled,
  onClick,
}: {
  children: React.ReactNode;
  label: string;
  disabled?: boolean;
  onClick: () => void;
}) {
  return (
    <button
      type="button"
      aria-label={label}
      title={label}
      disabled={disabled}
      className="grid size-7 place-items-center rounded-md border border-edge bg-panel text-xs text-ink-dim transition-colors hover:bg-sunken hover:text-ink disabled:opacity-30"
      onClick={onClick}
    >
      {children}
    </button>
  );
}
