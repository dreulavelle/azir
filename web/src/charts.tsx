import { useId, useMemo, useState } from "react";
import { cn } from "@/lib/cn";
import { Label, type Signal } from "./ui";

/**
 * Charts, drawn by hand in SVG.
 *
 * No charting library: these are small, they have to match the design tokens
 * exactly, and every library worth using is larger than the code below. Hand
 * drawing also means the hover behaviour is ours — a tooltip that reads in the
 * product's own words rather than a vendor's default.
 *
 * Colour follows the same rule as everything else: signal hues mean something
 * is wrong, and the brand violet is reserved for the assistant. A chart of how
 * many tickets arrived is neither — arriving work is not a fault — so it is
 * drawn in neutral ink, with only the most recent reading emphasised.
 */

export type Point = { label: string; value: number };

const STROKE: Record<Signal, string> = {
  critical: "var(--critical)",
  attention: "var(--attention)",
  steady: "var(--steady)",
  idle: "var(--ink-dim)",
};

/**
 * Volume over time, and optionally what it is being outrun by.
 *
 * An area rather than bars because the question is "is this getting worse",
 * which is a shape, not a set of individual readings.
 *
 * `against` draws a second series as a filled region underneath the line. For a
 * queue that is the whole story in one picture: work arriving as the line, work
 * finishing as the fill, and every day the line rides above the fill is a day
 * the backlog grew. Two numbers in a stat box cannot say that — they can say
 * this week was bad, but not that it has been bad for nine days running.
 */
export function Trend({
  points,
  against,
  height = 84,
  unit = "",
  tone = "idle",
  againstTone = "steady",
  legend,
}: {
  points: Point[];
  against?: Point[];
  height?: number;
  unit?: string;
  tone?: Signal;
  againstTone?: Signal;
  /** What the two series are called, when there are two. */
  legend?: { points: string; against: string };
}) {
  const gradientId = useId();
  const [hover, setHover] = useState<number | null>(null);

  const { path, area, coords, againstArea, againstCoords, peak } = useMemo(() => {
    if (points.length === 0) {
      return { path: "", area: "", coords: [], againstArea: "", againstCoords: [], peak: 0 };
    }

    // Both series share one scale, or the comparison would be a lie: a fill
    // drawn against its own maximum can sit above a line that is twice its size.
    const peak = Math.max(...points.map((p) => p.value), ...(against ?? []).map((p) => p.value), 1);
    const stepX = 100 / Math.max(points.length - 1, 1);
    // 8% of headroom so a peak is not glued to the top edge.
    const plot = (series: Point[]) =>
      series.map((p, i) => ({ x: i * stepX, y: 100 - (p.value / peak) * 92, point: p }));

    const coords = plot(points);
    const line = coords.map((c, i) => `${i === 0 ? "M" : "L"}${c.x},${c.y}`).join(" ");

    const againstCoords = against ? plot(against) : [];
    const againstLine = againstCoords.map((c, i) => `${i === 0 ? "M" : "L"}${c.x},${c.y}`).join(" ");

    return {
      path: line,
      area: `${line} L100,100 L0,100 Z`,
      coords,
      againstCoords,
      againstArea: againstLine ? `${againstLine} L100,100 L0,100 Z` : "",
      peak,
    };
  }, [points, against]);

  if (points.length === 0) return null;

  const colour = STROKE[tone];
  const againstColour = STROKE[againstTone];
  const active = hover !== null ? coords[hover] : null;
  const activeAgainst = hover !== null ? againstCoords[hover] : null;
  const last = coords[coords.length - 1];

  return (
    <div>
      <svg
        viewBox="0 0 100 100"
        preserveAspectRatio="none"
        style={{ height, width: "100%", display: "block", overflow: "visible" }}
        onMouseLeave={() => setHover(null)}
        onMouseMove={(e) => {
          const box = e.currentTarget.getBoundingClientRect();
          const ratio = (e.clientX - box.left) / box.width;
          setHover(
            Math.min(points.length - 1, Math.max(0, Math.round(ratio * (points.length - 1)))),
          );
        }}
      >
        <defs>
          <linearGradient id={gradientId} x1="0" y1="0" x2="0" y2="1">
            <stop offset="0%" stopColor={colour} stopOpacity="0.2" />
            <stop offset="100%" stopColor={colour} stopOpacity="0" />
          </linearGradient>
        </defs>

        {/* The comparison sits underneath as solid ground, so the line above it
            reads as "more than this" without needing a legend to work it out. */}
        {againstArea && (
          <>
            <path d={againstArea} fill={againstColour} fillOpacity="0.16" />
            <path
              d={againstArea}
              fill="none"
              stroke={againstColour}
              strokeWidth="1"
              strokeOpacity="0.55"
              vectorEffect="non-scaling-stroke"
            />
          </>
        )}

        {!againstArea && <path d={area} fill={`url(#${gradientId})`} />}
        <path
          d={path}
          fill="none"
          stroke={colour}
          strokeWidth="1.5"
          strokeLinejoin="round"
          strokeLinecap="round"
          vectorEffect="non-scaling-stroke"
        />

        {/* Today, marked — in a fortnight of readings it is the only one anybody
            is deciding anything about. Clamped off the edges, because a final
            reading of zero puts the marker exactly on the axis, where half is
            cut away and the rest reads as a smudge. */}
        {!active && last && (
          <circle
            cx={Math.min(Math.max(last.x, 1), 99)}
            cy={Math.min(Math.max(last.y, 3), 97)}
            r="2.6"
            fill="var(--ink)"
            vectorEffect="non-scaling-stroke"
          />
        )}

        {active && (
          <>
            <line
              x1={active.x}
              y1="0"
              x2={active.x}
              y2="100"
              stroke="var(--edge-strong)"
              strokeWidth="1"
              vectorEffect="non-scaling-stroke"
            />
            <circle
              cx={active.x}
              cy={Math.min(Math.max(active.y, 3), 97)}
              r="2.6"
              fill="var(--ink)"
              vectorEffect="non-scaling-stroke"
            />
            {activeAgainst && (
              <circle
                cx={activeAgainst.x}
                cy={activeAgainst.y}
                r="2.2"
                fill={againstColour}
                vectorEffect="non-scaling-stroke"
              />
            )}
          </>
        )}
      </svg>

      {/* Read out in words rather than as a floating box, so it never covers
          the shape it is describing. */}
      <div className="mt-2 flex h-4 items-center gap-3">
        {active ? (
          <Label className="text-ink">
            {legend ? `${legend.points} ${active.point.value}` : `${active.point.value}${unit}`}
            {activeAgainst && legend ? ` · ${legend.against} ${activeAgainst.point.value}` : ""} ·{" "}
            {active.point.label}
          </Label>
        ) : legend ? (
          <>
            <Key colour={colour} label={legend.points} />
            <Key colour={againstColour} label={legend.against} filled />
          </>
        ) : (
          <Label>
            peak {peak}
            {unit} · {points.length} days
          </Label>
        )}
      </div>
    </div>
  );
}

