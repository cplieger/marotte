// ---------------------------------------------------------------------------
// Opening a file: one activation per open, stat before the read, the view the rules pick, and
// the read verified before it becomes an editable baseline.
// ---------------------------------------------------------------------------
import { describe, it, expect, beforeEach, onTestFinished, vi } from "vitest";
import { effect } from "@cplieger/reactive";
import type { FileState } from "./editor-types.js";
import type { FileStat } from "./wire/types.gen.js";

/** The tab store reduced to the one fact `open` reads and the one rule activateTab enforces:
 *  the tab's activation hook fires only when the ACTIVE tab changes. Ids are minted per path and
 *  handed back through `tabIdFor`, the only lookup production has. */
let activeTab = "";
const minted = new Map<string, string>();

const openEditorView = vi.fn((path: string) => {
  let id = minted.get(path);
  if (id === undefined) {
    id = `tb_${String(minted.size + 1).padStart(3, "0")}`;
    minted.set(path, id);
  }
  if (activeTab === id) {
    return Promise.resolve();
  }
  activeTab = id;
  editorShow(path);
  return Promise.resolve();
});

let editorShow: (path: string) => void = () => {
  /* replaced below */
};

interface Answer {
  ok: boolean;
  status: number;
  data: unknown;
  error: string;
  body?: unknown;
}

/** Staged answers per route: the stat, then the whole read. */
let statAnswer: Answer;
let readAnswer: Answer;
const apiGetTypedOrError = vi.fn(
  (url: string, _decoder: unknown, _signal?: AbortSignal): Promise<Answer> =>
    Promise.resolve(url.startsWith("/api/file/stat") ? statAnswer : readAnswer),
);

vi.mock("./tabs.js", () => ({
  openEditorView: (path: string) => openEditorView(path),
  getActiveTabId: () => activeTab,
  tabIdFor: (_kind: string, ref = "") => minted.get(ref) ?? "",
  setTabDirty: vi.fn(),
}));
vi.mock("./api-client.js", () => ({
  apiGetTypedOrError: (url: string, decoder: unknown, signal?: AbortSignal) =>
    apiGetTypedOrError(url, decoder, signal),
  apiGet: vi.fn(),
  apiGetTyped: vi.fn(),
  apiGetOrError: vi.fn(),
}));
vi.mock("./router.js", () => ({ pushRoute: vi.fn() }));
vi.mock("./editor-conflict.js", () => ({
  abortSuggestion: vi.fn(),
  clearSuggestionState: vi.fn(),
}));
vi.mock("./editor-modes.js", () => ({ restoreUI: vi.fn() }));
vi.mock("./editor-ui.js", () => ({
  applyPendingLine: vi.fn(),
  fetchAgentLines: vi.fn(),
  clearAgentLineCache: vi.fn(),
  requestedBy: (state: { mode: { value: { kind: string; editing?: boolean } } }) =>
    state.mode.value.kind === "diff" ? "diff" : state.mode.value.editing === true ? "edit" : "read",
  showSurface: vi.fn(),
}));
vi.mock("./editor-scroll.js", () => ({ captureSelection: vi.fn(), restoreEditorView: vi.fn() }));
vi.mock("./editor-pane.js", () => ({
  viewer: () => ({ atBottom: () => false, pinBottom: vi.fn() }),
  paneBody: () => ({ scrollTo: vi.fn() }),
}));
vi.mock("./viewer-live.js", () => ({
  liveActivate: vi.fn(),
  liveDispose: vi.fn(),
  paintLiveButton: vi.fn(),
  setLiveReload: vi.fn(),
}));
vi.mock("./actions/editor.js", () => ({
  loadDiff: { dispatch: () => ({ outcome: Promise.resolve({ status: "cancelled" }) }) },
}));
vi.mock("./actions/index.js", () => ({ registerCleanup: vi.fn() }));

