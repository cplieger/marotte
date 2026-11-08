// The turn-actions overflow menu as a GROUPED <details>, plus the inline path it must not disturb.
// A shared `name` makes the per-turn menus exclusive with no JS. The inline row's buttons
// stay painted only through `::details-content { content-visibility: visible }` on that element,
// so the group is pinned with what it could break. Dismissal: `pointerdown` fires BEFORE `click`,
// so the document listener exempts the menu's subtree or a menu click loses the `open` its
// "Copied" toast depends on (asserted through `silent`).

import { describe, it, expect, vi, beforeAll, afterAll, afterEach } from "vitest";

import type { Turn } from "./turns.js";
import type { Entry, EntryText } from "./wire/types.gen.js";
import { mountAppCSS } from "./__test-helpers__/css-rules.js";

vi.mock("./store.js", () => ({
  getActive: () => ({ id: "c1", name: "chat", messages: [] }),
  getActiveId: () => "c1",
  get: vi.fn(() => undefined),
  getSessions: vi.fn(() => []),
  tabStatusFor: vi.fn(() => ""),
}));
vi.mock("./actions/messages.js", () => ({
  copyClipboard: { dispatch: vi.fn(() => Promise.resolve()) },
}));
vi.mock("./chat-export.js", () => ({ downloadChatExport: vi.fn() }));
vi.mock("./notice-subject.js", () => ({ chatNotice: vi.fn() }));
vi.mock("./chat.js", () => ({ openTangentChat: vi.fn() }));

const { mountTurnFooterActions, initTurnActionCallbacks } =
  await import("./messages-turn-actions.js");
const { copyClipboard } = await import("./actions/messages.js");

initTurnActionCallbacks({
  svgTemplate: () => () => document.createElement("span"),
});

let style: HTMLStyleElement;

beforeAll(() => {
  style = mountAppCSS();
});

afterAll(() => {
  style.remove();
});

afterEach(() => {
  document.body.replaceChildren();
});

/** `width` narrows the footer, so its four actions (`branch`) do not all fit and `…` collapses them. */
function mountCard(id: string, width?: string): HTMLDetailsElement {
  const card = document.createElement("div");
  card.className = "turn";
  const footer = document.createElement("div");
  footer.className = "turn-footer";
  if (width !== undefined) {
    footer.style.width = width;
  }
  card.appendChild(footer);
  document.body.appendChild(card);
  // The body needs a `text` entry: `mountTurnFooterActions` returns early on an empty `turnMarkdown`.
  const payload: EntryText = { text: "reply" };
  const reply: Entry = { id: `${id}-e1`, turn: id, kind: "text", seq: 1, ts: 1, payload };
  const turn: Turn = {
    id,
    n: 1,
    trigger: undefined,
    body: [reply],
    openEntries: new Map(),
    ts: 1,
    outcome: "completed",
    rewindTo: undefined,
    branchFrom: width === undefined ? undefined : { id: `${id}-p`, text: "prompt" },
  };
  mountTurnFooterActions(footer, card, turn);
  const menu = footer.querySelector<HTMLDetailsElement>("details.turn-actions-more");
  expect(menu, "the footer mounts an overflow details").not.toBeNull();
  if (menu === null) {
    throw new Error("no overflow menu");
  }
  return menu;
}

describe("the overflow menu is an exclusive group", () => {
  it("declares the shared group name on every turn's menu", () => {
    const first = mountCard("m1");
    const second = mountCard("m2");
    // One name, so the group has more than one member — which is what makes the
    // attribute do anything at all.
    expect(first.name).toBe("turn-actions-overflow");
    expect(second.name).toBe(first.name);
  });

  it("closes the other turn's menu when one opens", () => {
    const first = mountCard("m1");
    const second = mountCard("m2");

    first.open = true;
    expect([first.open, second.open]).toEqual([true, false]);

    // The whole point: opening the second must not leave the first standing.
    second.open = true;
    expect([first.open, second.open], "at most one menu is open").toEqual([false, true]);
  });

  it("still closes on its own action, which is the self-scoped path", () => {
    const menu = mountCard("m1");
    menu.open = true;
    const btn = menu.querySelector<HTMLButtonElement>(".turn-actions-group .turn-action-btn");
    expect(btn).not.toBeNull();
    btn?.click();
    expect(menu.open).toBe(false);
  });
});

