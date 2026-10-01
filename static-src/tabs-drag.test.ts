// The strip's reorder drag: what may START one, and every path that has to END one.
//
// The reported defect is a keyboard dismissal starting a drag nobody asked for and
// leaving it stuck, so the two halves are tested apart. A LAYOUT SHIFT under a
// stationary pointer must be unreadable as travel, whichever event delivers it —
// the Pointer Events spec requires only BOUNDARY events for a layout change under a
// stationary uncaptured pointer, so the cases dispatch the move explicitly and
// assert the property rather than the trigger. And a live drag must be endable from
// every path, because the reported symptom is that further clicks made it worse.
//
// `visualViewport` is replaced by a fake carrying the two fields `viewport-frame.ts`
// reads and really recording its listeners (`shell-viewport.test.ts`'s shape). Every
// gesture states `buttons`, which the real ones carry and the constructor defaults
// to 0: a held button is one of the gates, and touch reports 1 while the contact is
// present (Pointer Events, the `buttons` table).
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";

import { isDragHandled, setReorderCallback, attachDrag, DRAG_THRESHOLD_PX } from "./tabs-drag.js";

const POINTER_ID = 7;
const ROW_H = 30;

type Listener = () => void;

interface FakeViewport {
  height: number;
  offsetTop: number;
  addEventListener(type: string, fn: Listener): void;
  removeEventListener(type: string, fn: Listener): void;
  fire(type: string): void;
}

function fakeViewport(height: number, offsetTop = 0): FakeViewport {
  const map = new Map<string, Set<Listener>>();
  return {
    height,
    offsetTop,
    addEventListener(type, fn): void {
      let set = map.get(type);
      if (set === undefined) {
        set = new Set();
        map.set(type, set);
      }
      set.add(fn);
    },
    removeEventListener(type, fn): void {
      map.get(type)?.delete(fn);
    },
    fire(type): void {
      for (const fn of [...(map.get(type) ?? [])]) {
        fn();
      }
    },
  };
}

/** Back the three pointer-capture methods with a Set so the capture-gated paths run
 *  under a synthetic gesture: `setPointerCapture` throws `NotFoundError` for a
 *  pointerId that is not a real active pointer, so the HARNESS adapts and the
 *  control never grows a try/catch for a test. `effort-slider.test.ts` and
 *  `shell.test.ts` are the precedents. */
function stubPointerCapture(el: HTMLElement): void {
  const captured = new Set<number>();
  el.setPointerCapture = (id: number): void => {
    captured.add(id);
  };
  el.releasePointerCapture = (id: number): void => {
    captured.delete(id);
  };
  el.hasPointerCapture = (id: number): boolean => captured.has(id);
}

function ptr(
  type: string,
  opts: { clientY?: number; buttons?: number; pointerType?: string; pointerId?: number } = {},
): PointerEvent {
  return new PointerEvent(type, {
    bubbles: true,
    cancelable: true,
    isPrimary: true,
    pointerId: opts.pointerId ?? POINTER_ID,
    pointerType: opts.pointerType ?? "mouse",
    clientY: opts.clientY ?? 0,
    buttons: opts.buttons ?? 0,
  });
}

let list: HTMLElement;
let rows: Record<string, HTMLElement>;
let reorder: ReturnType<typeof vi.fn>;
let vv: FakeViewport;

/** Four rows: three top-level plus `a`'s sub-tab, so the fold that a drag applies
 *  to a child and the top-level-ids-only read-back are both real rather than
 *  vacuous. Real boxes, because the drop target is computed from rects. */
function buildStrip(): void {
  list = document.createElement("div");
  list.id = "tab-list";
  list.style.cssText = "position:absolute;top:0;left:0;width:200px";
  rows = {};
  for (const [id, child] of [
    ["a", false],
    ["s", true],
    ["b", false],
    ["c", false],
  ] as const) {
    const row = document.createElement("div");
    row.dataset["tabId"] = id;
    row.className = child ? "tab tab-child" : "tab";
    row.style.cssText = `display:block;height:${ROW_H}px`;
    list.appendChild(row);
    rows[id] = row;
    attachDrag(row);
    stubPointerCapture(row);
  }
  document.body.appendChild(list);
}

const domOrder = (): string[] =>
  [...list.querySelectorAll<HTMLElement>("[data-tab-id]")].map((r) => r.dataset["tabId"] ?? "");

const mid = (row: HTMLElement): number => {
  const r = row.getBoundingClientRect();
  return r.top + r.height / 2;
};

const ghost = (): HTMLElement | null => document.querySelector(".tab-drag-ghost");

