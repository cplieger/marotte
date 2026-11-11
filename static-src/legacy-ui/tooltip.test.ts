import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";

import { initTooltips, _resetForTest } from "./tooltip.js";

beforeEach(() => {
  vi.useFakeTimers();
});

afterEach(() => {
  _resetForTest();
  vi.useRealTimers();
  document.body.innerHTML = "";
});

function anchor(text: string): HTMLElement {
  const el = document.createElement("button");
  el.setAttribute("data-uip-tooltip", text);
  document.body.appendChild(el);
  return el;
}

function tip(): HTMLElement | null {
  return document.querySelector(".uip-tooltip");
}

/** The tooltip that is not fading out — the one a user would read. */
function liveTip(): HTMLElement | null {
  return document.querySelector(".uip-tooltip:not(.is-leaving)");
}

function tipCount(): number {
  return document.querySelectorAll(".uip-tooltip").length;
}

/**
 * Present a viewport of exactly `width` x `height` at the origin.
 *
 * The positioner reads visualViewport first, falling back to inner*; stub both
 * or the test silently measures the real window.
 */
function stubViewport(width: number, height: number): void {
  vi.stubGlobal("visualViewport", {
    offsetLeft: 0,
    offsetTop: 0,
    width,
    height,
    addEventListener: vi.fn(),
    removeEventListener: vi.fn(),
  });
  vi.stubGlobal("innerWidth", width);
  vi.stubGlobal("innerHeight", height);
}

function anchorRect(el: HTMLElement, left: number, top: number, w: number, h: number): void {
  el.getBoundingClientRect = (): DOMRect =>
    ({
      x: left,
      y: top,
      left,
      top,
      width: w,
      height: h,
      right: left + w,
      bottom: top + h,
      toJSON: () => ({}),
    }) as DOMRect;
}

function pointerOver(el: HTMLElement): void {
  el.dispatchEvent(new Event("pointerover", { bubbles: true }));
}

function pointerOut(el: HTMLElement, related: EventTarget | null = null): void {
  const e = new Event("pointerout", { bubbles: true }) as Event & {
    relatedTarget: EventTarget | null;
  };
  e.relatedTarget = related;
  el.dispatchEvent(e);
}

