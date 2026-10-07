// The segmented switcher's controller: the tab-bar chrome the Settings, docs, git, History and MCP
// bars share. It owns the ARIA wiring, the keyboard model, the icon-only fit and the active-segment
// projection, and nothing else — no route, no panel, no subtitle, no store.

import { rovingFocus } from "@cplieger/ui-primitives/roving-focus";
import { fitTabBar, refitTabBar } from "./tab-bar-fit.js";

interface SegmentedTab<T extends string> {
  readonly id: T;
  readonly label: string;
}

export interface SegmentedBarOpts<T extends string> {
  /** The data attribute naming a segment's tab, e.g. `data-settings-tab`. */
  readonly attr: string;
  /** Prefix for the ids this writes: `<prefix>-tab-<id>` on a segment and `<prefix>-panel-<id>`
   *  in its `aria-controls`. The page's panel pass owns the other half of that pairing. */
  readonly idPrefix: string;
  /** Every segment, in bar order. */
  readonly tabs: readonly SegmentedTab<T>[];
  readonly onSelect: (tab: T) => void;
}

/** Wire a segmented bar and return its `paint`. */
export function initSegmentedBar<T extends string>(
  bar: HTMLElement,
  opts: SegmentedBarOpts<T>,
): (active: T) => void {
  const { attr, idPrefix, tabs, onSelect } = opts;
  // `CSS.escape` because an id is the CALLER's text, not this module's: every bar before the spec
  // page's fed a closed set of literals, and that page's segments are the filenames a spec
  // directory holds, so a name carrying a quote (legal on every filesystem this runs on) made the
  // selector below throw a SyntaxError and took the whole page's paint with it.
  const segment = (tab: T): HTMLButtonElement | null =>
    bar.querySelector<HTMLButtonElement>(`[${attr}="${CSS.escape(tab)}"]`);

  bar.setAttribute("role", "tablist");

  for (const { id, label } of tabs) {
    const btn = segment(id);
    // A bar whose markup is missing a segment is wired for the ones it has rather than throwing:
    // the buttons are static HTML, so an absent one is a markup defect that must not take the whole
    // page down with it.
    if (btn === null) {
      continue;
    }
    btn.setAttribute("role", "tab");
    btn.id = `${idPrefix}-tab-${id}`;
    btn.setAttribute("aria-label", label);
    btn.setAttribute("aria-controls", `${idPrefix}-panel-${id}`);
    btn.addEventListener("click", () => {
      onSelect(id);
    });
  }

  // Drop every label only when one cannot fit.
  fitTabBar(bar);
  // A withdrawn segment cannot take focus, so arrow keys skip it rather than stall on it.
  rovingFocus(bar, `[${attr}]:not(.hidden)`, { orientation: "horizontal" });

  return (active: T): void => {
    for (const { id } of tabs) {
      const btn = segment(id);
      const on = id === active;
      btn?.classList.toggle("active", on);
      btn?.setAttribute("aria-selected", on ? "true" : "false");
      btn?.setAttribute("tabindex", on ? "0" : "-1");
    }
  };
}

/** Withdraw one segment from the bar, or bring it back, and refit the labels to what remains.
 *  The caller moves the selection off a segment it withdraws. */
export function setSegmentHidden(
  bar: HTMLElement,
  attr: string,
  tab: string,
  hidden: boolean,
): void {
  const btn = bar.querySelector<HTMLElement>(`[${attr}="${CSS.escape(tab)}"]`);
  if (btn === null || btn.classList.contains("hidden") === hidden) {
    return;
  }
  btn.classList.toggle("hidden", hidden);
  refitTabBar(bar);
}
