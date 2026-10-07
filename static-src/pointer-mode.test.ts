// The touch/mouse toggle: its three visibility conditions, the click, and the a11y triple.

import { describe, it, expect, beforeAll, beforeEach, afterEach, afterAll, vi } from "vitest";
// The viewport control, for the one gate that can only be measured by resizing. `vitest/browser` is
// the Vitest 5 spelling; `@vitest/browser/context` is a stub that throws.
import { page } from "vitest/browser";
import indexHtml from "../static/index.html?raw";

import { initPointerModeToggle, revealPointerModeToggle } from "./pointer-mode.js";
import { initPointerTier, currentTier } from "./pointer-tier.js";
import { markCoarseSeen, pointerModeChoice, setPointerModeChoice } from "./device-view.js";
import { loadCSS, mountAppCSS, ruleBody } from "./__test-helpers__/css-rules.js";

/** The real button, lifted out of the page by id. */
function markupFor(anchor: string): string {
  const at = indexHtml.indexOf(anchor);
  expect(at, `static/index.html has no ${anchor}`).toBeGreaterThan(-1);
  const open = indexHtml.lastIndexOf("<button", at);
  const end = indexHtml.indexOf("</button>", at);
  expect(end, `${anchor} is not inside a button`).toBeGreaterThan(at);
  return indexHtml.slice(open, end + "</button>".length);
}

function mountButton(): HTMLButtonElement {
  document.body.innerHTML = markupFor('id="pointer-mode-btn"');
  const btn = document.getElementById("pointer-mode-btn");
  expect(btn).not.toBeNull();
  return btn as HTMLButtonElement;
}

/** The whole `<div …>…</div>` opening with `openTag`, matched by counting nested div tags.
 *  Nothing formats `static/index.html` — prettier runs from `static-src/` and the page is its
 *  sibling — so its whitespace is not a contract and a blank-line delimiter would break on a
 *  reindent. */
function divAt(openTag: string): string {
  const start = indexHtml.indexOf(openTag);
  expect(start, `static/index.html has no ${openTag}`).toBeGreaterThan(-1);
  const tags = /<div\b|<\/div\s*>/g;
  tags.lastIndex = start;
  let depth = 0;
  for (let m = tags.exec(indexHtml); m !== null; m = tags.exec(indexHtml)) {
    depth += m[0].startsWith("</") ? -1 : 1;
    if (depth === 0) {
      return indexHtml.slice(start, m.index + m[0].length);
    }
  }
  throw new Error(`unbalanced divs after ${openTag}`);
}

function hiddenGlyphs(btn: HTMLElement): string[] {
  return [...btn.querySelectorAll("svg.hidden")].map((el) =>
    el.classList.contains("pointer-icon-fine") ? "fine" : "coarse",
  );
}

beforeEach(() => {
  localStorage.clear();
  document.documentElement.removeAttribute("data-pointer");
});

afterEach(() => {
  localStorage.clear();
  document.documentElement.removeAttribute("data-pointer");
  document.body.innerHTML = "";
});

describe("the toggle's visibility", () => {
  it("stays hidden on a device that has never reported a coarse pointer", () => {
    const btn = mountButton();
    initPointerTier();
    initPointerModeToggle();

    expect(btn.classList.contains("hidden")).toBe(true);
  });

  it("is shown once the sticky flag is set", () => {
    markCoarseSeen();
    const btn = mountButton();
    initPointerTier();
    initPointerModeToggle();

    expect(btn.classList.contains("hidden")).toBe(false);
  });

  it("is shown on fresh storage when the platform reports touch points", () => {
    vi.spyOn(Navigator.prototype, "maxTouchPoints", "get").mockReturnValue(5);
    const btn = mountButton();
    initPointerTier();
    initPointerModeToggle();

    expect(btn.classList.contains("hidden")).toBe(false);
  });

  it("is shown on fresh storage when any pointer reports coarse, in mouse mode too", () => {
    vi.stubGlobal("matchMedia", (query: string) => ({
      media: query,
      matches: query === "(any-pointer: coarse)",
    }));
    setPointerModeChoice("fine");
    const btn = mountButton();
    initPointerTier();
    initPointerModeToggle();

    expect(currentTier()).toBe("fine");
    expect(btn.classList.contains("hidden")).toBe(false);
  });

  it("is revealed by the callback the tier's observer fires, once per device", () => {
    const btn = mountButton();
    initPointerTier({ onCoarseSeen: revealPointerModeToggle });
    initPointerModeToggle();
    expect(btn.classList.contains("hidden")).toBe(true);

    btn.dispatchEvent(new PointerEvent("pointerdown", { bubbles: true, pointerType: "touch" }));
    expect(btn.classList.contains("hidden")).toBe(false);

    // Idempotent, so the composition root needs no guard of its own.
    revealPointerModeToggle();
    expect(btn.classList.contains("hidden")).toBe(false);
  });
});

