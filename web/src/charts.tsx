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
 * Volume over time.
 *
 * An area rather than bars because the question is "is this getting worse",
 * which is a shape, not a set of individual readings.
 */
export function Trend({
  points,
  height = 84,
  unit = "",
  tone = "idle",
}: {
  points: Point[];
  height?: number;
  unit?: string;
  tone?: Signal;
}) {
  const gradientId = useId();
  const [hover, setHover] = useState<number | null>(null);

  const { path, area, coords, peak } = useMemo(() => {
    if (points.length === 0) return { path: "", area: "", coords: [], peak: 0 };

    const peak = Math.max(...points.map((p) => p.value), 1);
    const stepX = 100 / Math.max(points.length - 1, 1);

    const coords = points.map((p, i) => ({
      x: i * stepX,
      // 8% of headroom so a peak is not glued to the top edge.
      y: 100 - (p.value / peak) * 92,
      point: p,
    }));

    const line = coords.map((c, i) => `${i === 0 ? "M" : "L"}${c.x},${c.y}`).join(" ");
    return { path: line, area: `${line} L100,100 L0,100 Z`, coords, peak };
  }, [points]);

  if (points.length === 0) return null;

  const colour = STROKE[tone];
  const active = hover !== null ? coords[hover] : null;
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

        <path d={area} fill={`url(#${gradientId})`} />
        <path
          d={path}
          fill="none"
          stroke={colour}
          strokeWidth="1.5"
          strokeLinejoin="round"
          strokeLinecap="round"
          vectorEffect="non-scaling-stroke"
        />

        {/* Today, marked. In a fortnight of readings it is the only one anybody
            is deciding anything about. */}
        {!active && last && (
          <circle cx={last.x} cy={last.y} r="2.6" fill="var(--ink)" vectorEffect="non-scaling-stroke" />
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
            <circle cx={active.x} cy={active.y} r="2.6" fill="var(--ink)" vectorEffect="non-scaling-stroke" />
          </>
        )}
      </svg>

      {/* Read out in words rather than as a floating box, so it never covers
          the shape it is describing. */}
      <div className="mt-2 h-4">
        {active ? (
          <Label className="text-ink">
            {active.point.value}
            {unit} · {active.point.label}
          </Label>
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
