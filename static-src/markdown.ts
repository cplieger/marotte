// marotte's hooks over @cplieger/ui/markdown: file and page links, payload links, served images and media, MathML,
// the code block's Run, svg and mermaid, path linkify, and while streaming the per-chunk fade, the caret and the
// per-block fade. Every render passes a signal, which `releaseMarkdownIn` aborts before its host is discarded.

import { createMarkdownStream as uiStream } from "@cplieger/ui/markdown";
import type {
  CodeNode,
  Hooks,
  ImageNode,
  LinkNode,
  MarkdownStream,
  MathNode,
  StreamOptions,
} from "@cplieger/ui/markdown";
import { el } from "@cplieger/reactive";
import { join as joinKey } from "@cplieger/keyenc";
import { CHROME_ATTR } from "./chrome-attr.js";
import { codeLang, finishCodeBlock, CODE_STATE_ATTR } from "./code-blocks.js";
import { formPayloadLink } from "./link-guard.js";
import { bindFileLink, linkifyPaths } from "./linkify.js";
import { latexToMathML } from "./mathml.js";
import { mediaElementFor } from "./media-block.js";
import { holdStreamingMermaid, releaseMermaid, renderMermaidBlock } from "./mermaid-block.js";
import { buildPreviewCard, previewCardFolder, previewHrefPage } from "./preview-card.js";
import { fileOpenRoute } from "./preview-page.js";
import { buildPath } from "./route-path.js";
import { renderSvgBlock } from "./svg-block.js";
import {
  decodeDestination,
  rewriteServedImageSrc,
  servedFileRoute,
  servedPath,
} from "./utils-url.js";
import { followRoot } from "./workspace.js";

export type { MarkdownStream } from "@cplieger/ui/markdown";

/** Each streamed text emission's wrapper, which the per-chunk fade in 13-messages.css animates once on mount. */
const CHUNK_ENTER_ATTR = "data-vk-chunk-enter";
/** A chunk span whose fade has played. Re-inserting a node restarts its CSS animation (measured in Chromium), so
 *  every move of streamed text marks the spans it moves first. */
const CHUNK_SETTLED_ATTR = "data-vk-chunk-settled";
/** On the element the stream last wrote text into, where the CSS caret renders inline after the last word. */
const CARET_ATTR = "data-vk-caret";
const BLOCK_ENTER_ATTR = "data-vk-block-enter";

/** The objects a link or image becomes that stand as blocks inside their paragraph. */
const BLOCK_OBJECT = ".preview-card, .media-audio, .media-download";
const OBJECT_TAIL = "block-object-tail";

/** internal/filebrowse.WholeFileMax: a document view renders every byte the viewer reads. */
const WHOLE_FILE_BYTES = 2 << 20;

type HeadingLevel = 1 | 2 | 3 | 4 | 5 | 6;

export interface RenderOpts {
  /** The element `#` renders as: 2 under the page's title bar, 3 under a page's own `h2`. Default 2. */
  readonly headingLevel?: HeadingLevel;
  /** A whole file's view reads up to the viewer's cap rather than the library's 1 MiB. */
  readonly wholeFile?: boolean;
}

export interface StreamOpts extends RenderOpts {
  /** Milliseconds a write waits before it is parsed (the library's default when absent); `0` parses on write, which
   *  a caller pacing its own writes (`reveal.ts`) wants, or the buffer re-lumps what it spread. */
  readonly flushIntervalMs?: number;
}

/** A live render's caret holder, which every move of streamed text carries along. */
interface Live {
  caret: Element | null;
  /** Top-level blocks on screen at the end of a slice: one completing later is not tagged to fade in again. */
  readonly onScreen: WeakSet<Element>;
}

function settleChunks(root: Element): void {
  for (const span of root.querySelectorAll(`[${CHUNK_ENTER_ATTR}]`)) {
    span.setAttribute(CHUNK_SETTLED_ATTR, "");
  }
}

function moveCaret(live: Live, to: Element | null): void {
  if (live.caret === to) {
    return;
  }
  live.caret?.removeAttribute(CARET_ATTR);
  to?.setAttribute(CARET_ATTR, "");
  live.caret = to;
}

/** `to` replaced `from`: a caret on `from` goes to `to`'s label, or `to`. */
function followCaret(live: Live | null, from: Element, to: Node, label?: Element | null): void {
  if (live?.caret === from && to instanceof Element) {
    moveCaret(live, label ?? to);
  }
}

