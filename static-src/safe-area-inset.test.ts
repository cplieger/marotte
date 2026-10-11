// HOW THE APP SPENDS `env(safe-area-inset-*)`, measured.
import { describe, it, expect, beforeAll, afterAll } from "vitest";

import { mountAppCSS } from "./__test-helpers__/css-rules.js";

const INSET_TOP = 47;
const INSET_BOTTOM = 34;
/** A notched iPhone in LANDSCAPE, where the notch inset lands on an inline edge rather than on
 *  the top. This is the orientation the inline rules exist for, and the one where the `(width <=
 *  48rem)` phone block stops matching (844px on an iPhone 15), so the base `.bottom-bar` rule
 *  governs alone. */
const INSET_SIDE = 59;
/** Chosen against two BOUNDS rather than a datasheet, because every assertion below reads the
 *  constant and a retune moves the whole block: above `--sp-2` (8px), or a case cannot tell an
 *  inset-reading rule from its house value, and under `--composer-inset-cap` (24px), so no case is
 *  accidentally measuring the cap. */
const INSET_BOTTOM_IPAD = 20;

let style: HTMLStyleElement;
let frame: HTMLIFrameElement;
let doc: Document;
let sheet: HTMLStyleElement;
/** A second frame in landscape, for the two rules whose behaviour depends on whether
 *  `50-mobile.css`'s `(width <= 48rem)` arm matches. */
let wide: HTMLIFrameElement;
let wideDoc: Document;
let wideSheet: HTMLStyleElement;
/** A third frame at an installed iPad's window geometry, 1106 x 829 CSS px, which is the DESKTOP
 *  branch — 1106px is past `(width <= 48rem)`, so every rule the phone block carries is inert
 *  and only the unconditional ones answer. A coarse pointer like its siblings, because the tier
 *  is the pointer rather than the width (01-tokens.css). */
let ipad: HTMLIFrameElement;
let ipadDoc: Document;
let ipadSheet: HTMLStyleElement;

/** The shipped bundle with the device's values substituted for its `env()` reads, or unchanged
 *  when `on` is false. `side` is the inline inset: it defaults to 0 so every existing case keeps
 *  neutralizing left/right exactly as it did. `bottom` is a PARAMETER rather than a second pass
 *  over the substituted text, because the */
function substituted(on: boolean, side: number, bottom: number = INSET_BOTTOM): string {
  const css = style.textContent ?? "";
  return on
    ? css
        .replace(/env\(\s*safe-area-inset-top\s*(?:,[^)]*)?\)/g, `${INSET_TOP}px`)
        .replace(/env\(\s*safe-area-inset-bottom\s*(?:,[^)]*)?\)/g, `${bottom}px`)
        .replace(/env\(\s*safe-area-inset-(?:left|right)\s*(?:,[^)]*)?\)/g, `${side}px`)
    : css;
}

/** Swap the sheet in the phone frame for one with the iPhone values substituted, or (`false`)
 *  for the shipped one, where every inset resolves to 0. */
function withInsets(on: boolean, side = 0): void {
  sheet.textContent = substituted(on, side);
}

/** The same swap in the landscape frame. */
function withWideInsets(on: boolean, side = 0): void {
  wideSheet.textContent = substituted(on, side);
}

/** The same swap in the iPad frame, carrying that device's own bottom band. */
function withIpadInsets(on: boolean, side = 0): void {
  ipadSheet.textContent = substituted(on, side, INSET_BOTTOM_IPAD);
}

/** `getComputedStyle` from the element's OWN view, which is what a fixture inside an iframe
 *  needs. */
function styleOf(el: Element): CSSStyleDeclaration {
  const view = el.ownerDocument.defaultView;
  if (view === null) {
    throw new Error("element has no view");
  }
  return view.getComputedStyle(el);
}

/** The composer as `static/index.html` authors it, down to the one pill the measurement is
 *  about. `#prompt-form` is `#chat-area`'s last flex child, so in production its block-end
 *  border edge IS the viewport's bottom — which is what makes a gap measured against this
 *  element a gap to the screen edge. */
