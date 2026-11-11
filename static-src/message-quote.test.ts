import { describe, it, expect } from "vitest";
import fc from "fast-check";
import { carriesContextMention } from "./context-mentions.js";
import {
  QUOTE_ATTRIBUTION,
  QUOTE_TEXT_MAX,
  appendQuote,
  blockquote,
  codeSpan,
  quoteSelection,
  leadQuote,
  readQuoted,
  splitQuotes,
} from "./message-quote.js";

describe("blockquote", () => {
  it("prefixes every line and keeps an empty line a bare marker", () => {
    expect(blockquote("first\n\nsecond")).toBe("> first\n>\n> second");
  });
});

const SRC = "From Kiro's reply";
const SRC_LINE = `> ${SRC}:\n>`;

/** The one quote `text` holds, or undefined. */
function onlyQuote(text: string): { source: string | null; quote: string } | undefined {
  const parts = splitQuotes(text);
  const [part] = parts;
  return parts.length === 1 && part?.kind === "quote"
    ? { source: part.source, quote: part.quote }
    : undefined;
}

describe("quoteSelection", () => {
  it("names the source, quotes the text, then the attribution as its own quoted paragraph", () => {
    expect(quoteSelection("the passage", SRC)).toBe(
      `${SRC_LINE}\n> the passage\n>\n> ${QUOTE_ATTRIBUTION}`,
    );
  });

  it("names a command source in its own code span", () => {
    expect(quoteSelection("PASS", `From the output of ${codeSpan("go test ./...")}`)).toBe(
      `> From the output of \`go test ./...\`:\n>\n> PASS\n>\n> ${QUOTE_ATTRIBUTION}`,
    );
  });

  it("keeps the source on one line", () => {
    expect(quoteSelection("x", "From the tool call `a\n  b`").split("\n")[0]).toBe(
      "> From the tool call `a b`:",
    );
  });

  it("escapes a context token in the source as in the quoted words", () => {
    const block = quoteSelection("x", "From run a#[[file:b]]");
    expect(carriesContextMention(block)).toBe(false);
    expect(block.split("\n")[0]).toBe("> From run a#\\[[file:b]]:");
    expect(onlyQuote(block)?.source).toBe("From run a#[[file:b]]");
  });

  it("keeps the selection's leading and trailing whitespace", () => {
    expect(quoteSelection("  make check-pins\n", SRC)).toBe(
      `${SRC_LINE}\n>   make check-pins\n>\n>\n> ${QUOTE_ATTRIBUTION}`,
    );
    expect(quoteSelection("\tx ", SRC)).toBe(`${SRC_LINE}\n> \tx \n>\n> ${QUOTE_ATTRIBUTION}`);
  });

  it("quotes nothing for a blank selection", () => {
    expect(quoteSelection(" \n\t", SRC)).toBe("");
    expect(quoteSelection("\r\n \r", SRC)).toBe("");
  });

  it("normalises CRLF so no line keeps a stray return", () => {
    expect(quoteSelection("a\r\nb\rc", SRC)).toBe(
      `${SRC_LINE}\n> a\n> b\n> c\n>\n> ${QUOTE_ATTRIBUTION}`,
    );
  });

  it("caps a long selection at a word, ending on an ellipsis, within the limit", () => {
    // 1,499 kept code points end one letter into a word, which the cut steps back from.
    const quoted = onlyQuote(quoteSelection("abcdef ".repeat(500), SRC))?.quote ?? "";
    expect(Array.from(quoted).length).toBeLessThanOrEqual(QUOTE_TEXT_MAX);
    expect(Array.from(quoted).length).toBeGreaterThan(QUOTE_TEXT_MAX - 10);
    expect(quoted.endsWith(" abcdef\u2026")).toBe(true);
  });

  it("never splits a surrogate pair at the cap", () => {
    const quoted = onlyQuote(quoteSelection("\u{1F600}".repeat(2000), SRC))?.quote ?? "";
    expect(Array.from(quoted)).toHaveLength(QUOTE_TEXT_MAX);
    expect(quoted).not.toMatch(
      /[\uD800-\uDBFF](?![\uDC00-\uDFFF])|(?<![\uD800-\uDBFF])[\uDC00-\uDFFF]/,
    );
    expect(quoted.endsWith("\u{1F600}\u2026")).toBe(true);
  });

  it("leaves a selection at the limit whole", () => {
    const text = "x".repeat(QUOTE_TEXT_MAX);
    expect(onlyQuote(quoteSelection(text, SRC))?.quote).toBe(text);
  });

  it("escapes a context token so the quote never reads as one", () => {
    const block = quoteSelection("see #[[file:a.ts]] here", SRC);
    expect(carriesContextMention(block)).toBe(false);
    expect(block).toContain("#\\[[file:a.ts]]");
  });
});

