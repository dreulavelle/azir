import { useEffect, useRef, useState } from "react";
import { Explain } from "./components";
import { Mark, readableOn, useBranding, type Branding } from "./branding";
import { SignInShell } from "./SignInShell";
import defaultSplash from "./assets/login-splash.webp";
import { toWebp } from "./images";
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
  const splashFile = useRef<HTMLInputElement>(null);
  const [stamp, setStamp] = useState(0);
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

  async function upload(what: "logo" | "splash", chosen: File | null) {
    setBusy(true);
    try {
      // Re-encoded before it is sent. A logo is drawn at about 40 pixels and a
      // picture fills half a card, so neither needs the dimensions a design
      // tool exports at, and WebP is a fraction of the size at both.
      const ready = chosen
        ? await toWebp(chosen, what === "logo" ? 512 : 1600)
        : null;

      const res = await fetch(`/api/branding/${what}`, {
        method: "PUT",
        headers: { "content-type": ready ? ready.type : "text/plain" },
        body: ready ? ready.blob : new Blob([]),
      });
      if (!res.ok) throw new Error((await res.json()).error ?? "Could not upload");
      await reload();
      // Cache-busting the preview: the picture is served from a fixed URL with
      // an entity tag, so without this the browser shows the previous one
      // until something else makes it revalidate.
      setStamp(Date.now());
      const noun = what === "logo" ? "Logo" : "Picture";
      toast(chosen ? `${noun} updated` : `${noun} removed`);
    } catch (e) {
      toast("Could not upload that", {
        tone: "bad",
        detail: e instanceof Error ? e.message : undefined,
      });
    } finally {
      setBusy(false);
      if (file.current) file.current.value = "";
      if (splashFile.current) splashFile.current.value = "";
    }
  }

  const accent = draft.accent?.trim() || brand.effective_accent;
  const splashSrc = brand.has_splash
    ? `/api/branding/splash?v=${stamp}`
    : defaultSplash;

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
                PNG, JPEG, SVG, WebP or GIF. Converted to WebP and scaled down
                here in the browser before it is sent, so what gets stored is a
                fraction of what you picked. Stored rather than linked, so no
                page load reaches out to another server. An SVG is rasterised on
                the way — nothing keeps its markup, because markup served back
                to a browser can carry code.
              </Explain>
            </span>
            <div className="flex items-center gap-3">
              <Mark size={40} />
              <input
                ref={file}
                type="file"
                accept="image/png,image/jpeg,image/webp,image/gif,image/svg+xml"
                className="text-xs file:mr-2 file:rounded-md file:border file:border-edge file:bg-sunken file:px-2.5 file:py-1 file:text-xs file:text-ink"
                onChange={(e) => void upload("logo", e.target.files?.[0] ?? null)}
                disabled={busy}
              />
              {brand.has_logo && (
                <button
                  className="rounded-md px-2 py-1 text-xs text-ink-dim transition-colors hover:bg-critical/10 hover:text-critical"
                  disabled={busy}
                  onClick={() => void upload("logo", null)}
                >
                  Remove
                </button>
              )}
            </div>
          </div>

          <div className="flex flex-col gap-1.5">
            <span className="flex items-center gap-2 text-sm font-medium">
              Sign-in picture
              <Explain>
                PNG, JPEG, SVG, WebP or GIF. It fills half the sign-in card, so
                something tall looks best. Converted to WebP and scaled to 1600
                pixels here before it is sent. Leave it empty to keep the one
                Azir ships.
              </Explain>
            </span>
            <div className="flex items-center gap-3">
              <div className="h-10 w-8 shrink-0 overflow-hidden rounded ring-1 ring-edge">
                <img src={splashSrc} alt="" className="h-full w-full object-cover" />
              </div>
              <input
                ref={splashFile}
                type="file"
                accept="image/png,image/jpeg,image/webp,image/gif,image/svg+xml"
                className="text-xs file:mr-2 file:rounded-md file:border file:border-edge file:bg-sunken file:px-2.5 file:py-1 file:text-xs file:text-ink"
                onChange={(e) => void upload("splash", e.target.files?.[0] ?? null)}
                disabled={busy}
              />
              {brand.has_splash && (
                <button
                  className="rounded-md px-2 py-1 text-xs text-ink-dim transition-colors hover:bg-critical/10 hover:text-critical"
                  disabled={busy}
                  onClick={() => void upload("splash", null)}
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
          {/*
            The real screen, at a smaller size.

            Not a drawing of it. This renders the same component the sign-in
            page renders, at its natural width, scaled down to whatever room
            the settings column has. A hand-built miniature would be a second
            copy of the layout that somebody has to remember to update, and it
            drifted from the real page within an afternoon of being written.

            The draft accent is put on the wrapper as --azir, so every gradient,
            glow and button inside picks it up without a value being threaded
            through any of them.
          */}
          <ScaledPreview
            style={{
              ["--azir" as string]: accent,
              ["--azir-ink" as string]: readableOn(accent),
            }}
          >
            <SignInShell
              fill={false}
              splash={splashSrc}
              tagline={draft.tagline?.trim() || brand.effective_tagline}
              eyebrow="Sign in"
              heading={`Sign in to ${draft.name?.trim() || brand.effective_name}`}
              mark={
                <span
                  className="grid size-[52px] place-items-center rounded-xl font-mono text-2xl font-semibold shadow-e2"
                  style={{ background: accent, color: readableOn(accent) }}
                >
                  {draft.mark?.trim() ||
                    (draft.name?.trim()?.[0]?.toUpperCase() ?? brand.effective_mark)}
                </span>
              }
            >
              {/* Inert, but the same shapes and spacing the real form has, so
                  the proportions in the preview are the proportions shipped. */}
              <div className="flex flex-col gap-3">
                <div className="text-sm font-medium">Email</div>
                <div className="h-9 rounded-md bg-sunken ring-1 ring-edge" />
                <div className="text-sm font-medium">Password</div>
                <div className="h-9 rounded-md bg-sunken ring-1 ring-edge" />
                <div
                  className="mt-1 h-9 rounded-md text-center text-sm font-medium leading-9"
                  style={{ background: accent, color: readableOn(accent) }}
                >
                  Sign in
                </div>
              </div>
            </SignInShell>
          </ScaledPreview>
        </div>
      </div>
    </section>
  );
}

