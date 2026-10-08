import { CHROME_ATTR } from "./chrome-attr.js";
import { iconEl } from "./icon-el.js";
import { ICON_TAB_WEB } from "./icons.js";
import { isPreviewablePage } from "./preview-page.js";
import { decodeDestination } from "./utils-url.js";
import { relToWorkspace } from "./workspace.js";

// Injected: the markdown renderer reaching the tab system would close an import cycle.
let opener: ((path: string) => void) | null = null;

export function setPreviewOpener(fn: (path: string) => void): void {
  opener = fn;
}

/** The page a link target names when the card replaces it, decoded once, else null. A query or
 *  fragment means the author linked something other than the page itself, so it stays a link. */
export function previewHrefPage(href: string): string | null {
  if (/[?#]/.test(href)) {
    return null;
  }
  // Not `servedPath`: its roots are fixed, and the page predicate follows the live workspace root.
  const path = decodeDestination(href);
  return isPreviewablePage(path) ? path : null;
}

/** `label` is moved in, not copied: the renderer hands over the link's own children. An empty
 *  label shows the file name. */
export function buildPreviewCard(path: string, label: readonly Node[]): HTMLButtonElement {
  const card = document.createElement("button");
  card.type = "button";
  card.className = "preview-card";
  const icon = chrome("preview-card-icon");
  icon.setAttribute("aria-hidden", "true");
  icon.append(iconEl(ICON_TAB_WEB));
  const name = document.createElement("span");
  name.className = "preview-card-label";
  name.append(...label);
  if (name.textContent === "") {
    name.textContent = path.slice(path.lastIndexOf("/") + 1);
  }
  const folder = chrome("preview-card-folder");
  folder.textContent = relToWorkspace(path.slice(0, path.lastIndexOf("/")));
  const open = chrome("preview-card-open");
  open.textContent = "Open";
  card.append(icon, name, folder, open);
  card.addEventListener("click", () => {
    opener?.(path);
  });
  return card;
}

function chrome(className: string): HTMLSpanElement {
  const span = document.createElement("span");
  span.className = className;
  span.setAttribute(CHROME_ATTR, "");
  return span;
}
