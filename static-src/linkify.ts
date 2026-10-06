// `src/foo.ts:42` in rendered prose becomes a button opening the editor at that line. Any SKIP_TAGS ancestor skips a
// text node (the streamer wraps a link label in a span); <a>/<button> skip since a control inside a control is broken.

import { el } from "@cplieger/reactive";
import { fileIcon } from "./icons.js";
import { iconEl } from "./icon-el.js";
import { FILE_EXTS } from "./file-extensions.js";

const SUFFIX = "(?:" + FILE_EXTS.join("|") + ")";

const RELATIVE_PATH = "[\\w.-]+\\/[\\w./-]*\\." + SUFFIX;

/**
 * Roots an absolute path must start under: these are what `/api/file` can open, so `/etc/*.conf` in shell output is
 * not linked. An extra `MAROTTE_BROWSE_ROOTS` mount is missed, the safe direction.
 */
const LINKABLE_ROOTS = ["workspace", "config", "uploads"] as const;

const ABSOLUTE_PATH = "\\/(?:" + LINKABLE_ROOTS.join("|") + ")\\/[\\w.-][\\w./-]*\\." + SUFFIX;

const PATH_PATTERN =
  "(?<![\\w/.-])(" + ABSOLUTE_PATH + "|" + RELATIVE_PATH + ")(?::(\\d+)(?::\\d+)?)?(?![\\w/.-])";

// Non-global, for acceptNode (no lastIndex mutation).
const PATH_TEST_RX = new RegExp(PATH_PATTERN);
// Global, for the replacePaths exec loop.
const PATH_EXEC_RX = new RegExp(PATH_PATTERN, "g");

const SKIP_TAGS = "code, pre, a, button";

/**
 * Injected, like `initAttachmentPillCallbacks`: importing `openAtLine` would close a ring through the editor. A no-op
 * default so a test link renders unwired.
 */
let _open: (path: string, line?: number) => void = () => {
  /* not wired */
};

export function initLinkifyCallbacks(cbs: { open: (path: string, line?: number) => void }): void {
  _open = cbs.open;
}

/** Open `path` in the editor on a plain primary click; a modified or non-primary click follows the anchor's href. */
export function bindFileLink(a: HTMLAnchorElement, path: string, line?: number): void {
  a.addEventListener("click", (e) => {
    if (e.metaKey || e.ctrlKey || e.shiftKey || e.altKey || e.button !== 0) {
      return;
    }
    e.preventDefault();
    _open(path, line);
  });
}

export function linkifyPaths(root: HTMLElement): void {
  const walker = document.createTreeWalker(root, NodeFilter.SHOW_TEXT, {
    acceptNode(n) {
      if (n.parentElement?.closest(SKIP_TAGS) !== null) {
        return NodeFilter.FILTER_REJECT;
      }
      return PATH_TEST_RX.test(n.nodeValue ?? "")
        ? NodeFilter.FILTER_ACCEPT
        : NodeFilter.FILTER_REJECT;
    },
  });
  const targets: Text[] = [];
  let cur: Node | null;
  while ((cur = walker.nextNode()) !== null) {
    targets.push(cur as Text);
  }
  for (const textNode of targets) {
    replacePaths(textNode);
  }
}

function replacePaths(textNode: Text): void {
  const text = textNode.nodeValue ?? "";
  PATH_EXEC_RX.lastIndex = 0;
  const frag = document.createDocumentFragment();
  let last = 0;
  let m: RegExpExecArray | null;
  while ((m = PATH_EXEC_RX.exec(text)) !== null) {
    if (m.index > last) {
      frag.appendChild(document.createTextNode(text.slice(last, m.index)));
    }
    frag.appendChild(makeLink(m[1]!, m[2])); // eslint-disable-line @typescript-eslint/no-non-null-assertion
    last = m.index + m[0].length;
  }
  if (last < text.length) {
    frag.appendChild(document.createTextNode(text.slice(last)));
  }
  textNode.replaceWith(frag);
}

function makeLink(path: string, lineStr: string | undefined): HTMLButtonElement {
  const line = lineStr !== undefined ? parseInt(lineStr, 10) : undefined;
  const basename = path.split("/").pop() ?? path;
  const label = line !== undefined ? `${basename}:${String(line)}` : basename;
  const btn = el("button", {
    className: "inline-file-link",
    title: line !== undefined ? `${path}:${String(line)}` : path,
  });
  const iconSpan = el("span", { className: "inline-file-icon" }, iconEl(fileIcon(basename, false)));
  const labelSpan = el("span", undefined, label);
  btn.append(iconSpan, labelSpan);
  btn.addEventListener("click", () => {
    _open(path, line);
  });
  return btn as HTMLButtonElement;
}
