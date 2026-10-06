// The strip's reorder drag: what may START one, every path that ENDS one, and the preview. A LAYOUT
// SHIFT under a still pointer must not read as travel (the move is dispatched explicitly: Pointer
// Events requires only boundary events for it). `visualViewport` is a fake recording listeners;
// every gesture states `buttons`. The harness's `reorder`/`reproject`/`tap` stand in for the
// projection.
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";

import {
  isDragHandled,
  dragOwnsStrip,
  setReorderCallback,
  setReprojectCallback,
  setTapCallback,
  attachDrag,
  pointerDragActivation,
  exceedsSlop,
  noteRestSample,
  TOUCH_DRAG,
  PEN_DRAG,
  MOUSE_DRAG,
  REORDER_STILL_MS,
  REORDER_REST_MS,
  REORDER_MOVE_EPS_PX,
  REORDER_SHIFT_TRANS,
  REORDER_SETTLE_MS,
  REORDER_SLOT_FADE_MS,
} from "./tabs-drag.js";
import { announce } from "@cplieger/ui-primitives/announce";
import shellCss from "./css/10-shell-app.css?raw";

vi.mock("@cplieger/ui-primitives/announce", () => ({ announce: vi.fn() }));

const MOUSE_SLOP = MOUSE_DRAG.slopPx;
const TOUCH_HOLD = TOUCH_DRAG.holdMs;
const TOUCH_SLOP = TOUCH_DRAG.slopPx;

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

/** Back the pointer-capture methods with a Set: `setPointerCapture` throws `NotFoundError` for a
 *  synthetic pointer, so the harness adapts, not the control. */
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
  opts: {
    clientX?: number;
    clientY?: number;
    buttons?: number;
    pointerType?: string;
    pointerId?: number;
    isPrimary?: boolean;
  } = {},
): PointerEvent {
  return new PointerEvent(type, {
    bubbles: true,
    cancelable: true,
    isPrimary: opts.isPrimary ?? true,
    pointerId: opts.pointerId ?? POINTER_ID,
    pointerType: opts.pointerType ?? "mouse",
    clientX: opts.clientX ?? 0,
    clientY: opts.clientY ?? 0,
    buttons: opts.buttons ?? 0,
  });
}

const START = ["a", "s", "b", "c"];

let list: HTMLElement;
let rows: Record<string, HTMLElement>;
let committed: string[];
let reorder: ReturnType<typeof vi.fn>;
let reproject: ReturnType<typeof vi.fn>;
let tap: ReturnType<typeof vi.fn>;
let vv: FakeViewport;

/** Four rows: three top-level plus `a`'s sub-tab, so the fold that a drag applies
 *  to a child and the top-level-ids-only read-back are both real rather than
 *  vacuous. Real boxes, because the drop target is computed from layout. */
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
    row.style.cssText = `display:block;height:${String(ROW_H)}px`;
    const name = document.createElement("span");
    name.className = "tab-name";
    name.textContent = id;
    row.appendChild(name);
    list.appendChild(row);
    rows[id] = row;
    attachDrag(row);
    stubPointerCapture(row);
  }
  stubPointerCapture(list);
  document.body.appendChild(list);
}

/** `renderDOM`'s re-seat: ascending, and a row already in place is not touched. */
function harnessReproject(): void {
  committed.forEach((id, i) => {
    const row = rows[id] as HTMLElement;
    if (list.children[i] !== row) {
      list.insertBefore(row, list.children[i] ?? null);
    }
  });
}

const domOrder = (): string[] =>
  [...list.querySelectorAll<HTMLElement>("[data-tab-id]")].map((r) => r.dataset["tabId"] ?? "");

const mid = (row: HTMLElement): number => {
  const r = row.getBoundingClientRect();
  return r.top + r.height / 2;
};

const ghost = (): HTMLElement | null => document.querySelector(".tab-drag-ghost");

/** The strip holds its rows and nothing else: there is no slot element. */
function expectOnlyRows(): void {
  expect([...list.children].every((c) => (c as HTMLElement).dataset["tabId"] !== undefined)).toBe(
    true,
  );
  expect(list.children).toHaveLength(4);
}

/** Nothing of a drag survives: no ghost, no row marked as the slot, no held
 *  selection, no folded child, and the strip in `order`. */
function expectStripQuiet(order: string[]): void {
  expect(ghost(), "ghost").toBeNull();
  expect(list.querySelectorAll(".dragging"), "a row still marked as the slot").toHaveLength(0);
  expect(document.body.classList.contains("tab-dragging"), "body selection lock").toBe(false);
  expect(list.querySelectorAll("[data-drag-collapsed]"), "folded children").toHaveLength(0);
  expect(dragOwnsStrip(), "the drag still owns the strip").toBe(false);
  expectOnlyRows();
  expect(domOrder()).toEqual(order);
}

/** A drag the reader really made: press on `row`, travel past the threshold with
 *  the button held, far enough by default to cross a neighbour. Asserts the drag is
 *  live, so a case built on it cannot pass by never having started one. */
function startRealDrag(row: HTMLElement, toY: number = mid(row) + ROW_H * 2): void {
  row.dispatchEvent(ptr("pointerdown", { clientY: mid(row), buttons: 1 }));
  row.dispatchEvent(ptr("pointermove", { clientY: toY, buttons: 1 }));
  expect(ghost(), "a real drag is live before the path under test fires").not.toBeNull();
}

beforeEach(() => {
  vi.mocked(announce).mockClear();
  committed = [...START];
  reorder = vi.fn((order: string[]): readonly string[] | null => {
    committed = order.flatMap((id) => (id === "a" ? ["a", "s"] : [id]));
    return order;
  });
  reproject = vi.fn(harnessReproject);
  tap = vi.fn();
  setReorderCallback(reorder as unknown as (order: string[]) => readonly string[] | null);
  setReprojectCallback(reproject as unknown as () => void);
  setTapCallback(tap as unknown as (tabID: string) => void);
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
  const holder = list.parentElement;
  list.remove();
  if (holder !== document.body) {
    holder?.remove();
  }
  document.getElementById("kbd-input")?.remove();
  document.body.classList.remove("tab-dragging");
  vi.unstubAllGlobals();
});

