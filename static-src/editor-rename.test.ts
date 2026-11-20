// Rename from the editor's toolbar or F2: it swaps the file's name for the file browser's inline
// field and sends the browser's rename, so its refusals are the browser's. A dirty buffer blocks it
// with a message, and a file the server says cannot be renamed offers no Rename.
import { describe, it, expect, beforeAll, afterAll, beforeEach, afterEach, vi } from "vitest";
import type * as ApiClientModule from "./api-client.js";
import type * as EditorActions from "./actions/editor.js";
import type * as ConfirmModule from "./confirm.js";
import type * as Git from "./git.js";
import type * as RouterModule from "./router.js";
import type * as TabsModule from "./tabs.js";
import type * as Toast from "./toast.js";

vi.mock("./api-client.js", async (importOriginal) => ({
  ...(await importOriginal<typeof ApiClientModule>()),
  ...(await import("./__test-helpers__/editor-transitions-harness.js")).mocks.apiClient(),
}));
vi.mock("./actions/editor.js", async (importOriginal) => {
  const real = await importOriginal<typeof EditorActions>();
  const { mocks } = await import("./__test-helpers__/editor-transitions-harness.js");
  return { ...real, ...mocks.editorActions(real) };
});
vi.mock("./confirm.js", async (importOriginal) => ({
  ...(await importOriginal<typeof ConfirmModule>()),
  ...(await import("./__test-helpers__/editor-transitions-harness.js")).mocks.confirm(),
}));
vi.mock("./git.js", async (importOriginal) => ({
  ...(await importOriginal<typeof Git>()),
  ...(await import("./__test-helpers__/editor-transitions-harness.js")).mocks.git(),
}));
vi.mock("./tabs.js", async (importOriginal) => ({
  ...(await importOriginal<typeof TabsModule>()),
  ...(await import("./__test-helpers__/editor-transitions-harness.js")).mocks.tabs(),
}));
vi.mock("./router.js", async (importOriginal) => ({
  ...(await importOriginal<typeof RouterModule>()),
  ...(await import("./__test-helpers__/editor-transitions-harness.js")).mocks.router(),
}));
vi.mock("./toast.js", async (importOriginal) => ({
  ...(await importOriginal<typeof Toast>()),
  ...(await import("./__test-helpers__/toast-mock.js")).toastMock(),
}));

import {
  byId,
  drain,
  heldRequest,
  hold,
  releaseAll,
  resetHarness,
  server,
  shown,
  stage,
  statURL,
  tabs,
} from "./__test-helpers__/editor-transitions-harness.js";
import { opened, typeInto } from "./__test-helpers__/editor-transitions-driver.js";
import { mountEditorView } from "./__test-helpers__/editor-dom.js";
import { resetActionFramework } from "./actions/__test-helpers__/action-test-setup.js";
import { initEditor } from "./editor-core.js";
import { iconEl } from "./icon-el.js";
import { ICON_RENAME_UI } from "./icons.js";
import { activateFile, closeEditorFile, moveEditorFile } from "./editor-openers.js";
import { fileStates } from "./editor-types.js";
import { BUS_FILES_REVEAL, onBus } from "./bus.js";
import { _resetLiveForTest } from "./viewer-live.js";
import { setWorkspaceRoot } from "./workspace.js";
import * as toast from "./toast.js";

const PATH = "/workspace/.kiro/prompts/Remember.md";
const BODY = "Remember this: $ARGUMENTS\n";

interface RenameCall {
  readonly body: unknown;
  readonly answer: (r: Response) => void;
}
const renames: RenameCall[] = [];

// The transition harness's own install resets the action framework before every case, which
// orphans the pending signal Rename's loading state is bound to at init; this suite resets it once.
let host: HTMLElement;
beforeAll(() => {
  resetActionFramework();
  host = mountEditorView();
  initEditor();
});
afterAll(() => {
  host.remove();
});

beforeEach(() => {
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout", "Date"] });
  _resetLiveForTest();
  setWorkspaceRoot("/workspace");
  resetHarness();
  renames.length = 0;
  vi.mocked(toast.error).mockClear();
  vi.stubGlobal("fetch", (url: unknown, init?: RequestInit): Promise<Response> => {
    if (url === "/api/files/action") {
      return new Promise<Response>((resolve) => {
        renames.push({ body: JSON.parse(String(init?.body)), answer: resolve });
      });
    }
    return Promise.resolve(new Response("", { status: 404 }));
  });
});

afterEach(async () => {
  // An unanswered rename would hold the next case's behind it in the action's scope.
  for (const r of renames.splice(0)) {
    r.answer(new Response("{}", { status: 200 }));
  }
  await drain();
  for (const path of [...fileStates.keys()]) {
    closeEditorFile(path);
  }
  vi.unstubAllGlobals();
  vi.useRealTimers();
});

