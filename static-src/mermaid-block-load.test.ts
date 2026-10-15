// The first diagram of a page: pele is not loaded yet, so the fence shows its source until the renderer arrives.
// Its own file, because the loaded module is page state no other test can unload.
import { afterEach, describe, expect, it, vi } from "vitest";
import { renderMarkdownInto } from "./markdown.js";

const FLOW = "flowchart TD\n  A --> B\n";

let host: HTMLElement;

afterEach(() => {
  host.remove();
});

describe("before pele has loaded", () => {
  it("shows the source, then draws the diagram once the renderer arrives", async () => {
    host = document.createElement("div");
    document.body.appendChild(host);
    renderMarkdownInto(host, "```mermaid\n" + FLOW + "```\n");
    const wrap = host.querySelector(".code-wrap");
    expect(wrap?.getAttribute("data-mermaid")).toBe("pending");
    expect(host.querySelector("pre")?.hidden).toBe(false);
    expect(host.querySelector(".mermaid-canvas")).toBeNull();
    await vi.waitFor(() => {
      expect(wrap?.getAttribute("data-mermaid")).toBe("drawn");
    });
    expect(host.querySelector(".mermaid-canvas > svg")).not.toBeNull();
    expect(host.querySelector("pre")?.hidden).toBe(true);
  });
});
