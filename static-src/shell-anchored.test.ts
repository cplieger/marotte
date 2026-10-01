// THE SHELL IS ANCHORED AT BOTH EDGES AND STATES NO HEIGHT.
//
// `#app`, `#sidebar` and the fullscreen shell panel are `position: fixed` with `top`
// and `bottom` both resolved, so their bottom edge IS the containing block's and an
// overshoot is unrepresentable. Chromium resolves every viewport unit to the frame
// height and reports `env(safe-area-inset-*)` as 0, so the device defect cannot be
// reproduced here; what CAN be measured is that the three bottoms track the frame
// through a RESIZE (the class no earlier suite exercised), that the one CSS input
// the module publishes (`--shell-shortfall`) is consumed by exactly those three
// declarations, and that no stated viewport-unit height has come back.
import { describe, it, expect, beforeAll, afterAll } from "vitest";

import { mountAppCSS, manifestSheets, allRules } from "./__test-helpers__/css-rules.js";

/** The installed iPad window the reported clip came from, in CSS px. Past
 *  `(width <= 48rem)`, so this is the desktop branch. */
const FRAME_W = 1106;
const FRAME_H = 829;

let style: HTMLStyleElement;
let frame: HTMLIFrameElement;
let doc: Document;

/** The frame's OWN viewport height, read off its document: an `iframe` carries a
 *  2px UA border per edge, so the viewport is 4px shorter than the attribute. */
function windowH(): number {
  return doc.documentElement.clientHeight;
}

/** Resize the iframe and wait one frame for layout. */
async function resizeFrame(h: number): Promise<void> {
  frame.height = String(h);
  await new Promise<void>((r) => {
    requestAnimationFrame(() => r());
  });
  expect(windowH(), `the frame's viewport after resizing to ${h}`).toBe(h - 4);
}

function bottom(el: Element): number {
  return el.getBoundingClientRect().bottom;
}

/** The LAYOUT border-box height. `getBoundingClientRect()` is the wrong
 *  instrument for the fullscreen shell panel: `vk-shell-fs-in` starts at
 *  `transform: scale(0.99)` under a `backwards` fill, so before the animation
 *  runs the rect reports the keyframe. */
function layoutHeight(el: HTMLElement): number {
  return el.offsetHeight;
}

/** The LAYOUT bottom edge, for the same reason; a fixed box's `offsetParent` is
 *  null, so `offsetTop` is against the containing block. */
function layoutBottom(el: HTMLElement): number {
  return el.offsetTop + el.offsetHeight;
}

function computed(el: Element): CSSStyleDeclaration {
  const view = doc.defaultView;
  if (view === null) {
    throw new Error("iframe has no window");
  }
  return view.getComputedStyle(el);
}

function mk(tag: string, id?: string, cls?: string): HTMLElement {
  const el = doc.createElement(tag);
  if (id !== undefined) {
    el.id = id;
  }
  if (cls !== undefined) {
    el.className = cls;
  }
  return el;
}

/** The shell as `static/index.html` authors it, down to the two LAST children the
 *  clip is about. The growing middles (`#tab-list`, `#messages-wrap-outer`) are
 *  `flex: 1`, so the two last children sit at the bottom of their panels. */
function mountShell(): {
  app: HTMLElement;
  sidebar: HTMLElement;
  footer: HTMLElement;
  send: HTMLElement;
} {
  const app = mk("div", "app");

  const sidebar = mk("nav", "sidebar");
  const footer = mk("div", undefined, "sidebar-footer");
  footer.append(mk("button", undefined, "account-btn"));
  sidebar.append(mk("div", undefined, "sidebar-header"), mk("div", "tab-list"), footer);

  const area = mk("main", "chat-area");
  const form = mk("form", "prompt-form", "bottom-bar");
  const promptBox = mk("div", undefined, "prompt-box");
  const pills = mk("div", undefined, "prompt-pills");
  const slot = mk("span", undefined, "pill-slot");
  const send = mk("button", undefined, "send-btn");
  const glyph = doc.createElementNS("http://www.w3.org/2000/svg", "svg");
  glyph.setAttribute("class", "ic-ui");
  send.appendChild(glyph);
  slot.appendChild(send);
  pills.appendChild(slot);
  promptBox.append(mk("textarea", "prompt-input"), pills);
  form.appendChild(promptBox);
  area.append(mk("div", "messages-wrap-outer"), form);

  app.append(sidebar, area);
  doc.body.replaceChildren(app);
  return { app, sidebar, footer, send };
}

