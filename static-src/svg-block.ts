// One inert render path for agent-produced SVG: `<img src="data:image/svg+xml,…">`, inert by
// specification (no scripts, fetches, external styles or DOM access), so no sanitizer is needed.
// INVARIANT: SVG source never reaches an HTML parser sink. It must be an ELEMENT given a `src`.
// A `data:` URL because the CSP is `img-src 'self' data:` (internal/server/security.go), and the
// `src` is set here rather than through the markdown gate, which refuses `data:`. The carrier is a
// closed ```svg fence (markdown.ts's `code` hook); a ```mermaid one is `mermaid-block.ts`'s, which
// draws markup and so cannot share this inert path.

import { el } from "@cplieger/reactive";

/** The inert diagram a closed ```svg fence's `source` becomes, to stand where its code block was;
 *  null for an empty fence. */
export function renderSvgBlock(source: string): HTMLElement | null {
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
  img.src = `data:image/svg+xml,${encodeURIComponent(source)}`;
  return figure;
}
