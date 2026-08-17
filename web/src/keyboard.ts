import { useEffect, useRef } from "react";

/**
 * The keyboard layer.
 *
 * A technician works one ticket after another, and reaching for the mouse
 * between each one is the difference between a tool that keeps up and a tool
 * that is in the way. Every shortcut here maps to something already on screen,
 * so nothing is hidden behind a key nobody can discover — `?` lists the lot.
 */

/** True when the keystroke belongs to whatever the person is typing into. */
function isTyping(target: EventTarget | null): boolean {
  const el = target as HTMLElement | null;
  if (!el) return false;
  const tag = el.tagName;
  return (
    tag === "INPUT" ||
    tag === "TEXTAREA" ||
    tag === "SELECT" ||
    el.isContentEditable ||
    // Radix marks its open menus and dialogs; they own the keyboard while up.
    el.closest("[role='dialog'],[role='listbox'],[role='menu']") !== null
  );
}

export type Chord = {
  /** A single key, or a two-key sequence like ["g", "t"]. */
  keys: string | [string, string];
  run: () => void;
  /** What it does, for the shortcuts sheet. Omit to keep it out of the list. */
  label?: string;
  group?: string;
};

/**
 * Binds chords globally.
 *
 * Two-key sequences follow the "g then t" convention rather than a modifier,
 * because modifiers collide with the browser's own and with screen readers,
 * while a prefix key does not.
 */
export function useShortcuts(chords: Chord[], enabled = true) {
  // Held in a ref so the listener is installed once and still sees current
  // handlers — otherwise every render rebinds, and a keystroke during the gap
  // is dropped.
  const current = useRef(chords);
  current.current = chords;

  useEffect(() => {
    if (!enabled) return;

    let prefix: string | null = null;
    let prefixTimer: number | undefined;

    const clearPrefix = () => {
      prefix = null;
      window.clearTimeout(prefixTimer);
    };

    const onKey = (e: KeyboardEvent) => {
      if (e.metaKey || e.ctrlKey || e.altKey) return;
      if (isTyping(e.target)) return;

      const key = e.key.toLowerCase();

      // Complete a pending sequence first, so "g" then "t" wins over any
      // single-key binding on "t".
      if (prefix) {
        const match = current.current.find(
          (c) => Array.isArray(c.keys) && c.keys[0] === prefix && c.keys[1] === key,
        );
        clearPrefix();
        if (match) {
          e.preventDefault();
          match.run();
        }
        return;
      }

      // Start a sequence if anything is waiting on this key as a prefix.
      if (current.current.some((c) => Array.isArray(c.keys) && c.keys[0] === key)) {
        prefix = key;
        // Abandoned after a moment, so a stray "g" does not swallow the next
        // thing someone types.
        prefixTimer = window.setTimeout(clearPrefix, 1400);
        return;
      }

      const match = current.current.find((c) => c.keys === key);
      if (match) {
        e.preventDefault();
        match.run();
      }
    };

    window.addEventListener("keydown", onKey);
    return () => {
      window.removeEventListener("keydown", onKey);
      clearPrefix();
    };
  }, [enabled]);
}
