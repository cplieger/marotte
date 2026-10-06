// The app's ONE status vocabulary (run card, exec tree, tool card through icons.ts). Each state
// carries EXACTLY ONE mark, so no row can paint a glyph AND a character.

import { describe, it, expect } from "vitest";
import {
  STATE_MARK,
  STATE_WORD,
  inFlight,
  neverRan,
  paintStateMark,
  settled,
  type ExecState,
} from "./status.js";

/** Every member of the vocabulary, listed once so the exhaustiveness assertions
 *  below cannot silently shrink when a state is added. */
const ALL: readonly ExecState[] = [
  "pending",
  "running",
  "waiting",
  "input",
  "unknown",
  "ok",
  "fail",
  "warn",
  "skipped",
];

describe("STATE_MARK is total, and every state carries exactly one channel", () => {
  it("covers the whole vocabulary and nothing else", () => {
    expect(Object.keys(STATE_MARK).sort()).toEqual([...ALL].sort());
    // Same population as the accessible-name table: a state with a mark and no
    // word (or the reverse) is a half-added state.
    expect(Object.keys(STATE_MARK).sort()).toEqual(Object.keys(STATE_WORD).sort());
  });

  it("gives the four in-flight states no mark, because CSS draws their ring", () => {
    for (const state of ["pending", "running", "waiting", "unknown"] as const) {
      expect(STATE_MARK[state]).toEqual({ kind: "none" });
    }
  });

  it("keeps a character for exactly the two states that earn one", () => {
    // `input` is the one step state a reader must ACT on, so a `?` says that where
    // a silhouette cannot; `skipped` means nothing happened, so it keeps a dash.
    expect(STATE_MARK.input).toEqual({ kind: "char", text: "?" });
    expect(STATE_MARK.skipped).toEqual({ kind: "char", text: "\u2013" });
    const chars = ALL.filter((s) => STATE_MARK[s].kind === "char");
    expect(chars).toEqual(["input", "skipped"]);
  });

  it("gives the three settled outcomes SVG marks, pairwise distinct", () => {
    const icons = ALL.filter((s) => STATE_MARK[s].kind === "icon");
    expect(icons).toEqual(["ok", "fail", "warn"]);
    const svgs = icons.map((s) => {
      const mark = STATE_MARK[s];
      return mark.kind === "icon" ? mark.svg : "";
    });
    // THE SHAPE CHANNEL. `fail` is red while `ok` and `warn` share nothing but
    // being settled, so if two states ever resolved to one glyph the surfaces
    // would separate them by hue alone (WCAG 1.4.1) — this is what fails then.
    expect(new Set(svgs).size).toBe(3);
    for (const svg of svgs) {
      expect(svg).toContain("<svg");
      // Solid fills, so they hold at 14px, and `currentColor` so the existing
      // tints reach them with no new colour token.
      expect(svg).toContain('fill="currentColor"');
    }
  });
});

// Three buckets, not two: the complement of `inFlight` is "ran or never will". Disjoint and
// covering, so a new state in `ALL` must be classified here.
describe("inFlight, neverRan and settled partition the vocabulary", () => {
  it("agrees on no state, pairwise", () => {
    expect(ALL.filter((s) => inFlight(s) && neverRan(s))).toEqual([]);
    expect(ALL.filter((s) => inFlight(s) && settled(s))).toEqual([]);
    expect(ALL.filter((s) => neverRan(s) && settled(s))).toEqual([]);
  });

  it("names the in-flight four", () => {
    expect(ALL.filter(inFlight)).toEqual(["running", "waiting", "input", "unknown"]);
  });

  it("names the two states with no execution behind them", () => {
    expect(ALL.filter(neverRan)).toEqual(["pending", "skipped"]);
  });

  // Hardcoded, not derived: `fail`/`warn` IN (a capture worth reading) and `skipped` OUT are the
  // decisions the results region's done-gate carries.
  it("names exactly the three states that ran and stopped", () => {
    expect(ALL.filter(settled)).toEqual(["ok", "fail", "warn"]);
  });

  it("covers every state exactly once between the three", () => {
    for (const s of ALL) {
      const buckets = [inFlight(s), neverRan(s), settled(s)].filter(Boolean);
      expect(buckets, s).toHaveLength(1);
    }
  });
});

describe("paintStateMark writes one mark, whatever it is called with", () => {
  const slot = (): HTMLElement => document.createElement("span");

  it("writes one svg for an icon state", () => {
    const el = slot();
    paintStateMark(el, "fail");
    expect(el.querySelectorAll("svg")).toHaveLength(1);
    expect(el.textContent).toBe("");
  });

  it("writes one text node for a character state", () => {
    const el = slot();
    paintStateMark(el, "input");
    expect(el.querySelector("svg")).toBeNull();
    expect(el.textContent).toBe("?");
    expect(el.childNodes).toHaveLength(1);
  });

  it("empties the slot for a ring state", () => {
    const el = slot();
    paintStateMark(el, "fail");
    paintStateMark(el, "running");
    expect(el.childNodes).toHaveLength(0);
  });

  it("leaves exactly one child across a repeat, a change, and a change back", () => {
    const el = slot();
    paintStateMark(el, "ok");
    paintStateMark(el, "ok");
    expect(el.childNodes).toHaveLength(1);
    paintStateMark(el, "skipped");
    expect(el.childNodes).toHaveLength(1);
    expect(el.textContent).toBe("\u2013");
    paintStateMark(el, "ok");
    expect(el.childNodes).toHaveLength(1);
    expect(el.querySelectorAll("svg")).toHaveLength(1);
  });
});
