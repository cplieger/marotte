import { describe, it, expect } from "vitest";
import fc from "fast-check";
import {
  lineDelta,
  lineDiff,
  windowHunks,
  stats,
  wordDiff,
  wordMarks,
  type CharRange,
  type DiffLine,
} from "./diff.js";

/**
 * What a valid edit script reconstructs from `newText`: every line, without the final newline. Shared by the
 * time-budget case and the reconstruction invariant so the two cannot drift.
 */
function withoutFinalNewline(s: string): string {
  return s.endsWith("\n") ? s.slice(0, -1) : s;
}

describe("lineDiff", () => {
  const cases: {
    name: string;
    old: string;
    new: string;
    opts?: { ignoreWhitespace?: boolean };
    expected: DiffLine[];
  }[] = [
    {
      name: "identical texts produce all context",
      old: "a\nb\nc",
      new: "a\nb\nc",
      expected: [
        { kind: "ctx", oldNo: 1, newNo: 1, text: "a" },
        { kind: "ctx", oldNo: 2, newNo: 2, text: "b" },
        { kind: "ctx", oldNo: 3, newNo: 3, text: "c" },
      ],
    },
    {
      name: "completely different texts",
      old: "a\nb",
      new: "x\ny",
      expected: [
        { kind: "del", oldNo: 1, newNo: 0, text: "a" },
        { kind: "del", oldNo: 2, newNo: 0, text: "b" },
        { kind: "add", oldNo: 0, newNo: 1, text: "x" },
        { kind: "add", oldNo: 0, newNo: 2, text: "y" },
      ],
    },
    {
      name: "single add",
      old: "a\nc",
      new: "a\nb\nc",
      expected: [
        { kind: "ctx", oldNo: 1, newNo: 1, text: "a" },
        { kind: "add", oldNo: 0, newNo: 2, text: "b" },
        { kind: "ctx", oldNo: 2, newNo: 3, text: "c" },
      ],
    },
    {
      name: "single del",
      old: "a\nb\nc",
      new: "a\nc",
      expected: [
        { kind: "ctx", oldNo: 1, newNo: 1, text: "a" },
        { kind: "del", oldNo: 2, newNo: 0, text: "b" },
        { kind: "ctx", oldNo: 3, newNo: 2, text: "c" },
      ],
    },
    {
      name: "interleaved changes",
      old: "a\nb\nc\nd",
      new: "a\nX\nc\nY",
      expected: [
        { kind: "ctx", oldNo: 1, newNo: 1, text: "a" },
        { kind: "del", oldNo: 2, newNo: 0, text: "b" },
        { kind: "add", oldNo: 0, newNo: 2, text: "X" },
        { kind: "ctx", oldNo: 3, newNo: 3, text: "c" },
        { kind: "del", oldNo: 4, newNo: 0, text: "d" },
        { kind: "add", oldNo: 0, newNo: 4, text: "Y" },
      ],
    },
    {
      name: "empty old text (all adds)",
      old: "",
      new: "a\nb",
      expected: [
        { kind: "add", oldNo: 0, newNo: 1, text: "a" },
        { kind: "add", oldNo: 0, newNo: 2, text: "b" },
      ],
    },
    {
      name: "empty new text (all dels)",
      old: "a\nb",
      new: "",
      expected: [
        { kind: "del", oldNo: 1, newNo: 0, text: "a" },
        { kind: "del", oldNo: 2, newNo: 0, text: "b" },
      ],
    },
    {
      name: "both empty",
      old: "",
      new: "",
      expected: [],
    },
    {
      name: "whitespace-only diff without ignoreWhitespace",
      old: "  a",
      new: "a",
      expected: [
        { kind: "del", oldNo: 1, newNo: 0, text: "  a" },
        { kind: "add", oldNo: 0, newNo: 1, text: "a" },
      ],
    },
    {
      name: "whitespace-only diff with ignoreWhitespace",
      old: "  a",
      new: "a",
      opts: { ignoreWhitespace: true },
      expected: [{ kind: "ctx", oldNo: 1, newNo: 1, text: "  a" }],
    },
    {
      name: "internal whitespace diff with ignoreWhitespace",
      old: "a  b",
      new: "a b",
      opts: { ignoreWhitespace: true },
      expected: [{ kind: "ctx", oldNo: 1, newNo: 1, text: "a  b" }],
    },
    {
      name: "single line identical",
      old: "hello",
      new: "hello",
      expected: [{ kind: "ctx", oldNo: 1, newNo: 1, text: "hello" }],
    },
    {
      name: "CRLF input: \\r stripped from line text (EOL owned by consumers)",
      old: "a\r\nold\r\nc",
      new: "a\r\nnew\r\nc",
      expected: [
        { kind: "ctx", oldNo: 1, newNo: 1, text: "a" },
        { kind: "del", oldNo: 2, newNo: 0, text: "old" },
        { kind: "add", oldNo: 0, newNo: 2, text: "new" },
        { kind: "ctx", oldNo: 3, newNo: 3, text: "c" },
      ],
    },
  ];

  for (const tc of cases) {
    it(tc.name, () => {
      const result = lineDiff(tc.old, tc.new, tc.opts);
      expect(result).toEqual(tc.expected);
    });
  }

  it("falls back to a coarse but valid script past the time budget", () => {
    // 5100×5100 = 26M cells > TIME_BUDGET_CELLS; every line differs, so the trim removes nothing.
    const n = 5100;
    const oldText = Array.from({ length: n }, (_, i) => `left ${String(i)}`).join("\n");
    const newText = Array.from({ length: n }, (_, i) => `right ${String(i)}`).join("\n");
    const result = lineDiff(oldText, newText);
    const s = stats(result);
    // The fallback is still a valid edit script.
    expect(s.dels + s.ctx).toBe(n);
    expect(s.adds + s.ctx).toBe(n);
    const reconstructed = result.filter((l) => l.kind !== "del").map((l) => l.text);
    // Against the invariant's own expression, not a raw `split`, so the two keep agreeing if the generator changes.
    expect(reconstructed.join("\n")).toBe(withoutFinalNewline(newText));
  });

  it("bounded time: shared prefix/suffix keeps huge similar files on the exact path", () => {
    // One differing middle line: the trim reduces the exact middle to 1×1, so this must be minimal and fast.
    const n = 100_000;
    const lines = Array.from({ length: n }, (_, i) => `line ${String(i)}`);
    const oldText = lines.join("\n");
    const changed = [...lines];
    changed[n / 2] = "CHANGED";
    const newText = changed.join("\n");
    const result = lineDiff(oldText, newText);
    const s = stats(result);
    expect(s.dels).toBe(1);
    expect(s.adds).toBe(1);
    expect(s.ctx).toBe(n - 1);
  });
});