describe("initTooltips", () => {
  it("shows a tooltip after the cold delay and wires aria-describedby", () => {
    initTooltips();
    const a = anchor("Hello");
    pointerOver(a);
    vi.advanceTimersByTime(499);
    expect(tip()).toBeNull();
    vi.advanceTimersByTime(1);
    const t = tip();
    expect(t).not.toBeNull();
    expect(t!.getAttribute("role")).toBe("tooltip");
    expect(t!.textContent).toBe("Hello");
    expect(t!.id).not.toBe("");
    expect(a.getAttribute("aria-describedby")).toBe(t!.id);
  });

  it("hides on pointerout, removing aria-describedby and the element", () => {
    initTooltips();
    const a = anchor("Hello");
    pointerOver(a);
    vi.advanceTimersByTime(1000);
    expect(tip()).not.toBeNull();

    pointerOut(a, null);
    expect(a.getAttribute("aria-describedby")).toBeNull();
    expect(tip()!.classList.contains("is-leaving")).toBe(true);
    vi.advanceTimersByTime(400);
    expect(tip()).toBeNull();
  });

  it("keeps the tooltip when the pointer moves within the anchor's own subtree", () => {
    initTooltips();
    const a = anchor("Hi");
    const child = document.createElement("span");
    a.appendChild(child);
    pointerOver(a);
    vi.advanceTimersByTime(1000);
    expect(tip()).not.toBeNull();
    pointerOut(a, child);
    expect(tip()).not.toBeNull();
    expect(tip()!.classList.contains("is-leaving")).toBe(false);
  });

  it("makes every hover wait the same delay by default — no instant peer", () => {
    // Warm defaults to the cold delay, so a peer hovered right after a tooltip
    // hid still costs the full wait.
    initTooltips();
    const a = anchor("A");
    const b = anchor("B");
    pointerOver(a);
    vi.advanceTimersByTime(500);
    expect(tip()!.textContent).toBe("A");
    pointerOut(a, null);
    vi.advanceTimersByTime(400);
    pointerOver(b);
    vi.advanceTimersByTime(499);
    expect(tip()).toBeNull();
    vi.advanceTimersByTime(1);
    expect(tip()!.textContent).toBe("B");
  });

  it("uses the warm delay for a peer within the cooldown window when opted into", () => {
    initTooltips({ delayWarm: 0 });
    const a = anchor("A");
    const b = anchor("B");
    pointerOver(a);
    vi.advanceTimersByTime(500);
    expect(tip()!.textContent).toBe("A");
    pointerOut(a, null);
    vi.advanceTimersByTime(400);
    pointerOver(b);
    vi.advanceTimersByTime(1);
    expect(tip()!.textContent).toBe("B");
  });

  it("takes the cold delay as the warm delay when only delayCold is given", () => {
    initTooltips({ delayCold: 2000 });
    const a = anchor("A");
    const b = anchor("B");
    pointerOver(a);
    vi.advanceTimersByTime(2000);
    expect(tip()!.textContent).toBe("A");
    pointerOut(a, null);
    vi.advanceTimersByTime(400);
    pointerOver(b);
    vi.advanceTimersByTime(1999);
    expect(tip()).toBeNull();
    vi.advanceTimersByTime(1);
    expect(tip()!.textContent).toBe("B");
  });

  it("splits multiline text on newlines with <br>", () => {
    initTooltips();
    const a = anchor("line1\nline2");
    pointerOver(a);
    vi.advanceTimersByTime(1000);
    const t = tip()!;
    expect(t.querySelectorAll("br")).toHaveLength(1);
    expect(t.textContent).toBe("line1line2");
  });

  it("Escape hides the tooltip", () => {
    initTooltips();
    const a = anchor("Hi");
    pointerOver(a);
    vi.advanceTimersByTime(1000);
    expect(tip()).not.toBeNull();
    document.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape" }));
    vi.advanceTimersByTime(400);
    expect(tip()).toBeNull();
  });

  it("honors a custom attribute and delay options", () => {
    initTooltips({ attribute: "data-hint", delayCold: 200 });
    const el = document.createElement("button");
    el.setAttribute("data-hint", "Custom");
    document.body.appendChild(el);
    pointerOver(el);
    vi.advanceTimersByTime(200);
    expect(tip()!.textContent).toBe("Custom");
  });

  it("is idempotent — a second initTooltips does not double-install", () => {
    initTooltips();
    initTooltips();
    const a = anchor("X");
    pointerOver(a);
    vi.advanceTimersByTime(1000);
    expect(document.querySelectorAll(".uip-tooltip")).toHaveLength(1);
  });

  it("ignores triggers with an empty tooltip value", () => {
    initTooltips();
    const a = anchor("");
    pointerOver(a);
    vi.advanceTimersByTime(1000);
    expect(tip()).toBeNull();
  });

  it("cancels a still-pending tooltip when the pointer leaves during the cold delay", () => {
    initTooltips();
    const a = anchor("Hi");
    pointerOver(a);
    pointerOut(a, null);
    vi.advanceTimersByTime(1000);
    expect(tip()).toBeNull();
  });

  it("hides on capture-phase scroll", () => {
    initTooltips();
    const a = anchor("Hi");
    pointerOver(a);
    vi.advanceTimersByTime(1000);
    expect(tip()).not.toBeNull();
    document.dispatchEvent(new Event("scroll"));
    vi.advanceTimersByTime(400);
    expect(tip()).toBeNull();
  });

  it("hides on window blur", () => {
    initTooltips();
    const a = anchor("Hi");
    pointerOver(a);
    vi.advanceTimersByTime(1000);
    expect(tip()).not.toBeNull();
    window.dispatchEvent(new Event("blur"));
    vi.advanceTimersByTime(400);
    expect(tip()).toBeNull();
  });

  it("preserves a pre-existing aria-describedby, appending then removing only its own id", () => {
    initTooltips();
    const a = anchor("Hi");
    a.setAttribute("aria-describedby", "foo");
    pointerOver(a);
    vi.advanceTimersByTime(1000);
    const t = tip()!;
    expect(a.getAttribute("aria-describedby")).toBe(`foo ${t.id}`);
    pointerOut(a, null);
    expect(a.getAttribute("aria-describedby")).toBe("foo");
    vi.advanceTimersByTime(400);
  });

  it("renders into an open ancestor <dialog> so it clears the modal's top layer", () => {
    initTooltips();
    const d = document.createElement("dialog");
    d.setAttribute("open", "");
    const a = document.createElement("button");
    a.setAttribute("data-uip-tooltip", "In dialog");
    d.appendChild(a);
    document.body.appendChild(d);
    pointerOver(a);
    vi.advanceTimersByTime(1000);
    const t = tip()!;
    expect(t.parentElement).toBe(d);
  });
});