/** Two frames: the fit runs on the frame after the mount. */
function fitted(): Promise<void> {
  return new Promise((resolve) => {
    requestAnimationFrame(() => {
      requestAnimationFrame(() => {
        resolve();
      });
    });
  });
}

describe("the menu layout, once two or more actions do not fit", () => {
  // 100px leaves room for the `…` and one action on either tier; the other three go behind it.
  const NARROW = "100px";

  afterEach(() => {
    document.documentElement.removeAttribute("data-pointer");
  });

  for (const tier of ["fine", "coarse"] as const) {
    it(`collapses the actions behind the … trigger and reveals them on open, on the ${tier} tier`, async () => {
      document.documentElement.dataset["pointer"] = tier;
      const menu = mountCard("m1", NARROW);
      await fitted();
      const summary = menu.querySelector<HTMLElement>("summary.turn-action-more");
      const group = menu.querySelector<HTMLElement>(".turn-actions-group");
      expect(getComputedStyle(summary as HTMLElement).display).not.toBe("none");
      expect(getComputedStyle(group as HTMLElement).display, "closed hides the group").toBe("none");
      expect(group?.children.length, "the menu holds two or more").toBeGreaterThanOrEqual(2);

      menu.open = true;
      expect(getComputedStyle(group as HTMLElement).display, "open reveals it").toBe("flex");
      expect((group as HTMLElement).getBoundingClientRect().height).toBeGreaterThan(0);
    });
  }

  it("never shows two menus at once, even opened by the trigger", async () => {
    const first = mountCard("m1", NARROW);
    const second = mountCard("m2", NARROW);
    await fitted();
    const trigger = (m: HTMLDetailsElement): HTMLElement => {
      const s = m.querySelector<HTMLElement>("summary.turn-action-more");
      if (s === null) {
        throw new Error("no trigger");
      }
      return s;
    };

    trigger(first).click();
    expect(first.open).toBe(true);
    trigger(second).click();

    expect([first.open, second.open]).toEqual([false, true]);
    // And the consequence that matters: only one menu card is painted.
    const painted = [...document.querySelectorAll<HTMLElement>(".turn-actions-group")].filter(
      (g) => getComputedStyle(g).display !== "none",
    );
    expect(painted, "one floating menu on screen, not two").toHaveLength(1);
  });

  it("fades the menu in on open, at the context menu's enter motion", async () => {
    const menu = mountCard("m1", NARROW);
    await fitted();
    const group = menu.querySelector<HTMLElement>(".turn-actions-group");
    if (group === null) {
      throw new Error("no group");
    }
    menu.open = true;
    const fades = group
      .getAnimations()
      .filter((a) => a instanceof CSSTransition && a.transitionProperty === "opacity");
    expect(fades, "one opacity entry").toHaveLength(1);
    const root = getComputedStyle(document.documentElement);
    const want = root.getPropertyValue("--uip-popover-enter-duration").trim();
    expect(fades[0]?.effect?.getTiming().duration).toBe(
      want.endsWith("ms") ? parseFloat(want) : parseFloat(want) * 1000,
    );
    menu.open = false;
  });

  it("puts the actions back inline once the footer is wide enough", async () => {
    const menu = mountCard("m1", NARROW);
    await fitted();
    const footer = menu.closest<HTMLElement>(".turn-footer") as HTMLElement;
    footer.style.width = "";
    await fitted();
    const summary = menu.querySelector<HTMLElement>("summary.turn-action-more");
    expect(getComputedStyle(summary as HTMLElement).display, "no trigger").toBe("none");
    const painted = [
      ...footer.querySelectorAll<HTMLElement>(".turn-action-btn:not(.turn-action-more)"),
    ];
    expect(
      painted.map((b) => b.getAttribute("aria-label")),
      "in reading order",
    ).toEqual(["Copy as text", "Copy as markdown", "Copy link to this turn", "Branch from here"]);
    expect(painted.every((b) => b.checkVisibility())).toBe(true);
  });
});

