// The compaction band, measured: it carries no text or shape, so its separation from the track is its legibility.
// Token names are resolved per theme from `01-tokens.css`, so a retune moves these numbers.

import { describe, it, expect } from "vitest";
import { execFileSync } from "node:child_process";
import { existsSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));
const script = join(here, "..", "scripts", "css-contrast.py");

interface Measurement {
  theme: string;
  ratio: number;
}

function pair(fg: string, bg: string): Measurement[] {
  const out = execFileSync("python3", [script, "pair", fg, bg], { encoding: "utf8" });
  const rows = out
    .trim()
    .split("\n")
    .map((line) => {
      const cols = line.split("\t");
      return { theme: cols[0] ?? "", ratio: Number(cols[3]) };
    });
  expect(
    rows.map((r) => r.theme),
    `expected both themes for ${fg} on ${bg}`,
  ).toEqual(["dark", "light"]);
  return rows;
}

const BAND = "--c-context-wedge";
const TRACK = "--c-border";
const PAGE = "--c-bg-primary";
const FILL_HUES = ["--c-green", "--c-yellow", "--c-red"];

/**
 * OKLab floor between the band and the nearest fill stroke, anchored to the ramp's own scale (light green-to-yellow
 * is 0.1029 apart). Cleared at 0.1603 dark, 0.1217 light.
 */
const MIN_RAMP_SEPARATION = 0.09;

/**
 * Every mix `context-ring.ts` can emit, enumerated (one-decimal quantization makes 1001 steps per segment), measured
 * through the script's own OKLab conversion and premultiplied oklch mix.
 */
const RAMP_SEPARATION_PY = `
import importlib.util, math, sys
spec = importlib.util.spec_from_file_location("cc", sys.argv[1])
cc = importlib.util.module_from_spec(spec)
spec.loader.exec_module(cc)
def de(a, b):
    la, aa, ba = cc.colour_to_oklab(a)
    lb, ab, bb = cc.colour_to_oklab(b)
    return math.sqrt((la - lb) ** 2 + (aa - ab) ** 2 + (ba - bb) ** 2)
for th in cc.parse_themes():
    band = th.colour("--c-context-wedge")
    worst = (9.9, "")
    for to, frm in (("--c-yellow", "--c-green"), ("--c-red", "--c-yellow")):
        for i in range(1001):
            expr = "color-mix(in oklch, var(%s) %.1f%%, var(%s))" % (to, i / 10, frm)
            d = de(band, th.resolve(expr))
            if d < worst[0]:
                worst = (d, "%s %.1f%% into %s" % (to, i / 10, frm))
    print("%s\\t%.6f\\t%s" % (th.name, worst[0], worst[1]))
`;

interface Separation {
  theme: string;
  dE: number;
  at: string;
}

/** OKLab, not WCAG ratio: the band separates on chroma, which the luminance-only ratio barely sees. */
function rampSeparation(): Separation[] {
  const out = execFileSync("python3", ["-c", RAMP_SEPARATION_PY, script], { encoding: "utf8" });
  const rows = out
    .trim()
    .split("\n")
    .map((line) => {
      const cols = line.split("\t");
      return { theme: cols[0] ?? "", dE: Number(cols[1]), at: cols[2] ?? "" };
    });
  expect(
    rows.map((r) => r.theme),
    "expected both themes for the band against the fill's ramp",
  ).toEqual(["dark", "light"]);
  return rows;
}

// Stryker's sandbox copies static-src alone and the script lives outside it, so its absence is a skip.
describe.skipIf(!existsSync(script))("the compaction band, measured", () => {
  it("clears 3:1 against the track it is drawn on, in both themes", () => {
    for (const m of pair(BAND, TRACK)) {
      expect(m.ratio, `${m.theme}: band vs track`).toBeGreaterThanOrEqual(3.0);
    }
  });

  it("clears 3:1 against the page the pill sits on, in both themes", () => {
    // The track is a wash, so the page shows through; a band clear of the track alone could still vanish into the surface.
    for (const m of pair(BAND, PAGE)) {
      expect(m.ratio, `${m.theme}: band vs page`).toBeGreaterThanOrEqual(3.0);
    }
  });

  it("stays quieter than every hue the fill can take", () => {
    // A comparison, not a ceiling: a band as loud as the fill reads as already-filled.
    const band = new Map(pair(BAND, TRACK).map((m) => [m.theme, m.ratio]));
    for (const hue of FILL_HUES) {
      for (const m of pair(hue, TRACK)) {
        expect(band.get(m.theme) ?? 0, `${m.theme}: band vs ${hue} on the track`).toBeLessThan(
          m.ratio,
        );
      }
    }
  });

  it("keeps its distance from every stroke the fill can take, not just the three seeds", () => {
    // The fill is a continuous ramp between the seeds, so a mid-mix can sit close to a band that passes every seed
    // (the light theme shipped a 0.0664 pair this way).
    for (const m of rampSeparation()) {
      expect(m.dE, `${m.theme}: band vs the nearest fill stroke (${m.at})`).toBeGreaterThanOrEqual(
        MIN_RAMP_SEPARATION,
      );
    }
  });
});
