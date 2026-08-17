import { useEffect, useRef, useState } from "react";
import { cn } from "@/lib/cn";
import { Explain } from "./components";
import { Mark, readableOn, useBranding, type Branding } from "./branding";
import { SignInScene } from "./scene";
import { useToast } from "./Toast";
import { Label } from "./ui";

/**
 * Making it somebody else's product.
 *
 * Everything here is optional and everything falls back, so a deployment that
 * never opens this page still looks finished. What it changes reaches the
 * corner of every screen, the browser tab, the icon and the sign-in page at
 * once — a rename that only got half the surfaces would be worse than none.
 */
export function BrandingSettings() {
  const { brand, reload } = useBranding();
  const [draft, setDraft] = useState<Partial<Branding>>({});
  const [busy, setBusy] = useState(false);
  const file = useRef<HTMLInputElement>(null);
  const toast = useToast();

  useEffect(() => {
    setDraft({
      name: brand.name,
      tagline: brand.tagline,
      mark: brand.mark,
      accent: brand.accent,
    });
  }, [brand.name, brand.tagline, brand.mark, brand.accent]);

  const set = (patch: Partial<Branding>) => setDraft((d) => ({ ...d, ...patch }));

  async function save() {
    setBusy(true);
    try {
      const res = await fetch("/api/branding", {
        method: "PUT",
        headers: { "content-type": "application/json" },
        body: JSON.stringify(draft),
      });
      if (!res.ok) throw new Error((await res.json()).error ?? "Could not save");
      await reload();
      toast("Saved");
    } catch (e) {
      toast("Could not save", { tone: "bad", detail: e instanceof Error ? e.message : undefined });
    } finally {
      setBusy(false);
    }
  }

  async function upload(chosen: File | null) {
    setBusy(true);
    try {
      const res = await fetch("/api/branding/logo", {
        method: "PUT",
        headers: { "content-type": chosen ? chosen.type : "text/plain" },
        body: chosen ?? new Blob([]),
      });
      if (!res.ok) throw new Error((await res.json()).error ?? "Could not upload");
      await reload();
      toast(chosen ? "Logo updated" : "Logo removed");
    } catch (e) {
      toast("Could not upload that", {
        tone: "bad",
        detail: e instanceof Error ? e.message : undefined,
      });
    } finally {
      setBusy(false);
      if (file.current) file.current.value = "";
    }
  }

  const accent = draft.accent?.trim() || brand.effective_accent;

  return (
    <section className="rounded-lg border border-edge bg-panel shadow-e1">
      <div className="border-b border-edge px-4 py-3">
        <h2 className="text-sm font-semibold">Branding</h2>
      </div>

      <div className="grid gap-6 p-4 lg:grid-cols-[minmax(0,420px)_minmax(0,1fr)]">
        <div className="flex flex-col gap-5">
          <p className="max-w-[62ch] text-xs text-ink-dim">
            What this deployment calls itself. Anything you leave blank keeps
            what Azir ships, so a future version that improves a default still
            reaches you.
          </p>

          <div className="flex flex-col gap-1.5">
            <label htmlFor="brand-name" className="text-sm font-medium">
              Name
            </label>
            <input
              id="brand-name"
              className="h-8 w-full rounded-md border border-edge bg-sunken px-2.5 text-sm placeholder:text-ink-faint focus-visible:border-azir focus-visible:outline-none"
              maxLength={40}
              placeholder={brand.effective_name}
              value={draft.name ?? ""}
              onChange={(e) => set({ name: e.target.value })}
            />
          </div>

          <div className="flex flex-col gap-1.5">
            <label htmlFor="brand-tagline" className="text-sm font-medium">
              Line under the name
            </label>
            <input
              id="brand-tagline"
              className="h-8 w-full rounded-md border border-edge bg-sunken px-2.5 text-sm placeholder:text-ink-faint focus-visible:border-azir focus-visible:outline-none"
              maxLength={120}
              placeholder={brand.effective_tagline}
              value={draft.tagline ?? ""}
              onChange={(e) => set({ tagline: e.target.value })}
            />
          </div>

          <div className="grid gap-4 sm:grid-cols-2">
            <div className="flex flex-col gap-1.5">
              <label htmlFor="brand-mark" className="flex items-center gap-2 text-sm font-medium">
                Mark
                <Explain>
                  One or two characters for the square icon, used when there is
                  no logo. Left blank it follows the name.
                </Explain>
              </label>
              <input
                id="brand-mark"
                className="h-8 w-full rounded-md border border-edge bg-sunken px-2.5 text-center font-mono text-sm focus-visible:border-azir focus-visible:outline-none"
                maxLength={2}
                placeholder={brand.effective_mark}
                value={draft.mark ?? ""}
                onChange={(e) => set({ mark: e.target.value })}
              />
            </div>

            <div className="flex flex-col gap-1.5">
              <label htmlFor="brand-accent" className="flex items-center gap-2 text-sm font-medium">
                Colour
                <Explain>
                  Used for the mark, the assistant and focus. Red, amber and
                  green are not brandable — they mean something is wrong, and
                  that is the product working rather than decoration.
                </Explain>
              </label>
              <div className="flex items-center gap-2">
                <input
                  type="color"
                  className="size-8 shrink-0 cursor-pointer rounded-md border border-edge bg-sunken"
                  value={accent}
                  aria-label="Pick a colour"
                  onChange={(e) => set({ accent: e.target.value })}
                />
                <input
                  id="brand-accent"
                  className="h-8 w-full rounded-md border border-edge bg-sunken px-2.5 font-mono text-xs focus-visible:border-azir focus-visible:outline-none"
                  placeholder={brand.effective_accent}
                  value={draft.accent ?? ""}
                  onChange={(e) => set({ accent: e.target.value })}
                />
              </div>
            </div>
          </div>

          <div className="flex flex-col gap-1.5">
            <span className="flex items-center gap-2 text-sm font-medium">
              Logo
              <Explain>
                PNG, JPEG, WebP or GIF, up to 512KB. It is drawn at about 40
                pixels. Stored here rather than linked, so no page load reaches
                out to another server. SVG is not accepted because it can carry
                code.
              </Explain>
            </span>
            <div className="flex items-center gap-3">
              <Mark size={40} />
              <input
                ref={file}
                type="file"
                accept="image/png,image/jpeg,image/webp,image/gif"
                className="text-xs file:mr-2 file:rounded-md file:border file:border-edge file:bg-sunken file:px-2.5 file:py-1 file:text-xs file:text-ink"
                onChange={(e) => void upload(e.target.files?.[0] ?? null)}
                disabled={busy}
              />
              {brand.has_logo && (
                <button
                  className="rounded-md px-2 py-1 text-xs text-ink-dim transition-colors hover:bg-critical/10 hover:text-critical"
                  disabled={busy}
                  onClick={() => void upload(null)}
                >
                  Remove
                </button>
              )}
            </div>
          </div>

          <div>
            <button
              className="h-8 rounded-md bg-azir px-3.5 text-sm font-medium text-azir-ink transition-opacity hover:opacity-90 disabled:opacity-50"
              disabled={busy}
              onClick={() => void save()}
            >
              {busy ? "Saving…" : "Save"}
            </button>
          </div>
        </div>

        {/* The sign-in page as it will actually look, because that is the
            screen this changes most and the one nobody signed in ever sees. */}
        <div className="flex flex-col gap-2">
          <Label>How the sign-in page will look</Label>
          <div className="relative aspect-[4/3] overflow-hidden rounded-lg border border-edge bg-ground">
            <SignInScene accent={accent} />
            <div
              className="pointer-events-none absolute inset-0"
              style={{
                background:
                  "radial-gradient(46% 40% at 50% 45%, color-mix(in srgb, var(--ground) 78%, transparent), color-mix(in srgb, var(--ground) 24%, transparent))",
              }}
            />
            <div className="absolute inset-0 grid place-items-center p-6">
              <div className="w-full max-w-[240px] rounded-xl border border-edge bg-panel/95 p-5 text-center shadow-e3">
                <span
                  className="mx-auto mb-3 grid size-9 place-items-center rounded-lg font-mono text-sm font-semibold"
                  style={{ background: accent, color: readableOn(accent) }}
                >
                  {draft.mark?.trim() ||
                    (draft.name?.trim()?.[0]?.toUpperCase() ?? brand.effective_mark)}
                </span>
                <div className="text-sm font-semibold tracking-tight">
                  {draft.name?.trim() || brand.effective_name}
                </div>
                <div className="mt-0.5 text-2xs text-ink-dim">
                  {draft.tagline?.trim() || brand.effective_tagline}
                </div>
                <div
                  className={cn("mt-3 h-7 rounded-md text-2xs font-medium leading-7")}
                  style={{ background: accent, color: readableOn(accent) }}
                >
                  Sign in
                </div>
              </div>
            </div>
          </div>
        </div>
      </div>
    </section>
  );
}
