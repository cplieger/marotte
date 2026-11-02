// A diagram type's chunk cannot be fetched (offline, or a deploy removed the old build's chunk): the fence stays
// its source under a notice saying so, rather than a parse error it did not have.
import { afterEach, describe, expect, it, vi } from "vitest";
import { renderMarkdownInto } from "./markdown.js";

vi.mock("pele/lazy", () => ({
  detectType: () => "flowchart",
  registered: () => false,
  load: () => Promise.reject(new Error("chunk fetch failed")),
}));

let host: HTMLElement;

afterEach(() => {
  host.remove();
});

describe("when the diagram's renderer cannot be loaded", () => {
  it("keeps the source under a notice saying the renderer is missing", async () => {
    host = document.createElement("div");
    document.body.appendChild(host);
    renderMarkdownInto(host, "```mermaid\nflowchart TD\n  A --> B\n```\n");
    const wrap = host.querySelector<HTMLElement>(".code-wrap");
    expect(wrap?.querySelector(".mermaid-skeleton")).not.toBeNull();
    await vi.waitFor(() => {
      expect(wrap?.getAttribute("data-mermaid")).toBe("failed");
    });
    expect(wrap?.querySelector(".mermaid-notice")?.textContent).toBe(
      "Diagram renderer could not be loaded",
    );
    expect(wrap?.querySelector("pre")?.hidden).toBe(false);
    expect(wrap?.querySelector(".mermaid-canvas")).toBeNull();
    expect(wrap?.querySelector(".mermaid-skeleton")).toBeNull();
  });
});
