// The attention favicon variants (static/favicon-*.svg): a missing variant silently breaks the link swap.

import { describe, it, expect } from "vitest";
import { loadCSS, ruleContaining } from "./__test-helpers__/css-rules.js";

const staticAssets = import.meta.glob<string>(["../static/*.svg", "../static/index.html"], {
  query: "?raw",
  import: "default",
  eager: true,
});

/** Each field pins a link in the icon generator's chain, which names the tab-dot tokens. */
const CUES = {
  input: {
    token: "--c-dot-input",
    oklch: "oklch(78% 0.15 95deg)",
    fill: "#d6b529",
    state: "input",
    shape: "circle",
  },
  done: {
    token: "--c-dot-done",
    oklch: "oklch(78% 0.15 150deg)",
    fill: "#67d283",
    state: "done",
    shape: "circle",
  },
  alert: {
    token: "--c-dot-failed",
    oklch: "oklch(70% 0.125 27.3deg)",
    fill: "#e27e73",
    state: "failed",
    shape: "diamond",
  },
} as const;

const VARIANTS = ["input", "done", "alert"] as const;

/** The generator's 32-unit geometry; every output scales from it. */
const DOT_R = 5.5;
const PAD = 3;
const UNIT = 32;

async function readStatic(name: string): Promise<string | null> {
  return Promise.resolve(staticAssets[`../static/${name}`] ?? null);
}