function mountComposer(d: Document = doc): {
  form: HTMLElement;
  box: HTMLElement;
  pill: HTMLElement;
} {
  const form = d.createElement("form");
  form.id = "prompt-form";
  form.className = "bottom-bar";

  const box = d.createElement("div");
  box.className = "prompt-box";
  const ta = d.createElement("textarea");
  ta.id = "prompt-input";
  const pills = d.createElement("div");
  pills.className = "prompt-pills";
  const slot = d.createElement("span");
  slot.className = "pill-slot";
  const pill = d.createElement("button");
  pill.type = "button";
  pill.className = "send-btn";
  const glyph = d.createElementNS("http://www.w3.org/2000/svg", "svg");
  glyph.setAttribute("class", "ic-ui");
  pill.appendChild(glyph);
  slot.appendChild(pill);
  pills.appendChild(slot);
  box.append(ta, pills);
  form.appendChild(box);
  d.body.replaceChildren(form);
  return { form, box, pill };
}

/** The sidebar as `static/index.html` authors it, down to the three children the measurement
 *  needs: the header, the `flex: 1` tab list that pushes the footer down, and the footer itself.
 *  Without that middle element the footer sits at the TOP of the panel and the gap to the
 *  panel's own bottom edge measures the panel's leftover space rather than its inset. */
function mountSidebar(d: Document): { panel: HTMLElement; footer: HTMLElement } {
  const panel = d.createElement("nav");
  panel.id = "sidebar";

  const header = d.createElement("div");
  header.className = "sidebar-header";
  const list = d.createElement("div");
  list.id = "tab-list";
  const footer = d.createElement("div");
  footer.className = "sidebar-footer";
  const account = d.createElement("button");
  account.type = "button";
  account.className = "account-btn";
  const logout = d.createElement("button");
  logout.type = "button";
  logout.className = "icon-btn";
  const glyph = d.createElementNS("http://www.w3.org/2000/svg", "svg");
  glyph.setAttribute("class", "ic-ui");
  logout.appendChild(glyph);
  footer.append(account, logout);
  panel.append(header, list, footer);
  d.body.replaceChildren(panel);
  return { panel, footer };
}

/** The settings shell as all three tabbed pages author it: a title bar above, then the scroller
 *  holding the sticky header that carries the tab bar. */
function mountTabbedPage(): { bar: HTMLElement; header: HTMLElement; titlebar: HTMLElement } {
  const area = doc.createElement("main");
  area.id = "chat-area";
  const titlebar = doc.createElement("div");
  titlebar.className = "chat-toolbar";
  const h1 = doc.createElement("h1");
  h1.className = "titlebar-heading";
  h1.textContent = "Settings";
  titlebar.appendChild(h1);

  const view = doc.createElement("div");
  view.id = "settings-view";
  view.dataset["tabView"] = "";
  const shell = doc.createElement("div");
  shell.className = "page-shell";
  const header = doc.createElement("header");
  header.className = "page-header";
  const bar = doc.createElement("nav");
  bar.id = "settings-tab-bar";
  bar.className = "seg-bar";
  bar.setAttribute("role", "tablist");
  for (const label of ["General", "Tools", "Permissions"]) {
    const tab = doc.createElement("button");
    tab.type = "button";
    tab.className = "seg";
    tab.setAttribute("role", "tab");
    tab.textContent = label;
    bar.appendChild(tab);
  }
  header.appendChild(bar);
  shell.appendChild(header);
  view.appendChild(shell);
  area.append(titlebar, view);
  doc.body.replaceChildren(area);
  return { bar, header, titlebar };
}

