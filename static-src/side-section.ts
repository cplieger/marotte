// A sidebar section whose rows are its owner's, not tabs (History, Runs): it folds through
// section-header.ts as every section does, and seats its rows and puts focus back when a repaint takes
// it away. Outside section-header.ts because the focus chain reads tabs.ts, which imports section-header.ts.

import { isSliding } from "./disclosure-row.js";
import { $, maybeEl } from "./dom.js";
import { type Press, focusAs } from "./legacy-ui/scripted-focus.js";
import {
  bindSectionToggle,
  foldRows,
  isSectionFolded,
  paintSectionHeader,
  sectionToggle,
  type SectionHeaderView,
  type SidebarSection,
} from "./section-header.js";
import { focusHistoryEntry, focusTabRow, getActiveTabId } from "./tabs.js";

/** One row as its owner draws it: `controls[0]` is the row's own control, the rest its others (Delete). */
interface SectionRow {
  readonly ref: string;
  readonly li: HTMLElement;
  readonly controls: readonly HTMLElement[];
}

/** Where focus was in the section: a row's control by its ref and index, or another control (More,
 *  the header), with the rows' order then. */
type SectionFocus =
  | { readonly ref: string; readonly control: number; readonly order: readonly string[] }
  | { readonly at: HTMLElement };

interface SideSectionOptions {
  readonly key: string;
  /** The fold a browser that never pressed the header sees. */
  readonly foldedByDefault: boolean;
  readonly noun: SidebarSection["noun"];
  /** The controls after the rows, in the list's last item (History's More). */
  readonly tail: readonly HTMLElement[];
  /** The header's dot while folded: the first hidden row state, in the owner's precedence. */
  readonly foldDot: () => SectionHeaderView["dot"];
  /** The item a fold still shows, or null. Read on every paint, so a signal it reads repaints the fold. */
  readonly kept: () => HTMLElement | null;
}

export interface SideSection {
  noteFocus(): SectionFocus | null;
  /** A mark at the first control of the row `ref`, in the rows' order as last seated. */
  markRow(ref: string): SectionFocus;
  /** Draw `rows` in this order before the tail: drop the others, seat each only when out of place,
   *  then paint the fold and the header. A re-seat blurs the focused row: `restoreFocus` after. */
  seat(rows: readonly SectionRow[]): void;
  /** When focus is lost (on the body or hidden) after it was at `mark`: the same control, the rows
   *  after it, before it, the tail, the header, the Open section's first row, History's entry, the
   *  composer, else the active tab's row, with the ring of `press`. Focus anywhere shown is left alone. */
  restoreFocus(mark: SectionFocus | null, press?: Press | null): void;
}

function isShown(e: Element | null | undefined): e is HTMLElement {
  return e instanceof HTMLElement && e.isConnected && e.getClientRects().length > 0;
}

/** Wire the section at `#<id>` around its list `#<listId>`; null when the markup is not in the page. */
export function sideSection(id: string, listId: string, o: SideSectionOptions): SideSection | null {
  const section = maybeEl(id);
  const list = maybeEl(listId);
  const toggle = list === null ? null : sectionToggle(list);
  if (section === null || list === null || toggle === null) {
    return null;
  }
  const s: SidebarSection = {
    key: o.key,
    list,
    foldedByDefault: o.foldedByDefault,
    noun: o.noun,
    emptyHides: "section",
  };
  let seated = new Map<string, SectionRow>();
  let order: readonly string[] = [];

  /** Hide the items `folded` hides unless a running slide owns them, and paint the header. Reads
   *  `kept` first, so the caller's effect tracks it. */
  const paintFold = (folded: boolean, slideOwnsRows = false): void => {
    const kept = o.kept();
    if (!slideOwnsRows) {
      foldRows(list.querySelectorAll<HTMLElement>(":scope > li"), folded, (li) => li === kept);
    }
    paintSectionHeader(s, { count: order.length, dot: folded ? o.foldDot() : null });
  };

  const noteFocus = (): SectionFocus | null => {
    const f = document.activeElement;
    if (!(f instanceof HTMLElement) || !section.contains(f)) {
      return null;
    }
    for (const [ref, row] of seated) {
      const control = row.controls.indexOf(f);
      if (control !== -1) {
        return { ref, control, order };
      }
    }
    return { at: f };
  };

  const markRow = (ref: string): SectionFocus => ({ ref, control: 0, order });

  const restoreFocus = (mark: SectionFocus | null, press?: Press | null): void => {
    const now = document.activeElement;
    if (mark === null || (now !== document.body && isShown(now))) {
      return;
    }
    const stops: (HTMLElement | undefined)[] = [];
    if ("at" in mark) {
      stops.push(mark.at);
    } else {
      const i = mark.order.indexOf(mark.ref);
      const first = (ref: string): HTMLElement | undefined => seated.get(ref)?.controls[0];
      stops.push(
        seated.get(mark.ref)?.controls[mark.control],
        ...mark.order.slice(i + 1).map(first),
        ...mark.order.slice(0, Math.max(i, 0)).reverse().map(first),
      );
    }
    const openRow = [
      ...(maybeEl("tab-list")?.querySelectorAll<HTMLElement>(":scope > [data-tab-id]") ?? []),
    ].find((r) => !r.classList.contains("exiting") && isShown(r));
    for (const to of [...stops, ...o.tail, toggle, openRow]) {
      if (isShown(to)) {
        focusAs(to, press);
        if (document.activeElement === to) {
          return;
        }
      }
    }
    if (!focusHistoryEntry(press)) {
      $.promptInput.focus();
      if (document.activeElement !== $.promptInput) {
        focusTabRow(getActiveTabId(), press);
      }
    }
  };

  const seat = (rows: readonly SectionRow[]): void => {
    const next = new Map(rows.map((r) => [r.ref, r]));
    for (const [ref, row] of seated) {
      if (next.get(ref)?.li !== row.li) {
        row.li.remove();
      }
    }
    let prev: HTMLElement | null = null;
    for (const row of rows) {
      const at: Element | null = prev === null ? list.firstElementChild : prev.nextElementSibling;
      if (at !== row.li) {
        if (prev === null) {
          list.prepend(row.li);
        } else {
          prev.after(row.li);
        }
      }
      prev = row.li;
    }
    seated = next;
    order = rows.map((r) => r.ref);
    paintFold(isSectionFolded(s), isSliding(list));
  };

  bindSectionToggle(s, (folded) => {
    paintFold(folded);
  });

  return { noteFocus, markRow, seat, restoreFocus };
}