describe("windowHunks", () => {
  // The unit is the hunk: the first three changed lines once showed a 12-line rewrite as a quarter of itself.
  const ctx = (n: number, t: string): DiffLine => ({ kind: "ctx", oldNo: n, newNo: n, text: t });
  const del = (n: number, t: string): DiffLine => ({ kind: "del", oldNo: n, newNo: 0, text: t });
  const add = (n: number, t: string): DiffLine => ({ kind: "add", oldNo: 0, newNo: n, text: t });

  it("a diff with no changes yields nothing — there is no hunk to show", () => {
    const got = windowHunks([ctx(1, "a"), ctx(2, "b")]);
    expect(got.lines).toEqual([]);
    expect(got.hunksOmitted).toBe(0);
  });

  it("empty input is empty", () => {
    expect(windowHunks([])).toEqual({ lines: [], hunksOmitted: 0 });
  });

  it("keeps a whole hunk even when it exceeds maxRows, rather than cutting it", () => {
    const hunk = [del(1, "a"), del(2, "b"), add(1, "c"), add(2, "d"), add(3, "e")];
    const got = windowHunks(hunk, { maxRows: 2 });
    // The first hunk always goes in whole.
    expect(got.lines).toEqual(hunk);
    expect(got.hunksOmitted).toBe(0);
  });

  it("drops a LATER hunk whole when the cap is reached, and counts it", () => {
    const lines = [del(1, "a"), add(1, "b"), ctx(2, "keep"), del(3, "c"), add(3, "d")];
    const got = windowHunks(lines, { maxRows: 2, context: 1 });
    expect(got.hunksOmitted).toBe(1);
    expect(got.lines.some((l) => l.text === "c" || l.text === "d")).toBe(false);
  });

  it("keeps context adjoining a hunk and elides a long run between two", () => {
    const lines = [
      ctx(1, "far-before"),
      ctx(2, "near-before"),
      del(3, "changed"),
      ctx(4, "near-after"),
      ctx(5, "middle"),
      ctx(6, "middle2"),
      ctx(7, "near-before2"),
      add(8, "changed2"),
    ];
    const got = windowHunks(lines, { maxRows: 40, context: 1 });
    const texts = got.lines.map((l) => l.text);
    expect(texts).toContain("near-before");
    expect(texts).toContain("near-after");
    expect(texts).toContain("near-before2");
    expect(texts).not.toContain("middle");
    // A context run touching no hunk on its far side contributes nothing there.
    expect(texts).not.toContain("far-before");
    expect(got.hunksOmitted).toBe(0);
  });

  it("keeps a short context run whole rather than double-counting its ends", () => {
    const lines = [del(1, "x"), ctx(2, "only"), add(3, "y")];
    const got = windowHunks(lines, { context: 2 });
    expect(got.lines.filter((l) => l.text === "only")).toHaveLength(1);
  });

  it("drops trailing context that adjoins no later hunk", () => {
    const lines = [del(1, "x"), ctx(2, "n1"), ctx(3, "n2"), ctx(4, "n3")];
    const got = windowHunks(lines, { context: 1 });
    // Only the side touching the hunk contributes.
    expect(got.lines).toEqual([del(1, "x"), ctx(2, "n1")]);
  });

  it("keeps a hunk that exactly fills the remaining budget", () => {
    const lines = [del(1, "a"), add(1, "b"), ctx(2, "k1"), ctx(3, "k2"), del(4, "c"), add(4, "d")];
    // 2 hunk + 2 context + 2 hunk === maxRows, so the last hunk fits exactly; cutting past the cap would drop it.
    const got = windowHunks(lines, { maxRows: 6, context: 1 });
    expect(got).toEqual({ lines, hunksOmitted: 0 });
  });
});

describe("stats", () => {
  it("counts adds, dels, and ctx", () => {
    const lines: DiffLine[] = [
      { kind: "add", oldNo: 0, newNo: 1, text: "a" },
      { kind: "del", oldNo: 1, newNo: 0, text: "b" },
      { kind: "ctx", oldNo: 2, newNo: 2, text: "c" },
      { kind: "add", oldNo: 0, newNo: 3, text: "d" },
    ];
    expect(stats(lines)).toEqual({ adds: 2, dels: 1, ctx: 1 });
  });

  it("returns zeros for empty input", () => {
    expect(stats([])).toEqual({ adds: 0, dels: 0, ctx: 0 });
  });
});

