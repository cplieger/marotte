import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import type * as Dom from "./dom.js";
import type * as Tabs from "./tabs.js";
import type * as EditorActions from "./actions/editor.js";

const btn = document.createElement("button");
btn.setAttribute("aria-label", "Live refresh");
const reason = document.createElement("span");
reason.classList.add("hidden");

vi.mock("./dom.js", async (importOriginal) => ({
  ...(await importOriginal<typeof Dom>()),
  $: { editorLiveBtn: btn, editorLiveReason: reason },
}));

let tabShown = true;
vi.mock("./tabs.js", async (importOriginal) => ({
  ...(await importOriginal<typeof Tabs>()),
  getActiveTabId: () => (tabShown ? "tb_1" : "tb_other"),
  tabIdFor: () => "tb_1",
}));

type Answer =
  { kind: "stat"; stat: Record<string, unknown> } | { kind: "gone" } | { kind: "skipped" };
/** What the next stat answers, and how many requests reached the server. */
let next: Answer;
let requests = 0;
vi.mock("./actions/editor.js", async (importOriginal) => ({
  ...(await importOriginal<typeof EditorActions>()),
  statFile: {
    dispatch: (args: { shown: () => boolean }) => {
      if (!args.shown()) {
        return Promise.resolve({ kind: "skipped" });
      }
      requests++;
      return Promise.resolve(next);
    },
  },
}));

const { fileStates, freshState, setActiveFilePath, setPainter } = await import("./editor-types.js");
const {
  initLive,
  liveActivate,
  liveDispose,
  liveHold,
  paintLiveButton,
  setLiveReload,
  _resetLiveForTest,
  LIVE_SWITCH,
  LIVE_PAUSE,
  LIVE_HELD,
} = await import("./viewer-live.js");

// The pane's painter, reduced to the one control this file observes.
setPainter(paintLiveButton);

const PATH = "/w/log.txt";
const OLD = `sha256:${"0".repeat(64)}`;
const NEW = `sha256:${"1".repeat(64)}`;

const stat = (id: string): Answer => ({
  kind: "stat",
  stat: {
    path: PATH,
    file_id: id,
    modified: "x",
    size: 1,
    large: false,
    binary: false,
    utf8: true,
    read_only: false,
  },
});

/** The opener's re-read, reduced to what the machine observes of it: the adopted identity. */
const adopt = (s: unknown, st: unknown): Promise<void> => {
  (s as { fileId: string }).fileId = (st as { file_id: string }).file_id;
  return Promise.resolve();
};
const reload = vi.fn((s: unknown, st: unknown, _stillCurrent: () => boolean) => adopt(s, st));

function openState(): ReturnType<typeof freshState> {
  const state = freshState(PATH);
  state.fileId = OLD;
  state.loaded = true;
  state.facts.value = {
    kind: "small",
    binary: false,
    utf8: true,
    conflict: false,
    readOnly: false,
    size: 1,
  };
  fileStates.set(PATH, state);
  setActiveFilePath(PATH);
  return state;
}

async function advance(ms: number): Promise<void> {
  await vi.advanceTimersByTimeAsync(ms);
}

initLive();
setLiveReload(reload);

beforeEach(() => {
  vi.useFakeTimers();
  _resetLiveForTest();
  fileStates.clear();
  requests = 0;
  tabShown = true;
  next = stat(OLD);
  reload.mockReset();
  reload.mockImplementation((s, st) => adopt(s, st));
  btn.classList.add("hidden");
  reason.classList.add("hidden");
});

afterEach(() => {
  _resetLiveForTest();
  vi.useRealTimers();
});