beforeAll(() => {
  style = mountAppCSS();
  frame = document.createElement("iframe");
  frame.width = "390";
  frame.height = "844";
  document.body.appendChild(frame);
  const inner = frame.contentDocument;
  if (inner === null) {
    throw new Error("iframe has no contentDocument");
  }
  doc = inner;
  doc.documentElement.dataset["pointer"] = "coarse";
  sheet = doc.createElement("style");
  doc.head.appendChild(sheet);

  wide = document.createElement("iframe");
  wide.width = "844";
  wide.height = "390";
  document.body.appendChild(wide);
  const innerWide = wide.contentDocument;
  if (innerWide === null) {
    throw new Error("landscape iframe has no contentDocument");
  }
  wideDoc = innerWide;
  wideDoc.documentElement.dataset["pointer"] = "coarse";
  wideSheet = wideDoc.createElement("style");
  wideDoc.head.appendChild(wideSheet);

  ipad = document.createElement("iframe");
  ipad.width = "1106";
  ipad.height = "829";
  document.body.appendChild(ipad);
  const innerIpad = ipad.contentDocument;
  if (innerIpad === null) {
    throw new Error("iPad iframe has no contentDocument");
  }
  ipadDoc = innerIpad;
  ipadDoc.documentElement.dataset["pointer"] = "coarse";
  ipadSheet = ipadDoc.createElement("style");
  ipadDoc.head.appendChild(ipadSheet);
});

afterAll(() => {
  frame.remove();
  wide.remove();
  ipad.remove();
  style.remove();
});

describe("the composer's bottom clearance", () => {
  // The band is page below the card, and it is capped.
  it("spends the band as page BELOW the card, capped under the device's inset", () => {
    withInsets(true);
    const { form, box } = mountComposer();
    const page = form.getBoundingClientRect().bottom - box.getBoundingClientRect().bottom;
    const cap =
      parseFloat(getComputedStyle(form).getPropertyValue("--composer-inset-cap")) * 16 || 24;
    expect(page, `${page}px of page below the card`).toBeCloseTo(cap, 0);
    // Pin that the cap BINDS here: an uncapped rule would put the device's own 34px in this gap
    // and the case above would still pass.
    expect(page, "the cap binds rather than the device's own inset").toBeLessThan(INSET_BOTTOM);
  });

  it("leaves the card's own material uniform, so the box reads symmetric", () => {
    // The other half of the same rule.
    withInsets(true);
    const { pill } = mountComposer();
    const row = pill.closest(".prompt-pills") as HTMLElement;
    const cs = getComputedStyle(row);
    const top = parseFloat(cs.paddingBlockStart);
    const bottom = parseFloat(cs.paddingBlockEnd);
    expect(bottom, `${bottom}px under the controls against ${top}px above them`).toBeCloseTo(
      top,
      0,
    );
  });

  it("keeps every control clear of the indicator band", () => {
    // What the clearance is actually FOR: no control may sit in the home-indicator band. Measured
    // from the last control's painted edge to the bar's own, which is the viewport's bottom in
    // production (mountComposer's doc comment).
    withInsets(true);
    const { form, pill } = mountComposer();
    const gap = form.getBoundingClientRect().bottom - pill.getBoundingClientRect().bottom;
    // Comfortably past the visible indicator while staying under Apple's reserve, which is the
    // trade the cap makes. A lower bound rather than an equality, so a retune of the cap or of the
    // row's inset moves this without a test edit.
    expect(gap, `the last control sits ${gap}px above the screen edge`).toBeGreaterThan(24);
    expect(gap).toBeLessThan(INSET_BOTTOM);
  });

  it("leaves an inset-less device exactly as it was", () => {
    // The control, and it is what stops the three cases above passing for a stylesheet that reads
    // the inset nowhere: with `env()` at 0 the pill row keeps its uniform inset and the bar keeps
    // the house gap, so the whole mechanism is invisible.
    withInsets(false);
    const { form, box, pill } = mountComposer();
    const row = pill.closest(".prompt-pills") as HTMLElement;
    const inset = parseFloat(getComputedStyle(row).paddingBlockEnd);
    // Read off the row's own TOP inset rather than restated, so a retune of `--composer-pill-pad`
    // moves both sides of the comparison together.
    const top = parseFloat(getComputedStyle(row).paddingBlockStart);
    expect(top, "the row's own inset term, resolved").toBeCloseTo(4, 0);
    expect(inset).toBeCloseTo(top, 0);
    const page = form.getBoundingClientRect().bottom - box.getBoundingClientRect().bottom;
    expect(page).toBeCloseTo(12, 0);
  });
});

