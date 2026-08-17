import { createContext, useContext, useEffect, useState, type ReactNode } from "react";

/**
 * Whose product this is.
 *
 * Azir is a name, not the product. An MSP reselling this should be able to put
 * their own on it, and that has to reach everywhere at once — the corner, the
 * browser tab, the sign-in page — or the seams show and the visitor learns
 * whose software it really is.
 *
 * Fetched before anything else renders, and before sign-in: the first screen
 * anybody sees is the one that most needs to be right.
 */

export type Branding = {
  name: string;
  tagline: string;
  mark: string;
  accent: string;
  has_logo: boolean;
  effective_name: string;
  effective_mark: string;
  effective_tagline: string;
  effective_accent: string;
};

/** What to draw before the answer arrives, so nothing flashes a wrong name. */
const unknown: Branding = {
  name: "",
  tagline: "",
  mark: "",
  accent: "",
  has_logo: false,
  effective_name: "",
  effective_mark: "",
  effective_tagline: "",
  effective_accent: "",
};

const BrandContext = createContext<{ brand: Branding; reload: () => Promise<void> }>({
  brand: unknown,
  reload: async () => {},
});

export function useBranding() {
  return useContext(BrandContext);
}

export function BrandingProvider({ children }: { children: ReactNode }) {
  const [brand, setBrand] = useState<Branding>(unknown);

  const reload = async () => {
    try {
      const res = await fetch("/api/branding");
      if (res.ok) setBrand(await res.json());
    } catch {
      // An unreachable server has bigger problems than its own name.
    }
  };

  useEffect(() => {
    void reload();
  }, []);

  // The accent is a token, so renaming the product recolours every place that
  // reads it — the mark, the assistant, focus rings — without touching any of
  // them. Signal colours are deliberately not brandable: red means something is
  // wrong, and that is the product working rather than decoration.
  useEffect(() => {
    if (!brand.effective_accent) return;
    document.documentElement.style.setProperty("--azir", brand.effective_accent);
    document.documentElement.style.setProperty("--azir-ink", readableOn(brand.effective_accent));
  }, [brand.effective_accent]);

  useEffect(() => {
    if (brand.effective_name) document.title = brand.effective_name;
  }, [brand.effective_name]);

  // The tab icon, drawn from the mark rather than uploaded separately. One
  // fewer thing to configure, and it stays in step with a rename.
  useEffect(() => {
    if (!brand.effective_mark || !brand.effective_accent) return;
    const svg =
      `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 64 64">` +
      `<rect width="64" height="64" rx="14" fill="${brand.effective_accent}"/>` +
      `<text x="50%" y="52%" dy=".35em" text-anchor="middle" ` +
      `font-family="ui-monospace,monospace" font-size="34" font-weight="600" ` +
      `fill="${readableOn(brand.effective_accent)}">${escapeXml(brand.effective_mark)}</text></svg>`;

    let link = document.querySelector<HTMLLinkElement>("link[rel='icon']");
    if (!link) {
      link = document.createElement("link");
      link.rel = "icon";
      document.head.appendChild(link);
    }
    link.type = "image/svg+xml";
    link.href = "data:image/svg+xml," + encodeURIComponent(svg);
  }, [brand.effective_mark, brand.effective_accent]);

  return <BrandContext.Provider value={{ brand, reload }}>{children}</BrandContext.Provider>;
}

/**
 * Black or white, whichever can actually be read on this colour.
 *
 * A reseller picking a pale yellow should not end up with white text on it.
 * The threshold is the usual relative-luminance one rather than a guess.
 */
export function readableOn(hex: string): string {
  const v = hex.replace("#", "");
  if (v.length !== 6) return "#ffffff";
  const channel = (i: number) => {
    const c = parseInt(v.slice(i, i + 2), 16) / 255;
    return c <= 0.03928 ? c / 12.92 : Math.pow((c + 0.055) / 1.055, 2.4);
  };
  const luminance = 0.2126 * channel(0) + 0.7152 * channel(2) + 0.0722 * channel(4);
  return luminance > 0.45 ? "#0a0d12" : "#ffffff";
}

function escapeXml(s: string): string {
  return s.replace(/[<>&"']/g, (c) =>
    ({ "<": "&lt;", ">": "&gt;", "&": "&amp;", '"': "&quot;", "'": "&apos;" })[c] ?? c,
  );
}

/** The mark, as an uploaded logo when there is one and a letter when there is not. */
export function Mark({ size = 28, className }: { size?: number; className?: string }) {
  const { brand } = useBranding();

  if (brand.has_logo) {
    return (
      <img
        src="/api/branding/logo"
        alt={brand.effective_name}
        className={className}
        style={{ width: size, height: size, objectFit: "contain", borderRadius: size * 0.22 }}
      />
    );
  }

  return (
    <span
      className={className}
      style={{
        width: size,
        height: size,
        borderRadius: size * 0.22,
        background: "var(--azir)",
        color: "var(--azir-ink)",
        display: "grid",
        placeItems: "center",
        fontFamily: "var(--font-mono)",
        fontSize: size * 0.46,
        fontWeight: 600,
        flex: "none",
      }}
      aria-hidden="true"
    >
      {brand.effective_mark}
    </span>
  );
}
