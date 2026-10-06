// Guards on the token layer (css/01-tokens.css and its consumers), over the shipped sheets: every token read is
// declared (a `var(--x, fallback)` hides an undeclared token from stylelint's `no-unknown-custom-properties`), and a
// selected fill never ships without its ink (70-selection.css).

import { describe, it, expect } from "vitest";
import { readFileSync, readdirSync, existsSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));
const cssDir = join(here, "css");
const vendorDirs = [
  join(here, "node_modules", "@cplieger", "ui-primitives", "css"),
  join(here, "node_modules", "@cplieger", "web-terminal-ui", "css"),
];

interface Sheet {
  name: string;
  text: string;
}

function readSheets(dir: string): Sheet[] {
  if (!existsSync(dir)) {
    return [];
  }
  return readdirSync(dir)
    .filter((f) => f.endsWith(".css"))
    .sort()
    .map((f) => ({ name: f, text: readFileSync(join(dir, f), "utf8") }));
}

function stripComments(css: string): string {
  return css.replace(/\/\*[\s\S]*?\*\//g, (m) => "\n".repeat((m.match(/\n/g) ?? []).length));
}

/** Every pattern here declares group 1 as mandatory, so a missing group is a broken pattern, asserted once here. */
function capture(m: RegExpExecArray, re: RegExp): string {
  const group = m[1];
  if (group === undefined) {
    throw new Error(`${re.source} matched ${JSON.stringify(m[0])} with no capture group 1`);
  }
  return group;
}

function* captures(text: string, re: RegExp): Generator<string> {
  for (const m of text.matchAll(re)) {
    yield capture(m, re);
  }
}

const DECLARATION = /(--[\w-]+)\s*:/g;
const VAR_READ = /var\(\s*(--[\w-]+)/g;
const INNERMOST_BLOCK = /\{([^{}]*)\}/g;
// oklch(<L> 0 <hue>) — chroma written as a bare zero, hue anything but `none`.
const ZERO_CHROMA = /oklch\(\s*[\d.]+%?\s+0(?:\.0+)?\s+([^)/\s]+)/g;

const appSheets = readSheets(cssDir);
const vendorSheets = vendorDirs.flatMap(readSheets);

function declaredIn(sheets: Sheet[]): Set<string> {
  const out = new Set<string>();
  for (const s of sheets) {
    for (const name of captures(stripComments(s.text), DECLARATION)) {
      out.add(name);
    }
    // @property registers a name with an initial value, which counts as a declaration.
    for (const name of captures(s.text, /@property\s+(--[\w-]+)/g)) {
      out.add(name);
    }
  }
  return out;
}

/**
 * Names a TS module writes at runtime count as declared. Test files are excluded: a test naming a token is not a
 * declaration, and counting one let a token stay read by a sheet and declared nowhere.
 */
function writtenByScript(): Set<string> {
  const out = new Set<string>();
  const walk = (dir: string): void => {
    for (const entry of readdirSync(dir, { withFileTypes: true })) {
      if (entry.name === "node_modules" || entry.name === "dist") {
        continue;
      }
      const p = join(dir, entry.name);
      if (entry.isDirectory()) {
        walk(p);
      } else if (entry.name.endsWith(".ts") && !entry.name.endsWith(".test.ts")) {
        // Comments stripped: a doc comment naming a property in backticks is not a write.
        for (const name of captures(
          stripComments(readFileSync(p, "utf8")),
          /["'`](--[\w-]+)["'`]/g,
        )) {
          out.add(name);
        }
      }
    }
  };
  walk(here);
  return out;
}

describe("design tokens are declared before they are read", () => {
  it("has no var() read — fallback or not — without a declaration", () => {
    const declared = new Set([
      ...declaredIn(appSheets),
      ...declaredIn(vendorSheets),
      ...writtenByScript(),
    ]);

    const orphans: string[] = [];
    for (const sheet of appSheets) {
      const body = stripComments(sheet.text);
      const lines = body.split("\n");
      lines.forEach((line, i) => {
        // The name is var()'s first argument; a fallback is deliberately not what makes the read legitimate.
        for (const name of captures(line, VAR_READ)) {
          if (!declared.has(name)) {
            orphans.push(`${sheet.name}:${i + 1} reads ${name}`);
          }
        }
      });
    }

    expect(
      orphans,
      "A custom property read but never declared is a literal wearing a token's " +
        "name: the value lives in each call site's fallback, so changing it means " +
        "finding every site. Declare it in css/01-tokens.css.",
    ).toEqual([]);
  });

  it("has no colour or elevation token declared with nothing reading it", () => {
    // The inverse: a colour, shadow or elevation token with no reader is dead, since it is chosen for a surface.
    // Geometry and motion tokens are a vocabulary and out of scope. The four exempt `--c-term-*` members are the
    // terminal engine's cursor and selection vocabulary, unwired today (shell.ts's SHELL_THEME maps five other variables).
    const scoped = /^--(c|shadow|elev)-/;
    const vocabulary = /^--c-term-(cursor|cursor-accent|selection|selection-inactive)$/;

    const declared = new Map<string, string>();
    for (const sheet of appSheets) {
      for (const name of captures(stripComments(sheet.text), DECLARATION)) {
        if (scoped.test(name) && !vocabulary.test(name) && !declared.has(name)) {
          declared.set(name, sheet.name);
        }
      }
    }

    // A reader is any var() in any sheet, a name a TS module writes as a string literal, or a use inside another token.
    const read = new Set<string>([...writtenByScript()]);
    for (const sheet of [...appSheets, ...vendorSheets]) {
      for (const name of captures(stripComments(sheet.text), VAR_READ)) {
        read.add(name);
      }
    }

    const orphans = [...declared]
      .filter(([name]) => !read.has(name))
      .map(([name, where]) => `${where} declares ${name}, nothing reads it`);

    expect(
      orphans,
      "A token nobody reads is either a value that was chosen and never wired " +
        "up, or one whose last consumer went away. Wire it or delete it — a " +
        "green battery over an unconsumed token is how a half-finished change " +
        "looks finished.",
    ).toEqual([]);
  });

  it("declares exactly one spelling of the pill radius", () => {
    // A stadium is reserved for non-interactive status badges, so a 999px/999rem literal is a shape decided off-ladder.
    const literals: string[] = [];
    for (const sheet of appSheets) {
      if (sheet.name === "01-tokens.css") {
        continue;
      }
      stripComments(sheet.text)
        .split("\n")
        .forEach((line, i) => {
          if (/border(-[a-z]+)*-radius:[^;]*\b999(px|rem)\b/.test(line)) {
            literals.push(`${sheet.name}:${i + 1}`);
          }
        });
    }
    expect(literals, "Use var(--r-pill); a stadium is a badge, not a button.").toEqual([]);
  });

  it("declares exactly one spelling of the focus ring", () => {
    // The focus ring's width and style are tokens with one owner. Two offsets: outside the edge, or inside for a
    // full-bleed row whose container would clip an outside ring.
    const literals: string[] = [];
    for (const sheet of appSheets) {
      if (sheet.name === "01-tokens.css") {
        continue;
      }
      const lines = stripComments(sheet.text).split("\n");
      lines.forEach((line, i) => {
        if (/outline:\s*2px\s+solid\s+var\(--c-accent\)/.test(line)) {
          literals.push(`${sheet.name}:${i + 1} spells the ring out`);
        }
        // An offset belongs to this vocabulary only beside the ring; a flash keyframe and the danger ring carry their own.
        if (
          /^\s*outline-offset:\s*-?[\d.]/.test(line) &&
          /outline:\s*var\(--focus-ring\)/.test(lines[i - 1] ?? "")
        ) {
          literals.push(`${sheet.name}:${i + 1} spells the offset out`);
        }
      });
    }
    expect(
      literals,
      "Use var(--focus-ring) with var(--focus-offset) or var(--focus-offset-inset).",
    ).toEqual([]);
  });
});

function rules(css: string): { selector: string; body: string; line: number }[] {
  const out: { selector: string; body: string; line: number }[] = [];
  const text = stripComments(css);
  let depth = 0;
  let selStart = 0;
  let bodyStart = 0;
  let selector = "";
  for (let i = 0; i < text.length; i++) {
    const ch = text[i];
    if (ch === "{") {
      if (depth === 0) {
        selector = text.slice(selStart, i).trim();
        bodyStart = i + 1;
      }
      depth++;
    } else if (ch === "}") {
      depth--;
      if (depth === 0) {
        out.push({
          selector,
          body: text.slice(bodyStart, i),
          line: text.slice(0, selStart).split("\n").length,
        });
        selStart = i + 1;
      }
    }
  }
  return out;
}

function ownDeclarations(body: string): string[] {
  let depth = 0;
  let buf = "";
  for (const ch of body) {
    if (ch === "{") {
      depth++;
    } else if (ch === "}") {
      depth--;
    } else if (depth === 0) {
      buf += ch;
    }
  }
  return buf.split(";").map((d) => d.trim());
}

describe("the selected state carries fill and ink together", () => {
  const FILL = /^(background|background-color)\s*:\s*var\(--c-selected-bg\)/;

  // Two channels, fill and ink: a Chromium census found the edge doing nothing on most selectors
  // (70-selection.css's header has it).
  it("never sets the selected fill without its ink", () => {
    const broken: string[] = [];
    for (const sheet of appSheets) {
      for (const rule of rules(sheet.text)) {
        // A @keyframes step animates what it animates; it has no resting treatment.
        if (/^@keyframes\b/.test(rule.selector)) {
          continue;
        }
        // Recurse one level so a selected rule inside @media / @supports is checked too.
        const blocks = rule.selector.startsWith("@") ? rules(rule.body) : [rule];
        for (const block of blocks) {
          const decls = ownDeclarations(block.body);
          if (!decls.some((d) => FILL.test(d))) {
            continue;
          }
          if (!decls.some((d) => /^color\s*:/.test(d))) {
            broken.push(`${sheet.name}:${rule.line} ${block.selector} is missing color`);
          }
        }
      }
    }
    expect(
      broken,
      "A selected surface that fills without colouring its ink is how nine " +
        "different selected-state recipes happened. Set both, or add the " +
        "selector to the shared rule in css/70-selection.css.",
    ).toEqual([]);
  });

  it("keeps the treatment in one place rather than per surface", () => {
    const owner = appSheets.find((s) => s.name === "70-selection.css");
    expect(owner, "css/70-selection.css is the selected-state owner").toBeDefined();

    const elsewhere: string[] = [];
    for (const sheet of appSheets) {
      if (sheet.name === "70-selection.css" || sheet.name === "01-tokens.css") {
        continue;
      }
      for (const rule of rules(sheet.text)) {
        // A keyframe may borrow the fill as a transient attention flash.
        if (/^@keyframes\b/.test(rule.selector)) {
          continue;
        }
        const offset = rule.line;
        rule.body.split("\n").forEach((line, i) => {
          if (/var\(--c-selected-(bg|fg)\)/.test(line)) {
            elsewhere.push(`${sheet.name}:${offset + i}`);
          }
        });
      }
    }
    // 10-shell-app.css keeps the tab strip's selected hover/press and close-button ink: at (0,3,0) and (0,2,1) they
    // still beat 70-selection.css.
    expect(
      elsewhere.filter((l) => !l.startsWith("10-shell-app.css")),
      "Add the selector to css/70-selection.css instead of re-deriving the " +
        "treatment on one more surface.",
    ).toEqual([]);
  });
});

describe("an achromatic colour leaves its hue powerless", () => {
  // A hue is powerless only when MISSING: `oklch(100% 0 0deg)` carries a real hue, so every `color-mix(in oklch, …)`
  // with that ink swung toward red, while `scripts/css-contrast.py` (powerless rule) reported no drift. Write `none`.
  // Scoped to chroma 0, where the hue carries no information.
  it("authors no zero-chroma oklch() with an explicit hue", () => {
    const offenders: string[] = [];
    for (const sheet of appSheets) {
      stripComments(sheet.text)
        .split("\n")
        .forEach((line, i) => {
          // ZERO_CHROMA matches the declaration form only; a mix's arguments are var() references.
          for (const m of line.matchAll(ZERO_CHROMA)) {
            if (capture(m, ZERO_CHROMA) !== "none") {
              offenders.push(`${sheet.name}:${i + 1} - ${m[0]})`);
            }
          }
        });
    }
    expect(
      offenders,
      "A zero-chroma oklch() must write its hue as `none`, or color-mix() will " +
        "interpolate that hue and rotate whatever it is mixed with.",
    ).toEqual([]);
  });
});

describe("an ink is only paired with a fill it clears", () => {
  // The ink ramp clears every resting rung text sits on (ink-ramp.node.test.ts). --c-bg-elevated hosts primary and
  // secondary ink only: the hint ink measures 4.10:1 dark / 4.19:1 light there, and as a resting declaration that
  // pairing is checkable from one block.
  const RAISED = /background(?:-color)?\s*:\s*[^;]*var\(--c-bg-elevated\)/;
  // Anchored so `border-color` / `outline-color` are excluded: the hint ink may draw an edge at the 3:1 graphic floor.
  const HINT = /(?<![-\w])color:\s*var\(--c-text-tertiary\)/;

  it("keeps the hint ink off the elevated fill", () => {
    const offenders: string[] = [];
    for (const sheet of appSheets) {
      const text = stripComments(sheet.text);
      // Innermost blocks only: a nested raised fill with the ink on its parent is not decidable from one block.
      for (const m of text.matchAll(INNERMOST_BLOCK)) {
        const body = capture(m, INNERMOST_BLOCK);
        if (RAISED.test(body) && HINT.test(body)) {
          offenders.push(`${sheet.name}:${text.slice(0, m.index).split("\n").length}`);
        }
      }
    }
    expect(
      offenders,
      "--c-bg-elevated is a two-level surface: it hosts --c-text-primary and " +
        "--c-text-secondary only. Step the hint ink up to --c-text-secondary there.",
    ).toEqual([]);
  });

  // An `opacity` below 1 multiplies the ink's measured contrast, and scripts/css-contrast.py cannot see it (it reads the
  // token graph). Every offender so far declared both in one block. Exempt: an inactive control (WCAG 1.4.3) and chrome,
  // keyed on the selector.
  const INACTIVE_STATE =
    /:disabled|\[disabled\]|aria-disabled|aria-busy|-cloning\b|-rejected\b|\.btn-loading\b|-disabled\b/;
  // Containers whose `color` only feeds an svg's `currentColor` (WCAG 1.4.11's 3:1, not 1.4.3). An explicit list, so a
  // name pattern cannot adopt a control that holds a label. Each entry carries its measured ratio.
  const GRAPHIC_ONLY = new Set([
    // 48px empty-state illustration on --c-bg-primary: 3.582 dark / 3.044 light.
    ".git-multirepo-empty-icon",
    // 1.5rem steer-row glyph button on --c-bg-secondary: 5.214 dark / 3.997 light.
    ".steer-act",
  ]);
  const TEXT_INK =
    /(?<![-\w])color:\s*var\(--c-text-(?:primary|secondary|tertiary|control|aside)\)/;
  const DIMMED = /(?<![-\w])opacity:\s*0?\.\d+/;

  it("never dims a text ink with opacity", () => {
    const offenders: string[] = [];
    for (const sheet of appSheets) {
      const text = stripComments(sheet.text);
      for (const m of text.matchAll(INNERMOST_BLOCK)) {
        const body = capture(m, INNERMOST_BLOCK);
        if (!TEXT_INK.test(body) || !DIMMED.test(body)) {
          continue;
        }
        const line = text.slice(0, m.index).split("\n").length;
        const before = text.slice(0, m.index).split("\n");
        const selector = (before[before.length - 1] ?? "").trim();
        if (INACTIVE_STATE.test(selector)) {
          continue;
        }
        if (GRAPHIC_ONLY.has(selector.replace(/\s*\{$/, "").trim())) {
          continue;
        }
        offenders.push(`${sheet.name}:${line} ${selector}`);
      }
    }
    expect(
      offenders,
      "An `opacity` multiplies the ink's measured contrast and is invisible to " +
        "both contrast gates, so the number the tools report is not the number on " +
        "screen. Dim active text by choosing an ink token (--c-text-aside for " +
        "subordinate prose); keep opacity for chrome and inactive controls, and " +
        "add a graphic-only container to GRAPHIC_ONLY with its measured ratio.",
    ).toEqual([]);
  });

  it("keeps a status ink off the top rung", () => {
    // red 2.797, danger 2.585, warning 2.980 against --c-bg-elevated, under a coloured glyph's 3:1.
    const hue = /(?<![-\w])color:\s*var\(--c-(?:red|danger|warning)\)/;
    const top = /background(?:-color)?\s*:\s*[^;]*var\(--c-bg-elevated\)/;
    const offenders: string[] = [];
    for (const sheet of appSheets) {
      const text = stripComments(sheet.text);
      for (const m of text.matchAll(INNERMOST_BLOCK)) {
        const body = capture(m, INNERMOST_BLOCK);
        if (top.test(body) && hue.test(body)) {
          offenders.push(`${sheet.name}:${text.slice(0, m.index).split("\n").length}`);
        }
      }
    }
    expect(offenders, "Use the on-selected ink family, or a lower rung.").toEqual([]);
  });
});
