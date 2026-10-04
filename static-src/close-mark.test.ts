// A close or remove mark is `ICON_CLOSE`, never a text character: a text node
// centres its line box, not its ink. A source scan over the builder files,
// because the failure is a new button rather than an edit to an existing one.

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

/** Comments explain the glyphs, so they are stripped before every scan below —
 *  including this file's own prose, which names the characters it forbids. */
function code(src: string): string {
  return src.replace(/\/\*[\s\S]*?\*\//g, " ").replace(/^\s*\/\/.*$/gm, " ");
}

/** Every module that builds a close or remove affordance, paired with the escape of the
 *  character it must not carry. The five search builders are here rather than in
 *  `search-centring.test.ts`, which keeps the ARROWS: those are a search bar's own
 *  glyphs and no other surface has them, where the close mark is app-wide. */
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

/** The two that were in use, plus the two a future author is likeliest to reach for.
 *  Written as escapes so the scan cannot match this list's own source. */
const FORBIDDEN = ["\\u00d7", "\\u2715", "\\u2716", "\\u274c"] as const;

/** The sites the conversion touched that still build a close mark. Named, so the scan
 *  above cannot be satisfied by deleting an affordance instead of converting it. */
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
  // `icons.ts` had `ICON_EDIT_14`, `ICON_TRASH_14` and `ICON_PLUS_16`. Both suffixes
  // named the `ui` tier, which is 1rem on a fine pointer and 1.25rem on a coarse one,
  // so neither number was ever what rendered — and `svg()`'s own doc says its argument
  // is a size TIER, never a pixel count. A rename is caught by the type checker; a NEW
  // export carrying a pixel suffix is not, which is the only thing this scans for.
  it("exports no icon name ending in a pixel count", () => {
    const names = [...code(iconsSrc).matchAll(/export const (ICON_\w+)/g)].map((m) => m[1] ?? "");
    expect(
      names.filter((n) => /_\d+$/.test(n)),
      "an icon export names a SIZE TIER, never a pixel count (svg() in icons.ts)",
    ).toEqual([]);
  });
});
