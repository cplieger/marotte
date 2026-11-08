// The gesture tracker on a real overflowing scroller: which input opens a gesture, which way it aims, and when the
// motion it started ends.
import { afterEach, beforeAll, afterAll, beforeEach, describe, expect, it, vi } from "vitest";
import { GESTURE_IDLE_MS, GestureTracker, type Aim, type GestureHost } from "./scroll-gesture.js";
import { trackShellPresses } from "./shell-keys.js";

const nextFrame = window.requestAnimationFrame.bind(window);
async function frames(n: number): Promise<void> {
  for (let i = 0; i < n; i++) {
    await new Promise((r) => nextFrame(r));
  }
}

interface Log {
  calls: string[];
  rejoinable: boolean;
}

let scroller: HTMLElement;
let view: HTMLElement;
let log: Log;
let tracker: GestureTracker;

function host(): GestureHost {
  return {
    opened: (aim: Aim) => log.calls.push(`open ${String(aim)}`),
    aimed: (aim: Aim) => log.calls.push(`aim ${String(aim)}`),
    ended: () => log.calls.push("end"),
    atEndDown: () => log.calls.push("at-end down"),
    rejoinable: () => log.rejoinable,
    pressed: () => log.calls.push("press"),
    endKey: () => log.calls.push("end key"),
    endTop: () => scroller.scrollHeight - scroller.clientHeight,
  };
}

/** One tracker per scroller: its listeners live as long as the element, so every case builds a fresh pair. */
function mount(top: number): void {
  scroller = document.createElement("div");
  scroller.style.cssText =
    "position:fixed;top:0;left:0;width:300px;height:400px;overflow-y:auto;scrollbar-gutter:stable;";
  view = document.createElement("div");
  view.style.cssText = "height:3000px;";
  scroller.append(view);
  document.body.append(scroller);
  scroller.scrollTop = top;
  log = { calls: [], rejoinable: false };
  tracker = new GestureTracker(scroller, () => view, host());
  tracker.listen();
}

function wheel(target: EventTarget, deltaY: number, init: WheelEventInit = {}): void {
  target.dispatchEvent(new WheelEvent("wheel", { deltaY, deltaMode: 1, bubbles: true, ...init }));
}

/** An ordinary event carrying `touches`, the one field the tracker reads: a page without touch support may expose
 *  no `Touch` constructor. */
function touch(
  type: "touchstart" | "touchmove" | "touchend" | "touchcancel",
  y?: number,
  x = 10,
): void {
  const ev = new Event(type, { bubbles: true, cancelable: true });
  Object.defineProperty(ev, "touches", {
    value: y === undefined ? [] : [{ identifier: 1, target: scroller, clientX: x, clientY: y }],
  });
  scroller.dispatchEvent(ev);
}

beforeAll(() => {
  vi.useFakeTimers({ toFake: ["Date"] });
});
afterAll(() => {
  vi.useRealTimers();
  // Kept until here: trackShellPresses' document listener reads #shell-terminal at every later
  // pointerdown in the file.
  document.getElementById("shell-panel")?.remove();
});
beforeEach(() => {
  mount(1300);
});
afterEach(() => {
  tracker.forget();
  scroller.remove();
});

