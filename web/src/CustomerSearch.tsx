import { useCallback, useEffect, useId, useLayoutEffect, useRef, useState } from "react";
import { createPortal } from "react-dom";
import { api, type Customer } from "./api";

/**
 * Choosing a customer by typing their name.
 *
 * A dropdown is the right control for five options and the wrong one for five
 * hundred: it asks somebody to find a name in a list ordered by nothing they
 * are thinking about. This searches instead — any part of a name, in any case,
 * plus trigram similarity for near misses — and it runs in Postgres rather
 * than over whatever happened to be loaded, so it does not need the whole
 * customer table in the browser to work.
 *
 * Focusing the field used to open a list of whatever the search returned for
 * the empty string, which is a dropdown by another name: on a deployment with
 * three hundred customers it is twenty arbitrary ones, ordered by nothing, in
 * front of somebody who already knows which one they want.
 *
 * What replaces it is the handful this person last changed something on. A
 * technician works on a few customers at a time and comes back to them, so
 * before anything is typed that is a far better guess than the alphabet — and
 * it stays short whatever the deployment looks like.
 *
 * Debounced, because the search is a real query and a request per keystroke
 * would ask the database for answers nobody reads. Two hundred milliseconds is
 * about the gap between typing quickly and having stopped.
 *
 * The list is drawn in a portal. Panels clip their contents so a rounded
 * corner stays rounded, which would cut the results off two rows in — and a
 * picker that only shows you part of the answer is worse than the dropdown it
 * replaced.
 *
 * Answers are matched to the request that asked for them. Debouncing makes a
 * race less likely and does not remove it: a slow "coo" landing after a fast
 * "cooli" would otherwise replace the right list with a stale one, and on a
 * screen whose worst failure is acting on the wrong customer's phone system
 * that is not a cosmetic problem.
 */
