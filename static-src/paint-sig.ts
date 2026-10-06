/** Rebuild a subtree only when the state it renders moved. */

import { join } from "@cplieger/keyenc";

/** Where the last painted signature is recorded. Distinct from `reconcile`'s
 *  `data-reconcile-key`, so a reconciled row can carry both. */
const SIG_ATTR = "data-sig";

/** Replace `host`'s children with `build()`'s output when `parts` differ from the last call's.
 *  Answers whether it painted. */
export function paintIfChanged(
  host: Element,
  parts: readonly string[],
  build: () => readonly Node[],
): boolean {
  const sig = join(...parts);
  if (host.getAttribute(SIG_ATTR) === sig) {
    return false;
  }
  host.setAttribute(SIG_ATTR, sig);
  host.replaceChildren(...build());
  return true;
}

/** The same guard without the paint, for a caller whose repaint is not one `replaceChildren`.
 *  Answers whether `parts` moved, recording them either way. */
export function sigChanged(host: Element, parts: readonly string[]): boolean {
  const sig = join(...parts);
  if (host.getAttribute(SIG_ATTR) === sig) {
    return false;
  }
  host.setAttribute(SIG_ATTR, sig);
  return true;
}

/** Drop `host`'s recorded signature, for a caller that changed the subtree in place: the next
 *  guard then repaints it. */
export function forgetSig(host: Element): void {
  host.removeAttribute(SIG_ATTR);
}

/** A whole DECODED wire value as one signature part. */
export function wireSignature(value: object): string {
  return JSON.stringify(value);
}
