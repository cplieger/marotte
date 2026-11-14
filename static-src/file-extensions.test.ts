import { describe, it, expect } from "vitest";
import { badgeForExt, extToIconKey, KNOWN_EXTENSIONS, FILE_EXTS } from "./file-extensions.js";

describe("extToIconKey", () => {
  const iconCases = Object.entries(KNOWN_EXTENSIONS)
    .filter(([, meta]) => meta.iconKey)
    .map(([ext, meta]) => ({ ext, iconKey: meta.iconKey! }));

  it.each(iconCases)("$ext → $iconKey", ({ ext, iconKey }) => {
    expect(extToIconKey(ext)).toBe(iconKey);
  });

  it("returns empty string for unknown extension", () => {
    expect(extToIconKey("zzz_unknown")).toBe("");
  });

  it("returns empty string for empty string", () => {
    expect(extToIconKey("")).toBe("");
  });

  // A file named `x.constructor` must not read Object.prototype's member as its icon.
  it("returns empty string for a name the object inherits", () => {
    expect(extToIconKey("constructor")).toBe("");
    expect(extToIconKey("toString")).toBe("");
  });
});

describe("badgeForExt", () => {
  it("returns empty string for a name the object inherits", () => {
    expect(badgeForExt("constructor")).toBe("");
    expect(badgeForExt("__proto__")).toBe("");
  });
});

describe("FILE_EXTS registry integrity", () => {
  it("FILE_EXTS length matches KNOWN_EXTENSIONS keys", () => {
    expect(FILE_EXTS.length).toBe(Object.keys(KNOWN_EXTENSIONS).length);
  });

  it("every FILE_EXTS entry exists in KNOWN_EXTENSIONS", () => {
    for (const ext of FILE_EXTS) {
      expect(KNOWN_EXTENSIONS[ext]).toBeDefined();
    }
  });
});
