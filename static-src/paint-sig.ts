/** Rebuild a subtree only when the state it renders moved: for a subtree derived
 *  wholesale from data (a list with identities wants `@cplieger/reactive`'s keyed
 *  `reconcile`). The parts must be TOTAL over what the subtree renders, or it stops
 *  updating. A subtree the reader mutates DIRECTLY (a native radio or checkbox) is
 *  disqualified: after a refused write the model is unchanged and the guard skips
 *  the repaint that corrects the DOM.
 */

import { join } from "@cplieger/keyenc";

/** Where the last painted signature is recorded. Distinct from `reconcile`'s
 *  `data-reconcile-key`, so a reconciled row can carry both. */
const SIG_ATTR = "data-sig";

/**
 * Replace `host`'s children with `build()`'s output when `parts` differ from the
 * last call's. Answers whether it painted.
 *
 * `build` is a thunk so an unchanged subtree costs no construction either.
 */
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

/**
 * The same guard without the paint, for a caller whose repaint is not one
 * `replaceChildren`. Answers whether `parts` moved, recording them either way.
 */
export function sigChanged(host: Element, parts: readonly string[]): boolean {
  const sig = join(...parts);
  if (host.getAttribute(SIG_ATTR) === sig) {
    return false;
  }
  host.setAttribute(SIG_ATTR, sig);
  return true;
}

/** Drop `host`'s recorded signature, for a caller that changed the subtree in
 *  place: the next guard then repaints it. */
export function forgetSig(host: Element): void {
  host.removeAttribute(SIG_ATTR);
}

/**
 * A whole DECODED wire value as one signature part.
 *
 * Total by construction: `JSON.stringify` over a value from `JSON.parse` walks
 * the keys in the producer's order, and Go's `encoding/json` emits struct fields
 * in declaration order. Only for a decoded value — a locally built object's key
 * order is its construction path's, so name its fields through a total
 * `Record<keyof T, string>` instead.
 */
export function wireSignature(value: object): string {
  // `object` rather than `unknown`: the three values `JSON.stringify` answers
  // `undefined` for are not assignable, so this needs no nullish fallback the
  // type system cannot see is live.
  return JSON.stringify(value);
}
