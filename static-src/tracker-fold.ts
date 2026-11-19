// The fold of a tracker above the composer (the run bar, the jobs tracker): from `HEADER_AT` items a header row heads
// the list with a summary toggle, and the list starts folded from `FOLD_AT` items until the reader chooses.

import { el } from "@cplieger/reactive";
import { chevronEl } from "./chevron.js";
import { isSliding, selectableButton, slideBox, targetOpen } from "./disclosure-row.js";
import { setAttr, setText } from "./dom.js";
import { FOLD_AT } from "./stack-fold.js";

/** From this many items the header row shows; one item gets none. */
const HEADER_AT = 2;

interface TrackerFoldSpec {
  /** The list the fold slides and folds (`.tracker-host > [data-folded]`, 26-dock.css). */
  readonly list: HTMLElement;
  /** The header row the toggle opens; the owner may append its own controls after it. */
  readonly header: HTMLElement;
  /** The summary's mark, painted by the owner. */
  readonly glyph: HTMLElement;
  /** How many items the list shows now, read when the reader toggles. */
  readonly count: () => number;
  /** The reader's last choice on this device, or null when never chosen. */
  readonly stored: () => boolean | null;
  readonly store: (open: boolean) => void;
  /** Repaints the owner after a toggle, which ends in `paint`. */
  readonly repaint: () => void;
}

export interface TrackerFold {
  /** Brings the header and the fold to `count` items under `summary`; `resized` says rows came or went. */
  paint(count: number, summary: string, resized: boolean): void;
  /** The toggle, once a header has shown. */
  toggle(): HTMLElement | null;
}

export function trackerFold(spec: TrackerFoldSpec): TrackerFold {
  const { list, header } = spec;
  let btn: HTMLElement | null = null;
  const words = el("span", { className: "tracker-count" });
  /** Whether the list shows its rows for `count` items. */
  const isOpen = (count: number): boolean =>
    count < HEADER_AT || (spec.stored() ?? count < FOLD_AT);
  const apply = (open: boolean): void => {
    list.toggleAttribute("data-folded", !open);
  };

  const build = (): HTMLElement => {
    const b = selectableButton("div", "tracker-toggle", () => {
      const open = !isOpen(spec.count());
      spec.store(open);
      // 0 is `.tracker-host > [data-folded]`'s `block-size` (26-dock.css).
      slideBox(list, open, apply, { fade: true, closedHeight: 0 });
      spec.repaint();
    });
    b.setAttribute("aria-expanded", "false");
    b.append(
      el("span", { className: "tracker-chev", "aria-hidden": "true" }, chevronEl()),
      spec.glyph,
      words,
    );
    return b;
  };

  /** Brings the list to `open` at once, unless the reader's slide is already heading there: it owns the attribute
   *  until it ends, so a repaint mid-close keeps the rows shown. An open whose rows came or went slides on to the
   *  new height; a slide heading the other way (the header left) is cut. */
  const settle = (open: boolean, resized: boolean): void => {
    if (!isSliding(list)) {
      apply(open);
    } else if (targetOpen(list, open) !== open) {
      slideBox(list, open, apply, { motion: false });
    } else if (open && resized) {
      slideBox(list, true, apply, { fade: true });
    }
  };

  return {
    paint(count, summary, resized) {
      const open = isOpen(count);
      settle(open, resized);
      const headed = count >= HEADER_AT;
      setAttr(header, "hidden", headed ? null : "");
      if (!headed) {
        return;
      }
      if (btn === null) {
        btn = build();
        header.prepend(btn);
      }
      setText(words, summary);
      setAttr(btn, "aria-expanded", String(open));
    },
    toggle: () => btn,
  };
}
