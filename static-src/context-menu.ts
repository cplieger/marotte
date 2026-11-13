// Right-click menu over `./legacy-ui/popover.js`'s `createPopover` at `pointAnchor(x, y)`. The controller owns dismissal
// and Escape (isolated, so a menu inside a modal does not close the modal) and on-screen placement.

import { el } from "@cplieger/reactive";
import { createPopover, pointAnchor } from "./legacy-ui/popover.js";
import { rovingFocus } from "./legacy-ui/roving-focus.js";

export interface ContextMenuItem {
  label: string;
  action: () => void;
  /** A disabled item stays in the menu and its roving focus (WAI-ARIA menu pattern); a click on it leaves the menu open. */
  disabled?: boolean;
}

interface ContextMenuPosition {
  x: number;
  y: number;
}

/** Show a menu at a viewport point; dismisses on outside click or Escape. Returns a dismiss function.
 *  `returnFocus` takes focus back when the menu closes with focus inside it (Escape, an item). */
export function showContextMenu(
  items: ContextMenuItem[],
  position: ContextMenuPosition,
  opts?: { returnFocus?: HTMLElement },
): () => void {
  const menu = el("div", { className: "tab-context-menu", role: "menu" });

  // Created before the item handlers so they can close it.
  const pop = createPopover(pointAnchor(position.x, position.y), menu, {
    placement: "bottom",
    align: "start",
    offset: 0,
    ...(opts?.returnFocus === undefined ? {} : { returnFocus: opts.returnFocus }),
    onClose: () => {
      menu.remove();
    },
  });

  for (const item of items) {
    const btn = el("button", { className: "tab-context-item", role: "menuitem" }, item.label);
    if (item.disabled === true) {
      btn.setAttribute("aria-disabled", "true");
    }
    btn.addEventListener("click", () => {
      if (item.disabled === true) {
        return;
      }
      pop.hide();
      item.action();
    });
    menu.appendChild(btn);
  }

  rovingFocus(menu, ".tab-context-item");

  pop.show();

  // The popover leaves focus to the caller.
  menu.querySelector<HTMLButtonElement>("button")?.focus();

  return () => {
    pop.hide();
  };
}
