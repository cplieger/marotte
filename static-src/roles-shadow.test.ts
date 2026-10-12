// Agent scope labels and grouping; the shadow marker itself is the role picker's (role-picker-chip-order.test.ts).
import { describe, it, expect } from "vitest";
import { isCustomSource, scopeLabel } from "./roles.js";

describe("scopeLabel", () => {
  it("names the two custom scopes", () => {
    expect(scopeLabel("workspace")).toBe("workspace");
    expect(scopeLabel("global")).toBe("global");
  });

  it("says nothing for a bundled or unset source", () => {
    // Every row in the top group is bundled, so labelling each one restates the divider above it.
    expect(scopeLabel("bundled")).toBe("");
    expect(scopeLabel(undefined)).toBe("");
  });
});

describe("isCustomSource", () => {
  it("puts the two custom scopes under the divider", () => {
    expect(isCustomSource("workspace")).toBe(true);
    expect(isCustomSource("global")).toBe(true);
  });

  it("keeps bundled and unset in the top group", () => {
    // BUILTIN_MODES tags itself bundled, but the pre-fetch fallback path and any catalog entry that
    // omits the field must not land under "Custom agents".
    expect(isCustomSource("bundled")).toBe(false);
    expect(isCustomSource(undefined)).toBe(false);
    expect(isCustomSource("")).toBe(false);
  });

  it("treats a source value it has never seen as custom", () => {
    // The whole reason this tests what IS bundled rather than enumerating what is custom. A
    // vocabulary that grows upstream must not put an unknown scope in the group a reader trusts to
    // be Kiro's own; the divider is the safe side.
    expect(isCustomSource("organization")).toBe(true);
  });
});