describe("live refresh", () => {
  it("opens paused with no button, and polls every 5 s", async () => {
    openState();
    liveActivate(PATH);
    await advance(0);
    expect(requests).toBe(1);
    expect(btn.classList.contains("hidden")).toBe(true);
    expect(reason.classList.contains("hidden")).toBe(true);
    await advance(5000);
    expect(requests).toBe(2);
  });

  it("shows 'Switch to live refresh' on a detected change and stops polling", async () => {
    openState();
    liveActivate(PATH);
    await advance(0);
    next = stat(NEW);
    await advance(5000);
    expect(btn.classList.contains("hidden")).toBe(false);
    expect(btn.textContent).toBe(LIVE_SWITCH);
    expect(btn.getAttribute("aria-pressed")).toBe("false");
    const after = requests;
    await advance(30_000);
    expect(requests).toBe(after);
    expect(reload).not.toHaveBeenCalled();
  });

  it("asks nothing while the tab is not shown", async () => {
    openState();
    tabShown = false;
    liveActivate(PATH);
    await advance(30_000);
    expect(requests).toBe(0);
  });

  it("goes live on a click after a fresh check, follows every second, and pauses on the next click", async () => {
    openState();
    liveActivate(PATH);
    await advance(0);
    next = stat(NEW);
    await advance(5000);
    btn.click();
    await advance(0);
    expect(reload).toHaveBeenCalledTimes(1);
    expect(btn.textContent).toBe(LIVE_PAUSE);
    expect(btn.getAttribute("aria-pressed")).toBe("true");
    expect(btn.getAttribute("aria-label")).toBe("Live refresh");
    const before = requests;
    await advance(3000);
    expect(requests - before).toBeGreaterThanOrEqual(3);
    btn.click();
    expect(btn.textContent).toBe(LIVE_SWITCH);
    expect(btn.getAttribute("aria-pressed")).toBe("false");
    const paused = requests;
    await advance(10_000);
    expect(requests).toBe(paused);
  });

  it("holds while the buffer is dirty, and releases when it is clean again", async () => {
    const state = openState();
    liveActivate(PATH);
    await advance(0);
    next = stat(NEW);
    await advance(5000);
    state.current.value = "edited";
    expect(btn.disabled).toBe(true);
    expect(btn.getAttribute("data-tooltip")).toBe(LIVE_HELD);
    expect(btn.getAttribute("aria-describedby")).toBe("editor-live-reason");
    expect(reason.classList.contains("hidden")).toBe(false);
    expect(btn.getAttribute("aria-label")).toBe("Live refresh");
    state.original.value = "edited";
    expect(btn.disabled).toBe(false);
    expect(btn.hasAttribute("aria-describedby")).toBe(false);
    expect(reason.classList.contains("hidden")).toBe(true);
  });

  // A composition holds before anything is dirty, and drops an answer in flight.
  it("drops an answer in flight when a composition starts", async () => {
    const state = openState();
    let stillCurrent: (() => boolean) | null = null;
    reload.mockImplementation((_s, _st, sc) => {
      stillCurrent = sc;
      return new Promise<void>(() => undefined);
    });
    liveActivate(PATH);
    await advance(0);
    next = stat(NEW);
    await advance(5000);
    btn.click();
    await advance(0);
    expect(stillCurrent).not.toBeNull();
    liveHold(state, "composing");
    expect((stillCurrent as unknown as () => boolean)()).toBe(false);
  });

  it("keeps an answer dropped once a composition that changed nothing has ended", async () => {
    const state = openState();
    let stillCurrent: (() => boolean) | null = null;
    reload.mockImplementation((_s, _st, sc) => {
      stillCurrent = sc;
      return new Promise<void>(() => undefined);
    });
    liveActivate(PATH);
    await advance(0);
    next = stat(NEW);
    await advance(5000);
    btn.click();
    await advance(0);
    liveHold(state, "composing")();
    expect((stillCurrent as unknown as () => boolean)()).toBe(false);
  });

  it("follows nothing through a composition, and follows again once it ends", async () => {
    const state = openState();
    liveActivate(PATH);
    await advance(0);
    next = stat(NEW);
    await advance(5000);
    btn.click();
    await advance(0);
    expect(reload).toHaveBeenCalledTimes(1);
    const release = liveHold(state, "composing");
    next = stat(`sha256:${"2".repeat(64)}`);
    const before = requests;
    await advance(5000);
    expect(requests).toBe(before);
    expect(reload).toHaveBeenCalledTimes(1);
    release();
    await advance(1000);
    expect(reload).toHaveBeenCalledTimes(2);
  });

  it("lets a closed tab's late release leave the reopened file's composition holding", async () => {
    const retired = openState();
    liveActivate(PATH);
    await advance(0);
    const releaseRetired = liveHold(retired, "composing");
    liveDispose(PATH);
    fileStates.delete(PATH);
    const reopened = openState();
    liveActivate(PATH);
    await advance(0);
    liveHold(reopened, "composing");
    releaseRetired();
    const before = requests;
    await advance(10_000);
    expect(requests).toBe(before);
  });

  it("holds nothing for a retired record", async () => {
    const retired = openState();
    liveActivate(PATH);
    await advance(0);
    liveDispose(PATH);
    fileStates.delete(PATH);
    openState();
    liveActivate(PATH);
    await advance(0);
    liveHold(retired, "composing");
    const before = requests;
    await advance(5000);
    expect(requests).toBe(before + 1);
  });

  it("asks nothing about a file over the cap", async () => {
    const state = openState();
    state.facts.value = { kind: "large", size: 3 << 20 };
    liveActivate(PATH);
    await advance(30_000);
    expect(requests).toBe(0);
  });

  it("stops following once a live file grows past the cap", async () => {
    openState();
    reload.mockImplementation((s) => {
      (s as { facts: { value: unknown } }).facts.value = { kind: "large", size: 3 << 20 };
      return Promise.resolve();
    });
    liveActivate(PATH);
    await advance(0);
    next = stat(NEW);
    await advance(5000);
    btn.click();
    await advance(0);
    expect(reload).toHaveBeenCalledTimes(1);
    const after = requests;
    await advance(10_000);
    expect(requests).toBe(after);
    expect(btn.classList.contains("hidden")).toBe(true);
  });

  it("stops its live cadence once a followed change leaves the file over the cap", async () => {
    openState();
    liveActivate(PATH);
    await advance(0);
    next = stat(NEW);
    await advance(5000);
    btn.click();
    await advance(0);
    reload.mockImplementation((s) => {
      (s as { facts: { value: unknown } }).facts.value = { kind: "large", size: 3 << 20 };
      return Promise.resolve();
    });
    next = stat(`sha256:${"2".repeat(64)}`);
    await advance(1000);
    expect(reload).toHaveBeenCalledTimes(2);
    const after = requests;
    await advance(10_000);
    expect(requests).toBe(after);
  });

  it("stops polling when the file is gone, and records it", async () => {
    const state = openState();
    liveActivate(PATH);
    await advance(0);
    next = { kind: "gone" };
    await advance(5000);
    expect(state.live).toBe("gone");
    const after = requests;
    await advance(30_000);
    expect(requests).toBe(after);
  });
});
