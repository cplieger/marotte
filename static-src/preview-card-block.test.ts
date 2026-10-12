// A workspace page link becomes a card that stands as a full-width block in the transcript, whatever
// sentence it sat in: the text around it breaks above and below, never onto the card's line. Laid out
// with the app's sheet, rendered whole (a reload) and streamed one character at a time (live).
import { afterAll, afterEach, beforeAll, describe, expect, it } from "vitest";

import { mountAppCSS } from "./__test-helpers__/css-rules.js";
import { createMarkdownStream, renderMarkdownInto } from "./markdown.js";
import { setWorkspaceRoot, _resetForTest as resetWorkspace } from "./workspace.js";

let style: HTMLStyleElement;
const hosts: HTMLElement[] = [];

beforeAll(() => {
  style = mountAppCSS();
  setWorkspaceRoot("/workspace");
});

afterAll(() => {
  style.remove();
  resetWorkspace();
});

afterEach(() => {
  for (const h of hosts.splice(0)) {
    h.remove();
  }
});

type Mode = "reload" | "live";

/** The transcript's prose element, 640px wide, holding `md` rendered the way `mode` renders it. */
function prose(md: string, mode: Mode): HTMLElement {
  const host = document.createElement("div");
  host.className = "message assistant";
  host.style.inlineSize = "640px";
  document.body.appendChild(host);
  hosts.push(host);
  if (mode === "reload") {
    renderMarkdownInto(host, md);
  } else {
    const stream = createMarkdownStream(host, { flushIntervalMs: 0 });
    for (const ch of md) {
      stream.writeDelta(ch);
    }
    stream.end();
  }
  return host;
}

function cardIn(host: HTMLElement, selector = ".preview-card"): HTMLElement {
  const card = host.querySelector<HTMLElement>(selector);
  if (card === null) {
    throw new Error(`no ${selector}`);
  }
  return card;
}

/** Every painted line of text outside `card` within `host`, as viewport rects. */
function textLines(host: HTMLElement, card: HTMLElement): DOMRect[] {
  const out: DOMRect[] = [];
  const walk = document.createTreeWalker(host, NodeFilter.SHOW_TEXT);
  for (let n = walk.nextNode(); n !== null; n = walk.nextNode()) {
    if (card.contains(n) || (n.textContent ?? "").trim() === "") {
      continue;
    }
    const range = document.createRange();
    range.selectNodeContents(n);
    out.push(...[...range.getClientRects()].filter((r) => r.width > 0 && r.height > 0));
  }
  return out;
}

/** The visible text of `host` outside the card, as the reader sees it. */
function shownText(host: HTMLElement, card: HTMLElement): string {
  return host.innerText.replace(card.innerText, " ").replace(/\s+/gu, " ").trim();
}

function expectBlockCard(host: HTMLElement, card: HTMLElement, what: string): void {
  const box = card.getBoundingClientRect();
  const column = host.getBoundingClientRect();
  expect(Math.abs(box.width - column.width), `${what}: full width`).toBeLessThanOrEqual(0.5);
  for (const line of textLines(host, card)) {
    const overlap = Math.min(line.bottom, box.bottom) - Math.max(line.top, box.top);
    expect(
      overlap,
      `${what}: text "${String(line.top)}" shares the card's band`,
    ).toBeLessThanOrEqual(0);
  }
}

describe("a workspace page link renders as a full-width block card", () => {
  for (const mode of ["reload", "live"] as const) {
    it(`breaks a sentence above and below a link in its middle (${mode})`, () => {
      const host = prose(
        "Here is the mock: [Mock](/workspace/demo/index.html) to compare against.",
        mode,
      );
      const card = cardIn(host);
      expectBlockCard(host, card, "mid-sentence");
      const above = textLines(host, card).filter(
        (r) => r.bottom <= card.getBoundingClientRect().top,
      );
      const below = textLines(host, card).filter(
        (r) => r.top >= card.getBoundingClientRect().bottom,
      );
      expect(above.length, "text above the card").toBeGreaterThan(0);
      expect(below.length, "text below the card").toBeGreaterThan(0);
      expect(shownText(host, card)).toBe("Here is the mock: to compare against.");
    });

    it(`leaves no orphan punctuation under a link that ends a sentence (${mode})`, () => {
      const host = prose(
        "Here is the mock: [Mock](/workspace/demo/index.html). Full write-up below.",
        mode,
      );
      const card = cardIn(host);
      expectBlockCard(host, card, "sentence end");
      expect(shownText(host, card)).toBe("Here is the mock: Full write-up below.");
      expect(host.textContent, "the punctuation stays in the text").toContain(". Full write-up");
    });

    it(`stands alone for a link on a line of its own (${mode})`, () => {
      const host = prose("Open it:\n\n[Mock](/workspace/demo/index.html)\n\nThen reply.", mode);
      const card = cardIn(host);
      expectBlockCard(host, card, "alone");
      expect(shownText(host, card)).toBe("Open it: Then reply.");
    });
  }

  it("shows the punctuation again once a root change turns the card back into a link", () => {
    const host = prose("See [Mock](/workspace/demo/index.html). Then reply.", "reload");
    cardIn(host);
    try {
      setWorkspaceRoot("/elsewhere");
      expect(host.querySelector(".preview-card"), "no longer a page beneath the root").toBeNull();
      expect(host.innerText.replace(/\s+/gu, " ").trim()).toBe("See Mock. Then reply.");
    } finally {
      setWorkspaceRoot("/workspace");
    }
  });

  it("puts Open at the card's far end", () => {
    const host = prose("[Mock](/workspace/demo/index.html)", "reload");
    const card = cardIn(host).getBoundingClientRect();
    const open = cardIn(host, ".preview-card-open").getBoundingClientRect();
    const pad = parseFloat(getComputedStyle(cardIn(host)).paddingInlineEnd);
    const border = parseFloat(getComputedStyle(cardIn(host)).borderInlineEndWidth);
    expect(Math.abs(card.right - pad - border - open.right)).toBeLessThanOrEqual(0.5);
  });
});

describe("the other objects a prose link or image becomes stand as blocks too", () => {
  it.each([
    ["an audio file", "![clip](/workspace/out/clip.mp3)", ".media-audio"],
    ["a downloadable file", "![report](/workspace/out/report.pdf)", ".media-download"],
  ])("lays %s full width, its sentence broken around it", (_name, embed, selector) => {
    const host = prose(`Listen: ${embed}. Then reply.`, "reload");
    const card = cardIn(host, selector);
    expectBlockCard(host, card, selector);
    expect(shownText(host, card)).toContain("Listen:");
    expect(shownText(host, card)).not.toMatch(/^Listen:\s*\./u);
  });
});
