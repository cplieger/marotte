import { effect, el, signal, touch } from "@cplieger/reactive";
import { payloadShaped } from "@cplieger/ui/markdown";
import { pageRoots } from "./dom.js";
import { cutAt } from "./strings.js";
import type { EffectiveSettings } from "./wire/types.gen.js";

/** Marks both forms of a guarded link, so a switch flip finds every one. Empty: the address lives in `payloadUrls`. */
const PAYLOAD_ATTR = "data-payload-url";
// Never read back from the DOM, where any writer could have replaced it.
const payloadUrls = new WeakMap<Element, string>();
const CHUNK_ENTER_ATTR = "data-vk-chunk-enter";
const CHUNK_SETTLED_ATTR = "data-vk-chunk-settled";

// True until the server answers, so the boot paint fails closed.
const guardOn = signal(true);

let copyLink: ((url: string) => void) | null = null;

export function setLinkCopyCallback(cb: (url: string) => void): void {
  copyLink = cb;
}

export function adoptLinkGuard(s: EffectiveSettings): void {
  guardOn.value = s.guard_payload_links;
}

/** A payload link let through: what @cplieger/ui/markdown gives every link that leaves the page. */
function openLink(a: Element, url: string): void {
  a.removeAttribute("data-cp-withheld");
  a.querySelector(':scope > [data-part="dest"]')?.remove();
  a.setAttribute("href", url);
  a.setAttribute("target", "_blank");
  a.setAttribute("rel", "noopener noreferrer nofollow");
}

function hostOf(url: string): string {
  try {
    return new URL(url, document.baseURI).host;
  } catch {
    return cutAt(url, 40);
  }
}

function withheldButton(url: string): HTMLButtonElement {
  const host = hostOf(url);
  const b = el("button") as HTMLButtonElement;
  b.type = "button";
  b.className = "link-withheld";
  b.setAttribute("aria-label", `Copy link to ${host}`);
  b.dataset["tooltip"] =
    `Copy link to ${host}. Its address carries encoded data, so a click copies it instead of opening it.`;
  b.addEventListener("click", () => {
    copyLink?.(payloadUrls.get(b) ?? url);
  });
  return b;
}

function markGuarded(node: Element, url: string): void {
  node.setAttribute(PAYLOAD_ATTR, "");
  payloadUrls.set(node, url);
}

/** Children move, so a find mark or a linkified path survives; a moved chunk span is marked settled or it replays its fade. */
function reseat(from: Element, to: Element, url: string): void {
  for (const span of from.querySelectorAll(`[${CHUNK_ENTER_ATTR}]`)) {
    span.setAttribute(CHUNK_SETTLED_ATTR, "");
  }
  markGuarded(to, url);
  payloadUrls.delete(from);
  to.append(...from.childNodes);
  from.replaceWith(to);
}

/**
 * Give a link the markdown renderer withheld the form the switch asks for and answer the element now in its place:
 * the anchor with `href` when off, a copy button when on. Reads the switch untracked, so a renderer in an effect does
 * not subscribe.
 */
export function formPayloadLink(anchor: HTMLAnchorElement, url: string): Element {
  markGuarded(anchor, url);
  if (!guardOn.peek()) {
    openLink(anchor, url);
    return anchor;
  }
  // The button names the destination in its tooltip, so the renderer's inline copy of it goes.
  anchor.querySelector(':scope > [data-part="dest"]')?.remove();
  anchor.removeAttribute("data-cp-withheld");
  const button = withheldButton(url);
  reseat(anchor, button, url);
  return button;
}

/** The address of a diagram link the markdown renderer would withhold (`payloadShaped`, leaving the page's origin),
 *  else null. */
function withheldHref(href: string): string | null {
  let url: URL;
  try {
    url = new URL(href, document.baseURI);
  } catch {
    return null;
  }
  const web = url.protocol === "http:" || url.protocol === "https:";
  return web && url.origin !== location.origin && payloadShaped(url) ? url.href : null;
}

/** A diagram's SVG `<a>`, which cannot hold the copy button: a payload-shaped link loses its `href` while the switch
 *  is on and keeps its text. Any other link is left alone. */
export function formDiagramLink(a: SVGAElement, href: string): void {
  const url = withheldHref(href);
  if (url === null) {
    return;
  }
  markGuarded(a, url);
  if (guardOn.peek()) {
    a.removeAttribute("href");
  }
}

export function reformPayloadLinks(root: ParentNode): void {
  const on = guardOn.peek();
  for (const node of root.querySelectorAll(`[${PAYLOAD_ATTR}]`)) {
    const url = payloadUrls.get(node);
    if (url === undefined) {
      continue;
    }
    if (node instanceof SVGAElement) {
      if (on) {
        node.removeAttribute("href");
      } else {
        node.setAttribute("href", url);
      }
    } else if (on && node.tagName === "A") {
      reseat(node, withheldButton(url), url);
    } else if (!on && node.tagName === "BUTTON") {
      const a = el("a") as HTMLAnchorElement;
      openLink(a, url);
      reseat(node, a, url);
    }
  }
}

/** Re-form the page's guarded links, parked surfaces included, whenever the switch changes; the first run
 *  changes nothing. */
export function installLinkGuard(): () => void {
  let first = true;
  return effect(() => {
    touch(guardOn);
    if (first) {
      first = false;
      return;
    }
    for (const root of pageRoots()) {
      reformPayloadLinks(root);
    }
  });
}
