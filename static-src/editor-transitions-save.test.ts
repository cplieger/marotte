// A save's answer settles on the record and the baseline it was dispatched under, or nowhere.
import { describe, it, expect, vi } from "vitest";
import type * as ApiClientModule from "./api-client.js";
import type * as EditorActions from "./actions/editor.js";
import type * as ConfirmModule from "./confirm.js";
import type * as Git from "./git.js";
import type * as RouterModule from "./router.js";
import type * as TabsModule from "./tabs.js";

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

import type { FileRefusal } from "./wire/types.gen.js";
import {
  A,
  B,
  FIRST,
  FOLLOWED,
  NEW,
  byId,
  drain,
  idOf,
  onScreen,
  saves,
  server,
  shown,
  stage,
  statURL,
  type SaveCall,
} from "./__test-helpers__/editor-transitions-harness.js";
import {
  closedAndReopened,
  expectFreshlyWatched,
  installTransitionHarness,
  opened,
  recordOf,
  showTab,
  typeInto,
  wentLive,
} from "./__test-helpers__/editor-transitions-driver.js";
import { closeEditorFile } from "./editor-openers.js";
import { fileStates } from "./editor-types.js";
import { markGitDirty } from "./git.js";
import { GONE_SENTENCE } from "./viewer-live.js";

installTransitionHarness();

const changedOnDisk: FileRefusal = {
  error: "This file changed on disk.",
  code: "changed",
  content_kind: "text",
  content: "z\n",
  size: 2,
  file_id: idOf("z\n"),
};

describe("a save answered while another file is shown", () => {
  async function editedAndSaved(): Promise<void> {
    stage(A, "x\n");
    await opened(A);
    byId("editor-edit-btn").click();
    const area = byId<HTMLTextAreaElement>("editor-content");
    area.value = "x\ny";
    area.dispatchEvent(new Event("input", { bubbles: true }));
    byId<HTMLButtonElement>("editor-save-btn").click();
    expect(saves).toHaveLength(1);
    await opened(B);
  }

  it("shows the disk version's diff and the reason on return, and nothing meanwhile", async () => {
    await editedAndSaved();
    saves[0]?.handlers.onSuccess({ kind: "stale", refusal: changedOnDisk });
    expect(shown("editor-error")).toBe(false);
    expect(shown("editor-viewer")).toBe(true);
    showTab(A);
    await drain();
    expect(byId("editor-error").textContent).toBe("This file changed on disk.");
    expect(shown("editor-diff-pane")).toBe(true);
    expect(shown("editor-save-btn")).toBe(false);
  });

  it("says a save failed on the file's own tab, on the return", async () => {
    await editedAndSaved();
    saves[0]?.handlers.onError(new Error("disk full"));
    expect(shown("editor-error")).toBe(false);
    showTab(A);
    await drain();
    expect(byId("editor-error").textContent).toBe("disk full");
  });

  it("keeps a refusal and its ways on for the return, with Save disabled", async () => {
    await editedAndSaved();
    saves[0]?.handlers.onSuccess({
      kind: "stale",
      refusal: { error: "changed", code: "binary", content_kind: "binary" },
    });
    showTab(A);
    await drain();
    expect(byId("editor-error").querySelector(".editor-error-text")?.textContent).toBe(
      "This file changed on disk and is now binary.",
    );
    expect(
      [...byId("editor-error").querySelectorAll(".editor-error-actions button")].map(
        (b) => b.textContent,
      ),
    ).toEqual(["Overwrite", "Download my version", "Discard my changes"]);
    expect(byId<HTMLButtonElement>("editor-save-btn").disabled).toBe(true);
  });
});

// Every outcome a save can answer, released once something newer than the save owns the record.
const outcomes: Record<string, (call: SaveCall) => void> = {
  "a written answer": (c) => {
    c.handlers.onSuccess({
      kind: "saved",
      result: { file_id: idOf("draft base\nedited"), size: 17, ok: true },
    });
  },
  "a stale answer carrying the disk text": (c) => {
    c.handlers.onSuccess({ kind: "stale", refusal: changedOnDisk });
  },
  "a stale answer refusing the file": (c) => {
    c.handlers.onSuccess({
      kind: "stale",
      refusal: { error: "changed", code: "binary", content_kind: "binary" },
    });
  },
  "a failed save": (c) => {
    c.handlers.onError(new Error("disk full"));
  },
};