describe("which input opens a gesture", () => {
  it("opens on a wheel aimed by its delta's sign, and extends with each later tick", () => {
    wheel(scroller, -3);
    wheel(scroller, 3);
    expect(log.calls).toEqual(["open -1", "aim 1"]);
    expect(tracker.isOpen()).toBe(true);
  });

  it("ignores a horizontal wheel and a Ctrl+wheel zoom", () => {
    scroller.dispatchEvent(new WheelEvent("wheel", { deltaX: 40, deltaY: 0, bubbles: true }));
    wheel(scroller, -3, { ctrlKey: true });
    expect(log.calls).toEqual([]);
  });

  it("opens nothing for an input aimed at the clamp the scroller sits on", () => {
    scroller.scrollTop = 0;
    wheel(scroller, -3);
    scroller.scrollTop = scroller.scrollHeight - scroller.clientHeight;
    wheel(scroller, 3);
    expect(log.calls).toEqual([]);
  });

  it("reports a down input at the end as a rejoin only when the owner asks for one", () => {
    scroller.scrollTop = scroller.scrollHeight - scroller.clientHeight;
    wheel(scroller, 3);
    log.rejoinable = true;
    wheel(scroller, 3);
    expect(log.calls).toEqual(["at-end down"]);
  });

  it("leaves a wheel to a nested scroller that can still move that way", () => {
    const inner = document.createElement("div");
    inner.style.cssText = "height:300px;overflow-y:auto;";
    const tall = document.createElement("div");
    tall.style.cssText = "height:1000px;";
    inner.append(tall);
    view.prepend(inner);
    scroller.scrollTop = 0;
    inner.scrollTop = 350;
    wheel(tall, 3);
    wheel(tall, -3);
    expect(log.calls, "the inner box takes both").toEqual([]);

    inner.scrollTop = inner.scrollHeight - inner.clientHeight;
    wheel(tall, 3);
    expect(log.calls, "at its own end it chains to the transcript").toEqual(["open 1"]);
  });

  it("ignores a key another control handled, and a key typed in a field", () => {
    const tab = document.createElement("button");
    tab.addEventListener("keydown", (e) => {
      e.preventDefault();
    });
    const field = document.createElement("input");
    document.body.append(tab, field);
    tab.dispatchEvent(
      new KeyboardEvent("keydown", { key: "ArrowUp", bubbles: true, cancelable: true }),
    );
    field.dispatchEvent(new KeyboardEvent("keydown", { key: "PageUp", bubbles: true }));
    tab.remove();
    field.remove();
    expect(log.calls).toEqual([]);
  });

  // web-terminal-ui docs/input.md: with a hardware keyboard on an iPad, a press in the terminal
  // leaves focus on the body and the terminal types the keys the body gets.
  it("ignores a key the shell's terminal takes from the body after a press inside it", () => {
    const panel = document.createElement("div");
    panel.id = "shell-panel";
    const terminal = document.createElement("div");
    terminal.id = "shell-terminal";
    panel.append(terminal);
    document.body.append(panel);
    trackShellPresses();
    (document.activeElement as HTMLElement | null)?.blur();

    terminal.dispatchEvent(new PointerEvent("pointerdown", { bubbles: true }));
    for (const key of ["ArrowUp", "End"]) {
      document.body.dispatchEvent(new KeyboardEvent("keydown", { key, bubbles: true }));
    }
    expect(log.calls, "after a press in the terminal").toEqual([]);

    document.body.dispatchEvent(new PointerEvent("pointerdown", { bubbles: true }));
    document.body.dispatchEvent(new KeyboardEvent("keydown", { key: "ArrowUp", bubbles: true }));
    expect(log.calls, "after a press outside it").toEqual(["open -1"]);
  });

  it("aims the scrolling keys, Shift+Space up, and hands End to the owner", () => {
    for (const [key, shiftKey] of [
      ["ArrowUp", false],
      [" ", true],
      ["PageDown", false],
      ["End", false],
    ] as const) {
      document.body.dispatchEvent(new KeyboardEvent("keydown", { key, shiftKey, bubbles: true }));
    }
    expect(log.calls).toEqual(["open -1", "aim -1", "aim 1", "end key"]);
  });

  it("treats Enter and Space on a control in the view as a press, which Space on a button does not scroll", () => {
    const btn = document.createElement("button");
    view.append(btn);
    btn.dispatchEvent(new KeyboardEvent("keydown", { key: " ", bubbles: true }));
    btn.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true }));
    expect(log.calls).toEqual(["press", "press"]);
  });

  it("treats a pointer down off the scrollbar as a press, and on it as a held thumb aimed by its movement", () => {
    const box = scroller.getBoundingClientRect();
    const gutterX = box.right - (scroller.offsetWidth - scroller.clientWidth) / 2;
    scroller.dispatchEvent(
      new PointerEvent("pointerdown", { clientX: box.left + 5, clientY: 50, bubbles: true }),
    );
    expect(log.calls).toEqual(["press"]);
    expect(gutterX, "the premise: the scroller reserves a gutter").toBeLessThan(box.right);

    scroller.dispatchEvent(
      new PointerEvent("pointerdown", { clientX: gutterX, clientY: 200, bubbles: true }),
    );
    document.dispatchEvent(new PointerEvent("pointermove", { clientX: gutterX, clientY: 180 }));
    expect(log.calls).toEqual(["press", "open 0", "aim -1"]);
  });
});

