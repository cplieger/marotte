// Structural guard for the expandable-pill markup (static/index.html).
//
// Every expanded card is a SIBLING of its trigger, never a descendant of it.
// Two things break the moment a card is nested back inside its button:
//
//   1. The universal press scale (`:active { transform: scale(0.96) }`,
//      03-base.css) shrinks the open menu along with its trigger. The
//      workaround that lived here before pressed the pill's CHILDREN and
//      pinned the pill itself to `transform: none`, so a pressed trigger's
//      border and background did not move at all.
//   2. A card holding buttons (the model list, the mode list) puts
//      interactive content inside a <button>: invalid HTML, and assistive
//      tech flattens it.

import { afterEach, describe, it, expect, vi } from "vitest";
import indexHtml from "../static/index.html?raw";
import { framesBudgetMs, testTimeoutFor } from "./__test-helpers__/frame-budget.js";
import { makeExpandable } from "./pill-expand.js";

// Parse only the two regions that hold expandable pills, rather than the whole
// document: a full-document parse would make the runner chase the
// <link rel=stylesheet> over the network.
function slice(html: string, from: string, to: string): HTMLElement {
  const start = html.indexOf(from);
  const end = html.indexOf(to, start + 1);
  expect(start, `marker not found: ${from}`).toBeGreaterThan(-1);
  expect(end, `marker not found: ${to}`).toBeGreaterThan(start);
  const host = document.createElement("div");
  host.innerHTML = html.slice(start, end);
  return host;
}

describe("expandable pill markup (static/index.html)", () => {
  it("puts every expand card beside its trigger, never inside a button", () => {
    const regions = [
      slice(indexHtml, '<div class="prompt-pills">', "</form>"),
      // The closing marker was `'<a id="user-email"'`, which no longer exists: the
      // address is a SPAN inside the trigger now. `.sidebar-footer-actions` is the
      // next thing after the whole anchor, so the region still covers the card and
      // its trigger and nothing else.
      slice(indexHtml, '<div class="sidebar-footer">', '<div class="sidebar-footer-actions">'),
    ];

    let cards = 0;
    for (const region of regions) {
      for (const card of Array.from(region.querySelectorAll<HTMLElement>(".pill-expand-content"))) {
        cards++;
        const where = card.id === "" ? (card.className ?? "") : `#${card.id}`;
        expect(card.closest("button"), `${where} must not sit inside a <button>`).toBeNull();
        const trigger = card.previousElementSibling;
        expect(
          trigger?.classList.contains("pill-expandable"),
          `${where} must follow its .pill-expandable trigger`,
        ).toBe(true);
      }
    }
    // The context, model, mode, chat-options, task-list and status cards.
    expect(cards).toBe(6);
  });

  it("gives every expandable trigger a card next to it", () => {
    const regions = [
      slice(indexHtml, '<div class="prompt-pills">', "</form>"),
      // The closing marker was `'<a id="user-email"'`, which no longer exists: the
      // address is a SPAN inside the trigger now. `.sidebar-footer-actions` is the
      // next thing after the whole anchor, so the region still covers the card and
      // its trigger and nothing else.
      slice(indexHtml, '<div class="sidebar-footer">', '<div class="sidebar-footer-actions">'),
    ];

    for (const region of regions) {
      for (const trigger of Array.from(region.querySelectorAll<HTMLElement>(".pill-expandable"))) {
        expect(
          trigger.nextElementSibling?.classList.contains("pill-expand-content"),
          `#${trigger.id} must be followed by its .pill-expand-content card`,
        ).toBe(true);
      }
    }
  });
});

describe("expandable pill viewport clamp", () => {
  it("shifts a right-edge card left and keeps its transform origin on the trigger", () => {
    const slot = document.createElement("span");
    const pill = document.createElement("button");
    const card = document.createElement("div");
    slot.append(pill, card);
    document.body.appendChild(slot);

    const viewportLeft = window.visualViewport?.offsetLeft ?? 0;
    const viewportWidth = window.visualViewport?.width ?? window.innerWidth;
    const slotLeft = viewportLeft + viewportWidth - 40;
    const cardWidth = 192;
    Object.defineProperty(slot, "getBoundingClientRect", {
      configurable: true,
      value: () => ({ left: slotLeft }),
    });
    Object.defineProperty(pill, "getBoundingClientRect", {
      configurable: true,
      value: () => ({ left: slotLeft, width: 40 }),
    });
    Object.defineProperty(card, "offsetWidth", {
      configurable: true,
      value: cardWidth,
    });

    const ac = new AbortController();
    makeExpandable(pill, card, { signal: ac.signal });
    pill.click();

    const expectedLeft = viewportLeft + viewportWidth - 12 - cardWidth;
    const expectedShift = expectedLeft - slotLeft;
    expect(parseFloat(card.style.getPropertyValue("--pill-inline-shift"))).toBe(expectedShift);
    expect(parseFloat(card.style.getPropertyValue("--pill-origin-x"))).toBe(
      slotLeft + 20 - expectedLeft,
    );

    ac.abort();
    slot.remove();
  });
});

