import { cn } from "@/lib/cn";
import * as RSwitch from "@radix-ui/react-switch";
import * as RTooltip from "@radix-ui/react-tooltip";
import * as RTabs from "@radix-ui/react-tabs";
import * as RSelect from "@radix-ui/react-select";
import * as RDialog from "@radix-ui/react-dialog";
import * as RCollapsible from "@radix-ui/react-collapsible";
import type { ReactNode } from "react";

/**
 * The component layer.
 *
 * Radix supplies behaviour — focus management, keyboard handling, portalling,
 * collision detection, the ARIA wiring — and everything visual comes from our
 * own tokens. Hand-rolled versions of these are where an interface starts to
 * feel janky: a tooltip clipped by an overflow container, a dropdown that
 * cannot be driven from the keyboard, a switch that is really a checkbox with
 * a background image.
 */

// --- tooltip -----------------------------------------------------------------

/** Wraps the app once; tooltips inside share timing and behave as a group. */
export function TooltipLayer({ children }: { children: ReactNode }) {
  return (
    <RTooltip.Provider delayDuration={280} skipDelayDuration={400}>
      {children}
    </RTooltip.Provider>
  );
}

export function Tooltip({
  content,
  children,
  side = "top",
}: {
  content: ReactNode;
  children: ReactNode;
  side?: "top" | "bottom" | "left" | "right";
}) {
  if (!content) return <>{children}</>;
  return (
    <RTooltip.Root>
      <RTooltip.Trigger asChild>{children}</RTooltip.Trigger>
      <RTooltip.Portal>
        <RTooltip.Content className="z-50 max-w-72 rounded-md border border-edge bg-raised px-2.5 py-1.5 text-xs text-ink shadow-e3" side={side} sideOffset={6} collisionPadding={10}>
          {content}
          <RTooltip.Arrow className="fill-raised" width={10} height={5} />
        </RTooltip.Content>
      </RTooltip.Portal>
    </RTooltip.Root>
  );
}

/** A small "what is this?" marker, for where a label alone leaves a question. */
export function Explain({ children, side }: { children: ReactNode; side?: "top" | "bottom" | "left" | "right" }) {
  return (
    <Tooltip content={children} side={side}>
      <button type="button" className="grid size-4 place-items-center rounded-full border border-edge font-mono text-[9px] text-ink-faint transition-colors hover:border-ink-faint hover:text-ink" aria-label="What does this mean?">
        ?
      </button>
    </Tooltip>
  );
}

// --- switch ------------------------------------------------------------------

/**
 * A switch, for a setting that takes effect the moment it moves.
 *
 * Distinct from a checkbox on purpose: a checkbox states an intention that a
 * Save button later commits, and these commit immediately.
 */
export function Switch({
  checked,
  onChange,
  disabled,
  label,
  tone = "accent",
}: {
  checked: boolean;
  onChange: (next: boolean) => void;
  disabled?: boolean;
  label: string;
  tone?: "accent" | "urgent";
}) {
  return (
    <RSwitch.Root
      className={cn(
        "relative h-[18px] w-8 shrink-0 rounded-full border transition-colors disabled:cursor-not-allowed disabled:opacity-50",
        tone === "urgent"
          ? "border-edge bg-sunken data-[state=checked]:border-critical data-[state=checked]:bg-critical"
          : "border-edge bg-sunken data-[state=checked]:border-steady data-[state=checked]:bg-steady",
      )}
      checked={checked}
      onCheckedChange={onChange}
      disabled={disabled}
      aria-label={label}
    >
      <RSwitch.Thumb className="block size-3 translate-x-[2px] rounded-full bg-ink-faint transition-transform data-[state=checked]:translate-x-[16px] data-[state=checked]:bg-white" />
    </RSwitch.Root>
  );
}

// --- tabs --------------------------------------------------------------------