describe("a save outcome for a closed tab, after the same path is reopened", () => {
  it("a save's answer is recorded on neither record and paints nothing", async () => {
    vi.mocked(markGitDirty).mockClear();
    stage(A, "draft base\n");
    await opened(A);
    byId("editor-edit-btn").click();
    const area = byId<HTMLTextAreaElement>("editor-content");
    area.value = "draft base\nedited";
    area.dispatchEvent(new Event("input", { bubbles: true }));
    byId<HTMLButtonElement>("editor-save-btn").click();
    expect(saves).toHaveLength(1);
    const retired = await closedAndReopened();
    saves[0]?.handlers.onSuccess({
      kind: "saved",
      result: { file_id: idOf("draft base\nedited"), size: 17, ok: true },
    });
    await drain();
    expect(retired.original.value).toBe("draft base\n");
    expect(retired.fileId).toBe(idOf("draft base\n"));
    // The write did happen, so the git surfaces still hear of it.
    expect(markGitDirty).toHaveBeenCalledWith(["a.txt"]);
    await expectFreshlyWatched();
    expect(shown("editor-content")).toBe(false);
    expect(onScreen()).not.toContain("draft base");
  });

  it.each(Object.keys(outcomes).filter((name) => name !== "a written answer"))(
    "%s leaves the reopened file as it was read",
    async (name) => {
      stage(A, "draft base\n");
      await opened(A);
      byId("editor-edit-btn").click();
      const area = byId<HTMLTextAreaElement>("editor-content");
      area.value = "draft base\nedited";
      area.dispatchEvent(new Event("input", { bubbles: true }));
      byId<HTMLButtonElement>("editor-save-btn").click();
      const retired = fileStates.get(A);
      closeEditorFile(A);
      stage(A, FIRST);
      await opened(A);
      const call = saves[0];
      if (call === undefined || retired === undefined) {
        throw new Error("no save was dispatched");
      }
      outcomes[name]?.(call);
      await drain();
      expect(retired.alert.value).toBeNull();
      expect(retired.original.value).toBe("draft base\n");
      expect(fileStates.get(A)?.alert.value).toBeNull();
      expect(shown("editor-error")).toBe(false);
      expect(shown("editor-diff-pane")).toBe(false);
      expect(shown("editor-content")).toBe(false);
      expect(onScreen()).toContain(FIRST.trim());
    },
  );

  it("a written answer does not tell the reopened file that it is back on disk", async () => {
    stage(A, "draft base\n");
    await opened(A);
    byId("editor-edit-btn").click();
    const area = byId<HTMLTextAreaElement>("editor-content");
    area.value = "draft base\nedited";
    area.dispatchEvent(new Event("input", { bubbles: true }));
    byId<HTMLButtonElement>("editor-save-btn").click();
    closeEditorFile(A);
    stage(A, FIRST);
    await opened(A);
    server.routes.delete(statURL(A));
    await vi.advanceTimersByTimeAsync(5000);
    await vi.waitFor(() => {
      expect(byId("editor-error").textContent).toBe(GONE_SENTENCE);
    });
    const checks = server.hits.get(statURL(A)) ?? 0;
    saves[0]?.handlers.onSuccess({
      kind: "saved",
      result: { file_id: idOf("draft base\nedited"), size: 17, ok: true },
    });
    await drain();
    expect(fileStates.get(A)?.live).toBe("gone");
    expect(byId("editor-error").textContent).toBe(GONE_SENTENCE);
    expect(server.hits.get(statURL(A))).toBe(checks);
  });
});

