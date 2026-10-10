// One inert render path for agent-produced SVG: `<img src="data:image/svg+xml,…">`, inert by
// specification (no scripts, fetches, external styles or DOM access), so no sanitizer is needed.
// INVARIANT: SVG source never reaches an HTML parser sink. It must be an ELEMENT given a `src`;
// conversion runs BEFORE code decoration (markdown.ts `decorate`), so a fence never reaches
// `finalizeBlock`'s innerHTML. A `data:` URL because the CSP is `img-src 'self' data:`
// (internal/server/security.go), and `src` is set OUTSIDE `set_attr_dom`, whose `isSafeUrl`
// rejects `data:`. The carrier is a closed ```svg fence; never add `mermaid` here.

import { el } from "@cplieger/reactive";
import { extractLang } from "./code-blocks.js";

/** Deliberately one member. */
const SVG_FENCE_LANGS: ReadonlySet<string> = new Set(["svg"]);

/** Convert a closed ```svg fence into an inert diagram. Returns the `<figure>` standing where the
 *  `<pre>` (or its provisional `.code-wrap`) was, so the caller skips code decoration; null, with
 *  the `<pre>` untouched, for any other or empty fence. */
export function renderSvgBlock(pre: HTMLElement): HTMLElement | null {
  const code = pre.querySelector("code");
  if (code === null || !SVG_FENCE_LANGS.has(extractLang(pre, code))) {
    return null;
  }
  const source = code.textContent;
  if (source.trim() === "") {
    return null;
  }

  const img = el("img", { className: "svg-diagram" }) as HTMLImageElement;
  // A diagram with no description is an empty announcement to a screen reader.
  // There is nothing on the wire that describes it, so the alt names the KIND
  // rather than inventing content.
  img.alt = "Agent-produced diagram";
  const figure = el("figure", { className: "svg-figure" }, img);
  img.addEventListener(
    "error",
    () => {
      // Malformed SVG: a named message like the missing-image affordance; the source is not rebuilt.
      img.replaceWith(el("span", { className: "img-missing" }, "Diagram could not be rendered"));
    },
    { once: true },
  );
  // REPLACE THE WRAPPER: a streamed fence was provisionally decorated (`.code-wrap` + title bar,
  // `decorateStreamingCodeTail`); replay never is, so swapping only the `<pre>` diverged.
  const parent = pre.parentElement;
  const wrap = parent?.classList.contains("code-wrap") === true ? parent : null;
  (wrap ?? pre).replaceWith(figure);
  // After the swap, so a synchronous decode failure has a parent to replace into.
  img.src = `data:image/svg+xml,${encodeURIComponent(source)}`;
  return figure;
}
