// DOM renderer for smd-parser.

import {
  DOCUMENT,
  BLOCKQUOTE,
  PARAGRAPH,
  LINE_BREAK,
  RULE,
  HEADING_1,
  HEADING_2,
  HEADING_3,
  HEADING_4,
  HEADING_5,
  HEADING_6,
  ITALIC_AST,
  ITALIC_UND,
  STRONG_AST,
  STRONG_UND,
  STRIKE,
  CODE_INLINE,
  CODE_BLOCK,
  CODE_FENCE,
  RAW_URL,
  LINK,
  IMAGE,
  LIST_UNORDERED,
  LIST_ORDERED,
  LIST_ITEM,
  CHECKBOX,
  TABLE,
  TABLE_ROW,
  TABLE_CELL,
  EQUATION_BLOCK,
  EQUATION_INLINE,
  UNCLOSED,
  ALIGN,
  attr_to_html_attr,
} from "./smd-parser-types.js";
import type { Token, Attr, Renderer } from "./smd-parser-types.js";
import { CHROME_ATTR } from "./chrome-attr.js";
import { exfilShaped, isSafeUrl, rewriteServedImageSrc, servedFileRoute } from "./utils-url.js";
import { formPayloadLink, linkAnchor } from "./link-guard.js";
import { buildPath } from "./route-path.js";
import { bindFileLink } from "./linkify.js";
import { mediaElementFor } from "./media-block.js";
import { buildPreviewCard, previewHrefPage } from "./preview-card.js";
import { fileOpenRoute } from "./preview-page.js";
import { latexToMathML } from "./mathml.js";
import { el } from "@cplieger/reactive";

export type { Renderer } from "./smd-parser-types.js";

export interface DomRendererData {
  nodes: (Element | null)[];
  index: number;
  onBlockComplete: ((block: HTMLElement) => void) | undefined;
  /** Wrap each text emission in a `<span data-vk-chunk-enter>` so the per-chunk fade-in CSS
   *  animation fires as text streams in. Skipped inside `<code>` / `<pre>` because spans there
   *  break syntax highlighters that expect raw text children. */
  animateText: boolean;
  /** The current `data-vk-caret` holder — the element the stream last wrote text into, where the
   *  CSS caret renders. See moveCaret. */
  caretEl?: Element | null;
  /** Set by `set_attr(UNCLOSED, …)` and consumed by the `end_token` that must immediately follow
   *  it: the literal to restore in place of an inline element whose token never closed. */
  unclosedDelim?: string | null;
  /** The open table's per-column `text-align`, and how many cells of the current row have been
   *  created. A table cannot contain a table, so one of each is enough; both are reset when a
   *  TABLE or a TABLE_ROW opens. */
  align?: readonly string[] | null;
  cellIndex?: number;
}

function makeEl(tag: string): HTMLElement {
  return el(tag);
}

const TOKEN_TAG_MAP: Readonly<Record<number, string>> = {
  [BLOCKQUOTE]: "blockquote",
  [PARAGRAPH]: "p",
  [LINE_BREAK]: "br",
  [RULE]: "hr",
  [HEADING_1]: "h1",
  [HEADING_2]: "h2",
  [HEADING_3]: "h3",
  [HEADING_4]: "h4",
  [HEADING_5]: "h5",
  [HEADING_6]: "h6",
  [ITALIC_AST]: "em",
  [ITALIC_UND]: "em",
  [STRONG_AST]: "strong",
  [STRONG_UND]: "strong",
  [STRIKE]: "s",
  [CODE_INLINE]: "code",
  [IMAGE]: "img",
  [LIST_UNORDERED]: "ul",
  [LIST_ORDERED]: "ol",
  [LIST_ITEM]: "li",
  [TABLE]: "table",
};

/** Attribute marking an equation host, and the state it is in. */
const MATH_ATTR = "data-math";

/** The marker every streamed text emission's wrapper span carries, so the per-chunk fade in
 *  `13-messages.css` can animate each delta once on mount. Exported because a reader that
 *  MIRRORS this DOM as text has to know the span is a rendering artefact rather than a boundary
 *  in the content: one sentence arrives as dozens of these, and the delegate card's rolling tail
 *  (`fundamentals/subagent-block.ts`) put a space at every element boundary, so it printed gaps
 *  inside words for as long as a delegate streamed. */
const CHUNK_ENTER_ATTR = "data-vk-chunk-enter";
const MATH_RAW_ATTR = "data-math-raw";

