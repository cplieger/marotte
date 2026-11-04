import { iconEl } from "../icon-el.js";
import { ICON_MASK_ID } from "../icon-mask.js";

const CLONE_MASK_ID_RE = new RegExp(`${ICON_MASK_ID}-\\d+`, "g");

/** A drawn glyph's markup with `iconEl`'s per-clone mask id put back, so it compares equal to its registry SVG's. */
export function glyphMarkup(node: Element | null | undefined): string | null {
  return node?.outerHTML.replace(CLONE_MASK_ID_RE, ICON_MASK_ID) ?? null;
}

/** The markup a registry SVG draws once inserted. */
export function registryMarkup(svg: string): string {
  return glyphMarkup(iconEl(svg)) ?? "";
}
