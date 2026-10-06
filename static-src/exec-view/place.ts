// Where the exec view seats a reconciled child, and why it refuses to re-seat one.

/** Put `node` at `index` inside `into`, touching the DOM only when it is not already there:
 *  `appendChild` on an attached node is a remove plus insert, which restarts every CSS animation
 *  in the subtree (the running row's `vk-spin` ring never completed a turn) and drops `:hover`
 *  and focus. Correct only because callers place children in ASCENDING index order; out of
 *  order it is off by one. */
export function place(into: HTMLElement, node: HTMLElement, index: number): void {
  const current = into.childNodes[index];
  if (current === node) {
    return;
  }
  into.insertBefore(node, current ?? null);
}