// The diff of the buffer compares it with the saved text as it is now, so a save settling under it
// moves its saved side.
describe("a diff of the buffer opened while a save is in flight", () => {
  it("leaves the diff once the save has written every change", async () => {
    stage(A, "base\n");
    await opened(A);
    byId("editor-edit-btn").click();
    typeInto("base\nmine");
    byId<HTMLButtonElement>("editor-save-btn").click();
    saves[0]?.handlers.onSuccess({
      kind: "stale",
      refusal: { ...changedOnDisk, content: "disk\n", size: 5, file_id: idOf("disk\n") },
    });
    await drain();
    byId("editor-edit-btn").click();
    byId<HTMLButtonElement>("editor-save-btn").click();
    expect(shown("editor-diff-btn")).toBe(true);
    byId("editor-diff-btn").click();
    expect(shown("editor-diff-pane")).toBe(true);
    saves[1]?.handlers.onSuccess({
      kind: "saved",
      result: { file_id: idOf("base\nmine"), size: 9, ok: true },
    });
    await drain();
    expect(recordOf(A).dirty.value).toBe(false);
    expect(shown("editor-diff-pane")).toBe(false);
    expect(onScreen()).toContain("mine");
  });

  it("compares the edits made after the save with what it wrote", async () => {
    stage(A, "base\n");
    await opened(A);
    byId("editor-edit-btn").click();
    typeInto("base\nsaved line");
    await opened(B);
    showTab(A);
    await drain();
    byId<HTMLButtonElement>("editor-save-btn").click();
    typeInto("base\nsaved line\nlater line");
    expect(shown("editor-diff-btn")).toBe(true);
    byId("editor-diff-btn").click();
    saves[0]?.handlers.onSuccess({
      kind: "saved",
      result: { file_id: idOf("base\nsaved line"), size: 15, ok: true },
    });
    await drain();
    expect(shown("editor-diff-pane")).toBe(true);
    expect(byId("editor-diff-pane").querySelector(".diff-col-old")?.textContent).toContain(
      "saved line",
    );
  });
});

// Typing back to the saved text while a save is in flight makes the buffer clean, so live refresh
// may adopt newer bytes before the save answers. Those bytes, not the save's, are the baseline.
describe("a save answered after live refresh adopted newer bytes", () => {
  /** Save an edit of A, type back to the saved text, and let live refresh follow `next` on disk. */
  async function savedThenFollowed(
    next: () => void = () => {
      stage(A, NEW);
    },
  ): Promise<void> {
    await wentLive();
    byId("editor-edit-btn").click();
    typeInto("draft base\nedited");
    byId<HTMLButtonElement>("editor-save-btn").click();
    expect(saves).toHaveLength(1);
    next();
    typeInto(FOLLOWED);
    await vi.advanceTimersByTimeAsync(1000);
    await vi.waitFor(() => {
      expect(recordOf(A).fileId).not.toBe(idOf(FOLLOWED));
    });
    await drain();
  }

  it.each(Object.keys(outcomes))("%s leaves the adopted bytes the baseline", async (name) => {
    await savedThenFollowed();
    const call = saves[0];
    if (call === undefined) {
      throw new Error("no save was dispatched");
    }
    outcomes[name]?.(call);
    await drain();
    const state = recordOf(A);
    expect(state.original.value).toBe(NEW);
    expect(state.fileId).toBe(idOf(NEW));
    expect(state.dirty.value).toBe(false);
    expect(state.alert.value).toBeNull();
    expect(byId("editor-download-btn").dataset["fileId"]).toBe(idOf(NEW));
    expect(shown("editor-error")).toBe(false);
    expect(shown("editor-diff-pane")).toBe(false);
    expect(onScreen()).toContain(NEW.trim());
  });

  it("still tells the git surfaces of the write", async () => {
    vi.mocked(markGitDirty).mockClear();
    await savedThenFollowed();
    const call = saves[0];
    if (call === undefined) {
      throw new Error("no save was dispatched");
    }
    outcomes["a written answer"]?.(call);
    expect(markGitDirty).toHaveBeenCalledWith(["a.txt"]);
  });

  it("a written answer leaves a file live refresh found binary under its own identity", async () => {
    const binaryId = idOf("\u0000binary");
    await savedThenFollowed(() => {
      server.routes.set(statURL(A), {
        status: 200,
        body: {
          path: A,
          modified: "2026-10-01T00:00:00Z",
          size: 7,
          file_id: binaryId,
          large: false,
          binary: true,
          utf8: false,
          read_only: false,
        },
      });
    });
    expect(recordOf(A).mode.value.kind).toBe("binary");
    const call = saves[0];
    if (call === undefined) {
      throw new Error("no save was dispatched");
    }
    outcomes["a written answer"]?.(call);
    await drain();
    expect(recordOf(A).fileId).toBe(binaryId);
    expect(byId("editor-download-btn").dataset["fileId"]).toBe(binaryId);
    expect(recordOf(A).mode.value.kind).toBe("binary");
  });
});
