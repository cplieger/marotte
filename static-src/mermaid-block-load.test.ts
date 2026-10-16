// Diagrams drawn before pele or their type has loaded: the fence shows a skeleton until the renderer arrives.
// Its own file, because the loaded module is page state no other test can unload.
import { afterEach, describe, expect, it, vi } from "vitest";
import { createMarkdownStream, renderMarkdownInto } from "./markdown.js";

const FLOW = "flowchart TD\n  A --> B\n";

let host: HTMLElement;

afterEach(() => {
  host.remove();
});

describe("before pele has loaded", () => {
  it("shows a skeleton, never the source, then draws the diagram once the renderer arrives", async () => {
    host = document.createElement("div");
    document.body.appendChild(host);
    renderMarkdownInto(host, "```mermaid\n" + FLOW + "```\n");
    const wrap = host.querySelector(".code-wrap");
    expect(wrap?.getAttribute("data-mermaid")).toBe("pending");
    expect(host.querySelector("pre")?.hidden).toBe(true);
    expect(host.querySelector(".mermaid-skeleton")?.getAttribute("aria-label")).toBe(
      "Drawing diagram",
    );
    expect(host.querySelector(".mermaid-canvas")).toBeNull();
    await vi.waitFor(() => {
      expect(wrap?.getAttribute("data-mermaid")).toBe("drawn");
    });
    expect(host.querySelector(".mermaid-canvas > svg")).not.toBeNull();
    expect(host.querySelector(".mermaid-skeleton")).toBeNull();
    expect(host.querySelector("pre")?.hidden).toBe(true);
  });

  // This file loads only flowchart, so a sequence diagram still waits for its chunk when the reply ends.
  it("keeps a live reply's skeleton up when it ends before the diagram's type has loaded", async () => {
    host = document.createElement("div");
    document.body.appendChild(host);
    const stream = createMarkdownStream(host, { flushIntervalMs: 0 });
    stream.writeDelta("```mermaid\nsequenceDiagram\n  Alice->>Bob: hello\n");
    stream.writeDelta("```\n");
    stream.end();
    const wrap = host.querySelector(".code-wrap");
    expect(wrap?.getAttribute("data-mermaid")).toBe("pending");
    expect(host.querySelector("pre")?.checkVisibility()).toBe(false);
    expect(host.querySelector(".mermaid-skeleton")?.getAttribute("aria-label")).toBe(
      "Drawing diagram",
    );
    await vi.waitFor(() => {
      expect(wrap?.getAttribute("data-mermaid")).toBe("drawn");
    });
    expect(host.querySelector(".mermaid-canvas > svg")).not.toBeNull();
    expect(host.querySelector(".mermaid-skeleton")).toBeNull();
    expect(host.querySelector("pre")?.checkVisibility()).toBe(false);
  });
});
