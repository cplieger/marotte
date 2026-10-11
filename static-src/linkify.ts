// `src/foo.ts:42` in rendered prose becomes a button opening the editor at that line. Any SKIP_TAGS ancestor skips a
// text node (the streamer wraps a link label in a span); <a>/<button> skip since a control inside a control is broken.

import { el } from "@cplieger/reactive";
import { fileIcon } from "./icons.js";
import { iconEl } from "./icon-el.js";
import { FILE_EXTS } from "./file-extensions.js";
import { UPLOADS_DIR } from "./upload-policy.js";
import { followRoot, workspaceRootOrDefault } from "./workspace.js";

const SUFFIX = "(?:" + FILE_EXTS.join("|") + ")";

const RELATIVE_PATH = "[\\w.-]+\\/[\\w./-]*\\." + SUFFIX;

const FIXED_ROOTS = ["/config", UPLOADS_DIR] as const;

/**
 * Roots an absolute path must start under: the mounts `/api/file` can open (`browseRoots` in
 * internal/composition/config.go), so `/etc/*.conf` in shell output is not linked. A `/` work dir is never a mount.
 * An extra `MAROTTE_BROWSE_ROOTS` mount is missed, the safe direction.
 */
function linkableRoots(): readonly string[] {
  const root = workspaceRootOrDefault();
  return root === "/" ? FIXED_ROOTS : [root, ...FIXED_ROOTS];
}

interface PathPatterns {
  readonly root: string;
  /** Non-global, for acceptNode (no lastIndex mutation). */
  readonly test: RegExp;
  /** Global, for the replacePaths exec loop. */
  readonly exec: RegExp;
  /** Anchored, for whether one linked path still names a linkable root. */
  readonly absolute: RegExp;
}

let compiled: PathPatterns | null = null;

function patterns(): PathPatterns {
  const root = workspaceRootOrDefault();
  if (compiled?.root !== root) {
    const roots = linkableRoots().map((r) => r.replace(/[.*+?^${}()|[\]\\/]/g, "\\$&"));
    const absolute = "(?:" + roots.join("|") + ")\\/[\\w.-][\\w./-]*\\." + SUFFIX;
    const pattern =
      "(?<![\\w/.-])(" + absolute + "|" + RELATIVE_PATH + ")(?::(\\d+)(?::\\d+)?)?(?![\\w/.-])";
    compiled = {
      root,
      test: new RegExp(pattern),
      exec: new RegExp(pattern, "g"),
      absolute: new RegExp("^" + absolute + "$"),
    };
  }
  return compiled;
}

const SKIP_TAGS = "code, pre, a, button";

/**
 * Injected, like `initAttachmentPillCallbacks`: importing the opener would close a ring through the editor. A no-op
 * default so a test link renders unwired.
 */
let _open: (path: string, line?: number) => void = () => {
  /* not wired */
};

export function initLinkifyCallbacks(cbs: { open: (path: string, line?: number) => void }): void {
  _open = cbs.open;
}

/** Open `path` through the injected opener on a plain primary click; a modified or non-primary click follows the anchor's href. */
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
  const { test } = patterns();
  const walker = document.createTreeWalker(root, NodeFilter.SHOW_TEXT, {
    acceptNode(n) {
      if (n.parentElement?.closest(SKIP_TAGS) !== null) {
        return NodeFilter.FILTER_REJECT;
      }
      return test.test(n.nodeValue ?? "") ? NodeFilter.FILTER_ACCEPT : NodeFilter.FILTER_REJECT;
    },
  });
  const targets: Text[] = [];
  let cur: Node | null;
  while ((cur = walker.nextNode()) !== null) {
    targets.push(cur as Text);
  }
  let absolute = linkedByHost.get(root);
  if (absolute === undefined) {
    absolute = [];
    linkedByHost.set(root, absolute);
    followRoot(root, relinkify);
  }
  for (const textNode of targets) {
    replacePaths(textNode, absolute);
  }
}

interface LinkedPath {
  readonly link: WeakRef<HTMLButtonElement>;
  readonly path: string;
  readonly text: string;
}

/** The absolute-path buttons each linkified host holds, which a new root may leave naming no mount. */
const linkedByHost = new WeakMap<HTMLElement, LinkedPath[]>();

/** Under a new root, an absolute path the old one linked may name no mount, and one it left as text may. */
function relinkify(host: HTMLElement): boolean {
  const { absolute: linkable } = patterns();
  const kept = (linkedByHost.get(host) ?? []).filter(({ link, path, text }) => {
    const btn = link.deref();
    if (btn === undefined) {
      return false;
    }
    if (!linkable.test(path)) {
      btn.replaceWith(document.createTextNode(text));
      return false;
    }
    return true;
  });
  linkedByHost.set(host, kept);
  linkifyPaths(host);
  return true;
}

function replacePaths(textNode: Text, absolute: LinkedPath[]): void {
  const text = textNode.nodeValue ?? "";
  const { exec } = patterns();
  exec.lastIndex = 0;
  const frag = document.createDocumentFragment();
  let last = 0;
  let m: RegExpExecArray | null;
  while ((m = exec.exec(text)) !== null) {
    if (m.index > last) {
      frag.appendChild(document.createTextNode(text.slice(last, m.index)));
    }
    const path = m[1]!; // eslint-disable-line @typescript-eslint/no-non-null-assertion
    const link = makeLink(path, m[2]);
    if (path.startsWith("/")) {
      absolute.push({ link: new WeakRef(link), path, text: m[0] });
    }
    frag.appendChild(link);
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
