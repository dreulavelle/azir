import type { ReactNode } from "react";
import defaultSplash from "./assets/login-splash.webp";

/**
 * The sign-in screen's chrome, without the forms in it.
 *
 * Extracted because two things draw this screen: the screen itself, and the
 * preview on the branding page that shows an administrator what their colour
 * and their name will look like before they commit to either. Those were two
 * separate pieces of markup for about an hour, which is the shape of bug this
 * repository keeps finding — two lists that must agree, with nothing checking.
 * The preview drifted from the real page within the same afternoon it was
 * written.
 *
 * So there is one implementation and the preview renders it at a smaller
 * scale. Nothing here knows which of the two it is in.
 *
 * Colour comes from --azir rather than from a prop, so the preview can put a
 * draft accent on a wrapper and have every gradient, glow and button inside
 * follow it without a single value being threaded through.
 */
export function SignInShell({
  mark,
  tagline,
  eyebrow,
  heading,
  note,
  children,
  fill = true,
  splash = defaultSplash,
}: {
  /** The brand tile. A node, because the preview draws a draft of it. */
  mark: ReactNode;
  tagline: string;
  eyebrow: string;
  heading: string;
  /** Optional supporting line under the heading. */
  note?: ReactNode;
  /** The form, or something shaped like one. */
  children: ReactNode;
  /** False when the caller has already decided the height. */
  fill?: boolean;
  /** The picture. Defaults to the one shipped with this version. */
  splash?: string;
}) {
  return (
    <div
      className={`relative grid place-items-center overflow-hidden bg-ground p-4 sm:p-8 ${
        fill ? "min-h-screen" : "h-full"
      }`}
    >
      {/* Two washes behind the card, both drawn from the accent so they follow
          whatever a reseller sets.

          The first is the pool the card sits in. The second is a low bloom off
          the bottom edge, which is what stops the lower half going flat — a
          single centred gradient leaves the bottom corners dead and the card
          looks pasted onto them. */}
      <div
        className="pointer-events-none absolute inset-0"
        style={{
          background:
            "radial-gradient(58% 46% at 50% 40%, color-mix(in srgb, var(--azir) 20%, transparent), transparent 72%)",
        }}
        aria-hidden="true"
      />
      <div
        className="pointer-events-none absolute inset-0"
        style={{
          background:
            "radial-gradient(120% 60% at 50% 106%, color-mix(in srgb, var(--azir) 12%, transparent), transparent 62%)",
        }}
        aria-hidden="true"
      />

      {/*
        One card, holding a picture and a form.

        It floats on the ground rather than filling the viewport, which is what
        makes it read as an object somebody designed instead of a page split
        down the middle. The picture is inset inside the card's own padding, so
        the card frames it — that inset is doing more than it looks like,
        because without it the image runs into the corner radius and the whole
        thing goes back to being two panels.
      */}
      <div
        className="relative grid w-full max-w-5xl overflow-hidden rounded-2xl bg-panel p-2 ring-1 ring-edge lg:grid-cols-2"
        style={{
          // Elevation from the theme's own token, so this is as heavy in the
          // light theme as everything else and no heavier, plus an accent glow
          // wide enough to read as the card being lit rather than outlined.
          boxShadow:
            "var(--elev-3), 0 0 110px -28px color-mix(in srgb, var(--azir) 50%, transparent), inset 0 1px 0 0 color-mix(in srgb, white 7%, transparent)",
        }}
      >
        <div className="relative h-44 overflow-hidden rounded-xl sm:h-56 lg:h-auto lg:min-h-[34rem]">
          <img
            src={splash}
            alt=""
            className="absolute inset-0 h-full w-full object-cover"
            // First screen anybody loads, and the largest thing on it.
            fetchPriority="high"
            decoding="async"
          />
          {/* Type over a photograph needs a ground of its own, or it depends on
              whatever happens to be behind it. */}
          <div
            className="absolute inset-0"
            style={{
              background:
                "linear-gradient(to top, rgb(0 0 0 / 0.88) 0%, rgb(0 0 0 / 0.45) 34%, rgb(0 0 0 / 0.05) 62%, transparent 100%)",
            }}
            aria-hidden="true"
          />
          {/* The tagline is the headline. It is a line a reseller can already
              change, so nothing here is copy they would be stuck with. */}
          <p className="absolute inset-x-0 bottom-0 hidden p-6 text-lg font-medium leading-snug tracking-[-0.01em] text-white font-display sm:block lg:p-7 lg:text-xl">
            {tagline}
          </p>
        </div>

        <div className="flex flex-col justify-center px-6 py-10 sm:px-10 lg:px-12">
          <div className="mx-auto w-full max-w-[20rem]">
            {/* The mark sits on the panel rather than on the picture.

                It is a rounded tile with a ground of its own — an uploaded logo,
                or the accent behind an initial — so on a dark photograph it is a
                dark square on a dark square and disappears. Here it has room to
                be large enough to be the first thing seen. */}
            <div className="mb-6">{mark}</div>

            <p className="font-mono text-2xs font-medium uppercase tracking-[0.14em] text-ink-faint">
              {eyebrow}
            </p>
            <h1 className="mt-2 text-2xl font-semibold tracking-[-0.02em] font-display">
              {heading}
            </h1>
            {note && <p className="mt-2 text-sm leading-relaxed text-ink-dim">{note}</p>}

            <div className="mt-7">{children}</div>
          </div>
        </div>
      </div>
    </div>
  );
}
