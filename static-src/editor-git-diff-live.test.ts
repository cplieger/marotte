// A git diff opened for the first time follows the file on disk like any other view: the live
// machine starts when the diff settles, checks every 5 s, and offers "Switch to live refresh" on
// a change. Driven from openFileGitDiff through the real loadDiff, the real live machine and the
// tab-open path, with only the server and the tab strip staged.
import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import type * as DomModule from "./dom.js";
import type * as ApiClientModule from "./api-client.js";
import type * as EditorScrollModule from "./editor-scroll.js";
import type * as TabsModule from "./tabs.js";
import type * as RouterModule from "./router.js";

const { surfaces, routes, hits, tabs } = vi.hoisted(() => {
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
    editorLiveBtn: document.createElement("button"),
    editorLiveReason: document.createElement("span"),
  };
  document.createElement("div").append(s.editorViewer);
  return {
    surfaces: s,
    routes: new Map<string, { status: number; body: unknown }>(),
    hits: new Map<string, number>(),
    tabs: {
      active: "",
      minted: new Map<string, string>(),
      show: (_path: string) => {
        /* pointed at the real activateFile below */
      },
    },
  };
});

vi.mock("./dom.js", async (importOriginal) => ({
  ...(await importOriginal<typeof DomModule>()),
  $: surfaces,
  setBusy: () => undefined,
}));
// The staged answer per route prefix, longest prefix first so the stat route is not read as the
// file route; a 2xx goes through the caller's decoder.
vi.mock("./api-client.js", async (importOriginal) => ({
  ...(await importOriginal<typeof ApiClientModule>()),
  apiGetTypedOrError: (url: string, decoder: (v: unknown) => unknown) => {
    const prefix = [...routes.keys()]
      .sort((a, b) => b.length - a.length)
      .find((p) => url.startsWith(p));
    const s = prefix === undefined ? undefined : routes.get(prefix);
    if (prefix === undefined || s === undefined) {
      return Promise.resolve({ ok: false, status: 0, data: null, error: `unexpected ${url}` });
    }
    hits.set(prefix, (hits.get(prefix) ?? 0) + 1);
    return Promise.resolve({ ok: true, status: s.status, data: decoder(s.body), error: "" });
  },
}));
vi.mock("./editor-scroll.js", async (importOriginal) => ({
  ...(await importOriginal<typeof EditorScrollModule>()),
  scrollToEditorLine: () => undefined,
  flashEditorLine: () => undefined,
  trackEditorView: () => undefined,
  captureSelection: () => undefined,
  restoreEditorView: () => undefined,
  bindDiffView: () => undefined,
}));
vi.mock("./tabs.js", async (importOriginal) => ({
  ...(await importOriginal<typeof TabsModule>()),
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
vi.mock("./router.js", async (importOriginal) => ({
  ...(await importOriginal<typeof RouterModule>()),
  pushRoute: () => undefined,
}));

import { resetActionFramework } from "./actions/__test-helpers__/action-test-setup.js";
import { activateFile, closeEditorFile, openFileGitDiff } from "./editor-openers.js";
import { fileStates } from "./editor-types.js";
import { LIVE_SWITCH, _resetLiveForTest } from "./viewer-live.js";
import { setWorkspaceRoot } from "./workspace.js";
import { sha256Hex } from "./sha256.js";

tabs.show = activateFile;

const PATH = "/workspace/notes.txt";
const STAT = "/api/file/stat?";
const WORK = "working copy\n";

function idOf(content: string): string {
  return `sha256:${sha256Hex(new TextEncoder().encode(content))}`;
}

function stageStat(fileId: string): void {
  routes.set(STAT, {
    status: 200,
    body: {
      path: PATH,
      file_id: fileId,
      modified: "2026-10-01T00:00:00Z",
      size: WORK.length,
      large: false,
      binary: false,
      utf8: true,
      read_only: false,
    },
  });
}

async function openedDiff(): Promise<void> {
  openFileGitDiff(PATH);
  await vi.waitFor(() => {
    expect(fileStates.get(PATH)?.loaded).toBe(true);
  });
  await vi.waitFor(() => {
    expect(hits.get(STAT)).toBe(1);
  });
}

/** Fire the poll's next tick. Its stat resolves through a dynamic import no fake timer flushes,
 *  so a caller waits for the answer in real time. */
async function nextTick(): Promise<void> {
  await vi.advanceTimersByTimeAsync(5000);
}

beforeEach(() => {
  // setTimeout only: the poll's cadence, while the load's promises and frames run for real.
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout", "Date"] });
  resetActionFramework();
  _resetLiveForTest();
  setWorkspaceRoot("/workspace");
  routes.clear();
  hits.clear();
  fileStates.clear();
  tabs.active = "";
  tabs.minted.clear();
  surfaces.editorLiveBtn.className = "hidden";
  routes.set("/api/file?", {
    status: 200,
    body: {
      path: PATH,
      file_id: idOf(WORK),
      modified: "2026-10-01T00:00:00Z",
      content: WORK,
      size: WORK.length,
      utf8: true,
      read_only: false,
    },
  });
  routes.set("/api/git/show", { status: 200, body: { content: "base\n" } });
  stageStat(idOf(WORK));
});

afterEach(() => {
  closeEditorFile(PATH);
  vi.useRealTimers();
});

describe("a git diff opened for the first time", () => {
  it("checks the file on disk when it settles and again 5 s later", async () => {
    await openedDiff();
    await nextTick();
    await vi.waitFor(() => {
      expect(hits.get(STAT)).toBe(2);
    });
  });

  it("offers 'Switch to live refresh' when the file changes on disk", async () => {
    await openedDiff();
    expect(surfaces.editorLiveBtn.classList.contains("hidden")).toBe(true);
    stageStat(idOf("changed\n"));
    await nextTick();
    await vi.waitFor(() => {
      expect(surfaces.editorLiveBtn.classList.contains("hidden")).toBe(false);
    });
    expect(surfaces.editorLiveBtn.textContent).toBe(LIVE_SWITCH);
  });
});
