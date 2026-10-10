// ---------------------------------------------------------------------------
// Settling a git diff: the working side and a clean buffer come from one verified read, every
// settled state is announced, and live refresh re-asks git for the ref the diff was opened
// against rather than the caption the load found.
// ---------------------------------------------------------------------------

import { describe, it, expect, beforeEach, onTestFinished, vi } from "vitest";
import type * as TabsModule from "./tabs.js";
import type * as ApiClientModule from "./api-client.js";
import type * as RouterModule from "./router.js";
import type * as EditorConflictModule from "./editor-conflict.js";
import type * as EditorModesModule from "./editor-modes.js";
import type * as EditorUiModule from "./editor-ui.js";
import type * as EditorScrollModule from "./editor-scroll.js";
import type * as EditorPaneModule from "./editor-pane.js";
import type * as ViewerLiveModule from "./viewer-live.js";
import type * as ActionsEditorModule from "./actions/editor.js";
import type * as ActionsIndexModule from "./actions/index.js";
import type * as DomModule from "./dom.js";
import type { FileRead, FileStat } from "./wire/types.gen.js";

type Outcome =
  | { status: "success"; value: unknown }
  | { status: "error"; error: { message: string } }
  | { status: "cancelled" };

let outcome: Outcome = { status: "cancelled" };
const dispatch = vi.fn((_args: { path: string; repo: string; ref: string }) => ({
  outcome: Promise.resolve(outcome),
}));
const apiGetTypedOrError = vi.fn(() =>
  Promise.resolve({ ok: false, status: 0, data: null, error: "" }),
);

vi.mock("./tabs.js", async (importOriginal) => ({
  ...(await importOriginal<typeof TabsModule>()),
  openEditorView: vi.fn(() => Promise.resolve()),
  tabIdFor: () => "",
  getActiveTabId: () => "",
  setTabDirty: vi.fn(),
}));
vi.mock("./api-client.js", async (importOriginal) => ({
  ...(await importOriginal<typeof ApiClientModule>()),
  apiGet: vi.fn(() => Promise.resolve(null)),
  apiGetOrError: vi.fn(),
  apiGetTypedOrError: () => apiGetTypedOrError(),
}));
vi.mock("./router.js", async (importOriginal) => ({
  ...(await importOriginal<typeof RouterModule>()),
  pushRoute: vi.fn(),
}));
vi.mock("./editor-conflict.js", async (importOriginal) => ({
  ...(await importOriginal<typeof EditorConflictModule>()),
  abortSuggestion: vi.fn(),
  clearSuggestionState: vi.fn(),
}));
vi.mock("./editor-modes.js", async (importOriginal) => ({
  ...(await importOriginal<typeof EditorModesModule>()),
  restoreUI: vi.fn(),
}));
vi.mock("./editor-ui.js", async (importOriginal) => ({
  ...(await importOriginal<typeof EditorUiModule>()),
  applyPendingLine: vi.fn(),
  fetchAgentLines: vi.fn(),
  clearAgentLineCache: vi.fn(),
  requestedBy: () => "diff",
  showSurface: vi.fn(),
}));
vi.mock("./editor-scroll.js", async (importOriginal) => ({
  ...(await importOriginal<typeof EditorScrollModule>()),
  captureSelection: vi.fn(),
  restoreEditorView: vi.fn(),
}));
vi.mock("./editor-pane.js", async (importOriginal) => ({
  ...(await importOriginal<typeof EditorPaneModule>()),
  viewer: () => ({ atBottom: () => false, pinBottom: vi.fn() }),
  paneBody: () => ({ scrollTo: vi.fn() }),
}));
vi.mock("./viewer-live.js", async (importOriginal) => ({
  ...(await importOriginal<typeof ViewerLiveModule>()),
  liveActivate: vi.fn(),
  liveDispose: vi.fn(),
  paintLiveButton: vi.fn(),
  setLiveReload: vi.fn(),
}));
vi.mock("./actions/editor.js", async (importOriginal) => ({
  ...(await importOriginal<typeof ActionsEditorModule>()),
  loadDiff: { dispatch: (args: { path: string; repo: string; ref: string }) => dispatch(args) },
}));
vi.mock("./actions/index.js", async (importOriginal) => ({
  ...(await importOriginal<typeof ActionsIndexModule>()),
  registerCleanup: vi.fn(),
}));
vi.mock("./dom.js", async (importOriginal) => ({
  ...(await importOriginal<typeof DomModule>()),
  $: new Proxy({}, { get: () => document.createElement("div") }),
  setBusy: vi.fn(),
}));

const { fetchGitDiffSources, INCOMPLETE_READ } = await import("./editor-openers.js");
const { fileStates, freshState, setActiveFilePath } = await import("./editor-types.js");
const { setLiveReload } = await import("./viewer-live.js");
const { BUS_EDITOR_VIEW_CHANGED, onBus } = await import("./bus.js");
const { sha256Hex } = await import("./sha256.js");
type LiveReload = Parameters<typeof setLiveReload>[0];
const liveReload = vi.mocked(setLiveReload).mock.calls[0]?.[0] as LiveReload;

const PATH = "/workspace/notes.txt";
const idOf = (s: string): string => `sha256:${sha256Hex(new TextEncoder().encode(s))}`;