describe("the tabbed pages' sticky header", () => {
  it("adds no second top inset under a title bar that already paid it", () => {
    withInsets(true);
    const { bar, header, titlebar } = mountTabbedPage();
    // The premise: the title bar IS paying it, so a second payment here is a double charge rather
    // than the only one.
    expect(parseFloat(getComputedStyle(titlebar).paddingBlockStart)).toBeCloseTo(INSET_TOP, 0);
    const pad = parseFloat(getComputedStyle(header).paddingBlockStart);
    expect(pad, `the header reserves ${pad}px above its tab bar`).toBeLessThan(INSET_TOP);
    // And the visible consequence: the gap from the title bar's bottom to the tab bar's top. It
    // measured 68px with the second payment, 21px without it.
    const gap = bar.getBoundingClientRect().top - titlebar.getBoundingClientRect().bottom;
    expect(gap, `${gap}px between the title bar and the tab bar`).toBeLessThan(INSET_TOP);
  });

  it("reserves the same room whether the device has a top inset or not", () => {
    withInsets(true);
    const on = getComputedStyle(mountTabbedPage().header).paddingBlockStart;
    withInsets(false);
    const off = getComputedStyle(mountTabbedPage().header).paddingBlockStart;
    expect(on).toBe(off);
  });
});

// The inline edges. Same instrument, same pairing rule, one more axis: four surfaces
// reach a screen edge sideways, and in LANDSCAPE that is where a notch inset actually lands.

/** `padding-inline` as the engine resolved it, from the element's own view. */
function inlinePadding(el: Element): { start: number; end: number } {
  const cs = styleOf(el);
  return { start: parseFloat(cs.paddingInlineStart), end: parseFloat(cs.paddingInlineEnd) };
}

function mountBare(d: Document, html: string, selector: string): Element {
  d.body.innerHTML = html;
  const el = d.body.querySelector(selector);
  if (el === null) {
    throw new Error(`fixture has no ${selector}`);
  }
  return el;
}

/** The four edge surfaces, with the house value each keeps where there is no inset. */
const EDGE_SURFACES = {
  "#sidebar, a full-width drawer on a phone": {
    html: `<nav id="sidebar"></nav>`,
    selector: "[id='sidebar']",
    house: 0,
  },
  ".chat-toolbar, which holds the icon-button row": {
    html: `<div class="chat-toolbar"></div>`,
    selector: ".chat-toolbar",
    house: 12,
  },
  "#messages-wrap, keeping its scrollbar arithmetic": {
    html: `<div id="messages-wrap"></div>`,
    selector: "[id='messages-wrap']",
    house: 16,
  },
  ".bottom-bar under the phone rule": {
    html: `<div class="bottom-bar"></div>`,
    selector: ".bottom-bar",
    house: 16,
  },
} as const;

