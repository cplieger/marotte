// A burst of real clicks, each pressed while the previous target's view is still being shown and
// released after it, keeps the one cover up until the last target paints: no frame shows a
// superseded tab's content, also while a press waits for its release. Real tabs.ts and
// transcript-cover.ts against the fake tab server.
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

/** Hold the main thread, as a heavy chat's show does. */
function busy(ms: number): void {
  const end = performance.now() + ms;
  while (performance.now() < end) {
    // spin: input sent meanwhile waits in the queue
  }
}

interface Sample {
  readonly up: boolean;
  readonly painted: string;
}

/** What each frame paints, read in its animation-frame callback, as the visual harness reads it. */
function sampleFrames(): { readonly samples: Sample[]; stop(): void } {
  const samples: Sample[] = [];
  let on = true;
  const tick = (): void => {
    if (on) {
      samples.push({ up: coverUp(), painted: pointers.painted });
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
    const frames = sampleFrames();

    await queueClicks(
      ["b", "c", "d"].map((r) => centreOf(nameOf(ids[r] ?? ""))),
      GAP_MS,
      HOLD_MS,
    );
    await until(() => pointers.painted === "d" && !coverUp(), "D painted with the cover down", {
      timeout: 5000,
      detail: () => `painted ${pointers.painted}, cover up ${String(coverUp())}`,
    });
    frames.stop();

    // The premise: B's and C's shows ran, so C's and D's presses each waited behind one.
    expect(callsFor(fx.chat.show, "b"), "B was never shown").toBe(1);
    expect(callsFor(fx.chat.show, "c"), "C was never shown").toBe(1);
    const firstUp = frames.samples.findIndex((s) => s.up);
    expect(firstUp, "no frame showed the cover").toBeGreaterThanOrEqual(0);
    const leaked = frames.samples.slice(firstUp).filter((s) => !s.up && s.painted !== "d");
    expect(leaked, "frames showing a superseded tab's content uncovered").toEqual([]);
    expect(getActiveTabId()).toBe(ids["d"]);
    expect(fx.notices).toEqual([]);
  });
});
