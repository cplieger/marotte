// The two per-kind tables and the view contract: no DOM, store or mocks; a node test proves the
// module DOM-free.

import { describe, it, expect } from "vitest";
import { TAB_VIEWS, TAB_ICONS } from "./tab-view.js";
import type { TabKind } from "./types.js";

// Not the gate (the `Readonly<Record<TabKind, string>>` annotation is); it catches a table entry
// the wire does not know, or tables that disagree.
const KINDS: readonly TabKind[] = [
  "chat",
  "editor",
  "run",
  "subagent",
  "settings",
  "git",
  "files",
  "history",
  "docs",
  "spec",
  "web",
];

describe("TAB_VIEWS", () => {
  it("names a view element for every wire kind and no others", () => {
    expect(Object.keys(TAB_VIEWS).sort()).toEqual([...KINDS].sort());
  });

  it.each(KINDS)("%s maps to an id selector", (kind) => {
    expect(TAB_VIEWS[kind]).toMatch(/^#[a-z-]+$/);
  });

  // A kind pointing at another kind's view is the one mistake in this table that
  // a type cannot catch: every value is a string, so a copy-paste shows up only
  // as two kinds sharing a selector and one view never being reachable.
  it("gives each kind its own view", () => {
    const selectors = Object.values(TAB_VIEWS);
    expect(new Set(selectors).size).toBe(selectors.length);
  });
});

describe("TAB_ICONS", () => {
  it("covers exactly the kinds TAB_VIEWS covers", () => {
    expect(Object.keys(TAB_ICONS).sort()).toEqual(Object.keys(TAB_VIEWS).sort());
  });

  it.each(KINDS)("%s carries an svg glyph", (kind) => {
    expect(TAB_ICONS[kind]).toMatch(/^<svg/);
  });

  // Same trap as the view table, and the same reason a type cannot see it: two
  // kinds sharing a glyph reads as a tab wearing the wrong badge.
  it("gives each kind its own glyph", () => {
    const glyphs = Object.values(TAB_ICONS);
    expect(new Set(glyphs).size).toBe(glyphs.length);
  });
});