describe("focus-triggered tooltips (:focus-visible gate)", () => {
  function focusIn(el: HTMLElement): void {
    el.dispatchEvent(new Event("focusin", { bubbles: true }));
  }

  it("shows on keyboard-driven focus (element matches :focus-visible)", () => {
    initTooltips();
    const a = anchor("hint");
    vi.spyOn(a, "matches").mockImplementation((sel: string) => sel === ":focus-visible");
    focusIn(a);
    vi.advanceTimersByTime(1000);
    expect(tip()).not.toBeNull();
  });

  it("stays quiet on programmatic or pointer focus (no :focus-visible)", () => {
    // A modal focusing its first control must not pop a tooltip with no hover.
    initTooltips();
    const a = anchor("hint");
    vi.spyOn(a, "matches").mockReturnValue(false);
    focusIn(a);
    vi.advanceTimersByTime(5000);
    expect(tip()).toBeNull();
  });

  it("falls back to showing when the engine lacks :focus-visible (matches throws)", () => {
    initTooltips();
    const a = anchor("hint");
    vi.spyOn(a, "matches").mockImplementation(() => {
      throw new SyntaxError("unsupported selector");
    });
    focusIn(a);
    vi.advanceTimersByTime(1000);
    expect(tip()).not.toBeNull();
  });

  it("focusout hides a keyboard-shown tooltip", () => {
    initTooltips();
    const a = anchor("hint");
    vi.spyOn(a, "matches").mockImplementation((sel: string) => sel === ":focus-visible");
    focusIn(a);
    vi.advanceTimersByTime(1000);
    expect(tip()).not.toBeNull();
    const out = new Event("focusout", { bubbles: true }) as Event & {
      relatedTarget: EventTarget | null;
    };
    out.relatedTarget = null;
    a.dispatchEvent(out);
    vi.advanceTimersByTime(500);
    expect(document.querySelector(".uip-tooltip:not(.is-leaving)")).toBeNull();
  });
});

describe("dismissal triggers are specific", () => {
  it("leaves the tooltip up for a keydown that is not Escape", () => {
    // Only Escape dismisses; Tab or typed characters must leave it up.
    initTooltips();
    const a = anchor("Hi");
    pointerOver(a);
    vi.advanceTimersByTime(1000);
    expect(tip()).not.toBeNull();
    document.dispatchEvent(new KeyboardEvent("keydown", { key: "Tab" }));
    vi.advanceTimersByTime(400);
    expect(tip()).not.toBeNull();
  });

  it("hides on a nested scroller's scroll, which does not bubble", () => {
    // Scroll events do not bubble; only a capture-phase listener sees a nested one.
    initTooltips();
    const scroller = document.createElement("div");
    document.body.appendChild(scroller);
    const a = anchor("Hi");
    pointerOver(a);
    vi.advanceTimersByTime(1000);
    expect(tip()).not.toBeNull();
    scroller.dispatchEvent(new Event("scroll"));
    vi.advanceTimersByTime(400);
    expect(tip()).toBeNull();
  });

  it("a pointerout from an unrelated anchor leaves the tooltip up", () => {
    initTooltips();
    const a = anchor("A");
    const b = anchor("B");
    pointerOver(a);
    vi.advanceTimersByTime(500);
    pointerOut(b, null);
    expect(tip()!.classList.contains("is-leaving")).toBe(false);
  });
});

describe("re-entering an anchor the tooltip already tracks", () => {
  it("does not restart a pending tooltip's delay", () => {
    // pointerover refires per descendant crossed; a restart would delay forever.
    initTooltips();
    const a = anchor("Hi");
    pointerOver(a);
    vi.advanceTimersByTime(400);
    pointerOver(a);
    vi.advanceTimersByTime(100);
    expect(tip()).not.toBeNull();
  });

  it("does not rebuild a visible tooltip", () => {
    initTooltips();
    const a = anchor("Hi");
    pointerOver(a);
    vi.advanceTimersByTime(500);
    const first = tip();
    expect(first).not.toBeNull();
    pointerOver(a);
    expect(tip()).toBe(first);
  });
});