/** Turn a closed equation host's LaTeX into MathML in place. */
function finalizeMath(host: Element): void {
  const src = host.textContent;
  const math = latexToMathML(src, host.getAttribute(MATH_ATTR) === "block");
  if (math === null) {
    return;
  }
  host.replaceChildren(math);
  host.removeAttribute(MATH_RAW_ATTR);
}

function add_token_dom(data: DomRendererData, type: Token): void {
  // eslint-disable-next-line @typescript-eslint/no-non-null-assertion
  const parent = data.nodes[data.index]!;

  if (type === DOCUMENT) {
    return;
  }

  switch (type) {
    case CHECKBOX: {
      const cb = makeEl("input") as HTMLInputElement;
      cb.type = "checkbox";
      cb.disabled = true;
      cb.setAttribute("aria-label", "Task item");
      data.nodes[++data.index] = parent.appendChild(cb);
      return;
    }
    case CODE_BLOCK:
    case CODE_FENCE: {
      const pre = parent.appendChild(makeEl("pre"));
      pre.className = "code";
      const slot = makeEl("code");
      data.nodes[++data.index] = pre.appendChild(slot);
      return;
    }
    case EQUATION_BLOCK:
    case EQUATION_INLINE: {
      // A plain host in the HTML namespace, holding the raw LaTeX until the expression closes. It
      // cannot BE the `<math>` element: makeEl is document.createElement, and a `math` element
      // outside the MathML namespace is an unknown inline element rather than mathematics.
      const host = makeEl("span");
      host.setAttribute(MATH_ATTR, type === EQUATION_BLOCK ? "block" : "inline");
      host.setAttribute(MATH_RAW_ATTR, "");
      data.nodes[++data.index] = parent.appendChild(host);
      return;
    }
    case LINK:
    case RAW_URL: {
      data.nodes[++data.index] = parent.appendChild(linkAnchor());
      return;
    }
    case TABLE: {
      data.align = null;
      data.nodes[++data.index] = parent.appendChild(makeEl("table"));
      return;
    }
    case TABLE_ROW: {
      // Asked of the DOM rather than counted: `children.length` meant the first row landed in a
      // `<thead>`, the second in a `<tbody>` and every later one in `children[1]`, so anything a
      // third party appended to the `<table>` shifted the index and sent rows to the wrong section.
      const table = parent as HTMLTableElement;
      const section =
        table.tHead === null
          ? table.appendChild(makeEl("thead"))
          : (table.tBodies[0] ?? table.appendChild(makeEl("tbody")));
      data.cellIndex = 0;
      data.nodes[++data.index] = section.appendChild(makeEl("tr"));
      return;
    }
    case TABLE_CELL: {
      const isHeader = parent.parentElement?.tagName === "THEAD";
      const slot = makeEl(isHeader ? "th" : "td");
      const column = data.cellIndex ?? 0;
      data.cellIndex = column + 1;
      const align = data.align?.[column];
      if (align !== undefined && align !== "") {
        slot.setAttribute("style", `text-align:${align}`);
      }
      data.nodes[++data.index] = parent.appendChild(slot);
      return;
    }
    default:
      break;
  }

  const tag = TOKEN_TAG_MAP[type];
  if (tag === undefined) {
    return;
  }
  data.nodes[++data.index] = parent.appendChild(makeEl(tag));
}

/** Marks a per-chunk span whose fade has already played, so the CSS rule that animates one on
 *  mount skips it. Set only by the unwrap below: MEASURED in Chromium, re-inserting a node
 *  restarts its CSS animation, so re-parenting a settled span would re-run the fade over text
 *  already on screen — the flash the mount-once design exists to avoid. */
const CHUNK_SETTLED_ATTR = "data-vk-chunk-settled";

/** Replace an inline element whose token never closed with its own delimiter followed by its
 *  children, which is what CommonMark renders for an unclosed delimiter run. The parser is
 *  append-only and cannot un-open a token; the renderer still holds the element at close time
 *  and can. */
function unwrap_unclosed(data: DomRendererData, el: Element, delim: string): void {
  const parent = el.parentElement;
  if (parent === null) {
    return;
  }
  // The caret may be sitting on the element about to leave the document.
  if (data.caretEl === el) {
    el.removeAttribute(CARET_ATTR);
    data.caretEl = parent;
    parent.setAttribute(CARET_ATTR, "");
  }
  for (const span of el.querySelectorAll(`[${CHUNK_ENTER_ATTR}]`)) {
    span.setAttribute(CHUNK_SETTLED_ATTR, "");
  }
  // IMG is void: markdown's `![alt]` puts the swallowed text in `alt`, so the replacement is one
  // text node rather than a delimiter plus children.
  const head = el.tagName === "IMG" ? delim + (el.getAttribute("alt") ?? "") : delim;
  el.replaceWith(document.createTextNode(head), ...el.childNodes);
}

