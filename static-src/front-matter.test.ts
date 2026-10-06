import { describe, it, expect } from "vitest";
import { splitFrontMatter } from "./front-matter.js";

function keys(text: string): string[] {
  return splitFrontMatter(text).fields.map((f) => f.key);
}

function valueOf(text: string, key: string): string | undefined {
  return splitFrontMatter(text).fields.find((f) => f.key === key)?.value;
}

function itemsOf(text: string, key: string): string[] | undefined {
  return splitFrontMatter(text).fields.find((f) => f.key === key)?.items;
}

describe("splitFrontMatter fence detection", () => {
  it("reports no front-matter for a plain document and keeps the whole text as body", () => {
    const doc = "# Requirements\n\nSome prose.\n";
    const r = splitFrontMatter(doc);
    expect(r.present).toBe(false);
    expect(r.fields).toEqual([]);
    expect(r.body).toBe(doc);
  });

  it("splits a fenced header off the body", () => {
    const r = splitFrontMatter("---\ninclusion: always\n---\n# Title\n\nBody.\n");
    expect(r.present).toBe(true);
    expect(r.body).toBe("# Title\n\nBody.\n");
  });

  // A `---` a few lines down is a horizontal rule, not a header.
  it("ignores a fence that does not open the document", () => {
    const r = splitFrontMatter("# Title\n\n---\ninclusion: always\n---\n");
    expect(r.present).toBe(false);
  });

  it("treats an empty block as no front-matter", () => {
    const r = splitFrontMatter("---\n---\nbody\n");
    expect(r.present).toBe(false);
  });

  // A malformed header must render as markdown rather than vanish.
  it("keeps an unterminated block as body text", () => {
    const doc = "---\ninclusion: always\n";
    const r = splitFrontMatter(doc);
    expect(r.present).toBe(false);
    expect(r.body).toBe(doc);
  });

  // The closing fence must be a line exactly `---`, or an unterminated header swallows text up to the next rule.
  it.each([
    { desc: "a longer rule", fence: "----" },
    { desc: "a suffixed fence", fence: "---draft" },
  ])("does not accept $desc as the closing fence", ({ fence }) => {
    const doc = `---\ninclusion: always\n${fence}\n# Title\n\nBody.\n`;
    const r = splitFrontMatter(doc);
    expect(r.present).toBe(false);
    expect(r.fields).toEqual([]);
    expect(r.body).toBe(doc);
  });

  // A rule-looking line inside a header must not end the search either.
  it("keeps looking past a rule-looking line for the real fence", () => {
    const r = splitFrontMatter("---\ninclusion: always\n----\nname: x\n---\n# Title\n");
    expect(r.present).toBe(true);
    expect(r.body).toBe("# Title\n");
    expect(keys("---\ninclusion: always\n----\nname: x\n---\n")).toEqual(["inclusion", "name"]);
  });

  // An editor leaves trailing whitespace behind invisibly.
  it("tolerates trailing whitespace on the closing fence", () => {
    const r = splitFrontMatter("---\nname: x\n---  \n# Title\n");
    expect(r.present).toBe(true);
    expect(r.body).toBe("# Title\n");
  });

  it("tolerates a BOM and CRLF line endings", () => {
    const doc = "\ufeff---\r\ninclusion: fileMatch\r\n---\r\n# Title\r\n";
    const r = splitFrontMatter(doc);
    expect(r.present).toBe(true);
    expect(valueOf(doc, "inclusion")).toBe("fileMatch");
    expect(r.body).toBe("# Title\n");
  });

  // A lone CR would make the whole header one line, so the fence check fails (found by the Go parser's fuzz target).
  it("folds a lone carriage return", () => {
    const r = splitFrontMatter("---\rname: x\r---\rbody\r");
    expect(r.present).toBe(true);
    expect(r.fields.map((f) => f.key)).toEqual(["name"]);
  });
});

describe("splitFrontMatter field parsing", () => {
  it("reads flat scalars and strips one pair of quotes", () => {
    const doc = '---\nname: kiro\ninclusion: fileMatch\nfileMatchPattern: "marotte/**"\n---\n';
    expect(keys(doc)).toEqual(["name", "inclusion", "fileMatchPattern"]);
    expect(valueOf(doc, "fileMatchPattern")).toBe("marotte/**");
    expect(valueOf(doc, "name")).toBe("kiro");
  });

  // Every agent spec uses `description: >`, and a per-line reader returns the indicator.
  it("folds a `>` block scalar into one line", () => {
    const doc = "---\ndescription: >\n  First line\n  second line\nmodel: opus\n---\n";
    expect(valueOf(doc, "description")).toBe("First line second line");
    expect(valueOf(doc, "model")).toBe("opus");
  });

  it("folds a `|` block scalar and accepts chomping and indent indicators", () => {
    expect(valueOf("---\ndescription: |-\n  a\n  b\n---\n", "description")).toBe("a b");
    expect(valueOf("---\ndescription: >2\n  a\n  b\n---\n", "description")).toBe("a b");
  });

  it("does not treat `>foo` as a block scalar header", () => {
    expect(valueOf("---\ndescription: >foo\n---\n", "description")).toBe(">foo");
  });

  it("reads a flow sequence", () => {
    expect(itemsOf('---\ntools: [read, write, "grep"]\n---\n', "tools")).toEqual([
      "read",
      "write",
      "grep",
    ]);
  });

  it("reads a block sequence", () => {
    const doc = "---\ntools:\n  - read\n  - write\nmodel: sonnet\n---\n";
    expect(itemsOf(doc, "tools")).toEqual(["read", "write"]);
    // The sequence reader must hand the cursor back at the right line.
    expect(valueOf(doc, "model")).toBe("sonnet");
  });

  it("keeps a declared key with no value rather than dropping it", () => {
    const doc = "---\ntools:\nmodel: opus\n---\n";
    expect(keys(doc)).toEqual(["tools", "model"]);
    expect(itemsOf(doc, "tools")).toEqual([]);
  });

  it("skips comments and blank lines", () => {
    const doc = "---\n# a comment\n\ninclusion: manual\n---\n";
    expect(keys(doc)).toEqual(["inclusion"]);
  });

  // The editor shows one file's own header, so it invents no default the file did not declare.
  it("defaults nothing: an undeclared inclusion is simply absent", () => {
    expect(keys("---\nname: skill\n---\n")).toEqual(["name"]);
  });

  it("preserves the author's key order", () => {
    expect(keys("---\nzeta: 1\nalpha: 2\nmiddle: 3\n---\n")).toEqual(["zeta", "alpha", "middle"]);
  });
});