describe("handing the tooltip from one anchor to another", () => {
  it("removes the visible tooltip and its describedby without a pointerout", () => {
    initTooltips();
    const a = anchor("A");
    const b = anchor("B");
    pointerOver(a);
    vi.advanceTimersByTime(500);
    expect(a.getAttribute("aria-describedby")).not.toBeNull();
    pointerOver(b);
    expect(tipCount()).toBe(0);
    expect(a.getAttribute("aria-describedby")).toBeNull();
  });

  it("cancels a still-pending tooltip so only the newest anchor shows", () => {
    initTooltips();
    const a = anchor("A");
    const b = anchor("B");
    pointerOver(a);
    vi.advanceTimersByTime(400);
    pointerOver(b);
    vi.advanceTimersByTime(500);
    expect(tipCount()).toBe(1);
    expect(tip()!.textContent).toBe("B");
  });

  it("never paints the tooltip of an anchor the pointer already left", () => {
    // A's deadline falls inside B's wait, so a surviving timer would paint A
    // while B is pending — only observable at this mid-wait moment.
    initTooltips();
    const a = anchor("A");
    const b = anchor("B");
    pointerOver(a);
    vi.advanceTimersByTime(400);
    pointerOver(b);
    vi.advanceTimersByTime(100);
    expect(tip()).toBeNull();
  });

  it("removes a fading tooltip when its anchor is hovered again", () => {
    initTooltips({ delayWarm: 0 });
    const a = anchor("A");
    pointerOver(a);
    vi.advanceTimersByTime(500);
    pointerOut(a, null);
    expect(tip()!.classList.contains("is-leaving")).toBe(true);
    pointerOver(a);
    vi.advanceTimersByTime(1);
    expect(tipCount()).toBe(1);
  });

  it("an older tooltip's fade is cancelled outright, so it cannot reach the newer one", () => {
    // A superseded fade is cancelled outright, so B stays dismissable across the
    // window where A's ceiling would have fired.
    initTooltips({ delayWarm: 0 });
    const a = anchor("A");
    const b = anchor("B");
    pointerOver(a);
    vi.advanceTimersByTime(500);
    pointerOut(a, null);
    pointerOver(b);
    vi.advanceTimersByTime(1);
    expect(liveTip()!.textContent).toBe("B");
    vi.advanceTimersByTime(600);
    expect(liveTip()!.textContent).toBe("B");
    pointerOut(b, null);
    vi.advanceTimersByTime(400);
    expect(tipCount()).toBe(0);
  });
});

describe("the warm window", () => {
  it("gives a peer the warm delay while the first tooltip is still visible", () => {
    initTooltips({ delayWarm: 0 });
    const a = anchor("A");
    const b = anchor("B");
    pointerOver(a);
    vi.advanceTimersByTime(500);
    expect(tip()!.textContent).toBe("A");
    pointerOver(b);
    vi.advanceTimersByTime(1);
    expect(liveTip()!.textContent).toBe("B");
  });

  it("still makes the first tooltip of a cold group wait the cold delay", () => {
    // delayWarm only applies once warm; a cold group's opening hover pays delayCold.
    initTooltips({ delayWarm: 0 });
    const a = anchor("A");
    pointerOver(a);
    vi.advanceTimersByTime(499);
    expect(tip()).toBeNull();
  });

  it("opens after a pending tooltip is cancelled, not just after a visible one hides", () => {
    initTooltips({ delayWarm: 0 });
    const a = anchor("A");
    const b = anchor("B");
    pointerOver(a);
    vi.advanceTimersByTime(100);
    pointerOut(a, null);
    pointerOver(b);
    vi.advanceTimersByTime(1);
    expect(tip()!.textContent).toBe("B");
  });
});

describe("show-time guards and placement", () => {
  it("never paints a tooltip for an anchor removed during the delay", () => {
    initTooltips();
    const a = anchor("Hi");
    pointerOver(a);
    a.remove();
    vi.advanceTimersByTime(1000);
    expect(tip()).toBeNull();
  });

  it("positions the tooltip above its anchor, centered", () => {
    // Asserted as a relationship, not magic pixels: the tooltip's bottom edge
    // sits GAP above the anchor top, centred horizontally, at any measured size.
    stubViewport(2000, 2000);
    initTooltips();
    const a = anchor("Hi");
    anchorRect(a, 100, 200, 40, 40);
    pointerOver(a);
    vi.advanceTimersByTime(500);
    const t = tip()!;
    // offsetWidth/Height, as placeAnchored measures: a fractional text width
    // makes the bounding box differ from the integer layout box it placed by.
    expect(t.style.position).toBe("fixed");
    expect(t.offsetHeight).toBeGreaterThan(0);
    expect(parseFloat(t.style.top) + t.offsetHeight).toBe(194);
    expect(parseFloat(t.style.left) + t.offsetWidth / 2).toBe(120);
  });

  it("renders a single-line tooltip with no <br>", () => {
    initTooltips();
    const a = anchor("just one line");
    pointerOver(a);
    vi.advanceTimersByTime(500);
    expect(tip()!.querySelectorAll("br")).toHaveLength(0);
  });

  it("normalizes stray whitespace in an app-set aria-describedby", () => {
    initTooltips();
    const a = anchor("Hi");
    a.setAttribute("aria-describedby", "  foo  bar  ");
    pointerOver(a);
    vi.advanceTimersByTime(500);
    expect(a.getAttribute("aria-describedby")).toBe(`foo bar ${tip()!.id}`);
  });

  it("removes a live tooltip when the controller is torn down", () => {
    initTooltips();
    const a = anchor("Hi");
    pointerOver(a);
    vi.advanceTimersByTime(500);
    expect(tip()).not.toBeNull();
    _resetForTest();
    expect(tip()).toBeNull();
  });
});