function end_token_dom(data: DomRendererData): void {
  const closing = data.nodes[data.index];
  data.index -= 1;
  const unclosed = data.unclosedDelim;
  if (unclosed !== null && unclosed !== undefined) {
    data.unclosedDelim = null;
    if (closing !== null && closing !== undefined) {
      // Ordered before finalizeMath: an equation host being unwrapped must not be converted, and an
      // inline token never brings `index` to 0, so onBlockComplete is unreachable from here.
      unwrap_unclosed(data, closing, unclosed);
      return;
    }
  }
  if (closing?.hasAttribute(MATH_ATTR) === true) {
    finalizeMath(closing);
  }
  // If decrementing brought us back to the root (index 0), the node that just closed was a
  // top-level block. Fire the callback so callers can decorate / animate the freshly-completed
  // block.
  if (
    data.index === 0 &&
    closing !== null &&
    closing !== undefined &&
    data.onBlockComplete !== undefined
  ) {
    const tag = closing.tagName;
    const target =
      tag === "CODE" && closing.parentElement?.tagName === "PRE"
        ? closing.parentElement
        : (closing as HTMLElement);
    data.onBlockComplete(target);
  }
}

/** The streaming caret's home: the element text last landed in carries `data-vk-caret`, so the
 *  CSS caret (13-messages.css) renders inline after the last word rather than on its own line
 *  after the last block — a container `::after` is what put the cursor "on the next line". */
const CARET_ATTR = "data-vk-caret";

function moveCaret(data: DomRendererData, target: Element): void {
  if (!data.animateText || data.caretEl === target) {
    return;
  }
  data.caretEl?.removeAttribute(CARET_ATTR);
  target.setAttribute(CARET_ATTR, "");
  data.caretEl = target;
}

function add_text_dom(data: DomRendererData, text: string): void {
  const parent = data.nodes[data.index];
  if (parent === null || parent === undefined) {
    return;
  }

  const tag = parent.tagName;

  // IMG is void — text inside an image node is the alt text per markdown syntax `![alt](url)`.
  // Append to the alt attribute rather than creating (ignored) child text nodes.
  if (tag === "IMG") {
    const img = parent as HTMLImageElement;
    // eslint-disable-next-line @typescript-eslint/no-unnecessary-condition
    img.alt = (img.alt ?? "") + text;
    return;
  }

  moveCaret(data, parent);

  // For streaming render, wrap the text in an inline span so per-chunk fade-in CSS can animate each
  // delta as it arrives. Skipped inside <code>/<pre> (their syntax highlighter expects unwrapped
  // text children).
  if (data.animateText && tag !== "CODE" && tag !== "PRE" && !parent.hasAttribute(MATH_ATTR)) {
    const span = makeEl("span");
    span.setAttribute(CHUNK_ENTER_ATTR, "");
    span.appendChild(document.createTextNode(text));
    parent.appendChild(span);
    return;
  }
  parent.appendChild(document.createTextNode(text));
}

/** A bracket link's href arrives after its label, so the label's nodes exist and are MOVED into
 *  the card; the node stack and the caret follow them. */
function swapForPreviewCard(data: DomRendererData, link: Element, page: string): void {
  for (const span of link.querySelectorAll(`[${CHUNK_ENTER_ATTR}]`)) {
    span.setAttribute(CHUNK_SETTLED_ATTR, "");
  }
  const card = buildPreviewCard(page, [...link.childNodes]);
  link.replaceWith(card);
  data.nodes[data.index] = card;
  if (data.caretEl === link) {
    const label = card.querySelector(".preview-card-label") ?? card;
    label.setAttribute(CARET_ATTR, "");
    data.caretEl = label;
  }
}