export function Tabs({
  value,
  onChange,
  tabs,
}: {
  value: string;
  onChange: (next: string) => void;
  tabs: { id: string; label: string; count?: number }[];
}) {
  return (
    <RTabs.Root value={value} onValueChange={onChange}>
      <RTabs.List className="mb-6 flex flex-wrap gap-1 border-b border-edge">
        {tabs.map((t) => (
          <RTabs.Trigger key={t.id} value={t.id} className="-mb-px flex items-center gap-1.5 border-b-2 border-transparent px-3 pb-2.5 pt-1 text-sm font-medium text-ink-dim transition-colors hover:text-ink data-[state=active]:border-ink data-[state=active]:text-ink">
            {t.label}
            {t.count !== undefined && <span className="font-mono text-2xs tabular-nums text-ink-faint">{t.count}</span>}
          </RTabs.Trigger>
        ))}
      </RTabs.List>
    </RTabs.Root>
  );
}

// --- select ------------------------------------------------------------------

export function Select({
  value,
  onChange,
  options,
  placeholder = "Select…",
  label,
  width,
}: {
  value: string;
  onChange: (next: string) => void;
  options: { value: string; label: string }[];
  placeholder?: string;
  label: string;
  width?: number | string;
}) {
  return (
    <RSelect.Root value={value} onValueChange={onChange}>
      <RSelect.Trigger className="flex h-8 items-center justify-between gap-2 rounded-md border border-edge bg-sunken px-2.5 text-sm focus-visible:border-azir focus-visible:outline-none" aria-label={label} style={{ width }}>
        <RSelect.Value placeholder={placeholder} />
        <RSelect.Icon className="text-ink-faint">
          <Chevron />
        </RSelect.Icon>
      </RSelect.Trigger>
      <RSelect.Portal>
        <RSelect.Content className="z-50 overflow-hidden rounded-md border border-edge bg-raised p-1 shadow-e3" position="popper" sideOffset={5}>
          <RSelect.Viewport>
            {options.map((o) => (
              <RSelect.Item key={o.value} value={o.value} className="flex cursor-pointer items-center gap-2 rounded px-2 py-1.5 text-sm outline-none data-[highlighted]:bg-sunken">
                <RSelect.ItemText>{o.label}</RSelect.ItemText>
                <RSelect.ItemIndicator className="text-steady">
                  <Tick />
                </RSelect.ItemIndicator>
              </RSelect.Item>
            ))}
          </RSelect.Viewport>
        </RSelect.Content>
      </RSelect.Portal>
    </RSelect.Root>
  );
}

// --- dialog ------------------------------------------------------------------

export function Dialog({
  open,
  onOpenChange,
  title,
  description,
  children,
  footer,
  returnFocusTo,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  title: string;
  description?: ReactNode;
  children?: ReactNode;
  footer?: ReactNode;
  /**
   * A CSS selector for where focus should go when this closes.
   *
   * Radix returns focus to whatever opened the dialog, which is right when a
   * button did. A dialog opened by a keyboard shortcut has no such element, so
   * focus falls back to the body and the next Tab starts from the very top of
   * the page. A selector rather than a ref because the control is often wrapped
   * in a tooltip, and passing a ref through that is fragile in a way that fails
   * silently.
   */
  returnFocusTo?: string;
}) {
  return (
    <RDialog.Root open={open} onOpenChange={onOpenChange}>
      <RDialog.Portal>
        <RDialog.Overlay className="fixed inset-0 z-50 bg-black/50 backdrop-blur-[2px]" />
        <RDialog.Content
          className="fixed left-1/2 top-1/2 z-50 w-[min(92vw,480px)] -translate-x-1/2 -translate-y-1/2 rounded-xl border border-edge bg-raised p-5 shadow-e3"
          onCloseAutoFocus={(event) => {
            if (!returnFocusTo) return;
            const target = document.querySelector<HTMLElement>(returnFocusTo);
            if (!target) return;
            event.preventDefault();
            target.focus();
          }}
        >
          <RDialog.Title className="text-base font-semibold">{title}</RDialog.Title>
          {description && (
            <RDialog.Description className="mt-1.5 text-sm text-ink-dim">{description}</RDialog.Description>
          )}
          {children}
          {footer && <div className="mt-5 flex justify-end gap-2">{footer}</div>}
        </RDialog.Content>
      </RDialog.Portal>
    </RDialog.Root>
  );
}

