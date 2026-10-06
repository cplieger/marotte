// `[id="chat-area"]` is `isolation: isolate` so the composer's `z-index: 10` cannot beat the
// sidebar's in the root context (`container-type` makes none in Chromium). Only a hit test
// sees paint order; both cases assert their overlap premise.
import { describe, it, expect, beforeAll, afterAll, afterEach } from "vitest";

import { loadCSS, mountAppCSS, ruleContaining } from "./__test-helpers__/css-rules.js";
import { makeExpandable } from "./pill-expand.js";

/**
 * A declared width stands in for the credit meter's font-dependent width, so the overflow
 * is deterministic; that it overflows is asserted.
 */
const WIDE_ROW_PX = 300;

/** Tall enough that the composer's band reaches up into the opened card. Production gets
 *  there from a textarea plus a pill row; the height is what matters, not the controls. */
const COMPOSER_CONTENT_PX = 120;

let style: HTMLStyleElement;

beforeAll(() => {
  style = mountAppCSS();
  document.body.style.margin = "0";
});

afterAll(() => {
  style.remove();
});

afterEach(() => {
  document.body.replaceChildren();
});

interface Shell {
  sidebar: HTMLElement;
  card: HTMLElement;
  btn: HTMLButtonElement;
  composer: HTMLElement;
}

/**
 * The real two-column shell, every class production's, so the z-indexes come from the
 * shipped stylesheet.
 */
function mountShell(): Shell {
  const app = document.createElement("div");
  app.id = "app";

  const sidebar = document.createElement("nav");
  sidebar.id = "sidebar";
  // The tab list is what pushes the footer to the panel's bottom, which is what puts
  // the opened card in the composer's band.
  const tabs = document.createElement("div");
  tabs.id = "tab-list";
  tabs.style.flex = "1";

  const footer = document.createElement("div");
  footer.className = "sidebar-footer";
  const anchor = document.createElement("div");
  anchor.className = "popup-anchor";

  const btn = document.createElement("button");
  btn.type = "button";
  btn.id = "account-btn";
  btn.className = "account-btn pill-expandable";

  const card = document.createElement("span");
  card.id = "status-card";
  card.className = "pill-expand-content pill-status-content hidden";
  const row = document.createElement("span");
  row.className = "pill-detail";
  row.style.cssText = `display:block;width:${WIDE_ROW_PX}px;height:64px`;
  card.appendChild(row);

  anchor.append(btn, card);
  footer.appendChild(anchor);
  sidebar.append(tabs, footer);

  const chat = document.createElement("main");
  chat.id = "chat-area";
  const fill = document.createElement("div");
  fill.style.flex = "1";
  const composer = document.createElement("form");
  composer.id = "prompt-form";
  composer.className = "bottom-bar";
  const box = document.createElement("div");
  box.className = "prompt-box";
  box.style.cssText = `block-size:${COMPOSER_CONTENT_PX}px`;
  composer.appendChild(box);
  chat.append(fill, composer);

  app.append(sidebar, chat);
  document.body.replaceChildren(app);
  return { sidebar, card, btn, composer };
}

/**
 * Open the popup the way the app does, then settle the entry transition: the rect is the
 * SCALED box until it finishes.
 */
function open(btn: HTMLButtonElement, card: HTMLElement): void {
  makeExpandable(btn, card);
  btn.click();
  for (const a of card.getAnimations()) {
    a.finish();
  }
}

/** The centre of the region where the card overflows its panel AND the composer's band
 *  sits, with both memberships asserted so the hit test cannot answer vacuously. */
function overflowPoint(shell: Shell): { x: number; y: number } {
  const card = shell.card.getBoundingClientRect();
  const panel = shell.sidebar.getBoundingClientRect();
  const bar = shell.composer.getBoundingClientRect();

  expect(
    card.right,
    `the card is ${card.width}px in a ${panel.width}px panel, so it does not overflow`,
  ).toBeGreaterThan(panel.right);

  const top = Math.max(card.top, bar.top);
  const bottom = Math.min(card.bottom, bar.bottom);
  expect(bottom, "the card's band and the composer's band do not overlap").toBeGreaterThan(top);

  return { x: (panel.right + card.right) / 2, y: (top + bottom) / 2 };
}

describe("the sidebar's overflowing popup against the chat area", () => {
  it("paints over the composer rather than under it", () => {
    const shell = mountShell();
    open(shell.btn, shell.card);
    const { x, y } = overflowPoint(shell);

    const hit = document.elementFromPoint(x, y);
    expect(
      shell.card.contains(hit),
      `(${x}, ${y}) answered ${hit?.id === "" ? (hit?.nodeName ?? "null") : hit?.id}`,
    ).toBe(true);
  });
});

describe("the phone drawer against the chat area", () => {
  // The drawer's `z-index: 100` lives in `@media (width <= 48rem)`, which the fixed 1280px
  // viewport never matches, so its body is mounted unwrapped after the bundle.
  it("still paints over the chat area once the whole layer is isolated", () => {
    const mobile = loadCSS("50-mobile.css");
    // By selector: that file has TWO `width <= 48rem` at-rules, one prelude a prefix of the
    // other.
    const rules = ['[id="sidebar"]', '[id="sidebar"].open'].map((sel) =>
      ruleContaining(mobile, sel, "width <= 48rem"),
    );
    const phone = document.createElement("style");
    phone.textContent = rules.map((r) => `${r.selector} { ${r.body} }`).join("\n");
    document.head.appendChild(phone);
    try {
      const shell = mountShell();
      shell.sidebar.classList.add("open");
      const panel = shell.sidebar.getBoundingClientRect();
      const bar = shell.composer.getBoundingClientRect();

      // The drawer is full-width here, so it covers the composer outright.
      expect(panel.right, "the drawer does not reach the composer").toBeGreaterThan(bar.left);
      const x = (Math.max(panel.left, bar.left) + Math.min(panel.right, bar.right)) / 2;
      const y = (bar.top + bar.bottom) / 2;

      const hit = document.elementFromPoint(x, y);
      expect(
        shell.sidebar.contains(hit),
        `(${x}, ${y}) answered ${hit?.id === "" ? (hit?.nodeName ?? "null") : hit?.id}`,
      ).toBe(true);
    } finally {
      phone.remove();
    }
  });
});