/** Nothing of a drag survives: no ghost, no indicator, no marked row, no held
 *  selection, no folded child, and the strip in `order`. */
function expectStripQuiet(order: string[]): void {
  expect(ghost(), "ghost").toBeNull();
  expect(document.querySelector(".tab-drag-indicator"), "indicator").toBeNull();
  expect(document.querySelector(".tab-drag-placeholder"), "placeholder").toBeNull();
  expect(list.classList.contains("dragging"), "dragging class").toBe(false);
  expect(document.body.style.userSelect, "userSelect").toBe("");
  expect(list.querySelectorAll("[data-drag-collapsed]"), "folded children").toHaveLength(0);
  expect(domOrder()).toEqual(order);
}

/** A drag the reader really made: press on `row`, travel past the threshold with
 *  the button held. Asserts the drag is live, so a case built on it cannot pass by
 *  never having started one. */
function startRealDrag(row: HTMLElement, toY: number = mid(row) + DRAG_THRESHOLD_PX * 4): void {
  row.dispatchEvent(ptr("pointerdown", { clientY: mid(row), buttons: 1 }));
  row.dispatchEvent(ptr("pointermove", { clientY: toY, buttons: 1 }));
  expect(ghost(), "a real drag is live before the path under test fires").not.toBeNull();
}

beforeEach(() => {
  reorder = vi.fn();
  setReorderCallback(reorder as unknown as (order: string[]) => void);
  // Keyboard DOWN: the steady frame every case but the dismissal one starts from.
  vv = fakeViewport(700, 0);
  vi.stubGlobal("visualViewport", vv);
  buildStrip();
});

afterEach(async () => {
  // End a drag a case left live without setting `dragHandled`, then wait out the
  // 80ms window a RELEASE-shaped end holds it for, so no case inherits another's
  // click suppression. Browser Mode isolates per FILE, not per test.
  window.dispatchEvent(new Event("blur"));
  if (isDragHandled()) {
    await new Promise((r) => setTimeout(r, 100));
  }
  list.remove();
  document.getElementById("kbd-input")?.remove();
  document.body.style.userSelect = "";
  vi.unstubAllGlobals();
});