// The clamp has to TRACK the frame while a card is open, because tapping a pill blurs
// the composer and iOS then spends several frames closing the keyboard: the open's own
// read lands against a viewport that is about to stop existing.
//
// `window.visualViewport` is replaced by a fake carrying the four fields the clamp
// reads, installed after import because the module reads the global per call. No
// stylesheet is loaded, so `--pill-viewport-margin` falls back to MARGIN.

type Listener = () => void;

interface FakeViewport {
  offsetLeft: number;
  offsetTop: number;
  width: number;
  height: number;
  addEventListener(type: string, fn: Listener): void;
  removeEventListener(type: string, fn: Listener): void;
  fire(type: string): void;
  listeners(): number;
}

function fakeViewport(offsetTop: number): FakeViewport {
  const map = new Map<string, Set<Listener>>();
  return {
    offsetLeft: 0,
    offsetTop,
    width: 400,
    height: 700,
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
    listeners(): number {
      let n = 0;
      for (const set of map.values()) {
        n += set.size;
      }
      return n;
    },
  };
}

/** The fallback `popupViewportMargin` takes with no stylesheet loaded. */
const MARGIN = 12;
/** `onViewportChange` holds two of its three subscriptions on the viewport object. */
const VIEWPORT_LISTENERS_PER_CARD = 2;
const CARD_BOTTOM = 700;
const CARD_WIDTH = 192;

/** What `clampToViewport` publishes for a card whose bottom edge is `CARD_BOTTOM`. */
function expectedCap(offsetTop: number): number {
  return Math.max(Math.round(CARD_BOTTOM - offsetTop - MARGIN), 96);
}

function capOf(card: HTMLElement): number {
  return parseFloat(card.style.getPropertyValue("--pill-max-block"));
}

/** The declarations production's `.pill-expand-content` rule supplies, so the published
 *  cap drives a real scrolling box over real overflowing content. */
function scrollableCard(card: HTMLElement): void {
  card.style.overflowY = "auto";
  card.style.maxBlockSize = "var(--pill-max-block)";
  card.style.inlineSize = `${String(CARD_WIDTH)}px`;
  const focusable = document.createElement("button");
  focusable.textContent = "row 1";
  card.append(focusable);
  for (let i = 2; i < 60; i++) {
    const row = document.createElement("div");
    row.textContent = `row ${String(i)}`;
    row.style.blockSize = "40px";
    card.append(row);
  }
}

async function frames(n = 2): Promise<void> {
  for (let i = 0; i < n; i++) {
    await new Promise((r) => {
      requestAnimationFrame(() => {
        r(null);
      });
    });
  }
}

const controllers: AbortController[] = [];
const hosts: HTMLElement[] = [];

/** A pill and its sibling card, with the geometry the clamp reads held fixed so only
 *  the frame's `offsetTop` can move the published cap. The card's BOTTOM is stubbed
 *  rather than measured because production anchors the card upward from the composer,
 *  so its bottom edge does not follow its own height; measuring it in flow instead
 *  makes the room track the content and no cap can ever bind. Everything the box does
 *  — `clientHeight`, `scrollHeight`, `scrollTop`, focus — stays real. */
function stubbedPill(fill?: (card: HTMLElement) => void): {
  pill: HTMLElement;
  card: HTMLElement;
} {
  const slot = document.createElement("span");
  const pill = document.createElement("button");
  const card = document.createElement("div");
  fill?.(card);
  slot.append(pill, card);
  document.body.appendChild(slot);
  hosts.push(slot);

  Object.defineProperty(slot, "getBoundingClientRect", {
    configurable: true,
    value: () => ({ left: 20 }),
  });
  Object.defineProperty(pill, "getBoundingClientRect", {
    configurable: true,
    value: () => ({ left: 20, width: 40 }),
  });
  Object.defineProperty(card, "getBoundingClientRect", {
    configurable: true,
    value: () => ({ bottom: CARD_BOTTOM }),
  });
  Object.defineProperty(card, "offsetWidth", { configurable: true, value: CARD_WIDTH });

  const ac = new AbortController();
  controllers.push(ac);
  makeExpandable(pill, card, { signal: ac.signal });
  return { pill, card };
}

afterEach(() => {
  for (const ac of controllers.splice(0)) {
    ac.abort();
  }
  for (const host of hosts.splice(0)) {
    host.remove();
  }
  vi.unstubAllGlobals();
});

