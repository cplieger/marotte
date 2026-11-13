// A sidebar section header's inks over the sidebar and over the header's own hover, in both
// themes (WCAG 1.4.3, 4.5:1: the name and the count are `--fs-sm` text). Every ink and fill is
// read from the stylesheet and measured by css-contrast.py, so a CSS change cannot pass silently.

import { describe, it, expect } from "vitest";
import { execFileSync } from "node:child_process";
import { existsSync, readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

import { ruleBody } from "./__test-helpers__/css-rules.js";

const here = dirname(fileURLToPath(import.meta.url));
const script = join(here, "..", "scripts", "css-contrast.py");

// `node:fs`, because a `*.css?raw` import is empty in the node project.
// The leading newline lets `ruleBody` find the file's first rule by its selector line.
const shell = `\n${readFileSync(join(here, "css", "10-shell-app.css"), "utf8")}`;
const tabs = readFileSync(join(here, "css", "12-tabs.css"), "utf8");

const uncommented = (css: string): string => css.replace(/\/\*[\s\S]*?\*\//g, " ");

function decl(body: string, prop: string): string | undefined {
  const m = new RegExp(`(?:^|[;{\\s])${prop}:\\s*([^;]+);`).exec(uncommented(body));
  return m?.[1]?.trim();
}

function own(body: string, prop: string): string {
  // The rule's own declarations, before its first nested block.
  const head = uncommented(body).split("{")[0] ?? "";
  const v = decl(head, prop);
  expect(v, `${prop} is declared`).toBeDefined();
  return v ?? "";
}

function hoverBlock(body: string): string {
  const clean = uncommented(body);
  const at = clean.indexOf("&:hover");
  expect(at, "the header declares a hover").toBeGreaterThan(-1);
  const open = clean.indexOf("{", at);
  return clean.slice(open + 1, clean.indexOf("}", open));
}

function ratios(fg: string, bg: string): { theme: string; ratio: number }[] {
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
    `both themes for ${fg} on ${bg}`,
  ).toEqual(["dark", "light"]);
  return rows;
}

/** The header's rule, its hover block and the two backdrops its inks sit on. */
function header(): { rule: string; hover: string; hoverFill: string; sidebar: string } {
  const rule = ruleBody(tabs, ".side-section-toggle");
  const hover = hoverBlock(rule);
  return {
    rule,
    hover,
    hoverFill: decl(hover, "background") ?? decl(hover, "background-color") ?? "",
    sidebar: own(ruleBody(shell, '[id="sidebar"]'), "background"),
  };
}

function expectFloor(what: string, ink: string, bg: string): void {
  for (const m of ratios(ink, bg)) {
    expect(m.ratio, `${m.theme}: ${what} (${ink} on ${bg})`).toBeGreaterThanOrEqual(4.5);
  }
}

// The script lives outside static-src, and Stryker's sandbox copies static-src alone.
describe.skipIf(!existsSync(script))("a section header's inks", () => {
  it("paints the hover with --c-hover", () => {
    expect(header().hoverFill).toBe("var(--c-hover)");
  });

  it("holds the name's secondary ink to 4.5:1 at rest and hovered", () => {
    const h = header();
    const rest = own(h.rule, "color");
    expect(rest).toBe("var(--c-text-secondary)");
    expectFloor("the name at rest", rest, h.sidebar);
    expectFloor(
      "the name hovered",
      decl(h.hover, "color") ?? rest,
      `over(${h.hoverFill}, ${h.sidebar})`,
    );
  });

  it("holds the count's tertiary ink to 4.5:1 at rest and hovered", () => {
    const h = header();
    const ink = own(ruleBody(tabs, ".side-section-count"), "color");
    expect(ink).toBe("var(--c-text-tertiary)");
    expectFloor("the count at rest", ink, h.sidebar);
    expectFloor("the count hovered", ink, `over(${h.hoverFill}, ${h.sidebar})`);
  });
});
