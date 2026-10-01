// ---------------------------------------------------------------------------
// Inline file-path linkification: `src/foo.ts:42` in rendered PROSE becomes a
// button that opens the editor at that line. SKIP_TAGS is matched against a text
// node's immediate parent, not its ancestors, so text wrapped in an element
// inside a <pre> escapes it — a shape neither prose caller produces. <a>/<button>
// skip because a control inside a control is broken either way.
// ---------------------------------------------------------------------------

import { el } from "@cplieger/reactive";
import { fileIcon } from "./icons.js";
import { iconEl } from "./icon-el.js";
import { FILE_EXTS } from "./file-extensions.js";

const SUFFIX = "(?:" + FILE_EXTS.join("|") + ")";

const RELATIVE_PATH = "[\\w.-]+\\/[\\w./-]*\\." + SUFFIX;

/** Roots an absolute path must start under. A shape test alone linkifies every
 *  `/etc/*.conf` and `/var/log/*.log` a shell command prints; these three are
 *  what `/api/file` can open. An extra `MAROTTE_BROWSE_ROOTS` mount is missed,
 *  which is the safe direction — no link offered rather than a dead one. */
const LINKABLE_ROOTS = ["workspace", "config", "uploads"] as const;

const ABSOLUTE_PATH = "\\/(?:" + LINKABLE_ROOTS.join("|") + ")\\/[\\w.-][\\w./-]*\\." + SUFFIX;

const PATH_PATTERN =
  "(?<![\\w/.-])(" + ABSOLUTE_PATH + "|" + RELATIVE_PATH + ")(?::(\\d+)(?::\\d+)?)?(?![\\w/.-])";

// Non-global version for acceptNode test (no lastIndex mutation).
const PATH_TEST_RX = new RegExp(PATH_PATTERN);
// Global version for replacePaths exec loop.
const PATH_EXEC_RX = new RegExp(PATH_PATTERN, "g");

const SKIP_TAGS = new Set(["CODE", "PRE", "A", "BUTTON"]);

/** Open handler, injected — the same pattern `initAttachmentPillCallbacks` uses for
 *  this exact function, and for the same reason: `openAtLine` reaches
 *  `editor-openers` and `tabs` behind it, and the markdown renderer that calls this
 *  module is itself reached from `editor-markdown`, so importing it directly closed
 *  a ring through the editor. Default is a no-op so a link built in a test renders
 *  without wiring. */
let _open: (path: string, line?: number) => void = () => {
  /* not wired */
};

export function initLinkifyCallbacks(cbs: { open: (path: string, line?: number) => void }): void {
  _open = cbs.open;
}

export function linkifyPaths(root: HTMLElement): void {
  const walker = document.createTreeWalker(root, NodeFilter.SHOW_TEXT, {
    acceptNode(n) {
      const parent = n.parentElement;
      if (parent === null) {
        return NodeFilter.FILTER_REJECT;
      }
      if (SKIP_TAGS.has(parent.tagName)) {
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
