import { readdirSync, readFileSync } from "node:fs";
import { join } from "node:path";
import { describe, it, expect } from "vitest";

// Three dots after a word, `)` or `}` and before the string's end (or a template's `${`) is UI
// copy trailing off; spread follows a space, `(` or `[`, and an empty `"..."` is a placeholder.
const THREE_DOTS = /[^\s.("'`[]\.\.\.(?=["'`<]| \$\{)/;
// Comments quote paths and prose with three dots.
const COMMENT = /^\s*(\/\/|\/\*|\*)/;

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
          !COMMENT.test(line) && THREE_DOTS.test(line)
            ? [`${p}:${String(i + 1)}: ${line.trim()}`]
            : [],
        ),
    );
    expect(hits).toEqual([]);
  });
});