function readOf(content: string, over: Partial<FileRead> = {}): FileRead {
  return {
    path: PATH,
    file_id: idOf(content),
    modified: "2026-10-01T00:00:00Z",
    content,
    size: new TextEncoder().encode(content).length,
    utf8: true,
    read_only: false,
    ...over,
  };
}

function diffOf(read: FileRead | null, baseLabel = "HEAD"): Outcome {
  return {
    status: "success",
    value: {
      kind: "diff",
      oldText: "base\n",
      base: "text",
      baseLabel,
      workingLabel: "working tree",
      read,
    },
  };
}

/** A file parked in a git diff against `ref`, as `open` leaves it. */
function staged(ref = "HEAD"): ReturnType<typeof freshState> {
  const state = freshState(PATH);
  state.mode.value = {
    kind: "diff",
    diffSource: {
      oldText: "",
      newText: "",
      oldLabel: ref,
      newLabel: "working tree",
      kind: "git",
      ref,
      base: "text",
      pending: true,
    },
  };
  fileStates.set(PATH, state);
  return state;
}

function newContentOf(state: ReturnType<typeof freshState>): string {
  const m = state.mode.value;
  return m.kind === "diff" && m.diffSource.kind !== "buffer"
    ? m.diffSource.newText
    : "<not a diff>";
}

function announced(): string[] {
  const seen: string[] = [];
  onTestFinished(onBus(BUS_EDITOR_VIEW_CHANGED, ({ path }) => seen.push(path)));
  return seen;
}

beforeEach(() => {
  fileStates.clear();
  setActiveFilePath(PATH);
  dispatch.mockClear();
  apiGetTypedOrError.mockClear();
});

describe("the working side of a git diff", () => {
  it("is the read the buffer and its identity are taken from", async () => {
    const state = staged();
    outcome = diffOf(readOf("work\n"));
    await fetchGitDiffSources(state, "", "HEAD");
    expect(state.current.value).toBe("work\n");
    expect(state.fileId).toBe(idOf("work\n"));
    expect(newContentOf(state)).toBe("work\n");
  });

  it("is refused on a first load when the read is not the file it identifies", async () => {
    const state = staged();
    outcome = diffOf(readOf("work\n", { size: 99 }));
    await fetchGitDiffSources(state, "", "HEAD");
    expect(state.error.value).toBe(INCOMPLETE_READ);
    expect(state.current.value).toBe("");
    expect(newContentOf(state)).toBe("");
  });

  it("never replaces a dirty buffer, which keeps its own identity", async () => {
    const state = staged();
    state.original.value = "saved\n";
    state.current.value = "saved and typed\n";
    state.fileId = idOf("saved\n");
    state.loaded = true;
    outcome = diffOf(readOf("work\n"));
    await fetchGitDiffSources(state, "", "HEAD");
    expect(state.current.value).toBe("saved and typed\n");
    expect(state.fileId).toBe(idOf("saved\n"));
  });
});

describe("a settled git diff", () => {
  it("is announced when it paints", async () => {
    const seen = announced();
    outcome = diffOf(readOf("work\n"));
    await fetchGitDiffSources(staged(), "", "HEAD");
    expect(seen).toEqual([PATH]);
  });

  it("is announced when it fails", async () => {
    const seen = announced();
    outcome = { status: "error", error: { message: "git exploded" } };
    const state = staged();
    await fetchGitDiffSources(state, "", "HEAD");
    expect(state.error.value).toBe("Failed to load diff: git exploded");
    expect(seen).toEqual([PATH]);
  });
});

describe("a live refresh of a git diff", () => {
  const liveStat = (content: string): FileStat => ({
    path: PATH,
    file_id: idOf(content),
    modified: "2026-10-01T00:00:01Z",
    size: content.length,
    large: false,
    binary: false,
    utf8: true,
    read_only: false,
  });

  it("asks git for the ref the diff was opened against, not the caption it found", async () => {
    const state = staged("HEAD");
    outcome = diffOf(readOf("v1\n"), "not in HEAD");
    await fetchGitDiffSources(state, "", "HEAD");
    outcome = diffOf(readOf("v2\n"), "not in HEAD");
    await liveReload(state, liveStat("v2\n"), () => true);
    expect(dispatch.mock.calls.at(-1)?.[0].ref).toBe("HEAD");
  });

  it("adopts one read for the buffer and the diff, reading the file no second time", async () => {
    const state = staged();
    outcome = diffOf(readOf("v1\n"));
    await fetchGitDiffSources(state, "", "HEAD");
    outcome = diffOf(readOf("v2\n"));
    await liveReload(state, liveStat("v2\n"), () => true);
    expect(apiGetTypedOrError).not.toHaveBeenCalled();
    expect(state.current.value).toBe("v2\n");
    expect(state.fileId).toBe(idOf("v2\n"));
    expect(newContentOf(state)).toBe("v2\n");
  });

  it("keeps the view when the re-read is not the file it identifies", async () => {
    const state = staged();
    outcome = diffOf(readOf("v1\n"));
    await fetchGitDiffSources(state, "", "HEAD");
    outcome = diffOf(readOf("v2\n", { file_id: idOf("other") }));
    await liveReload(state, liveStat("v2\n"), () => true);
    expect(state.error.value).toBe("");
    expect(state.current.value).toBe("v1\n");
    expect(newContentOf(state)).toBe("v1\n");
  });
});
