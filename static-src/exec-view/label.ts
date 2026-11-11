// The one writer of a node's label on every exec surface: the verbatim text, with its scope
// qualifier in a span of its own so a bounded column can dim it and give it up first.

import { el } from "@cplieger/reactive";
import { join } from "@cplieger/keyenc";
import type { ExecNode } from "./model.js";

/** What each target last painted, so the 1s tick rewrites nothing. */
const painted = new WeakMap<HTMLElement, string>();

/** Write `node.label` into `target`. With a `qualifier` that is a strict prefix of the label, the
 *  target takes `ev-qualified` and two spans (31-exec-view.css); its text stays the whole label, so
 *  the accessible name, copy and find read the id KAS uses. */
export function paintLabel(target: HTMLElement, node: Pick<ExecNode, "label" | "qualifier">): void {
  const q = node.qualifier ?? "";
  const qualified = q !== "" && node.label.length > q.length && node.label.startsWith(q);
  const sig = join(qualified ? q : "", node.label);
  if (painted.get(target) === sig) {
    return;
  }
  painted.set(target, sig);
  target.classList.toggle("ev-qualified", qualified);
  if (!qualified) {
    target.textContent = node.label;
    return;
  }
  target.replaceChildren(
    // `bdi` keeps the trailing `/` on the right while the span runs rtl to ellipsize from its start.
    el("span", { className: "ev-q" }, el("bdi", { dir: "ltr" }, q)),
    el("span", { className: "ev-leaf" }, node.label.slice(q.length)),
  );
}
