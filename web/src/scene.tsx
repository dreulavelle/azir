import { useEffect, useRef } from "react";

/**
 * The sign-in background.
 *
 * Every other screen in this product is dense with data, which is why there is
 * no ambient motion anywhere else: a moving thing beside a queue competes with
 * the queue and the queue is what matters. Sign-in is the one screen with
 * nothing to compete with, so it is the one place where atmosphere is free.
 *
 * What it draws is the product's own language rather than a stock effect. The
 * whole interface asks you to scan for the light that is not green — a rack, a
 * patch panel, a wall of indicators. So this is that wall, seen at rest: a slow
 * field of faint lights, almost all idle, one occasionally warming and fading
 * again. It reads as equipment breathing rather than as decoration, and it says
 * what the product is for before a word does.
 *
 * Drawn on a canvas rather than with a 3D library. The whole effect is a few
 * hundred lines of arithmetic; importing a scene graph to move some dots would
 * add hundreds of kilobytes to the first screen anybody loads, which is exactly
 * the screen that should be quick.
 */

type Light = {
  x: number;
  y: number;
  /** 0 idle, 1 fully lit. */
  level: number;
  /** How fast it is heading toward its target. */
  speed: number;
  target: number;
  hue: "idle" | "warm" | "good";
};

/** How far apart the lights sit, in CSS pixels. */
const SPACING = 34;

export function SignInScene({ accent }: { accent: string }) {
  const ref = useRef<HTMLCanvasElement>(null);

  useEffect(() => {
    const canvas = ref.current;
    if (!canvas) return;
    const ctx = canvas.getContext("2d");
    if (!ctx) return;

    // Somebody who has asked for less motion gets a still field rather than
    // nothing: the texture is the point, the movement is the flourish.
    const still = window.matchMedia("(prefers-reduced-motion: reduce)").matches;

    let lights: Light[] = [];
    let width = 0;
    let height = 0;
    let frame = 0;
    let running = true;

    const build = () => {
      const dpr = Math.min(window.devicePixelRatio || 1, 2);
      width = canvas.clientWidth;
      height = canvas.clientHeight;
      canvas.width = width * dpr;
      canvas.height = height * dpr;
      ctx.setTransform(dpr, 0, 0, dpr, 0, 0);

      lights = [];
      const cols = Math.ceil(width / SPACING) + 1;
      const rows = Math.ceil(height / SPACING) + 1;
      for (let r = 0; r < rows; r++) {
        for (let c = 0; c < cols; c++) {
          lights.push({
            // Nudged off the grid so it reads as equipment rather than as a
            // spreadsheet. A perfect lattice looks like a screensaver.
            x: c * SPACING + (Math.sin(r * 12.9898 + c * 78.233) * 4),
            y: r * SPACING + (Math.cos(r * 39.346 + c * 11.135) * 4),
            level: 0.14 + Math.abs(Math.sin(r * 3.1 + c * 1.7)) * 0.1,
            speed: 0.004 + Math.abs(Math.cos(c * 5.3)) * 0.01,
            target: 0.16,
            hue: "idle",
          });
        }
      }
    };

    const paint = () => {
      if (!running) return;
      frame++;

      ctx.clearRect(0, 0, width, height);

      // Occasionally something wakes up. Rarely, and never more than a couple
      // at once: a wall where everything is blinking is an emergency, and this
      // is meant to look like a quiet night.
      if (!still && frame % 14 === 0 && lights.length > 0) {
        const pick = lights[Math.floor(Math.random() * lights.length)];
        pick.target = 0.5 + Math.random() * 0.5;
        pick.hue = Math.random() < 0.22 ? "warm" : "good";
      }

      for (const light of lights) {
        if (!still) {
          light.level += (light.target - light.level) * light.speed * 4;
          // Once lit, it fades back down. Nothing stays on.
          if (light.target > 0.2 && Math.abs(light.level - light.target) < 0.04) {
            light.target = 0.15;
            light.hue = light.hue === "warm" ? "warm" : light.hue;
          }
          if (light.target < 0.2 && light.level < 0.2) light.hue = "idle";
        }

        const alpha = light.level;
        ctx.beginPath();
        ctx.arc(light.x, light.y, 1.6, 0, Math.PI * 2);
        ctx.fillStyle = colourFor(light.hue, accent, alpha);
        ctx.fill();

        // A lit one gets a soft halo, which is what makes it read as a lamp
        // rather than a dot.
        if (alpha > 0.2) {
          ctx.beginPath();
          ctx.arc(light.x, light.y, 1.6 + alpha * 7, 0, Math.PI * 2);
          ctx.fillStyle = colourFor(light.hue, accent, alpha * 0.12);
          ctx.fill();
        }
      }

      if (!still) requestAnimationFrame(paint);
    };

    build();
    paint();

    const onResize = () => {
      build();
      if (still) paint();
    };
    window.addEventListener("resize", onResize);

    return () => {
      running = false;
      window.removeEventListener("resize", onResize);
    };
  }, [accent]);

  return (
    <canvas
      ref={ref}
      className="pointer-events-none absolute inset-0 h-full w-full"
      aria-hidden="true"
    />
  );
}

/**
 * What colour a light is.
 *
 * Green and amber come from the same signal palette the rest of the interface
 * uses, so the sign-in screen is teaching the vocabulary before anybody has
 * seen a queue. The resting colour is the brand, so a reseller's sign-in page
 * is theirs rather than ours.
 */
function colourFor(hue: Light["hue"], accent: string, alpha: number): string {
  const a = Math.max(0, Math.min(1, alpha));
  switch (hue) {
    case "good":
      return `rgba(49, 213, 131, ${a})`;
    case "warm":
      return `rgba(255, 167, 36, ${a})`;
    default:
      return tint(accent, a * 0.7);
  }
}

function tint(hex: string, alpha: number): string {
  const v = hex.replace("#", "");
  if (v.length !== 6) return `rgba(124, 107, 255, ${alpha})`;
  const r = parseInt(v.slice(0, 2), 16);
  const g = parseInt(v.slice(2, 4), 16);
  const b = parseInt(v.slice(4, 6), 16);
  return `rgba(${r}, ${g}, ${b}, ${alpha})`;
}