function set_attr_dom(data: DomRendererData, attr: Attr, value: string): void {
  if (attr === UNCLOSED) {
    data.unclosedDelim = value;
    return;
  }
  if (attr === ALIGN) {
    data.align = value.split(",");
    return;
  }
  const node = data.nodes[data.index];
  if (node === null || node === undefined) {
    return;
  }
  const attrName = attr_to_html_attr(attr);
  if (attrName === "") {
    return;
  }
  if ((attrName === "href" || attrName === "src") && !isSafeUrl(value)) {
    node.setAttribute(attrName, "#");
    return;
  }
  const page = attrName === "href" && node.tagName === "A" ? previewHrefPage(value) : null;
  if (page !== null) {
    swapForPreviewCard(data, node, page);
    return;
  }
  if (attrName === "title" && node.classList.contains("preview-card")) {
    node.setAttribute("data-tooltip", value);
    return;
  }
  // AFTER the scheme gate, so a rejected scheme stays a dead `#` whatever the switch says. The href
  // lands when `)` closes, so the link text is already complete here.
  if (attrName === "href" && node.tagName === "A" && exfilShaped(value)) {
    const formed = formPayloadLink(node as HTMLAnchorElement, value);
    if (formed !== node) {
      data.nodes[data.index] = formed;
      if (data.caretEl === node) {
        node.removeAttribute(CARET_ATTR);
        formed.setAttribute(CARET_ATTR, "");
        data.caretEl = formed;
      }
    }
    return;
  }
  // An agent-written workspace path is a real file, not a URL the SPA can serve. Rewritten AFTER
  // the safety gate so the rewrite can never launder a scheme isSafeUrl just rejected.
  if (attrName === "src" && node.tagName === "IMG") {
    // The FILE-ROLE swap, and it has to happen here rather than at add_token: the tag is chosen
    // when `![` opens and the path only arrives when `)` closes, so this is the first moment the
    // role is knowable.
    const swapped = mediaElementFor(value, node.getAttribute("alt") ?? "");
    if (swapped !== null) {
      node.replaceWith(swapped);
      // Re-point the node stack, or end_token would close a node no longer in the document and any
      // later emission would land on the departed `<img>`.
      data.nodes[data.index] = swapped;
      return;
    }
    node.setAttribute(attrName, rewriteServedImageSrc(value));
    // Defer the fetch until the image is near the viewport, and set it HERE rather than at
    // add_token so it reaches exactly the nodes that are still images with a real src: the
    // unsafe-URL gate above returned early with `src="#"`, and `mediaElementFor` may already have
    // replaced this `<img>` with an `<audio>` or a download link.
    node.setAttribute("loading", "lazy");
    // Name the file when it fails to load. The browser's default broken-image icon identifies
    // nothing, and "the picture did not appear" is the characteristic failure of the agent's
    // screenshot loop — the one fact worth having then is which path was asked for.
    node.addEventListener(
      "error",
      () => {
        const src = node.getAttribute("src") ?? "";
        const alt = node.getAttribute("alt") ?? "";
        const shown = alt !== "" ? alt : decodeURIComponent(src.replace(/^.*[?&]path=/, ""));
        const miss = document.createElement("span");
        miss.className = "img-missing";
        miss.setAttribute(CHROME_ATTR, "");
        miss.textContent = `Image not available: ${shown}`;
        node.replaceWith(miss);
      },
      { once: true },
    );
    return;
  }
  if (attrName === "href" && node.tagName === "A") {
    const file = servedFileRoute(value);
    if (file !== null) {
      // The browser would ask the SPA for `/workspace/...` and land on a chat. The href names the
      // tab the bound click opens, so a modified click or a copied link reaches the same one.
      node.setAttribute("href", buildPath(fileOpenRoute(file.path, file.line)));
      node.removeAttribute("target");
      node.removeAttribute("rel");
      bindFileLink(node as HTMLAnchorElement, file.path, file.line);
      return;
    }
  }
  if (attrName === "class" && node.tagName === "CODE") {
    node.setAttribute("class", "language-" + value);
    return;
  }
  node.setAttribute(attrName, value);
}

/** Factory for a DOM renderer rooted at `root`. Caller can register `onBlockComplete` to receive
 *  a callback when each top-level block finishes (paragraph, pre/code, heading, list,
 *  blockquote, table). */
export function domRenderer(
  root: HTMLElement,
  options: { onBlockComplete?: (block: HTMLElement) => void; animateText?: boolean } = {},
): Renderer<DomRendererData> {
  return {
    add_token: add_token_dom,
    end_token: end_token_dom,
    add_text: add_text_dom,
    set_attr: set_attr_dom,
    data: {
      nodes: [root],
      index: 0,
      onBlockComplete: options.onBlockComplete,
      animateText: options.animateText ?? false,
    },
  };
}
