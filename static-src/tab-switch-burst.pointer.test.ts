// A burst of real clicks, each pressed while the previous target's view is still being shown and
// released after it. No uncovered frame shows a tab other than the active one, and once a click is
// sent the tab it leaves shows uncovered for no more than input latency, even while that tab's
// stream repaints it. Real tabs.ts and transcript-cover.ts against the fake tab server.
import { describe, it, expect, vi } from "vitest";
import type * as TabsDrag from "./tabs-drag.js";

vi.mock("./router.js", async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  pushRoute: vi.fn(),
}));
vi.mock("./icons.js", async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  ...(await import("./__test-helpers__/tab-switch-mocks.js").then((m) => m.iconsMock())),
}));
vi.mock("./device-view.js", async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  ...(await import("./__test-helpers__/tab-switch-mocks.js").then((m) => m.deviceViewMock())),
}));
vi.mock("./dom.js", async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  ...(await import("./__test-helpers__/tab-switch-mocks.js").then((m) => m.domMock())),
}));
vi.mock("./tabs-drag.js", async (importOriginal) => ({
  ...(await importOriginal<typeof TabsDrag>()),
  attachDrag: vi.fn(),
  isDragHandled: vi.fn(() => false),
  setReorderCallback: vi.fn(),
}));
vi.mock("./store.js", async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  ...(await import("./__test-helpers__/store-mock.js")).storeMock,
  ...(await import("./__test-helpers__/tab-switch-mocks.js")).storePointerOverrides(),
}));
vi.mock("./run-store.js", async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  runLabelOf: vi.fn(() => ""),
}));
vi.mock("./composer-state.js", async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  ...(await import("./__test-helpers__/tab-switch-mocks.js").then((m) => m.composerStateMock())),
}));
vi.mock("./context-menu.js", async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  showContextMenu: vi.fn(),
}));
vi.mock("./chat-export.js", async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  downloadChatExport: vi.fn(),
}));
vi.mock("./toast.js", async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  ...(await import("./__test-helpers__/toast-mock.js").then((m) => m.toastMock())),
}));
vi.mock("./transport.js", async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  ...(await import("./__test-helpers__/tabs-server.js").then((m) => m.tabTransportMock())),
}));
vi.mock("./api-client.js", async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  ...(await import("./__test-helpers__/tabs-server.js").then((m) => ({
    apiBlobOrError: vi.fn(),
    apiGetTyped: m.tabListRead(),
    apiGet: vi.fn(() => Promise.resolve(null)),
  }))),
}));
import { getActiveTabId } from "./tabs.js";
import { releaseCover } from "./transcript-cover.js";
import { fx, installSwitchSuite, strip } from "./__test-helpers__/tab-switch-suite.js";
import { until } from "./__test-helpers__/until.js";
import { centreOf, queueClicks } from "./__test-helpers__/queued-clicks.js";
import { coverUp, pointers, rowEl } from "./__test-helpers__/tab-switch-mocks.js";

installSwitchSuite();

/** Long enough that a press sent `GAP_MS` after the previous release lands inside this show, and
 *  short enough that its release, `HOLD_MS` later, lands after it. */
const SHOW_MS = 300;
const GAP_MS = 200;
const HOLD_MS = 200;
/** A sent click dispatches in the task after the paint that freed the main thread, and the frame
 *  after it is covered: about a frame of input latency. Main-thread work while the tab it leaves
 *  shows uncovered, such as a view's show, runs past it. */
const STALE_MS = 100;

/** Hold the main thread, as a heavy chat's show does. */
function busy(ms: number): void {
  const end = performance.now() + ms;
  while (performance.now() < end) {
    // spin: input sent meanwhile waits in the queue
  }
}

interface Sample {
  /** When the frame's callbacks ran, just before it paints. The frame's own timestamp is its
   *  begin, which a long task in between pushes the paint well past. */
  readonly at: number;
  readonly up: boolean;
  readonly painted: string;
  readonly active: string;
}

/** What each frame paints, read in its animation-frame callback, as the visual harness reads it. */
function sampleFrames(): { readonly samples: Sample[]; stop(): void } {
  const samples: Sample[] = [];
  let on = true;
  const tick = (): void => {
    if (on) {
      samples.push({
        at: performance.now(),
        up: coverUp(),
        painted: pointers.painted,
        active: getActiveTabId(),
      });
      requestAnimationFrame(tick);
    }
  };
  requestAnimationFrame(tick);
  return {
    samples,
    stop: () => {
      on = false;
    },
  };
}