describe("lineDelta", () => {
  const bigFile = (n: number): string =>
    Array.from({ length: n }, (_, i) => `line ${String(i)}\n`).join("");

  const cases: { name: string; old: string; new: string; added: number; removed: number }[] = [
    {
      name: "one line edited in a 300-line file",
      old: bigFile(300),
      new: bigFile(300).replace("line 149\n", "line 149 EDITED\n"),
      added: 1,
      removed: 1,
    },
    { name: "a 2-line file creation", old: "", new: "line1\nline2\n", added: 2, removed: 0 },
    { name: "a file emptied", old: "x\ny\n", new: "", added: 0, removed: 2 },
    { name: "no-op write", old: "a\nb\nc\n", new: "a\nb\nc\n", added: 0, removed: 0 },
    { name: "pure insertion", old: "a\nd\n", new: "a\nb\nc\nd\n", added: 2, removed: 0 },
    { name: "pure deletion", old: "a\nb\nc\nd\n", new: "a\nd\n", added: 0, removed: 2 },
    { name: "whole-file replacement", old: "a\nb\n", new: "x\ny\n", added: 2, removed: 2 },
    {
      name: "a trailing newline is not a line",
      old: "a\nb",
      new: "a\nb\n",
      added: 0,
      removed: 0,
    },
    { name: "CRLF to LF", old: "a\r\nb\r\n", new: "a\nb\n", added: 0, removed: 0 },
    { name: "both sides empty", old: "", new: "", added: 0, removed: 0 },
  ];

  for (const c of cases) {
    it(c.name, () => {
      expect(lineDelta(c.old, c.new)).toEqual({ added: c.added, removed: c.removed });
    });
  }

  it("counts a 2-line file creation as 2 lines on both surfaces", () => {
    // The pane once drew a row for the final newline while the footer did not (28 vs `git diff`'s 27).
    expect(stats(lineDiff("", "a\nb\n")).adds).toBe(2);
    expect(lineDelta("", "a\nb\n").added).toBe(2);
  });
});

// A file's final newline is the writer's terminator, not a line. One split serves the whole diff surface (pane, tool
// card preview, editor diff mode, footer counts), matching `internal/buffer/linediff.go`'s `splitDiffLines` exactly.
describe("a file's final newline is not a line", () => {
  it("renders a newline-terminated creation as exactly its own lines", () => {
    expect(lineDiff("", "a\nb\n")).toEqual([
      { kind: "add", oldNo: 0, newNo: 1, text: "a" },
      { kind: "add", oldNo: 0, newNo: 2, text: "b" },
    ]);
  });

  it("renders a file NOT ending in a newline identically — the CONTROL", () => {
    // Both spellings of a two-line file give one answer.
    expect(lineDiff("", "a\nb")).toEqual([
      { kind: "add", oldNo: 0, newNo: 1, text: "a" },
      { kind: "add", oldNo: 0, newNo: 2, text: "b" },
    ]);
  });

  it("counts a file that is only newlines by its empty lines", () => {
    // The drop is a pop, not a strip of the trailing "\n": a file holding one newline holds one empty line, as `git diff`
    // counts it.
    expect(lineDiff("", "\n")).toEqual([{ kind: "add", oldNo: 0, newNo: 1, text: "" }]);
    expect(lineDiff("", "\n\n")).toEqual([
      { kind: "add", oldNo: 0, newNo: 1, text: "" },
      { kind: "add", oldNo: 0, newNo: 2, text: "" },
    ]);
  });

  const agree: {
    name: string;
    old: string;
    new: string;
    added: number;
    removed: number;
  }[] = [
    { name: "a newline-terminated pair", old: "a\nb\n", new: "a\nB\nc\n", added: 2, removed: 1 },
    { name: "a pair with no trailing newline", old: "a\nb", new: "a\nB\nc", added: 2, removed: 1 },
    { name: "a CRLF pair", old: "a\r\nb\r\n", new: "a\r\nB\r\nc\r\n", added: 2, removed: 1 },
    { name: "a file creation", old: "", new: "a\nb\n", added: 2, removed: 0 },
    { name: "a file emptied", old: "x\ny\n", new: "", added: 0, removed: 2 },
  ];

  for (const c of agree) {
    it(`states one count for ${c.name}, on both surfaces`, () => {
      // Both sides hardcoded: with one shared split, comparing `stats(lineDiff)` to `lineDelta` is a tautology.
      const s = stats(lineDiff(c.old, c.new));
      expect({ added: s.adds, removed: s.dels }, "the diff pane's count").toEqual({
        added: c.added,
        removed: c.removed,
      });
      expect(lineDelta(c.old, c.new), "the turn footer's count").toEqual({
        added: c.added,
        removed: c.removed,
      });
    });
  }

  it("renders a newline-only change as an UNCHANGED file — ACCEPTED RESIDUAL", () => {
    // Characterization: a newline-only change at EOF splits to identical lines, so every row is context and the pane says
    // "No changes". Accepted: such changes are rare, and `lineDelta` and the Go twin already report 0/0, so the surfaces agree.
    for (const [before, after] of [
      ["a\nb", "a\nb\n"],
      ["a\nb\n", "a\nb"],
    ] as const) {
      const d = lineDiff(before, after);
      expect(d.map((l) => l.kind)).toEqual(["ctx", "ctx"]);
      expect(stats(d)).toEqual({ adds: 0, dels: 0, ctx: 2 });
    }
  });
});