describe("a touch", () => {
  it("is a tap, opening nothing, until it leaves the slop", () => {
    touch("touchstart", 200);
    touch("touchmove", 205);
    touch("touchend");
    expect(log.calls).toEqual([]);
  });

  it("aims by each move's own step, so a finger that reverses aims the way the content moves now", () => {
    touch("touchstart", 400);
    touch("touchmove", 340);
    touch("touchmove", 370);
    expect(log.calls).toEqual(["open 1", "aim -1"]);
  });

  it("aims nowhere for a sideways move, and never rejoins at the end for one", () => {
    scroller.scrollTop = scroller.scrollHeight - scroller.clientHeight;
    log.rejoinable = true;
    touch("touchstart", 200, 100);
    touch("touchmove", 200, 130);
    touch("touchend");
    expect(log.calls).toEqual([]);
  });

  it("rejoins at the end only for a finger moved up the screen past the slop", () => {
    scroller.scrollTop = scroller.scrollHeight - scroller.clientHeight;
    log.rejoinable = true;
    touch("touchstart", 200);
    touch("touchmove", 180);
    expect(log.calls).toEqual(["at-end down"]);
  });

  it("keeps the gesture open while the finger is held, whatever the idle bound, and after touchcancel until scrollend", async () => {
    touch("touchstart", 200);
    touch("touchmove", 240);
    tracker.noteScroll();
    vi.advanceTimersByTime(GESTURE_IDLE_MS * 4);
    await frames(2);
    expect(tracker.isOpen(), "held").toBe(true);

    touch("touchcancel");
    await frames(1);
    expect(tracker.isOpen(), "momentum after the system took the touch").toBe(true);
    scroller.dispatchEvent(new Event("scrollend"));
    expect(log.calls).toEqual(["open -1", "end"]);
  });
});

describe("when a gesture ends", () => {
  it("ends at its own scrollend, after one of its scroll events", () => {
    wheel(scroller, -3);
    scroller.dispatchEvent(new Event("scrollend"));
    expect(tracker.isOpen(), "no scroll event of its own yet").toBe(true);
    tracker.noteScroll();
    scroller.dispatchEvent(new Event("scrollend"));
    expect(log.calls).toEqual(["open -1", "end"]);
  });

  it("ignores a scrollend within two frames of the owner's write, and takes one after", async () => {
    wheel(scroller, -3);
    tracker.noteScroll();
    tracker.noteOwnWrite();
    // The write's own frame: its scroll event already carries the tick's next step, then its scrollend.
    tracker.noteScroll();
    scroller.dispatchEvent(new Event("scrollend"));
    expect(tracker.isOpen(), "mid-tick").toBe(true);

    await frames(2);
    tracker.noteScroll();
    scroller.dispatchEvent(new Event("scrollend"));
    expect(tracker.isOpen()).toBe(false);
  });

  it("ends at the idle bound, which counts both time and frames, when the platform answers with no motion", async () => {
    wheel(scroller, -3);
    await frames(20);
    expect(tracker.isOpen(), "frames passed inside the time").toBe(true);
    tracker.noteScroll();
    vi.advanceTimersByTime(GESTURE_IDLE_MS + 10);
    await frames(3);
    expect(tracker.isOpen(), "the time passed in fewer frames").toBe(true);
    await frames(14);
    expect(log.calls).toEqual(["open -1", "end"]);
  });

  it("is dropped by forget with no end reported", () => {
    wheel(scroller, -3);
    tracker.forget();
    scroller.dispatchEvent(new Event("scrollend"));
    expect(log.calls).toEqual(["open -1"]);
    expect(tracker.isOpen()).toBe(false);
  });

  it("opens an aimless gesture for a scroll the owner saw with no input", () => {
    tracker.openAimless();
    tracker.noteScroll();
    scroller.dispatchEvent(new Event("scrollend"));
    expect(log.calls).toEqual(["open 0", "end"]);
  });
});

describe("a click", () => {
  it("is a press when it lands in the view, before the control's own handler runs", () => {
    const btn = document.createElement("button");
    btn.addEventListener("click", () => log.calls.push("handler"));
    view.append(btn);
    btn.click();
    expect(log.calls).toEqual(["press", "handler"]);
  });
});
