// marotte opens a widget-owned <dialog> only through legacy-ui (createDialog, createModal, ask), never
// with the native showModal(): legacy-ui's dialog engine mishandles a native showModal() made inside
// its own beforetoggle, and its property test leaves that case out on this premise
// (`opensNativelyInToggle` in legacy-ui/dialog.property.test.ts).

import { readdirSync, readFileSync } from "node:fs";
import { join, relative, sep } from "node:path";
import { describe, expect, it } from "vitest";

const ROOT = import.meta.dirname;
const SKIP_DIRS = new Set([
  "node_modules",
  "legacy-ui",
  "__test-helpers__",
  ".vitest",
  ".stryker-tmp",
]);

function sources(dir: string, out: string[] = []): string[] {
  for (const e of readdirSync(dir, { withFileTypes: true })) {
    if (e.isDirectory()) {
      if (!SKIP_DIRS.has(e.name)) {
        sources(join(dir, e.name), out);
      }
    } else if (
      e.name.endsWith(".ts") &&
      !e.name.endsWith(".test.ts") &&
      !e.name.endsWith(".d.ts")
    ) {
      out.push(join(dir, e.name));
    }
  }
  return out;
}

const SHOW_MODAL = /\.showModal\s*\(/;

describe("native dialog opens", () => {
  it("no marotte source outside legacy-ui calls showModal()", () => {
    const callers = sources(ROOT)
      .filter((f) => SHOW_MODAL.test(readFileSync(f, "utf8")))
      .map((f) => relative(ROOT, f).split(sep).join("/"));
    expect(
      callers,
      "open the dialog through legacy-ui's createDialog, createModal or ask instead",
    ).toEqual([]);
  });

  it("reads a call in any spacing and ignores the method's name alone", () => {
    expect(SHOW_MODAL.test("dlg.showModal();")).toBe(true);
    expect(SHOW_MODAL.test("dlg.showModal ( )")).toBe(true);
    expect(SHOW_MODAL.test('vi.spyOn(d, "showModal")')).toBe(false);
  });

  it("walks the tree it guards", () => {
    expect(sources(ROOT)).toContain(join(ROOT, "app.ts"));
  });
});
