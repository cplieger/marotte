import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type * as EditorOpeners from "./editor-openers.js";
import type * as Tabs from "./tabs.js";

vi.mock("./editor-openers.js", async (importOriginal) => ({
  ...(await importOriginal<typeof EditorOpeners>()),
  openFile: vi.fn(),
}));
vi.mock("./tabs.js", async (importOriginal) => ({
  ...(await importOriginal<typeof Tabs>()),
  ...(await import("./__test-helpers__/tabs-mock.js")).tabsMock(),
}));

import { buildAttachmentPill, initAttachmentPillCallbacks } from "./attachment-pill.js";
import { openFile } from "./editor-openers.js";
import { initLinkifyCallbacks } from "./linkify.js";
import { renderMarkdownInto } from "./markdown.js";
import { openFileOrPage } from "./navigate.js";
import { openTab } from "./tabs.js";
import { setWorkspaceRoot, _resetForTest as resetWorkspace } from "./workspace.js";

beforeEach(() => {
  setWorkspaceRoot("/workspace");
  initLinkifyCallbacks({ open: openFileOrPage });
  initAttachmentPillCallbacks({ open: openFileOrPage });
});

afterEach(() => {
  resetWorkspace();
});

function render(md: string): HTMLElement {
  const host = document.createElement("div");
  renderMarkdownInto(host, md);
  return host;
}

function primaryClick(target: Element | null): void {
  target?.dispatchEvent(new MouseEvent("click", { bubbles: true, cancelable: true, button: 0 }));
}

describe("a bare path in prose", () => {
  it("opens a page in its Preview tab", () => {
    primaryClick(render("Open /workspace/demo/index.html to see it.").querySelector("button"));
    expect(openTab).toHaveBeenCalledWith({ kind: "web", ref: "/workspace/demo/index.html" });
    expect(openFile).not.toHaveBeenCalled();
  });

  it("opens a page's source at a line", () => {
    primaryClick(
      render("The bug is at /workspace/demo/index.html:12 here.").querySelector("button"),
    );
    expect(openFile).toHaveBeenCalledWith("/workspace/demo/index.html", 12);
    expect(openTab).not.toHaveBeenCalled();
  });
});

describe("a markdown file link", () => {
  it("opens a page linked with a query in its Preview tab", () => {
    const link = render("[demo](/workspace/demo/index.html?v=2)").querySelector("a");
    primaryClick(link);
    expect(openTab).toHaveBeenCalledWith({ kind: "web", ref: "/workspace/demo/index.html" });
  });

  it.each(["/workspace/demo/index.html?v=2", "/workspace/demo/index.html#intro"])(
    "addresses the Preview tab from %s, the tab its click opens",
    (dest) => {
      const link = render(`[demo](${dest})`).querySelector("a");
      expect(link?.getAttribute("href")).toBe("/web/workspace/demo/index.html");
    },
  );

  it("addresses the source from a #L line", () => {
    const link = render("[demo](/workspace/demo/index.html#L7)").querySelector("a");
    expect(link?.getAttribute("href")).toBe("/file//workspace/demo/index.html#L7");
  });

  it("opens a page's source at a #L line", () => {
    primaryClick(render("[demo](/workspace/demo/index.html#L7)").querySelector("a"));
    expect(openFile).toHaveBeenCalledWith("/workspace/demo/index.html", 7);
    expect(openTab).not.toHaveBeenCalled();
  });
});

describe("an attachment pill", () => {
  it("opens an attached page in its Preview tab", () => {
    const pill = buildAttachmentPill({ path: "/workspace/demo/index.html", name: "index.html" });
    pill.querySelector<HTMLButtonElement>(".attachment-open")?.click();
    expect(openTab).toHaveBeenCalledWith({ kind: "web", ref: "/workspace/demo/index.html" });
  });

  it("opens any other attachment in the editor", () => {
    const pill = buildAttachmentPill({ path: "/workspace/notes.md", name: "notes.md" });
    pill.querySelector<HTMLButtonElement>(".attachment-open")?.click();
    expect(vi.mocked(openFile).mock.calls.map((c) => c[0])).toEqual(["/workspace/notes.md"]);
    expect(openTab).not.toHaveBeenCalled();
  });
});