describe("the inline path (every action fits) the group must not disturb", () => {
  it("keeps every action painted and hittable while `open` is absent", () => {
    // The browser project's own 1280px viewport, where every action fits, so this is the inline layout.
    const menu = mountCard("m1");
    expect(menu.open, "the inline layout is the CLOSED disclosure").toBe(false);

    // `display: contents` removes the details' own box, not the box the UA hides a
    // closed disclosure through, so this declaration is what cancels the skip.
    expect(getComputedStyle(menu, "::details-content").contentVisibility).toBe("visible");
    // The trigger is not painted while the actions fit inline.
    const summary = menu.querySelector<HTMLElement>("summary.turn-action-more");
    expect(getComputedStyle(summary as HTMLElement).display).toBe("none");

    const btns = [...menu.querySelectorAll<HTMLElement>(".turn-actions-group .turn-action-btn")];
    expect(btns).toHaveLength(3);
    for (const btn of btns) {
      const box = btn.getBoundingClientRect();
      expect(box.width, "a laid-out box, not a skipped subtree").toBeGreaterThan(0);
      expect(box.height).toBeGreaterThan(0);
      // Hittable: the element at its own centre is the button or something inside
      // it. A `content-visibility: hidden` subtree fails this even while its boxes
      // still measure, which is why the geometry check alone is not the assertion.
      const hit = document.elementFromPoint(box.left + box.width / 2, box.top + box.height / 2);
      expect(hit === null ? null : btn.contains(hit) || hit === btn).toBe(true);
    }
  });
});

/** The menu's first action button, which is Copy as text. */
function firstAction(menu: HTMLDetailsElement): HTMLButtonElement {
  const btn = menu.querySelector<HTMLButtonElement>(".turn-actions-group .turn-action-btn");
  expect(btn, "the group holds action buttons").not.toBeNull();
  if (btn === null) {
    throw new Error("no action button");
  }
  return btn;
}

/** A real pointer press on `node`, which is what puts the dismissal listener ahead
 *  of the button's own click the way a finger or a mouse does. */
function press(node: EventTarget): void {
  node.dispatchEvent(new PointerEvent("pointerdown", { bubbles: true }));
}

/** The `silent` flag the click's `copyClipboard.dispatch` carried: `false` asks for the "Copied"
 *  toast an OPEN-menu click needs. */
function dispatchedSilent(): boolean | undefined {
  const { calls } = vi.mocked(copyClipboard.dispatch).mock;
  expect(calls, "the copy action was dispatched exactly once").toHaveLength(1);
  return calls[0]?.[1]?.silent;
}

describe("dismissal, which the native disclosure does not supply", () => {
  it("closes an open menu on a pointerdown outside it", () => {
    const menu = mountCard("m1");
    menu.open = true;
    press(document.body);
    expect(menu.open).toBe(false);
  });

  it("leaves it open on a pointerdown INSIDE it", () => {
    const menu = mountCard("m1");
    menu.open = true;
    press(firstAction(menu));
    expect(menu.open).toBe(true);
  });

  it("closes an open menu on Escape", () => {
    const menu = mountCard("m1");
    menu.open = true;
    document.body.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true }));
    expect(menu.open).toBe(false);
  });

  it("lets Escape keep propagating, so the app's own handling still sees it", () => {
    mountCard("m1");
    let reached = false;
    const spy = (): void => {
      reached = true;
    };
    // On `window`, which is past `document` in the bubble order, so it is reached
    // only if the dismissal did not stop propagation.
    window.addEventListener("keydown", spy);
    document.body.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true }));
    window.removeEventListener("keydown", spy);
    expect(reached).toBe(true);
  });

  it("keeps an action's handler reading open===true when the click came from the menu", () => {
    const menu = mountCard("m1");
    menu.open = true;
    const btn = firstAction(menu);
    press(btn);
    btn.click();
    expect(dispatchedSilent(), "the menu click asked for the toast").toBe(false);
  });

  it("still closes the menu after that action's handler has run", () => {
    const menu = mountCard("m1");
    menu.open = true;
    const btn = firstAction(menu);
    press(btn);
    btn.click();
    expect(menu.open).toBe(false);
  });

  it("keeps the toast suppressed for a click made with the menu closed", () => {
    // The control that makes the case above mean something: `silent` is a
    // function of the menu's state, not a constant.
    const menu = mountCard("m1");
    const btn = firstAction(menu);
    press(btn);
    btn.click();
    expect(dispatchedSilent()).toBe(true);
  });
});
