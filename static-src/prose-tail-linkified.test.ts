import { describe, it, expect } from "vitest";
import { renderMarkdownInto, createMarkdownStream } from "./markdown.js";

function titles(root: HTMLElement): string[] {
  return [...root.querySelectorAll<HTMLButtonElement>("button.inline-file-link")].map(
    (b) => b.title,
  );
}

describe("a path in the final paragraph", () => {
  it("is linked on replay", () => {
    const host = document.createElement("div");
    renderMarkdownInto(host, "Done.\n\nOpen src/outer.ts to review it.");
    expect(titles(host)).toEqual(["src/outer.ts"]);
  });

  it("is linked when a stream ends", () => {
    const host = document.createElement("div");
    const stream = createMarkdownStream(host, { flushIntervalMs: 0 });
    stream.writeDelta("Done.\n\nOpen ");
    stream.writeDelta("src/outer.ts to review it.");
    stream.end();
    expect(titles(host)).toEqual(["src/outer.ts"]);
  });

  it("is linked once when the message already closed its last paragraph", () => {
    const host = document.createElement("div");
    renderMarkdownInto(host, "Open src/outer.ts to review it.\n\n");
    expect(titles(host)).toEqual(["src/outer.ts"]);
  });

  it("stays unlinked while the stream is still writing it", () => {
    const host = document.createElement("div");
    const stream = createMarkdownStream(host, { flushIntervalMs: 0 });
    stream.writeDelta("Open src/outer.ts to review it.");
    expect(titles(host)).toEqual([]);
    stream.end();
    expect(titles(host)).toEqual(["src/outer.ts"]);
  });

  it("leaves a final unclosed fence to the code-block pass", () => {
    const host = document.createElement("div");
    renderMarkdownInto(host, "```\nsee src/outer.ts\n");
    expect(titles(host)).toEqual([]);
  });
});
