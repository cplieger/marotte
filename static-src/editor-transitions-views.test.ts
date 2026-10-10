// The shared pane shows only the shown record's banner, view and controls.
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

import {
  A,
  B,
  BIG,
  CONFLICT,
  CONFLICTED,
  FIRST,
  NEW,
  byId,
  confirms,
  drain,
  heldRequest,
  hold,
  onScreen,
  readURL,
  releaseAll,
  server,
  showURL,
  shown,
  stage,
  stageLarge,
  statURL,
  suggestions,
} from "./__test-helpers__/editor-transitions-harness.js";
import {
  gitDiffInFlight,
  installTransitionHarness,
  opened,
  showTab,
} from "./__test-helpers__/editor-transitions-driver.js";
import { closeEditorFile, openFileGitDiff } from "./editor-openers.js";
import { fileStates } from "./editor-types.js";

installTransitionHarness();

describe("a banner belongs to its file", () => {
  it("says a file is gone on its own tab only", async () => {
    stage(A, FIRST);
    await opened(A);
    server.routes.delete(statURL(A));
    await vi.advanceTimersByTimeAsync(5000);
    await vi.waitFor(() => {
      expect(byId("editor-error").textContent).toBe("This file is no longer on disk.");
    });
    await opened(B);
    expect(shown("editor-error")).toBe(false);
    showTab(A);
    await drain();
    expect(byId("editor-error").textContent).toBe("This file is no longer on disk.");
  });
});

describe("resolving a file's last conflict", () => {
  it("takes the conflict's controls off the pane", async () => {
    stage(CONFLICTED, CONFLICT);
    await opened(CONFLICTED);
    const ours = [...byId("editor-conflict-overlay").querySelectorAll("button")].find(
      (b) => b.textContent === "Ours",
    );
    ours?.click();
    expect(fileStates.get(CONFLICTED)?.current.value).toBe("ours\n");
    expect(shown("editor-conflict-overlay")).toBe(false);
  });
});

describe("a switch from a conflict to a file over the cap", () => {
  it("shows the refusal and Download, and none of the conflict's controls", async () => {
    stage(CONFLICTED, CONFLICT);
    await opened(CONFLICTED);
    expect(shown("editor-conflict-overlay")).toBe(true);
    stageLarge(BIG);
    await opened(BIG);
    expect(byId("editor-notice").textContent).toBe(
      "File is too large to display. Download it to view.",
    );
    expect(shown("editor-download-btn")).toBe(true);
    for (const id of [
      "editor-conflict-overlay",
      "editor-error",
      "editor-edit-btn",
      "editor-save-btn",
      "editor-cancel-btn",
      "editor-diff-btn",
      "editor-live-btn",
      "editor-live-reason",
      "editor-readonly-label",
      "editor-content",
    ]) {
      expect(shown(id), id).toBe(false);
    }
  });
});

describe("a diff vs HEAD", () => {
  it("is labelled Git revision once it settles, and the label leaves with it", async () => {
    await gitDiffInFlight();
    expect(shown("editor-readonly-label")).toBe(false);
    await releaseAll();
    await vi.waitFor(() => {
      expect(shown("editor-readonly-label")).toBe(true);
    });
    expect(byId("editor-readonly-label").textContent).toBe("Git revision");
    byId("editor-git-diff-btn").click();
    await drain();
    expect(shown("editor-readonly-label")).toBe(false);
  });

  it("is not labelled while a file already read waits on its diff", async () => {
    stage(A, NEW);
    await opened(A);
    server.routes.set(showURL("a.txt"), { status: 200, body: { content: FIRST } });
    hold(readURL(A));
    openFileGitDiff(A);
    await heldRequest(readURL(A));
    expect(shown("editor-readonly-label")).toBe(false);
    await releaseAll();
    await vi.waitFor(() => {
      expect(shown("editor-readonly-label")).toBe(true);
    });
    expect(byId("editor-readonly-label").textContent).toBe("Git revision");
    byId("editor-git-diff-btn").click();
    await drain();
    expect(shown("editor-readonly-label")).toBe(false);
  });
});

describe("a discard confirmed for a closed tab, after the same path is reopened", () => {
  it("a discard confirmed after the reopen leaves the reopened file on screen", async () => {
    stage(A, "draft base\n");
    await opened(A);
    byId("editor-edit-btn").click();
    const area = byId<HTMLTextAreaElement>("editor-content");
    area.value = "draft base\nedited";
    area.dispatchEvent(new Event("input", { bubbles: true }));
    byId("editor-cancel-btn").click();
    expect(confirms).toHaveLength(1);
    closeEditorFile(A);
    stage(A, FIRST);
    await opened(A);
    confirms[0]?.(true);
    await drain();
    expect(onScreen()).toContain(FIRST.trim());
    expect(onScreen()).not.toContain("draft base");
  });
});

describe("a conflict suggestion for a closed tab, after the same path is reopened", () => {
  const suggestButton = (): HTMLButtonElement | undefined =>
    [...byId("editor-conflict-overlay").querySelectorAll("button")].find(
      (b) => b.textContent === "Suggest",
    );

  it("never paints over the reopened file's own request", async () => {
    stage(CONFLICTED, CONFLICT);
    await opened(CONFLICTED);
    suggestButton()?.click();
    expect(suggestions).toHaveLength(1);
    closeEditorFile(CONFLICTED);
    await opened(CONFLICTED);
    suggestButton()?.click();
    expect(suggestions).toHaveLength(2);
    suggestions[0]?.({ output: "the closed tab's merge" });
    await drain();
    expect(byId("editor-conflict-overlay").textContent).not.toContain("the closed tab's merge");
    expect(byId("editor-conflict-overlay").textContent).toContain("Suggesting");
  });
});
