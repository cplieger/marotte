//
// The image read surface, and the SVG rule that rides on it.
//
// Pinned: an image never reaches the whole-read route (its bytes come from the download route,
// keyed by the identity stat reported, so the server refuses any other bytes); a `.svg` is
// DISPLAYED and never offered as something to open, because the download route answers
// `Content-Type: image/svg+xml`, script-capable when navigated to, while an SVG referenced as an
// image is inert by specification.
import { describe, it, expect, beforeEach, vi } from "vitest";

const { surfaces, apiGetTypedOrError, tabs, live, diff } = vi.hoisted(() => {
  const s = {
    editorViewer: document.createElement("div"),
    editorEditGutter: document.createElement("div"),
    editorContent: document.createElement("textarea"),
    editorMarkdown: document.createElement("div"),
    editorImage: document.createElement("div"),
    editorNotice: document.createElement("div"),
    editorDiffPane: document.createElement("div"),
    editorFilename: document.createElement("span"),
    editorError: document.createElement("div"),
    editorConflictOverlay: document.createElement("div"),
    editorEditBtn: document.createElement("button"),
    editorSaveBtn: document.createElement("button"),
    editorCancelBtn: document.createElement("button"),
    editorDiffBtn: document.createElement("button"),
    editorDownloadBtn: document.createElement("button"),
    editorGotoBtn: document.createElement("button"),
    editorGoto: document.createElement("form"),
    editorReadonlyLabel: document.createElement("span"),
  };
  document.createElement("div").append(s.editorViewer);
  return {
    surfaces: s,
    apiGetTypedOrError: vi.fn(),
    tabs: {
      active: "",
      minted: new Map<string, string>(),
      show: (_path: string) => {
        /* pointed at the real activateFile below */
      },
    },
    live: {
      reload: null as
        null | ((state: unknown, stat: unknown, stillCurrent: () => boolean) => Promise<void>),
    },
    diff: { outcome: { status: "cancelled" } as unknown },
  };
});

vi.mock("./dom.js", () => ({
  $: surfaces,
  setBusy: (el: Element, busy: boolean) => {
    if (busy) {
      el.setAttribute("aria-busy", "true");
    } else {
      el.removeAttribute("aria-busy");
    }
  },
}));
vi.mock("./store.js", () => ({ getActiveId: () => "", get: vi.fn(() => undefined) }));
vi.mock("./api-client.js", () => ({
  apiGetTypedOrError,
  apiGet: vi.fn(),
  apiGetOrError: vi.fn(),
}));
vi.mock("./actions/editor.js", () => ({
  suggestResolution: undefined,
  fetchAgentLines: { cancel: () => undefined, dispatch: () => Promise.resolve(null) },
  loadDiff: { dispatch: () => ({ outcome: Promise.resolve(diff.outcome) }) },
}));
vi.mock("./viewer-live.js", () => ({
  GONE_SENTENCE: "This file is no longer on disk.",
  liveActivate: vi.fn(),
  liveDispose: vi.fn(),
  paintLiveButton: vi.fn(),
  setLiveReload: (
    fn: (state: unknown, stat: unknown, stillCurrent: () => boolean) => Promise<void>,
  ) => {
    live.reload = fn;
  },
}));
vi.mock("./editor-scroll.js", () => ({
  scrollToEditorLine: () => undefined,
  flashEditorLine: () => undefined,
  trackEditorView: () => undefined,
  captureSelection: () => undefined,
  restoreEditorView: () => undefined,
  bindDiffView: () => undefined,
}));
vi.mock("./tabs.js", () => ({
  openEditorView: (path: string) => {
    let id = tabs.minted.get(path);
    if (id === undefined) {
      id = `tb_${String(tabs.minted.size + 1).padStart(3, "0")}`;
      tabs.minted.set(path, id);
    }
    if (tabs.active !== id) {
      tabs.active = id;
      tabs.show(path);
    }
    return Promise.resolve();
  },
  getActiveTabId: () => tabs.active,
  getActiveTabKind: () => "editor",
  tabIdFor: (_kind: string, ref = "") => tabs.minted.get(ref) ?? "",
  setTabDirty: () => undefined,
}));
vi.mock("./router.js", () => ({ pushRoute: () => undefined }));
vi.mock("./actions/index.js", () => ({ registerCleanup: () => undefined }));

import { activateFile, openFile, openFileGitDiff } from "./editor-openers.js";

tabs.show = activateFile;
import { fileStates } from "./editor-types.js";
import { isViewableImage } from "./file-extensions.js";

