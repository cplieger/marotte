import { describe, it, expect } from "vitest";
import { catalogBaseModes, iconForMode } from "./roles.js";

describe("iconForMode", () => {
  it("gives every bundled mode and bundled agent its own glyph, apart from the custom-agent hexagon", () => {
    const custom = iconForMode("my-workspace-agent");
    const glyphs = catalogBaseModes().map((m) => iconForMode(m.id));
    expect(glyphs).not.toContain(custom);
    expect(new Set(glyphs).size).toBe(glyphs.length);
  });
});