/**
 * The sentence's punctuation right after a block object would open the next line alone, so it goes into a tail span
 * the stylesheet hides only while it follows the object: a card rebuilt as a link shows it again. `node` is its
 * parent's last child; what is left of it stays in place.
 */
function holdObjectTail(node: Text): void {
  const last = node.previousSibling;
  if (!(last instanceof Element)) {
    return;
  }
  const after = last.matches(BLOCK_OBJECT);
  if (
    !after &&
    !(
      last.classList.contains(OBJECT_TAIL) &&
      last.previousElementSibling?.matches(BLOCK_OBJECT) === true
    )
  ) {
    return;
  }
  const lead = /^[\s.,;:!?]+/u.exec(node.data)?.[0] ?? "";
  if (lead === "") {
    return;
  }
  let tail = last;
  if (after) {
    tail = el("span", { className: OBJECT_TAIL });
    node.before(tail);
  }
  tail.append(lead);
  node.data = node.data.slice(lead.length);
}

/** Streamed text: the object tail always; while live, the fade span outside code and math, and the caret. */
function onText(live: Live | null, parent: Element, node: Text): Node | undefined {
  holdObjectTail(node);
  if (node.data === "") {
    node.remove();
    return undefined;
  }
  if (live === null) {
    return undefined;
  }
  moveCaret(live, parent);
  // A span inside `code` would split the text a fence's highlighter and a math converter read.
  if (parent.closest("code") !== null) {
    return undefined;
  }
  const span = el("span", { [CHUNK_ENTER_ATTR]: "" });
  span.append(node);
  return span;
}

/** A link or image whose role reads the root, re-decided at every root change. */
interface RootDest {
  /** What stands for the destination now: the anchor or its card, the image, its media swap or its miss. */
  current: WeakRef<Element>;
  /** The library's element as its hook received it. */
  readonly pristine: Element;
  readonly node: LinkNode | ImageNode;
  /** What the role read from the root; the element is rebuilt only when a new root changes it. */
  readonly key: string;
}

/** Keyed by the element standing for the destination, so a swap keeps the record, and the record lives exactly as
 *  long as one of its elements does. */
const rootDestByEl = new WeakMap<Element, RootDest>();

function isImage(n: LinkNode | ImageNode): n is ImageNode {
  return "alt" in n;
}

/** Everything a destination's role reads from the root: a link's card page and folder label, else the tab its route
 *  names; an image's served path. */
function rootKey(n: LinkNode | ImageNode): string {
  if (isImage(n)) {
    return servedPath(n.raw) ?? "";
  }
  const page = previewHrefPage(n.raw);
  if (page !== null) {
    return joinKey(page, previewCardFolder(page));
  }
  const file = servedFileRoute(n.raw);
  return joinKey("", file === null ? "" : buildPath(fileOpenRoute(file.path, file.line)));
}

/** Only an absolute path can sit beneath a root. */
function underARoot(n: LinkNode | ImageNode): boolean {
  return /^\/(?![/\\])/.test(decodeDestination(n.raw));
}

function trackRootDest(n: LinkNode | ImageNode, pristine: Element | null, placed: Node): void {
  if (pristine === null || !(placed instanceof Element)) {
    return;
  }
  const rec: RootDest = { current: new WeakRef(placed), pristine, node: n, key: rootKey(n) };
  rootDestByEl.set(placed, rec);
  followRoot(rec, redecide);
}

function followRootDest(from: Element, to: Element): void {
  const rec = rootDestByEl.get(from);
  if (rec !== undefined) {
    rec.current = new WeakRef(to);
    rootDestByEl.set(to, rec);
  }
}

/** Rebuild a destination whose role the new root changed, from the library's element, through the hook a render
 *  runs; the rebuild tracks the fresh element under a record of its own. Answers whether to keep following `rec`. */