const IMG_ID = `sha256:${"b".repeat(64)}`;
const NEXT_ID = `sha256:${"c".repeat(64)}`;

function statOf(path: string, id = IMG_ID): Record<string, unknown> {
  return {
    path,
    file_id: id,
    modified: "2026-10-01T00:00:00Z",
    size: 10,
    large: false,
    binary: true,
    utf8: false,
    read_only: false,
  };
}

function img(): HTMLImageElement | null {
  return surfaces.editorImage.querySelector<HTMLImageElement>("img");
}

function hidden(el: HTMLElement): boolean {
  return el.classList.contains("hidden");
}

function urls(): string[] {
  return apiGetTypedOrError.mock.calls.map((c) => String(c[0]));
}

async function open(path: string): Promise<void> {
  openFile(path);
  for (let i = 0; i < 6; i++) {
    await Promise.resolve();
  }
}

beforeEach(() => {
  apiGetTypedOrError.mockReset();
  apiGetTypedOrError.mockImplementation((url: string) => {
    const path = decodeURIComponent(url.slice(url.indexOf("path=") + 5));
    return Promise.resolve({ ok: true, status: 200, data: statOf(path), error: "" });
  });
  fileStates.clear();
  diff.outcome = { status: "cancelled" };
  tabs.active = "";
  tabs.minted.clear();
  for (const el of Object.values(surfaces)) {
    el.className = "";
    if (el !== surfaces.editorViewer) {
      el.replaceChildren();
    }
  }
});

describe("isViewableImage", () => {
  it("covers every raster the browser paints, case-insensitively", () => {
    for (const p of [
      "out/shot.png",
      "a.JPG",
      "a.jpeg",
      "a.gif",
      "a.webp",
      "a.avif",
      "a.ico",
      "a.bmp",
    ]) {
      expect(isViewableImage(p), p).toBe(true);
    }
  });

  it("includes .svg, which is what routes it to the image surface", () => {
    expect(isViewableImage("docs/arch.svg")).toBe(true);
  });

  it("leaves text and unknown files to the text path", () => {
    for (const p of ["main.go", "README.md", "notes", "a.png.bak", "dir.png/file.go"]) {
      expect(isViewableImage(p), p).toBe(false);
    }
  });

  it("does not read a dotfile's name as an extension", () => {
    expect(isViewableImage(".png")).toBe(false);
    expect(isViewableImage("a/.svg")).toBe(false);
  });
});

describe("opening an image", () => {
  it("asks stat and never the whole-read route", async () => {
    await open("out/shot.png");
    expect(urls()).toEqual(["/api/file/stat?path=out%2Fshot.png"]);
  });

  // The control: the same call path DOES read a text file, once.
  it("reads a text file after its stat, exactly once", async () => {
    apiGetTypedOrError.mockImplementation((url: string) =>
      Promise.resolve(
        url.startsWith("/api/file/stat")
          ? {
              ok: true,
              status: 200,
              data: { ...statOf("main.go"), binary: false, utf8: true },
              error: "",
            }
          : { ok: false, status: 0, data: null, error: "" },
      ),
    );
    await open("main.go");
    expect(urls()).toEqual(["/api/file/stat?path=main.go", "/api/file?path=main.go"]);
  });

  // The image's bytes are pinned to the identity the shell shows.
  it("points the <img> at the byte route keyed by the file's identity", async () => {
    await open("out/shot.png");
    expect(img()?.getAttribute("src")).toBe(
      `/api/file/download?path=out%2Fshot.png&file_id=${encodeURIComponent(IMG_ID)}`,
    );
  });

  it("swaps the identity key when live refresh sees the file change", async () => {
    await open("out/shot.png");
    const state = fileStates.get("out/shot.png");
    await live.reload?.(state, statOf("out/shot.png", NEXT_ID), () => true);
    expect(img()?.getAttribute("src")).toContain(encodeURIComponent(NEXT_ID));
  });

  it("shows the image surface and hides every text surface", async () => {
    await open("out/shot.png");
    expect(hidden(surfaces.editorImage)).toBe(false);
    for (const el of [
      surfaces.editorViewer,
      surfaces.editorContent,
      surfaces.editorEditGutter,
      surfaces.editorMarkdown,
      surfaces.editorDiffPane,
      surfaces.editorNotice,
    ]) {
      expect(hidden(el)).toBe(true);
    }
  });

  it("hides every text-editing control and offers Download", async () => {
    await open("out/shot.png");
    for (const el of [
      surfaces.editorEditBtn,
      surfaces.editorSaveBtn,
      surfaces.editorCancelBtn,
      surfaces.editorDiffBtn,
      surfaces.editorGotoBtn,
    ]) {
      expect(hidden(el)).toBe(true);
    }
    expect(hidden(surfaces.editorDownloadBtn)).toBe(false);
  });

  it("names the file in the alt text so a failed load is legible", async () => {
    await open("out/shot.png");
    expect(img()?.alt).toBe("out/shot.png");
  });

  it("repaints from scratch, leaving no trace of the previous image", async () => {
    await open("out/a.png");
    await open("out/b.png");
    expect(surfaces.editorImage.querySelectorAll("img")).toHaveLength(1);
    expect(img()?.getAttribute("src")).toContain("path=out%2Fb.png");
  });

  it("marks the state loaded so a re-activation does not fetch again", async () => {
    await open("out/shot.png");
    expect(fileStates.get("out/shot.png")?.loaded).toBe(true);
    expect(fileStates.get("out/shot.png")?.mode.value.kind).toBe("image");
  });
});

