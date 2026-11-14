// The context pill's share is text, so every ink its ramp can emit clears 4.5:1 (WCAG 1.4.3) on each fill the pill
// paints with that ink, in both themes. The shades are read out of 15-input.css and the ramp out of `contextInk`.

import { describe, it, expect } from "vitest";
import { execFileSync } from "node:child_process";
import { existsSync, readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { ruleBody } from "./__test-helpers__/css-rules.js";
import { contextInk } from "./context-indicator.js";

const here = dirname(fileURLToPath(import.meta.url));
const script = join(here, "..", "scripts", "css-contrast.py");
const css = (): string => readFileSync(join(here, "css", "15-input.css"), "utf8");

/** The `--ctx-ink-*` shades a rule declares. */
function shades(selector: string): Map<string, string> {
  const body = ruleBody(css(), selector).replace(/\/\*[\s\S]*?\*\//g, " ");
  const found = new Map(
    [...body.matchAll(/(--ctx-ink-[a-z]+):\s*([^;]+);/g)].map((m) => [m[1]!, m[2]!.trim()]),
  );
  expect([...found.keys()].sort(), `${selector} declares every shade`).toEqual([
    "--ctx-ink-green",
    "--ctx-ink-red",
    "--ctx-ink-yellow",
  ]);
  return found;
}

/** Every distinct ink the ramp emits, across the compaction points the slider reaches. */
function ramp(): string[] {
  const out = new Set<string>();
  for (const t of [50, 80, 100]) {
    for (let i = 0; i <= 1000; i++) {
      out.add(contextInk(i / 10, t));
    }
  }
  return [...out];
}

const WORST_PY = `
import importlib.util, sys
spec = importlib.util.spec_from_file_location("cc", sys.argv[1])
cc = importlib.util.module_from_spec(spec)
spec.loader.exec_module(cc)
bg = sys.argv[2]
inks = sys.stdin.read().split("\\n")
for th in cc.parse_themes():
    b = th.resolve("var(%s)" % bg)
    worst = min((cc.contrast(th.resolve(e).over(b), b), e) for e in inks if e)
    print("%s\\t%.4f\\t%s" % (th.name, worst[0], worst[1]))
`;

/** The lowest ratio any ink reaches on `bg`, per theme. */
function worst(inks: string[], bg: string): { theme: string; ratio: number; ink: string }[] {
  const out = execFileSync("python3", ["-c", WORST_PY, script, bg], {
    encoding: "utf8",
    input: inks.join("\n"),
  });
  const rows = out
    .trim()
    .split("\n")
    .map((line) => {
      const [theme = "", ratio = "", ink = ""] = line.split("\t");
      return { theme, ratio: Number(ratio), ink };
    });
  expect(
    rows.map((r) => r.theme),
    `both themes on ${bg}`,
  ).toEqual(["dark", "light"]);
  return rows;
}

function resolved(selector: string): string[] {
  const s = shades(selector);
  return ramp().map((e) =>
    e.replace(/var\((--ctx-ink-[a-z]+)\)/g, (_, name: string) => s.get(name)!),
  );
}

// Stryker's sandbox copies static-src alone and the script lives outside it, so its absence is a skip.
describe.skipIf(!existsSync(script))("the context pill's share, as text", () => {
  it.each(["--c-bg-primary", "--c-bg-tertiary"])("clears 4.5:1 at rest and on hover (%s)", (bg) => {
    for (const m of worst(resolved(".pill-context"), bg)) {
      expect(m.ratio, `${m.theme}: ${m.ink} on ${bg}`).toBeGreaterThanOrEqual(4.5);
    }
  });

  it.each(["--c-selected-bg", "--c-selected-bg-hover"])(
    "clears 4.5:1 with its card open (%s)",
    (bg) => {
      for (const m of worst(resolved(".pill-context.pill-expanded"), bg)) {
        expect(m.ratio, `${m.theme}: ${m.ink} on ${bg}`).toBeGreaterThanOrEqual(4.5);
      }
    },
  );
});