describe("lineDiff property-based invariants", () => {
  /** Spelled independently of `splitLines`, or every invariant below would be a tautology. */
  function countLines(s: string): number {
    if (s === "") {
      return 0;
    }
    const n = s.split("\n").length;
    return s.endsWith("\n") ? n - 1 : n;
  }

  function reconstructNew(lines: DiffLine[]): string {
    const parts: string[] = [];
    for (const l of lines) {
      if (l.kind === "add" || l.kind === "ctx") {
        parts.push(l.text);
      }
    }
    return parts.length === 0 ? "" : parts.join("\n");
  }

  // Small multi-line inputs: the dense LCS path.
  const smallText = fc
    .array(fc.string({ minLength: 0, maxLength: 20 }), { minLength: 0, maxLength: 30 })
    .map((lines) => lines.join("\n"));

  it("invariant 1: adds + ctx === countLines(newText)", () => {
    fc.assert(
      fc.property(smallText, smallText, (a, b) => {
        const d = lineDiff(a, b);
        const s = stats(d);
        expect(s.adds + s.ctx).toBe(countLines(b));
      }),
    );
  });

  it("invariant 2: dels + ctx === countLines(oldText)", () => {
    fc.assert(
      fc.property(smallText, smallText, (a, b) => {
        const d = lineDiff(a, b);
        const s = stats(d);
        expect(s.dels + s.ctx).toBe(countLines(a));
      }),
    );
  });

  it("invariant 3: applying diff reconstructs newText, minus its terminator", () => {
    fc.assert(
      fc.property(smallText, smallText, (a, b) => {
        const d = lineDiff(a, b);
        expect(reconstructNew(d)).toBe(withoutFinalNewline(b));
      }),
    );
  });

  it("invariant 4: lineDiff(a, a) produces only ctx entries", () => {
    fc.assert(
      fc.property(smallText, (a) => {
        const d = lineDiff(a, a);
        for (const l of d) {
          expect(l.kind).toBe("ctx");
        }
      }),
    );
  });

  it("invariant 5: lineDiff('', b) produces only add entries", () => {
    fc.assert(
      fc.property(fc.string({ minLength: 1, maxLength: 100 }), (b) => {
        const d = lineDiff("", b);
        for (const l of d) {
          expect(l.kind).toBe("add");
        }
      }),
    );
  });

  it("invariant 6: lineDiff(a, '') produces only del entries", () => {
    fc.assert(
      fc.property(fc.string({ minLength: 1, maxLength: 100 }), (a) => {
        const d = lineDiff(a, "");
        for (const l of d) {
          expect(l.kind).toBe("del");
        }
      }),
    );
  });

  // Over SPACE_THRESHOLD (4M cells): 2001×2001 lines reaches the Hirschberg path.
  const largeText = fc
    .array(
      fc
        .array(fc.constantFrom("a", "b", "c", "d"), { minLength: 0, maxLength: 5 })
        .map((cs) => cs.join("")),
      { minLength: 2001, maxLength: 2001 },
    )
    .map((lines) => lines.join("\n"));

  // 60s, not the global 10s fast-check interrupt: under Stryker a 2001×2001 run takes ~5s and `markInterruptAsFailure`
  // failed the dry run. The per-test cap in vitest.stryker.config.ts sits above it.
  it("Hirschberg path: invariants 1-3 hold for large inputs", () => {
    fc.assert(
      fc.property(largeText, largeText, (a, b) => {
        const d = lineDiff(a, b);
        const s = stats(d);
        expect(s.adds + s.ctx).toBe(countLines(b));
        expect(s.dels + s.ctx).toBe(countLines(a));
        expect(reconstructNew(d)).toBe(withoutFinalNewline(b));
      }),
      { numRuns: 3, interruptAfterTimeLimit: 60_000 },
    ); // fewer runs due to cost
  });

  it("Hirschberg path: lineDiff(a, a) produces only ctx entries", () => {
    fc.assert(
      fc.property(largeText, (a) => {
        const d = lineDiff(a, a);
        for (const l of d) {
          expect(l.kind).toBe("ctx");
        }
      }),
      { numRuns: 3, interruptAfterTimeLimit: 60_000 },
    );
  });
});