describe("an image over the viewer's cap", () => {
  it("shows the too-large sentence and Download where Edit would be, and nothing else", async () => {
    apiGetTypedOrError.mockImplementation(() =>
      Promise.resolve({
        ok: true,
        status: 200,
        data: { ...statOf("big.jpg"), file_id: undefined, large: true, size: 5 << 20 },
        error: "",
      }),
    );
    await open("big.jpg");
    expect(img()).toBeNull();
    expect(surfaces.editorNotice.textContent).toBe(
      "File is too large to display. Download it to view.",
    );
    expect(hidden(surfaces.editorDownloadBtn)).toBe(false);
    for (const el of [
      surfaces.editorEditBtn,
      surfaces.editorSaveBtn,
      surfaces.editorCancelBtn,
      surfaces.editorDiffBtn,
      surfaces.editorGotoBtn,
      surfaces.editorReadonlyLabel,
    ]) {
      expect(hidden(el)).toBe(true);
    }
  });
});

describe("a file over the cap reached from a diff too large to show", () => {
  it("shows the sentence and Download, with no note beside them", async () => {
    diff.outcome = { status: "success", value: { kind: "too_large" } };
    apiGetTypedOrError.mockImplementation(() =>
      Promise.resolve({
        ok: true,
        status: 200,
        data: { ...statOf("big.log"), file_id: undefined, large: true, size: 5 << 20 },
        error: "",
      }),
    );
    openFileGitDiff("big.log");
    for (let i = 0; i < 10; i++) {
      await Promise.resolve();
    }
    expect(fileStates.get("big.log")?.mode.value).toEqual({ kind: "large" });
    expect(surfaces.editorNotice.textContent).toBe(
      "File is too large to display. Download it to view.",
    );
    expect(hidden(surfaces.editorReadonlyLabel)).toBe(true);
    expect(hidden(surfaces.editorDownloadBtn)).toBe(false);
  });
});

describe("a binary working copy reached from a diff", () => {
  it("shows the file's binary view and Download, with no note beside them", async () => {
    diff.outcome = { status: "success", value: { kind: "binary" } };
    openFileGitDiff("blob.dat");
    for (let i = 0; i < 10; i++) {
      await Promise.resolve();
    }
    expect(fileStates.get("blob.dat")?.mode.value).toEqual({ kind: "binary" });
    expect(fileStates.get("blob.dat")?.error.value).toBe("");
    expect(surfaces.editorNotice.querySelector("p")?.textContent).toBe(
      "This is a binary file. Download it to open it.",
    );
    expect(hidden(surfaces.editorDownloadBtn)).toBe(false);
    for (const el of [
      surfaces.editorError,
      surfaces.editorEditBtn,
      surfaces.editorDiffPane,
      surfaces.editorReadonlyLabel,
    ]) {
      expect(hidden(el)).toBe(true);
    }
  });
});

describe("an SVG is an image to display, never a page to open", () => {
  it("renders it as an image without the whole-read route", async () => {
    await open("docs/arch.svg");
    expect(hidden(surfaces.editorImage)).toBe(false);
    expect(img()?.getAttribute("src")).toContain("/api/file/download?path=docs%2Farch.svg");
    expect(urls().some((u) => u.startsWith("/api/file?"))).toBe(false);
  });

  it("offers no raw link, frame or embed for it, and nothing but the image", async () => {
    await open("docs/arch.svg");
    const host = surfaces.editorImage;
    expect(host.querySelector("a, iframe, object, embed, [href]")).toBeNull();
    expect([...host.children].map((c) => c.tagName)).toEqual(["IMG"]);
  });
});