function redecide(rec: RootDest): boolean {
  const current = rec.current.deref();
  if (current?.parentNode == null) {
    return false;
  }
  if (rootKey(rec.node) === rec.key) {
    return true;
  }
  const fresh = rec.pristine.cloneNode(true) as Element;
  if (!isImage(rec.node)) {
    // The label's own nodes move back, so a find mark or a linkified path survives.
    const label = current.classList.contains("preview-card")
      ? current.querySelector(".preview-card-label")
      : current;
    if (label !== null) {
      settleChunks(label);
      fresh.replaceChildren(...label.childNodes);
    }
  }
  current.replaceWith(fresh);
  const result = isImage(rec.node)
    ? decideImage(rec.node, fresh)
    : decideLink(null, rec.node, fresh as HTMLAnchorElement);
  const placed = result ?? fresh;
  if (result !== undefined) {
    fresh.replaceWith(result);
  } else if (
    fresh instanceof HTMLImageElement &&
    !fresh.hasAttribute("src") &&
    rec.node.url !== null
  ) {
    // In a render the library sets a kept image's `src` after its hook.
    fresh.src = rec.node.url.href;
  }
  trackRootDest(rec.node, rec.pristine, placed);
  return false;
}

/** A payload link takes the form the guard switch asks for; a page link becomes its preview card; a served file
 *  opens its editor tab. */
function decideLink(live: Live | null, n: LinkNode, a: HTMLAnchorElement): Node | undefined {
  if (n.withheld && n.url !== null) {
    const formed = formPayloadLink(a, n.url.href);
    followCaret(live, a, formed);
    return formed === a ? undefined : formed;
  }
  if (n.url === null) {
    return undefined;
  }
  const page = previewHrefPage(n.raw);
  if (page !== null) {
    settleChunks(a);
    const card = buildPreviewCard(page, [...a.childNodes]);
    if (n.title !== "") {
      card.setAttribute("data-tooltip", n.title);
    }
    followCaret(live, a, card, card.querySelector(".preview-card-label"));
    return card;
  }
  const file = servedFileRoute(n.raw);
  if (file !== null) {
    // The browser would ask the SPA for `/workspace/...` and land on a chat. The href names the tab the bound click
    // opens, so a modified click or a copied link reaches the same one.
    a.setAttribute("href", buildPath(fileOpenRoute(file.path, file.line)));
    a.removeAttribute("target");
    a.removeAttribute("rel");
    bindFileLink(a, file.path, file.line);
  }
  return undefined;
}

/** Name the file when it fails to load: "the picture did not appear" is the agent's screenshot loop's characteristic
 *  failure, and the browser's broken-image icon identifies nothing. */
function nameMissingImage(img: HTMLImageElement): void {
  img.addEventListener(
    "error",
    () => {
      const src = img.getAttribute("src") ?? "";
      const alt = img.getAttribute("alt") ?? "";
      const shown = alt !== "" ? alt : decodeURIComponent(src.replace(/^.*[?&]path=/, ""));
      const miss = el("span", { className: "img-missing", [CHROME_ATTR]: "" });
      miss.textContent = `Image not available: ${shown}`;
      img.replaceWith(miss);
      followRootDest(img, miss);
    },
    { once: true },
  );
}

/** A served file that is no image becomes its player or download link; a served image takes the byte route. */
function decideImage(n: ImageNode, elem: Element): Node | undefined {
  if (!(elem instanceof HTMLImageElement)) {
    if (
      elem instanceof HTMLAnchorElement &&
      elem.hasAttribute("data-cp-withheld") &&
      n.url !== null
    ) {
      const formed = formPayloadLink(elem, n.url.href);
      return formed === elem ? undefined : formed;
    }
    return undefined;
  }
  const media = mediaElementFor(n.raw, n.alt);
  if (media !== null) {
    return media;
  }
  const served = rewriteServedImageSrc(n.raw);
  if (served === n.raw) {
    nameMissingImage(elem);
    return undefined;
  }
  // A copy of the library's `img`, so it is never given the gated `src` the SPA would answer.
  const own = elem.cloneNode(false) as HTMLImageElement;
  own.src = served;
  nameMissingImage(own);
  return own;
}

function decideMath(n: MathNode): Node | undefined {
  const math = latexToMathML(n.tex, n.display);
  // Left as `code[data-math]` with its TeX: the degraded state 13-messages.css draws.
  return math === null
    ? undefined
    : el("span", { "data-math": n.display ? "block" : "inline" }, math);
}

/** A closed top-level ```svg fence becomes its inert diagram; then Run on a shell fence; then a top-level ```mermaid
 *  fence draws, or gives its source back when it never closed. */
function decideCode(host: Element, n: CodeNode, block: HTMLElement): Node | undefined {
  const lang = n.lang.toLowerCase();
  const top = block.parentElement === host;
  if (top && n.closed && lang === "svg") {
    const figure = renderSvgBlock(n.text);
    if (figure !== null) {
      return figure;
    }
  }
  finishCodeBlock(block, lang, n.text);
  if (top && lang === "mermaid") {
    if (n.closed) {
      renderMermaidBlock(block, n.text);
    } else {
      releaseMermaid(block);
    }
  }
  return undefined;
}

