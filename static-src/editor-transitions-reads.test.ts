// A read answered after a transition is adopted only by the view and the record it was issued for.
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
  FIRST,
  NEW,
  byId,
  drain,
  heldRequest,
  hold,
  onScreen,
  releaseAll,
  server,
  shown,
  showURL,
  stage,
  statURL,
} from "./__test-helpers__/editor-transitions-harness.js";
import {
  gitDiffInFlight,
  installTransitionHarness,
  liveCheckInFlight,
  liveReadInFlight,
  loadInFlight,
  opened,
  showTab,
  switchCheckInFlight,
} from "./__test-helpers__/editor-transitions-driver.js";
import { closeEditorFile, openFileGitDiff } from "./editor-openers.js";
import { fileStates } from "./editor-types.js";

installTransitionHarness();

// Each in-flight read crossed with each transition that can reach it. The control row makes no
// transition and must adopt, which is what proves the release ran to the end in every other row.
describe("a read answered after a transition", () => {
  const transitions = {
    nothing: (): Promise<void> => Promise.resolve(),
    "Pause live refresh": (): Promise<void> => {
      byId("editor-live-btn").click();
      return Promise.resolve();
    },
    "a switch to another tab": () => opened(B),
    // The new tab's own load must not be the one the old answer paints over.
    "closing and reopening the tab": async (): Promise<void> => {
      closeEditorFile(A);
      server.holding.clear();
      stage(A, FIRST);
      await opened(A);
    },
    Edit: (): Promise<void> => {
      byId("editor-edit-btn").click();
      return Promise.resolve();
    },
    "leaving the git diff": (): Promise<void> => {
      byId("editor-git-diff-btn").click();
      return Promise.resolve();
    },
  } as const;
  type Transition = keyof typeof transitions;

  const adopted = (): boolean => fileStates.get(A)?.current.value.includes(NEW) === true;

  const reads = {
    "a live check": liveCheckInFlight,
    "the check Switch to live refresh makes": switchCheckInFlight,
    "a live re-read": liveReadInFlight,
    "a git diff's first load": gitDiffInFlight,
    "a file's first read": loadInFlight,
  } as const;

  it.each<[keyof typeof reads, Transition, boolean]>([
    ["a live check", "nothing", true],
    ["a live check", "a switch to another tab", false],
    ["a live check", "closing and reopening the tab", false],
    ["the check Switch to live refresh makes", "nothing", true],
    ["the check Switch to live refresh makes", "a switch to another tab", false],
    ["the check Switch to live refresh makes", "closing and reopening the tab", false],
    ["a live re-read", "nothing", true],
    ["a live re-read", "Pause live refresh", false],
    ["a live re-read", "a switch to another tab", false],
    ["a live re-read", "closing and reopening the tab", false],
    ["a live re-read", "Edit", false],
    ["a git diff's first load", "nothing", true],
    ["a git diff's first load", "a switch to another tab", false],
    ["a git diff's first load", "closing and reopening the tab", false],
    ["a git diff's first load", "leaving the git diff", false],
    ["a file's first read", "nothing", true],
    ["a file's first read", "a switch to another tab", false],
    ["a file's first read", "closing and reopening the tab", false],
  ])("%s, after %s: adopted %s", async (read, transition, adopts) => {
    await reads[read]();
    // The record the read was issued for: a closed tab's record takes no answer either.
    const issuedFor = fileStates.get(A);
    await transitions[transition]();
    await releaseAll();
    expect(adopted()).toBe(adopts);
    if (!adopts) {
      expect(issuedFor?.current.value).not.toContain(NEW);
      expect(onScreen()).not.toContain(NEW.trim());
    }
  });

  it("loads a git diff left before it settled when it is shown again", async () => {
    await gitDiffInFlight();
    await opened(B);
    await releaseAll();
    showTab(A);
    await drain();
    expect(shown("editor-diff-pane")).toBe(true);
    expect(byId("editor-diff-pane").textContent).toContain(NEW.trim());
  });

  it("checks the disk on a return to a read file's git diff it left unsettled", async () => {
    stage(A, FIRST);
    await opened(A);
    server.routes.set(showURL("a.txt"), { status: 200, body: { content: "base\n" } });
    hold(showURL("a.txt"));
    openFileGitDiff(A);
    await heldRequest(showURL("a.txt"));
    await opened(B);
    await releaseAll();
    const checks = server.hits.get(statURL(A)) ?? 0;
    showTab(A);
    await drain();
    expect(server.hits.get(statURL(A))).toBe(checks + 1);
  });
});

describe("a repaint that moves only chrome", () => {
  it("keeps the rows the reader is on", async () => {
    stage(A, FIRST);
    await opened(A);
    const row = byId("editor-viewer").querySelector(".viewer-row");
    expect(row).not.toBeNull();
    server.routes.delete(statURL(A));
    await vi.advanceTimersByTimeAsync(5000);
    await vi.waitFor(() => {
      expect(shown("editor-error")).toBe(true);
    });
    expect(byId("editor-viewer").querySelector(".viewer-row")).toBe(row);
  });
});

describe("a file still loading", () => {
  it("shows none of the previous file's controls", async () => {
    await opened(B);
    byId("editor-edit-btn").click();
    expect(shown("editor-save-btn")).toBe(true);
    await loadInFlight();
    expect(shown("editor-notice")).toBe(true);
    for (const id of [
      "editor-save-btn",
      "editor-cancel-btn",
      "editor-edit-btn",
      "editor-content",
      "editor-viewer",
    ]) {
      expect(shown(id), id).toBe(false);
    }
  });
});