// Minimality, not just validity: any valid script satisfies the round-trip invariants, so these pin the script on
// inputs small enough to solve by hand, which catches an off-by-one in the table.
describe("lineDiff minimal edit scripts", () => {
  const cases: {
    name: string;
    old: string;
    new: string;
    opts?: { ignoreWhitespace?: boolean };
    expected: DiffLine[];
  }[] = [
    {
      // Both ends differ, so the whole table is walked; the walk must prefer an add twice while the pair is still coming.
      name: "adds precede a matching pair when the table says the pair is worth more",
      old: "X\nA\nB\nY",
      new: "Q\nC\nA\nB\nZ",
      expected: [
        { kind: "del", oldNo: 1, newNo: 0, text: "X" },
        { kind: "add", oldNo: 0, newNo: 1, text: "Q" },
        { kind: "add", oldNo: 0, newNo: 2, text: "C" },
        { kind: "ctx", oldNo: 2, newNo: 3, text: "A" },
        { kind: "ctx", oldNo: 3, newNo: 4, text: "B" },
        { kind: "del", oldNo: 4, newNo: 0, text: "Y" },
        { kind: "add", oldNo: 0, newNo: 5, text: "Z" },
      ],
    },
    {
      // The first row is the one an "i >= 0" bound is easiest to lose; a missing row shows as a deleted A.
      name: "a leading insertion is found from the first row of the table",
      old: "A\nB\nz",
      new: "X\nA\nB\nw",
      expected: [
        { kind: "add", oldNo: 0, newNo: 1, text: "X" },
        { kind: "ctx", oldNo: 1, newNo: 2, text: "A" },
        { kind: "ctx", oldNo: 2, newNo: 3, text: "B" },
        { kind: "del", oldNo: 3, newNo: 0, text: "z" },
        { kind: "add", oldNo: 0, newNo: 4, text: "w" },
      ],
    },
    {
      // "D" appears twice in the new text; anchoring on the second costs the B match.
      name: "a repeated line is anchored where it keeps the most context",
      old: "E\nD\nB",
      new: "D\nC\nB\nD",
      expected: [
        { kind: "del", oldNo: 1, newNo: 0, text: "E" },
        { kind: "ctx", oldNo: 2, newNo: 1, text: "D" },
        { kind: "add", oldNo: 0, newNo: 2, text: "C" },
        { kind: "ctx", oldNo: 3, newNo: 3, text: "B" },
        { kind: "add", oldNo: 0, newNo: 4, text: "D" },
      ],
    },
    {
      // The B/A pair is reachable only by skipping the earlier A and C copies.
      name: "duplicated old lines do not cost context",
      old: "C\nA\nC\nB\nA\nD\nB",
      new: "E\nB\nA",
      expected: [
        { kind: "del", oldNo: 1, newNo: 0, text: "C" },
        { kind: "del", oldNo: 2, newNo: 0, text: "A" },
        { kind: "del", oldNo: 3, newNo: 0, text: "C" },
        { kind: "add", oldNo: 0, newNo: 1, text: "E" },
        { kind: "ctx", oldNo: 4, newNo: 2, text: "B" },
        { kind: "ctx", oldNo: 5, newNo: 3, text: "A" },
        { kind: "del", oldNo: 6, newNo: 0, text: "D" },
        { kind: "del", oldNo: 7, newNo: 0, text: "B" },
      ],
    },
    {
      // Two trimmed suffix lines, so the suffix loop's line numbers must climb.
      name: "a two-line common suffix keeps its own line numbers",
      old: "X\nc\nd",
      new: "Y\nc\nd",
      expected: [
        { kind: "del", oldNo: 1, newNo: 0, text: "X" },
        { kind: "add", oldNo: 0, newNo: 1, text: "Y" },
        { kind: "ctx", oldNo: 2, newNo: 2, text: "c" },
        { kind: "ctx", oldNo: 3, newNo: 3, text: "d" },
      ],
    },
    {
      // The mirror case: here the new side needs normalizing.
      name: "ignoreWhitespace normalizes the new side too",
      old: "a",
      new: "  a",
      opts: { ignoreWhitespace: true },
      expected: [{ kind: "ctx", oldNo: 1, newNo: 1, text: "a" }],
    },
    {
      name: "ignoreWhitespace still separates a real content change",
      old: "a",
      new: "b",
      opts: { ignoreWhitespace: true },
      expected: [
        { kind: "del", oldNo: 1, newNo: 0, text: "a" },
        { kind: "add", oldNo: 0, newNo: 1, text: "b" },
      ],
    },
  ];

  for (const tc of cases) {
    it(tc.name, () => {
      expect(lineDiff(tc.old, tc.new, tc.opts)).toEqual(tc.expected);
    });
  }
});