describe("attention favicon variants", () => {
  it("ships one variant per cue icon, named the way the swap rewrites hrefs", async () => {
    const base = await readStatic("favicon.svg");
    if (base === null) {
      return; // Stryker sandbox: ../static is not copied in.
    }
    for (const variant of VARIANTS) {
      const svg = await readStatic(`favicon-${variant}.svg`);
      expect(
        svg,
        `static/favicon-${variant}.svg is missing: the tab icon would 404`,
      ).not.toBeNull();
    }
  });

  it("derives each variant from the base icon plus exactly one dot", async () => {
    const base = await readStatic("favicon.svg");
    if (base === null) {
      return;
    }
    const [head, , tail] = [
      base.slice(0, base.lastIndexOf("</svg>")),
      "</svg>",
      base.slice(base.lastIndexOf("</svg>") + "</svg>".length),
    ];
    for (const variant of VARIANTS) {
      const svg = await readStatic(`favicon-${variant}.svg`);
      if (svg === null) {
        continue;
      }
      // A variant is the base icon pixel-for-pixel plus a badge; any other difference is a hand edit.
      expect(svg.startsWith(head), `favicon-${variant}.svg diverges from favicon.svg`).toBe(true);
      expect(svg.endsWith(`</svg>${tail}`)).toBe(true);
      const inserted = svg.slice(head.length, svg.length - `</svg>${tail}`.length);
      // Circle or diamond path, one element each.
      expect(inserted).toMatch(
        CUES[variant].shape === "diamond"
          ? /^<path d="M[-\d. LZ]+" fill="#[0-9a-f]{6}"\/>$/
          : /^<circle cx="[\d.]+" cy="[\d.]+" r="[\d.]+" fill="#[0-9a-f]{6}"\/>$/,
      );
    }
  });

  it("places every variant's dot at the generator's own badge position", async () => {
    const base = await readStatic("favicon.svg");
    if (base === null) {
      return;
    }
    // Scaled onto the base's viewBox (48 for marotte); an unscaled dot sits mid-artwork.
    const box = /viewBox="0 0 ([\d.]+) ([\d.]+)"/.exec(base);
    expect(box).not.toBeNull();
    const side = Number(box?.[1]);
    for (const variant of VARIANTS) {
      const svg = await readStatic(`favicon-${variant}.svg`);
      if (svg === null) {
        continue;
      }
      // Top-right with PAD clear beyond it; every shape holds one footprint.
      const wantCx = ((UNIT - PAD - DOT_R) / UNIT) * side;
      const wantCy = ((PAD + DOT_R) / UNIT) * side;
      const wantR = (DOT_R / UNIT) * side;
      if (CUES[variant].shape === "diamond") {
        const path = /<path d="M([-\d. LZ]+)" fill="#[0-9a-f]{6}"\/>(?=<\/svg>)/.exec(svg);
        expect(path, `favicon-${variant}.svg has no trailing status badge`).not.toBeNull();
        const pts = (path?.[1] ?? "")
          .replace(/Z$/, "")
          .split("L")
          .map((p) => p.trim().split(/\s+/).map(Number));
        expect(pts, `favicon-${variant}.svg diamond must have four vertices`).toHaveLength(4);
        expect(pts).toEqual([
          [wantCx, wantCy - wantR],
          [wantCx + wantR, wantCy],
          [wantCx, wantCy + wantR],
          [wantCx - wantR, wantCy],
        ]);
      } else {
        const dot =
          /<circle cx="([\d.]+)" cy="([\d.]+)" r="([\d.]+)" fill="#[0-9a-f]{6}"\/>(?=<\/svg>)/.exec(
            svg,
          );
        expect(dot, `favicon-${variant}.svg has no trailing status badge`).not.toBeNull();
        expect(Number(dot?.[1]), `favicon-${variant}.svg dot cx`).toBeCloseTo(wantCx, 5);
        expect(Number(dot?.[2]), `favicon-${variant}.svg dot cy`).toBeCloseTo(wantCy, 5);
        expect(Number(dot?.[3]), `favicon-${variant}.svg dot radius`).toBeCloseTo(wantR, 5);
      }
    }
  });

  it("gives each cue its own colour, so done and alert cannot be swapped", async () => {
    const base = await readStatic("favicon.svg");
    if (base === null) {
      return;
    }
    // Pins the fill, or a red "done" and green "alert" pass.
    for (const variant of VARIANTS) {
      const svg = await readStatic(`favicon-${variant}.svg`);
      if (svg === null) {
        continue;
      }
      const fill = /<(?:circle|path)[^>]*fill="(#[0-9a-f]{6})"\/>(?=<\/svg>)/.exec(svg);
      expect(fill?.[1], `favicon-${variant}.svg carries the wrong cue colour`).toBe(
        CUES[variant].fill,
      );
    }
    // Three cues, three colours.
    expect(new Set(VARIANTS.map((v) => CUES[v].fill)).size).toBe(VARIANTS.length);
  });

  it("gives each cue the silhouette its own tab dot has", async () => {
    // The shape is read from the state's CSS: `failed` and `done` differ only in hue otherwise (WCAG 1.4.1).
    const tabs = loadCSS("12-tabs.css");
    for (const variant of VARIANTS) {
      const { state, shape } = CUES[variant];
      const rule = ruleContaining(tabs, `.tab-status-dot[data-status="${state}"]`, "top");
      const isDiamond = /transform: rotate\(45deg\)/.test(rule.body);
      expect(
        isDiamond ? "diamond" : "circle",
        `favicon-${variant} mirrors the ${state} dot, whose rule says otherwise`,
      ).toBe(shape);
    }
  });

  it("does not claim the ring that its dot has and its icon cannot carry", async () => {
    // The ring is dropped at 16px: 0.85px at 30% alpha is invisible.
    const svg = await readStatic("favicon-input.svg");
    if (svg === null) {
      return;
    }
    const badges = [
      ...svg.matchAll(new RegExp(`<(?:circle|path)[^>]*fill="${CUES.input.fill}"`, "g")),
    ];
    expect(badges, "favicon-input.svg must carry exactly one input-coloured mark").toHaveLength(1);
    expect(svg).not.toContain("stroke");
  });

  it("keeps each cue's colour token where the generator reads it from", () => {
    const css = loadCSS("01-tokens.css");
    for (const variant of VARIANTS) {
      const { token, oklch } = CUES[variant];
      // The default theme's :root, which the generator transcribed; one icon serves both themes.
      const declared = new RegExp(`${token}:\\s*([^;]+);`).exec(css);
      expect(declared?.[1]?.trim(), `${token} moved: re-run gen-attention-icons.py`).toBe(oklch);
    }
  });

  it("has one icon link for the swap to rewrite, pointing at the base", async () => {
    const html = await readStatic("index.html");
    if (html === null) {
      return;
    }
    // apple-touch-icon is excluded by `rel~="icon"`: the OS caches it at install.
    const icons = [...html.matchAll(/<link\s+rel="icon"[^>]*href="([^"]+)"/g)].map((m) => m[1]);
    expect(icons).toEqual(["/favicon.svg"]);
  });
});