/** A click's release as the page received it: `at` is its event time, so a release that waited
 *  behind a long task still reads when it was sent. */
interface Sent {
  readonly at: number;
  readonly ref: string;
}

/** Record each trusted release, in order, as the click on the matching ref of `refs`. */
function recordReleases(refs: readonly string[]): { readonly sent: Sent[]; stop(): void } {
  const sent: Sent[] = [];
  const onUp = (e: PointerEvent): void => {
    const ref = refs[sent.length];
    if (e.isTrusted && ref !== undefined) {
      sent.push({ at: e.timeStamp, ref });
    }
  };
  window.addEventListener("pointerup", onUp, { capture: true });
  return {
    sent,
    stop: () => {
      window.removeEventListener("pointerup", onUp, { capture: true });
    },
  };
}

/** Milliseconds each chat showed uncovered while a click for another tab had been sent. A frame
 *  stays on screen until the next one, and the tab requested at an instant is the last one sent. */
function staleWindows(samples: readonly Sample[], sent: readonly Sent[]): Record<string, number> {
  const out: Record<string, number> = {};
  for (let i = 0; i + 1 < samples.length; i++) {
    const s = samples[i];
    const next = samples[i + 1];
    if (s === undefined || next === undefined || s.up) {
      continue;
    }
    for (let k = 0; k < sent.length; k++) {
      const req = sent[k];
      if (req === undefined || req.ref === s.painted) {
        continue;
      }
      const until = sent[k + 1]?.at ?? Infinity;
      const overlap = Math.min(next.at, until) - Math.max(s.at, req.at);
      if (overlap > 0) {
        out[s.painted] = (out[s.painted] ?? 0) + overlap;
      }
    }
  }
  return out;
}

/** The row's name: a point on the row that is not its close button. */
function nameOf(id: string): Element {
  const name = rowEl(id).querySelector(".tab-name");
  if (name === null) {
    throw new Error(`row ${id} has no name`);
  }
  return name;
}

function callsFor(spy: { mock: { calls: unknown[][] } }, id: string): number {
  return spy.mock.calls.filter((c) => c[0] === id).length;
}

describe("a burst of real clicks queued behind each show", () => {
  it("paints no superseded tab's content between the first click and D", async () => {
    const ids = await strip("a", "b", "c", "d");
    // chat.ts's show: the view's work, then the paint, whose last write releases the cover.
    fx.chat.show.mockImplementation((id: string) => {
      if (id !== "d") {
        busy(SHOW_MS);
      }
      pointers.painted = id;
      releaseCover(id);
    });
    // The chat being left keeps streaming: a repaint of it lands before the next frame, and a
    // paint's last write releases for its chat.
    fx.chat.select.mockImplementation((id: string) => {
      const leaving = pointers.painted;
      pointers.active = id;
      queueMicrotask(() => {
        releaseCover(leaving);
      });
    });
    const refs = ["b", "c", "d"];
    const releases = recordReleases(refs);
    const frames = sampleFrames();

    await queueClicks(
      refs.map((r) => centreOf(nameOf(ids[r] ?? ""))),
      GAP_MS,
      HOLD_MS,
    );
    await until(() => pointers.painted === "d" && !coverUp(), "D painted with the cover down", {
      timeout: 5000,
      detail: () => `painted ${pointers.painted}, cover up ${String(coverUp())}`,
    });
    frames.stop();
    releases.stop();

    // The premise: B's and C's shows ran, so C's and D's presses each waited behind one.
    expect(callsFor(fx.chat.show, "b"), "B was never shown").toBe(1);
    expect(callsFor(fx.chat.show, "c"), "C was never shown").toBe(1);
    expect(
      releases.sent.map((r) => r.ref),
      "a release went unrecorded",
    ).toEqual(refs);
    const leaked = frames.samples.filter((s) => !s.up && ids[s.painted] !== s.active);
    expect(leaked, "frames showing a superseded tab's content uncovered").toEqual([]);
    expect(
      frames.samples.some((s) => s.up),
      "no frame showed the cover",
    ).toBe(true);
    const stale = Object.entries(staleWindows(frames.samples, releases.sent)).filter(
      ([, ms]) => ms > STALE_MS,
    );
    expect(stale, "ms a tab showed uncovered after a click left it").toEqual([]);
    expect(getActiveTabId()).toBe(ids["d"]);
    expect(fx.notices).toEqual([]);
  });
});
