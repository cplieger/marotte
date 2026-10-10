// What a file shows is derived from its own record, and an answer landing after the reader moved
// on is never adopted. The transition suites drive the shipped editor markup, the real editor
// modules, the real live machine and the real loadDiff; only what the harness stages is replaced.
// A held route answers only when the test releases it.
import { afterAll, afterEach, beforeAll, beforeEach, expect, vi } from "vitest";
import { mountEditorView } from "./editor-dom.js";
import { resetActionFramework } from "../actions/__test-helpers__/action-test-setup.js";
import { initEditor } from "../editor-core.js";
import { activateFile, closeEditorFile, openFile, openFileGitDiff } from "../editor-openers.js";
import { fileStates } from "../editor-types.js";
import { LIVE_PAUSE, LIVE_SWITCH, _resetLiveForTest } from "../viewer-live.js";
import { setWorkspaceRoot } from "../workspace.js";
import {
  A,
  B,
  FIRST,
  FOLLOWED,
  NEW,
  byId,
  drain,
  heldRequest,
  hold,
  onScreen,
  readURL,
  resetHarness,
  server,
  shown,
  showURL,
  stage,
  statURL,
  tabs,
} from "./editor-transitions-harness.js";

tabs.show = activateFile;

type FileRecord = NonNullable<ReturnType<typeof fileStates.get>>;

/** Mount the editor once per file and reset the staged world around every test. */
export function installTransitionHarness(): void {
  let host: HTMLElement;
  beforeAll(() => {
    host = mountEditorView();
    initEditor();
  });
  afterAll(() => {
    host.remove();
  });
  beforeEach(() => {
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout", "Date"] });
    resetActionFramework();
    _resetLiveForTest();
    setWorkspaceRoot("/workspace");
    resetHarness();
    stage(B, "the other file\n");
  });
  afterEach(() => {
    for (const path of [...fileStates.keys()]) {
      closeEditorFile(path);
    }
    vi.useRealTimers();
  });
}

/** The tab strip activating a tab it already holds. */
export function showTab(path: string): void {
  tabs.active = tabs.minted.get(path) ?? "";
  activateFile(path);
}

export async function opened(path: string, line?: number): Promise<void> {
  openFile(path, line);
  await vi.waitFor(() => {
    expect(fileStates.get(path)?.loaded).toBe(true);
  });
  await drain();
}

export function recordOf(path: string): FileRecord {
  const state = fileStates.get(path);
  if (state === undefined) {
    throw new Error(`${path} has no record`);
  }
  return state;
}

/** Type `text` into the textarea as the reader would. */
export function typeInto(text: string): void {
  const area = byId<HTMLTextAreaElement>("editor-content");
  area.value = text;
  area.dispatchEvent(new Event("input", { bubbles: true }));
}

export async function wentLive(): Promise<void> {
  stage(A, FIRST);
  await opened(A);
  stage(A, FOLLOWED);
  await vi.advanceTimersByTimeAsync(5000);
  await vi.waitFor(() => {
    expect(byId("editor-live-btn").textContent).toBe(LIVE_SWITCH);
  });
  // Going live adopts at once; the change after it is the request held in flight.
  byId("editor-live-btn").click();
  await vi.waitFor(() => {
    expect(fileStates.get(A)?.current.value).toBe(FOLLOWED);
  });
  await drain();
  expect(byId("editor-live-btn").textContent).toBe(LIVE_PAUSE);
}

/** Follow A live, with the re-read of NEW in flight. */
export async function liveReadInFlight(): Promise<void> {
  await wentLive();
  stage(A, NEW);
  hold(readURL(A));
  await vi.advanceTimersByTimeAsync(1000);
  await heldRequest(readURL(A));
}

/** A's change seen, with the check "Switch to live refresh" makes first in flight. */
export async function switchCheckInFlight(): Promise<void> {
  stage(A, FIRST);
  await opened(A);
  stage(A, NEW);
  await vi.advanceTimersByTimeAsync(5000);
  await vi.waitFor(() => {
    expect(byId("editor-live-btn").textContent).toBe(LIVE_SWITCH);
  });
  hold(statURL(A));
  byId("editor-live-btn").click();
  await heldRequest(statURL(A));
}

/** Follow A live, with the check that would find NEW in flight. */
export async function liveCheckInFlight(): Promise<void> {
  await wentLive();
  stage(A, NEW);
  hold(statURL(A));
  await vi.advanceTimersByTimeAsync(1000);
  await heldRequest(statURL(A));
}

/** Open A as a git diff with its first load in flight. */
export async function gitDiffInFlight(): Promise<void> {
  stage(A, NEW);
  server.routes.set(showURL("a.txt"), { status: 200, body: { content: FIRST } });
  hold(readURL(A));
  openFileGitDiff(A);
  await heldRequest(readURL(A));
}

/** Open A with its first read in flight. */
export async function loadInFlight(): Promise<void> {
  stage(A, NEW);
  hold(readURL(A));
  openFile(A);
  await heldRequest(readURL(A));
}

/** Close A and open it again onto FIRST, leaving what A had in flight held. */
export async function closedAndReopened(): Promise<FileRecord> {
  const retired = recordOf(A);
  closeEditorFile(A);
  server.holding.clear();
  stage(A, FIRST);
  await opened(A);
  return retired;
}

/** The reopened A as a fresh open leaves it: its own bytes, watched every 5 s, no toggle and no
 *  banner. */
export async function expectFreshlyWatched(): Promise<void> {
  expect(fileStates.get(A)?.current.value).toBe(FIRST);
  expect(onScreen()).toContain(FIRST.trim());
  expect(fileStates.get(A)?.live).toBe("watching");
  expect(shown("editor-live-btn")).toBe(false);
  expect(shown("editor-error")).toBe(false);
  const checks = server.hits.get(statURL(A)) ?? 0;
  await vi.advanceTimersByTimeAsync(9000);
  expect((server.hits.get(statURL(A)) ?? 0) - checks).toBe(1);
}
