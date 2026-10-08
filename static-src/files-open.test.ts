import { describe, it, expect, vi, beforeAll } from "vitest";
import type * as Dom from "./dom.js";
import type * as Bus from "./bus.js";
import type * as Tabs from "./tabs.js";
import type * as EditorOpeners from "./editor-openers.js";
import type * as Modals from "./modals.js";
import type * as Confirm from "./confirm.js";
import type * as Upload from "./upload.js";
import type * as Chat from "./chat.js";
import type * as FilesBrowserDrop from "./files-browser-drop.js";
import type * as FilesSearch from "./files-search.js";
import type * as FilesPicker from "./files-picker.js";
import type * as ApiClient from "./api-client.js";
import type * as Transport from "./transport.js";
import type * as Store from "./store.js";

const h = vi.hoisted(() => {
  // A memoizing registry, so a row built into `$.fbList` is still there to click.
  const els = new Map<string, HTMLElement>();
  function stub(key: string): HTMLElement {
    const existing = els.get(key);
    if (existing !== undefined) {
      return existing;
    }
    const made =
      key === "fbPath" || key === "fbSearchInput"
        ? document.createElement("input")
        : key.startsWith("fb") && key !== "fbList"
          ? document.createElement("button")
          : document.createElement("div");
    els.set(key, made);
    return made;
  }
  return { els, stub };
});

// Each mock spreads the original, so a name the graph reaches is never missing.
vi.mock("./dom.js", async (importOriginal) => ({
  ...(await importOriginal<typeof Dom>()),
  $: new Proxy({} as Record<string, HTMLElement>, { get: (_t, k: string) => h.stub(k) }),
  el: () => document.createElement("div"),
  byId: (id: string) => h.stub(id),
  setBusy: vi.fn(),
}));
vi.mock("./bus.js", async (importOriginal) => ({
  ...(await importOriginal<typeof Bus>()),
  onSSE: undefined,
  onBus: vi.fn(),
  BUS_KEYS_ESCAPE: "escape",
}));
vi.mock("./tabs.js", async (importOriginal) => ({
  ...(await importOriginal<typeof Tabs>()),
  ...(await import("./__test-helpers__/tabs-mock.js")).tabsMock(),
  getActiveTabKind: vi.fn(() => "files"),
  filesTabIdFor: vi.fn(() => "t-files"),
}));
vi.mock("./editor-openers.js", async (importOriginal) => ({
  ...(await importOriginal<typeof EditorOpeners>()),
  openFileGitDiff: undefined,
  openFileDiff: undefined,
  openFile: vi.fn(),
  openFileInBackground: vi.fn(),
}));
vi.mock("./modals.js", async (importOriginal) => ({
  ...(await importOriginal<typeof Modals>()),
  closeModal: vi.fn(),
}));
vi.mock("./confirm.js", async (importOriginal) => ({
  ...(await importOriginal<typeof Confirm>()),
  confirm: vi.fn().mockResolvedValue(true),
}));
vi.mock("./upload.js", async (importOriginal) => ({
  ...(await importOriginal<typeof Upload>()),
  uploadFiles: vi.fn(),
}));
vi.mock("./chat.js", async (importOriginal) => ({
  ...(await importOriginal<typeof Chat>()),
  attachPathsToActiveChat: vi.fn(),
}));
vi.mock("./files-browser-drop.js", async (importOriginal) => ({
  ...(await importOriginal<typeof FilesBrowserDrop>()),
  initBrowserDragDrop: vi.fn(),
}));
vi.mock("./files-search.js", async (importOriginal) => ({
  ...(await importOriginal<typeof FilesSearch>()),
  initFilesSearch: vi.fn(),
  resetFilesSearch: vi.fn(),
  closeFilesSearch: vi.fn(),
}));
vi.mock("./files-picker.js", async (importOriginal) => ({
  ...(await importOriginal<typeof FilesPicker>()),
  setOnUploadComplete: vi.fn(),
}));
vi.mock("./api-client.js", async (importOriginal) => ({
  ...(await importOriginal<typeof ApiClient>()),
  apiPost: vi.fn(),
  apiGet: vi.fn(),
  apiGetOrError: vi.fn(() => Promise.resolve({ ok: false, status: 0, data: null, error: "" })),
}));
vi.mock("@cplieger/ui-primitives/skeleton", () => ({
  skeletonTiming: () => ({ cancel: vi.fn(), commit: (r: () => void) => r() }),
}));
vi.mock("./transport.js", async (importOriginal) => ({
  ...(await importOriginal<typeof Transport>()),
  send: vi.fn(),
}));
vi.mock("./store.js", async (importOriginal) => ({
  ...(await importOriginal<typeof Store>()),
  getActiveId: vi.fn(() => ""),
  get: vi.fn(() => undefined),
  getActive: vi.fn(() => undefined),
  getSessions: vi.fn(() => []),
  tabStatusFor: vi.fn(() => ""),
}));

