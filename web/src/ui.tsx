import { useEffect, useState, type ReactNode } from "react";
import {
  Download,
  AtSign,
  Building2,
  Check,
  ChevronLeft,
  Clock,
  Copy,
  CornerUpLeft,
  ExternalLink,
  ListFilter,
  LogOut,
  Stethoscope,
  Phone,
  Plus,
  RefreshCw,
  Search,
  SendHorizontal,
  Settings2,
  Sparkles,
  StickyNote,
  Ticket as TicketIcon,
  UserRound,
  Users,
} from "lucide-react";
import { cn } from "@/lib/cn";

/** Shared presentation pieces. Nothing here knows about any integration. */

// --- icons -------------------------------------------------------------------
// One family, one stroke weight. The previous set was hand-drawn at three
// different weights and sizes, which is a small thing that makes every screen
// look slightly unfinished.

export const Icon = {
  triage: () => <ListFilter className="size-4" />,
  ticket: () => <TicketIcon className="size-4" />,
  people: () => <Users className="size-4" />,
  settings: () => <Settings2 className="size-4" />,
  search: () => <Search className="size-3.5" />,
  reply: () => <CornerUpLeft className="size-3.5" />,
  note: () => <StickyNote className="size-3.5" />,
  clock: () => <Clock className="size-3.5" />,
  spark: () => <Sparkles className="size-3.5" />,
  back: () => <ChevronLeft className="size-4" />,
  plus: () => <Plus className="size-3.5" />,
  copy: () => <Copy className="size-3.5" />,
  tick: () => <Check className="size-3.5" />,
  refresh: () => <RefreshCw className="size-3.5" />,
  external: () => <ExternalLink className="size-3.5" />,
  send: () => <SendHorizontal className="size-3.5" />,
  out: () => <LogOut className="size-3.5" />,
  diagnostics: () => <Stethoscope className="size-4" />,
  person: () => <UserRound className="size-3.5" />,
  business: () => <Building2 className="size-3.5" />,
  phone: () => <Phone className="size-3.5" />,
  mail: () => <AtSign className="size-3.5" />,
  download: () => <Download className="size-3.5" />,
};

// --- the design language -----------------------------------------------------

/**
 * The four states anything in Azir can be in.
 *
 * Named after what they mean rather than after a colour, because the colour is
 * an implementation of the meaning and gets to change: "critical" stays true if
 * the red moves.
 */
export type Signal = "critical" | "attention" | "steady" | "idle";

/** Signal colour as a background, for the rail that runs down the left edge. */
const RAIL: Record<Signal, string> = {
  critical: "bg-critical",
  attention: "bg-attention",
  steady: "bg-steady",
  idle: "bg-edge-strong",
};

/** Signal colour as text and a wash, for chips and counts. */
const WASH: Record<Signal, string> = {
  critical: "text-critical bg-critical/10 border-critical/25",
  attention: "text-attention bg-attention/10 border-attention/25",
  steady: "text-steady bg-steady/10 border-steady/25",
  idle: "text-ink-dim bg-sunken border-edge",
};

/**
 * The structural voice: mono, small, wide-tracked, quiet.
 *
 * Used for the things a technician reads as data rather than as prose — lane
 * headers, field names, counts, units. Keeping it to one class means the
 * distinction between "this is a label" and "this is what someone wrote" holds
 * everywhere without being re-decided per screen.
 */
/*
Label names a control.

A real <label> when it is given something to point at, and a span otherwise.
Clicking a label should focus its field and a screen reader should read the two
together, and neither happens for a span — but plenty of these sit above a
group of controls rather than one, where a <label> pointing at nothing would be
worse than no label element at all.
*/
export function Label({
  children,
  className,
  title,
  htmlFor,
}: {
  children: ReactNode;
  className?: string;
  title?: string;
  /** The id of the control this names. */
  htmlFor?: string;
}) {
  const As = htmlFor ? "label" : "span";
  return (
    <As
      htmlFor={htmlFor}
      title={title}
      className={cn(
        "font-mono text-2xs font-medium uppercase tracking-[0.09em] text-ink-faint",
        htmlFor ? "cursor-pointer" : "",
        className,
      )}
    >
      {children}
    </As>
  );
}

/**
 * A number worth looking at, with the word that says what it counts.
 *
 * The number leads at a size nothing else on the page uses, so a row of these
 * reads before any of the prose around them does.
 */
export function Stat({
  value,
  label,
  tone,
}: {
  value: ReactNode;
  label: string;
  tone?: Signal;
}) {
  return (
    <div className="flex flex-col gap-0.5">
      <span
        className={cn(
          "font-mono text-xl font-medium tabular-nums leading-none",
          tone === "critical" && "text-critical",
          tone === "attention" && "text-attention",
          tone === "steady" && "text-steady",
        )}
      >
        {value}
      </span>
      <Label>{label}</Label>
    </div>
  );
}

