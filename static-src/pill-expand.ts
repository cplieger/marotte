// Expandable pills: click or keyboard to expand a pill into a detail card anchored to
// the pill's position. One pill open at a time; click outside, click the pill, or press
// Escape to collapse.
//
// The popup lifecycle — outside-click dismissal, Escape, single-open coordination,
// trigger ARIA (aria-expanded / aria-haspopup), and the enter/leave state classes with
// transition-end settling — is @cplieger/ui-primitives' createPopup, the
// non-positioning popup primitive. The card is a SIBLING of the pill inside .pill-slot,
// which positions it, so popover's placement engine is deliberately not involved;
// pill-expand.test.ts states what breaks when a card is nested back inside its trigger.
// What is left here is the pill-specific glue: the toggle wiring, the .pill-expanded
// skin class, and the legacy hidden-class normalization. Enter/exit motion stays in
// 15-input.css, keyed off the library's is-open class on .pill-expand-content.

import { closePopupGroup, createPopup } from "@cplieger/ui-primitives/popup";

import type { ViewportBox } from "./viewport-frame.js";
import { onViewportChange, viewportBox, viewportMoved } from "./viewport-frame.js";

/** Single-open coordination group shared by every expandable pill. */
const PILL_GROUP = "pill-expand";

export function makeExpandable(
  pill: HTMLElement,
  contentEl: HTMLElement,
  opts?: {
    onExpand?: () => void;
    onCollapse?: () => void;
    signal?: AbortSignal;
    haspopup?: "menu" | "listbox" | "tree" | "grid" | "dialog" | true;
  },
): void {
  const listenerOpts = opts?.signal !== undefined ? { signal: opts.signal } : undefined;
  let releaseViewport: (() => void) | undefined;

  // Normalize the legacy display state: consumers author the card with the
  // `hidden` utility CLASS; the popup primitive drives the `[hidden]`
  // ATTRIBUTE plus the is-open / is-leaving state classes.
  contentEl.classList.remove("hidden");
  contentEl.hidden = true;

  // Collapsed ARIA present before the first toggle (createPopup writes the
  // same attributes on show/hide).
  pill.setAttribute("aria-expanded", "false");
  pill.setAttribute("aria-haspopup", String(opts?.haspopup ?? "true"));

  const popup = createPopup(contentEl, {
    trigger: pill,
    group: PILL_GROUP,
    // The old document-level Escape handler let the key keep propagating to
    // the app's global key handling; keep that contract.
    isolateEscape: false,
    ...(opts?.haspopup !== undefined ? { haspopup: opts.haspopup } : {}),
    onOpen: () => {
      pill.classList.add("pill-expanded");
      opts?.onExpand?.();
      // Consumers such as the model and mode pickers build their rows on open;
      // position from that final synchronous width, not the empty card's width.
      releaseViewport?.();
      releaseViewport = trackViewport(pill, contentEl);
    },
    onClose: () => {
      releaseViewport?.();
      releaseViewport = undefined;
      pill.classList.remove("pill-expanded");
      opts?.onCollapse?.();
    },
  });

  pill.addEventListener(
    "click",
    (e: MouseEvent) => {
      const target = e.target as HTMLElement;
      // A card OUTSIDE the pill (every consumer today, see the header) sends
      // no click here at all. The guard stays for a consumer that nests its
      // card: clicks on the card's CONTENT must not toggle, because the
      // buttons and inputs inside an expanded card have to work.
      if (contentEl.contains(target) && target !== contentEl) {
        return;
      }
      // Shield other document-level click handlers from pill toggles, exactly
      // like the old delegated implementation did.
      e.stopPropagation();
      popup.toggle();
    },
    listenerOpts,
  );

  // Keyboard: Enter/Space to toggle, Escape (while focus is on the pill) to
  // collapse — the popup's own document-level Escape covers the general case.
  pill.addEventListener(
    "keydown",
    (e: KeyboardEvent) => {
      if (e.key === "Enter" || e.key === " ") {
        e.preventDefault();
        popup.toggle();
      } else if (e.key === "Escape" && popup.isOpen) {
        e.preventDefault();
        popup.hide();
      }
    },
    listenerOpts,
  );

  // A consumer tearing down via its AbortSignal also drops the popup wiring.
  opts?.signal?.addEventListener("abort", () => {
    popup.dispose();
  });
}

