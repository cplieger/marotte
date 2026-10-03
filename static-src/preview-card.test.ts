import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { buildPreviewCard, isPreviewHref, setPreviewOpener } from "./preview-card.js";
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

describe("isPreviewHref", () => {
  it.each(["/workspace/demo/index.html", "/workspace/demo/PAGE.HTM", "/workspace/root.html"])(
    "accepts %s",
    (href) => {
      expect(isPreviewHref(href)).toBe(true);
    },
  );

  it.each([
    "demo/index.html",
    "/workspace/demo/index.html?x=1",
    "/workspace/demo/index.html#top",
    "/config/index.html",
    "/workspace-old/index.html",
    "/workspace/demo/logo.svg",
    "https://example.com/index.html",
  ])("refuses %s", (href) => {
    expect(isPreviewHref(href)).toBe(false);
  });

  it("falls back to /workspace before the handshake names the root", () => {
    resetWorkspace();
    expect(isPreviewHref("/workspace/demo/index.html")).toBe(true);
  });

  it("accepts a page under a root published with a trailing slash", () => {
    setWorkspaceRoot("/custom/work/");
    expect(isPreviewHref("/custom/work/demo/index.html")).toBe(true);
    expect(isPreviewHref("/custom/work-old/index.html")).toBe(false);
  });

  it("accepts a page under the filesystem root", () => {
    setWorkspaceRoot("/");
    expect(isPreviewHref("/demo/index.html")).toBe(true);
    expect(isPreviewHref("demo/index.html")).toBe(false);
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

  it("leaves a non-HTML workspace link a link", () => {
    const host = render("[notes](/workspace/demo/notes.md)");
    expect(host.querySelector("a")?.getAttribute("href")).toBe("/workspace/demo/notes.md");
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