/** A raised surface. One border, one shadow, no decoration. */
export function Panel({
  children,
  className,
  rail,
}: {
  children: ReactNode;
  className?: string;
  /** Runs the signal colour down the left edge. */
  rail?: Signal;
}) {
  return (
    <div
      className={cn(
        "relative overflow-hidden rounded-lg border border-edge bg-panel shadow-e1",
        className,
      )}
    >
      {rail && <span className={cn("absolute inset-y-0 left-0 w-[3px]", RAIL[rail])} />}
      {children}
    </div>
  );
}

/** The bar across the top of a panel: a name on the left, a count on the right. */
export function PanelHead({
  children,
  className,
}: {
  children: ReactNode;
  className?: string;
}) {
  return (
    <div
      className={cn(
        "flex items-baseline justify-between gap-3 border-b border-edge px-4 py-3",
        className,
      )}
    >
      {children}
    </div>
  );
}

// --- controls ----------------------------------------------------------------
//
// These exist because the same hundred-and-thirty character class string was
// pasted onto twenty-one inputs across eleven files. That is not a style
// complaint: it means the focus ring, the disabled state and the placeholder
// colour could only ever be changed in twenty-one places at once, and the three
// that had already drifted apart proved nobody was going to manage it.

const CONTROL =
  "h-8 w-full rounded-md border border-edge bg-sunken px-2.5 text-sm placeholder:text-ink-faint focus-visible:border-azir focus-visible:outline-none disabled:opacity-50";

export function TextInput({
  className,
  ...rest
}: React.InputHTMLAttributes<HTMLInputElement>) {
  return <input className={cn(CONTROL, className)} {...rest} />;
}

export function Picker({
  className,
  children,
  ...rest
}: React.SelectHTMLAttributes<HTMLSelectElement>) {
  return (
    <select className={cn(CONTROL, className)} {...rest}>
      {children}
    </select>
  );
}

export function TextArea({
  className,
  ...rest
}: React.TextareaHTMLAttributes<HTMLTextAreaElement>) {
  return (
    <textarea
      className={cn(
        "w-full resize-y rounded-md border border-edge bg-sunken px-2.5 py-2 text-sm placeholder:text-ink-faint focus-visible:border-azir focus-visible:outline-none disabled:opacity-50",
        className,
      )}
      {...rest}
    />
  );
}

/**
 * A button, in the three weights this product actually uses.
 *
 * `primary` commits to something, `standing` is the ordinary bordered action,
 * and `quiet` is for the ones that should be available without asking to be
 * noticed — Clear, Cancel, Reset.
 */
export function Button({
  weight = "standing",
  className,
  children,
  ...rest
}: React.ButtonHTMLAttributes<HTMLButtonElement> & {
  weight?: "primary" | "standing" | "quiet";
}) {
  const WEIGHT = {
    // Laid out the same way as "standing", which sits next to it constantly.
    // Without the flex, a primary button given an icon drew the icon and the
    // label on top of each other.
    primary:
      "flex h-8 items-center gap-1.5 rounded-md bg-azir px-3.5 text-sm font-medium text-azir-ink transition-opacity hover:opacity-90 disabled:opacity-50",
    standing:
      "flex h-8 items-center gap-1.5 rounded-md border border-edge bg-panel px-3 text-sm font-medium transition-colors hover:bg-sunken disabled:opacity-50",
    quiet:
      "rounded-md px-2 py-1 text-xs text-ink-dim transition-colors hover:bg-sunken hover:text-ink disabled:opacity-50",
  };
  return (
    <button className={cn(WEIGHT[weight], className)} {...rest}>
      {children}
    </button>
  );
}

// --- time --------------------------------------------------------------------

/**
 * Elapsed time, in the largest unit that is still honest.
 *
 * "3d" rather than "2 days, 21 hours": a queue is scanned, and the extra
 * precision would be read more slowly while changing no decision.
 */
export function since(iso?: string): string {
  if (!iso) return "—";
  const ms = Date.now() - new Date(iso).getTime();
  if (Number.isNaN(ms)) return "—";
  const mins = Math.floor(ms / 60000);
  if (mins < 1) return "just now";
  if (mins < 60) return `${mins}m`;
  const hours = Math.floor(mins / 60);
  if (hours < 24) return `${hours}h`;
  const days = Math.floor(hours / 24);
  if (days < 30) return `${days}d`;
  const months = Math.floor(days / 30);
  return months < 12 ? `${months}mo` : `${Math.floor(months / 12)}y`;
}

/**
 * Elapsed time as a phrase, so callers never have to append "ago" themselves.
 *
 * They did, and produced "just now ago" — because the largest-unit answer is
 * sometimes already a complete phrase.
 */
