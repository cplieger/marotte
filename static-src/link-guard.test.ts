import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import {
  adoptLinkGuard,
  formPayloadLink,
  installLinkGuard,
  linkAnchor,
  reformPayloadLinks,
  setLinkCopyCallback,
} from "./link-guard.js";
import { settingsPayload } from "./__test-helpers__/settings.js";

const URL_ = `https://github.com/o/r/issues/new?body=${"word ".repeat(50)}`;

let host: HTMLElement;
let stop: (() => void) | undefined;

beforeEach(() => {
  adoptLinkGuard(settingsPayload({ guard_payload_links: true }));
  host = document.createElement("div");
  document.body.appendChild(host);
});

afterEach(() => {
  stop?.();
  stop = undefined;
  setLinkCopyCallback(() => {
    /* reset */
  });
  host.remove();
});

function anchorWithText(): { a: HTMLAnchorElement; textNode: Text; span: HTMLElement } {
  const a = linkAnchor();
  const textNode = document.createTextNode("file ");
  const span = document.createElement("span");
  span.setAttribute("data-vk-chunk-enter", "");
  span.textContent = "an issue";
  a.append(textNode, span);
  host.append(a);
  return { a, textNode, span };
}

// toBe per node: toEqual passes for a rebuilt element.
function expectSameNodes(after: NodeList, before: readonly Node[]): void {
  expect(after).toHaveLength(before.length);
  before.forEach((node, i) => {
    expect(after[i]).toBe(node);
  });
}

describe("formPayloadLink", () => {
  it("swaps the anchor for a copy button carrying its own text nodes when ON", () => {
    const { a, textNode, span } = anchorWithText();
    const formed = formPayloadLink(a, URL_);
    expect(formed.tagName).toBe("BUTTON");
    expect(a.isConnected).toBe(false);
    expect(formed.parentElement).toBe(host);
    expectSameNodes(formed.childNodes, [textNode, span]);
    expect(formed.getAttribute("data-payload-url")).toBe(URL_);
    expect(formed.getAttribute("aria-label")).toBe("Copy link to github.com");
    expect(host.querySelector("a[href]")).toBeNull();
  });

  it("keeps the anchor and sets its href when OFF", () => {
    adoptLinkGuard(settingsPayload({ guard_payload_links: false }));
    const { a } = anchorWithText();
    const formed = formPayloadLink(a, URL_);
    expect(formed).toBe(a);
    expect(a.getAttribute("href")).toBe(URL_);
    expect(a.getAttribute("data-payload-url")).toBe(URL_);
    expect(host.querySelector("button")).toBeNull();
  });

  it("copies the URL on a click rather than navigating", () => {
    const copied = vi.fn();
    setLinkCopyCallback(copied);
    const { a } = anchorWithText();
    (formPayloadLink(a, URL_) as HTMLButtonElement).click();
    expect(copied).toHaveBeenCalledExactlyOnceWith(URL_);
  });
});

describe("reformPayloadLinks", () => {
  it("turns a guarded button back into an ordinary link, moving its text nodes", () => {
    const { a, textNode, span } = anchorWithText();
    formPayloadLink(a, URL_);
    adoptLinkGuard(settingsPayload({ guard_payload_links: false }));
    reformPayloadLinks(host);
    const link = host.querySelector("a") as HTMLAnchorElement;
    expect(host.querySelector("button")).toBeNull();
    expect(link.getAttribute("href")).toBe(URL_);
    expect(link.target).toBe("_blank");
    expect(link.rel).toBe("noopener");
    expectSameNodes(link.childNodes, [textNode, span]);
    expect(span.hasAttribute("data-vk-chunk-settled")).toBe(true);
  });

  it("turns an unguarded payload link back into a button when the switch comes on", () => {
    adoptLinkGuard(settingsPayload({ guard_payload_links: false }));
    const { a } = anchorWithText();
    formPayloadLink(a, URL_);
    adoptLinkGuard(settingsPayload({ guard_payload_links: true }));
    reformPayloadLinks(host);
    expect(host.querySelector("a")).toBeNull();
    expect(host.querySelector("button.link-withheld")?.getAttribute("data-payload-url")).toBe(URL_);
  });

  it("never touches an ordinary link", () => {
    const plain = linkAnchor();
    plain.setAttribute("href", "https://e.example/short");
    host.append(plain);
    adoptLinkGuard(settingsPayload({ guard_payload_links: true }));
    reformPayloadLinks(host);
    adoptLinkGuard(settingsPayload({ guard_payload_links: false }));
    reformPayloadLinks(host);
    expect(host.firstElementChild).toBe(plain);
  });
});

describe("installLinkGuard", () => {
  it("re-forms a link rendered before the server's answer once that answer lands", () => {
    const { a } = anchorWithText();
    formPayloadLink(a, URL_);
    stop = installLinkGuard();
    expect(host.querySelector("button")).not.toBeNull();
    adoptLinkGuard(settingsPayload({ guard_payload_links: false }));
    expect(host.querySelector("a")?.getAttribute("href")).toBe(URL_);
  });

  it("writes nothing on its first run", () => {
    adoptLinkGuard(settingsPayload({ guard_payload_links: false }));
    const { a } = anchorWithText();
    a.setAttribute("data-payload-url", URL_);
    // OFF never leaves an href-less anchor, so a pass on install would rebuild it.
    adoptLinkGuard(settingsPayload({ guard_payload_links: true }));
    const before = host.firstElementChild;
    stop = installLinkGuard();
    expect(host.firstElementChild).toBe(before);
  });
});
