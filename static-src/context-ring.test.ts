// Hardcoded expectations: recomputing them with the module's arithmetic would assert nothing.

import { describe, it, expect } from "vitest";

import {
  KAS_SUMMARIZATION_PCT,
  compactionPoint,
  contextStroke,
  tokensUsed,
  wedgeDash,
} from "./context-ring.js";

describe("the KAS fallback", () => {
  // One number, one place: a second copy lets client and server disagree about where compaction happens.
  it("mirrors KAS's own summarization threshold", () => {
    expect(KAS_SUMMARIZATION_PCT).toBe(80);
  });
});

describe("wedgeDash", () => {
  // With pathLength="100" every dash is a percent: a `100-T` dash after a `T` gap, offset by the band's width.
  it.each([
    [80, "20 80", "20"],
    [95, "5 95", "5"],
    [50, "50 50", "50"],
    [0, "100 0", "100"],
    [100, "0 100", "0"],
  ])(
    "draws the band from %i percent as dasharray %s offset %s",
    (threshold, dasharray, dashoffset) => {
      expect(wedgeDash(threshold)).toEqual({ dasharray, dashoffset });
    },
  );

  it.each([
    [-40, "100 0", "100"],
    [140, "0 100", "0"],
  ])("clamps an out-of-range threshold of %i", (threshold, dasharray, dashoffset) => {
    expect(wedgeDash(threshold)).toEqual({ dasharray, dashoffset });
  });
});

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
    // The ring saturates at 100%, so the readout may not claim 240K of a 200K window.
    expect(tokensUsed(120, 200_000)).toBe(200_000);
    expect(tokensUsed(-20, 200_000)).toBe(0);
  });
});

describe("compactionPoint", () => {
  it("defers to KAS's reported threshold at the default value", () => {
    expect(compactionPoint({ enabled: true, pct: 80 }, 75)).toEqual({ band: 75, t: 75 });
    expect(compactionPoint({ enabled: true, pct: 80 }, undefined)).toEqual({ band: 80, t: 80 });
  });

  it("is the slider's value anywhere else, whatever KAS reports", () => {
    expect(compactionPoint({ enabled: true, pct: 60 }, 80)).toEqual({ band: 60, t: 60 });
    expect(compactionPoint({ enabled: true, pct: 90 }, 80)).toEqual({ band: 90, t: 90 });
  });

  it("has no band and keys the ramp on 100 when switched off", () => {
    expect(compactionPoint({ enabled: false, pct: 60 }, 75)).toEqual({ band: null, t: 100 });
  });
});

describe("the ramp keyed on other compaction points", () => {
  it.each([
    [60, 30, "var(--c-green)"],
    [60, 35, "color-mix(in oklch, var(--c-yellow) 50.0%, var(--c-green))"],
    [60, 50, "var(--c-red)"],
    [90, 60, "var(--c-green)"],
    [90, 75, "color-mix(in oklch, var(--c-red) 50.0%, var(--c-yellow))"],
    [90, 80, "var(--c-red)"],
    [100, 70, "var(--c-green)"],
    [100, 80, "color-mix(in oklch, var(--c-red) 0.0%, var(--c-yellow))"],
    [100, 89.9, "color-mix(in oklch, var(--c-red) 99.0%, var(--c-yellow))"],
    [100, 90, "var(--c-red)"],
  ])("at T=%i maps %i percent to %s", (t, pct, want) => {
    expect(contextStroke(pct, t)).toBe(want);
  });
});

describe("contextStroke at the default T=80, today's 50/70", () => {
  it("is continuous across nearby percentages", () => {
    const a = contextStroke(55, 80);
    const b = contextStroke(55.01, 80);
    expect(a).toBe("color-mix(in oklch, var(--c-yellow) 50.0%, var(--c-green))");
    expect(b).toBe("color-mix(in oklch, var(--c-yellow) 50.1%, var(--c-green))");
    expect(a).not.toBe(b);
  });

  // One percentage is one colour: the window size is not an input, so the ring matches the number beside it.
  it("resolves one percentage to one colour whatever the window", () => {
    expect(contextStroke(25, 80)).toBe("var(--c-green)");
    expect(contextStroke(75, 80)).toBe("var(--c-red)");
  });

  it.each([
    [0, "var(--c-green)"],
    [25, "var(--c-green)"],
    [50, "var(--c-green)"],
    [55, "color-mix(in oklch, var(--c-yellow) 50.0%, var(--c-green))"],
    [60, "color-mix(in oklch, var(--c-red) 0.0%, var(--c-yellow))"],
    [65, "color-mix(in oklch, var(--c-red) 50.0%, var(--c-yellow))"],
    [70, "var(--c-red)"],
    [80, "var(--c-red)"],
    [100, "var(--c-red)"],
  ])("maps %i percent to %s", (pct, want) => {
    expect(contextStroke(pct, 80)).toBe(want);
  });

  // Green holds through the first band; a ramp from 0 shows a warm ring at 40%.
  it("holds green across the whole first band", () => {
    for (const pct of [0, 10, 20, 30, 40, 49.9, 50]) {
      expect(contextStroke(pct, 80)).toBe("var(--c-green)");
    }
  });

  it.each([70, 75, 90, 100])("saturates at red for %i percent", (pct) => {
    expect(contextStroke(pct, 80)).toBe("var(--c-red)");
  });

  it("hands the yellow midpoint to the second segment, not the first", () => {
    // At the midpoint the mix names --c-red, so the boundary belongs to segment 2.
    expect(contextStroke(60, 80)).toBe("color-mix(in oklch, var(--c-red) 0.0%, var(--c-yellow))");
  });

  it("produces no NaN or Infinity anywhere on the ramp", () => {
    for (const pct of [0, 25, 50, 55, 60, 65, 70, 100]) {
      const got = contextStroke(pct, 80);
      expect(got).not.toContain("NaN");
      expect(got).not.toContain("Infinity");
    }
  });

  it("clamps an out-of-range percentage at both ends", () => {
    expect(contextStroke(-20, 80)).toBe(contextStroke(0, 80));
    expect(contextStroke(140, 80)).toBe(contextStroke(100, 80));
  });

  // Tokens only: a literal would fork the palette and be wrong in one theme.
  it.each([0, 50, 55, 65, 100])("names only design tokens at %i percent", (pct) => {
    expect(contextStroke(pct, 80)).toMatch(/^(var\(--c-[a-z]+\)|color-mix\(in oklch, .+\))$/);
    expect(contextStroke(pct, 80)).not.toMatch(/#|rgb|oklch\(\d/);
  });
});
