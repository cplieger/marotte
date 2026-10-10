import { describe, it, expect } from "vitest";
import fc from "fast-check";

import { EolTracker, normalize, serialize, serializeRange } from "./viewer-eol.js";

/** A raw file of short lines with any mix of LF, CRLF and lone CR, with or without a BOM or a
 *  final break. */
const rawFile = fc
  .tuple(
    fc.boolean(),
    fc.array(
      fc.tuple(
        fc.string({ unit: fc.constantFrom("a", "b", "x", " ", "é"), maxLength: 4 }),
        fc.constantFrom("\n", "\r\n", "\r"),
      ),
      {
        maxLength: 8,
      },
    ),
    fc.string({ unit: fc.constantFrom("a", "z"), maxLength: 3 }),
  )
  .map(
    ([bom, lines, tail]) => (bom ? "\ufeff" : "") + lines.map(([t, e]) => t + e).join("") + tail,
  );

/** The model: the file as lines, each with its own ending ("" on the last). */
interface Line {
  text: string;
  end: string;
}

function linesOf(raw: string): Line[] {
  const out: Line[] = [];
  let start = 0;
  for (let i = 0; i < raw.length; i++) {
    if (raw[i] === "\n" || raw[i] === "\r") {
      const end = raw[i] === "\r" && raw[i + 1] === "\n" ? "\r\n" : raw[i]!;
      out.push({ text: raw.slice(start, i), end });
      i += end.length - 1;
      start = i + 1;
    }
  }
  out.push({ text: raw.slice(start), end: "" });
  return out;
}

/** Where a normalized offset lands: its line and column. */
function locate(lines: readonly Line[], n: number): { line: number; col: number } {
  let left = n;
  for (let i = 0; i < lines.length; i++) {
    const len = lines[i]!.text.length;
    if (left <= len) {
      return { line: i, col: left };
    }
    left -= len + 1;
  }
  return { line: lines.length - 1, col: lines[lines.length - 1]!.text.length };
}

/** Replace normalized `[a, b)` with break-free `ins`: the lines between merge, and the merged
 *  line keeps the ending of the last line the range touched. */
function modelEdit(lines: Line[], a: number, b: number, ins: string): Line[] {
  const from = locate(lines, a);
  const to = locate(lines, b);
  const merged = {
    text: lines[from.line]!.text.slice(0, from.col) + ins + lines[to.line]!.text.slice(to.col),
    end: lines[to.line]!.end,
  };
  return [...lines.slice(0, from.line), merged, ...lines.slice(to.line + 1)];
}

/** Drive one native edit through the tracker the way the textarea's events do. */
function edit(
  t: EolTracker,
  old: string,
  a: number,
  b: number,
  inserted: string,
  inputType: string,
): string {
  const next = old.slice(0, a) + inserted + old.slice(b);
  t.noteBeforeInput(a, b, inputType);
  t.noteInput(next);
  return next;
}

describe("normalize and serialize", () => {
  it("round-trips any mix of endings byte for byte", () => {
    fc.assert(
      fc.property(rawFile, (raw) => {
        const { text, record } = normalize(raw);
        expect(text.includes("\r")).toBe(false);
        expect(serialize(text, record)).toBe(raw);
      }),
    );
  });

  it("serializes a range with the endings of exactly that range", () => {
    const raw = "a\r\nb\nc\rd";
    const { text, record } = normalize(raw);
    expect(serializeRange(text, record, 2, 6)).toBe("b\nc\r");
    expect(serializeRange(text, record, 0, 3)).toBe("a\r\nb");
  });

  it("gives a file with no break a uniform LF record", () => {
    expect(normalize("one line").record).toEqual({ uniform: "lf", tags: null });
    expect(normalize("a\r\nb\r\n").record).toEqual({ uniform: "crlf", tags: null });
  });
});

