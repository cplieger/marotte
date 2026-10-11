// Saving from the editor: the bytes written keep the file's own line endings and name the identity
// they were read under, an edit the line-ending record cannot attribute is refused rather than
// guessed, and a save the server refuses as stale leaves the buffer and offers the ways on until
// the reader picks one.
import { describe, it, expect, beforeAll, afterAll, beforeEach, vi } from "vitest";
import { userEvent } from "vitest/browser";
import type * as EditorActions from "./actions/editor.js";
import type * as Git from "./git.js";
import type { SaveOutcome } from "./actions/editor.js";

interface SaveArgs {
  readonly path: string;
  readonly content: string;
  readonly fileId?: string;
}
interface SaveHandlers {
  readonly onSuccess: (o: SaveOutcome) => void;
}

const { saves } = vi.hoisted(() => ({
  saves: [] as { args: SaveArgs; handlers: SaveHandlers }[],
}));

vi.mock("./actions/editor.js", async (importOriginal) => {
  const real = await importOriginal<typeof EditorActions>();
  return {
    ...real,
    saveFile: {
      ...real.saveFile,
      dispatch: (args: SaveArgs, handlers: SaveHandlers) => {
        saves.push({ args, handlers });
        return Promise.resolve();
      },
    },
  };
});
vi.mock("./git.js", async (importOriginal) => ({
  ...(await importOriginal<typeof Git>()),
  markGitDirty: vi.fn(),
}));

const { mountEditorView } = await import("./__test-helpers__/editor-dom.js");
const { initEditor } = await import("./editor-core.js");
const { activateFile } = await import("./editor-openers.js");
const { renderTextModeUI } = await import("./editor-ui.js");
const { fileStates, freshState, setActiveFilePath } = await import("./editor-types.js");
const { EolTracker, normalize } = await import("./viewer-eol.js");
const { sha256Hex } = await import("./sha256.js");
const idOf = (s: string): string => `sha256:${sha256Hex(new TextEncoder().encode(s))}`;

const PATH = "/workspace/notes.txt";
const READ_ID = `sha256:${"a".repeat(64)}`;
const DISK_ID = `sha256:${"b".repeat(64)}`;
let host: HTMLElement;

beforeAll(() => {
  host = mountEditorView();
  initEditor();
});

afterAll(() => {
  host.remove();
});

beforeEach(() => {
  saves.length = 0;
  fileStates.clear();
  setActiveFilePath("");
});

const byId = <T extends HTMLElement>(id: string): T => document.getElementById(id) as T;
const area = (): HTMLTextAreaElement => byId("editor-content");
const errorText = (): string =>
  byId("editor-error").querySelector(".editor-error-text")?.textContent ?? "";
const actionLabels = (): string[] =>
  [...byId("editor-error").querySelectorAll(".editor-error-actions button")].map(
    (b) => b.textContent ?? "",
  );

/** Open `raw` (its endings intact) as read under READ_ID, then enter the edit state. */
function openForEdit(raw: string): ReturnType<typeof freshState> {
  const { text, record } = normalize(raw);
  const state = freshState(PATH);
  state.loaded = true;
  state.original.value = text;
  state.current.value = text;
  state.eol = { saved: record, tracker: new EolTracker(text, record) };
  state.fileId = READ_ID;
  state.facts.value = {
    kind: "small",
    binary: false,
    utf8: true,
    conflict: false,
    readOnly: false,
    size: raw.length,
  };
  fileStates.set(PATH, state);
  setActiveFilePath(PATH);
  renderTextModeUI(state);
  byId("editor-edit-btn").click();
  return state;
}

async function typeAtEnd(text: string): Promise<void> {
  area().setSelectionRange(area().value.length, area().value.length);
  await userEvent.type(area(), text);
}