/** One entry in a two-series legend. */
function Key({ colour, label, filled }: { colour: string; label: string; filled?: boolean }) {
  return (
    <span className="flex items-center gap-1.5">
      <span
        className="h-[3px] w-3 rounded-full"
        style={{ background: colour, opacity: filled ? 0.45 : 1 }}
      />
      <Label>{label}</Label>
    </span>
  );
}

/**
 * How long a ticket has been alive, and how much of that was silence.
 *
 * Sized against the oldest ticket on screen so the bars are comparable to each
 * other, with the quiet tail drawn in the colour of how quiet it has got. A
 * ticket open three days and answered yesterday and a ticket open three days
 * and never touched produce the same "3d" in a column of numbers; they do not
 * produce the same bar, and the difference is the entire point of the screen.
 */
export function Life({
  ageDays,
  silentDays,
  longest,
  width = 56,
}: {
  ageDays: number;
  silentDays: number;
  /** The oldest ticket in view, so every bar shares one scale. */
  longest: number;
  width?: number;
}) {
  if (ageDays <= 0) return <span className="inline-block" style={{ width }} aria-hidden="true" />;

  // Square root rather than linear, because one forgotten ticket sets the
  // scale for the whole screen: against a ninety day outlier, a linear bar
  // renders every ordinary ticket as the same one-pixel speck, which says
  // nothing at all. Compressing the range keeps the old ones visibly longest
  // while leaving the recent ones readable.
  const span = Math.max(Math.sqrt(ageDays / Math.max(longest, 1)) * width, 8);
  const silent = Math.min(silentDays / Math.max(ageDays, 0.01), 1) * span;
  const tone = silentDays >= 14 ? "bg-critical" : silentDays >= 5 ? "bg-attention" : "bg-steady";

  return (
    <span
      className="inline-flex h-1 overflow-hidden rounded-full bg-edge align-middle"
      style={{ width: span }}
      aria-hidden="true"
      title={`open ${Math.round(ageDays)}d, quiet for ${Math.round(silentDays)}d`}
    >
      {/* The part of its life something was happening, then the silence. */}
      <span className="h-full bg-edge-strong" style={{ width: span - silent }} />
      <span className={cn("h-full", tone)} style={{ width: silent }} />
    </span>
  );
}

