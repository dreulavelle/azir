import { cn } from "@/lib/cn";
import { createContext, useCallback, useContext, useEffect, useState, type ReactNode } from "react";

/**
 * Confirmation that something happened, where the person's attention already is.
 *
 * A message appearing inside the panel someone has just finished with is a
 * message they have already looked away from. These sit in a corner, say what
 * happened in the past tense, and leave on their own — except failures, which
 * stay until dismissed, because a failure that vanishes is a failure nobody
 * knows about.
 */

type Tone = "good" | "bad" | "info";

type Note = {
  id: number;
  tone: Tone;
  message: string;
  detail?: string;
  onOpen?: () => void;
  holdMs?: number;
};

type Push = (
  message: string,
  opts?: {
    tone?: Tone;
    detail?: string;
    /** Makes the whole toast a control that takes you to what it is about. */
    onOpen?: () => void;
    /** How long before it goes on its own. Zero waits to be dismissed. */
    holdMs?: number;
  },
) => void;

const ToastContext = createContext<Push>(() => {});

/** Announces the result of an action. */
export function useToast(): Push {
  return useContext(ToastContext);
}

let nextId = 1;

export function ToastHost({ children }: { children: ReactNode }) {
  const [notes, setNotes] = useState<Note[]>([]);

  const push = useCallback<Push>((message, opts = {}) => {
    const note: Note = {
      id: nextId++,
      tone: opts.tone ?? "good",
      message,
      detail: opts.detail,
      onOpen: opts.onOpen,
      holdMs: opts.holdMs,
    };
    setNotes((n) => [...n, note]);
  }, []);

  const dismiss = useCallback((id: number) => {
    setNotes((n) => n.filter((note) => note.id !== id));
  }, []);

  return (
    <ToastContext.Provider value={push}>
      {children}
      <div className="pointer-events-none fixed bottom-5 right-5 z-[60] flex w-[min(92vw,360px)] flex-col gap-2" role="status" aria-live="polite">
        {notes.map((note) => (
          <Toast key={note.id} note={note} onDismiss={() => dismiss(note.id)} />
        ))}
      </div>
    </ToastContext.Provider>
  );
}

function Toast({ note, onDismiss }: { note: Note; onDismiss: () => void }) {
  useEffect(() => {
    // A failure stays until it is acknowledged; anything else is just noise
    // after a few seconds.
    if (note.tone === "bad") return;
    // A notification about somebody else's action stays longer than a
    // confirmation of your own: you were not watching for it, so four seconds
    // is long enough to miss entirely.
    const hold = note.holdMs ?? (note.onOpen ? 12_000 : 4200);
    if (hold === 0) return;
    const timer = window.setTimeout(onDismiss, hold);
    return () => window.clearTimeout(timer);
  }, [note.tone, note.holdMs, note.onOpen, onDismiss]);

  return (
    <div
      className={cn(
        "pointer-events-auto flex items-start gap-3 rounded-lg border bg-raised px-3.5 py-3 shadow-e3",
        note.tone === "bad" ? "border-critical/40" : note.tone === "good" ? "border-steady/40" : "border-edge",
      )}
    >
      {note.onOpen ? (
        <button
          className="min-w-0 flex-1 text-left"
          onClick={() => {
            note.onOpen?.();
            onDismiss();
          }}
        >
          <div className="text-sm font-medium">{note.message}</div>
          {note.detail && <div className="mt-0.5 truncate text-xs text-ink-dim">{note.detail}</div>}
          <div className="mt-1 font-mono text-2xs uppercase tracking-[0.09em] text-azir">Open</div>
        </button>
      ) : (
        <div className="min-w-0 flex-1">
          <div className="text-sm font-medium">{note.message}</div>
          {note.detail && <div className="mt-0.5 text-xs text-ink-dim">{note.detail}</div>}
        </div>
      )}
      <button className="-mr-1 -mt-1 grid size-6 shrink-0 place-items-center rounded text-ink-faint transition-colors hover:bg-sunken hover:text-ink" onClick={onDismiss} aria-label="Dismiss">
        ×
      </button>
    </div>
  );
}