/** How many of a spy's calls were registrations for `type`. */
function callsFor(spy: { mock: { calls: unknown[][] } }, type: string): number {
  return spy.mock.calls.filter((call) => call[0] === type).length;
}

describe("teardown leaves the document as it found it", () => {
  it("removes every listener init installed, on document and on window", () => {
    const docAdd = vi.spyOn(document, "addEventListener");
    const docRemove = vi.spyOn(document, "removeEventListener");
    const winAdd = vi.spyOn(window, "addEventListener");
    const winRemove = vi.spyOn(window, "removeEventListener");

    initTooltips();
    _resetForTest();

    // A listener not given back accumulates across every init/reset cycle.
    for (const type of ["pointerover", "pointerout", "focusin", "focusout", "keydown", "scroll"]) {
      expect(callsFor(docAdd, type)).toBe(1);
      expect(callsFor(docRemove, type)).toBe(1);
    }
    expect(callsFor(winAdd, "blur")).toBe(1);
    expect(callsFor(winRemove, "blur")).toBe(1);
  });

  it("gives the capture-phase scroll listener back with the capture flag it used", () => {
    const docRemove = vi.spyOn(document, "removeEventListener");

    initTooltips();
    _resetForTest();

    // scroll is registered capture-phase; removeEventListener must match the flag
    // or the listener leaks for the life of the page.
    expect(docRemove).toHaveBeenCalledWith("scroll", expect.any(Function), true);
  });

  it("repeated init/reset cycles do not accumulate listeners", () => {
    const docAdd = vi.spyOn(document, "addEventListener");
    const docRemove = vi.spyOn(document, "removeEventListener");

    for (let i = 0; i < 4; i++) {
      initTooltips();
      _resetForTest();
    }

    expect(callsFor(docAdd, "pointerout")).toBe(4);
    expect(callsFor(docRemove, "pointerout")).toBe(4);
    expect(callsFor(docAdd, "keydown")).toBe(4);
    expect(callsFor(docRemove, "keydown")).toBe(4);
  });
});

describe("the warm window's edges", () => {
  it("a hover exactly at the warm deadline waits the cold delay, not the warm one", () => {
    // The window is [hide, hide + cooldown): at the deadline the group is cold again.
    initTooltips({ delayCold: 500, delayWarm: 100, cooldown: 500 });
    const a = anchor("A");
    const b = anchor("B");

    pointerOver(a);
    vi.advanceTimersByTime(500);
    pointerOut(a, null);
    vi.advanceTimersByTime(500);

    pointerOver(b);
    vi.advanceTimersByTime(100);
    expect(liveTip()).toBeNull();
    vi.advanceTimersByTime(400);
    expect(liveTip()!.textContent).toBe("B");
  });

  it("gives every tooltip a fresh id that rises with show order", () => {
    initTooltips({ delayCold: 0 });
    const a = anchor("A");
    const b = anchor("B");

    pointerOver(a);
    vi.advanceTimersByTime(1);
    const first = tip()!.id;
    pointerOut(a, null);
    vi.advanceTimersByTime(400);
    pointerOver(b);
    vi.advanceTimersByTime(1);
    const second = tip()!.id;

    expect(first).toMatch(/^uip-tip-\d+$/);
    expect(second).toMatch(/^uip-tip-\d+$/);
    const seq = (id: string): number => Number(id.slice("uip-tip-".length));
    expect(seq(second)).toBeGreaterThan(seq(first));
  });
});

