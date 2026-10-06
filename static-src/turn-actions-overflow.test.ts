// The turn-actions overflow menu as a GROUPED <details>, plus the desktop path it must not disturb.
// A shared `name` makes the per-turn phone menus exclusive with no JS. The desktop row's buttons
// stay painted only through `::details-content { content-visibility: visible }` on that element,
// so the group is pinned with what it could break. Dismissal: `pointerdown` fires BEFORE `click`,
// so the document listener exempts the menu's subtree or a menu click loses the `open` its
// "Copied" toast depends on (asserted through `silent`).

import { describe, it, expect, vi, beforeAll, afterAll, afterEach } from "vitest";
import { page } from "vitest/browser";

import type { Turn } from "./turns.js";
import type { Entry, EntryText } from "./wire/types.gen.js";
import { loadCSS, mountAppCSS } from "./__test-helpers__/css-rules.js";

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

/** One turn card with a mounted footer, appended to the page. */
function mountCard(id: string): HTMLDetailsElement {
  const card = document.createElement("div");
  card.className = "turn";
  const footer = document.createElement("div");
  footer.className = "turn-footer";
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

describe("the phone layout, at a real viewport size", () => {
  // A media query answers about the VIEWPORT, so the only honest test resizes one.
  // The block restores the entry size in `afterAll`, read off the frame rather than
  // copied from `vitest.config.ts`.
  let entry: { readonly width: number; readonly height: number } | null = null;

  beforeAll(() => {
    entry = { width: window.innerWidth, height: window.innerHeight };
  });

  afterAll(async () => {
    if (entry !== null) {
      await page.viewport(entry.width, entry.height);
    }
  });

  async function phone(): Promise<void> {
    await page.viewport(375, 800);
    expect([window.innerWidth, window.innerHeight], "viewport actually resized").toEqual([
      375, 800,
    ]);
  }

  it("collapses the actions behind the … trigger and reveals them on open", async () => {
    await phone();
    const menu = mountCard("m1");
    const summary = menu.querySelector<HTMLElement>("summary.turn-action-more");
    const group = menu.querySelector<HTMLElement>(".turn-actions-group");
    expect(summary).not.toBeNull();
    expect(group).not.toBeNull();

    // The trigger exists here and only here; on desktop it is `display: none`.
    expect(getComputedStyle(summary as HTMLElement).display).not.toBe("none");
    expect(getComputedStyle(group as HTMLElement).display, "closed hides the group").toBe("none");

    menu.open = true;
    expect(getComputedStyle(group as HTMLElement).display, "open reveals it").toBe("flex");
    expect((group as HTMLElement).getBoundingClientRect().height).toBeGreaterThan(0);
  });

  it("never shows two menus at once, even opened by the trigger", async () => {
    await phone();
    const first = mountCard("m1");
    const second = mountCard("m2");
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
    // And the CONSEQUENCE that matters on a phone: only one menu card is painted.
    const painted = [...document.querySelectorAll<HTMLElement>(".turn-actions-group")].filter(
      (g) => getComputedStyle(g).display !== "none",
    );
    expect(painted, "one floating menu on screen, not two").toHaveLength(1);
  });
});

describe("the desktop path the group must not disturb", () => {
  it("keeps every action painted and hittable while `open` is absent", () => {
    // The browser project's own 1280px viewport, which is above the 40rem media
    // rule, so this is the inline layout.
    const menu = mountCard("m1");
    expect(menu.open, "the inline layout is the CLOSED disclosure").toBe(false);

    // `display: contents` removes the details' own box, not the box the UA hides a
    // closed disclosure through, so this declaration is what cancels the skip.
    expect(getComputedStyle(menu, "::details-content").contentVisibility).toBe("visible");
    // The trigger is not part of the desktop row.
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

/** The block that follows `marker`, brace-matched. */
function blockAfter(css: string, marker: string): string {
  const at = css.indexOf(marker);
  expect(at, `not found in the stylesheet: ${marker}`).toBeGreaterThan(-1);
  const open = css.indexOf("{", at + marker.length);
  let depth = 0;
  for (let i = open; i < css.length; i++) {
    if (css[i] === "{") {
      depth++;
    } else if (css[i] === "}") {
      depth--;
      if (depth === 0) {
        return css.slice(open + 1, i);
      }
    }
  }
  throw new Error(`unbalanced braces after ${marker}`);
}

/** A block's declarations, with comments dropped and whitespace flattened — so the
 *  comparison below is about the rules and not about how deep one arm is nested. */
function declarations(block: string): string {
  return block
    .replace(/\/\*[\s\S]*?\*\//gu, " ")
    .replace(/\s+/gu, " ")
    .trim();
}

describe("the two collapse arms are one rule", () => {
  it("states each threshold exactly once", () => {
    // Or "the two arms" is a claim about whichever pair a reader's grep found
    // first, and a third arm could sit anywhere in the file unnoticed.
    const css = loadCSS("61-mcp-tools.css");
    expect(css.match(/@media \(width <= 40rem\)/gu), "the every-tier arm").toHaveLength(1);
    expect(css.match(/@media \(width <= 56rem\)/gu), "the coarse arm").toHaveLength(1);
  });

  it("carries identical declarations in both", () => {
    // A media query cannot read a custom property, so the QUERY is stated twice and this keeps the two
    // bodies equal.
    const css = loadCSS("61-mcp-tools.css");
    const fine = declarations(blockAfter(css, "@media (width <= 40rem)"));
    const coarse = declarations(
      blockAfter(
        blockAfter(css, "@media (width <= 56rem)"),
        ':where(:root:not([data-pointer="fine"]))',
      ),
    );
    expect(fine, "the arm being compared is the collapsed-actions one").toContain(
      ".turn-actions-more",
    );
    expect(coarse).toBe(fine);
  });
});

describe("the coarse tier collapses at a width a fine pointer does not", () => {
  // 896px IS 56rem, the widest row a coarse pointer collapses, above the 40rem arm and the 48rem no-JS
  // fallback, so only the tier can collapse it. At 1024px NEITHER tier collapses (the third case).
  // The tier is an attribute, so this tests the RULE, not a real tablet. Last in the file and restores
  // its size: `page.viewport` has no getter.
  let entry: { readonly width: number; readonly height: number } | null = null;

  beforeAll(() => {
    entry = { width: window.innerWidth, height: window.innerHeight };
  });

  afterAll(async () => {
    document.documentElement.removeAttribute("data-pointer");
    if (entry !== null) {
      await page.viewport(entry.width, entry.height);
    }
  });

  async function at(width: number, tier: "fine" | "coarse"): Promise<void> {
    await page.viewport(width, 900);
    expect([window.innerWidth, window.innerHeight], "viewport actually resized").toEqual([
      width,
      900,
    ]);
    document.documentElement.dataset["pointer"] = tier;
  }

  it("hides the closed group and paints the … trigger on a coarse pointer", async () => {
    await at(896, "coarse");
    const menu = mountCard("m1");
    const summary = menu.querySelector<HTMLElement>("summary.turn-action-more");
    const group = menu.querySelector<HTMLElement>(".turn-actions-group");
    expect(summary).not.toBeNull();
    expect(group).not.toBeNull();
    expect(getComputedStyle(group as HTMLElement).display, "closed hides the group").toBe("none");
    expect(getComputedStyle(summary as HTMLElement).display, "the trigger is painted").not.toBe(
      "none",
    );
  });

  it("keeps the actions inline at the same width on a fine pointer", async () => {
    await at(896, "fine");
    const menu = mountCard("m1");
    const summary = menu.querySelector<HTMLElement>("summary.turn-action-more");
    const group = menu.querySelector<HTMLElement>(".turn-actions-group");
    expect(getComputedStyle(group as HTMLElement).display, "the row is inline").toBe("inline-flex");
    expect(getComputedStyle(summary as HTMLElement).display, "no trigger to press").toBe("none");
  });

  it("leaves a coarse pointer inline once the row is wider than the arm", async () => {
    await at(1024, "coarse");
    const menu = mountCard("m1");
    const group = menu.querySelector<HTMLElement>(".turn-actions-group");
    expect(getComputedStyle(group as HTMLElement).display, "56rem is a bound, not a tier").toBe(
      "inline-flex",
    );
  });
});
