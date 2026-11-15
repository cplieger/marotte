// Hardcoded expectations: recomputing them with the module's arithmetic would assert nothing.

import { describe, it, expect } from "vitest";

import { compactionPoint, contextInk, tokensUsed } from "./context-indicator.js";

describe("tokensUsed", () => {
  // One derivation, read by the ramp and the expanded card's readout.
  it.each([
    [25, 200_000, 50_000],
    [50, 200_000, 100_000],
    [10, 1_000_000, 100_000],
    [0, 1_000_000, 0],
    [25, 0, 0],
  ])("puts %i percent of a %i-token window at %i tokens", (pct, size, want) => {
    expect(tokensUsed(pct, size)).toBe(want);
  });

  it("never reports more tokens than the window holds", () => {
    // The share saturates at 100%, so the readout may not claim 240K of a 200K window.
    expect(tokensUsed(120, 200_000)).toBe(200_000);
    expect(tokensUsed(-20, 200_000)).toBe(0);
  });
});

describe("compactionPoint", () => {
  it("is the slider's value, where a turn queues its compaction", () => {
    expect(compactionPoint({ enabled: true, pct: 60 })).toEqual({ at: 60, t: 60 });
    expect(compactionPoint({ enabled: true, pct: 80 })).toEqual({ at: 80, t: 80 });
  });

  it("compacts nowhere and keys the ramp on 100 when switched off", () => {
    expect(compactionPoint({ enabled: false, pct: 60 })).toEqual({ at: null, t: 100 });
  });
});

describe("the ramp keyed on other compaction points", () => {
  it.each([
    [60, 30, "var(--ctx-ink-green)"],
    [60, 35, "color-mix(in oklch, var(--ctx-ink-yellow) 50.0%, var(--ctx-ink-green))"],
    [60, 50, "var(--ctx-ink-red)"],
    [90, 60, "var(--ctx-ink-green)"],
    [90, 75, "color-mix(in oklch, var(--ctx-ink-red) 50.0%, var(--ctx-ink-yellow))"],
    [90, 80, "var(--ctx-ink-red)"],
    [100, 70, "var(--ctx-ink-green)"],
    [100, 80, "color-mix(in oklch, var(--ctx-ink-red) 0.0%, var(--ctx-ink-yellow))"],
    [100, 89.9, "color-mix(in oklch, var(--ctx-ink-red) 99.0%, var(--ctx-ink-yellow))"],
    [100, 90, "var(--ctx-ink-red)"],
  ])("at T=%i maps %i percent to %s", (t, pct, want) => {
    expect(contextInk(pct, t)).toBe(want);
  });
});

describe("contextInk at the default T=80, today's 50/70", () => {
  it("is continuous across nearby percentages", () => {
    const a = contextInk(55, 80);
    const b = contextInk(55.01, 80);
    expect(a).toBe("color-mix(in oklch, var(--ctx-ink-yellow) 50.0%, var(--ctx-ink-green))");
    expect(b).toBe("color-mix(in oklch, var(--ctx-ink-yellow) 50.1%, var(--ctx-ink-green))");
    expect(a).not.toBe(b);
  });

  // One percentage is one colour: the window size is not an input, so the ink matches the number it paints.
  it("resolves one percentage to one colour whatever the window", () => {
    expect(contextInk(25, 80)).toBe("var(--ctx-ink-green)");
    expect(contextInk(75, 80)).toBe("var(--ctx-ink-red)");
  });

  it.each([
    [0, "var(--ctx-ink-green)"],
    [25, "var(--ctx-ink-green)"],
    [50, "var(--ctx-ink-green)"],
    [55, "color-mix(in oklch, var(--ctx-ink-yellow) 50.0%, var(--ctx-ink-green))"],
    [60, "color-mix(in oklch, var(--ctx-ink-red) 0.0%, var(--ctx-ink-yellow))"],
    [65, "color-mix(in oklch, var(--ctx-ink-red) 50.0%, var(--ctx-ink-yellow))"],
    [70, "var(--ctx-ink-red)"],
    [80, "var(--ctx-ink-red)"],
    [100, "var(--ctx-ink-red)"],
  ])("maps %i percent to %s", (pct, want) => {
    expect(contextInk(pct, 80)).toBe(want);
  });

  // Green holds through the first band; a ramp from 0 shows a warm share at 40%.
  it("holds green across the whole first band", () => {
    for (const pct of [0, 10, 20, 30, 40, 49.9, 50]) {
      expect(contextInk(pct, 80)).toBe("var(--ctx-ink-green)");
    }
  });

  it.each([70, 75, 90, 100])("saturates at red for %i percent", (pct) => {
    expect(contextInk(pct, 80)).toBe("var(--ctx-ink-red)");
  });

  it("hands the yellow midpoint to the second segment, not the first", () => {
    // At the midpoint the mix names --c-red, so the boundary belongs to segment 2.
    expect(contextInk(60, 80)).toBe(
      "color-mix(in oklch, var(--ctx-ink-red) 0.0%, var(--ctx-ink-yellow))",
    );
  });

  it("produces no NaN or Infinity anywhere on the ramp", () => {
    for (const pct of [0, 25, 50, 55, 60, 65, 70, 100]) {
      const got = contextInk(pct, 80);
      expect(got).not.toContain("NaN");
      expect(got).not.toContain("Infinity");
    }
  });

  it("clamps an out-of-range percentage at both ends", () => {
    expect(contextInk(-20, 80)).toBe(contextInk(0, 80));
    expect(contextInk(140, 80)).toBe(contextInk(100, 80));
  });

  // Tokens only: a literal would fork the palette and be wrong in one theme.
  it.each([0, 50, 55, 65, 100])("names only design tokens at %i percent", (pct) => {
    expect(contextInk(pct, 80)).toMatch(/^(var\(--ctx-ink-[a-z]+\)|color-mix\(in oklch, .+\))$/);
    expect(contextInk(pct, 80)).not.toMatch(/#|rgb|oklch\(\d/);
  });
});
