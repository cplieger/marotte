// The Clock toggle in the sidebar header: Automatic, 24-hour and 12-hour, per screen. This module
// owns the button; `locale-time.ts` owns the choice and what Automatic resolves to.

import { LS_UI_STATE_KEY } from "./ls-keys.js";
import type { ClockChoice } from "./device-view.js";
import { $, forceReflow } from "./dom.js";
import { clock, redrawClocks, reloadClock, resolvedClock, setClock } from "./locale-time.js";

const NEXT: Record<ClockChoice, ClockChoice> = { auto: "h23", h23: "h12", h12: "auto" };

const LABEL: Record<ClockChoice, string> = {
  auto: "automatic",
  h23: "24-hour",
  h12: "12-hour",
};

const GLYPH: Record<ClockChoice, string> = {
  auto: ".clock-icon-auto",
  h23: ".clock-icon-h23",
  h12: ".clock-icon-h12",
};

/** The glyph on screen; null before the first paint, which slides nothing out. */
let shown: ClockChoice | null = null;
/** Bumped by every paint, so a settle deferred behind the slide yields to a newer click. */
let generation = 0;
let bound: AbortController | null = null;

/** The same slide as the theme and touch toggles (10-shell-app.css "THE ICON-SWAP SLIDE"). */
function paint(choice: ClockChoice): void {
  const btn = $.clockBtn;
  const incoming = btn.querySelector(GLYPH[choice]);
  if (incoming === null) {
    return;
  }
  const outgoing = shown === null || shown === choice ? null : btn.querySelector(GLYPH[shown]);
  shown = choice;
  generation += 1;
  const mine = generation;

  let settled = false;
  const settle = (): void => {
    if (settled || mine !== generation) {
      return;
    }
    settled = true;
    for (const c of Object.keys(GLYPH) as ClockChoice[]) {
      const g = btn.querySelector(GLYPH[c]);
      g?.classList.remove("icon-setting", "icon-rising");
      g?.classList.toggle("hidden", c !== choice);
    }
    incoming.classList.add("icon-rising");
    forceReflow(incoming);
    incoming.classList.remove("icon-rising");
  };
  if (outgoing === null) {
    settle();
  } else {
    outgoing.classList.add("icon-setting");
    outgoing.addEventListener("transitionend", settle, { once: true });
    setTimeout(settle, 350);
  }

  // Automatic also names what it resolved to, the one state whose face does not say.
  const now = choice === "auto" ? ` (now ${LABEL[resolvedClock()]})` : "";
  const label = `Clock: ${LABEL[choice]}${now}. Switch to ${LABEL[NEXT[choice]]}`;
  btn.setAttribute("data-tooltip", label);
  btn.setAttribute("aria-label", label);
}

/** Wire the toggle and paint the stored choice. A click, or another tab's write, redraws the
 *  `time[data-clock]` and `time[data-clock-tip]` elements on the page (`redrawClocks`). */
export function initClockToggle(): void {
  shown = null;
  paint(clock());
  bound?.abort();
  bound = new AbortController();
  $.clockBtn.addEventListener(
    "click",
    () => {
      setClock(NEXT[clock()]);
      paint(clock());
      redrawClocks(document);
    },
    { signal: bound.signal },
  );
  addEventListener(
    "storage",
    (e) => {
      if (e.key !== LS_UI_STATE_KEY && e.key !== null) {
        return;
      }
      const before = clock();
      reloadClock();
      if (clock() !== before) {
        paint(clock());
        redrawClocks(document);
      }
    },
    { signal: bound.signal },
  );
}
