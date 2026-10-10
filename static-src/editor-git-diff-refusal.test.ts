// A git diff whose base git refuses to show as text opens in a named view, picked by the rules
// from the decoded refusal: the binary notice with Download for a binary base, one sentence for a
// base that is not UTF-8, and a failed load for a refusal whose body is not the shape its status
// promises. Driven from openFileGitDiff through the real loadDiff and the real pane.
import { describe, it, expect, beforeEach, vi } from "vitest";
import type * as DomModule from "./dom.js";
import type * as ApiClientModule from "./api-client.js";
import type * as ViewerLiveModule from "./viewer-live.js";
import type * as EditorScrollModule from "./editor-scroll.js";
import type * as TabsModule from "./tabs.js";
import type * as RouterModule from "./router.js";

const { surfaces, routes, tabs } = vi.hoisted(() => {
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
    routes: new Map<string, { status: number; body: unknown }>(),
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
  setBusy: (el: Element, busy: boolean) => {
    if (busy) {
      el.setAttribute("aria-busy", "true");
    } else {
      el.removeAttribute("aria-busy");
    }
  },
}));
// The staged answer per route prefix, decoded the way apiGetTypedOrError does: a 2xx through
// the caller's decoder, a failure with its body undecoded.
vi.mock("./api-client.js", async (importOriginal) => ({
  ...(await importOriginal<typeof ApiClientModule>()),
  apiGetTypedOrError: (url: string, decoder: (v: unknown) => unknown) => {
    const prefix = [...routes.keys()].find((p) => url.startsWith(p));
    const s = prefix === undefined ? undefined : routes.get(prefix);
    if (s === undefined) {
      return Promise.resolve({ ok: false, status: 0, data: null, error: `unexpected ${url}` });
    }
    if (s.status >= 200 && s.status < 300) {
      return Promise.resolve({ ok: true, status: s.status, data: decoder(s.body), error: "" });
    }
    const error =
      typeof s.body === "object" && s.body !== null && "error" in s.body
        ? String(s.body.error)
        : "";
    return Promise.resolve({ ok: false, status: s.status, data: null, error, body: s.body });
  },
}));
vi.mock("./viewer-live.js", async (importOriginal) => ({
  ...(await importOriginal<typeof ViewerLiveModule>()),
  liveActivate: vi.fn(),
  liveDispose: vi.fn(),
  paintLiveButton: vi.fn(),
  setLiveReload: vi.fn(),
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
import { activateFile, openFileGitDiff } from "./editor-openers.js";
import { fileStates } from "./editor-types.js";
import { setWorkspaceRoot } from "./workspace.js";
import { sha256Hex } from "./sha256.js";

tabs.show = activateFile;

const PATH = "/workspace/blob.dat";
const WORK = "working copy\n";

function stageRead(): void {
  const bytes = new TextEncoder().encode(WORK);
  routes.set("/api/file?", {
    status: 200,
    body: {
      path: PATH,
      file_id: `sha256:${sha256Hex(bytes)}`,
      modified: "2026-10-01T00:00:00Z",
      content: WORK,
      size: bytes.length,
      utf8: true,
      read_only: false,
    },
  });
}

function stageBase(status: number, body: unknown): void {
  routes.set("/api/git/show", { status, body });
}

function hidden(el: HTMLElement): boolean {
  return el.classList.contains("hidden");
}

async function openedDiff(): Promise<void> {
  openFileGitDiff(PATH);
  await vi.waitFor(() => {
    expect(fileStates.get(PATH)?.loaded).toBe(true);
  });
}

beforeEach(() => {
  resetActionFramework();
  setWorkspaceRoot("/workspace");
  routes.clear();
  fileStates.clear();
  tabs.active = "";
  tabs.minted.clear();
  for (const el of Object.values(surfaces)) {
    el.className = "";
    if (el !== surfaces.editorViewer) {
      el.replaceChildren();
    }
  }
  stageRead();
});

describe("a git diff whose base is binary", () => {
  it("shows the binary notice and Download, with no pane, Edit or Go to line", async () => {
    stageBase(415, { error: "binary revision", code: "binary" });
    await openedDiff();
    const m = fileStates.get(PATH)?.mode.value;
    expect(m?.kind === "diff" && m.diffSource.kind === "git" ? m.diffSource.base : "").toBe(
      "binary",
    );
    expect(hidden(surfaces.editorNotice)).toBe(false);
    expect(surfaces.editorNotice.querySelector("p")?.textContent).toBe(
      "This is a binary file. Download it to open it.",
    );
    expect(hidden(surfaces.editorDownloadBtn)).toBe(false);
    for (const el of [
      surfaces.editorDiffPane,
      surfaces.editorViewer,
      surfaces.editorContent,
      surfaces.editorError,
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

describe("a git diff whose base is not UTF-8", () => {
  it("says no diff can be shown, and offers Edit and Download for the working copy", async () => {
    stageBase(415, { error: "revision is not UTF-8 text", code: "not_utf8" });
    await openedDiff();
    expect(hidden(surfaces.editorNotice)).toBe(false);
    expect(surfaces.editorNotice.textContent).toBe("Cannot show a diff of this file.");
    expect(hidden(surfaces.editorDownloadBtn)).toBe(false);
    expect(hidden(surfaces.editorEditBtn)).toBe(false);
    for (const el of [
      surfaces.editorDiffPane,
      surfaces.editorError,
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

// The control: the same path paints a pane for a base git showed.
describe("a git diff whose base is text", () => {
  it("paints the diff pane and no notice", async () => {
    stageBase(200, { content: "base\n" });
    await openedDiff();
    expect(hidden(surfaces.editorDiffPane)).toBe(false);
    expect(hidden(surfaces.editorNotice)).toBe(true);
  });
});

describe("a base refusal that is not the shape its status promises", () => {
  it.each([
    ["no code", { error: "binary revision" }],
    ["the cap's code", { error: "x", code: "too_large" }],
  ])("is a failed diff, not a view, for a 415 with %s", async (_name, body) => {
    stageBase(415, body);
    await openedDiff();
    expect(fileStates.get(PATH)?.error.value).toBe(
      "Failed to load diff: Could not read HEAD for blob.dat",
    );
    expect(hidden(surfaces.editorError)).toBe(false);
    expect(hidden(surfaces.editorNotice)).toBe(true);
  });
});