describe("the inline safe-area insets", () => {
  it.each(Object.entries(EDGE_SURFACES))("%s clears a side inset", (_name, surface) => {
    withInsets(true, INSET_SIDE);
    const el = mountBare(doc, surface.html, surface.selector);
    const pad = inlinePadding(el);
    expect(pad.start, `leading edge: ${pad.start}px against a ${INSET_SIDE}px inset`).toBeCloseTo(
      INSET_SIDE,
      0,
    );
    expect(pad.end, `trailing edge: ${pad.end}px against a ${INSET_SIDE}px inset`).toBeCloseTo(
      INSET_SIDE,
      0,
    );
  });

  it.each(Object.entries(EDGE_SURFACES))(
    "%s keeps its house value with no inset",
    (_name, surface) => {
      // The control. Without it every case above passes for a sheet that reads no inset at all,
      // since 59px is also a value a hand-written literal could carry.
      withInsets(false);
      const el = mountBare(doc, surface.html, surface.selector);
      const pad = inlinePadding(el);
      expect(pad.start).toBeCloseTo(surface.house, 0);
      expect(pad.end).toBeCloseTo(surface.house, 0);
    },
  );

  it("keeps #messages-wrap's scrollbar subtraction and its 0 clamp", () => {
    // The one surface whose trailing edge is not a plain `max(house, inset)`: it gives the
    // scrollbar's width back out of its own padding, clamped at 0px, so the inset joins as a THIRD
    // term rather than replacing that arithmetic.
    withInsets(true, INSET_SIDE);
    const el = mountBare(
      doc,
      `<div id="messages-wrap"></div>`,
      "[id='messages-wrap']",
    ) as HTMLElement;
    el.style.setProperty("--scrollbar-w", "20px");
    expect(inlinePadding(el).end).toBeCloseTo(INSET_SIDE, 0);

    withInsets(false);
    const bare = mountBare(
      doc,
      `<div id="messages-wrap"></div>`,
      "[id='messages-wrap']",
    ) as HTMLElement;
    bare.style.setProperty("--scrollbar-w", "20px");
    expect(inlinePadding(bare).end, "clamped at 0, not -4px").toBeCloseTo(0, 0);
  });

  it("folds the inset into the BASE .bottom-bar rule, which is what governs landscape", () => {
    // The rule that matters most, and the reason the fold is written twice: at 844px the `(width <=
    // 48rem)` phone block stops matching, so this base rule is the only one in force — and
    // landscape is exactly the orientation where the notch inset lands on an inline edge.
    withWideInsets(true, INSET_SIDE);
    const el = mountBare(wideDoc, `<div class="bottom-bar"></div>`, ".bottom-bar");
    const view = wideDoc.defaultView;
    if (view === null) {
      throw new Error("landscape frame has no view");
    }
    expect(
      parseFloat(view.getComputedStyle(el).paddingBlockStart),
      "the base rule is the one in force at this width",
    ).toBeCloseTo(8, 0);

    const pad = inlinePadding(el);
    expect(pad.start).toBeCloseTo(INSET_SIDE, 0);
    expect(pad.end).toBeCloseTo(INSET_SIDE, 0);
  });

  it("leaves the base .bottom-bar's house measure alone with no inset", () => {
    withWideInsets(false);
    const el = mountBare(wideDoc, `<div class="bottom-bar"></div>`, ".bottom-bar");
    const pad = inlinePadding(el);
    expect(pad.start).toBeCloseTo(16, 0);
    expect(pad.end).toBeCloseTo(16, 0);
  });

  it("is the PHONE rule that answers at phone width, so both rules need the fold", () => {
    // The pair to the case above, and together they are why the same fold is written in two files:
    // this rule replaces the whole `padding` shorthand, so a side inset restored only in the base
    // rule would be discarded here.
    withInsets(true, INSET_SIDE);
    const el = mountBare(doc, `<div class="bottom-bar"></div>`, ".bottom-bar");
    const view = doc.defaultView;
    if (view === null) {
      throw new Error("phone frame has no view");
    }
    expect(
      parseFloat(view.getComputedStyle(el).paddingBlockStart),
      "the phone rule is the one in force at this width",
    ).toBeCloseTo(12, 0);
    expect(inlinePadding(el).start).toBeCloseTo(INSET_SIDE, 0);
  });
});

// THE iPAD'S BOTTOM CLEARANCE, ON THE DESKTOP BRANCH.