/**
 * Renders children at a fixed natural size and scales them to fit the width
 * available, so a preview is the real thing seen from further away rather than
 * a smaller thing built to look like it.
 */
function ScaledPreview({
  children,
  style,
}: {
  children: React.ReactNode;
  style?: React.CSSProperties;
}) {
  const box = useRef<HTMLDivElement>(null);
  const [scale, setScale] = useState(0.3);

  useEffect(() => {
    const node = box.current;
    if (!node) return;
    const fit = () => setScale(node.clientWidth / NATURAL_W);
    fit();
    // The settings column changes width with the window and with the assistant
    // panel opening beside it, so this cannot be measured once.
    const observer = new ResizeObserver(fit);
    observer.observe(node);
    return () => observer.disconnect();
  }, []);

  return (
    <div
      ref={box}
      className="relative overflow-hidden rounded-lg border border-edge bg-ground"
      style={{ height: NATURAL_H * scale }}
    >
      <div
        className="absolute left-0 top-0 origin-top-left"
        style={{ width: NATURAL_W, height: NATURAL_H, transform: `scale(${scale})`, ...style }}
        // Decorative, and its controls are not real ones.
        aria-hidden="true"
      >
        {children}
      </div>
    </div>
  );
}

/** The size the preview is drawn at before being scaled down. A desktop shape,
 *  because that is what the card is laid out for. */
const NATURAL_W = 1180;
const NATURAL_H = 740;