// The linear-space path, with an optimum known by construction: new text = old minus deletions plus lines absent from
// the old, so the LCS is old length minus deletions. Each pair is just past SPACE_THRESHOLD after the trim.
describe("lineDiff on the linear-space path", () => {
  const body = Array.from({ length: 2000 }, (_, i) => `L${String(i).padStart(4, "0")}`);
  const bodyNew = body
    .filter((l) => l !== "L0500")
    .flatMap((l) => (l === "L1500" ? [l, "NEW-LINE"] : [l]));
  const oldText = [
    "shared-1",
    "shared-2",
    "A-HEAD",
    ...body,
    "A-TAIL",
    "shared-3",
    "shared-4",
  ].join("\n");
  const newText = [
    "shared-1",
    "shared-2",
    "B-HEAD",
    ...bodyNew,
    "B-TAIL",
    "shared-3",
    "shared-4",
  ].join("\n");
  // Computed inside each test, not in beforeAll: Stryker's perTest coverage attributes hook-run code to no test.
  const sparseDiff = (): DiffLine[] => lineDiff(oldText, newText);

  it("keeps every line the two files still share", () => {
    // 2000 body lines minus the one deletion, plus the 4 trimmed shared lines.
    expect(stats(sparseDiff())).toEqual({ adds: 3, dels: 3, ctx: 2003 });
  });

  it("numbers an interior deletion and insertion against their own files", () => {
    const sparse = sparseDiff();
    expect(sparse.find((l) => l.text === "L0500")).toEqual({
      kind: "del",
      oldNo: 504,
      newNo: 0,
      text: "L0500",
    });
    expect(sparse.find((l) => l.text === "NEW-LINE")).toEqual({
      kind: "add",
      oldNo: 0,
      newNo: 1504,
      text: "NEW-LINE",
    });
  });

  it("numbers a context line that sits at different rows in the two files", () => {
    // Past the deleted L0500 the files are one line out of step, where a single shared offset would look right.
    expect(sparseDiff().find((l) => l.text === "L1500")).toEqual({
      kind: "ctx",
      oldNo: 1504,
      newNo: 1503,
      text: "L1500",
    });
  });

  it("numbers the trimmed prefix and suffix around the recursion", () => {
    const sparse = sparseDiff();
    expect(sparse[0]).toEqual({ kind: "ctx", oldNo: 1, newNo: 1, text: "shared-1" });
    expect(sparse.find((l) => l.text === "A-HEAD")).toEqual({
      kind: "del",
      oldNo: 3,
      newNo: 0,
      text: "A-HEAD",
    });
    expect(sparse.find((l) => l.text === "B-HEAD")).toEqual({
      kind: "add",
      oldNo: 0,
      newNo: 3,
      text: "B-HEAD",
    });
    expect(sparse.find((l) => l.text === "A-TAIL")).toEqual({
      kind: "del",
      oldNo: 2004,
      newNo: 0,
      text: "A-TAIL",
    });
    expect(sparse.find((l) => l.text === "B-TAIL")).toEqual({
      kind: "add",
      oldNo: 0,
      newNo: 2004,
      text: "B-TAIL",
    });
    expect(sparse[sparse.length - 1]).toEqual({
      kind: "ctx",
      oldNo: 2006,
      newNo: 2006,
      text: "shared-4",
    });
  });

  it("finds every match when a third of the lines changed", () => {
    // One change every three lines, so the scratch rows are rewritten constantly.
    const changedA = Array.from({ length: 2004 }, (_, i) =>
      i % 3 === 0 ? `OLD-${String(i)}` : `SAME-${String(i)}`,
    );
    const changedB = Array.from({ length: 2004 }, (_, i) =>
      i % 3 === 0 ? `NEW-${String(i)}` : `SAME-${String(i)}`,
    );
    const d = lineDiff(
      ["shared-1", "shared-2", ...changedA, "shared-3", "shared-4"].join("\n"),
      ["shared-1", "shared-2", ...changedB, "shared-3", "shared-4"].join("\n"),
    );
    expect(stats(d)).toEqual({ adds: 668, dels: 668, ctx: 1340 });
  });

  it("finds every match when the body is 40 copies of every line", () => {
    // 2000 lines over 50 values; FRESH-1/FRESH-2 appear nowhere in the old text, so counts stay exact.
    const dup = Array.from({ length: 2000 }, (_, i) => `D-${String(i % 50)}`);
    const dropped = new Set([100, 700, 1300]);
    const dupNew = dup.flatMap((l, i) => {
      const kept = dropped.has(i) ? [] : [l];
      if (i === 400) {
        return [...kept, "FRESH-1"];
      }
      if (i === 1600) {
        return [...kept, "FRESH-2"];
      }
      return kept;
    });
    const d = lineDiff(
      ["shared-1", "shared-2", "A-HEAD", ...dup, "A-TAIL", "shared-3", "shared-4"].join("\n"),
      ["shared-1", "shared-2", "B-HEAD", ...dupNew, "B-TAIL", "shared-3", "shared-4"].join("\n"),
    );
    expect(stats(d)).toEqual({ adds: 4, dels: 5, ctx: 2001 });
    expect(d.find((l) => l.text === "FRESH-1")).toEqual({
      kind: "add",
      oldNo: 0,
      newNo: 404,
      text: "FRESH-1",
    });
  });

  it("reads the same edit distance whichever file is called old", () => {
    // Many equally long alignments, so only the size is checked: LCS is symmetric, so swapping arguments swaps adds and
    // dels. A scratch row one line too far biases the split and shows only here.
    const noise = (seed: number): string[] => {
      let state = seed;
      return Array.from({ length: 2002 }, () => {
        state = (state * 48271) % 2147483647;
        return `D-${String(state % 50)}`;
      });
    };
    const left = ["shared-1", "shared-2", "A-HEAD", ...noise(1), "A-TAIL", "shared-3"].join("\n");
    const right = ["shared-1", "shared-2", "B-HEAD", ...noise(7), "B-TAIL", "shared-3"].join("\n");
    const forward = stats(lineDiff(left, right));
    const backward = stats(lineDiff(right, left));
    expect(forward.ctx).toBe(backward.ctx);
    expect(forward.adds).toBe(backward.dels);
    expect(forward.dels).toBe(backward.adds);
    expect(forward.ctx).toBeGreaterThan(100);
    expect(forward.adds).toBeGreaterThan(1000);
  });
});

// These files share 5000 lines, so going coarse past the budget is observable, and required to keep the tab responsive.
describe("lineDiff past the time budget", () => {
  const shared = Array.from({ length: 5000 }, (_, i) => `M${String(i).padStart(4, "0")}`);
  // 5002×5002 = 25,020,004 cells after the trim, just past TIME_BUDGET_CELLS.
  const coarseDiff = (): DiffLine[] =>
    lineDiff(
      ["OLD-FIRST", ...shared, "OLD-LAST"].join("\n"),
      ["NEW-FIRST", ...shared, "NEW-LAST"].join("\n"),
    );

  it("gives up on context it could have found, rather than the main thread", () => {
    expect(stats(coarseDiff())).toEqual({ adds: 5002, dels: 5002, ctx: 0 });
  });

  it("still numbers the coarse script from the top of each file", () => {
    const coarse = coarseDiff();
    expect(coarse[0]).toEqual({ kind: "del", oldNo: 1, newNo: 0, text: "OLD-FIRST" });
    expect(coarse[5001]).toEqual({ kind: "del", oldNo: 5002, newNo: 0, text: "OLD-LAST" });
    expect(coarse[5002]).toEqual({ kind: "add", oldNo: 0, newNo: 1, text: "NEW-FIRST" });
    expect(coarse[coarse.length - 1]).toEqual({
      kind: "add",
      oldNo: 0,
      newNo: 5002,
      text: "NEW-LAST",
    });
  });
});

