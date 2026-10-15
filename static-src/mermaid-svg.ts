// What the inline block and the full-screen view share about a pele-drawn SVG (https://pele.run): the render options
// that meet the "App responsibilities" of pele's security policy, the post-render pass over the inserted SVG, and
// the export.

import type * as PeleModule from "pele/lazy";
import type { MountOptions } from "pele/lazy";
import { el } from "@cplieger/reactive";
import { formDiagramLink } from "./link-guard.js";

export type Pele = typeof PeleModule;

let pele: Pele | null = null;

/** pele's lazy entry once imported, else null; a diagram type's own chunk still needs `load`. */
export function loadedPele(): Pele | null {
  return pele;
}

/** Import pele's lazy entry. The page's module map makes a repeat call free once it has loaded, and refetches
 *  after a failed fetch, so nothing here caches either outcome. */
export async function loadPele(): Promise<Pele> {
  pele = await import("pele/lazy");
  return pele;
}

/** Constant and trusted (pele's security policy: never fill `options` or `options.config` from the diagram). No `icons`
 *  resolver, because pele inserts its markup unfiltered. `imageSchemes: []` keeps only relative images, which the
 *  page CSP's `img-src 'self' data:` would hold to this origin anyway. Links keep pele's default schemes. */
export const RENDER_OPTIONS = {
  imageSchemes: [],
  linkRel: "noopener noreferrer",
  // The chat's prose size (`--fs-md`), where pele's 16px default drew labels a size above the text around them.
  fontSize: 14,
} as const satisfies MountOptions;

let idSeq = 0;

/** A prefix no element id the app owns starts with, unique per drawing. */
export function nextIdPrefix(): string {
  idSeq++;
  return `mmd${String(idSeq)}-`;
}

/** The accessible name a diagram with no `accTitle` gets; pele's `aria-roledescription` names the kind beside it. */
const UNTITLED_LABEL = "Mermaid diagram";

/** The diagram's `accTitle`, or null when it has none. */
export function diagramTitle(svg: SVGSVGElement): string | null {
  const title = svg.querySelector(":scope > title")?.textContent.trim() ?? "";
  return title === "" ? null : title;
}

/**
 * Make the SVG pele just inserted into `host` safe and nameable in this page; returns it, or null when there is none.
 * Every id is prefixed: pele copies diagram names into ids (a click target, a node), and an untrusted `id="composer"`
 * would shadow the app's own element for `getElementById` and on `window`. Run synchronously after each insert.
 */
export function adoptSvg(host: HTMLElement, prefix: string): SVGSVGElement | null {
  const svg = host.querySelector(":scope > svg");
  if (!(svg instanceof SVGSVGElement)) {
    return null;
  }
  for (const node of svg.querySelectorAll("[id]")) {
    node.id = prefix + node.id;
  }
  for (const attr of ["aria-labelledby", "aria-describedby"]) {
    const refs = svg.getAttribute(attr);
    if (refs !== null) {
      svg.setAttribute(
        attr,
        refs
          .split(/\s+/)
          .filter((r) => r !== "")
          .map((r) => prefix + r)
          .join(" "),
      );
    }
  }
  // `img` makes every child presentational: a drawing with links keeps pele's `graphics-document` role, or its links
  // could be focused but never announced.
  if (svg.querySelector("a") === null) {
    svg.setAttribute("role", "img");
  }
  if (!svg.hasAttribute("aria-labelledby")) {
    svg.setAttribute("aria-label", UNTITLED_LABEL);
  }
  for (const a of svg.querySelectorAll<SVGAElement>("a")) {
    const href = a.getAttribute("href");
    if (href !== null) {
      // Like every rendered model link (link-guard.ts `linkAnchor`): a new tab, never this one.
      a.setAttribute("target", "_blank");
      formDiagramLink(a, href);
    }
  }
  return svg;
}

const PELE_VAR = /var\(\s*(--pele-[\w-]+)\s*(?:,([^()]*))?\)/g;
const PRIVATE_DECL = /(--_[\w-]+)\s*:\s*((?:&[a-z]+;|[^;"])+)/g;
const PRIVATE_VAR = /var\(\s*(--_[\w-]+)\s*\)/g;

let probeCtx: CanvasRenderingContext2D | null | undefined;

function colourProbe(): CanvasRenderingContext2D | null {
  if (probeCtx === undefined) {
    const canvas = document.createElement("canvas");
    canvas.width = 1;
    canvas.height = 1;
    probeCtx = canvas.getContext("2d", { willReadFrequently: true });
  }
  return probeCtx;
}

/** `value` as sRGB hex when it is a colour, so an `oklch()` or `color-mix()` token survives an editor that predates
 *  them; anything else, or a page with no 2D canvas, is returned unchanged. */
function srgb(value: string): string {
  const probe = CSS.supports("color", value) ? colourProbe() : null;
  if (probe === null) {
    return value;
  }
  probe.clearRect(0, 0, 1, 1);
  probe.fillStyle = value;
  probe.fillRect(0, 0, 1, 1);
  const [r = 0, g = 0, b = 0, a = 0] = probe.getImageData(0, 0, 1, 1).data;
  const hex = (n: number): string => n.toString(16).padStart(2, "0");
  return `#${hex(r)}${hex(g)}${hex(b)}${a === 255 ? "" : hex(a)}`;
}

/** A CSS value inside a double-quoted attribute: a quoted font family takes single quotes. */
function attrSafe(value: string): string {
  return value.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/"/g, "'");
}

/**
 * pele's SVG text with every `var()` resolved against `scope`'s computed theme, so the file draws the diagram as the
 * reader saw it in any viewer; the root also gets the theme's background, or a dark theme's light ink lands on white.
 */
export function exportableSvg(svg: string, scope: Element): string {
  const style = getComputedStyle(scope);
  const peleValue = (name: string, fallback: string | undefined): string =>
    srgb(style.getPropertyValue(name).trim() || (fallback ?? "").trim());
  let out = svg.replace(PELE_VAR, (_m, name: string, fallback: string | undefined) =>
    attrSafe(peleValue(name, fallback)),
  );
  const privates = new Map<string, string>();
  for (const [, name = "", value = ""] of out.matchAll(PRIVATE_DECL)) {
    if (!privates.has(name)) {
      privates.set(name, value.trim());
    }
  }
  out = out.replace(PRIVATE_VAR, (whole, name: string) => privates.get(name) ?? whole);
  const bg = attrSafe(peleValue("--pele-bg", undefined));
  return bg === "" ? out : out.replace(/^(<svg\b[^>]*?\sstyle=")/, `$1background-color:${bg};`);
}

/** Save `svg` (pele's text, not the page's adopted copy) as `diagram.svg`, resolved against `scope`. */
export function downloadSvg(svg: string, scope: Element): void {
  const blob = new Blob([exportableSvg(svg, scope)], { type: "image/svg+xml" });
  const url = URL.createObjectURL(blob);
  const a = el("a", { href: url, download: "diagram.svg", rel: "noopener" });
  document.body.appendChild(a);
  a.click();
  a.remove();
  setTimeout(() => {
    URL.revokeObjectURL(url);
  }, 0);
}
