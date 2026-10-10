// A `#L<n>` jump lands two frames after its file's load, and only on the record it was opened for.
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
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

import { A, B, byId, shown, stage } from "./__test-helpers__/editor-transitions-harness.js";
import { installTransitionHarness, opened } from "./__test-helpers__/editor-transitions-driver.js";
import { closeEditorFile } from "./editor-openers.js";

installTransitionHarness();

// Frames are held until the test runs them, so a transition can land between the load and the jump.
const frames = new Map<number, FrameRequestCallback>();
let lastFrame = 0;

beforeEach(() => {
  frames.clear();
  vi.stubGlobal("requestAnimationFrame", (cb: FrameRequestCallback): number => {
    lastFrame++;
    frames.set(lastFrame, cb);
    return lastFrame;
  });
  vi.stubGlobal("cancelAnimationFrame", (handle: number): void => {
    frames.delete(handle);
  });
  stage(A, "one\ntwo\nthree");
});

afterEach(() => {
  vi.unstubAllGlobals();
});

/** Run every held frame, and the frames those request, until none is left. */
function runFrames(): void {
  for (let round = 0; round < 10 && frames.size > 0; round++) {
    const due = [...frames.values()];
    frames.clear();
    for (const cb of due) {
      cb(performance.now());
    }
  }
}

const gotoStatus = (): string => byId("editor-goto-status").textContent;

describe("a #L<n> deep link", () => {
  it("past the end of the file it was opened for says which line it shows", async () => {
    await opened(A, 40);
    runFrames();
    expect(shown("editor-goto")).toBe(true);
    expect(gotoStatus()).toBe("Line 40 is past the end; showing line 3.");
  });

  it("is dropped by a switch to another tab before its frames run", async () => {
    await opened(A, 40);
    await opened(B);
    runFrames();
    expect(shown("editor-goto")).toBe(false);
    expect(gotoStatus()).toBe("");
  });

  it("is dropped by closing and reopening its tab before its frames run", async () => {
    await opened(A, 40);
    closeEditorFile(A);
    await opened(A);
    runFrames();
    expect(shown("editor-goto")).toBe(false);
    expect(gotoStatus()).toBe("");
  });
});
