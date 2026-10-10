// The mode picker and the role pill draw iconForMode's glyph.
import { describe, it, expect } from "vitest";
import { catalogBaseModes, iconForMode, setCatalogModes } from "./roles.js";
import { ICON_TAB_AGENT, ICON_TAB_CHAT } from "./icons.js";

describe("iconForMode", () => {
  it("gives default-v2 the Default mode's glyph", () => {
    expect(iconForMode("default-v2")).toBe(ICON_TAB_CHAT);
    expect(iconForMode("default-v2")).toBe(iconForMode("vibe"));
  });

  it("keeps the hexagon for a custom agent", () => {
    expect(iconForMode("my-workspace-agent")).toBe(ICON_TAB_AGENT);
  });

  it("does not seed default-v2 before a catalog lands", () => {
    setCatalogModes([]);
    expect(catalogBaseModes().map((m) => m.id)).not.toContain("default-v2");
  });
});
