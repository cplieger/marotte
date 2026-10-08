import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { buildPreviewCard, previewHrefPage, setPreviewOpener } from "./preview-card.js";
import { createMarkdownStream, renderMarkdownInto } from "./markdown.js";
import { setWorkspaceRoot, _resetForTest as resetWorkspace } from "./workspace.js";

const opened = vi.fn();

beforeEach(() => {
  setWorkspaceRoot("/workspace");
  setPreviewOpener(opened);
  opened.mockClear();
});

afterEach(() => {
  resetWorkspace();
});

function render(md: string): HTMLElement {
  const host = document.createElement("div");
  renderMarkdownInto(host, md);
  return host;
}

describe("previewHrefPage", () => {
  it.each(["/workspace/demo/index.html", "/workspace/demo/PAGE.HTM"])("accepts %s", (href) => {
    expect(previewHrefPage(href)).toBe(href);
  });

  it.each([
    "demo/index.html",
    "/workspace/demo/index.html?x=1",
    "/workspace/demo/index.html#top",
    "/workspace/root.html",
    "/workspace/.uploads/x.html",
    "/config/index.html",
    "/workspace-old/index.html",
    "/workspace/demo/logo.svg",
    "https://example.com/index.html",
  ])("refuses %s", (href) => {
    expect(previewHrefPage(href)).toBeNull();
  });

  it("decodes a percent-escaped destination once", () => {
    expect(previewHrefPage("/workspace/my%20demo/index.html")).toBe(
      "/workspace/my demo/index.html",
    );
  });

  it("falls back to /workspace before the handshake names the root", () => {
    resetWorkspace();
    expect(previewHrefPage("/workspace/demo/index.html")).toBe("/workspace/demo/index.html");
  });
});

describe("buildPreviewCard", () => {
  it("is a button holding the label, the folder and Open, chrome marked", () => {
    const card = buildPreviewCard("/workspace/demo/index.html", [document.createTextNode("Demo")]);
    expect(card.tagName).toBe("BUTTON");
    expect(card.type).toBe("button");
    expect(card.querySelector(".preview-card-label")?.textContent).toBe("Demo");
    const folder = card.querySelector(".preview-card-folder");
    const open = card.querySelector(".preview-card-open");
    expect(folder?.textContent).toBe("demo");
    expect(open?.textContent).toBe("Open");
    expect(folder?.hasAttribute("data-vk-chrome")).toBe(true);
    expect(open?.hasAttribute("data-vk-chrome")).toBe(true);
    expect(card.querySelector(".preview-card-label")?.hasAttribute("data-vk-chrome")).toBe(false);
  });

  it("names the file when the label is empty", () => {
    const card = buildPreviewCard("/workspace/demo/index.html", []);
    expect(card.querySelector(".preview-card-label")?.textContent).toBe("index.html");
  });

  it("calls the injected opener with the absolute path", () => {
    buildPreviewCard("/workspace/demo/index.html", []).click();
    expect(opened).toHaveBeenCalledWith("/workspace/demo/index.html");
  });
});

describe("the markdown renderer", () => {
  it("renders a workspace HTML link as a card inside its paragraph", () => {
    const host = render("See [Demo](/workspace/demo/index.html) here.");
    const card = host.querySelector("p > button.preview-card");
    expect(card).not.toBeNull();
    expect(card?.querySelector(".preview-card-label")?.textContent).toBe("Demo");
    expect(host.querySelector("a")).toBeNull();
  });

  it("turns a link title into the card's tooltip", () => {
    const host = render('[Demo](/workspace/demo/index.html "The demo")');
    expect(host.querySelector(".preview-card")?.getAttribute("data-tooltip")).toBe("The demo");
  });

  it("leaves a link to a page directly in the workspace root a file link", () => {
    const host = render("[root](/workspace/root.html)");
    expect(host.querySelector("a")?.getAttribute("href")).toBe("/file//workspace/root.html");
    expect(host.querySelector(".preview-card")).toBeNull();
  });

  it("opens the decoded page from a percent-escaped link", () => {
    render("[Demo](/workspace/my%20demo/index.html)")
      .querySelector<HTMLElement>(".preview-card")
      ?.click();
    expect(opened).toHaveBeenCalledWith("/workspace/my demo/index.html");
  });

  it("renders a card for a page under a root published with a trailing slash", () => {
    setWorkspaceRoot("/custom/work/");
    render("[Demo](/custom/work/demo/index.html)")
      .querySelector<HTMLElement>(".preview-card")
      ?.click();
    expect(opened).toHaveBeenCalledWith("/custom/work/demo/index.html");
  });

  it("leaves a page beside a custom root, not beneath it, without a card", () => {
    setWorkspaceRoot("/custom/work");
    expect(
      render("[x](/custom/work-old/demo/index.html)").querySelector(".preview-card"),
    ).toBeNull();
  });

  it("renders a card for a page under the filesystem root", () => {
    setWorkspaceRoot("/");
    render("[Demo](/demo/index.html)").querySelector<HTMLElement>(".preview-card")?.click();
    expect(opened).toHaveBeenCalledWith("/demo/index.html");
  });

  it("leaves a non-HTML workspace link a link", () => {
    const host = render("[notes](/workspace/demo/notes.md)");
    expect(host.querySelector("a")?.getAttribute("href")).toBe("/file//workspace/demo/notes.md");
    expect(host.querySelector(".preview-card")).toBeNull();
  });

  it("still neutralises an unsafe href", () => {
    const host = render("[x](javascript:alert(1))");
    expect(host.querySelector("a")?.getAttribute("href")).toBe("#");
  });

  it("builds the same card streamed one character at a time", () => {
    const md = "See [Demo](/workspace/demo/index.html) here.";
    const host = document.createElement("div");
    const stream = createMarkdownStream(host, { flushIntervalMs: 0 });
    for (const ch of md) {
      stream.writeDelta(ch);
    }
    stream.end();
    expect(host.querySelector("p > button.preview-card .preview-card-label")?.textContent).toBe(
      "Demo",
    );
    expect(host.querySelectorAll("[data-vk-caret]").length).toBeLessThanOrEqual(1);
  });
});
