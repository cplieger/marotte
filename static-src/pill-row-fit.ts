// Which pills the composer row has room for. A row too narrow for every control at its tier's target moves Mode, then
// Security profile, into the Chat options card (chat-options.ts), then drops Cancel's and Send's words, then the task
// list's count, and undoes each once it fits again. The row's controls never shrink below their square (15-input.css),
// so an overflowing row is the one signal, whatever the device or pointer.

import { signal } from "@cplieger/reactive";

/** The pills that can leave the row, in the order they leave. */
export type MovablePill = "mode" | "profile";
const ORDER: readonly MovablePill[] = ["mode", "profile"];
/** The steps the fit takes: each pill in `ORDER`, then the send buttons' words, then the task list's count. */
const WORDLESS = ORDER.length + 1;
const STEPS = WORDLESS + 1;

/** The pills the row has moved into the Chat options card. */
export const movedPills = signal<readonly MovablePill[]>([]);

/** Keep `row` fitting as its width or its controls change. `closeCards` runs when a moved pill's card was open, since
 *  the card leaves with its slot. Returns the disconnect. */
export function watchPillRow(
  row: HTMLElement,
  closeCards: () => void = () => undefined,
): () => void {
  const fit = (): void => {
    let n = 0;
    apply(row, n);
    while (n < STEPS && row.scrollWidth > row.clientWidth) {
      n += 1;
      apply(row, n);
    }
    commit(row, ORDER.slice(0, n), closeCards);
  };
  const ro = new ResizeObserver(fit);
  ro.observe(row);
  // Mutations land before the frame paints, so a control that appears (a turn's split Send, the task list) and
  // overflows the row is moved out before it is ever drawn.
  const mo = new MutationObserver((records) => {
    if (records.some((r) => !ownWrite(r))) {
      fit();
    }
  });
  // Among attributes only class and hidden move a control's width (names, tooltips and --ctx-ink do not); text stays
  // observed because the task list's count is a text node.
  mo.observe(row, {
    subtree: true,
    childList: true,
    characterData: true,
    attributeFilter: ["class", "hidden"],
  });
  fit();
  return () => {
    ro.disconnect();
    mo.disconnect();
    movedPills.value = [];
  };
}

function apply(row: HTMLElement, n: number): void {
  const moved = ORDER.slice(0, n).join(" ");
  if (moved === "") {
    row.removeAttribute("data-moved");
  } else if (row.dataset["moved"] !== moved) {
    row.dataset["moved"] = moved;
  }
  flag(row, "data-wordless", n >= WORDLESS);
  flag(row, "data-countless", n >= STEPS);
}

function flag(row: HTMLElement, name: string, on: boolean): void {
  if (row.hasAttribute(name) !== on) {
    row.toggleAttribute(name, on);
  }
}

function commit(row: HTMLElement, moved: readonly MovablePill[], closeCards: () => void): void {
  const before = movedPills.peek();
  if (moved.length === before.length) {
    return;
  }
  movedPills.value = moved;
  if (moved.some((m) => row.querySelector(`.pill-slot-${m} > .pill-expanded`) !== null)) {
    closeCards();
  }
}

/** The cards' contents, which take no room in the row. */
function ownWrite(r: MutationRecord): boolean {
  const target = r.target instanceof Element ? r.target : r.target.parentElement;
  return (target?.closest(".pill-expand-content") ?? null) !== null;
}
