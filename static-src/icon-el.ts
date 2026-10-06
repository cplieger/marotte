// The one icon string → DOM helper. Each SVG string is parsed once into a <template> and cached; callers get clones.
// The HTML template puts <svg> in the SVG namespace though the strings carry no xmlns (an XML parse would not paint).
// innerHTML is safe: the inputs are compile-time constants.

const SVG_NS = "http://www.w3.org/2000/svg";

// Lazy, so importing this module never touches the DOM (node-environment tests).
let iconTemplate: HTMLTemplateElement | null = null;
const iconCache = new Map<string, Element>();

/** Parse an SVG string once, cache it, and return a fresh clone. An input with no element root yields an empty <svg>. */
export function iconEl(svg: string): Element {
  let cached = iconCache.get(svg);
  if (cached === undefined) {
    iconTemplate ??= document.createElement("template");
    iconTemplate.innerHTML = svg;
    cached = iconTemplate.content.firstElementChild ?? document.createElementNS(SVG_NS, "svg");
    iconCache.set(svg, cached);
  }
  return cached.cloneNode(true) as Element;
}
