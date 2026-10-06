// An edit stays on the caret's line, and the touch path agrees with the keydown path on every value.

import { describe, it, expect } from "vitest";
import fc from "fast-check";
import { continueAfterBreak, continueList } from "./list-continue.js";
import type { Edit } from "./text-edit.js";

function apply(value: string, e: Edit): string {
  return value.slice(0, e.start) + e.text + value.slice(e.end);
}

describe("continueList", () => {
  it("never touches text outside the caret's line and keeps the caret in bounds", () => {
    fc.assert(
      fc.property(
        fc.array(fc.constantFrom("- ", "1. ", "* [ ] ", "```", "x", " ", "\n", "-", ".", "a"), {
          maxLength: 30,
        }),
        fc.nat(),
        (parts, at) => {
          const value = parts.join("");
          const caret = at % (value.length + 1);
          const e = continueList(value, caret);
          if (e === null) {
            return;
          }
          const lineStart = value.lastIndexOf("\n", caret - 1) + 1;
          const nl = value.indexOf("\n", caret);
          const lineEnd = nl === -1 ? value.length : nl;
          expect(e.start).toBeGreaterThanOrEqual(lineStart);
          expect(e.end).toBeLessThanOrEqual(lineEnd);
          const out = apply(value, e);
          expect(out.slice(0, lineStart)).toBe(value.slice(0, lineStart));
          expect(out.slice(out.length - (value.length - lineEnd))).toBe(value.slice(lineEnd));
          expect(e.caret).toBeGreaterThanOrEqual(0);
          expect(e.caret).toBeLessThanOrEqual(out.length);
        },
      ),
    );
  });
});

describe("continueAfterBreak", () => {
  it("agrees with continueList on every value", () => {
    fc.assert(
      fc.property(
        fc.array(fc.constantFrom("- ", "2) ", "- [ ] ", "```", "x", " ", "\n", "-"), {
          maxLength: 20,
        }),
        fc.nat(),
        (parts, at) => {
          const value = parts.join("");
          const caret = at % (value.length + 1);
          const typed = value.slice(0, caret) + "\n" + value.slice(caret);
          const before = continueList(value, caret);
          const after = continueAfterBreak(typed, caret + 1);
          const viaKey = before === null ? typed : apply(value, before);
          const viaBreak = after === null ? typed : apply(typed, after);
          expect(viaBreak).toBe(viaKey);
        },
      ),
    );
  });
});