// --- sheet -------------------------------------------------------------------

/**
 * A panel that slides in from the right.
 *
 * A dialog in the middle of the screen is right for a question and wrong for a
 * form: it covers what you were looking at, and editing an extension is work
 * you do while reading the list you picked it from. This keeps the list
 * visible and gives the form a full column of height, which a centred dialog
 * cannot without becoming a page of its own.
 */
export function Sheet({
  open,
  onOpenChange,
  title,
  description,
  children,
  footer,
  wide,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  title: ReactNode;
  description?: ReactNode;
  children?: ReactNode;
  footer?: ReactNode;
  /** For a form with two columns rather than one. */
  wide?: boolean;
}) {
  return (
    <RDialog.Root open={open} onOpenChange={onOpenChange}>
      <RDialog.Portal>
        <RDialog.Overlay className="fixed inset-0 z-40 bg-black/40 backdrop-blur-[1px]" />
        <RDialog.Content
          className={cn(
            "fixed inset-y-0 right-0 z-50 flex w-full flex-col border-l border-edge bg-raised shadow-e3",
            wide ? "max-w-[720px]" : "max-w-[520px]",
          )}
        >
          <div className="flex items-start justify-between gap-3 border-b border-edge px-5 py-4">
            <div className="min-w-0">
              <RDialog.Title className="text-base font-semibold">{title}</RDialog.Title>
              {description && (
                <RDialog.Description className="mt-0.5 text-xs text-ink-dim">
                  {description}
                </RDialog.Description>
              )}
            </div>
            <RDialog.Close
              className="shrink-0 rounded-md px-2 py-1 text-sm text-ink-faint transition-colors hover:bg-sunken hover:text-ink"
              aria-label="Close"
            >
              ✕
            </RDialog.Close>
          </div>

          <div className="min-h-0 flex-1 overflow-y-auto px-5 py-4">{children}</div>

          {footer && (
            <div className="flex items-center justify-between gap-3 border-t border-edge px-5 py-3">
              {footer}
            </div>
          )}
        </RDialog.Content>
      </RDialog.Portal>
    </RDialog.Root>
  );
}

// --- collapsible -------------------------------------------------------------

export function Collapsible({
  open,
  onOpenChange,
  trigger,
  children,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  trigger: ReactNode;
  children: ReactNode;
}) {
  return (
    <RCollapsible.Root open={open} onOpenChange={onOpenChange}>
      <RCollapsible.Trigger asChild>{trigger}</RCollapsible.Trigger>
      <RCollapsible.Content className="overflow-hidden data-[state=closed]:animate-[accordion-up_160ms_ease-out] data-[state=open]:animate-[accordion-down_160ms_ease-out]">{children}</RCollapsible.Content>
    </RCollapsible.Root>
  );
}

// --- glyphs used by the controls above ---------------------------------------

function Chevron() {
  return (
    <svg width="12" height="12" viewBox="0 0 20 20" aria-hidden="true">
      <path
        d="M5.5 8L10 12.5 14.5 8"
        fill="none"
        stroke="currentColor"
        strokeWidth="1.7"
        strokeLinecap="round"
        strokeLinejoin="round"
      />
    </svg>
  );
}

function Tick() {
  return (
    <svg width="13" height="13" viewBox="0 0 20 20" aria-hidden="true">
      <path
        d="M4.5 10.5l3.5 3.5 7.5-8"
        fill="none"
        stroke="currentColor"
        strokeWidth="1.9"
        strokeLinecap="round"
        strokeLinejoin="round"
      />
    </svg>
  );
}