// The prefix and suffix trims share the bound min(a, b): with max, a repeated line at the join is counted twice and
// an added line is shown as context.
describe("lineDiff prefix and suffix trimming", () => {
  it("does not count a repeated line as both prefix and suffix on an append", () => {
    expect(lineDiff("x\nx", "x\nx\nx")).toEqual([
      { kind: "ctx", oldNo: 1, newNo: 1, text: "x" },
      { kind: "ctx", oldNo: 2, newNo: 2, text: "x" },
      { kind: "add", oldNo: 0, newNo: 3, text: "x" },
    ]);
  });

  it("does not count a repeated line as both prefix and suffix on a truncation", () => {
    expect(lineDiff("x\nx\nx", "x\nx")).toEqual([
      { kind: "ctx", oldNo: 1, newNo: 1, text: "x" },
      { kind: "ctx", oldNo: 2, newNo: 2, text: "x" },
      { kind: "del", oldNo: 3, newNo: 0, text: "x" },
    ]);
  });
});

// The recursion's base case (one old line against many new) emits before, match, after; this fixture reaches the
// third group. 2001×2502 lines is 5.0M cells after the trim, so it is the recursion.
describe("lineDiff on the linear-space path, one old line against many new", () => {
  const oldLines = Array.from({ length: 2001 }, (_, i) => `L${String(i).padStart(4, "0")}`);
  const newLines = oldLines.flatMap((l, i) => (i % 4 === 0 ? [l, `INS-${String(i)}`] : [l]));
  const oldText = oldLines.join("\n");
  const newText = newLines.join("\n");
  // Computed per test: see the note on sparseDiff above.
  const scatteredDiff = (): DiffLine[] => lineDiff(oldText, newText);

  it("keeps every insertion, including the one behind the last match", () => {
    expect(stats(scatteredDiff())).toEqual({ adds: 501, dels: 0, ctx: 2001 });
  });

  it("numbers an insertion that follows its matched line", () => {
    // The final insertion is emitted after its match, the only entry numbered by that third group.
    expect(scatteredDiff().find((l) => l.text === "INS-2000")).toEqual({
      kind: "add",
      oldNo: 0,
      newNo: 2502,
      text: "INS-2000",
    });
  });

  it("gives every entry the line number of the line its text came from", () => {
    // Every entry addresses its own file at its own number, and both files are walked strictly forward.
    let lastOld = 0;
    let lastNew = 0;
    for (const l of scatteredDiff()) {
      if (l.kind === "add") {
        expect(l.oldNo).toBe(0);
      } else {
        expect(l.oldNo).toBeGreaterThan(lastOld);
        expect(oldLines[l.oldNo - 1]).toBe(l.text);
        lastOld = l.oldNo;
      }
      if (l.kind === "del") {
        expect(l.newNo).toBe(0);
      } else {
        expect(l.newNo).toBeGreaterThan(lastNew);
        expect(newLines[l.newNo - 1]).toBe(l.text);
        lastNew = l.newNo;
      }
    }
    expect([lastOld, lastNew]).toEqual([2001, 2502]);
  });
});

// The guard is `>`, so exactly TIME_BUDGET_CELLS still gets the exact algorithm.
describe("lineDiff at the time budget", () => {
  it("still finds the shared body at exactly the budget", () => {
    // 5000×5000 = 25,000,000 cells; both files differ on their first and last line, so nothing is trimmed.
    const body = Array.from({ length: 4998 }, (_, i) => `B${String(i).padStart(4, "0")}`);
    const d = lineDiff(
      ["A-HEAD", ...body, "A-TAIL"].join("\n"),
      ["B-HEAD", ...body, "B-TAIL"].join("\n"),
    );
    expect(stats(d)).toEqual({ adds: 2, dels: 2, ctx: 4998 });
  });
});

// Word-level diff: each case names exact substrings.

function marked(line: string, ranges: readonly CharRange[]): string[] {
  return ranges.map((r) => line.slice(r.start, r.end));
}