export function CustomerSearch({
  value,
  onChange,
  placeholder = "Search customers",
  autoFocus,
  id,
  className,
  ariaLabel,
}: {
  /** The chosen customer's id, or "" for none. */
  value: string;
  onChange: (id: string, customer?: Customer) => void;
  placeholder?: string;
  autoFocus?: boolean;
  id?: string;
  className?: string;
  ariaLabel?: string;
}) {
  const [term, setTerm] = useState("");
  const [hits, setHits] = useState<Customer[]>([]);
  const [chosen, setChosen] = useState<Customer | null>(null);
  // Focused is the field being edited; open is the list having something to
  // show. They were one flag, which is what made clicking the field a
  // dropdown — there is no reason for those two things to be the same.
  const [focused, setFocused] = useState(false);
  const [cursor, setCursor] = useState(0);
  const [searching, setSearching] = useState(false);
  // Where somebody was last. Fetched once when the field is first opened,
  // because it does not change while they are looking at it.
  const [recent, setRecent] = useState<Customer[] | null>(null);
  const inputRef = useRef<HTMLInputElement>(null);
  // Which request is the current one. Anything older is thrown away.
  const latest = useRef(0);
  // Both unconditionally: `id ?? useId()` would skip the hook whenever a
  // caller passed an id, which is a different number of hooks between renders.
  const listID = useId();
  // Where to draw the list, in viewport coordinates.
  const [at, setAt] = useState<{ left: number; top: number; width: number; above: boolean } | null>(null);
  const ownID = useId();
  const inputID = id ?? ownID;

  // Somebody can arrive holding an id — a link, a sheet reopened, a page that
  // remembered. The control has to be able to put a name to it.
  useEffect(() => {
    if (!value) {
      setChosen(null);
      return;
    }
    if (chosen?.id === value) return;
    let live = true;
    void (async () => {
      try {
        const got = await api.customer(value);
        if (live) setChosen(got);
      } catch {
        // A customer that cannot be read is shown as nothing rather than as a
        // raw identifier, which would only ever be read as a bug.
        if (live) setChosen(null);
      }
    })();
    return () => {
      live = false;
    };
  }, [value, chosen?.id]);

  const search = useCallback(async (q: string) => {
    const mine = ++latest.current;
    setSearching(true);
    try {
      const found = await api.findCustomers(q);
      if (mine === latest.current) setHits(found);
    } catch {
      if (mine === latest.current) setHits([]);
    } finally {
      if (mine === latest.current) setSearching(false);
    }
  }, []);

  const asked = term.trim();
  // Typed, or the handful they were last working on. Not the alphabet.
  const showing = asked !== "" ? hits : (recent ?? []);
  const open = focused && (asked !== "" || showing.length > 0);

  useEffect(() => {
    if (!focused || asked === "") {
      setHits([]);
      return;
    }
    const timer = setTimeout(() => void search(asked), 200);
    return () => clearTimeout(timer);
  }, [asked, focused, search]);

  // Once, on first focus. A list of five that somebody is about to click does
  // not need refetching every time they tab through the field.
  useEffect(() => {
    if (!focused || recent !== null) return;
    void (async () => {
      try {
        setRecent(await api.recentCustomers(5));
      } catch {
        // Only a shortcut. Typing still finds everything.
        setRecent([]);
      }
    })();
  }, [focused, recent]);

  useEffect(() => setCursor(0), [hits, recent]);

  // Follows the input, because the list is drawn outside the panel that would
  // otherwise clip it and so cannot be positioned by the layout.
  useLayoutEffect(() => {
    if (!open) {
      setAt(null);
      return;
    }
    const place = () => {
      const box = inputRef.current?.getBoundingClientRect();
      if (!box) return;
      const room = window.innerHeight - box.bottom;
      const above = room < 200 && box.top > room;
      setAt({
        left: box.left,
        top: above ? box.top : box.bottom + 4,
        width: box.width,
        above,
      });
    };
    place();
    // Capture, so a scroll inside any container moves the list with the field
    // rather than leaving it behind.
    window.addEventListener("scroll", place, true);
    window.addEventListener("resize", place);
    return () => {
      window.removeEventListener("scroll", place, true);
      window.removeEventListener("resize", place);
    };
  }, [open, showing.length]);

  function pick(customer: Customer) {
    setChosen(customer);
    setTerm("");
    setFocused(false);
    onChange(customer.id, customer);
    inputRef.current?.blur();
  }

  function clear() {
    setChosen(null);
    setTerm("");
    onChange("");
    inputRef.current?.focus();
  }

  function onKeyDown(e: React.KeyboardEvent) {
    if (e.key === "Escape") {
      e.preventDefault();
      setTerm("");
      inputRef.current?.blur();
      return;
    }
    // Nothing to move through until something has been typed. Arrow-down used
    // to open the list on an empty field, which is the same dropdown by
    // another route.
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

  // Unfocused, the field reads as the chosen customer. Focused, it reads as
  // what is being typed — and the chosen name moves to the placeholder, so
  // clicking in does not look like it was deleted.
  const shown = focused ? term : (chosen?.display_name ?? "");

  return (
    <div className={`relative ${className ?? ""}`}>
      <div className="relative">
        <input
          id={inputID}
          ref={inputRef}
          type="text"
          role="combobox"
          aria-expanded={open}
          aria-controls={listID}
          aria-autocomplete="list"
          aria-activedescendant={open && showing[cursor] ? `${listID}-${showing[cursor].id}` : undefined}
          aria-label={ariaLabel}
          autoComplete="off"
          autoFocus={autoFocus}
          className="h-8 w-full rounded-md border border-edge bg-sunken px-2.5 pr-7 text-sm placeholder:text-ink-faint focus-visible:border-azir focus-visible:outline-none disabled:opacity-50"
          placeholder={chosen ? chosen.display_name : placeholder}
          value={shown}
          onFocus={() => setFocused(true)}
          // A click outside closes the list. Options are taken on mousedown
          // below, before this runs, so choosing one is not a race with it.
          onBlur={() => {
            setFocused(false);
            setTerm("");
          }}
          onChange={(e) => setTerm(e.target.value)}
          onKeyDown={onKeyDown}
        />
        {chosen && !focused && (
          <button
            type="button"
            aria-label={`Clear ${chosen.display_name}`}
            className="absolute right-1.5 top-1/2 -translate-y-1/2 rounded px-1 text-xs text-ink-faint transition-colors hover:text-ink"
            onClick={clear}
          >
            ×
          </button>
        )}
      </div>

      {open && at && createPortal(
        <ul
          id={listID}
          role="listbox"
          className="fixed z-50 max-h-64 overflow-auto rounded-md border border-edge bg-raised py-1 shadow-e2"
          style={{
            left: at.left,
            width: at.width,
            ...(at.above
              ? { bottom: window.innerHeight - at.top + 4 }
              : { top: at.top }),
          }}
        >
          {asked === "" && showing.length > 0 && (
            <li className="px-2.5 pb-1 pt-1.5 text-2xs uppercase tracking-wide text-ink-faint">
              Where you were last
            </li>
          )}
          {showing.map((customer, i) => (
            <li
              key={customer.id}
              id={`${listID}-${customer.id}`}
              role="option"
              aria-selected={i === cursor}
              className={`cursor-pointer px-2.5 py-1.5 text-sm ${
                i === cursor ? "bg-sunken" : ""
              } ${customer.id === value ? "font-medium" : ""}`}
              onMouseEnter={() => setCursor(i)}
              // Taken before the input's blur, so a click lands on the option
              // rather than on a list that has just closed.
              onMouseDown={(e) => {
                e.preventDefault();
                pick(customer);
              }}
            >
              {customer.display_name}
            </li>
          ))}

          {showing.length === 0 && (
            <li className="px-2.5 py-2 text-xs text-ink-faint">
              {searching ? "Searching…" : `Nothing matches "${asked}"`}
            </li>
          )}
        </ul>,
        document.body,
      )}
    </div>
  );
}
