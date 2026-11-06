import { describe, it, expect } from "vitest";
import { carriesContextMention } from "./context-mentions.js";
import {
  QUOTE_ATTRIBUTION,
  QUOTE_TEXT_MAX,
  appendQuote,
  blockquote,
  codeSpan,
  quoteSelection,
  splitLeadingQuote,
} from "./message-quote.js";

describe("blockquote", () => {
  it("prefixes every line and keeps an empty line a bare marker", () => {
    expect(blockquote("first\n\nsecond")).toBe("> first\n>\n> second");
  });
});

const SRC = "From Kiro's reply";
const SRC_LINE = `> ${SRC}:\n>`;

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
    expect(splitLeadingQuote(block)?.source).toBe("From run a#[[file:b]]");
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
    const quoted = splitLeadingQuote(quoteSelection("abcdef ".repeat(500), SRC))?.quote ?? "";
    expect(Array.from(quoted).length).toBeLessThanOrEqual(QUOTE_TEXT_MAX);
    expect(Array.from(quoted).length).toBeGreaterThan(QUOTE_TEXT_MAX - 10);
    expect(quoted.endsWith(" abcdef\u2026")).toBe(true);
  });

  it("never splits a surrogate pair at the cap", () => {
    const quoted = splitLeadingQuote(quoteSelection("\u{1F600}".repeat(2000), SRC))?.quote ?? "";
    expect(Array.from(quoted)).toHaveLength(QUOTE_TEXT_MAX);
    expect(quoted).not.toMatch(
      /[\uD800-\uDBFF](?![\uDC00-\uDFFF])|(?<![\uD800-\uDBFF])[\uDC00-\uDFFF]/,
    );
    expect(quoted.endsWith("\u{1F600}\u2026")).toBe(true);
  });

  it("leaves a selection at the limit whole", () => {
    const text = "x".repeat(QUOTE_TEXT_MAX);
    expect(splitLeadingQuote(quoteSelection(text, SRC))?.quote).toBe(text);
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

describe("splitLeadingQuote", () => {
  it("round-trips a quoted selection and returns the question after it", () => {
    const text = "line one\n\nline #[[file:x]] two";
    const prompt = `${appendQuote("", quoteSelection(text, SRC))}why is that?`;
    expect(splitLeadingQuote(prompt)).toEqual({ source: SRC, quote: text, rest: "why is that?" });
  });

  it.each([0, 1, 2])("round-trips a context opener after %i backslashes", (n) => {
    const text = `see #${"\\".repeat(n)}[[file:x]] here`;
    const block = quoteSelection(text, SRC);
    expect(carriesContextMention(block)).toBe(false);
    expect(splitLeadingQuote(block)?.quote).toBe(text);
  });

  it("returns an empty rest for a prompt that is only the quote", () => {
    expect(splitLeadingQuote(quoteSelection("x", SRC))).toEqual({
      source: SRC,
      quote: "x",
      rest: "",
    });
  });

  it("reads a quote sent before quotes named their source", () => {
    expect(splitLeadingQuote(`> old words\n>\n> ${QUOTE_ATTRIBUTION}\n\nq`)).toEqual({
      source: null,
      quote: "old words",
      rest: "q",
    });
  });

  it("reads a quote whose only text is its source line as having no text", () => {
    expect(splitLeadingQuote(`> ${SRC}:\n>\n>\n> ${QUOTE_ATTRIBUTION}`)).toBeNull();
  });

  it("ignores a hand-typed blockquote", () => {
    expect(splitLeadingQuote("> something I typed\n> over two lines\n\nmy question")).toBeNull();
  });

  it("ignores a quote that does not lead the prompt", () => {
    expect(splitLeadingQuote(`intro\n${quoteSelection("x", SRC)}`)).toBeNull();
  });

  it("ignores an attribution with nothing quoted above it", () => {
    expect(splitLeadingQuote(`>\n> ${QUOTE_ATTRIBUTION}\n\nq`)).toBeNull();
    expect(splitLeadingQuote(`> ${QUOTE_ATTRIBUTION}\n\nq`)).toBeNull();
  });
});
