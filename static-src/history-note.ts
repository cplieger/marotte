// History's pane notes, a leaf outside history.ts so tabs.ts can state that the page itself failed to load.

import { el } from "@cplieger/reactive";

/** A note shown in place of a pane's rows; a list item, since both panes are ARIA lists. */
export function paneNote(className: string, ...children: (Node | string)[]): HTMLElement {
  return el("div", { className, role: "listitem" }, ...children);
}

/** A failed read of the History lists, with a Retry that runs `retry`. */
export function historyLoadError(retry: () => void): HTMLElement {
  const button = el("button", { type: "button", className: "btn-small" }, "Retry");
  button.addEventListener("click", retry);
  return paneNote(
    "list-empty history-error",
    el("span", {}, "Could not load previous sessions. Check your connection and try again."),
    button,
  );
}