export function ago(iso?: string): string {
  const elapsed = since(iso);
  if (elapsed === "—") return "—";
  if (elapsed === "just now") return elapsed;
  return `${elapsed} ago`;
}

/**
 * Time still to run, for the things that expire rather than the things that
 * happened.
 *
 * `since` counts backwards and clamps anything in the future to "just now",
 * which read as "expires just now" on a capture with a fortnight left on it.
 */
export function until(iso?: string): string {
  if (!iso) return "—";
  const ms = new Date(iso).getTime() - Date.now();
  if (Number.isNaN(ms)) return "—";
  if (ms <= 0) return "any moment";
  const mins = Math.floor(ms / 60000);
  if (mins < 60) return `in ${mins}m`;
  const hours = Math.floor(mins / 60);
  if (hours < 24) return `in ${hours}h`;
  const days = Math.floor(hours / 24);
  return `in ${days}d`;
}

/** A dotted internal name as something to read: "tickets.search" → "Search tickets". */
export function actionTitle(name: string): string {
  const parts = name.split(".");
  if (parts.length === 2) {
    const [subject, verb] = parts;
    const readable = `${verb.replace(/[_-]/g, " ")} ${subject.replace(/[_-]/g, " ")}`;
    return readable.charAt(0).toUpperCase() + readable.slice(1);
  }
  const readable = name.replace(/[._-]/g, " ");
  return readable.charAt(0).toUpperCase() + readable.slice(1);
}

/** Whole and fractional days since a timestamp. Zero for anything unreadable. */
export function daysSince(iso?: string): number {
  if (!iso) return 0;
  const ms = Date.now() - new Date(iso).getTime();
  return Number.isNaN(ms) ? 0 : ms / 86_400_000;
}

export function absolute(iso?: string): string {
  if (!iso) return "";
  const d = new Date(iso);
  return Number.isNaN(d.getTime()) ? "" : d.toLocaleString();
}

/** Minutes as a duration someone would say out loud. */
export function duration(minutes?: number): string {
  if (!minutes || minutes <= 0) return "—";
  if (minutes < 60) return `${minutes}m`;
  const hours = Math.floor(minutes / 60);
  if (hours < 24) return minutes % 60 ? `${hours}h ${minutes % 60}m` : `${hours}h`;
  const days = Math.floor(hours / 24);
  return hours % 24 ? `${days}d ${hours % 24}h` : `${days}d`;
}

// --- status ------------------------------------------------------------------

/**
 * Where a status sits, without hardcoding one vendor's vocabulary.
 *
 * Statuses are whatever the connected system defines — Syncro lets an account
 * invent its own — so this reads intent from the words rather than matching a
 * fixed list, and falls back to neutral rather than guessing wrong.
 */
export function statusTone(status?: string): "good" | "warn" | "urgent" | "accent" | "" {
  const s = (status ?? "").toLowerCase();
  if (isDone(status)) return "good";
  if (/wait|hold|pending|parts/.test(s)) return "warn";
  if (/new|open|unassigned/.test(s)) return "accent";
  if (/escalat|urgent|breach/.test(s)) return "urgent";
  return "";
}

/**
 * Whether a status means nobody is working on this any more.
 *
 * The word is read for intent because the statuses are whatever the connected
 * system defines, and word boundaries are the whole trick: "Incomplete"
 * contains "complete" and means the opposite, and a live ticket vanishing out
 * of the queue is the worst way to find that out.
 *
 * The same rule lives in Go (internal/syncro/status.go, IsDone) because that is
 * where the filtering happens. The two have to agree — a ticket the server
 * excluded but the interface counts leaves a number that does not describe the
 * list beneath it.
 */
export function isDone(status?: string): boolean {
  return /\b(resolv|close|complete|done|cancel)/i.test(status ?? "");
}

/** Priority, normalised across the "0 Urgent" / "High" spellings in the wild. */
export function priorityRank(priority?: string): "urgent" | "high" | "normal" | "low" {
  const p = (priority ?? "").toLowerCase();
  if (/urgent|critical|p0|^0/.test(p)) return "urgent";
  if (/high|p1|^1/.test(p)) return "high";
  if (/low|p3|^3/.test(p)) return "low";
  return "normal";
}

/** The older tone words, mapped onto the four signals the interface draws. */
export function toSignal(tone: ReturnType<typeof statusTone>): Signal {
  if (tone === "urgent") return "critical";
  if (tone === "warn") return "attention";
  if (tone === "good") return "steady";
  return "idle";
}

export function prioritySignal(priority?: string): Signal {
  const rank = priorityRank(priority);
  if (rank === "urgent") return "critical";
  if (rank === "high") return "attention";
  if (rank === "low") return "idle";
  return "steady";
}

