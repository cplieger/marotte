import { readdirSync, readFileSync } from "node:fs";
import { join } from "node:path";
import { describe, it, expect } from "vitest";

// A word then three dots then the string's end (or a template's `${`) is UI copy
// trailing off; spread syntax and code comments never take that shape.
const THREE_DOTS = /[A-Za-z]\.\.\.(?=["`<]| \$\{)/;

function shippedSources(dir: string): string[] {
  return readdirSync(dir, { withFileTypes: true }).flatMap((e) => {
    const p = join(dir, e.name);
    if (e.isDirectory()) {
      return e.name === "node_modules" || e.name.startsWith("__") ? [] : shippedSources(p);
    }
    return e.name.endsWith(".ts") && !e.name.endsWith(".test.ts") ? [p] : [];
  });
}

describe("UI copy", () => {
  it("trails off with the ellipsis character, never three dots", () => {
    const hits = [...shippedSources("."), "../static/index.html"].flatMap((p) =>
      readFileSync(p, "utf8")
        .split("\n")
        .flatMap((line, i) =>
          THREE_DOTS.test(line) ? [`${p}:${String(i + 1)}: ${line.trim()}`] : [],
        ),
    );
    expect(hits).toEqual([]);
  });
});