vi.mock("./dom.js", () => {
  const make = (tag: string): HTMLElement => document.createElement(tag);
  return {
    $: {
      editorFilename: make("span"),
      editorError: make("div"),
      editorContent: make("textarea"),
      editorEditBtn: make("button"),
      editorNotice: make("div"),
      editorViewer: make("div"),
    },
    setBusy: (el: Element, busy: boolean) => {
      if (busy) {
        el.setAttribute("aria-busy", "true");
      } else {
        el.removeAttribute("aria-busy");
      }
    },
  };
});

const { openFile, openFileDiff, openFileGitDiff, activateFile, INCOMPLETE_READ } =
  await import("./editor-openers.js");
const { fileStates, setActiveFilePath, getActiveFilePath } = await import("./editor-types.js");
const { restoreUI } = await import("./editor-modes.js");
const { setLiveReload } = await import("./viewer-live.js");
const { BUS_EDITOR_VIEW_CHANGED, onBus } = await import("./bus.js");
type LiveReload = Parameters<typeof setLiveReload>[0];
// The openers hand live refresh its re-read once, at load; beforeEach clears the record.
const liveReload = vi.mocked(setLiveReload).mock.calls[0]?.[0] as LiveReload;

editorShow = activateFile;

const { sha256Hex } = await import("./sha256.js");
// sha256.node.test.ts pins sha256Hex against node:crypto, which Browser Mode does not have.
const idOf = (s: string): string => `sha256:${sha256Hex(new TextEncoder().encode(s))}`;

function stat(over: Record<string, unknown> = {}): Answer {
  return {
    ok: true,
    status: 200,
    data: {
      path: "/w/a",
      file_id: idOf("hello"),
      modified: "2026-10-01T00:00:00Z",
      size: 5,
      large: false,
      binary: false,
      utf8: true,
      read_only: false,
      ...over,
    },
    error: "",
  };
}

function read(content: string, over: Record<string, unknown> = {}): Answer {
  return {
    ok: true,
    status: 200,
    data: {
      path: "/w/a",
      file_id: idOf(content),
      modified: "2026-10-01T00:00:00Z",
      content,
      size: new TextEncoder().encode(content).length,
      utf8: true,
      read_only: false,
      ...over,
    },
    error: "",
  };
}

/** A failed answer as apiGetTypedOrError hands it over: the body undecoded. */
const refused = (status: number, error: string, body: unknown = { error }): Answer => ({
  ok: false,
  status,
  data: null,
  error,
  body,
});

const TOO_LARGE = "File is too large to display. Download it to view.";
const tooLarge = (): Answer =>
  refused(413, TOO_LARGE, { error: TOO_LARGE, code: "too_large", size: 3 << 20 });
const binaryRefusal = (): Answer =>
  refused(415, "binary file", { error: "binary file", code: "binary" });

function reads(): number {
  return apiGetTypedOrError.mock.calls.filter((c) => c[0].startsWith("/api/file?")).length;
}

async function settle(): Promise<void> {
  for (let i = 0; i < 6; i++) {
    await Promise.resolve();
  }
}

beforeEach(() => {
  vi.clearAllMocks();
  fileStates.clear();
  setActiveFilePath("");
  activeTab = "";
  minted.clear();
  statAnswer = stat();
  readAnswer = read("hello");
});

describe("opening a file activates it once", () => {
  it("issues ONE read on a first open, after the stat", async () => {
    openFile("/workspace/a.go");
    await settle();
    expect(apiGetTypedOrError.mock.calls.map((c) => c[0].split("?")[0])).toEqual([
      "/api/file/stat",
      "/api/file",
    ]);
  });

  it("still activates when the tab was already active and the hook is skipped", async () => {
    minted.set("/workspace/a.go", "tb_001");
    activeTab = "tb_001";
    openFile("/workspace/a.go");
    await settle();
    expect(reads()).toBe(1);
  });

  it("does not abort the read it just issued", async () => {
    openFile("/workspace/a.go");
    await settle();
    expect(apiGetTypedOrError.mock.calls.at(-1)?.[2]?.aborted).toBe(false);
  });
});

