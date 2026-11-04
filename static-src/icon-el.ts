// The one icon string → DOM helper. Each SVG string is parsed once into a <template> and cached; callers get clones.
// The HTML template puts <svg> in the SVG namespace though the strings carry no xmlns (an XML parse would not paint).
// innerHTML is safe: the inputs are compile-time constants.

import { ICON_MASK_ID } from "./icon-mask.js";

const SVG_NS = "http://www.w3.org/2000/svg";

// Lazy, so importing this module never touches the DOM (node-environment tests).
let iconTemplate: HTMLTemplateElement | null = null;
const iconCache = new Map<string, Element>();
let maskSeq = 0;

/**
 * Parse an SVG string once, cache it, and return a fresh clone. An input with no element root yields an empty <svg>.
 * A badged glyph's mask gets an id of its own per clone: `url(#id)` resolves to the first element in the document
 * with that id, and a shared one inside a hidden tab would leave every later badge unmasked.
 */
export function iconEl(svg: string): Element {
  let cached = iconCache.get(svg);
  if (cached === undefined) {
    iconTemplate ??= document.createElement("template");
    iconTemplate.innerHTML = svg;
    cached = iconTemplate.content.firstElementChild ?? document.createElementNS(SVG_NS, "svg");
    iconCache.set(svg, cached);
  }
  const clone = cached.cloneNode(true) as Element;
  const mask = clone.querySelector(`mask#${ICON_MASK_ID}`);
  if (mask !== null) {
    maskSeq += 1;
    const id = `${ICON_MASK_ID}-${String(maskSeq)}`;
    mask.id = id;
    clone.querySelector(`[mask="url(#${ICON_MASK_ID})"]`)?.setAttribute("mask", `url(#${id})`);
  }
  return clone;
}