describe("codeSpan", () => {
  it("wraps plain text in single backticks", () => {
    expect(codeSpan("go test ./...")).toBe("`go test ./...`");
  });

  // CommonMark 6.1: the fence is a backtick run no run inside matches, padded when the text touches a backtick.
  it("fences text holding backticks with a longer run", () => {
    expect(codeSpan("a`b")).toBe("``a`b``");
    expect(codeSpan("a``b`c")).toBe("```a``b`c```");
  });

  it("pads text that starts or ends with a backtick", () => {
    expect(codeSpan("`x`")).toBe("`` `x` ``");
  });
});

describe("appendQuote", () => {
  it("leaves a blank line after the block of an empty draft, for the question", () => {
    expect(appendQuote("  \n", "> q")).toBe("> q\n\n");
  });

  it("separates the block from a draft by one blank line", () => {
    expect(appendQuote("my draft\n\n\n", "> q")).toBe("my draft\n\n> q\n\n");
  });
});

describe("splitQuotes", () => {
  const quote = (text: string, source: string | null = SRC) =>
    ({ kind: "quote", source, quote: text }) as const;
  const words = (text: string) => ({ kind: "text", text }) as const;

  it("round-trips a quoted selection and returns the question after it", () => {
    const text = "line one\n\nline #[[file:x]] two";
    const prompt = `${appendQuote("", quoteSelection(text, SRC))}why is that?`;
    expect(splitQuotes(prompt)).toEqual([quote(text), words("why is that?")]);
  });

  it.each([0, 1, 2])("round-trips a context opener after %i backslashes", (n) => {
    const text = `see #${"\\".repeat(n)}[[file:x]] here`;
    const block = quoteSelection(text, SRC);
    expect(carriesContextMention(block)).toBe(false);
    expect(onlyQuote(block)?.quote).toBe(text);
  });

  it("returns the quote alone for a prompt that is only the quote", () => {
    expect(splitQuotes(quoteSelection("x", SRC))).toEqual([quote("x")]);
  });

  it("reads a quote sent before quotes named their source", () => {
    expect(splitQuotes(`> old words\n>\n> ${QUOTE_ATTRIBUTION}\n\nq`)).toEqual([
      quote("old words", null),
      words("q"),
    ]);
  });

  it("reads every quote wherever it sits, the reader's words between them trimmed", () => {
    const first = appendQuote("intro", quoteSelection("one", SRC));
    const prompt = `${appendQuote(`${first}  why?\n`, quoteSelection("two", "From Kiro's thinking"))}and?`;
    expect(splitQuotes(prompt)).toEqual([
      words("intro"),
      quote("one"),
      words("why?"),
      quote("two", "From Kiro's thinking"),
      words("and?"),
    ]);
  });

  it("reads two quotes with no blank line between as two", () => {
    const prompt = `${quoteSelection("one", SRC)}\n${quoteSelection("two", SRC)}`;
    expect(splitQuotes(prompt)).toEqual([quote("one"), quote("two")]);
  });

  it.each([
    ["the whole selection", QUOTE_ATTRIBUTION],
    ["its last line", `line a\n${QUOTE_ATTRIBUTION}`],
    ["a middle line", `line a\n${QUOTE_ATTRIBUTION}\nline b`],
    ["the line before a source-like line", `${QUOTE_ATTRIBUTION}\nFrom the docs:\n\nline b`],
    ["the line before a source-like last line", `line a\n${QUOTE_ATTRIBUTION}\nFrom the docs:`],
  ])("keeps a passage holding the attribution sentence as %s whole", (_, passage) => {
    const prompt = `${appendQuote("", quoteSelection(passage, SRC))}what now?`;
    expect(splitQuotes(prompt)).toEqual([quote(passage), words("what now?")]);
    expect(splitQuotes(quoteSelection(passage, SRC))).toEqual([quote(passage)]);
  });

  it("leaves a typed `>` line touching a quote as text, above it or below it", () => {
    const block = quoteSelection("q", SRC);
    expect(splitQuotes(`> my note\n${block}\n\nwhy`)).toEqual([
      words("> my note"),
      quote("q"),
      words("why"),
    ]);
    expect(splitQuotes(`${block}\n> my note\n\nwhy`)).toEqual([
      quote("q"),
      words("> my note\n\nwhy"),
    ]);
  });

  // The one passage the stored form cannot tell from two quotes: words, then the attribution sentence on a line of
  // its own, a source-like line and a blank one, then more words.
  const ambiguous = new RegExp(
    `\\S.*\\n${QUOTE_ATTRIBUTION.replace(".", "\\.")}\\nFrom [^\\n]*:\\n\\n.*\\S`,
    "s",
  );

  it("round-trips any two passages and the words around them", () => {
    const passage = fc
      .array(
        fc.constantFrom(
          QUOTE_ATTRIBUTION,
          "From the docs:",
          "> typed",
          "#[[file:a]]",
          "word",
          " ",
          "",
        ),
        { minLength: 1, maxLength: 8 },
      )
      .map((lines) => lines.join("\n"))
      .filter((p) => p.trim() !== "" && !ambiguous.test(p));
    const reply = fc.constantFrom("why?", "a\nb", "> mine");
    fc.assert(
      fc.property(passage, passage, reply, reply, (one, two, between, after) => {
        const prompt = `${appendQuote(`${appendQuote("", quoteSelection(one, SRC))}${between}`, quoteSelection(two, SRC))}${after}`;
        expect(splitQuotes(prompt)).toEqual([
          quote(one.trimEnd()),
          words(between),
          quote(two.trimEnd()),
          words(after),
        ]);
      }),
    );
  });

  it("reads a quote whose only text is its source line as the reader's text", () => {
    const block = `> ${SRC}:\n>\n>\n> ${QUOTE_ATTRIBUTION}`;
    expect(splitQuotes(block)).toEqual([words(block)]);
  });

  it("leaves a hand-typed blockquote as text, before a quote or after one", () => {
    const typed = "> something I typed\n> over two lines";
    expect(splitQuotes(`${typed}\n\nmy question`)).toEqual([words(`${typed}\n\nmy question`)]);
    expect(splitQuotes(`${quoteSelection("x", SRC)}\n\n${typed}`)).toEqual([
      quote("x"),
      words(typed),
    ]);
  });

  it("leaves an attribution with nothing quoted above it as text", () => {
    expect(splitQuotes(`>\n> ${QUOTE_ATTRIBUTION}\n\nq`)).toEqual([
      words(`>\n> ${QUOTE_ATTRIBUTION}\n\nq`),
    ]);
    expect(splitQuotes(`> ${QUOTE_ATTRIBUTION}\n\nq`)).toEqual([
      words(`> ${QUOTE_ATTRIBUTION}\n\nq`),
    ]);
  });

  it("returns nothing for blank text", () => {
    expect(splitQuotes(" \n ")).toEqual([]);
  });
});