describe("the view the server's facts pick", () => {
  it("shows a file over the cap as the too-large view and reads nothing", async () => {
    statAnswer = stat({ large: true, file_id: undefined, size: 3 << 20 });
    openFile("/workspace/big.log");
    await settle();
    expect(fileStates.get("/workspace/big.log")?.mode.value).toEqual({ kind: "large" });
    expect(reads()).toBe(0);
  });

  it("shows an image over the cap as the too-large view too", async () => {
    statAnswer = stat({ large: true, file_id: undefined, size: 5 << 20 });
    openFile("/workspace/photo.jpg");
    await settle();
    expect(fileStates.get("/workspace/photo.jpg")?.mode.value).toEqual({ kind: "large" });
  });

  it("shows a binary file as the binary view with its facts, and reads nothing", async () => {
    statAnswer = stat({ binary: true, utf8: false, size: 1234 });
    openFile("/workspace/blob.dat");
    await settle();
    const state = fileStates.get("/workspace/blob.dat");
    expect(state?.mode.value).toEqual({ kind: "binary" });
    expect(state?.notice).toEqual({ size: 1234, modified: "2026-10-01T00:00:00Z" });
    expect(reads()).toBe(0);
  });

  it("keys an image by the identity stat reported", async () => {
    statAnswer = stat({ file_id: idOf("png bytes") });
    openFile("/workspace/shot.png");
    await settle();
    const state = fileStates.get("/workspace/shot.png");
    expect(state?.mode.value).toEqual({ kind: "image" });
    expect(state?.fileId).toBe(idOf("png bytes"));
  });

  it("renders markdown to read", async () => {
    readAnswer = read("# Title\n");
    openFile("/workspace/README.md");
    await settle();
    expect(fileStates.get("/workspace/README.md")?.mode.value).toEqual({ kind: "markdown" });
  });
});

describe("adopting the read", () => {
  it("holds LF-normalized text with the file's own endings recorded", async () => {
    readAnswer = read("a\r\nb\r\n");
    openFile("/workspace/w.bat");
    await settle();
    const state = fileStates.get("/workspace/w.bat");
    expect(state?.current.value).toBe("a\nb\n");
    expect(state?.dirty.value).toBe(false);
    expect(state?.eol?.saved).toEqual({ uniform: "crlf", tags: null });
    expect(state?.fileId).toBe(idOf("a\r\nb\r\n"));
  });

  // A read that is not the whole identified file never becomes a baseline.
  it.each([
    ["a size that disagrees", { size: 99 }],
    ["an identity that disagrees", { file_id: idOf("something else") }],
  ])("refuses a read with %s", async (_name, over) => {
    readAnswer = read("hello", over);
    openFile("/workspace/a.go");
    await settle();
    const state = fileStates.get("/workspace/a.go");
    expect(state?.error.value).toBe(INCOMPLETE_READ);
    expect(state?.current.value).toBe("");
  });

  it("adopts a non-UTF-8 read for display only", async () => {
    readAnswer = read("caf\ufffd", { utf8: false, size: 4, file_id: idOf("caf\xe9") });
    openFile("/workspace/latin1.txt");
    await settle();
    const state = fileStates.get("/workspace/latin1.txt");
    expect(state?.error.value).toBe("");
    expect(state?.facts.value).toMatchObject({ kind: "small", utf8: false });
  });
});

