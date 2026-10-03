import { describe, it, expect, vi, beforeAll, beforeEach } from "vitest";
import type * as Dom from "./dom.js";
import type * as Bus from "./bus.js";
import type * as Tabs from "./tabs.js";
import type * as WebOpen from "./web-open.js";
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
  return { els, stub, openWebPreview: vi.fn() };
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
vi.mock("./web-open.js", async (importOriginal) => ({
  ...(await importOriginal<typeof WebOpen>()),
  openWebPreview: h.openWebPreview,
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
import { apiGet } from "./api-client.js";
import { FB_CHECK } from "./files-shared.js";
import { initFileBrowser, releaseFilesTab, showFilesTab } from "./files.js";
import { setWorkspaceRoot } from "./workspace.js";

const DIR = "/workspace/demo";

async function show(): Promise<void> {
  showFilesTab(DIR);
  await new Promise((r) => setTimeout(r, 0));
}

function select(...names: string[]): void {
  for (const row of $.fbList.children) {
    const name = (row as HTMLElement).dataset["name"] ?? "";
    const check = row.querySelector<HTMLInputElement>(`.${FB_CHECK}`);
    if (check !== null && names.includes(name)) {
      check.checked = true;
      check.dispatchEvent(new Event("change"));
    }
  }
}

const preview = (): HTMLButtonElement => $.fbPreview;

beforeAll(() => {
  setWorkspaceRoot("/workspace");
  initFileBrowser();
});

beforeEach(async () => {
  releaseFilesTab(DIR);
  h.openWebPreview.mockClear();
  vi.mocked(apiGet).mockImplementation(() =>
    Promise.resolve({
      files: [
        { name: "assets", isDir: true },
        { name: "index.html", isDir: false },
        { name: "about.HTM", isDir: false },
        { name: "notes.txt", isDir: false },
      ],
      writable: true,
    }),
  );
  await show();
});

describe("#fb-preview", () => {
  it("is enabled for one selected HTML file and opens its preview", () => {
    select("index.html");
    expect(preview().disabled).toBe(false);
    preview().click();
    expect(h.openWebPreview).toHaveBeenCalledWith("/workspace/demo/index.html");
  });

  it("accepts an upper-case .HTM name", () => {
    select("about.HTM");
    expect(preview().disabled).toBe(false);
  });

  it("is disabled for two selected pages", () => {
    select("index.html", "about.HTM");
    expect(preview().disabled).toBe(true);
  });

  it("is disabled for a directory", () => {
    select("assets");
    expect(preview().disabled).toBe(true);
  });

  it("is disabled for a text file", () => {
    select("notes.txt");
    expect(preview().disabled).toBe(true);
  });
});
