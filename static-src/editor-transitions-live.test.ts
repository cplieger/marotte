// Live refresh's toggle and checks belong to the record they were started for.
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
  FIRST,
  NEW,
  byId,
  drain,
  heldRequest,
  hold,
  readURL,
  releaseAll,
  server,
  shown,
  stage,
  stageLarge,
  statURL,
} from "./__test-helpers__/editor-transitions-harness.js";
import {
  closedAndReopened,
  expectFreshlyWatched,
  installTransitionHarness,
  liveCheckInFlight,
  opened,
  showTab,
  switchCheckInFlight,
} from "./__test-helpers__/editor-transitions-driver.js";
import { fileStates } from "./editor-types.js";
import { GONE_SENTENCE, LIVE_SWITCH } from "./viewer-live.js";

installTransitionHarness();

// The reason describes a held toggle; with no toggle on screen there is nothing for it to explain.
describe("the live-refresh reason", () => {
  it("is absent from a freshly opened file, which has no live control", async () => {
    stage(A, FIRST);
    await opened(A);
    expect(shown("editor-live-btn")).toBe(false);
    expect(shown("editor-live-reason")).toBe(false);
  });

  it("is present only while a shown toggle is held by unsaved changes", async () => {
    stage(A, FIRST);
    await opened(A);
    stage(A, NEW);
    await vi.advanceTimersByTimeAsync(5000);
    await vi.waitFor(() => {
      expect(shown("editor-live-btn")).toBe(true);
    });
    expect(shown("editor-live-reason")).toBe(false);
    const state = fileStates.get(A);
    if (state === undefined) {
      throw new Error("A has no state");
    }
    state.current.value = "edited\n";
    expect(shown("editor-live-reason")).toBe(true);
    state.current.value = state.original.value;
    expect(shown("editor-live-reason")).toBe(false);
  });

  it("leaves with the held toggle when a file over the cap is shown", async () => {
    stage(A, FIRST);
    await opened(A);
    stage(A, NEW);
    await vi.advanceTimersByTimeAsync(5000);
    await vi.waitFor(() => {
      expect(shown("editor-live-btn")).toBe(true);
    });
    const state = fileStates.get(A);
    if (state === undefined) {
      throw new Error("A has no state");
    }
    state.current.value = "edited\n";
    expect(shown("editor-live-reason")).toBe(true);
    stageLarge(BIG);
    await opened(BIG);
    expect(shown("editor-live-btn")).toBe(false);
    expect(shown("editor-live-reason")).toBe(false);
  });
});

// Closing a tab retires its record and reopening the path makes a new one, so an answer still in
// flight for the old record must reach neither the new record nor the pane, whatever it answers.
describe("an answer for a closed tab, after the same path is reopened", () => {
  it("a live check leaves the reopened file watched every 5 s", async () => {
    await liveCheckInFlight();
    await closedAndReopened();
    await releaseAll();
    await expectFreshlyWatched();
  });

  it("the check Switch to live refresh makes does not take the reopened file live", async () => {
    await switchCheckInFlight();
    await closedAndReopened();
    await releaseAll();
    await expectFreshlyWatched();
  });

  it("the re-read the check Switch makes leads to does not take the reopened file live", async () => {
    stage(A, FIRST);
    await opened(A);
    stage(A, NEW);
    await vi.advanceTimersByTimeAsync(5000);
    await vi.waitFor(() => {
      expect(byId("editor-live-btn").textContent).toBe(LIVE_SWITCH);
    });
    hold(readURL(A));
    byId("editor-live-btn").click();
    await heldRequest(readURL(A));
    await closedAndReopened();
    await releaseAll();
    await expectFreshlyWatched();
  });

  it("the check Switch makes, finding the file gone, says nothing of the reopened file", async () => {
    stage(A, FIRST);
    await opened(A);
    stage(A, NEW);
    await vi.advanceTimersByTimeAsync(5000);
    await vi.waitFor(() => {
      expect(byId("editor-live-btn").textContent).toBe(LIVE_SWITCH);
    });
    server.routes.delete(statURL(A));
    hold(statURL(A));
    byId("editor-live-btn").click();
    await heldRequest(statURL(A));
    await closedAndReopened();
    await releaseAll();
    await expectFreshlyWatched();
  });
});

function compose(type: "compositionstart" | "compositionend"): void {
  byId("editor-content").dispatchEvent(new CompositionEvent(type, { bubbles: true }));
}

/** Open `path` and begin editing it, the textarea focused. */
async function editing(path: string): Promise<void> {
  stage(path, FIRST);
  await opened(path);
  byId("editor-edit-btn").click();
  byId<HTMLTextAreaElement>("editor-content").focus();
  await drain();
}

/** A shown, clean file asks the server again within one 5 s watch. */
async function expectWatchedAgain(path: string): Promise<void> {
  const checks = server.hits.get(statURL(path)) ?? 0;
  await vi.advanceTimersByTimeAsync(5000);
  expect((server.hits.get(statURL(path)) ?? 0) - checks).toBeGreaterThanOrEqual(1);
}

// A composition holds the file it began on; its end, or the textarea moving to another file,
// releases that file whichever one is on screen by then.
describe("an IME composition crossing a tab transition", () => {
  it("ends on the file it began on after a switch to another tab", async () => {
    await editing(A);
    compose("compositionstart");
    await opened(B);
    compose("compositionend");
    showTab(A);
    await drain();
    await expectWatchedAgain(A);
  });

  it("does not strand the file it began on when the IME never ends it", async () => {
    await editing(B);
    await editing(A);
    compose("compositionstart");
    showTab(B);
    await drain();
    showTab(A);
    await drain();
    await expectWatchedAgain(A);
  });

  it("ends when the textarea loses focus, for an IME that never sends its end", async () => {
    await editing(A);
    compose("compositionstart");
    byId("editor-content").blur();
    await drain();
    await expectWatchedAgain(A);
  });

  it("ending after the tab closed and reopened leaves the reopened file watched", async () => {
    await editing(A);
    compose("compositionstart");
    await closedAndReopened();
    compose("compositionend");
    await expectFreshlyWatched();
  });
});

describe("the check Switch to live refresh makes, answered after a switch to another tab", () => {
  it("leaves the toggle offering Switch when the file is shown again", async () => {
    await switchCheckInFlight();
    await opened(B);
    await releaseAll();
    showTab(A);
    await drain();
    expect(fileStates.get(A)?.live).toBe("changed");
    expect(byId("editor-live-btn").textContent).toBe(LIVE_SWITCH);
  });
});

describe("the check Switch to live refresh makes, finding the file gone", () => {
  it("says the file is gone after that one check, rather than going live", async () => {
    stage(A, FIRST);
    await opened(A);
    stage(A, NEW);
    await vi.advanceTimersByTimeAsync(5000);
    await vi.waitFor(() => {
      expect(byId("editor-live-btn").textContent).toBe(LIVE_SWITCH);
    });
    server.routes.delete(statURL(A));
    const checks = server.hits.get(statURL(A)) ?? 0;
    byId("editor-live-btn").click();
    await drain();
    expect(fileStates.get(A)?.live).toBe("gone");
    expect(byId("editor-error").textContent).toBe(GONE_SENTENCE);
    expect(shown("editor-live-btn")).toBe(false);
    await vi.advanceTimersByTimeAsync(10_000);
    expect(server.hits.get(statURL(A))).toBe(checks + 1);
  });
});