describe("pointer traffic that is not over an anchor", () => {
  it("a pointerover away from any anchor shows nothing", () => {
    initTooltips();
    const plain = document.createElement("div");
    document.body.appendChild(plain);

    pointerOver(plain);
    vi.advanceTimersByTime(1000);

    expect(tip()).toBeNull();
  });

  it("a pointerover on an anchor's ancestor, not the anchor, shows nothing", () => {
    initTooltips();
    const box = document.createElement("div");
    const a = anchor("A");
    box.appendChild(a);
    document.body.appendChild(box);

    pointerOver(box);
    vi.advanceTimersByTime(1000);

    expect(tip()).toBeNull();
  });
});

describe("a superseded fade", () => {
  it("is cancelled outright, so only one fade is ever in flight", () => {
    // Superseding cancels the older fade outright, so its ceiling never arrives
    // and cannot adopt the newer tip.
    initTooltips({ delayCold: 0, delayWarm: 0, cooldown: 0 });
    const a = anchor("A");
    const b = anchor("B");

    pointerOver(a);
    vi.advanceTimersByTime(1);
    pointerOut(a, null);
    pointerOver(b);
    vi.advanceTimersByTime(1);
    expect(tipCount()).toBe(1);
    pointerOut(b, null);
    vi.advanceTimersByTime(600);

    pointerOver(a);
    expect(tipCount()).toBe(0);
  });
});

describe("handing an app-owned aria-describedby back", () => {
  it("drops the attribute when only the tip's own token (and whitespace) is left", () => {
    initTooltips();
    const a = anchor("Hi");

    pointerOver(a);
    vi.advanceTimersByTime(500);
    const t = tip()!;
    // The app may rewrite the attribute while the tooltip is up; the tooltip must
    // still leave nothing behind.
    a.setAttribute("aria-describedby", ` ${t.id} `);

    pointerOut(a, null);

    expect(a.hasAttribute("aria-describedby")).toBe(false);
  });
});

