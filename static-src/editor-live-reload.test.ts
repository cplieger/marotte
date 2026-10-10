// A live refresh re-reads the file and repaints the shared pane. Over the shipped stylesheet in a
// real browser: the reader's line survives the repaint, including at the physical end of a file
// taller than the browser's height cap, where the end of the scroll range is not the end of the
// file.
import { describe, it, expect, beforeAll, afterAll, afterEach, vi } from "vitest";
import type * as Live from "./viewer-live.js";
import type * as Api from "./api-client.js";
import type { FileRead, FileStat } from "./wire/types.gen.js";

const hoisted = vi.hoisted(() => ({
  reload: null as Live.LiveReload | null,
  read: null as FileRead | null,
}));

vi.mock("./viewer-live.js", async (importOriginal) => {
  const real = await importOriginal<typeof Live>();
  return {
    ...real,
    liveActivate: () => undefined,
    setLiveReload: (fn: Live.LiveReload) => {
      hoisted.reload = fn;
      real.setLiveReload(fn);
    },
  };
});

vi.mock("./api-client.js", async (importOriginal) => ({
  ...(await importOriginal<typeof Api>()),
  apiGetTypedOrError: () =>
    Promise.resolve({ ok: true, status: 200, data: hoisted.read, error: "" }),
}));

const { mountAppCSS } = await import("./__test-helpers__/css-rules.js");
const { mountEditorView } = await import("./__test-helpers__/editor-dom.js");
const { initEditor } = await import("./editor-core.js");
const { activateFile, adoptDiskBytes, closeEditorFile } = await import("./editor-openers.js");
const { fileStates, freshState, setActiveFilePath } = await import("./editor-types.js");
const { viewer } = await import("./editor-pane.js");
const { sha256Hex } = await import("./sha256.js");

/** The logical line of the read surface's top row. */
function topLine(): number {
  return viewer().rows?.lines[viewer().topRow()] ?? 1;
}

let sheet: HTMLStyleElement;
let sizing: HTMLStyleElement;
let host: HTMLElement;

beforeAll(() => {
  sheet = mountAppCSS();
  sizing = document.createElement("style");
  sizing.textContent = `[id="editor-view"] { width: 480px; height: 240px; display: flex; flex-direction: column; }
    [id="editor-view"] > .editor-page { flex: 1; min-height: 0; }`;
  document.head.appendChild(sizing);
  host = mountEditorView();
  initEditor();
});

afterAll(() => {
  sheet.remove();
  sizing.remove();
  host.remove();
});

const PATH = "/workspace/big.log";

afterEach(() => {
  closeEditorFile(PATH);
  setActiveFilePath("");
});

function readOf(content: string): FileRead {
  const bytes = new TextEncoder().encode(content);
  return {
    path: PATH,
    file_id: `sha256:${sha256Hex(bytes)}`,
    modified: "2026-10-01T00:00:00Z",
    content,
    size: bytes.length,
    utf8: true,
    read_only: false,
  };
}

function open(content: string): ReturnType<typeof freshState> {
  const state = freshState(PATH);
  fileStates.set(PATH, state);
  adoptDiskBytes(state, readOf(content));
  state.loaded = true;
  activateFile(PATH);
  return state;
}

function body(): HTMLElement {
  return document.querySelector<HTMLElement>(".editor-body")!;
}

async function frames(n = 2): Promise<void> {
  for (let i = 0; i < n; i++) {
    await new Promise<void>((r) => requestAnimationFrame(() => r()));
  }
}

async function liveRefresh(state: ReturnType<typeof freshState>, content: string): Promise<void> {
  hoisted.read = readOf(content);
  const { content: _bytes, ...facts } = readOf(content);
  const stat: FileStat = { ...facts, large: false, binary: false };
  await hoisted.reload?.(state, stat, () => true);
}

describe("a live refresh", () => {
  it("keeps the line at the physical end of a file taller than the height cap", async () => {
    const text = "\n".repeat((2 << 20) - 1);
    const state = open(text);
    body().scrollTop = body().scrollHeight;
    await frames();
    const line = topLine();
    await liveRefresh(state, `${text.slice(1)}x`);
    expect(topLine()).toBe(line);
  });

  it("stays at the end of a file under the cap that grows", async () => {
    const text = Array.from({ length: 400 }, (_, i) => `line ${String(i + 1)}`).join("\n");
    const state = open(text);
    body().scrollTop = body().scrollHeight;
    await frames();
    await liveRefresh(state, `${text}\nline 401\nline 402`);
    const last = document.querySelector<HTMLElement>('.viewer-row[data-row="401"]');
    const b = body().getBoundingClientRect();
    expect(last?.getBoundingClientRect().bottom ?? Infinity).toBeLessThanOrEqual(b.bottom);
    expect(viewer().atBottom()).toBe(true);
  });
});