// A refusal is shown in the server's words; only a network failure is generic.
describe("a failed load", () => {
  it.each([
    [403, "access denied: protected path"],
    [404, "not found"],
    [500, "read failed"],
  ])("shows the server's sentence for a %i", async (status, sentence) => {
    readAnswer = refused(status, sentence);
    openFile("/workspace/a.go");
    await settle();
    expect(fileStates.get("/workspace/a.go")?.error.value).toBe(sentence);
  });

  it("shows the refusal's sentence when the read stays binary after the second stat", async () => {
    readAnswer = binaryRefusal();
    openFile("/workspace/a.go");
    await settle();
    expect(fileStates.get("/workspace/a.go")?.error.value).toBe("binary file");
    expect(reads()).toBe(2);
  });

  // A refusal names a view only once its body is the shape its status promises.
  it.each([
    ["a 413 with no code", refused(413, TOO_LARGE, { error: TOO_LARGE, size: 3 << 20 })],
    ["a 413 with no size", refused(413, TOO_LARGE, { error: TOO_LARGE, code: "too_large" })],
    ["a 413 naming binary", refused(413, TOO_LARGE, { error: TOO_LARGE, code: "binary", size: 9 })],
    ["a 413 with no body", refused(413, TOO_LARGE, undefined)],
    ["a 415 naming the cap", refused(415, "binary file", { error: "x", code: "too_large" })],
    ["a 415 with a non-string error", refused(415, "", { error: 7, code: "binary" })],
  ])("treats %s as a failed load, not a view", async (_name, answer) => {
    readAnswer = answer;
    openFile("/workspace/a.go");
    await settle();
    const state = fileStates.get("/workspace/a.go");
    expect(state?.error.value).toBe("Failed to load file");
    expect(state?.mode.value).toEqual({ kind: "text", editing: false });
    expect(reads()).toBe(1);
  });

  it("re-stats once after a 415 on the read, and shows the binary view the new facts pick", async () => {
    apiGetTypedOrError
      .mockImplementationOnce(() => Promise.resolve(stat()))
      .mockImplementationOnce(() => Promise.resolve(binaryRefusal()))
      .mockImplementationOnce(() => Promise.resolve(stat({ binary: true, utf8: false })));
    openFile("/workspace/turned.bin");
    await settle();
    const state = fileStates.get("/workspace/turned.bin");
    expect(state?.mode.value).toEqual({ kind: "binary" });
    expect(state?.error.value).toBe("");
  });

  // A 413 from the whole read is the server's verdict that the file is over the cap, however
  // many races preceded it.
  it.each([
    ["the first read", [stat(), tooLarge()]],
    ["a read after a binary race", [stat(), binaryRefusal(), stat(), tooLarge()]],
  ])("shows the too-large view, unpinned, for a 413 on %s", async (_name, answers) => {
    for (const a of answers) {
      apiGetTypedOrError.mockImplementationOnce(() => Promise.resolve(a));
    }
    openFile("/workspace/grew.log");
    await settle();
    const state = fileStates.get("/workspace/grew.log");
    expect(state?.mode.value).toEqual({ kind: "large" });
    expect(state?.facts.value).toEqual({ kind: "large", size: 3 << 20 });
    expect(state?.error.value).toBe("");
    expect(state?.fileId).toBe("");
  });

  // A view picked from a stat is pinned to the stat's identity; one with none cannot be shown.
  it.each([
    ["an image with no identity", "/workspace/shot.png", { file_id: undefined }],
    ["a binary file with no identity", "/workspace/blob.dat", { binary: true, file_id: undefined }],
    ["an image with a malformed identity", "/workspace/shot.png", { file_id: "sha256:XYZ" }],
  ])("refuses %s", async (_name, path, over) => {
    statAnswer = stat(over);
    openFile(path);
    await settle();
    const state = fileStates.get(path);
    expect(state?.error.value).toBe(INCOMPLETE_READ);
    expect(reads()).toBe(0);
  });

  it("refuses a non-UTF-8 read with a malformed identity", async () => {
    readAnswer = read("caf\ufffd", { utf8: false, size: 4, file_id: "sha256:" });
    openFile("/workspace/latin1.txt");
    await settle();
    expect(fileStates.get("/workspace/latin1.txt")?.error.value).toBe(INCOMPLETE_READ);
  });

  it("shows the stat's refusal the same way", async () => {
    statAnswer = refused(403, "access denied: outside granted roots");
    openFile("/workspace/a.go");
    await settle();
    expect(fileStates.get("/workspace/a.go")?.error.value).toBe(
      "access denied: outside granted roots",
    );
  });

  it("says 'Failed to load file' only for a network failure", async () => {
    readAnswer = refused(0, "TypeError: Failed to fetch");
    openFile("/workspace/a.go");
    await settle();
    expect(fileStates.get("/workspace/a.go")?.error.value).toBe("Failed to load file");
  });
});