const renameBtn = (): HTMLButtonElement => byId<HTMLButtonElement>("editor-rename-btn");
const header = (): HTMLElement => byId("editor-filename");
const field = (): HTMLInputElement | null =>
  header().querySelector<HTMLInputElement>("input.editor-rename-input");

function pressF2(target: EventTarget = document.body, init: KeyboardEventInit = {}): KeyboardEvent {
  const e = new KeyboardEvent("keydown", { key: "F2", bubbles: true, cancelable: true, ...init });
  target.dispatchEvent(e);
  return e;
}

async function openPrompt(renamable = true): Promise<void> {
  stage(PATH, BODY, renamable);
  await opened(PATH);
}

async function typeName(name: string): Promise<void> {
  const f = field();
  if (f === null) {
    throw new Error("no rename field");
  }
  f.value = name;
  f.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true, cancelable: true }));
  await drain();
}

describe("the editor's Rename", () => {
  it("draws Rename with the registry's Rename icon, a different glyph from Edit's pencil", () => {
    const glyph = (id: string): string => byId(id).querySelector("svg")?.innerHTML ?? "";
    expect(glyph("editor-rename-btn")).toBe(iconEl(ICON_RENAME_UI).innerHTML);
    expect(glyph("editor-rename-btn")).not.toBe(glyph("editor-edit-btn"));
  });

  it("shows Rename (F2) on a renamable file and hides it when the server says it cannot be renamed", async () => {
    await openPrompt();
    expect(shown("editor-rename-btn")).toBe(true);
    expect(renameBtn().getAttribute("aria-label")).toBe("Rename (F2)");
    expect(renameBtn().dataset["tooltip"]).toBe("Rename (F2)");
    expect(renameBtn().querySelector("svg")).not.toBeNull();

    closeEditorFile(PATH);
    await openPrompt(false);
    expect(shown("editor-rename-btn")).toBe(false);
    pressF2();
    expect(field()).toBeNull();
  });

  it("swaps the file name for a field with the stem selected, keeping the folder before it", async () => {
    await openPrompt();
    renameBtn().click();
    const f = field();
    expect(header().classList.contains("is-renaming")).toBe(true);
    expect(header().querySelector(".editor-rename-dir")?.textContent).toBe(".kiro/prompts/");
    expect(f?.value).toBe("Remember.md");
    expect(document.activeElement).toBe(f);
    expect([f?.selectionStart, f?.selectionEnd]).toEqual([0, "Remember".length]);
  });

  it("F2 starts the rename on an editor tab, from the page or the textarea", async () => {
    await openPrompt();
    const e = pressF2();
    expect(e.defaultPrevented).toBe(true);
    expect(field()?.value).toBe("Remember.md");
  });

  it("F2 does nothing while the field is open, on another tab kind, with a modifier, or after another handler took it", async () => {
    await openPrompt();
    renameBtn().click();
    const open = field();
    pressF2();
    expect(field()).toBe(open);
    open?.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true }));
    expect(field()).toBeNull();

    tabs.kind = "files";
    pressF2();
    expect(field()).toBeNull();
    tabs.kind = "editor";

    pressF2(document.body, { shiftKey: true });
    pressF2(document.body, { ctrlKey: true });
    expect(field()).toBeNull();

    const take = (e: Event): void => {
      e.preventDefault();
    };
    document.body.addEventListener("keydown", take);
    pressF2();
    document.body.removeEventListener("keydown", take);
    expect(field()).toBeNull();
    expect(renames).toHaveLength(0);
  });

  it("Enter sends the file browser's rename for the shown file, showing the new name while it is pending", async () => {
    await openPrompt();
    renameBtn().click();
    await typeName("Recall.md");
    expect(renames.map((r) => r.body)).toEqual([
      { action: "rename", path: PATH, name: "Recall.md" },
    ]);
    expect(field()).toBeNull();
    expect(header().textContent).toBe(".kiro/prompts/Recall.md");
    expect(renameBtn().disabled).toBe(true);

    renames[0]?.answer(new Response(JSON.stringify({ ok: true }), { status: 200 }));
    await drain();
    expect(renameBtn().disabled).toBe(false);
    expect(header().classList.contains("is-renaming")).toBe(false);
    expect(toast.error).not.toHaveBeenCalled();
  });

  it("a refused rename restores the old name, keeps the file open at its path and says why", async () => {
    await openPrompt();
    renameBtn().click();
    await typeName("Recall.md");
    renames[0]?.answer(
      new Response(JSON.stringify({ error: "Recall.md already exists", code: "name_taken" }), {
        status: 409,
      }),
    );
    await drain();
    expect(header().textContent).toBe(".kiro/prompts/Remember.md");
    expect(header().querySelector("bdi")).not.toBeNull();
    expect(header().classList.contains("is-renaming")).toBe(false);
    expect(fileStates.has(PATH)).toBe(true);
    expect(renameBtn().disabled).toBe(false);
    expect(toast.error).toHaveBeenCalledWith(
      'Could not rename "Remember.md": Recall.md already exists',
      undefined,
    );
  });

  it("a dirty buffer blocks the rename with a message, from the button and from F2", async () => {
    await openPrompt();
    byId("editor-edit-btn").click();
    typeInto("changed\n");
    renameBtn().click();
    pressF2(byId("editor-content"));
    expect(field()).toBeNull();
    expect(header().textContent).toBe(".kiro/prompts/Remember.md");
    expect(renames).toHaveLength(0);
    expect(vi.mocked(toast.error).mock.calls).toEqual([
      ["Save or discard your changes to Remember.md before renaming it."],
      ["Save or discard your changes to Remember.md before renaming it."],
    ]);
  });

  it("Show, Download and Edit do nothing while the rename is open or in flight", async () => {
    const asked: string[] = [];
    const off = onBus(BUS_FILES_REVEAL, ({ path }) => {
      asked.push(path);
    });
    const download = vi
      .spyOn(HTMLAnchorElement.prototype, "click")
      .mockImplementation(() => undefined);
    await openPrompt();
    renameBtn().click();
    const f = field();
    if (f === null) {
      throw new Error("no rename field");
    }
    f.value = "Recall.md";
    f.blur();
    byId("editor-reveal-btn").click();
    byId("editor-download-btn").click();
    byId("editor-edit-btn").click();
    await drain();
    expect(renames.map((r) => r.body)).toEqual([
      { action: "rename", path: PATH, name: "Recall.md" },
    ]);
    expect(asked).toEqual([]);
    expect(download).not.toHaveBeenCalled();
    expect(fileStates.get(PATH)?.mode.value).toEqual({ kind: "markdown" });

    renames[0]?.answer(new Response(JSON.stringify({ ok: true }), { status: 200 }));
    await drain();
    byId("editor-reveal-btn").click();
    off();
    download.mockRestore();
    expect(asked).toEqual([PATH]);
  });

  it("Escape, or an unchanged name, restores and sends nothing", async () => {
    await openPrompt();
    renameBtn().click();
    field()?.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true }));
    expect(field()).toBeNull();
    expect(header().textContent).toBe(".kiro/prompts/Remember.md");
    expect(header().classList.contains("is-renaming")).toBe(false);

    renameBtn().click();
    await typeName("Remember.md");
    expect(field()).toBeNull();
    expect(header().textContent).toBe(".kiro/prompts/Remember.md");
    expect(renames).toHaveLength(0);
  });
});