import { $ } from "./dom.js";
import { apiGetOrError } from "./api-client.js";
import { openFile, openFileInBackground } from "./editor-openers.js";
import { FB_NAME } from "./files-shared.js";
import { initFileBrowser, releaseFilesTab, showFilesTab } from "./files.js";
import { openTab } from "./tabs.js";
import { setWorkspaceRoot } from "./workspace.js";

let shown = "";

async function rowIn(dir: string, name: string): Promise<HTMLElement> {
  if (shown !== "") {
    releaseFilesTab(shown);
  }
  shown = dir;
  vi.mocked(apiGetOrError).mockResolvedValue({
    ok: true,
    status: 200,
    data: { files: [{ name, isDir: false }], writable: true },
    error: "",
  });
  showFilesTab(dir);
  await new Promise((r) => setTimeout(r, 0));
  const row = [...$.fbList.children].find((r) => (r as HTMLElement).dataset["name"] === name);
  if (!(row instanceof HTMLElement)) {
    throw new Error(`no row for ${name} in ${dir}`);
  }
  return row;
}

function editorPaths(): string[] {
  return vi.mocked(openFile).mock.calls.map((c) => c[0]);
}

function clickName(row: HTMLElement): void {
  row.querySelector<HTMLElement>(`.${FB_NAME}`)?.click();
}

beforeAll(() => {
  setWorkspaceRoot("/workspace");
  initFileBrowser();
});

describe("clicking a file row's name", () => {
  it("opens a page in its Preview tab, not the editor", async () => {
    clickName(await rowIn("/workspace/demo", "index.html"));
    expect(openTab).toHaveBeenCalledWith({ kind: "web", ref: "/workspace/demo/index.html" });
    expect(openFile).not.toHaveBeenCalled();
  });

  it("opens an upper-case .HTM page in its Preview tab", async () => {
    clickName(await rowIn("/workspace/demo", "about.HTM"));
    expect(openTab).toHaveBeenCalledWith({ kind: "web", ref: "/workspace/demo/about.HTM" });
  });

  it("opens a page directly in the workspace root in the editor", async () => {
    clickName(await rowIn("/workspace", "root.html"));
    expect(editorPaths()).toEqual(["/workspace/root.html"]);
    expect(openTab).not.toHaveBeenCalled();
  });

  it("opens a page under a dot folder in the editor", async () => {
    clickName(await rowIn("/workspace/.uploads", "x.html"));
    expect(editorPaths()).toEqual(["/workspace/.uploads/x.html"]);
    expect(openTab).not.toHaveBeenCalled();
  });

  it("opens any other file in the editor", async () => {
    clickName(await rowIn("/workspace/demo", "notes.txt"));
    expect(editorPaths()).toEqual(["/workspace/demo/notes.txt"]);
    expect(openTab).not.toHaveBeenCalled();
  });
});

describe("middle-clicking a file row", () => {
  function middleClick(row: HTMLElement): void {
    row
      .querySelector(`.${FB_NAME}`)
      ?.dispatchEvent(new MouseEvent("auxclick", { button: 1, bubbles: true, cancelable: true }));
  }

  it("opens a page's Preview tab in the background", async () => {
    middleClick(await rowIn("/workspace/demo", "index.html"));
    expect(openTab).toHaveBeenCalledWith({
      kind: "web",
      ref: "/workspace/demo/index.html",
      activate: false,
    });
    expect(openFileInBackground).not.toHaveBeenCalled();
  });

  it("opens any other file in a background editor tab", async () => {
    middleClick(await rowIn("/workspace/demo", "notes.txt"));
    expect(openFileInBackground).toHaveBeenCalledWith("/workspace/demo/notes.txt");
    expect(openTab).not.toHaveBeenCalled();
  });
});