describe("EolTracker over native edits", () => {
  // Character edits and deletions never move an untouched break's ending: the tracked bytes equal
  // the raw file with the same byte ranges replaced.
  it("keeps every untouched ending through edits that insert no break", () => {
    const step = fc.record({
      a: fc.nat(),
      len: fc.nat({ max: 4 }),
      ins: fc.string({ unit: fc.constantFrom("q", "w", "é"), maxLength: 2 }),
    });
    fc.assert(
      fc.property(rawFile, fc.array(step, { maxLength: 8 }), (raw, steps) => {
        const { text, record } = normalize(raw);
        const t = new EolTracker(text, record);
        let norm = text;
        let model = linesOf(raw);
        for (const s of steps) {
          const a = norm.length === 0 ? 0 : s.a % (norm.length + 1);
          const b = Math.min(norm.length, a + s.len);
          norm = edit(t, norm, a, b, s.ins, s.ins === "" ? "deleteContentBackward" : "insertText");
          model = modelEdit(model, a, b, s.ins);
        }
        expect(t.uncertain).toBe(false);
        expect(serialize(norm, t.snapshot())).toBe(model.map((l) => l.text + l.end).join(""));
      }),
    );
  });

  // Native undo restores the breaks a logged edit removed, with their own endings.
  it("undoes every edit back to the exact original bytes", () => {
    const step = fc.record({
      a: fc.nat(),
      len: fc.nat({ max: 5 }),
      ins: fc.constantFrom("", "k", "\n", "z\nz"),
    });
    fc.assert(
      fc.property(rawFile, fc.array(step, { minLength: 1, maxLength: 6 }), (raw, steps) => {
        const { text, record } = normalize(raw);
        const t = new EolTracker(text, record);
        const history = [text];
        let norm = text;
        for (const s of steps) {
          const a = norm.length === 0 ? 0 : s.a % (norm.length + 1);
          const b = Math.min(norm.length, a + s.len);
          norm = edit(
            t,
            norm,
            a,
            b,
            s.ins,
            s.ins.includes("\n") ? "insertFromPaste" : "insertText",
          );
          history.push(norm);
        }
        for (let i = history.length - 2; i >= 0; i--) {
          t.noteBeforeInput(0, 0, "historyUndo");
          t.noteInput(history[i] ?? "");
        }
        expect(t.uncertain).toBe(false);
        expect(serialize(text, t.snapshot())).toBe(raw);
      }),
    );
  });

  it("keeps a CRLF when the first line of a mixed file is edited", () => {
    const { text, record } = normalize("a\r\nb\nc\n");
    const t = new EolTracker(text, record);
    const next = edit(t, text, 1, 1, "!", "insertText");
    expect(serialize(next, t.snapshot())).toBe("a!\r\nb\nc\n");
  });

  it("removes the deleted line's own ending when two identical lines differ only in it", () => {
    const raw = "x\r\nx\nend";
    const first = normalize(raw);
    const t1 = new EolTracker(first.text, first.record);
    expect(serialize(edit(t1, first.text, 0, 2, "", "deleteContentBackward"), t1.snapshot())).toBe(
      "x\nend",
    );
    const second = normalize(raw);
    const t2 = new EolTracker(second.text, second.record);
    expect(serialize(edit(t2, second.text, 2, 4, "", "deleteContentBackward"), t2.snapshot())).toBe(
      "x\r\nend",
    );
  });

  // Enter at the end of "b" inserts a break before b's own LF; it takes the ending of the break
  // before the caret, and b's LF moves down with the text after it.
  it("gives a typed break the ending of the break before it", () => {
    const { text, record } = normalize("a\r\nb\nc");
    const t = new EolTracker(text, record);
    const next = edit(t, text, 3, 3, "\n", "insertLineBreak");
    expect(serialize(next, t.snapshot())).toBe("a\r\nb\r\n\nc");
  });

  it("gives a typed break the file's one ending in a uniform file", () => {
    const { text, record } = normalize("a\r\nb\r\n");
    const t = new EolTracker(text, record);
    const next = edit(t, text, 1, 1, "\n", "insertLineBreak");
    expect(serialize(next, t.snapshot())).toBe("a\r\n\r\nb\r\n");
  });

  it("marks the record uncertain when an undo pairs with nothing it logged", () => {
    const { text, record } = normalize("a\r\nb\nc");
    const t = new EolTracker(text, record);
    t.noteBeforeInput(0, 0, "historyUndo");
    t.noteInput("a\nb\nzz\nc");
    expect(t.uncertain).toBe(true);
  });

  it("undoes a new edit exactly after undoing history that filled the log's budget", () => {
    const raw = "a\r\nb\nc";
    const { text, record } = normalize(raw);
    const t = new EolTracker(text, record);
    const chunk = "q".repeat(4 << 20);
    const s1 = edit(t, text, 0, 0, chunk, "insertText");
    const s2 = edit(t, s1, 0, 0, chunk, "insertText");
    edit(t, s2, 0, 0, chunk, "insertText");
    // The third chunk pushed the first out of the log; undo the two it kept.
    for (const back of [s2, s1]) {
      t.noteBeforeInput(0, 0, "historyUndo");
      t.noteInput(back);
    }
    edit(t, s1, 0, 0, "!", "insertText");
    t.noteBeforeInput(0, 0, "historyUndo");
    t.noteInput(s1);
    expect(t.uncertain).toBe(false);
    expect(serialize(s1, t.snapshot())).toBe(chunk + raw);
  });

  it.each([
    ["ours", "mine\nend", "mine\r\nend"],
    ["theirs", "theirs\nend", "theirs\nend"],
    ["both", "mine\ntheirs\nend", "mine\r\ntheirs\nend"],
  ])("keeps each moved line's ending through a conflict resolution (%s)", (_side, next, want) => {
    const raw = "<<<<<<< ours\r\nmine\r\n=======\ntheirs\n>>>>>>> b\nend";
    const { text, record } = normalize(raw);
    const t = new EolTracker(text, record);
    t.replace(next, "moved");
    expect(t.uncertain).toBe(false);
    expect(serialize(next, t.snapshot())).toBe(want);
  });

  it("refuses to vouch for moved text that is not the file's own lines", () => {
    const { text, record } = normalize("a\r\nb\nc");
    const t = new EolTracker(text, record);
    t.replace("ab\nc", "moved");
    expect(t.uncertain).toBe(true);
  });

  it("refuses to vouch when identical lines with different endings make the move ambiguous", () => {
    const { text, record } = normalize("x\r\nx\nend");
    const t = new EolTracker(text, record);
    t.replace("x\nend", "moved");
    expect(t.uncertain).toBe(true);
  });

  it("starts over clean on reset", () => {
    const { text, record } = normalize("a\r\nb\nc");
    const t = new EolTracker(text, record);
    t.noteBeforeInput(0, 0, "historyUndo");
    t.noteInput("zz\n");
    t.reset(text, record);
    expect(t.uncertain).toBe(false);
    expect(serialize(text, t.snapshot())).toBe("a\r\nb\nc");
  });
});