describe("leadQuote", () => {
  it("takes the quote opening the prompt apart from the rest", () => {
    const one = quoteSelection("one", SRC);
    const two = quoteSelection("two", SRC);
    expect(leadQuote(appendQuote(`${appendQuote("", one)}why?`, two))).toEqual({
      lead: { source: SRC, quote: "one" },
      rest: [
        { kind: "text", text: "why?" },
        { kind: "quote", source: SRC, quote: "two" },
      ],
    });
  });

  it("has no lead for a prompt opening on the reader's words", () => {
    expect(leadQuote(appendQuote("intro", quoteSelection("x", SRC)))).toEqual({
      lead: null,
      rest: [
        { kind: "text", text: "intro" },
        { kind: "quote", source: SRC, quote: "x" },
      ],
    });
  });
});

describe("readQuoted", () => {
  it("reads a leading quote as its band does, then the rest after a blank line", () => {
    expect(readQuoted(`${appendQuote("", quoteSelection("the\npassage", SRC))}why?`)).toBe(
      `${SRC}: the\npassage\n\nwhy?`,
    );
  });

  it("reads a quote with nothing after it, or one that names no source, as the passage alone", () => {
    expect(readQuoted(quoteSelection("x", SRC))).toBe(`${SRC}: x`);
    expect(readQuoted(`> old words\n>\n> ${QUOTE_ATTRIBUTION}\n\nq`)).toBe("old words\n\nq");
  });

  it("reads every quote as its band does, wherever it sits", () => {
    const one = quoteSelection("one", SRC);
    const two = quoteSelection("two", "From Kiro's thinking");
    expect(readQuoted(`${appendQuote(`${appendQuote("", one)}why?`, two)}and?`)).toBe(
      `${SRC}: one\n\nwhy?\n\nFrom Kiro's thinking: two\n\nand?`,
    );
    expect(readQuoted(`${appendQuote(appendQuote("", one), two)}`)).toBe(
      `${SRC}: one\n\nFrom Kiro's thinking: two`,
    );
    expect(readQuoted(appendQuote("intro", one))).toBe(`intro\n\n${SRC}: one`);
  });

  it("leaves any other text as it was, a hand-typed blockquote included", () => {
    expect(readQuoted("> quoted\n\nreply")).toBe("> quoted\n\nreply");
    expect(readQuoted("  plain  ")).toBe("  plain  ");
  });
});
