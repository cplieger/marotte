// A sidebar tab section's header (`#tab-sections` > `.side-section` > `h2.side-section-h`): its fold
// toggle, the count, and the dot that speaks for the rows a fold hides. The rows are tabs.ts's.

import { sectionFolded, setSectionFolded } from "./device-view.js";
import { slideBox } from "./disclosure-row.js";
import { setAttr, setText } from "./dom.js";
import { dragOwnsStrip } from "./tabs-drag.js";

/** What a header shows; `dot` is the first hidden row state, with the words it is described by. */
export interface SectionHeaderView {
  readonly count: number;
  readonly folded: boolean;
  readonly dot: { readonly status: string; readonly phrase: string } | null;
}

const bound = new WeakSet<HTMLElement>();

/** The header toggle of the section holding `list`, or null for a list with no header. */
export function sectionToggle(list: HTMLElement): HTMLButtonElement | null {
  return (
    list
      .closest(".side-section")
      ?.querySelector<HTMLButtonElement>(":scope > .side-section-h > .side-section-toggle") ?? null
  );
}

/** Wire the toggle once: a press stores the fold and slides the list through `paintRows(folded)`,
 *  whose header paint marks the stored fold at once, since `slideBox` applies before it measures.
 *  Inert while a drag owns the strip. */
export function bindSectionToggle(
  list: HTMLElement,
  key: string,
  paintRows: (folded: boolean) => void,
): void {
  const toggle = sectionToggle(list);
  if (toggle === null || bound.has(toggle)) {
    return;
  }
  bound.add(toggle);
  toggle.addEventListener("click", () => {
    if (dragOwnsStrip()) {
      return;
    }
    const folded = !sectionFolded(key);
    setSectionFolded(key, folded);
    slideBox(list, !folded, (open) => {
      paintRows(!open);
    });
  });
}

/** Paint the header of the section holding `list` from `view`, and mark the list's fold. */
export function paintSectionHeader(list: HTMLElement, view: SectionHeaderView): void {
  const toggle = sectionToggle(list);
  if (toggle === null) {
    return;
  }
  toggle.setAttribute("aria-expanded", String(!view.folded));
  list.toggleAttribute("data-folded", view.folded);
  setText(toggle.querySelector(".side-section-count"), String(view.count));
  const words = `${String(view.count)} ${view.count === 1 ? "tab" : "tabs"}`;
  setText(
    toggle.querySelector(".sr-only"),
    view.dot === null ? words : `${words}, one ${view.dot.phrase}`,
  );
  const dot = toggle.querySelector(".side-section-dot");
  if (dot !== null) {
    setAttr(dot, "data-status", view.dot?.status ?? null);
  }
}
