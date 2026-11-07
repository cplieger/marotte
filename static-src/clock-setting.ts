// The Clock row on Settings > General: Automatic, 24-hour or 12-hour, per screen and never sent to
// the server. This module owns the select; `locale-time.ts` owns the choice and what Automatic
// resolves to.

import { LS_UI_STATE_KEY } from "./ls-keys.js";
import type { ClockChoice } from "./device-view.js";
import { $, pageRoots } from "./dom.js";
import { clock, redrawClocks, reloadClock, resolvedClock, setClock } from "./locale-time.js";

const CYCLE_LABEL = { h23: "24-hour", h12: "12-hour" } as const;

let bound: AbortController | null = null;

function redrawAll(): void {
  for (const root of pageRoots()) {
    redrawClocks(root);
  }
}

function asChoice(v: string): ClockChoice | null {
  return v === "auto" || v === "h23" || v === "h12" ? v : null;
}

function paint(): void {
  const select = $.clockSelect;
  const choice = clock();
  select.value = choice;
  // Automatic also names what it resolved to, the one choice whose label does not say.
  const auto = select.querySelector('option[value="auto"]');
  if (auto !== null) {
    auto.textContent =
      choice === "auto" ? `Automatic (now ${CYCLE_LABEL[resolvedClock()]})` : "Automatic";
  }
}

/** Wire the select and paint the stored choice. A change, or another tab's write, redraws the
 *  `time[data-clock]` and `time[data-clock-tip]` elements on the page (`redrawClocks`). */
export function initClockSetting(): void {
  paint();
  bound?.abort();
  bound = new AbortController();
  $.clockSelect.addEventListener(
    "change",
    () => {
      const next = asChoice($.clockSelect.value);
      if (next !== null) {
        setClock(next);
        redrawAll();
      }
      paint();
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
        paint();
        redrawAll();
      }
    },
    { signal: bound.signal },
  );
}