describe("what may start a drag", () => {
  it("isDragHandled() is false initially", () => {
    expect(isDragHandled()).toBe(false);
  });

  // The keyboard's blur closes the visual viewport offset, moving the row under a still mouse.
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

    expectStripQuiet(START);
    expect(reorder).not.toHaveBeenCalled();
    expect(isDragHandled()).toBe(false);
  });

  // The press is CANCELLED, never re-based: a re-based press lifts a row the reader
  // may no longer be pointing at. Nothing fires the viewport event here, so the move
  // handler is the only thing that can notice.
  it("needs a fresh press after the viewport moved: travel after the reflow starts no drag", () => {
    const row = rows["b"] as HTMLElement;
    row.dispatchEvent(ptr("pointerdown", { clientY: mid(row), buttons: 1 }));
    vv.height = 400;
    vv.offsetTop = 300;
    row.dispatchEvent(ptr("pointermove", { clientY: mid(row) + 1, buttons: 1 }));
    row.dispatchEvent(ptr("pointermove", { clientY: mid(row) + 40, buttons: 1 }));

    expectStripQuiet(START);
    expect(reorder).not.toHaveBeenCalled();
  });

  // NEGATIVE CONTROL. Without it every case in this file passes with startDrag's
  // body emptied.
  it("still reorders on a drag the reader made, and folds the child while it runs", () => {
    const row = rows["b"] as HTMLElement;
    const aboveA = mid(rows["a"] as HTMLElement) - 1;
    // Two moves, as a real drag is: one crosses the threshold, the next steers.
    row.dispatchEvent(ptr("pointerdown", { clientY: mid(row), buttons: 1 }));
    row.dispatchEvent(ptr("pointermove", { clientY: mid(row) - MOUSE_SLOP - 1, buttons: 1 }));

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

    expectStripQuiet(START);
    expect(reorder).not.toHaveBeenCalled();
  });

  // A release the page DID see disarms whatever it landed on, so no later move can
  // resume that press.
  it("clears the arm on a release that landed off the row", () => {
    const row = rows["b"] as HTMLElement;
    row.dispatchEvent(ptr("pointerdown", { clientY: mid(row), buttons: 1 }));
    document.body.dispatchEvent(ptr("pointerup", { clientY: 500 }));
    row.dispatchEvent(ptr("pointermove", { clientY: mid(row) + 40, buttons: 1 }));

    expectStripQuiet(START);
    expect(reorder).not.toHaveBeenCalled();
  });

  it("starts no drag from a move below the threshold", () => {
    const row = rows["b"] as HTMLElement;
    row.dispatchEvent(ptr("pointerdown", { clientY: 100, buttons: 1 }));
    row.dispatchEvent(ptr("pointermove", { clientY: 100 + MOUSE_SLOP, buttons: 1 }));
    row.dispatchEvent(ptr("pointerup", { clientY: 100 + MOUSE_SLOP }));

    expectStripQuiet(START);
    expect(isDragHandled()).toBe(false);
  });

  it("does not arm from a press that started on the ×", () => {
    const row = rows["b"] as HTMLElement;
    const close = document.createElement("span");
    close.className = "tab-close";
    row.appendChild(close);

    close.dispatchEvent(ptr("pointerdown", { clientY: mid(row), buttons: 1 }));
    row.dispatchEvent(ptr("pointermove", { clientY: mid(row) + 200, buttons: 1 }));

    expectStripQuiet(START);
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
    vi.advanceTimersByTime(TOUCH_HOLD);
    expect(ghost(), "the hold starts the drag").not.toBeNull();
  });

  it("does not start one when the press is released first", () => {
    const row = rows["b"] as HTMLElement;
    row.dispatchEvent(ptr("pointerdown", { clientY: mid(row), buttons: 1, pointerType: "touch" }));
    vi.advanceTimersByTime(100);
    row.dispatchEvent(ptr("pointerup", { clientY: mid(row), pointerType: "touch" }));
    vi.advanceTimersByTime(TOUCH_HOLD);
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
    vi.advanceTimersByTime(TOUCH_HOLD);
    expect(ghost()).toBeNull();
    expect(reorder).not.toHaveBeenCalled();
  });

  // The viewport event is a trigger, and the geometry decides: `visualViewport`
  // fires `scroll` on an ordinary page scroll with nothing moved.
  it("keeps the hold through a viewport event that moved nothing", () => {
    const row = rows["b"] as HTMLElement;
    row.dispatchEvent(ptr("pointerdown", { clientY: mid(row), buttons: 1, pointerType: "touch" }));
    vv.fire("scroll");
    window.dispatchEvent(new Event("resize"));
    vi.advanceTimersByTime(TOUCH_HOLD);
    expect(ghost()).not.toBeNull();
  });

  // The live drag is judged against the frame at the PRESS: the hold's timer can run
  // after the geometry moved and before its viewport event is delivered.
  it("ends the drag when a viewport move from before the lift is reported after it", () => {
    const row = rows["b"] as HTMLElement;
    row.dispatchEvent(ptr("pointerdown", { clientY: mid(row), buttons: 1, pointerType: "touch" }));
    vv.height = 400;
    vv.offsetTop = 300;
    vi.advanceTimersByTime(TOUCH_HOLD);
    expect(ghost(), "the hold lifted before the event").not.toBeNull();

    vv.fire("resize");

    expectStripQuiet(START);
    expect(reorder).not.toHaveBeenCalled();
  });

  it.each([
    {
      desc: "the window losing focus",
      fire: (): void => {
        window.dispatchEvent(new Event("blur"));
      },
    },
    {
      desc: "the page being hidden",
      fire: (): void => {
        vi.spyOn(document, "visibilityState", "get").mockReturnValue("hidden");
        document.dispatchEvent(new Event("visibilitychange"));
      },
    },
    {
      desc: "Escape",
      fire: (): void => {
        const key = new KeyboardEvent("keydown", { key: "Escape", cancelable: true });
        window.dispatchEvent(key);
        // No terminal sits under the sidebar, so the key is left to the page.
        expect(key.defaultPrevented).toBe(false);
      },
    },
  ])("drops the hold on $desc", ({ fire }) => {
    const row = rows["b"] as HTMLElement;
    row.dispatchEvent(ptr("pointerdown", { clientY: mid(row), buttons: 1, pointerType: "touch" }));
    fire();
    vi.advanceTimersByTime(TOUCH_HOLD);
    expect(ghost()).toBeNull();
    expect(dragOwnsStrip()).toBe(false);
  });

  // Bubble phase on purpose: no terminal sits under this strip, so an Escape a page
  // handler consumed is not the gesture's (web-terminal-ui's strip takes it in capture).
  it("leaves an Escape the page consumed to the page, held or lifted", () => {
    const row = rows["b"] as HTMLElement;
    list.addEventListener("keydown", (e) => {
      e.stopPropagation();
    });
    const escape = (): KeyboardEvent =>
      new KeyboardEvent("keydown", { key: "Escape", bubbles: true, cancelable: true });
    row.dispatchEvent(ptr("pointerdown", { clientY: mid(row), buttons: 1, pointerType: "touch" }));

    row.dispatchEvent(escape());
    vi.advanceTimersByTime(TOUCH_HOLD);
    expect(ghost(), "the hold survived the consumed Escape").not.toBeNull();

    row.dispatchEvent(escape());
    expect(ghost(), "the drag survived the consumed Escape").not.toBeNull();

    const unconsumed = escape();
    window.dispatchEvent(unconsumed);
    expect(ghost(), "an Escape that reaches the window still ends it").toBeNull();
    expect(unconsumed.defaultPrevented).toBe(false);
  });

  // A press landing during a momentum scroll of the strip must not lift.
  it("drops the hold when the strip scrolls under it", () => {
    const row = rows["b"] as HTMLElement;
    row.dispatchEvent(ptr("pointerdown", { clientY: mid(row), buttons: 1, pointerType: "touch" }));
    list.dispatchEvent(new Event("scroll"));
    vi.advanceTimersByTime(TOUCH_HOLD);
    expect(ghost()).toBeNull();
  });

  it("lifts at the hold and not a millisecond before", () => {
    const row = rows["b"] as HTMLElement;
    row.dispatchEvent(ptr("pointerdown", { clientY: mid(row), buttons: 1, pointerType: "touch" }));
    vi.advanceTimersByTime(TOUCH_HOLD - 1);
    expect(ghost()).toBeNull();
    vi.advanceTimersByTime(1);
    expect(ghost()).not.toBeNull();
  });

  // Travel during the hold is a scroll and stays the scroller's. A hold that ignored
  // travel would fire mid-swipe whenever the browser had not yet claimed the pan.
  it("gives a press that travels past the slop back to the scroller", () => {
    const row = rows["b"] as HTMLElement;
    row.dispatchEvent(ptr("pointerdown", { clientY: mid(row), buttons: 1, pointerType: "touch" }));
    row.dispatchEvent(
      ptr("pointermove", { clientY: mid(row) + TOUCH_SLOP + 1, buttons: 1, pointerType: "touch" }),
    );
    // Far past any hold, so the case cannot pass by the hold not having elapsed.
    vi.advanceTimersByTime(1000);
    expect(ghost()).toBeNull();
  });

  it("keeps the hold through a wobble at the slop", () => {
    const row = rows["b"] as HTMLElement;
    row.dispatchEvent(ptr("pointerdown", { clientY: mid(row), buttons: 1, pointerType: "touch" }));
    row.dispatchEvent(
      ptr("pointermove", { clientY: mid(row) + TOUCH_SLOP, buttons: 1, pointerType: "touch" }),
    );
    vi.advanceTimersByTime(TOUCH_HOLD);
    expect(ghost()).not.toBeNull();
  });

  it("drops the hold when a second finger lands", () => {
    const row = rows["b"] as HTMLElement;
    row.dispatchEvent(ptr("pointerdown", { clientY: mid(row), buttons: 1, pointerType: "touch" }));
    (rows["c"] as HTMLElement).dispatchEvent(
      ptr("pointerdown", {
        pointerId: POINTER_ID + 1,
        isPrimary: false,
        pointerType: "touch",
        buttons: 1,
      }),
    );
    vi.advanceTimersByTime(1000);
    expect(ghost()).toBeNull();
  });

  // A slow tap lifts the row; letting go without travel is still the tap. The lift
  // put the pointer capture on the list, so the browser sends that release to the
  // LIST, which is not on the row's propagation path.
  it("activates the row when a lifted hold is released without travel", () => {
    const row = rows["b"] as HTMLElement;
    const rowRelease = vi.fn();
    row.addEventListener("pointerup", rowRelease);
    row.dispatchEvent(ptr("pointerdown", { clientY: mid(row), buttons: 1, pointerType: "touch" }));
    vi.advanceTimersByTime(TOUCH_HOLD);
    expect(ghost()).not.toBeNull();
    list.dispatchEvent(ptr("pointerup", { clientY: mid(row) + TOUCH_SLOP, pointerType: "touch" }));

    expect(rowRelease, "the release never reached the row").not.toHaveBeenCalled();
    expect(tap.mock.calls).toEqual([["b"]]);
    expectStripQuiet(START);
    expect(reorder).not.toHaveBeenCalled();
    expect(isDragHandled()).toBe(false);
  });

  // With no capture the release lands on the row, whose own listener activates it;
  // the drag asking too would activate the tab twice.
  it("leaves a tap released on the row itself to the row", () => {
    const row = rows["b"] as HTMLElement;
    row.dispatchEvent(ptr("pointerdown", { clientY: mid(row), buttons: 1, pointerType: "touch" }));
    vi.advanceTimersByTime(TOUCH_HOLD);
    row.dispatchEvent(ptr("pointerup", { clientY: mid(row) + TOUCH_SLOP, pointerType: "touch" }));

    expect(tap).not.toHaveBeenCalled();
    expectStripQuiet(START);
    expect(isDragHandled()).toBe(false);
  });

  it("commits a lifted hold that travelled", () => {
    const row = rows["b"] as HTMLElement;
    row.dispatchEvent(ptr("pointerdown", { clientY: mid(row), buttons: 1, pointerType: "touch" }));
    vi.advanceTimersByTime(TOUCH_HOLD);
    const aboveA = mid(rows["a"] as HTMLElement) - 1;
    row.dispatchEvent(ptr("pointermove", { clientY: aboveA, buttons: 1, pointerType: "touch" }));
    row.dispatchEvent(ptr("pointerup", { clientY: aboveA, pointerType: "touch" }));

    expect(reorder.mock.calls).toEqual([[["b", "a", "c"]]]);
    expect(isDragHandled()).toBe(true);
    // The suppression window is a timer, and these timers are fake.
    vi.advanceTimersByTime(100);
  });

  // `touch-action` is decided at touchstart and pointer capture does not stop a
  // pan, so a lifted row's movement is kept from the scroller by cancelling it.
  it("cancels the native pan only once the row is lifted", () => {
    const row = rows["b"] as HTMLElement;
    row.dispatchEvent(ptr("pointerdown", { clientY: mid(row), buttons: 1, pointerType: "touch" }));
    const before = new Event("touchmove", { bubbles: true, cancelable: true });
    row.dispatchEvent(before);
    expect(before.defaultPrevented, "a pending press leaves the pan to the browser").toBe(false);

    vi.advanceTimersByTime(TOUCH_HOLD);
    const after = new Event("touchmove", { bubbles: true, cancelable: true });
    row.dispatchEvent(after);
    expect(after.defaultPrevented, "a lifted row owns the gesture").toBe(true);
  });

  it("yields an unmoved lift to the row's own long-press menu", () => {
    const row = rows["b"] as HTMLElement;
    const rowMenu = vi.fn();
    row.addEventListener("contextmenu", rowMenu);
    row.dispatchEvent(ptr("pointerdown", { clientY: mid(row), buttons: 1, pointerType: "touch" }));
    vi.advanceTimersByTime(TOUCH_HOLD);
    expect(ghost(), "the hold lifted the row").not.toBeNull();
    const menu = new MouseEvent("contextmenu", { bubbles: true, cancelable: true });
    row.dispatchEvent(menu);

    expect(rowMenu).toHaveBeenCalledTimes(1);
    expect(menu.defaultPrevented).toBe(false);
    expectStripQuiet(START);
    expect(isDragHandled()).toBe(false);
  });

  it("keeps a platform long-press menu from opening under a travelling row", () => {
    const row = rows["b"] as HTMLElement;
    const rowMenu = vi.fn();
    row.addEventListener("contextmenu", rowMenu);
    row.dispatchEvent(ptr("pointerdown", { clientY: mid(row), buttons: 1, pointerType: "touch" }));
    vi.advanceTimersByTime(TOUCH_HOLD);
    row.dispatchEvent(
      ptr("pointermove", { clientY: mid(row) + TOUCH_SLOP + 1, buttons: 1, pointerType: "touch" }),
    );
    const menu = new MouseEvent("contextmenu", { bubbles: true, cancelable: true });
    row.dispatchEvent(menu);

    expect(menu.defaultPrevented).toBe(true);
    expect(rowMenu).not.toHaveBeenCalled();
    expect(ghost(), "the drag carries on").not.toBeNull();
  });
});