/** The fullscreen shell panel NESTED as `static/index.html` nests it, inside
 *  `<main id="chat-area">` inside `#app`: it is the one shell box with an ancestor
 *  between it and `body`, so a flat fixture measures a resolution path production
 *  does not take. `areaPx` forces `#chat-area` to a height that is neither the
 *  window's nor anything else in the fixture, so the two candidate containing
 *  blocks give distinguishable answers. */
function mountFullscreenShell(areaPx?: number): HTMLElement {
  const app = mk("div", "app");
  const area = mk("main", "chat-area");
  if (areaPx !== undefined) {
    area.style.height = `${areaPx}px`;
  }
  const panel = mk("div", "shell-panel", "shell-panel shell-fullscreen");
  area.appendChild(panel);
  app.appendChild(area);
  doc.body.replaceChildren(app);
  return panel;
}

beforeAll(() => {
  style = mountAppCSS();
  frame = document.createElement("iframe");
  frame.width = String(FRAME_W);
  frame.height = String(FRAME_H);
  document.body.appendChild(frame);
  const inner = frame.contentDocument;
  if (inner === null) {
    throw new Error("iframe has no contentDocument");
  }
  doc = inner;
  doc.documentElement.dataset["pointer"] = "coarse";
  const sheet = doc.createElement("style");
  sheet.textContent = style.textContent;
  doc.head.appendChild(sheet);
  expect(windowH(), "the frame's own viewport height").toBe(FRAME_H - 4);
});

afterAll(() => {
  frame.remove();
  style.remove();
});

/** Every bottom the clip was about, against the frame's current viewport. */
function assertAnchored(label: string): void {
  const h = windowH();
  const { app, sidebar, footer, send } = mountShell();
  expect(bottom(app), `#app's bottom ${label}`).toBeCloseTo(h, 0);
  expect(bottom(sidebar), `#sidebar's bottom ${label}`).toBeCloseTo(h, 0);
  expect(bottom(footer), `the sidebar footer ${label}`).toBeLessThanOrEqual(h);
  expect(bottom(footer), `the sidebar footer sits AT the edge ${label}`).toBeGreaterThan(h - 100);
  expect(bottom(send), `the send button ${label}`).toBeLessThanOrEqual(h);
  expect(bottom(send), `the send button sits AT the edge ${label}`).toBeGreaterThan(h - 100);
  const panel = mountFullscreenShell();
  expect(layoutHeight(panel), `the fullscreen panel ${label}`).toBeCloseTo(h, 0);
  expect(layoutBottom(panel), `the fullscreen panel's bottom ${label}`).toBeCloseTo(h, 0);
}

describe("the three shell boxes", () => {
  it("track the frame's bottom edge through resizes, at 829, 600 and 950", async () => {
    assertAnchored("at 829");
    await resizeFrame(600);
    assertAnchored("after shrinking to 600");
    await resizeFrame(950);
    assertAnchored("after growing to 950");
    await resizeFrame(FRAME_H);
    assertAnchored("back at 829");
  });
});

describe("--shell-shortfall", () => {
  afterAll(() => {
    doc.documentElement.style.removeProperty("--shell-shortfall");
  });

  it("extends the three boxes below the frame by exactly its value, and nothing else moves", () => {
    const h = windowH();
    const before = mountShell();
    const sendTop = before.send.getBoundingClientRect().top;
    const headerBottom = bottom(before.sidebar.querySelector(".sidebar-header") ?? before.sidebar);

    doc.documentElement.style.setProperty("--shell-shortfall", "40px");
    const { app, sidebar, send } = mountShell();
    expect(bottom(app), "#app extends by the shortfall").toBeCloseTo(h + 40, 0);
    expect(bottom(sidebar), "#sidebar extends by the shortfall").toBeCloseTo(h + 40, 0);
    expect(
      send.getBoundingClientRect().top,
      "the composer's last control moves down by it",
    ).toBeCloseTo(sendTop + 40, 0);
    expect(
      bottom(sidebar.querySelector(".sidebar-header") ?? sidebar),
      "the sidebar header, anchored at the top, does not move",
    ).toBeCloseTo(headerBottom, 0);
    const panel = mountFullscreenShell();
    expect(layoutBottom(panel), "the fullscreen panel extends by the shortfall").toBeCloseTo(
      h + 40,
      0,
    );

    doc.documentElement.style.removeProperty("--shell-shortfall");
    const after = mountShell();
    expect(bottom(after.app), "#app returns to the frame's edge").toBeCloseTo(h, 0);
  });
});

