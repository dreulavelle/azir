import { useEffect, useId, useLayoutEffect, useRef, useState } from "react";
import { createPortal } from "react-dom";

/**
 * Choosing from a list too long to scroll.
 *
 * A native select is the right control for eight options and the wrong one for
 * eighty: it opens a column of names ordered by nothing anybody is thinking
 * about, and finding one means reading all of them. A deployment with a session
 * border controller per site, or with every router-capable handset on it, has
 * eighty.
 *
 * So above a threshold the same field becomes something to type into. The
 * options are already here — they arrived with the field list — so the
 * filtering is local and immediate. Nothing is debounced, because there is no
 * request to spare: a delay would only make it feel slower than it is.
 *
 * Drawn in a portal for the same reason the customer picker is. This sits in a
 * drawer that scrolls its own contents, which would cut the list off two rows
 * in.
 */

/** One thing that can be picked, with how it reads and how it is doing. */
export type Option = {
  value: string;
  label: string;
  /** "up", "down", or absent where the phone system does not track it. */
  state?: string;
};

/** How many options before scrolling a list stops being reasonable. */
export const TOO_MANY = 8;

export function LongChoice({
  id,
  options,
  value,
  onChange,
  disabled,
  ariaLabel,
  placeholder = "Type to find one",
}: {
  id?: string;
  options: Option[];
  value: string;
  onChange: (next: string) => void;
  disabled?: boolean;
  ariaLabel?: string;
  placeholder?: string;
}) {
  const [term, setTerm] = useState("");
  const [focused, setFocused] = useState(false);
  const [cursor, setCursor] = useState(0);
  const inputRef = useRef<HTMLInputElement>(null);
  const listID = useId();
  const [at, setAt] = useState<{ left: number; top: number; width: number; above: boolean } | null>(
    null,
  );

  const asked = term.trim().toLowerCase();
  const showing = asked
    ? options.filter(
        (o) => o.label.toLowerCase().includes(asked) || o.value.toLowerCase().includes(asked),
      )
    : options;
  const open = focused;
  const current = options.find((o) => o.value === value);

  useEffect(() => setCursor(0), [asked]);

  useLayoutEffect(() => {
    if (!open) {
      setAt(null);
      return;
    }
    const place = () => {
      const box = inputRef.current?.getBoundingClientRect();
      if (!box) return;
      const room = window.innerHeight - box.bottom;
      const above = room < 220 && box.top > room;
      setAt({ left: box.left, top: above ? box.top : box.bottom + 4, width: box.width, above });
    };
    place();
    window.addEventListener("scroll", place, true);
    window.addEventListener("resize", place);
    return () => {
      window.removeEventListener("scroll", place, true);
      window.removeEventListener("resize", place);
    };
  }, [open, showing.length]);

  function pick(option: Option) {
    onChange(option.value);
    setTerm("");
    setFocused(false);
    inputRef.current?.blur();
  }

  function onKeyDown(e: React.KeyboardEvent) {
    if (e.key === "Escape") {
      e.preventDefault();
      setTerm("");
      inputRef.current?.blur();
      return;
    }
    if (!open) return;
    if (e.key === "ArrowDown") {
      e.preventDefault();
      setCursor((c) => Math.min(c + 1, showing.length - 1));
    } else if (e.key === "ArrowUp") {
      e.preventDefault();
      setCursor((c) => Math.max(c - 1, 0));
    } else if (e.key === "Enter") {
      e.preventDefault();
      const active = showing[cursor];
      if (active) pick(active);
    }
  }

  return (
    <div className="relative">
      <input
        id={id}
        ref={inputRef}
        type="text"
        role="combobox"
        aria-expanded={open}
        aria-controls={listID}
        aria-autocomplete="list"
        aria-label={ariaLabel}
        autoComplete="off"
        disabled={disabled}
        className="h-8 w-full rounded-md border border-edge bg-sunken px-2.5 text-sm placeholder:text-ink-faint focus-visible:border-azir focus-visible:outline-none disabled:opacity-50"
        placeholder={current ? current.label : placeholder}
        value={focused ? term : (current?.label ?? "")}
        onFocus={() => setFocused(true)}
        onBlur={() => {
          setFocused(false);
          setTerm("");
        }}
        onChange={(e) => setTerm(e.target.value)}
        onKeyDown={onKeyDown}
      />

      {open &&
        at &&
        createPortal(
          <ul
            id={listID}
            role="listbox"
            className="fixed z-50 max-h-64 overflow-auto rounded-md border border-edge bg-raised py-1 shadow-e2"
            style={{
              left: at.left,
              width: at.width,
              ...(at.above ? { bottom: window.innerHeight - at.top + 4 } : { top: at.top }),
            }}
          >
            {showing.map((option, i) => (
              <li
                key={option.value}
                role="option"
                aria-selected={i === cursor}
                className={`flex cursor-pointer items-center gap-2 px-2.5 py-1.5 text-sm ${
                  i === cursor ? "bg-sunken" : ""
                } ${option.value === value ? "font-medium" : ""}`}
                onMouseEnter={() => setCursor(i)}
                // Before the input's blur, so a click lands on the option
                // rather than on a list that has just closed.
                onMouseDown={(e) => {
                  e.preventDefault();
                  pick(option);
                }}
              >
                {option.state && (
                  <span
                    aria-hidden
                    title={option.state === "down" ? "Not connected" : "Connected"}
                    className={`size-1.5 shrink-0 rounded-full ${
                      option.state === "down" ? "bg-critical" : "bg-steady"
                    }`}
                  />
                )}
                <span className="truncate">{option.label}</span>
              </li>
            ))}

            {showing.length === 0 && (
              <li className="px-2.5 py-2 text-xs text-ink-faint">Nothing matches "{term.trim()}"</li>
            )}
          </ul>,
          document.body,
        )}
    </div>
  );
}