// A TRIGGER WHOSE HIT BOX IS BIGGER THAN ITS INK names that ink, and the tip is
// placed against the MARK while everything else stays on the trigger.
//
// The consumer shape this exists for: a row-wide disclosure whose only ink is a
// leading glyph, and a full-width file row whose name sits at its leading edge.
// Both anchored the tip at the centre of their own box, which is empty space —
// measured at 269px and 243px from the ink respectively.
describe("a marked mark inside the trigger", () => {
  /** A 600x24 row with `w`x16 of ink at its leading edge, marked. Returns the
   *  trigger and the mark, each with a stubbed box. */
  function row(text: string, w: number): { trigger: HTMLElement; mark: HTMLElement } {
    const trigger = anchor(text);
    anchorRect(trigger, 100, 200, 600, 24);
    const mark = document.createElement("span");
    mark.setAttribute("data-uip-tooltip-anchor", "");
    trigger.appendChild(mark);
    anchorRect(mark, 112, 204, w, 16);
    return { trigger, mark };
  }

  /** The tip's centre on the inline axis, from the styles the positioner wrote. */
  function tipCenterX(t: HTMLElement): number {
    return parseFloat(t.style.left) + t.getBoundingClientRect().width / 2;
  }

  it("centres the tip on the mark rather than on the trigger's own box", () => {
    stubViewport(2000, 2000);
    initTooltips();
    const { trigger, mark } = row("Show turn details", 16);
    pointerOver(trigger);
    vi.advanceTimersByTime(500);

    const t = tip()!;
    const inkCenter = mark.getBoundingClientRect().left + 8;
    expect(tipCenterX(t)).toBeCloseTo(inkCenter, 0);
    // The trigger's own centre is 280px away.
    expect(Math.abs(tipCenterX(t) - 400)).toBeGreaterThan(200);
    // Vertically it still sits GAP above what it points at — the mark's top edge
    // rather than the row's, so a tall band does not push the tip off the ink.
    expect(parseFloat(t.style.top) + t.getBoundingClientRect().height).toBe(198);
  });

  it("clips the mark to the trigger, so ellipsised ink cannot drag the tip off the row", () => {
    // A file name's own rect runs past the row that clips it; the tip belongs over
    // the part a reader can see.
    stubViewport(2000, 2000);
    initTooltips();
    const { trigger, mark } = row("a/long/path.ts", 16);
    anchorRect(mark, 112, 204, 2000, 16);
    pointerOver(trigger);
    vi.advanceTimersByTime(500);

    // Visible ink is 112..700 (the row's own right edge), so the centre is 406.
    // `toBeCloseTo` because the positioner writes a rounded `left`, so a centre
    // derived from it is fractional whenever the tip's own width is.
    expect(tipCenterX(tip()!)).toBeCloseTo(406, 0);
    expect(trigger.getBoundingClientRect().right).toBe(700);
  });

  it("falls back to the trigger when the mark renders nothing", () => {
    // A glyph slot with no content yet, or a mark behind `display: none`: there is
    // no ink to point at, so the trigger is what is left.
    stubViewport(2000, 2000);
    initTooltips();
    const { trigger, mark } = row("Show turn details", 16);
    anchorRect(mark, 0, 0, 0, 0);
    pointerOver(trigger);
    vi.advanceTimersByTime(500);
    expect(tipCenterX(tip()!)).toBeCloseTo(400, 0);
  });

  it("falls back to the trigger when the mark is scrolled fully out of it", () => {
    stubViewport(2000, 2000);
    initTooltips();
    const { trigger, mark } = row("Show turn details", 16);
    anchorRect(mark, 900, 204, 16, 16);
    pointerOver(trigger);
    vi.advanceTimersByTime(500);
    expect(tipCenterX(tip()!)).toBeCloseTo(400, 0);
  });

  it("ignores a mark that belongs to a nested trigger", () => {
    stubViewport(2000, 2000);
    initTooltips();
    const outer = anchor("Outer");
    anchorRect(outer, 100, 200, 600, 24);
    const inner = document.createElement("span");
    inner.setAttribute("data-uip-tooltip", "Inner");
    const innerMark = document.createElement("span");
    innerMark.setAttribute("data-uip-tooltip-anchor", "");
    inner.appendChild(innerMark);
    outer.appendChild(inner);
    anchorRect(inner, 600, 200, 40, 24);
    anchorRect(innerMark, 604, 204, 16, 16);

    pointerOver(outer);
    vi.advanceTimersByTime(500);
    expect(tipCenterX(tip()!)).toBeCloseTo(400, 0);
  });

  it("keeps the trigger as the hover target and the described element", () => {
    // The mark is POSITIONING only: a reader pointing anywhere in the row still
    // gets the tip, and the description still belongs to the control that has a
    // name and takes focus.
    stubViewport(2000, 2000);
    initTooltips();
    const { trigger, mark } = row("Show turn details", 16);

    // Entering over a child of the row is what a pointer actually does.
    mark.dispatchEvent(new Event("pointerover", { bubbles: true }));
    vi.advanceTimersByTime(500);
    const t = tip()!;
    expect(trigger.getAttribute("aria-describedby")).toBe(t.id);
    expect(mark.hasAttribute("aria-describedby")).toBe(false);
  });

  it("derives the mark attribute from a renamed trigger attribute", () => {
    stubViewport(2000, 2000);
    initTooltips({ attribute: "data-tooltip" });
    const trigger = document.createElement("button");
    trigger.setAttribute("data-tooltip", "Show turn details");
    document.body.appendChild(trigger);
    anchorRect(trigger, 100, 200, 600, 24);
    const mark = document.createElement("span");
    mark.setAttribute("data-tooltip-anchor", "");
    trigger.appendChild(mark);
    anchorRect(mark, 112, 204, 16, 16);

    pointerOver(trigger);
    vi.advanceTimersByTime(500);
    expect(tipCenterX(tip()!)).toBeCloseTo(120, 0);
  });
});

