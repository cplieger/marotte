// A sidebar section whose rows are its owner's, not tabs (History, Runs): its fold, its header, the
// rows' seating and where focus goes when a repaint takes it away. Outside section-header.ts because
// the focus chain reads tabs.ts, which imports section-header.ts.

import { sectionFolded, setSectionFolded } from "./device-view.js";
import { isSliding, slideBox } from "./disclosure-row.js";
import { $, maybeEl } from "./dom.js";
import { type Press, focusAs } from "./legacy-ui/scripted-focus.js";
import { paintSectionHeader, type SectionHeaderView } from "./section-header.js";
import { focusHistoryEntry, focusTabRow, getActiveTabId } from "./tabs.js";
import { dragOwnsStrip } from "./tabs-drag.js";

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

export interface SideSectionOptions {
  readonly key: string;
  /** The fold a browser that never pressed the header sees. */
  readonly foldedByDefault: boolean;
  readonly noun: SectionHeaderView["noun"];
  /** The controls after the rows, in the list's last item (History's More). */
  readonly tail: readonly HTMLElement[];
  /** The header's dot while folded: the first hidden row state, in the owner's precedence. */
  readonly foldDot: () => SectionHeaderView["dot"];
}

export interface SideSection {
  readonly toggle: HTMLButtonElement;
  folded(): boolean;
  noteFocus(): SectionFocus | null;
  /** Draw `rows` in this order: drop the others, seat each only when out of place, then paint the
   *  header and hide the section while empty. A re-seat blurs the focused row: `restoreFocus` after. */
  seat(rows: readonly SectionRow[]): void;
  /** Repaint the header from the rows as last seated, for a state change that moved no row. */
  paintHeader(): void;
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
  const header = section?.querySelector<HTMLElement>(":scope > h2.side-section-h") ?? null;
  const toggle = header?.querySelector<HTMLButtonElement>(".side-section-toggle") ?? null;
  if (section === null || list === null || header === null || toggle === null) {
    return null;
  }
  let seated = new Map<string, SectionRow>();
  let order: readonly string[] = [];
  const folded = (): boolean => sectionFolded(o.key, o.foldedByDefault);

  const paintHeader = (): void => {
    const f = folded();
    paintSectionHeader(list, {
      noun: o.noun,
      count: order.length,
      folded: f,
      dot: f ? o.foldDot() : null,
    });
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
    section.hidden = rows.length === 0;
    paintHeader();
    if (!isSliding(section)) {
      list.hidden = folded();
    }
  };

  toggle.addEventListener("click", () => {
    if (dragOwnsStrip()) {
      return;
    }
    const fold = !folded();
    setSectionFolded(o.key, fold);
    paintHeader();
    // The section less its list is its height while the list is hidden, so the close needs no measuring layout.
    const closed = fold
      ? {
          closedHeight:
            section.getBoundingClientRect().height - list.getBoundingClientRect().height,
        }
      : {};
    // A click that moves no focus (WebKit) leaves it on a row the fold hides.
    const mark = noteFocus();
    slideBox(
      section,
      !fold,
      (open) => {
        list.hidden = !open;
        restoreFocus(mark);
      },
      closed,
    );
  });

  return { toggle, folded, noteFocus, seat, paintHeader, restoreFocus };
}