// The server retargets the tab (Membership.FollowRename) and the tab strip hands the old spec the new ref:
// `onRetarget` moves the record (moveEditorFile), then the active row's `onShow` shows the new path.
describe("after the rename, the tab follows the file", () => {
  const RENAMED = "/workspace/.kiro/prompts/Recall.md";

  function retarget(): void {
    const id = tabs.minted.get(PATH) ?? "";
    tabs.minted.delete(PATH);
    tabs.minted.set(RENAMED, id);
    stage(RENAMED, BODY);
    moveEditorFile(PATH, RENAMED);
    activateFile(RENAMED);
  }

  it("moves the record with its buffer and view, and the toolbar shows the new path", async () => {
    await openPrompt();
    byId("editor-edit-btn").click();
    typeInto("changed\n");
    const before = fileStates.get(PATH);
    retarget();
    await drain();
    expect(fileStates.has(PATH)).toBe(false);
    const after = fileStates.get(RENAMED);
    expect(after, "the same record, not a reload").toBe(before);
    expect(after?.current.value).toBe("changed\n");
    expect(after?.dirty.value).toBe(true);
    expect(after?.mode.value).toEqual({ kind: "text", editing: true });
    expect(header().textContent).toBe(".kiro/prompts/Recall.md");
    expect(byId<HTMLTextAreaElement>("editor-content").value).toBe("changed\n");
  });

  it("the live check follows the new path and drops an answer issued for the old one", async () => {
    await openPrompt();
    stage(PATH, "bytes the old path answers with\n");
    hold(statURL(PATH));
    await vi.advanceTimersByTimeAsync(5000);
    await heldRequest(statURL(PATH));
    retarget();
    await releaseAll();
    expect(fileStates.get(RENAMED)?.current.value).toBe(BODY);
    server.hits.clear();
    await vi.advanceTimersByTimeAsync(5000);
    await drain();
    expect(server.hits.get(statURL(RENAMED)) ?? 0).toBeGreaterThan(0);
    expect(server.hits.get(statURL(PATH)) ?? 0).toBe(0);
  });
});