describe("what may start a drag", () => {
  it("isDragHandled() is false initially", () => {
    expect(isDragHandled()).toBe(false);
  });

  // THE REPORTED SEQUENCE. An input is focused, so the keyboard is up and the visual
  // viewport sits OFFSET inside the layout viewport; the mouse presses a row; the
  // blur dismisses the keyboard, the offset closes, and the row's box moves under a
  // pointer that never left the glass.
  it("starts no drag when a keyboard dismissal moves the layout under a still mouse", () => {
    const input = document.createElement("input");
    input.id = "kbd-input";
    document.body.appendChild(input);
    input.focus();
    vv.height = 400;
    vv.offsetTop = 300;

    const row = rows["b"] as HTMLElement;
    row.dispatchEvent(ptr("pointerdown", { clientY: 320, buttons: 1 }));

    // The dismissal: the frame closes and the reflow moves the row's own box. An
    // OWN property, never a prototype patch.
    vv.height = 700;
    vv.offsetTop = 0;
    row.getBoundingClientRect = (): DOMRect =>
      ({ top: 10, bottom: 10 + ROW_H, height: ROW_H, left: 0, width: 200 }) as DOMRect;
    vv.fire("resize");

    // The same point on the glass, reading 300px lower now the offset has gone.
    row.dispatchEvent(ptr("pointermove", { clientY: 20, buttons: 1 }));
    row.dispatchEvent(ptr("pointerup", { clientY: 20 }));

    expectStripQuiet(["a", "s", "b", "c"]);
    expect(reorder).not.toHaveBeenCalled();
    expect(isDragHandled()).toBe(false);
  });

  // NEGATIVE CONTROL. Without it every case in this file passes with startDrag's
  // body emptied.
  it("still reorders on a drag the reader made, and folds the child while it runs", () => {
    const row = rows["b"] as HTMLElement;
    const aboveA = mid(rows["a"] as HTMLElement) - 1;
    // Two moves, as a real drag is: one crosses the threshold, the next steers.
    row.dispatchEvent(ptr("pointerdown", { clientY: mid(row), buttons: 1 }));
    row.dispatchEvent(
      ptr("pointermove", { clientY: mid(row) - DRAG_THRESHOLD_PX - 1, buttons: 1 }),
    );

    expect(ghost(), "a ghost tracks the row while the drag runs").not.toBeNull();
    expect(rows["s"]?.hasAttribute("data-drag-collapsed"), "the child folds").toBe(true);

    row.dispatchEvent(ptr("pointermove", { clientY: aboveA, buttons: 1 }));
    row.dispatchEvent(ptr("pointerup", { clientY: aboveA }));

    expectStripQuiet(["b", "a", "s", "c"]);
    // Top-level ids only: what the tab store persists.
    expect(reorder.mock.calls).toEqual([[["b", "a", "c"]]]);
  });

  // A release the page never received: the button came up outside the window, or the
  // platform swallowed it. The arm survives that, so the HELD BUTTON is the only
  // thing left to tell a later hover from a drag — and no reflow can fake one.
  it("starts no drag from a hover move when the release was never delivered", () => {
    const row = rows["b"] as HTMLElement;
    row.dispatchEvent(ptr("pointerdown", { clientY: mid(row), buttons: 1 }));
    row.dispatchEvent(ptr("pointermove", { clientY: mid(row) + 40, buttons: 0 }));

    expectStripQuiet(["a", "s", "b", "c"]);
    expect(reorder).not.toHaveBeenCalled();
  });

  // A release the page DID see disarms whatever it landed on, so no later move can
  // resume that press.
  it("clears the arm on a release that landed off the row", () => {
    const row = rows["b"] as HTMLElement;
    row.dispatchEvent(ptr("pointerdown", { clientY: mid(row), buttons: 1 }));
    document.body.dispatchEvent(ptr("pointerup", { clientY: 500 }));
    row.dispatchEvent(ptr("pointermove", { clientY: mid(row) + 40, buttons: 1 }));

    expectStripQuiet(["a", "s", "b", "c"]);
    expect(reorder).not.toHaveBeenCalled();
  });

  it("starts no drag from a move below the threshold", () => {
    const row = rows["b"] as HTMLElement;
    row.dispatchEvent(ptr("pointerdown", { clientY: 100, buttons: 1 }));
    row.dispatchEvent(ptr("pointermove", { clientY: 100 + DRAG_THRESHOLD_PX, buttons: 1 }));
    row.dispatchEvent(ptr("pointerup", { clientY: 100 + DRAG_THRESHOLD_PX }));

    expectStripQuiet(["a", "s", "b", "c"]);
    expect(isDragHandled()).toBe(false);
  });

  it("does not arm from a press that started on the ×", () => {
    const row = rows["b"] as HTMLElement;
    const close = document.createElement("span");
    close.className = "tab-close";
    row.appendChild(close);

    close.dispatchEvent(ptr("pointerdown", { clientY: mid(row), buttons: 1 }));
    row.dispatchEvent(ptr("pointermove", { clientY: mid(row) + 200, buttons: 1 }));

    expectStripQuiet(["a", "s", "b", "c"]);
  });
});

