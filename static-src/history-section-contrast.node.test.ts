// The History section's closed rows' inks, More's and the Runs rows', over the sidebar and over their own hover,
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

function nestedBlock(body: string, selector: string): string {
  const clean = uncommented(body);
  const at = clean.indexOf(selector);
  expect(at, `the row declares ${selector}`).toBeGreaterThan(-1);
  const open = clean.indexOf("{", at);
  return clean.slice(open + 1, clean.indexOf("}", open));
}

const hoverBlock = (body: string): string => nestedBlock(body, "&:hover");

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

/** The rule the closed rows' items, More and the Runs rows paint from; a row's button takes its item's ink. */
const ROWS = ":is(.history-item, .history-more, .runs-item)";

// The script lives outside static-src, and Stryker's sandbox copies static-src alone.
describe.skipIf(!existsSync(script))("the History section's row inks", () => {
  it("leaves the closed row's inks and fills to its item", () => {
    const body = uncommented(ruleBody(tabs, ".history-row"));
    expect(decl(body, "color"), "the row inherits its item's ink").toBe("inherit");
    expect(body).not.toMatch(/(?:^|[;{\s])background(?:-color)?:/);
    expect(body).not.toContain(":hover");
  });

  it("paints the rows' hover with --c-hover", () => {
    expect(row(ROWS).hoverFill).toBe("var(--c-hover)");
  });

  // The hover floors below then hold the row a menu is open on too.
  it("paints a row whose menu is open with the hover's fill and ink", () => {
    const r = row(ROWS);
    const mark = nestedBlock(r.rule, "&:has(> .history-row[data-menu-open])");
    expect(decl(mark, "background")).toBe(r.hoverFill);
    expect(decl(mark, "color")).toBe(decl(r.hover, "color"));
  });

  it("holds the rows' secondary ink to 4.5:1 at rest and hovered", () => {
    const r = row(ROWS);
    const rest = own(r.rule, "color");
    expect(rest).toBe("var(--c-text-secondary)");
    expectFloor("a row at rest", rest, sidebar());
    expectFloor(
      "a row hovered",
      decl(r.hover, "color") ?? rest,
      `over(${r.hoverFill}, ${sidebar()})`,
    );
  });

  // A live run is not a closed chat, so its row takes the tab rows' primary ink over the same fills.
  it("holds the Runs rows' primary ink to 4.5:1 at rest and hovered", () => {
    const r = row(ROWS);
    const ink = own(ruleBody(tabs, ".runs-item"), "color");
    expect(ink).toBe("var(--c-text-primary)");
    expectFloor("a run row at rest", ink, sidebar());
    expectFloor("a run row hovered", ink, `over(${r.hoverFill}, ${sidebar()})`);
  });

  it("holds the close time's tertiary ink to 4.5:1 at rest and hovered", () => {
    const r = row(ROWS);
    const ink = own(ruleBody(tabs, ".history-row-time"), "color");
    expect(ink).toBe("var(--c-text-tertiary)");
    expectFloor("the time at rest", ink, sidebar());
    expectFloor("the time hovered", ink, `over(${r.hoverFill}, ${sidebar()})`);
  });

  // WCAG 1.4.11: the trash glyph is a graphical object, 3:1, over the hovered row and over its own hover.
  it("holds Delete's glyph to 3:1 on a hovered row and on its own hover", () => {
    const r = row(ROWS);
    const del = ruleBody(tabs, ".history-row-delete");
    const ink = own(del, "color");
    expect(ink).toBe("var(--c-text-tertiary)");
    const own3 = decl(hoverBlock(del), "background") ?? "";
    for (const [what, fg, bg] of [
      ["Delete on a hovered row", ink, `over(${r.hoverFill}, ${sidebar()})`],
      ["Delete hovered", decl(hoverBlock(del), "color") ?? ink, `over(${own3}, ${sidebar()})`],
    ] as const) {
      for (const m of ratios(fg, bg)) {
        expect(m.ratio, `${m.theme}: ${what} (${fg} on ${bg})`).toBeGreaterThanOrEqual(3);
      }
    }
  });

  // WCAG 1.4.11: the closed ring is the row's one state mark, 3:1 at rest and on a hovered row.
  it("holds the closed ring to 3:1 at rest and on a hovered row", () => {
    const r = row(ROWS);
    const ring = ruleBody(tabs, '.tab-status-dot[data-status="closed"]');
    expect(decl(ring, "background"), "hollow").toBe("transparent");
    const ink = decl(ruleBody(tabs, ".tab-status-dot"), "--dot-color") ?? "";
    expect(ink).toBe("var(--c-dot-idle)");
    for (const [what, bg] of [
      ["the ring at rest", sidebar()],
      ["the ring on a hovered row", `over(${r.hoverFill}, ${sidebar()})`],
    ] as const) {
      // The ring's ink is translucent, so it is composited over the fill it sits on.
      for (const m of ratios(`over(${ink}, ${bg})`, bg)) {
        expect(m.ratio, `${m.theme}: ${what} (${ink} on ${bg})`).toBeGreaterThanOrEqual(3);
      }
    }
  });

  it("declares no token of its own", () => {
    const tokens = readFileSync(join(here, "css", "01-tokens.css"), "utf8");
    for (const sel of [ROWS, ".history-row", ".history-row-time"]) {
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
