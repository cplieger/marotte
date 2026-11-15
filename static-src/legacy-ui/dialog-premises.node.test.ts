// No marotte production module outside legacy-ui listens for beforetoggle. Tests and
// __test-helpers__ are not scanned. legacy-ui's dialog engine mishandles a controller created
// inside the beforetoggle of an open the page cancels, and its property test leaves that case
// out on this premise (`controlsCancelledOpen` in dialog.property.test.ts).

import { readdirSync, readFileSync } from "node:fs";
import { join, relative, sep } from "node:path";
import { describe, expect, it } from "vitest";

const ROOT = join(import.meta.dirname, "..");
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

const BEFORE_TOGGLE = /["'`]beforetoggle["'`]|\.onbeforetoggle\b/;

describe("dialog premises", () => {
  it("no marotte production module outside legacy-ui listens for beforetoggle", () => {
    expect(
      matching(BEFORE_TOGGLE),
      "legacy-ui's dialog engine mishandles a controller created in a cancelled open's beforetoggle",
    ).toEqual([]);
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