/** Measure the card's clamp now, then re-measure while it is open; the returned
 *  function detaches. iOS answers a raised keyboard by shrinking and OFFSETTING the
 *  visual viewport inside an unchanged layout viewport, which no `dvh` unit sees
 *  (https://developer.mozilla.org/en-US/docs/Web/API/VisualViewport), and tapping a
 *  pill blurs the composer, so the open's own read lands mid-dismissal. The gate is
 *  the frame moving since the last APPLIED pass, not since the last event: two
 *  sub-pixel steps in one frame each fail a per-event test and never apply. It reads
 *  the BLOCK axis only, so an inline-only viewport move re-clamps nothing. */
function trackViewport(pill: HTMLElement, card: HTMLElement): () => void {
  let applied: ViewportBox = viewportBox();
  let frame = 0;
  clampToViewport(pill, card);

  const release = onViewportChange(() => {
    if (frame !== 0 || !viewportMoved(viewportBox(), applied)) {
      return;
    }
    frame = requestAnimationFrame(() => {
      frame = 0;
      applied = viewportBox();
      clampToViewport(pill, card);
    });
  });

  return () => {
    if (frame !== 0) {
      cancelAnimationFrame(frame);
      frame = 0;
    }
    release();
  };
}

/** The smallest block size worth clamping to. Below this a card is unusable
 *  whatever it does, so the floor keeps one row plus its scroll affordance on
 *  screen and lets the overflow do the rest, rather than resolving to a height
 *  that renders nothing. Only reachable on a viewport short enough that the
 *  composer is nearly at the top edge. */
const MIN_CARD_BLOCK_PX = 96;

/** Keep an expanded card inside the visual viewport, on BOTH axes; `trackViewport` owns
 *  when this runs. Inline: the card stays a sibling positioned by `.pill-slot`, so only
 *  its inline offset moves, and the transform origin follows the trigger, which keeps a
 *  clamped card growing from the pill that opened it. Block: the card is anchored
 *  `bottom: calc(100% + var(--sp-1))` and grows UPWARD, so what bounds it is the room
 *  between the viewport's top edge and the card's own bottom, published as
 *  `--pill-max-block` — 15-input.css owns the two bounds it feeds and why they replaced
 *  two authored caps. */
function clampToViewport(pill: HTMLElement, card: HTMLElement): void {
  const slot = pill.parentElement;
  const width = card.offsetWidth;
  if (slot === null || width <= 0) {
    return;
  }
  const viewport = viewportBox();
  const viewportLeft = viewport.offsetLeft;
  const viewportRight = viewportLeft + viewport.width;
  const margin = popupViewportMargin(card);
  const minLeft = viewportLeft + margin;
  const maxLeft = Math.max(minLeft, viewportRight - margin - width);
  const naturalLeft = slot.getBoundingClientRect().left;
  const cardLeft = Math.min(Math.max(naturalLeft, minLeft), maxLeft);
  const pillRect = pill.getBoundingClientRect();

  card.style.setProperty("--pill-inline-shift", `${String(cardLeft - naturalLeft)}px`);
  card.style.setProperty(
    "--pill-origin-x",
    `${String(pillRect.left + pillRect.width / 2 - cardLeft)}px`,
  );

  // The card's own bottom rather than the pill's top, so the `--sp-1` gap between
  // them needs no second reader here. It is stable under the enter animation:
  // `transform-origin` is `bottom`, so the scale leaves that edge where it is.
  const room = card.getBoundingClientRect().bottom - viewport.offsetTop - margin;
  card.style.setProperty(
    "--pill-max-block",
    `${String(Math.max(Math.round(room), MIN_CARD_BLOCK_PX))}px`,
  );
}

function popupViewportMargin(card: HTMLElement): number {
  const raw = getComputedStyle(card).getPropertyValue("--pill-viewport-margin").trim();
  const n = Number.parseFloat(raw);
  if (Number.isFinite(n)) {
    if (raw.endsWith("rem")) {
      const rootSize = Number.parseFloat(getComputedStyle(document.documentElement).fontSize);
      return n * (Number.isFinite(rootSize) ? rootSize : 16);
    }
    if (raw.endsWith("px")) {
      return n;
    }
  }
  return 12;
}

export function collapseAll(): void {
  closePopupGroup(PILL_GROUP);
}
