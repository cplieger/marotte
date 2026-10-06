// Vertical centring of a text label in a flex row, pinned as a source fact (no app stylesheet or webfont here).
// `align-items: center` centres the box, which sits low of the glyph band by ((ascent - descent)/2 - band/2)/em.
// Line-height cannot fix it (CSS2.1 10.8.1 leading is symmetric) and `line-height: 1` clips descenders. Guarded: no
// numeric line-height returns, and every trimmed label that clips has symmetric descender room.

import { describe, it, expect } from "vitest";
import { loadCSS } from "./__test-helpers__/css-rules.js";

/** The labels the trim applies to, and where each rule lives. */
const TRIMMED: { name: string; sheet: string; clips: boolean }[] = [
  // Digits and a percent sign only: no descender.
  { name: "context-label", sheet: "15-input.css", clips: false },
  { name: "ctx-model-pill", sheet: "15-input.css", clips: true },
  { name: "pill-role-label", sheet: "15-input.css", clips: true },
  { name: "sidebar-email", sheet: "10-shell-app.css", clips: true },
  // Sits beside an 8px dot, a workflow mark and the ×, so an untrimmed box read low against all three.
  { name: "tab-name", sheet: "10-shell-app.css", clips: true },
  // Shares its flex line with `#ctx-model-pill`, so untrimmed the two labels disagreed.
  { name: "pill-model-effort", sheet: "15-input.css", clips: false },
  // Every row was low; the active one's accent fill showed it.
  { name: "pill-model-item", sheet: "15-input.css", clips: false },
  // The effort knob carries no text, and `.effort-label` sits on its own line.
];

const SHEETS = ["15-input.css", "10-shell-app.css"];

/** The comments here quote `line-height: 1` to explain why it is wrong. */
function source(sheet: string): string {
  return loadCSS(sheet).replace(/\/\*[\s\S]*?\*\//g, " ");
}

/** Selectors in id, attribute and class form across two files, one nested (`& [id="ctx-model-pill"]`). */
function declarationsFor(sheet: string, name: string): string {
  const css = source(sheet);
  // Boundary-anchored: `.pill` must not match `.pill-expand-content`.
  const wanted = [
    new RegExp(`#${name}(?![\\w-])`),
    new RegExp(`\\[id="${name}"\\]`),
    new RegExp(`\\.${name}(?![\\w-])`),
  ];
  const out: string[] = [];
  let depth = 0;
  let selStart = 0;
  const stack: { sel: string; bodyStart: number }[] = [];
  for (let i = 0; i < css.length; i++) {
    if (css[i] === "{") {
      stack.push({ sel: css.slice(selStart, i).trim(), bodyStart: i + 1 });
      depth++;
    } else if (css[i] === "}") {
      depth--;
      const frame = stack.pop();
      if (frame !== undefined && wanted.some((w) => w.test(frame.sel))) {
        // Own declarations only. `[^{};]*` for the nested selector: `[^{}]*` reaches back across `;` and takes the parent's
        // declarations with the block.
        out.push(css.slice(frame.bodyStart, i).replace(/[^{};]*\{[^{}]*\}/g, " "));
      }
      selStart = i + 1;
    } else if (depth === 0 && css[i] === ";") {
      selStart = i + 1;
    }
  }
  return out.join(";");
}

describe("label centring", () => {
  it("trims every label's box to its cap band", () => {
    for (const label of TRIMMED) {
      expect(
        declarationsFor(label.sheet, label.name),
        `${label.sheet} must trim ${label.name} to 'trim-both cap alphabetic': that makes the box edges ` +
          `the cap band, so centring the box centres the letterforms and the offset is zero by ` +
          `construction rather than by a value authored per font`,
      ).toMatch(/text-box:\s*trim-both\s+cap\s+alphabetic/);
    }
  });

  it("gives every trimmed label that clips symmetric descender room", () => {
    // The trim lifts the block-end edge to the baseline and `overflow: hidden` cuts descenders; symmetric padding keeps the
    // cap band centred, since `align-items: center` centres the margin box.
    const missing: string[] = [];
    for (const label of TRIMMED) {
      const decls = declarationsFor(label.sheet, label.name);
      const clips = /overflow:\s*hidden/.test(decls);
      expect(
        clips,
        `${label.name} is recorded as ${label.clips ? "clipping" : "not clipping"} but its rules say otherwise; ` +
          `update the table above with the reason`,
      ).toBe(label.clips);
      if (!clips) {
        continue;
      }
      // One `padding-block` value or an equal logical pair: a block-end-only pad shifts the cap band.
      if (!/padding-block:\s*[\d.]+em\s*;/.test(decls)) {
        missing.push(
          `${label.sheet}: ${label.name} is trimmed and clips, with no symmetric padding-block`,
        );
      }
    }
    expect(
      missing,
      "a cap/alphabetic trim under `overflow: hidden` clips descenders. That is the regression " +
        "`line-height: 1` would have shipped for no visible gain, and it is just as available here.",
    ).toEqual([]);
  });

  it("declares no numeric line-height on any of them", () => {
    // A `line-height` here looks like the fix and measures as nothing. `.pill-role-icon`'s `line-height: 0` stays: its
    // content is a replaced element whose strut oversizes it.
    const offenders: string[] = [];
    for (const sheet of SHEETS) {
      for (const label of [...TRIMMED.map((t) => t.name), "pill"]) {
        const decls = declarationsFor(sheet, label);
        const m = /line-height:\s*([^;]+)/.exec(decls);
        const value = m?.[1]?.trim() ?? "";
        if (value !== "" && value !== "0") {
          offenders.push(`${sheet}: ${label} declares line-height: ${value}`);
        }
      }
    }
    expect(
      offenders,
      "the vertical offset is ((ascent - descent)/2 - band/2)em and has no line-height term in it. " +
        "Adding one moves the letterforms by zero and shrinks the box out from under the descenders.",
    ).toEqual([]);
  });
});