describe("the touch hold", () => {
  beforeEach(() => {
    vi.useFakeTimers();
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  it("starts a drag once the hold completes", () => {
    const row = rows["b"] as HTMLElement;
    row.dispatchEvent(ptr("pointerdown", { clientY: mid(row), buttons: 1, pointerType: "touch" }));
    expect(ghost(), "nothing before the hold elapses").toBeNull();
    vi.advanceTimersByTime(300);
    expect(ghost(), "the hold starts the drag").not.toBeNull();
  });

  it("does not start one when the press is released first", () => {
    const row = rows["b"] as HTMLElement;
    row.dispatchEvent(ptr("pointerdown", { clientY: mid(row), buttons: 1, pointerType: "touch" }));
    vi.advanceTimersByTime(100);
    row.dispatchEvent(ptr("pointerup", { clientY: mid(row), pointerType: "touch" }));
    vi.advanceTimersByTime(300);
    expect(ghost()).toBeNull();
  });

  // A hold whose baseline moved cannot be verified, so it asks for a fresh press
  // rather than starting a drag from a coordinate the reflow supplied.
  it("drops the hold when the viewport moved under it", () => {
    const row = rows["b"] as HTMLElement;
    row.dispatchEvent(ptr("pointerdown", { clientY: mid(row), buttons: 1, pointerType: "touch" }));
    vv.height = 400;
    vv.offsetTop = 300;
    vv.fire("resize");
    vi.advanceTimersByTime(300);
    expect(ghost()).toBeNull();
    expect(reorder).not.toHaveBeenCalled();
  });
});

// Every one of these has to leave the strip in a settled state, because the report
// is that the stuck drag got WORSE with each further click. A cancel additionally
// dispatches no order: `reorderTabs` is a server mutation.
describe("ending a live drag", () => {
  const START = ["a", "s", "b", "c"];

  it("commits on a pointerup anywhere, not only on the dragged row", () => {
    startRealDrag(rows["b"] as HTMLElement);
    document.body.dispatchEvent(ptr("pointerup", { clientY: 500 }));

    expect(reorder).toHaveBeenCalledTimes(1);
    expect(isDragHandled(), "the release that ended the gesture is swallowed").toBe(true);
    expect(ghost()).toBeNull();
  });

  it.each([
    {
      desc: "a pointercancel on the window",
      fire: (): void => {
        window.dispatchEvent(ptr("pointercancel", { clientY: 500 }));
      },
      handled: true,
    },
    {
      // Reaching this listener with a live drag means capture was lost with no
      // release (the row left the document), so there is nothing to swallow.
      desc: "a lostpointercapture on the dragged row",
      fire: (): void => {
        rows["b"]?.dispatchEvent(new PointerEvent("lostpointercapture", { bubbles: true }));
      },
      handled: false,
    },
    {
      desc: "Escape",
      fire: (): void => {
        window.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true }));
      },
      handled: false,
    },
    {
      desc: "the window losing focus",
      fire: (): void => {
        window.dispatchEvent(new Event("blur"));
      },
      handled: false,
    },
    {
      desc: "the page being hidden",
      fire: (): void => {
        vi.spyOn(document, "visibilityState", "get").mockReturnValue("hidden");
        document.dispatchEvent(new Event("visibilitychange"));
      },
      handled: false,
    },
  ])("cancels on $desc, committing nothing", ({ fire, handled }) => {
    startRealDrag(rows["b"] as HTMLElement);
    fire();

    expectStripQuiet(START);
    expect(reorder, "a cancel sends no order").not.toHaveBeenCalled();
    // A cancel with no release of its own must not swallow the NEXT gesture's,
    // which is the "breaks further" half of the report.
    expect(isDragHandled()).toBe(handled);
  });

  it("cancels on a fresh pointerdown, leaving the new row free to activate", () => {
    startRealDrag(rows["b"] as HTMLElement);
    (rows["c"] as HTMLElement).dispatchEvent(
      ptr("pointerdown", { clientY: mid(rows["c"] as HTMLElement), buttons: 1 }),
    );

    expectStripQuiet(START);
    expect(reorder).not.toHaveBeenCalled();
    expect(isDragHandled(), "the new row's own release is not suppressed").toBe(false);
  });

  it("cancels into the pre-drag state when the viewport resizes mid-gesture", () => {
    startRealDrag(rows["b"] as HTMLElement);
    vv.height = 400;
    vv.offsetTop = 300;
    vv.fire("resize");

    expectStripQuiet(START);
    expect(reorder).not.toHaveBeenCalled();
    expect(isDragHandled()).toBe(false);
  });

  // The subscription is a TRIGGER, not a decision: `visualViewport` fires `scroll`
  // on an ordinary page scroll with the geometry unchanged.
  it("survives a viewport event that moved nothing", () => {
    startRealDrag(rows["b"] as HTMLElement);
    vv.fire("scroll");
    window.dispatchEvent(new Event("resize"));

    expect(ghost(), "the drag is still live").not.toBeNull();
    expect(reorder).not.toHaveBeenCalled();
  });

  // `setPointerCapture` throws `NotFoundError` for a pointer that is no longer
  // active, so it is asked for LAST: a throw has to leave a drag the window
  // listeners can still end rather than a half-built one. The stub ends the drag
  // from inside that call, which is only possible if the paths are already wired.
  it("wires every recovery path before it asks for pointer capture", () => {
    const row = rows["b"] as HTMLElement;
    let endableAtCaptureTime = false;
    row.setPointerCapture = (): void => {
      window.dispatchEvent(new Event("blur"));
      endableAtCaptureTime = ghost() === null;
    };
    row.dispatchEvent(ptr("pointerdown", { clientY: mid(row), buttons: 1 }));
    row.dispatchEvent(ptr("pointermove", { clientY: mid(row) + 40, buttons: 1 }));

    expect(endableAtCaptureTime).toBe(true);
    expectStripQuiet(START);
  });

  it("is idempotent across paths that fire in one turn", () => {
    startRealDrag(rows["b"] as HTMLElement);
    window.dispatchEvent(new Event("blur"));
    vi.spyOn(document, "visibilityState", "get").mockReturnValue("hidden");
    document.dispatchEvent(new Event("visibilitychange"));
    window.dispatchEvent(ptr("pointercancel", { clientY: 500 }));

    expectStripQuiet(START);
    expect(reorder).not.toHaveBeenCalled();
    expect(isDragHandled(), "a spent drag is not re-ended by a later release").toBe(false);
  });
});