function hooksFor(host: Element, live: Live | null): Hooks {
  return {
    link: (n, a) => {
      const pristine = underARoot(n) ? (a.cloneNode(true) as Element) : null;
      const result = decideLink(live, n, a);
      trackRootDest(n, pristine, result ?? a);
      return result;
    },
    image: (n, elem) => {
      const pristine = underARoot(n) ? (elem.cloneNode(true) as Element) : null;
      const result = decideImage(n, elem);
      trackRootDest(n, pristine, result ?? elem);
      return result;
    },
    math: (n) => decideMath(n),
    code: (n, block) => decideCode(host, n, block),
    block: (block) => {
      linkifyPaths(block);
      // A block that streamed in is on screen already; fading it in again at its close would blink it out.
      if (live !== null && !live.onScreen.has(block)) {
        block.setAttribute(BLOCK_ENTER_ATTR, "");
      }
      return undefined;
    },
  };
}

/** Each render that may still hold overflow observers, by the controller that lets them go, to its host. */
const renders = new Map<AbortController, Element>();

/** A scroller the library watches for overflow: a code block's `pre` or a table region. */
const SCROLLERS = ".cp-code > pre, .cp-table-scroll";

/** A finished render with no scroller holds nothing worth a release. */
function settle(controller: AbortController, host: Element): void {
  if (host.querySelector(SCROLLERS) === null && renders.delete(controller)) {
    controller.abort();
  }
}

/** Let go of every render inside `root`, `root` included: call it where `root` is about to be discarded. */
export function releaseMarkdownIn(root: Element): void {
  for (const [controller, host] of renders) {
    if (root === host || root.contains(host)) {
      controller.abort();
      renders.delete(controller);
    }
  }
}

function streamOptions(
  host: Element,
  opts: StreamOpts,
  live: Live | null,
  controller: AbortController,
): StreamOptions {
  return {
    base: document.baseURI,
    // The CSP is `img-src 'self' data:`, so another origin's image renders as a link to it.
    urls: (url, _raw, kind) => kind !== "src" || url.origin === location.origin,
    images: "load",
    headingLevel: opts.headingLevel ?? 2,
    ...(opts.wholeFile === true ? { maxBytes: WHOLE_FILE_BYTES } : {}),
    ...(opts.flushIntervalMs === undefined ? {} : { flushIntervalMs: opts.flushIntervalMs }),
    signal: controller.signal,
    hooks: hooksFor(host, live),
    text: (parent, node) => onText(live, parent, node),
    moved: (moved) => {
      settleChunks(moved);
      if (live?.caret === moved) {
        moveCaret(live, moved.parentElement);
      }
    },
    ...(live === null
      ? {}
      : {
          onSlice: (h: Element) => {
            for (const child of h.children) {
              live.onScreen.add(child);
            }
            const tail = h.lastElementChild;
            if (
              tail instanceof HTMLElement &&
              tail.matches(`.cp-code:not([${CODE_STATE_ATTR}])`) &&
              codeLang(tail) === "mermaid"
            ) {
              holdStreamingMermaid(tail);
            }
          },
        }),
  };
}

/** The live renderer: chunks fade in, the caret follows the last word, and a block that completes off screen fades
 *  in. Large flushes yield across tasks. */
export function createMarkdownStream(host: HTMLElement, opts: StreamOpts = {}): MarkdownStream {
  const live: Live = { caret: null, onScreen: new WeakSet() };
  const controller = new AbortController();
  renders.set(controller, host);
  const stream = uiStream(host, streamOptions(host, opts, live, controller));
  return {
    write: (chunk) => {
      stream.write(chunk);
    },
    flush: () => {
      stream.flush();
    },
    end: () => {
      stream.end();
      moveCaret(live, null);
      settle(controller, host);
    },
  };
}

/** Render `md` into `host`, after what it holds, at once and with no animation. */
export function renderMarkdownInto(host: HTMLElement, md: string, opts: RenderOpts = {}): void {
  // A stream ended at once parses synchronously, and only a stream runs the object tail's text hook.
  const controller = new AbortController();
  renders.set(controller, host);
  const stream = uiStream(host, streamOptions(host, opts, null, controller));
  stream.write(md);
  stream.end();
  settle(controller, host);
}