describe("the iPad's bottom clearance, on the desktop branch", () => {
  it("reserves the device's bottom inset on the composer bar", () => {
    withIpadInsets(true);
    const { form } = mountComposer(ipadDoc);
    const cs = styleOf(form);
    const bottom = parseFloat(cs.paddingBlockEnd);
    expect(bottom, `${bottom}px reserved against a ${INSET_BOTTOM_IPAD}px inset`).toBeCloseTo(
      INSET_BOTTOM_IPAD,
      0,
    );
    // WHICH rule answered, which is the whole point of measuring at this width: the base
    // `.bottom-bar` spends `--sp-2` on the block-start edge where the phone rule spends `--sp-3`.
    // Without this the case passes for a phone rule that reached a viewport it does not govern.
    expect(
      parseFloat(cs.paddingBlockStart),
      "the base rule is the one in force at this width",
    ).toBeCloseTo(8, 0);
  });

  it("pays the bottom inset on the sidebar, once, below its footer", () => {
    withIpadInsets(true);
    const { panel, footer } = mountSidebar(ipadDoc);
    const paid = parseFloat(styleOf(panel).paddingBlockEnd);
    expect(paid, `the panel reserves ${paid}px`).toBeCloseTo(INSET_BOTTOM_IPAD, 0);
    // The paid-once half: the footer is this panel's last child, so a term of its own would charge
    // the band twice (10-shell-app.css states that at the rule).
    expect(parseFloat(styleOf(footer).paddingBlockEnd), "the footer adds none").toBe(0);
    // And the rendered consequence, which is the fact a reader sees: the footer's painted edge sits
    // that far above the panel's own.
    const gap = panel.getBoundingClientRect().bottom - footer.getBoundingClientRect().bottom;
    expect(gap, `${gap}px between the footer and the panel's bottom edge`).toBeCloseTo(
      INSET_BOTTOM_IPAD,
      0,
    );
  });

  it("keeps the composer's last control clear of the band", () => {
    withIpadInsets(true);
    const { form, pill } = mountComposer(ipadDoc);
    const gap = form.getBoundingClientRect().bottom - pill.getBoundingClientRect().bottom;
    // A lower bound, matching this file's own style for the phone case: the card's border and the
    // pill row's inset add to the bar's reservation, so a retune of either moves this without a
    // test edit.
    expect(gap, `the last control sits ${gap}px above the bar's edge`).toBeGreaterThan(
      INSET_BOTTOM_IPAD,
    );
  });

  it("leaves an inset-less device at this width exactly as it was", () => {
    // The control. Without it the three cases above pass for a stylesheet that reads the inset
    // nowhere, since 20px is also a value a literal could carry.
    withIpadInsets(false);
    const { form } = mountComposer(ipadDoc);
    const bar = parseFloat(styleOf(form).paddingBlockEnd);
    expect(bar, "the bar keeps its house gap").toBeCloseTo(8, 0);
    expect(bar).not.toBeCloseTo(INSET_BOTTOM_IPAD, 0);

    const { panel } = mountSidebar(ipadDoc);
    expect(parseFloat(styleOf(panel).paddingBlockEnd), "the panel reserves nothing").toBe(0);
  });

  it("zeroes the composer's spend only while the shell panel is OPEN", () => {
    // `19-files.css`'s `[id="app"]:has(.shell-panel:not(.shell-closed)) .bottom-bar` is the ONE
    // rule that can take the reservation away, at (0,4,0) against the base rule's (0,1,0) —
    // correctly, because with the terminal open the composer is not the bottom-most surface and
    // there is no indicator under it.
    withIpadInsets(true);
    const app = ipadDoc.createElement("div");
    app.id = "app";
    const shell = ipadDoc.createElement("div");
    shell.id = "shell-panel";
    shell.className = "shell-panel shell-closed";
    const { form } = mountComposer(ipadDoc);
    app.append(shell, form);
    ipadDoc.body.replaceChildren(app);
    expect(
      parseFloat(styleOf(form).paddingBlockEnd),
      "closed: the base rule's reservation stands",
    ).toBeCloseTo(INSET_BOTTOM_IPAD, 0);

    shell.classList.remove("shell-closed");
    expect(
      parseFloat(styleOf(form).paddingBlockEnd),
      "open: the reset takes it back to the house gap",
    ).toBeCloseTo(8, 0);
  });
});