// A card's `+N -M` opens a diff whose two sides are already in hand, so the pane owes the read
// nothing: a failed read must not blank it.
describe("a diff that carries its own pair", () => {
  const OLD = "one\ntwo";
  const NEW = "one\nTWO";

  it("paints before the read instead of behind it", () => {
    openFileDiff("/workspace/a.go", OLD, NEW);
    expect(vi.mocked(restoreUI)).toHaveBeenCalledTimes(1);
  });

  it("keeps the diff when the read fails, and withholds the buffer", async () => {
    readAnswer = refused(404, "not found");
    openFileDiff("/workspace/gone.go", OLD, NEW);
    await settle();
    const state = fileStates.get("/workspace/gone.go");
    expect(state?.error.value).toBe("");
    expect(state?.loaded).toBe(false);
  });

  it("fills the buffer from the FILE, never from the diff's own after-side", async () => {
    openFileDiff("/workspace/a.go", OLD, NEW);
    await settle();
    const state = fileStates.get("/workspace/a.go");
    expect(state?.loaded).toBe(true);
    expect(state?.current.value).toBe("hello");
    expect(state?.mode.value.kind).toBe("diff");
  });

  it("leaves a git diff on its own fetch", () => {
    openFileGitDiff("/workspace/a.go", "HEAD");
    expect(apiGetTypedOrError).not.toHaveBeenCalled();
  });
});

describe("a live refresh", () => {
  async function openedAndWatched(): Promise<{ state: FileState; seen: string[] }> {
    openFile("/workspace/a.go");
    await settle();
    const seen: string[] = [];
    const off = onBus(BUS_EDITOR_VIEW_CHANGED, ({ path }) => seen.push(path));
    onTestFinished(off);
    return { state: fileStates.get("/workspace/a.go") as FileState, seen };
  }

  it("announces the bytes it adopted", async () => {
    const { state, seen } = await openedAndWatched();
    readAnswer = read("hello again");
    const next = stat({ file_id: idOf("hello again"), size: 11 }).data as FileStat;
    await liveReload(state, next, () => true);
    expect(state.current.value).toBe("hello again");
    expect(seen).toEqual(["/workspace/a.go"]);
  });

  it("shows the too-large view when the re-read answers 413", async () => {
    const { state, seen } = await openedAndWatched();
    readAnswer = tooLarge();
    const next = stat({ file_id: idOf("hello again"), size: 11 }).data as FileStat;
    await liveReload(state, next, () => true);
    expect(state.mode.value).toEqual({ kind: "large" });
    expect(state.fileId).toBe("");
    expect(seen).toEqual(["/workspace/a.go"]);
  });

  it("keeps the view when the re-read's 413 is malformed", async () => {
    const { state, seen } = await openedAndWatched();
    readAnswer = refused(413, TOO_LARGE, { error: TOO_LARGE });
    const next = stat({ file_id: idOf("hello again"), size: 11 }).data as FileStat;
    await liveReload(state, next, () => true);
    expect(state.mode.value).toEqual({ kind: "text", editing: false });
    expect(state.current.value).toBe("hello");
    expect(seen).toEqual([]);
  });

  it("announces a file that grew past the cap", async () => {
    const { state, seen } = await openedAndWatched();
    const next = stat({ large: true, file_id: undefined, size: 3 << 20 }).data as FileStat;
    await liveReload(state, next, () => true);
    expect(state.mode.value).toEqual({ kind: "large" });
    expect(seen).toEqual(["/workspace/a.go"]);
  });
});

describe("what an activation has in place before it moves the active path", () => {
  it("has the file's own state in the map on the run the path change triggers", () => {
    const seen: boolean[] = [];
    const dispose = effect(() => {
      const path = getActiveFilePath();
      if (path !== "") {
        seen.push(fileStates.has(path));
      }
    });
    activateFile("/workspace/a.go");
    dispose();
    expect(seen).toEqual([true]);
  });
});
