// No marotte source outside legacy-ui calls the native showModal() or listens for beforetoggle:
// a dialog opens through legacy-ui (createDialog, createModal, ask). legacy-ui's dialog engine
// mishandles a native showModal() made inside its own beforetoggle, and a controller created
// inside the beforetoggle of an open the page cancels.
// Its property test leaves both cases out on these premises (`opensNativelyInToggle` and
// `controlsCancelledOpen` in legacy-ui/dialog.property.test.ts).

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

function matching(pattern: RegExp): string[] {
  return sources(ROOT)
    .filter((f) => pattern.test(readFileSync(f, "utf8")))
    .map((f) => relative(ROOT, f).split(sep).join("/"));
}

const SHOW_MODAL = /\.showModal\s*\(/;
const BEFORE_TOGGLE = /["'`]beforetoggle["'`]|\.onbeforetoggle\b/;

describe("dialog premises", () => {
  it("no marotte source outside legacy-ui calls showModal()", () => {
    expect(
      matching(SHOW_MODAL),
      "open the dialog through legacy-ui's createDialog, createModal or ask instead",
    ).toEqual([]);
  });

  it("no marotte source outside legacy-ui listens for beforetoggle", () => {
    expect(
      matching(BEFORE_TOGGLE),
      "legacy-ui's dialog engine mishandles a controller created in a cancelled open's beforetoggle",
    ).toEqual([]);
  });

  it("reads a showModal() call in any spacing and ignores the method's name alone", () => {
    expect(SHOW_MODAL.test("dlg.showModal();")).toBe(true);
    expect(SHOW_MODAL.test("dlg.showModal ( )")).toBe(true);
    expect(SHOW_MODAL.test('vi.spyOn(d, "showModal")')).toBe(false);
  });

  it("reads a beforetoggle listener in any quoting or as a handler property", () => {
    expect(BEFORE_TOGGLE.test('d.addEventListener("beforetoggle", f)')).toBe(true);
    expect(BEFORE_TOGGLE.test("on(d, 'beforetoggle', f)")).toBe(true);
    expect(BEFORE_TOGGLE.test("d.onbeforetoggle = f;")).toBe(true);
    expect(BEFORE_TOGGLE.test('d.addEventListener("toggle", f)')).toBe(false);
  });

  it("walks the tree it guards", () => {
    expect(sources(ROOT)).toContain(join(ROOT, "app.ts"));
  });
});