describe("tooltips for touch", () => {
  function refTrigger(
    text: string,
    opts: { toggle?: boolean; describedBy?: string; attr?: string } = {},
  ): HTMLElement {
    const attr = opts.attr ?? "data-uip-tooltip";
    const desc = document.createElement("span");
    desc.id = `tip-${String(Math.random()).slice(2)}`;
    desc.hidden = true;
    desc.textContent = text;
    const b = document.createElement("button");
    b.type = "button";
    b.setAttribute(`${attr}-ref`, desc.id);
    b.setAttribute("aria-describedby", opts.describedBy ?? desc.id);
    if (opts.toggle === true) {
      b.setAttribute(`${attr}-toggle`, "");
    }
    document.body.append(desc, b);
    return b;
  }

  function touch(type: string, el: EventTarget, related: EventTarget | null = null): void {
    el.dispatchEvent(
      new PointerEvent(type, {
        bubbles: true,
        pointerType: "touch",
        relatedTarget: related as Element | null,
      }),
    );
  }

  it("shows a referenced text on hover, out of the tree, and leaves aria-describedby as the host set it", () => {
    initTooltips();
    const b = refTrigger("Starts a full check.\nTakes a minute.", { describedBy: "intro  tip-x" });
    pointerOver(b);
    vi.advanceTimersByTime(500);
    expect(liveTip()?.innerHTML).toBe("Starts a full check.<br>Takes a minute.");
    expect(liveTip()?.getAttribute("aria-hidden")).toBe("true");
    expect(b.getAttribute("aria-describedby")).toBe("intro  tip-x");
    pointerOut(b);
    vi.runAllTimers();
    expect(b.getAttribute("aria-describedby")).toBe("intro  tip-x");
  });

  it("shows nothing for a reference to nothing or to an empty element, and prefers the reference over text", () => {
    initTooltips();
    const missing = document.createElement("button");
    missing.setAttribute("data-uip-tooltip-ref", "no-such-id");
    document.body.append(missing);
    pointerOver(missing);
    vi.advanceTimersByTime(500);
    expect(tip()).toBeNull();
    const both = refTrigger("From the reference");
    both.setAttribute("data-uip-tooltip", "From the attribute");
    pointerOver(both);
    vi.advanceTimersByTime(500);
    expect(liveTip()?.textContent).toBe("From the reference");
  });

  it("derives the reference and toggle attributes from a renamed attribute", () => {
    initTooltips({ attribute: "data-tip" });
    const b = refTrigger("Renamed", { toggle: true, attr: "data-tip" });
    b.click();
    expect(liveTip()?.textContent).toBe("Renamed");
  });

  it("opens a toggle's tip at once on a click and pins it, and closes it on the second", () => {
    initTooltips();
    const b = refTrigger("About quick checks", { toggle: true });
    b.click();
    expect(liveTip()?.textContent).toBe("About quick checks");
    pointerOut(b);
    vi.advanceTimersByTime(1000);
    expect(liveTip()).not.toBeNull();
    b.click();
    expect(liveTip()).toBeNull();
  });

  it("keeps a tapped tip open through the whole touch sequence", () => {
    initTooltips();
    const b = refTrigger("About quick checks", { toggle: true });
    for (const t of ["pointerover", "pointerdown", "pointerup", "pointerout"]) {
      touch(t, b);
    }
    b.click();
    vi.advanceTimersByTime(1000);
    expect(liveTip()?.textContent).toBe("About quick checks");
  });

  it.each([
    ["Escape", () => document.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape" }))],
    ["a press outside", () => touch("pointerdown", document.body)],
    ["a scroll", () => document.dispatchEvent(new Event("scroll"))],
    ["the window losing focus", () => window.dispatchEvent(new Event("blur"))],
  ])("closes a pinned tip on %s", (_, close) => {
    initTooltips();
    const b = refTrigger("Pinned", { toggle: true });
    b.click();
    close();
    expect(liveTip()).toBeNull();
  });

  it("closes a pinned tip when focus moves elsewhere, or another tip opens, but not on a press inside it", () => {
    initTooltips();
    const b = refTrigger("Pinned", { toggle: true });
    const other = anchor("Other");
    b.click();
    touch("pointerdown", liveTip() as HTMLElement);
    expect(liveTip()?.textContent).toBe("Pinned");
    b.dispatchEvent(new FocusEvent("focusout", { bubbles: true, relatedTarget: other }));
    expect(liveTip()).toBeNull();
    b.click();
    pointerOver(other);
    vi.advanceTimersByTime(500);
    expect(liveTip()?.textContent).toBe("Other");
    expect(tipCount()).toBeLessThanOrEqual(2);
  });

  it("opens nothing on a click of a toggle with no reference", () => {
    initTooltips();
    const b = anchor("Text only");
    b.setAttribute("data-uip-tooltip-toggle", "");
    b.click();
    expect(tip()).toBeNull();
  });

  it("starts no hover tip from a finger, and still from a mouse or a pen", () => {
    initTooltips();
    const b = anchor("Hover text");
    touch("pointerover", b);
    vi.advanceTimersByTime(1000);
    expect(tip()).toBeNull();
    for (const pointerType of ["mouse", "pen"]) {
      b.dispatchEvent(new PointerEvent("pointerover", { bubbles: true, pointerType }));
      vi.advanceTimersByTime(500);
      expect(liveTip()?.textContent, pointerType).toBe("Hover text");
      pointerOut(b);
      vi.runAllTimers();
    }
  });

  it("removes a pinned tip on teardown and leaves the host's aria-describedby", () => {
    initTooltips();
    const b = refTrigger("Pinned", { toggle: true, describedBy: "help-1 help-2" });
    b.click();
    _resetForTest();
    expect([tip(), b.getAttribute("aria-describedby")]).toEqual([null, "help-1 help-2"]);
  });
});