describe("wordDiff", () => {
  it("marks only the inserted call, not the whole line", () => {
    const oldLine = `\tname := os.Getenv("NAME")`;
    const newLine = `\tname := strings.TrimSpace(os.Getenv("NAME"))`;
    const wd = wordDiff(oldLine, newLine);
    expect(wd).not.toBeNull();
    expect(marked(oldLine, wd!.del)).toEqual([]);
    expect(marked(newLine, wd!.add)).toEqual(["strings.TrimSpace(", ")"]);
  });

  it("marks a one-character insert as one character", () => {
    const oldLine = `fmt.Printf("hello %s\\n", name)`;
    const newLine = `fmt.Printf("hello, %s\\n", name)`;
    const wd = wordDiff(oldLine, newLine);
    expect(wd).not.toBeNull();
    expect(marked(newLine, wd!.add)).toEqual([","]);
  });

  it("marks both sides of a replacement", () => {
    const oldLine = `fmt.Println("total", total)`;
    const newLine = `fmt.Println("sum:", total)`;
    const wd = wordDiff(oldLine, newLine);
    expect(wd).not.toBeNull();
    expect(marked(oldLine, wd!.del)).toEqual(["total"]);
    expect(marked(newLine, wd!.add)).toEqual(["sum:"]);
  });

  it("merges touching tokens into one range", () => {
    // `strings` `.` `TrimSpace` `(` are four tokens and one edit.
    const wd = wordDiff("a := b", "a := strings.Trim(b)");
    expect(wd).not.toBeNull();
    expect(wd!.add).toHaveLength(2);
  });

  it("declines a pair sharing no non-blank token", () => {
    // Only whitespace matches, so the row tint already says the whole line changed.
    expect(wordDiff("\talpha beta", "\tgamma delta")).toBeNull();
  });

  it("declines a single-token pair, which shares nothing at all", () => {
    expect(wordDiff("aaa", "bbb")).toBeNull();
  });

  it("declines a scattered rewrite past the run budget", () => {
    const oldLine = "a 1 b 2 c 3 d 4 e 5 f 6 g 7 h 8 i 9";
    const newLine = "a x b x c x d x e x f x g x h x i x";
    expect(wordDiff(oldLine, newLine)).toBeNull();
  });

  it("declines a pair past the token budget, so a pathological line keeps the row tint", () => {
    // `splitWords` emits a token per word and per whitespace run: 201 words is 401 tokens, one past MAX_WORD_TOKENS.
    const words = Array.from({ length: 201 }, (_, i) => `w${String(i)}`);
    const oldLine = words.join(" ");
    const newLine = words.map((w, i) => (i === 100 ? "CHANGED" : w)).join(" ");
    expect(wordDiff(oldLine, newLine)).toBeNull();
  });

  it("marks the removed tail when the new line is a prefix of the old", () => {
    // The walk ends with the new side exhausted, the only path into the trailing-deletion drain.
    const oldLine = "alpha beta";
    const newLine = "alpha";
    const wd = wordDiff(oldLine, newLine);
    expect(wd).not.toBeNull();
    expect(marked(oldLine, wd!.del)).toEqual([" beta"]);
    expect(wd!.add).toEqual([]);
  });

  it("declines an identical or empty line", () => {
    expect(wordDiff("same", "same")).toBeNull();
    expect(wordDiff("", "text")).toBeNull();
    expect(wordDiff("text", "")).toBeNull();
  });

  it("keeps every range inside its own line and ascending", () => {
    const oldLine = `for i := 0; i < 10; i++ {`;
    const newLine = `for i := range 10 {`;
    const wd = wordDiff(oldLine, newLine);
    expect(wd).not.toBeNull();
    for (const [line, ranges] of [
      [oldLine, wd!.del],
      [newLine, wd!.add],
    ] as const) {
      let prev = 0;
      for (const r of ranges) {
        expect(r.start).toBeGreaterThanOrEqual(prev);
        expect(r.end).toBeGreaterThan(r.start);
        expect(r.end).toBeLessThanOrEqual(line.length);
        prev = r.end;
      }
    }
  });
});

describe("wordMarks", () => {
  it("pairs a del with the add at the same offset in its run", () => {
    const d1 = { kind: "del", oldNo: 1, newNo: 0, text: "one alpha" } as const;
    const d2 = { kind: "del", oldNo: 2, newNo: 0, text: "two alpha" } as const;
    const a1 = { kind: "add", oldNo: 0, newNo: 1, text: "one beta" } as const;
    const a2 = { kind: "add", oldNo: 0, newNo: 2, text: "two beta" } as const;
    const marks = wordMarks([d1, d2, a1, a2]);
    expect(marked(d1.text, marks.get(d1) ?? [])).toEqual(["alpha"]);
    expect(marked(a1.text, marks.get(a1) ?? [])).toEqual(["beta"]);
    expect(marked(d2.text, marks.get(d2) ?? [])).toEqual(["alpha"]);
    expect(marked(a2.text, marks.get(a2) ?? [])).toEqual(["beta"]);
  });

  it("pairs within a run and never across a context line", () => {
    // Without the context boundary these two would pair.
    const d = { kind: "del", oldNo: 1, newNo: 0, text: "a b" } as const;
    const c = { kind: "ctx", oldNo: 2, newNo: 1, text: "unchanged" } as const;
    const a = { kind: "add", oldNo: 0, newNo: 2, text: "a c" } as const;
    const marks = wordMarks([d, c, a]);
    expect(marks.size).toBe(0);
  });

  it("leaves an unpaired insert unmarked", () => {
    const a = { kind: "add", oldNo: 0, newNo: 1, text: "brand new line" } as const;
    expect(wordMarks([a]).size).toBe(0);
  });

  it("marks the paired lines of a run and leaves the surplus alone", () => {
    const d = { kind: "del", oldNo: 1, newNo: 0, text: "keep x" } as const;
    const a1 = { kind: "add", oldNo: 0, newNo: 1, text: "keep y" } as const;
    const a2 = { kind: "add", oldNo: 0, newNo: 2, text: "extra" } as const;
    const marks = wordMarks([d, a1, a2]);
    expect(marks.has(d)).toBe(true);
    expect(marks.has(a1)).toBe(true);
    expect(marks.has(a2)).toBe(false);
  });

  it("finds the word change a real lineDiff produces", () => {
    const d = lineDiff("alpha\nkeep me\nomega\n", "alpha\nkeep us\nomega\n");
    const marks = wordMarks(d);
    const changed = d.filter((l) => l.kind !== "ctx");
    expect(changed).toHaveLength(2);
    for (const l of changed) {
      expect(marked(l.text, marks.get(l) ?? [])).toEqual([l.kind === "del" ? "me" : "us"]);
    }
  });
});
