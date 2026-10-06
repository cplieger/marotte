import { effect, el, signal, touch } from "@cplieger/reactive";
import { isSafeUrl } from "./utils-url.js";
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

/** The anchor every rendered model link uses, the re-formed ones included. */
export function linkAnchor(): HTMLAnchorElement {
  const a = el("a") as HTMLAnchorElement;
  a.target = "_blank";
  a.rel = "noopener";
  return a;
}

function hostOf(url: string): string {
  try {
    return new URL(url, document.baseURI).host;
  } catch {
    return url.slice(0, 40);
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
 * Give a payload-shaped link the form the switch asks for and answer the element now in its place: the anchor with
 * `href` when off, a copy button when on. Reads the switch untracked, so a renderer in an effect does not subscribe.
 */
export function formPayloadLink(anchor: HTMLAnchorElement, url: string): Element {
  markGuarded(anchor, url);
  if (!guardOn.peek()) {
    anchor.setAttribute("href", url);
    return anchor;
  }
  const button = withheldButton(url);
  reseat(anchor, button, url);
  return button;
}

export function reformPayloadLinks(root: ParentNode): void {
  const on = guardOn.peek();
  for (const node of root.querySelectorAll(`[${PAYLOAD_ATTR}]`)) {
    const url = payloadUrls.get(node);
    if (url === undefined) {
      continue;
    }
    if (on && node.tagName === "A") {
      reseat(node, withheldButton(url), url);
    } else if (!on && node.tagName === "BUTTON" && isSafeUrl(url)) {
      const a = linkAnchor();
      a.setAttribute("href", url);
      reseat(node, a, url);
    }
  }
}

/** Re-form the document's guarded links whenever the switch changes; the first run
 *  changes nothing. */
export function installLinkGuard(): () => void {
  let first = true;
  return effect(() => {
    touch(guardOn);
    if (first) {
      first = false;
      return;
    }
    reformPayloadLinks(document);
  });
}