// The activation contract every gesture in this file is measured against, and the
// reorder timings the preview runs on.
describe("activation per pointer type", () => {
  beforeEach(() => {
    vi.useFakeTimers();
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  it("holds touch and pen for 150ms within 8px, and starts a mouse on 5px of travel", () => {
    expect(TOUCH_DRAG).toEqual({ holdMs: 150, slopPx: 8 });
    expect(PEN_DRAG).toEqual({ holdMs: 150, slopPx: 8 });
    expect(MOUSE_DRAG).toEqual({ holdMs: 0, slopPx: 5 });
    expect(pointerDragActivation("touch")).toBe(TOUCH_DRAG);
    expect(pointerDragActivation("pen")).toBe(PEN_DRAG);
    expect(pointerDragActivation("mouse")).toBe(MOUSE_DRAG);
    // An unknown pointer takes the rules that cannot hijack a scroll.
    expect(pointerDragActivation("")).toBe(TOUCH_DRAG);
  });

  it("carries the reorder timings and the slide", () => {
    expect({
      REORDER_STILL_MS,
      REORDER_REST_MS,
      REORDER_MOVE_EPS_PX,
      REORDER_SHIFT_TRANS,
      REORDER_SETTLE_MS,
      REORDER_SLOT_FADE_MS,
    }).toEqual({
      REORDER_STILL_MS: 50,
      REORDER_REST_MS: 450,
      REORDER_MOVE_EPS_PX: 3,
      REORDER_SHIFT_TRANS: "translate 0.2s cubic-bezier(0.2, 0, 0, 1)",
      REORDER_SETTLE_MS: 300,
      REORDER_SLOT_FADE_MS: 300,
    });
  });

  it("measures travel as a strict Euclidean distance against the rule", () => {
    expect(exceedsSlop(5, 0, MOUSE_DRAG)).toBe(false);
    expect(exceedsSlop(4, 4, MOUSE_DRAG)).toBe(true);
    expect(exceedsSlop(8, 0, TOUCH_DRAG)).toBe(false);
    expect(exceedsSlop(0, -8, TOUCH_DRAG)).toBe(false);
    expect(exceedsSlop(6, 6, TOUCH_DRAG)).toBe(true);
  });

  it("starts a mouse drag on diagonal travel past the distance", () => {
    const row = rows["b"] as HTMLElement;
    row.dispatchEvent(ptr("pointerdown", { clientX: 10, clientY: mid(row), buttons: 1 }));
    row.dispatchEvent(ptr("pointermove", { clientX: 14, clientY: mid(row) + 4, buttons: 1 }));
    expect(ghost()).not.toBeNull();
  });

  it("never lifts a still mouse on time alone", () => {
    const row = rows["b"] as HTMLElement;
    row.dispatchEvent(ptr("pointerdown", { clientY: mid(row), buttons: 1 }));
    vi.advanceTimersByTime(1000);
    expect(ghost()).toBeNull();
  });

  it("gives a pen the touch hold rather than the mouse distance", () => {
    const row = rows["b"] as HTMLElement;
    row.dispatchEvent(ptr("pointerdown", { clientY: mid(row), buttons: 1, pointerType: "pen" }));
    row.dispatchEvent(
      ptr("pointermove", { clientY: mid(row) + MOUSE_SLOP + 1, buttons: 1, pointerType: "pen" }),
    );
    expect(ghost(), "travel a mouse would drag on").toBeNull();
    vi.advanceTimersByTime(PEN_DRAG.holdMs);
    expect(ghost(), "the hold lifts it").not.toBeNull();
  });

  it("gives an unknown pointer type the touch hold", () => {
    const row = rows["b"] as HTMLElement;
    row.dispatchEvent(ptr("pointerdown", { clientY: mid(row), buttons: 1, pointerType: "" }));
    vi.advanceTimersByTime(TOUCH_HOLD);
    expect(ghost()).not.toBeNull();
  });
});

describe("noteRestSample", () => {
  it("never calls the first sample still", () => {
    const rest = { at: null, movedAt: 0 };
    expect(noteRestSample(rest, 100, 1000)).toBe(false);
    expect(rest).toEqual({ at: 100, movedAt: 1000 });
  });

  it("calls a pointer still only once it has stayed within the tremor for 50ms", () => {
    const rest = { at: null, movedAt: 0 };
    noteRestSample(rest, 100, 1000);
    expect(noteRestSample(rest, 101, 1049)).toBe(false);
    expect(noteRestSample(rest, 101, 1050)).toBe(true);
  });

  it("restarts the clock on a move past the tremor", () => {
    const rest = { at: null, movedAt: 0 };
    noteRestSample(rest, 100, 1000);
    expect(noteRestSample(rest, 104, 1060)).toBe(false);
    expect(noteRestSample(rest, 104, 1100)).toBe(false);
    expect(noteRestSample(rest, 104, 1110)).toBe(true);
  });

  it("counts exactly the tremor as still", () => {
    const rest = { at: null, movedAt: 0 };
    noteRestSample(rest, 100, 1000);
    expect(noteRestSample(rest, 100 + REORDER_MOVE_EPS_PX, 1050)).toBe(true);
  });
});

describe("the strip a drag leaves behind", () => {
  it("starts a drag when a fast press leaves the row in one move", () => {
    const row = rows["b"] as HTMLElement;
    row.dispatchEvent(ptr("pointerdown", { clientY: mid(row), buttons: 1 }));
    // A mouse has no implicit capture, so the next move lands wherever it now is.
    document.body.dispatchEvent(ptr("pointermove", { clientY: mid(row) + 80, buttons: 1 }));
    expect(ghost()).not.toBeNull();
  });

  it("reports no order for a drop that crossed nothing", () => {
    const row = rows["b"] as HTMLElement;
    startRealDrag(row, mid(row) + MOUSE_SLOP + 1);
    document.body.dispatchEvent(ptr("pointerup", { clientY: mid(row) + MOUSE_SLOP + 1 }));

    expect(reorder).not.toHaveBeenCalled();
    expect(isDragHandled(), "it was still a drag's release").toBe(true);
  });

  it("keeps keyboard focus on the row it dropped", () => {
    const row = rows["b"] as HTMLElement;
    row.tabIndex = 0;
    row.focus();
    const aboveA = mid(rows["a"] as HTMLElement) - 1;
    startRealDrag(row, aboveA);
    document.body.dispatchEvent(ptr("pointerup", { clientY: aboveA }));

    expect(domOrder()).toEqual(["b", "a", "s", "c"]);
    expect(document.activeElement).toBe(row);
  });

  it("locks text selection across the page for the life of the drag", () => {
    startRealDrag(rows["b"] as HTMLElement);
    expect(document.body.classList.contains("tab-dragging")).toBe(true);
    // Safari through 27 parses only the prefixed form.
    const rule = /\.tab-dragging\s*\{([^}]*)\}/u.exec(shellCss)?.[1] ?? "";
    expect(rule).toMatch(/-webkit-user-select:\s*none/u);
    expect(rule).toMatch(/(^|[^-])user-select:\s*none/u);
  });

  it("holds the strip from the lift until the drag ends", () => {
    expect(dragOwnsStrip()).toBe(false);
    startRealDrag(rows["b"] as HTMLElement);
    expect(dragOwnsStrip()).toBe(true);
    window.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape" }));
    expect(dragOwnsStrip()).toBe(false);
  });

  it("is over before the projection hears about the drop", () => {
    let ownedAtCallback: boolean | null = null;
    reorder.mockImplementation((order: string[]) => {
      ownedAtCallback = dragOwnsStrip();
      committed = order.flatMap((id) => (id === "a" ? ["a", "s"] : [id]));
    });
    const row = rows["b"] as HTMLElement;
    const aboveA = mid(rows["a"] as HTMLElement) - 1;
    startRealDrag(row, aboveA);
    row.dispatchEvent(ptr("pointerup", { clientY: aboveA }));
    expect(ownedAtCallback).toBe(false);
  });
});

// The real stylesheet, because the slot's size, the rows' slide and the hit-testing
// are geometry it produces.
describe("the dragged row as the slot, against the real stylesheet", () => {
  let style: HTMLStyleElement;

  beforeEach(() => {
    vi.useFakeTimers();
    style = document.createElement("style");
    style.textContent = `:root { --btn-h: ${String(ROW_H)}px; --dur-micro: 100ms; --dur-enter: 250ms; --ease-standard: linear; --ease-enter: linear; } *, *::before, *::after { box-sizing: border-box; } ${shellCss}`;
    document.head.appendChild(style);
  });

  afterEach(() => {
    style.remove();
    vi.useRealTimers();
  });

  it.each(["mouse", "touch"])(
    "keeps the %s-dragged row at full height as the slot, with no slot element",
    (pointerType) => {
      const row = rows["b"] as HTMLElement;
      const height = row.offsetHeight;
      row.dispatchEvent(ptr("pointerdown", { clientY: mid(row), buttons: 1, pointerType }));
      vi.advanceTimersByTime(TOUCH_HOLD);
      row.dispatchEvent(ptr("pointermove", { clientY: 1, buttons: 1, pointerType }));
      expect(ghost()).not.toBeNull();

      expect(row.classList.contains("dragging")).toBe(true);
      expect(row.offsetHeight).toBe(height);
      expect(getComputedStyle(row).borderTopStyle).toBe("dashed");
      expect(getComputedStyle(row.querySelector(".tab-name") as HTMLElement).opacity).toBe("0");
      expectOnlyRows();

      vi.advanceTimersByTime(REORDER_STILL_MS);
      expect(domOrder()[0], "the row itself moved to the slot").toBe("b");
      expectOnlyRows();
    },
  );

  it("moves the dragged row, not a separate element, when the slot opens", () => {
    const row = rows["c"] as HTMLElement;
    startRealDrag(row, mid(rows["a"] as HTMLElement) - 1);
    expect(domOrder(), "sweeping moves nothing").toEqual(START);
    vi.advanceTimersByTime(REORDER_STILL_MS);
    expect(domOrder()).toEqual(["c", "a", "s", "b"]);
    expect(row.classList.contains("tab-slotted"), "the slot announces itself").toBe(true);
    vi.advanceTimersByTime(REORDER_SLOT_FADE_MS);
    expect(row.classList.contains("tab-slotted")).toBe(false);
  });

  // A slide's inline transition overrides the row's own, so every displaced row is
  // handed back to the stylesheet once the slide has had time to finish; and the
  // slide must actually run, which needs its inverted start committed first.
  it("slides every displaced row and hands each back after REORDER_SETTLE_MS", () => {
    const row = rows["c"] as HTMLElement;
    startRealDrag(row, mid(rows["a"] as HTMLElement) - 1);
    vi.advanceTimersByTime(REORDER_STILL_MS);
    for (const id of ["a", "b"]) {
      const r = rows[id] as HTMLElement;
      expect(r.style.translate, `${id} starts its slide`).not.toBe("");
      expect(r.style.transition, id).toBe(REORDER_SHIFT_TRANS);
    }
    vi.advanceTimersByTime(REORDER_SETTLE_MS);
    for (const c of list.children) {
      const r = c as HTMLElement;
      expect(r.style.transition, r.dataset["tabId"]).toBe("");
      expect(r.style.translate, r.dataset["tabId"]).toBe("");
    }
  });

  // A row mid-slide is DRAWN at its old slot; hit-testing that drawn rect lets the
  // preview's own motion move the target, and the slot flips back and forth.
  it("aims against layout offsets while a slide is drawing rows elsewhere", () => {
    const row = rows["c"] as HTMLElement;
    const b = rows["b"] as HTMLElement;
    const bMid = mid(b);
    startRealDrag(row, mid(row) - MOUSE_SLOP - 1);
    row.dispatchEvent(ptr("pointermove", { clientY: bMid - 5, buttons: 1 }));
    vi.advanceTimersByTime(REORDER_STILL_MS);
    expect(row.nextElementSibling, "the slot opened above b").toBe(b);

    // Inside the slot just opened, and below where b is still being drawn from.
    row.dispatchEvent(ptr("pointermove", { clientY: bMid + 5, buttons: 1 }));
    vi.advanceTimersByTime(REORDER_STILL_MS * 2);
    expect(row.nextElementSibling, "the slot stays put").toBe(b);
  });

  it("skips the slide under reduced motion", () => {
    vi.stubGlobal(
      "matchMedia",
      (q: string): MediaQueryList =>
        ({ matches: q.includes("prefers-reduced-motion"), media: q }) as MediaQueryList,
    );
    const row = rows["c"] as HTMLElement;
    startRealDrag(row, mid(rows["a"] as HTMLElement) - 1);
    vi.advanceTimersByTime(REORDER_STILL_MS);
    expect(domOrder()).toEqual(["c", "a", "s", "b"]);
    for (const c of list.children) {
      expect((c as HTMLElement).style.translate, (c as HTMLElement).dataset["tabId"]).toBe("");
    }
  });
});

// A moving pointer rearranges nothing, a stopped one opens the slot, and a release
// decides by position at once.
describe("sweep, rest, drop", () => {
  beforeEach(() => {
    vi.useFakeTimers();
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  const aboveA = (): number => mid(rows["a"] as HTMLElement) - 1;

  it("rearranges nothing while the pointer sweeps", () => {
    const row = rows["c"] as HTMLElement;
    startRealDrag(row, mid(row) - MOUSE_SLOP - 1);
    for (const y of [70, 50, 30, 10]) {
      row.dispatchEvent(ptr("pointermove", { clientY: y, buttons: 1 }));
      vi.advanceTimersByTime(REORDER_STILL_MS - 10);
    }
    expect(domOrder()).toEqual(START);
    expect(vi.mocked(announce)).not.toHaveBeenCalled();
  });

  // A still pointer sends no pointermove; the tick re-reports it.
  it("opens the slot at rest via the still tick, and says so", () => {
    const row = rows["c"] as HTMLElement;
    startRealDrag(row, mid(row) - MOUSE_SLOP - 1);
    row.dispatchEvent(ptr("pointermove", { clientY: aboveA(), buttons: 1 }));
    vi.advanceTimersByTime(REORDER_STILL_MS - 1);
    expect(row.nextElementSibling).not.toBe(rows["a"]);
    vi.advanceTimersByTime(1);
    expect(row.nextElementSibling).toBe(rows["a"]);
    expect(vi.mocked(announce)).toHaveBeenLastCalledWith("Drop position 1");
  });

  it("does not let a resting hand's tremor push the slot out", () => {
    const row = rows["c"] as HTMLElement;
    startRealDrag(row, mid(row) - MOUSE_SLOP - 1);
    row.dispatchEvent(ptr("pointermove", { clientY: aboveA(), buttons: 1 }));
    vi.advanceTimersByTime(REORDER_STILL_MS / 2);
    row.dispatchEvent(ptr("pointermove", { clientX: 2, clientY: aboveA() - 2, buttons: 1 }));
    vi.advanceTimersByTime(REORDER_STILL_MS / 2);
    expect(row.nextElementSibling).toBe(rows["a"]);
  });

  it("falls back to the rest net when the tick is starved", () => {
    const tick = vi
      .spyOn(globalThis, "setInterval")
      .mockImplementation(() => 0 as unknown as ReturnType<typeof setInterval>);
    const row = rows["c"] as HTMLElement;
    startRealDrag(row, aboveA());
    vi.advanceTimersByTime(REORDER_REST_MS - 1);
    expect(domOrder(), "nothing before the net").toEqual(START);
    vi.advanceTimersByTime(1);
    expect(row.nextElementSibling).toBe(rows["a"]);
    tick.mockRestore();
  });

  it("commits where the release lands without waiting for rest", () => {
    const row = rows["c"] as HTMLElement;
    startRealDrag(row, aboveA());
    row.dispatchEvent(ptr("pointerup", { clientY: aboveA() }));
    expect(reorder.mock.calls).toEqual([[["c", "a", "b"]]]);
    expect(vi.mocked(announce)).toHaveBeenLastCalledWith("Moved c to position 1");
    vi.advanceTimersByTime(100);
  });

  // The projection's pin partition can undo a drop: what it applied is what the
  // reader hears, and the rows slide back rather than jump.
  it("announces a drop the projection refused as cancelled and slides the row home", () => {
    reorder.mockImplementation((): readonly string[] | null => null);
    const row = rows["c"] as HTMLElement;
    startRealDrag(row, aboveA());
    vi.advanceTimersByTime(REORDER_STILL_MS);
    vi.advanceTimersByTime(REORDER_SETTLE_MS);
    expect(domOrder(), "the preview moved the row").toEqual(["c", "a", "s", "b"]);
    row.dispatchEvent(ptr("pointerup", { clientY: aboveA() }));

    expect(reorder).toHaveBeenCalledTimes(1);
    expect(vi.mocked(announce)).toHaveBeenLastCalledWith("Move cancelled");
    expectStripQuiet(START);
    expect(row.style.translate, "the row slides back from the slot").not.toBe("");
    vi.advanceTimersByTime(100);
  });

  it("announces the position the projection applied, not the previewed one", () => {
    reorder.mockImplementation((): readonly string[] | null => {
      committed = ["a", "s", "c", "b"];
      return ["a", "c", "b"];
    });
    const row = rows["c"] as HTMLElement;
    startRealDrag(row, aboveA());
    vi.advanceTimersByTime(REORDER_STILL_MS);
    vi.advanceTimersByTime(REORDER_SETTLE_MS);
    row.dispatchEvent(ptr("pointerup", { clientY: aboveA() }));

    expect(reorder.mock.calls).toEqual([[["c", "a", "b"]]]);
    expect(vi.mocked(announce)).toHaveBeenLastCalledWith("Moved c to position 2");
    expect(domOrder()).toEqual(["a", "s", "c", "b"]);
    expect(row.style.translate, "the row slides into its applied place").not.toBe("");
    vi.advanceTimersByTime(100);
  });

  // The release flushes: a rest net armed before it must not commit a slot after.
  it("flushes on release and leaves no rest net behind", () => {
    const row = rows["c"] as HTMLElement;
    startRealDrag(row, aboveA());
    row.dispatchEvent(ptr("pointermove", { clientY: mid(rows["b"] as HTMLElement), buttons: 1 }));
    row.dispatchEvent(ptr("pointerup", { clientY: aboveA() }));
    vi.advanceTimersByTime(REORDER_REST_MS * 2);
    expect(reorder.mock.calls).toEqual([[["c", "a", "b"]]]);
    expect(domOrder()).toEqual(["c", "a", "s", "b"]);
    expect(vi.mocked(announce)).toHaveBeenLastCalledWith("Moved c to position 1");
  });

  // An HTML5 drop outside its target is refused and the strip reverts; a release
  // over the transcript is the same gesture here.
  it("reverts a release outside the strip", () => {
    const row = rows["c"] as HTMLElement;
    startRealDrag(row, aboveA());
    vi.advanceTimersByTime(REORDER_STILL_MS);
    expect(domOrder(), "the preview moved the row").toEqual(["c", "a", "s", "b"]);
    document.body.dispatchEvent(ptr("pointerup", { clientX: 400, clientY: aboveA() }));

    expectStripQuiet(START);
    expect(reorder).not.toHaveBeenCalled();
    expect(vi.mocked(announce)).toHaveBeenLastCalledWith("Move cancelled");
    expect(isDragHandled(), "it was still a drag's release").toBe(true);
    vi.advanceTimersByTime(100);
  });

  it("re-projects the committed order on a cancel and slides the row home", () => {
    const row = rows["c"] as HTMLElement;
    startRealDrag(row, aboveA());
    vi.advanceTimersByTime(REORDER_STILL_MS);
    vi.advanceTimersByTime(REORDER_SETTLE_MS);
    reproject.mockClear();
    window.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape" }));

    expect(reproject).toHaveBeenCalledTimes(1);
    expectStripQuiet(START);
    expect(row.style.translate, "the row slides back from the slot").not.toBe("");
    expect(vi.mocked(announce)).toHaveBeenLastCalledWith("Move cancelled");
  });

  it("says nothing on a cancel that previewed no move", () => {
    startRealDrag(rows["b"] as HTMLElement, mid(rows["b"] as HTMLElement) + MOUSE_SLOP + 1);
    window.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape" }));
    expect(vi.mocked(announce)).not.toHaveBeenCalled();
  });

  // The preview moves the parent alone, so a parent taken down the strip and back
  // ends with its folded child in front of it: an unchanged drop still re-projects.
  it("re-seats a dragged parent's sub-tab behind it on an unchanged drop", () => {
    const row = rows["a"] as HTMLElement;
    startRealDrag(row, mid(row) + MOUSE_SLOP + 1);
    row.dispatchEvent(ptr("pointermove", { clientY: 100, buttons: 1 }));
    vi.advanceTimersByTime(REORDER_STILL_MS);
    expect(domOrder()).toEqual(["s", "b", "a", "c"]);
    row.dispatchEvent(ptr("pointermove", { clientY: 5, buttons: 1 }));
    vi.advanceTimersByTime(REORDER_STILL_MS);
    expect(domOrder(), "the parent is back, its child is not").toEqual(["s", "a", "b", "c"]);
    row.dispatchEvent(ptr("pointerup", { clientY: 5 }));

    expectStripQuiet(START);
    expect(reorder).not.toHaveBeenCalled();
    vi.advanceTimersByTime(100);
  });

  // Edge auto-scroll is not a thing this strip does; the reader scrolls the list, and
  // the next rest aims against where the rows are now.
  it("re-aims the slot at the next rest after a wheel scroll of the list", () => {
    list.style.height = `${String(ROW_H * 2)}px`;
    list.style.overflowY = "auto";
    const row = rows["a"] as HTMLElement;
    startRealDrag(row, mid(row) + MOUSE_SLOP + 1);
    vi.advanceTimersByTime(REORDER_STILL_MS * 2);
    expect(domOrder(), "still over its own slot").toEqual(START);

    list.scrollTop = ROW_H * 2;
    vi.advanceTimersByTime(REORDER_STILL_MS);
    expect(row.nextElementSibling, "the slot followed the scrolled rows").toBe(rows["c"]);
  });

  // `#tab-list` is unpositioned in the app, so a row's offsets are measured from an
  // ancestor and the list's own offset has to come off.
  it("hit-tests an unpositioned list through its offset parent", () => {
    const frame = document.createElement("div");
    frame.style.cssText = "position:absolute;top:0;left:0;width:200px;padding-top:40px";
    list.style.position = "static";
    document.body.appendChild(frame);
    frame.appendChild(list);
    const row = rows["c"] as HTMLElement;
    startRealDrag(row, mid(row) - MOUSE_SLOP - 1);
    // Below a's midpoint and above b's in the list's own frame: 40px off would
    // read it as above a.
    row.dispatchEvent(
      ptr("pointermove", { clientY: mid(rows["s"] as HTMLElement) + 5, buttons: 1 }),
    );
    vi.advanceTimersByTime(REORDER_STILL_MS);
    expect(row.nextElementSibling).toBe(rows["b"]);
  });
});

// Every one of these has to leave the strip in a settled state, because the report
// is that the stuck drag got WORSE with each further click. A cancel additionally
// dispatches no order: `reorderTabs` is a server mutation.
describe("ending a live drag", () => {
  it("commits a release over the strip that the dragged row never received", () => {
    startRealDrag(rows["b"] as HTMLElement);
    document.body.dispatchEvent(ptr("pointerup", { clientX: 10, clientY: 110 }));

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
      // release, so there is nothing to swallow.
      desc: "a lostpointercapture on the strip",
      fire: (): void => {
        list.dispatchEvent(new PointerEvent("lostpointercapture", { bubbles: true }));
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

  // The row's implicit touch capture ends when the strip takes the capture over;
  // that loss bubbles to the strip and is not the drag's.
  it("survives a lostpointercapture that bubbles up from a row", () => {
    startRealDrag(rows["b"] as HTMLElement);
    rows["b"]?.dispatchEvent(new PointerEvent("lostpointercapture", { bubbles: true }));
    expect(ghost(), "the drag is still live").not.toBeNull();
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

  // `setPointerCapture` can throw for an inactive pointer, so it is asked LAST; the stub ends the drag
  // inside that call, possible only if recovery is already wired.
  it("wires every recovery path before it asks for pointer capture", () => {
    const row = rows["b"] as HTMLElement;
    let endableAtCaptureTime = false;
    list.setPointerCapture = (): void => {
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