describe(
  "expandable pill viewport tracking",
  { timeout: testTimeoutFor(framesBudgetMs(4)) },
  () => {
    it("re-measures the cap when the keyboard-shrunk frame settles", async () => {
      const fake = fakeViewport(300);
      vi.stubGlobal("visualViewport", fake);
      const { pill, card } = stubbedPill();

      pill.click();
      expect(capOf(card)).toBe(expectedCap(300));

      // An `offsetTop` change fires `scroll` alone, which is the iOS signature.
      fake.offsetTop = 0;
      fake.fire("scroll");
      await frames();
      expect(capOf(card)).toBe(expectedCap(0));
    });

    it("tracks a keyboard raise as well as a collapse", async () => {
      const fake = fakeViewport(0);
      vi.stubGlobal("visualViewport", fake);
      const { pill, card } = stubbedPill();

      pill.click();
      expect(capOf(card)).toBe(expectedCap(0));

      fake.offsetTop = 300;
      fake.fire("resize");
      await frames();
      expect(capOf(card)).toBe(expectedCap(300));
    });

    it("detaches on close", async () => {
      const fake = fakeViewport(300);
      vi.stubGlobal("visualViewport", fake);
      const { pill, card } = stubbedPill();

      pill.click();
      expect(fake.listeners()).toBe(VIEWPORT_LISTENERS_PER_CARD);
      pill.click();
      expect(fake.listeners()).toBe(0);

      const atClose = capOf(card);
      fake.offsetTop = 0;
      fake.fire("resize");
      window.dispatchEvent(new Event("resize"));
      await frames();
      expect(capOf(card)).toBe(atClose);
    });

    it("detaches when the consumer aborts with the card open", async () => {
      const fake = fakeViewport(300);
      vi.stubGlobal("visualViewport", fake);
      const { pill, card } = stubbedPill();

      pill.click();
      expect(fake.listeners()).toBe(VIEWPORT_LISTENERS_PER_CARD);
      // dispose() hides first, so the abort teardown routes through onClose.
      controllers.splice(0)[0]?.abort();
      expect(fake.listeners()).toBe(0);

      const atAbort = capOf(card);
      fake.offsetTop = 0;
      fake.fire("resize");
      window.dispatchEvent(new Event("resize"));
      await frames();
      expect(capOf(card)).toBe(atAbort);
    });

    it("does no work for a frame that has not moved", async () => {
      const fake = fakeViewport(300);
      vi.stubGlobal("visualViewport", fake);
      const { pill, card } = stubbedPill();

      pill.click();
      card.style.setProperty("--pill-max-block", "4321px");
      // A page scroll fires viewport `scroll` with the geometry unchanged.
      fake.fire("scroll");
      await frames();
      expect(capOf(card)).toBe(4321);
    });

    it("moves neither the card's scroll position nor focus across a re-measure", async () => {
      const fake = fakeViewport(0);
      vi.stubGlobal("visualViewport", fake);
      const { pill, card } = stubbedPill(scrollableCard);
      const child = card.querySelector("button");

      pill.click();
      const opened = capOf(card);
      expect(card.scrollHeight).toBeGreaterThan(card.clientHeight);
      child?.focus();
      card.scrollTop = 120;
      expect(document.activeElement).toBe(child);
      expect(card.scrollTop).toBe(120);

      // A SHRINKING cap only ever raises the maximum scroll offset, so the engine
      // cannot clamp and an unchanged scrollTop is a real assertion.
      fake.offsetTop = 300;
      fake.fire("scroll");
      await frames();

      expect(document.activeElement).toBe(child);
      expect(card.scrollTop).toBe(120);
      expect(capOf(card)).toBeLessThan(opened);
    });

    it("shrinks the rendered box to the settled frame's room", async () => {
      const fake = fakeViewport(0);
      vi.stubGlobal("visualViewport", fake);
      const { pill, card } = stubbedPill(scrollableCard);

      pill.click();
      expect(card.clientHeight).toBe(expectedCap(0));

      fake.offsetTop = 300;
      fake.fire("scroll");
      await frames();
      expect(card.clientHeight).toBe(expectedCap(300));
    });

    it("releases the first card's subscription when a second pill opens", async () => {
      const fake = fakeViewport(300);
      vi.stubGlobal("visualViewport", fake);
      const first = stubbedPill();
      const second = stubbedPill();

      first.pill.click();
      second.pill.click();
      expect(fake.listeners()).toBe(VIEWPORT_LISTENERS_PER_CARD);

      const firstAtClose = capOf(first.card);
      fake.offsetTop = 0;
      fake.fire("scroll");
      await frames();
      expect(capOf(first.card)).toBe(firstAtClose);
      expect(capOf(second.card)).toBe(expectedCap(0));
    });
  },
);
