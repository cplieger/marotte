// Every residency DOM writer in messages.ts runs inside the scroll owner's `heldMutation`, so no frame reader sees a
// mount the owner has not answered. A source scan, because the guarantee is about where calls sit.
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";

const here = dirname(fileURLToPath(import.meta.url));
const src = readFileSync(join(here, "messages.ts"), "utf8");

/** The `[start, end)` spans of every `heldMutation(() => { ... })` callback body. */
function heldSpans(text: string): [number, number][] {
  const spans: [number, number][] = [];
  const key = "heldMutation(() => {";
  for (let at = text.indexOf(key); at >= 0; at = text.indexOf(key, at + 1)) {
    let depth = 1;
    let i = at + key.length;
    for (; i < text.length && depth > 0; i++) {
      if (text[i] === "{") {
        depth++;
      } else if (text[i] === "}") {
        depth--;
      }
    }
    spans.push([at, i]);
  }
  return spans;
}

/** Call sites of `name(`, excluding its own declaration. */
function calls(text: string, name: string): number[] {
  const out: number[] = [];
  const re = new RegExp(`(?<![\\w.])${name}\\(`, "g");
  for (const m of text.matchAll(re)) {
    if (!text.slice(Math.max(0, m.index - 9), m.index).endsWith("function ")) {
      out.push(m.index);
    }
  }
  return out;
}

describe("residency writers in messages.ts", () => {
  const spans = heldSpans(src);
  const line = (at: number): number => src.slice(0, at).split("\n").length;

  it.each(["updateAssistantBody", "syncSpacers", "applyFoldPass", "buildAssistantBody"])(
    "calls %s only inside a heldMutation callback",
    (name) => {
      const sites = calls(src, name);
      expect(sites.length, "the premise: the writer is called").toBeGreaterThan(0);
      const outside = sites.filter((at) => !spans.some(([a, b]) => at > a && at < b)).map(line);
      expect(outside).toEqual([]);
    },
  );
});