describe("the toggle's click", () => {
  it("flips the mode, persists the choice, and moves data-pointer", () => {
    markCoarseSeen();
    const btn = mountButton();
    initPointerTier();
    initPointerModeToggle();
    expect(currentTier()).toBe("fine");

    btn.click();
    expect(currentTier()).toBe("coarse");
    expect(pointerModeChoice()).toBe("coarse");

    btn.click();
    expect(currentTier()).toBe("fine");
    expect(pointerModeChoice()).toBe("fine");
  });

  it("swaps which glyph carries .hidden", async () => {
    markCoarseSeen();
    const btn = mountButton();
    initPointerTier();
    initPointerModeToggle();
    // The FIRST paint has nothing on screen to slide out, so it settles synchronously — which is
    // also what puts the right glyph up on a cold load.
    expect(hiddenGlyphs(btn)).toEqual(["coarse"]);

    btn.click();
    // The swap waits for the outgoing glyph's slide, so it is the one part of the click that is not
    // synchronous. No stylesheet is mounted here, so nothing transitions and the settle arrives on
    // the module's own safety timeout — which is the same path reduced motion takes in production.
    await vi.waitFor(() => {
      expect(hiddenGlyphs(btn)).toEqual(["fine"]);
    });
  });

  it("converges on the second click when two land inside one slide", async () => {
    // The first click's settle is deferred behind the outgoing glyph's slide, so it is still
    // pending when the second click paints.
    markCoarseSeen();
    const btn = mountButton();
    initPointerTier();
    initPointerModeToggle();

    btn.click();
    btn.click();
    expect(currentTier()).toBe("fine");
    expect(hiddenGlyphs(btn)).toEqual(["coarse"]);
    expect([...btn.querySelectorAll("svg.icon-setting")], "a glyph left mid-slide").toEqual([]);

    // The first click's 350ms safety timeout still has to arrive and do nothing.
    await new Promise((resolve) => setTimeout(resolve, 400));
    expect(hiddenGlyphs(btn)).toEqual(["coarse"]);
    expect(btn.getAttribute("aria-pressed")).toBe("false");
  });

  it("rides `transform`, leaving the `translate` property to icon-crisp", () => {
    // A cross-module coupling no rendering test would name: this slide and `icon-crisp.ts` must
    // not both write the `translate` property on the same element.
    const sheet = loadCSS("10-shell-app.css");
    const SLIDE = ':is([id="theme-btn"], [id="pointer-mode-btn"]) svg';
    const base = ruleBody(sheet, SLIDE);
    expect(base, "the slide transitions `transform`").toMatch(/transition:[^;]*\btransform\b/);
    expect(base, "and NOT `translate`, which icon-crisp owns").not.toMatch(
      /transition:[^;]*\btranslate\b/,
    );
    for (const state of ["icon-setting", "icon-rising"]) {
      const body = ruleBody(sheet, `${SLIDE}.${state}`);
      expect(body, `${state} offsets with transform`).toMatch(/transform:\s*translateY\(/);
      expect(body, `${state} sets no \`translate\` property`).not.toMatch(/(^|[;\s])translate:/);
    }
  });

  it("flips aria-pressed and the tooltip while the accessible name stays put", () => {
    markCoarseSeen();
    const btn = mountButton();
    initPointerTier();
    initPointerModeToggle();

    expect(btn.getAttribute("aria-pressed")).toBe("false");
    expect(btn.getAttribute("aria-label")).toBe("Touch mode");
    expect(btn.getAttribute("data-tooltip")).toBe("Mouse mode. Switch to touch mode");

    btn.click();
    expect(btn.getAttribute("aria-pressed")).toBe("true");
    expect(btn.getAttribute("aria-label")).toBe("Touch mode");
    expect(btn.getAttribute("data-tooltip")).toBe("Touch mode. Switch to mouse mode");
  });

  it("reports the tier a stored choice put the document in", () => {
    // The button paints from what is IN FORCE, not from a default: a device that chose the enlarged
    // tier must not come back offering to enlarge it again.
    setPointerModeChoice("coarse");
    markCoarseSeen();
    const btn = mountButton();
    initPointerTier();
    initPointerModeToggle();

    expect(btn.getAttribute("aria-pressed")).toBe("true");
    expect(btn.getAttribute("aria-label")).toBe("Touch mode");
    expect(hiddenGlyphs(btn)).toEqual(["fine"]);
  });

  it("keeps one accessible name in both states while the tooltip carries the state", () => {
    // `aria-pressed` is the state channel, so the name may not also change with the state:
    // announced, a flipping action name reads as "Switch to mouse mode, pressed", attaching a state
    // to a phrase about the next press. The tooltip has no such channel beside it, so it says both.
    markCoarseSeen();
    const btn = mountButton();
    initPointerTier();
    initPointerModeToggle();

    const before = {
      label: btn.getAttribute("aria-label") ?? "",
      tooltip: btn.getAttribute("data-tooltip") ?? "",
    };
    btn.click();
    const after = {
      label: btn.getAttribute("aria-label") ?? "",
      tooltip: btn.getAttribute("data-tooltip") ?? "",
    };

    expect(after.label, "the accessible name is stable across a toggle").toBe(before.label);
    expect(before.label, "the name carries no action").not.toMatch(/\bSwitch\b/);
    expect(before.tooltip, "the tooltip names the state, then the action").toMatch(
      /^.+ mode\. Switch to .+ mode$/,
    );
    expect(after.tooltip, "the tooltip names the state, then the action").toMatch(
      /^.+ mode\. Switch to .+ mode$/,
    );
    expect(after.tooltip, "the tooltip's state half moves with the tier").not.toBe(before.tooltip);
  });
});

describe("the toolbar row at phone width", () => {
  // Nullable and cleaned up conditionally, so a failure BEFORE the fixture is built reports itself
  // rather than being replaced by a hook error on undefined.
  let styleEl: HTMLStyleElement | null = null;
  let app: HTMLElement | null = null;

  afterAll(() => {
    styleEl?.remove();
    app?.remove();
  });

  /** The bar at a stated chat-area width, with the coarse tier in force. Returns the visible
   *  action buttons; the viewport here is the browser project's 1280px, so `width <= 48rem` does
   *  not apply and `#menu-toggle` keeps its `display: none` — a real phone shows the hamburger
   *  too, and that is the one button these cases cannot account for. */
  function mountBarAt(width: string): HTMLElement[] {
    styleEl ??= mountAppCSS();
    app?.remove();
    app = document.createElement("div");
    app.id = "app";
    app.innerHTML = `<main id="chat-area">${divAt('<div class="chat-toolbar">')}</main>`;
    document.body.appendChild(app);
    const area = app.querySelector<HTMLElement>("#chat-area");
    expect(area).not.toBeNull();
    area?.style.setProperty("inline-size", width);
    document.documentElement.setAttribute("data-pointer", "coarse");
    return [...app.querySelectorAll<HTMLElement>(".chat-toolbar > button")].filter(
      (b) => b.offsetParent !== null,
    );
  }

  it("keeps every toolbar button on one row at 390px with coarse controls", () => {
    // Settings moved into this bar, so the row carries one more 44px touch target than it did, and
    // `.chat-toolbar` wraps rather than overflowing — this is the measurement that says whether it
    // has to.
    const buttons = mountBarAt("390px");
    expect(buttons.length, "the persistent toolbar buttons").toBe(7);
    const tops = [...new Set(buttons.map((b) => b.offsetTop))];
    const widths = buttons.map((b) => `${b.id}:${String(b.offsetWidth)}`).join(" ");
    expect(tops, `one row expected; button widths were ${widths}`).toHaveLength(1);
  });

  it("ends the actions on the bar's own gutter, with the heading on their row", () => {
    // The two halves of what the heading's own row cost, in one case because they are one layout.
    const buttons = mountBarAt("520px");
    const bar = app?.querySelector<HTMLElement>(".chat-toolbar");
    const heading = app?.querySelector<HTMLElement>(".titlebar-heading");
    expect(bar).not.toBeNull();
    expect(heading).not.toBeNull();

    const barBox = bar!.getBoundingClientRect();
    const gutter = parseFloat(getComputedStyle(bar!).paddingInlineEnd);
    const last = buttons[buttons.length - 1]!.getBoundingClientRect();
    expect(barBox.right - gutter - last.right, "slack left of the actions").toBeCloseTo(0, 0);

    // Same row as the actions, and to their left. CENTRES rather than top edges: the bar is
    // `align-items: center` over a 19px heading and 44px buttons, so equal tops would be the wrong
    // assertion and fail on a correct layout.
    const headBox = heading!.getBoundingClientRect();
    const mid = (r: DOMRect) => r.top + r.height / 2;
    expect(mid(headBox), "the heading shares the actions' row").toBeCloseTo(mid(last), 0);
    expect(headBox.left).toBeLessThan(buttons[0]!.getBoundingClientRect().left);
  });

  it("keeps the actions on the gutter once page-title.ts clips the heading", () => {
    // The state a phone actually renders, and the only one that pins the bar's own
    // `justify-content: flex-end`: `.sr-only` is `position: absolute`, so a clipped heading leaves
    // the flex line and takes its growth with it.
    const buttons = mountBarAt("390px");
    const bar = app?.querySelector<HTMLElement>(".chat-toolbar");
    app?.querySelector<HTMLElement>(".titlebar-heading")?.classList.add("sr-only");

    const barBox = bar!.getBoundingClientRect();
    const gutter = parseFloat(getComputedStyle(bar!).paddingInlineEnd);
    const last = buttons[buttons.length - 1]!.getBoundingClientRect();
    expect(barBox.right - gutter - last.right, "slack left of the actions").toBeCloseTo(0, 0);
  });

  it("wraps rather than pushing an action off the start edge at 320px", () => {
    // What the bar's `flex-wrap: wrap` is for, and the reason right-alignment cannot be the whole
    // rule: the actions need 326px of a 320px phone (7x44 + 6x2 + 24 of padding here, 8 buttons on
    // a real one), they cannot shrink into it because `min-width: var(--btn-h)` is the touch floor,
    // and overflow past `flex-end` leaves a control left of the bar with no scroll to reach it.
    const buttons = mountBarAt("320px");
    const bar = app?.querySelector<HTMLElement>(".chat-toolbar");
    const startEdge =
      bar!.getBoundingClientRect().left + parseFloat(getComputedStyle(bar!).paddingInlineStart);
    const offEdge = buttons.filter((b) => b.getBoundingClientRect().left < startEdge - 0.5);
    expect(
      offEdge.map((b) => b.id),
      "actions left of the bar's own gutter",
    ).toEqual([]);
  });
});

describe("the phone-shaped gate, measured at real viewport sizes", () => {
  // A media query answers about the VIEWPORT, so the only honest test of this gate resizes one. The
  // block restores the size in `afterAll`, because the toolbar cases above read the project's own.
  let entry: { readonly width: number; readonly height: number } | null = null;
  let styleEl: HTMLStyleElement | null = null;

  beforeAll(() => {
    entry = { width: window.innerWidth, height: window.innerHeight };
    styleEl = mountAppCSS();
  });

  afterAll(async () => {
    styleEl?.remove();
    if (entry !== null) {
      await page.viewport(entry.width, entry.height);
    }
  });

  /** The toggle's painted box on a touch-capable device in touch mode, both gates applied. This
   *  project's page has a mouse, so `any-pointer: fine` holds; the touch-only half is in
   *  `pointer-mode.touch.test.ts`. */
  async function paintedAt(width: number, height: number): Promise<DOMRect> {
    await page.viewport(width, height);
    expect([window.innerWidth, window.innerHeight], "viewport actually resized").toEqual([
      width,
      height,
    ]);
    vi.spyOn(Navigator.prototype, "maxTouchPoints", "get").mockReturnValue(5);
    setPointerModeChoice("coarse");
    const btn = mountButton();
    initPointerTier();
    initPointerModeToggle();
    expect(currentTier()).toBe("coarse");
    return btn.getBoundingClientRect();
  }

  it.each([
    ["a phone-sized portrait window", 360, 800],
    ["an iPad mini in portrait", 744, 1133],
    ["a short landscape window", 900, 400],
    ["a tablet in landscape", 1024, 768],
  ] as const)("paints the toggle on %s with a fine pointer attached", async (_, width, height) => {
    const box = await paintedAt(width, height);
    expect(box.width).toBeGreaterThan(0);
    expect(box.height).toBeGreaterThan(0);
  });
});

describe("the coarse glyph", () => {
  it("keeps folded fingers beside the extended one", () => {
    // No other gate can see this: prettier never formats
    // `static/index.html`, html-validate does not read path data, and `menu-icons.test.ts` only
    // asks whether the six header glyphs differ from each other.
    const glyph = markupFor('id="pointer-mode-btn"').match(
      /<svg class="pointer-icon-coarse[\s\S]*?<\/svg>/,
    )?.[0];
    expect(glyph, "the coarse glyph is in the button").toBeDefined();
    const subpaths = [...(glyph ?? "").matchAll(/<path\b/g)].length;
    expect(
      subpaths,
      "an extended finger plus a palm is two paths; a hand needs more",
    ).toBeGreaterThanOrEqual(4);
  });
});

describe("the ON state's paint", () => {
  let styleEl: HTMLStyleElement | null = null;

  afterAll(() => {
    styleEl?.remove();
  });

  /** The button's background in both `aria-pressed` states. `.icon-btn` transitions
   *  `background`, so a read taken straight after the flip returns an interpolated value —
   *  finishing the transitions is what makes the second reading the settled one rather than a
   *  sample of the ramp. */
  function fills(btn: HTMLElement): { off: string; on: string } {
    btn.setAttribute("aria-pressed", "false");
    btn.getAnimations().forEach((a) => a.finish());
    const off = getComputedStyle(btn).backgroundColor;
    btn.setAttribute("aria-pressed", "true");
    btn.getAnimations().forEach((a) => a.finish());
    return { off, on: getComputedStyle(btn).backgroundColor };
  }

  it("does not take the selected fill, while a sibling icon-btn still does", () => {
    // Reported as "it keeps an active background when it should not".
    styleEl ??= mountAppCSS();
    document.body.innerHTML = `${markupFor('id="pointer-mode-btn"')}${markupFor('id="find-btn"')}`;
    const pointer = document.getElementById("pointer-mode-btn");
    const find = document.getElementById("find-btn");
    expect(pointer).not.toBeNull();
    expect(find).not.toBeNull();

    const p = fills(pointer as HTMLElement);
    const f = fills(find as HTMLElement);

    expect(p.on, "the toggle's fill is unmoved by aria-pressed").toBe(p.off);
    expect(f.on, "the shared selected fill still reaches an ordinary icon-btn").not.toBe(f.off);
  });
});
