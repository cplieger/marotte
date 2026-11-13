// A Recently closed row's inks and the History row's, over the sidebar and over their own hover,
// in both themes (WCAG 1.4.3, 4.5:1: the name and the time are `--fs-sm` text). Every ink and fill
// is read from the stylesheet and measured by css-contrast.py, so a CSS change cannot pass silently.

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
  expect(at, "the row declares a hover").toBeGreaterThan(-1);
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

function expectFloor(what: string, ink: string, bg: string): void {
  for (const m of ratios(ink, bg)) {
    expect(m.ratio, `${m.theme}: ${what} (${ink} on ${bg})`).toBeGreaterThanOrEqual(4.5);
  }
}

const sidebar = (): string => own(ruleBody(shell, '[id="sidebar"]'), "background");

/** A row's rule, its hover block and the hover's fill. */
function row(selector: string): { rule: string; hover: string; hoverFill: string } {
  const rule = ruleBody(tabs, selector);
  const hover = hoverBlock(rule);
  return {
    rule,
    hover,
    hoverFill: decl(hover, "background") ?? decl(hover, "background-color") ?? "",
  };
}

// The script lives outside static-src, and Stryker's sandbox copies static-src alone.
describe.skipIf(!existsSync(script))("a Recently closed row's inks", () => {
  it.each([".recent-row", ".side-history-row"])("paints %s's hover with --c-hover", (sel) => {
    expect(row(sel).hoverFill).toBe("var(--c-hover)");
  });

  it.each([".recent-row", ".side-history-row"])(
    "holds %s's secondary ink to 4.5:1 at rest and hovered",
    (sel) => {
      const r = row(sel);
      const rest = own(r.rule, "color");
      expect(rest).toBe("var(--c-text-secondary)");
      expectFloor(`${sel} at rest`, rest, sidebar());
      expectFloor(
        `${sel} hovered`,
        decl(r.hover, "color") ?? rest,
        `over(${r.hoverFill}, ${sidebar()})`,
      );
    },
  );

  it("holds the close time's tertiary ink to 4.5:1 at rest and hovered", () => {
    const r = row(".recent-row");
    const ink = own(ruleBody(tabs, ".recent-row-time"), "color");
    expect(ink).toBe("var(--c-text-tertiary)");
    expectFloor("the time at rest", ink, sidebar());
    expectFloor("the time hovered", ink, `over(${r.hoverFill}, ${sidebar()})`);
  });

  it("declares no token of its own", () => {
    const tokens = readFileSync(join(here, "css", "01-tokens.css"), "utf8");
    for (const sel of [".recent-row", ".recent-row-time", ".side-history-row"]) {
      const body = uncommented(ruleBody(tabs, sel));
      expect(/(?:^|[;{\s])--[\w-]+\s*:/.test(body), `${sel} declares a custom property`).toBe(
        false,
      );
      for (const [, name] of body.matchAll(/var\((--[\w-]+)/g)) {
        expect(
          tokens.includes(`${name ?? ""}:`),
          `${sel} reads ${name ?? ""} from the tokens`,
        ).toBe(true);
      }
    }
  });
});