export function Chip({
  tone = "",
  children,
}: {
  tone?: "good" | "warn" | "urgent" | "accent" | "";
  children: ReactNode;
}) {
  // "accent" means new-and-untouched, which is a waiting state rather than a
  // healthy one, so it borrows the idle wash rather than the brand — the brand
  // is reserved for the assistant.
  const signal = tone === "accent" ? "idle" : toSignal(tone);
  return (
    <span
      className={cn(
        "inline-flex items-center rounded border px-1.5 py-px text-2xs font-medium whitespace-nowrap",
        WASH[signal],
      )}
    >
      {children}
    </span>
  );
}

// --- states ------------------------------------------------------------------

export function Empty({ headline, children }: { headline: string; children?: ReactNode }) {
  return (
    <div className="rounded-lg border border-dashed border-edge px-6 py-10 text-center">
      <div className="text-base font-medium">{headline}</div>
      {children && <div className="mt-1 text-sm text-ink-dim">{children}</div>}
    </div>
  );
}

export function Loading({ rows = 4 }: { rows?: number }) {
  return (
    <div className="flex flex-col gap-2" aria-busy="true" aria-label="Loading">
      {Array.from({ length: rows }, (_, i) => (
        <div key={i} className="h-14 animate-pulse rounded-lg bg-sunken" />
      ))}
    </div>
  );
}

export function Problem({ children }: { children: ReactNode }) {
  return (
    <p className="rounded-lg border border-critical/30 bg-critical/10 px-3.5 py-2.5 text-sm text-critical">
      {sentence(children)}
    </p>
  );
}

/**
 * Presents a message as a sentence.
 *
 * Errors reach here from several places — a plugin, a provider, the browser —
 * and not all of them were written to be read on their own. Capitalising and
 * closing them costs nothing and stops a perfectly good explanation from
 * looking like a fragment of a log line.
 */
export function sentence(value: ReactNode): ReactNode {
  if (typeof value !== "string") return value;
  const text = value.trim();
  if (!text) return text;
  const opened = text[0].toUpperCase() + text.slice(1);
  return /[.!?…]$/.test(opened) ? opened : opened + ".";
}

/**
 * Copies text, and says so.
 *
 * The assistant can draft a reply but never send one, so getting its words into
 * the PSA is a step the person has to take — and a step they take often enough
 * that hunting for it with a mouse is worth removing.
 */
export function CopyButton({
  text,
  label = "Copy",
  className,
}: {
  text: string;
  label?: string;
  className?: string;
}) {
  const [copied, setCopied] = useState(false);

  useEffect(() => {
    if (!copied) return;
    const timer = window.setTimeout(() => setCopied(false), 1800);
    return () => window.clearTimeout(timer);
  }, [copied]);

  async function copy() {
    try {
      await navigator.clipboard.writeText(text);
      setCopied(true);
    } catch {
      // The clipboard is refused without a secure context or a user gesture.
      // Selecting the text is the honest fallback rather than a silent no-op.
      setCopied(false);
    }
  }

  return (
    <button
      type="button"
      className={cn(
        "inline-flex items-center gap-1.5 rounded-md px-2 py-1 text-xs text-ink-dim",
        "transition-colors hover:bg-sunken hover:text-ink",
        className,
      )}
      onClick={() => void copy()}
      aria-label={copied ? "Copied" : label}
    >
      {copied ? <Icon.tick /> : <Icon.copy />}
      {copied ? "Copied" : label}
    </button>
  );
}

/** Initials for an avatar, from whatever name or address is available. */
export function initials(name: string): string {
  // Letters only. Splitting on whitespace alone let punctuation through, so an
  // account called "Claude (verification)" wore a badge reading "C(".
  const parts = name
    .replace(/@.*/, "")
    .split(/[^\p{L}\p{N}]+/u)
    .filter(Boolean);
  if (parts.length === 0) return "?";
  if (parts.length === 1) return parts[0].slice(0, 2).toUpperCase();
  return (parts[0][0] + parts[parts.length - 1][0]).toUpperCase();
}

/**
 * A role's name, as a person reads it.
 *
 * The stored name is an identifier — it is a foreign key, it travels in the
 * API, and lowercasing it is what keeps "Admin" and "admin" from becoming two
 * roles. So the capital letter is put on at the last moment, here, rather than
 * in the database where it would have to be got right forever.
 *
 * Hyphens and underscores become spaces so a role somebody adds later reads as
 * words rather than as a slug.
 */
export function roleLabel(name: string): string {
  return name
    .split(/[-_\s]+/)
    .filter(Boolean)
    .map((word) => word.charAt(0).toUpperCase() + word.slice(1))
    .join(" ");
}