const PIECE: Record<Signal, string> = {
  critical: "bg-critical",
  attention: "bg-attention",
  steady: "bg-steady",
  idle: "bg-edge-strong",
};

const DOT: Record<Signal, string> = {
  critical: "bg-critical",
  attention: "bg-attention",
  steady: "bg-steady",
  idle: "bg-edge-strong",
};

/**
 * How a total splits, as one bar.
 *
 * A pie would use more space to say the same thing and be harder to compare
 * across rows; a stacked bar reads at a glance and lines up with its neighbours.
 */
export function Split({
  parts,
}: {
  parts: { label: string; value: number; tone: "urgent" | "warn" | "good" | "accent" | "calm" }[];
}) {
  const total = parts.reduce((n, p) => n + p.value, 0);
  const [hover, setHover] = useState<string | null>(null);
  if (total === 0) return null;

  const signalOf = (tone: string): Signal =>
    tone === "urgent" ? "critical" : tone === "warn" ? "attention" : tone === "good" ? "steady" : "idle";

  const shown = parts.filter((p) => p.value > 0);

  return (
    <div onMouseLeave={() => setHover(null)}>
      <div className="flex h-2 gap-0.5 overflow-hidden rounded-full">
        {shown.map((p) => (
          <div
            key={p.label}
            className={cn(
              "h-full transition-opacity first:rounded-l-full last:rounded-r-full",
              PIECE[signalOf(p.tone)],
              hover && hover !== p.label && "opacity-30",
            )}
            style={{ width: `${(p.value / total) * 100}%` }}
            onMouseEnter={() => setHover(p.label)}
            title={`${p.value} ${p.label}`}
          />
        ))}
      </div>
      <div className="mt-2.5 flex flex-wrap gap-x-4 gap-y-1">
        {shown.map((p) => (
          <span
            key={p.label}
            className={cn(
              "flex items-center gap-1.5 text-xs text-ink-dim transition-opacity",
              hover && hover !== p.label && "opacity-40",
            )}
            onMouseEnter={() => setHover(p.label)}
          >
            <span className={cn("size-1.5 rounded-full", DOT[signalOf(p.tone)])} />
            {p.label}
            <span className="font-mono tabular-nums text-ink">{p.value}</span>
          </span>
        ))}
      </div>
    </div>
  );
}

/**
 * A sparkline, for a table cell.
 *
 * No axes, no labels, no hover — at this size those would be noise. It answers
 * one question at a glance: is this line going up. Anything more precise is a
 * click away on the row it belongs to.
 */
export function Spark({
  values,
  tone = "idle",
  width = 62,
  height = 18,
}: {
  values: number[];
  tone?: Signal;
  width?: number;
  height?: number;
}) {
  if (values.length < 2) {
    return <span className="inline-block" style={{ width }} aria-hidden="true" />;
  }

  const peak = Math.max(...values, 1);
  const stepX = width / (values.length - 1);
  const path = values
    .map(
      (v, i) =>
        `${i === 0 ? "M" : "L"}${(i * stepX).toFixed(1)},${(height - (v / peak) * (height - 2) - 1).toFixed(1)}`,
    )
    .join(" ");

  return (
    <svg width={width} height={height} aria-hidden="true" style={{ color: STROKE[tone] }}>
      <path
        d={path}
        fill="none"
        stroke="currentColor"
        strokeWidth="1.4"
        strokeLinejoin="round"
        strokeLinecap="round"
      />
      <circle
        cx={width}
        cy={height - (values[values.length - 1] / peak) * (height - 2) - 1}
        r="1.8"
        fill="currentColor"
      />
    </svg>
  );
}