describe("saving from the editor", () => {
  it("writes the file's own CRLF endings under the identity it was read with", async () => {
    openForEdit("one\r\ntwo\r\n");
    await typeAtEnd("three");
    byId<HTMLButtonElement>("editor-save-btn").click();
    expect(saves).toHaveLength(1);
    expect(saves[0]?.args).toEqual({ path: PATH, content: "one\r\ntwo\r\nthree", fileId: READ_ID });
  });

  it("refuses an edit to a mixed-ending file it cannot attribute, offering no Overwrite", () => {
    openForEdit("a\r\nb\nc");
    // A write with no `beforeinput` ahead of it: the record cannot tell which ending the new
    // break should carry.
    area().value = "a\r\nb\nc\n";
    area().dispatchEvent(new Event("input", { bubbles: true }));
    byId<HTMLButtonElement>("editor-save-btn").click();
    expect(saves).toHaveLength(0);
    expect(errorText()).toBe(
      "This file mixes line endings, and the last edit could not keep each line's ending exactly, so it was not saved.",
    );
    expect(actionLabels()).toEqual(["Download my version", "Discard my changes"]);
  });

  it("keeps a refusal when the file is shown again, Save waiting on a way on", () => {
    openForEdit("a\r\nb\nc");
    area().value = "a\r\nb\nc\n";
    area().dispatchEvent(new Event("input", { bubbles: true }));
    byId<HTMLButtonElement>("editor-save-btn").click();
    activateFile(PATH);
    expect(byId<HTMLButtonElement>("editor-save-btn").disabled).toBe(true);
    expect(actionLabels()).toEqual(["Download my version", "Discard my changes"]);
  });

  it("on a stale refusal of a file now binary, keeps the buffer and offers Overwrite, which writes without an identity", async () => {
    const state = openForEdit("x\n");
    await typeAtEnd("y");
    byId<HTMLButtonElement>("editor-save-btn").click();
    saves[0]?.handlers.onSuccess({
      kind: "stale",
      refusal: { error: "changed", code: "binary", content_kind: "binary" },
    });
    expect(errorText()).toBe("This file changed on disk and is now binary.");
    expect(actionLabels()).toEqual(["Overwrite", "Download my version", "Discard my changes"]);
    expect(state.current.value).toBe("x\ny");
    byId("editor-error").querySelector<HTMLButtonElement>(".editor-error-actions button")?.click();
    expect(saves).toHaveLength(2);
    expect(saves[1]?.args).toEqual({ path: PATH, content: "x\ny" });
  });

  it("on a stale refusal with the disk text, diffs the buffer against what is now on disk", async () => {
    const state = openForEdit("x\n");
    await typeAtEnd("y");
    byId<HTMLButtonElement>("editor-save-btn").click();
    saves[0]?.handlers.onSuccess({
      kind: "stale",
      refusal: {
        error: "This file changed on disk.",
        code: "changed",
        content_kind: "text",
        content: "z\r\n",
        size: 3,
        file_id: idOf("z\r\n"),
      },
    });
    expect(state.original.value).toBe("z\n");
    expect(state.fileId).toBe(idOf("z\r\n"));
    expect(state.current.value).toBe("x\ny");
    expect(state.mode.value.kind).toBe("diff");
  });

  // The disk text becomes the next save's baseline only under the identity that names it, or a
  // retry would write over whatever is on disk without the reader choosing Overwrite.
  it.each([
    ["no identity", { size: 3 }],
    ["no size", { file_id: idOf("z\r\n") }],
    ["an identity naming other bytes", { size: 3, file_id: DISK_ID }],
  ])(
    "on a stale text refusal with %s, adopts nothing and offers the ways on",
    async (_name, over) => {
      const state = openForEdit("x\n");
      await typeAtEnd("y");
      byId<HTMLButtonElement>("editor-save-btn").click();
      saves[0]?.handlers.onSuccess({
        kind: "stale",
        refusal: {
          error: "This file changed on disk.",
          code: "changed",
          content_kind: "text",
          content: "z\r\n",
          ...over,
        },
      });
      expect(state.original.value).toBe("x\n");
      expect(state.fileId).toBe(READ_ID);
      expect(state.mode.value).toEqual({ kind: "text", editing: true });
      expect(actionLabels()).toEqual(["Overwrite", "Download my version", "Discard my changes"]);
    },
  );

  it("downloads a mixed-ending buffer typed back to the saved text with the saved endings", async () => {
    const blobs: Blob[] = [];
    vi.spyOn(URL, "createObjectURL").mockImplementation((b) => {
      blobs.push(b as Blob);
      return "blob:fv-test";
    });
    vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(() => undefined);
    const state = openForEdit("a\r\nb\nc");
    const write = (v: string): void => {
      area().value = v;
      area().dispatchEvent(new Event("input", { bubbles: true }));
    };
    write("a\nb\nc\n");
    byId<HTMLButtonElement>("editor-save-btn").click();
    // Back to the saved text by edits that lose the CRLF and re-insert its break by guess.
    write("ab\nc\n");
    write("ab\nc");
    write("a\nb\nc");
    expect(state.current.value).toBe(state.original.value);
    [...byId("editor-error").querySelectorAll<HTMLButtonElement>(".editor-error-actions button")]
      .find((b) => b.textContent === "Download my version")
      ?.click();
    expect(blobs).toHaveLength(1);
    expect(await blobs[0]?.text()).toBe("a\r\nb\nc");
  });

  it("adopts the identity the server reports after a save", async () => {
    const state = openForEdit("x\n");
    await typeAtEnd("y");
    byId<HTMLButtonElement>("editor-save-btn").click();
    saves[0]?.handlers.onSuccess({
      kind: "saved",
      result: { file_id: DISK_ID, size: 3, ok: true },
    });
    expect(state.fileId).toBe(DISK_ID);
    expect(state.original.value).toBe("x\ny");
  });
});
