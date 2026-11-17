// A sidebar section's header (`#tab-sections` > `.side-section` > `h2.side-section-h`): its fold
// toggle, the count, and the dot that speaks for the rows a fold hides. Every section folds through
// here; its rows are its owner's (tabs.ts, history-section.ts).

import { sectionFolded, setSectionFolded } from "./device-view.js";
import { slideBox } from "./disclosure-row.js";
import { setAttr, setText } from "./dom.js";
import { dragOwnsStrip } from "./tabs-drag.js";

/** One section: `key` names its fold in `sidebar_folds`, `list` is what its fold slides. */
export interface SidebarSection {
  readonly key: string;
  readonly list: HTMLElement;
  readonly foldedByDefault: boolean;
  /** The count's word for a screen reader, made plural by an `s`. */
  readonly noun: "tab" | "chat";
  /** What an empty section hides: Open's list holds the boot skeleton, so only its header goes. */
  readonly emptyHides: "section" | "header";
}

/** What a header shows; `dot` is the first hidden row state, with the words it is described by. */
export interface SectionHeaderView {
  readonly count: number;
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

/** The stored fold of `s`, its default when none is stored. */
export function isSectionFolded(s: SidebarSection): boolean {
  return sectionFolded(s.key, s.foldedByDefault);
}

/** Wire the toggle once: a press stores the fold and slides the list through `paintRows(folded)`,
 *  whose header paint marks the stored fold at once, since `slideBox` applies before it measures.
 *  A fold that hides the focused row hands focus to the toggle: a press that moves no focus (a
 *  WebKit click, a script) would leave it on `body`. Inert while a drag owns the strip. */
export function bindSectionToggle(s: SidebarSection, paintRows: (folded: boolean) => void): void {
  const toggle = sectionToggle(s.list);
  if (toggle === null || bound.has(toggle)) {
    return;
  }
  bound.add(toggle);
  toggle.addEventListener("click", () => {
    if (dragOwnsStrip()) {
      return;
    }
    const folded = !isSectionFolded(s);
    setSectionFolded(s.key, folded);
    const f = document.activeElement;
    const held = f instanceof HTMLElement && s.list.contains(f) ? f : null;
    slideBox(s.list, !folded, (open) => {
      paintRows(!open);
      const now = document.activeElement;
      if (held !== null && (now === held || now === document.body) && !held.checkVisibility()) {
        toggle.focus({ preventScroll: true });
      }
    });
  });
}

/** Paint the header of `s` from `view`, mark its list's stored fold, and hide what an empty
 *  section hides. */
export function paintSectionHeader(s: SidebarSection, view: SectionHeaderView): void {
  const toggle = sectionToggle(s.list);
  const header = toggle?.parentElement ?? null;
  const target = s.emptyHides === "header" ? header : s.list.closest<HTMLElement>(".side-section");
  if (target !== null) {
    target.hidden = view.count === 0;
  }
  if (toggle === null) {
    return;
  }
  const folded = isSectionFolded(s);
  toggle.setAttribute("aria-expanded", String(!folded));
  s.list.toggleAttribute("data-folded", folded);
  setText(toggle.querySelector(".side-section-count"), String(view.count));
  const words = `${String(view.count)} ${s.noun}${view.count === 1 ? "" : "s"}`;
  setText(
    toggle.querySelector(".sr-only"),
    view.dot === null ? words : `${words}, one ${view.dot.phrase}`,
  );
  const dot = toggle.querySelector(".side-section-dot");
  if (dot !== null) {
    setAttr(dot, "data-status", view.dot?.status ?? null);
  }
}