describe("the fullscreen panel's containing block", () => {
  // `#chat-area` carries `position: relative`, `overflow: hidden`, `isolation:
  // isolate` and `container: chat-area / inline-size`, none of which establishes
  // a fixed-positioning containing block (`container-type: inline-size` applies
  // style and inline-size containment only, per MDN). A `transform`, a `filter`,
  // `will-change: transform`, `content-visibility` or a `contain` carrying
  // `layout`/`paint`/`strict` added there WOULD, and the panel would silently
  // anchor to `#chat-area`'s box instead of the window.
  const AREA = 400;

  it("is the WINDOW, not #chat-area", () => {
    const panel = mountFullscreenShell(AREA);
    const area = doc.getElementById("chat-area") as HTMLElement;
    expect(computed(area).containerType, "#chat-area's container-type, from the real sheet").toBe(
      "inline-size",
    );
    expect(layoutHeight(area), "the fixture's #chat-area").toBeCloseTo(AREA, 0);
    expect(layoutBottom(panel), `the panel's bottom against a ${AREA}px #chat-area`).toBeCloseTo(
      windowH(),
      0,
    );
  });

  it("moves to #chat-area the moment that box establishes one", () => {
    // The positive control: with the answer forced to the ancestor the same fixture
    // reads AREA, so the case above is discriminating.
    const panel = mountFullscreenShell(AREA);
    const area = doc.getElementById("chat-area") as HTMLElement;
    area.style.contain = "layout";
    expect(layoutBottom(panel), `the panel's bottom against a contained #chat-area`).toBeCloseTo(
      AREA,
      0,
    );
  });
});

// ---------------------------------------------------------------------------
// The mechanical guard over the assembled CSS.
// ---------------------------------------------------------------------------

const SHELL_SELECTORS = ['[id="app"]', '[id="sidebar"]', ".shell-fullscreen"] as const;
const STATES_HEIGHT =
  /(?:^|[;{}\s])(min-block-size|min-height|block-size|height|max-height|max-block-size)\s*:\s*([^;}]*)/g;

describe("the assembled stylesheet", () => {
  it("carries no --app-h", () => {
    for (const { name, css } of manifestSheets()) {
      expect(css.includes("--app-h"), `${name} mentions --app-h`).toBe(false);
    }
  });

  it("anchors every shell box at the bottom and states no height on it", () => {
    // Textual rather than computed: `getComputedStyle().bottom` on a positioned
    // box is the USED value, which reads `0px` for `bottom: auto` under a stated
    // height too, so it cannot tell the two shapes apart.
    const offenders: string[] = [];
    const anchored = new Set<string>();
    for (const { name, css } of manifestSheets()) {
      for (const rule of allRules(css)) {
        const sel = SHELL_SELECTORS.find((s) => rule.selector.includes(s));
        if (sel === undefined) {
          continue;
        }
        if (/(?:^|[;{}\s])(?:bottom|inset)\s*:/.test(rule.body)) {
          anchored.add(sel);
        }
        for (const m of rule.body.matchAll(STATES_HEIGHT)) {
          const value = (m[2] ?? "").replace(/!important/g, "").trim();
          if (value !== "auto" && value !== "none") {
            offenders.push(`${name}: ${rule.selector} { ${m[1]}: ${value} }`);
          }
        }
      }
    }
    expect([...anchored].sort(), "every shell box declares a bottom anchor").toEqual(
      [...SHELL_SELECTORS].sort(),
    );
    expect(offenders, "a shell box states no height of its own").toEqual([]);
  });
});
