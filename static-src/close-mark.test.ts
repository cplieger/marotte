// A close mark is `ICON_CLOSE`, never a text character: a text node centres its line box, not its ink.
// A source scan, because the failure is a new button.

import { describe, it, expect } from "vitest";
import attachmentPillSrc from "./attachment-pill.ts?raw";
import bannerStackSrc from "./banner-stack.ts?raw";
import chipSrc from "./chip.ts?raw";
import editorFindSrc from "./editor-find.ts?raw";
import filesSearchSrc from "./files-search.ts?raw";
import findInChatSrc from "./find-in-chat.ts?raw";
import iconsSrc from "./icons.ts?raw";
import mcpPairsSrc from "./mcp-pairs.ts?raw";
import permissionsUISrc from "./permissions-ui.ts?raw";
import searchPopupSrc from "./search-popup.ts?raw";
import searchShellSrc from "./search-shell.ts?raw";
import tabsSrc from "./tabs.ts?raw";

/** Comments are stripped first: they name the forbidden glyphs. */
function code(src: string): string {
  return src.replace(/\/\*[\s\S]*?\*\//g, " ").replace(/^\s*\/\/.*$/gm, " ");
}

/** Every module building a close or remove affordance, with the escape of the character it must not carry. */
const BUILDERS = [
  ["attachment-pill.ts", attachmentPillSrc],
  ["banner-stack.ts", bannerStackSrc],
  ["chip.ts", chipSrc],
  ["editor-find.ts", editorFindSrc],
  ["files-search.ts", filesSearchSrc],
  ["find-in-chat.ts", findInChatSrc],
  ["mcp-pairs.ts", mcpPairsSrc],
  ["permissions-ui.ts", permissionsUISrc],
  ["search-popup.ts", searchPopupSrc],
  ["search-shell.ts", searchShellSrc],
  ["tabs.ts", tabsSrc],
] as const satisfies readonly (readonly [string, string])[];

/** Written as escapes so the scan cannot match this list's own source. */
const FORBIDDEN = ["\\u00d7", "\\u2715", "\\u2716", "\\u274c"] as const;

/** Named, so the scan cannot be satisfied by deleting an affordance. */
const CONVERTED = ["attachment-pill.ts", "banner-stack.ts", "permissions-ui.ts"] as const;

describe("a close mark is the registry's drawing", () => {
  for (const [file, src] of BUILDERS) {
    it(`${file} carries no bare close character`, () => {
      const scanned = code(src);
      for (const glyph of FORBIDDEN) {
        expect(
          scanned,
          `${file} must not carry a bare ${glyph}: a text node's LINE BOX is what ` +
            `align-items centres, so the ink lands off-centre by a font-dependent ` +
            `amount \u2014 build the mark with iconEl(ICON_CLOSE) instead`,
        ).not.toContain(glyph);
      }
    });
  }

  for (const file of CONVERTED) {
    it(`${file} builds its close mark from the registry`, () => {
      const entry = BUILDERS.find(([name]) => name === file);
      expect(entry, `${file} must be in the scanned population`).toBeDefined();
      expect(
        code(entry?.[1] ?? ""),
        `${file} lost its close affordance rather than converting it`,
      ).toContain("iconEl(ICON_CLOSE)");
    });
  }
});

describe("the icon registry names a tier pair by its TIER", () => {
  // A rename is caught by the type checker, a new pixel-suffixed export is not. Sizes name a tier (see `svg()`).
  it("exports no icon name ending in a pixel count", () => {
    const names = [...code(iconsSrc).matchAll(/export const (ICON_\w+)/g)].map((m) => m[1] ?? "");
    expect(
      names.filter((n) => /_\d+$/.test(n)),
      "an icon export names a SIZE TIER, never a pixel count (svg() in icons.ts)",
    ).toEqual([]);
  });
});
