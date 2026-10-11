// The DOM find engine: match discovery, <mark> highlighting and step state for one root, shared by the transcript's and
// the editor's find. A leaf with no app imports, scrolling or overlay.

import { el } from "@cplieger/reactive";
import { occurrences, prepare } from "./textsearch/scan.js";
import type { Needle } from "./textsearch/scan.js";

const HIT_CLASS = "find-hit";
const CURRENT_CLASS = "find-hit-current";

const TEXT_NODE = 3;
const ELEMENT_NODE = 1;

/** A run ends on entering and leaving these, so a phrase never crosses a block boundary. */
const BLOCK_TAGS = new Set([
  "P",
  "DIV",
  "LI",
  "PRE",
  "TD",
  "TH",
  "H1",
  "H2",
  "H3",
  "H4",
  "H5",
  "H6",
  "BLOCKQUOTE",
  "SUMMARY",
  "DT",
  "DD",
  "BR",
]);

/**
 * UI chrome the walker skips, except two producers whose text the server searches: the denial block (`tool_denial`)
 * and the MCP badge.
 */
const CHROME_SELECTOR = "[data-vk-chrome]:not(.tool-denial):not(.tool-mcp-badge)";

/**
 * Prunes script/style, wrapped hits, chrome, structurally hidden subtrees and elements `checkVisibility` calls
 * invisible (except `display: contents`, and content inside `keep` that is hidden only by a skipped box).
 */
function isSearchableElement(elem: Element, keep: Element | null): boolean {
  const tag = elem.tagName;
  if (tag === "SCRIPT" || tag === "STYLE" || tag === "MARK") {
    return false;
  }
  if (elem.hasAttribute("hidden") || elem.getAttribute("aria-hidden") === "true") {
    return false;
  }
  if (elem.classList.contains("hidden")) {
    return false;
  }
  // `.streaming` marks the live assistant bubble and live reasoning block.
  if (elem.classList.contains("streaming")) {
    return false;
  }
  if (elem.matches(CHROME_SELECTOR)) {
    return false;
  }
  if (tag === "DETAILS" && !(elem as HTMLDetailsElement).open) {
    return false;
  }
  const cv = (elem as { checkVisibility?: (opts?: unknown) => boolean }).checkVisibility;
  if (
    typeof cv === "function" &&
    !cv.call(elem, {
      contentVisibilityAuto: true,
      visibilityProperty: true,
      opacityProperty: false,
    })
  ) {
    if (
      keep?.contains(elem) === true &&
      cv.call(elem, {
        contentVisibilityAuto: false,
        visibilityProperty: true,
        opacityProperty: false,
      })
    ) {
      return true;
    }
    return rendersWithoutBox(elem);
  }
  return true;
}

/** `display: contents` is the one shape `checkVisibility` calls invisible that is not hidden, so the walker descends it. */
function rendersWithoutBox(elem: Element): boolean {
  return getComputedStyle(elem).display === "contents";
}

/** The outermost `content-visibility: auto` box holding `el` below `root`: the box whose skipping hides it. */
function skippableBoxOf(el: Element, root: Element): Element | null {
  let box: Element | null = null;
  for (let cur: Element | null = el; cur !== null && cur !== root; cur = cur.parentElement) {
    if (getComputedStyle(cur).contentVisibility === "auto") {
      box = cur;
    }
  }
  return box;
}

/**
 * Where a hit starts, which a re-walk keeps and an index does not: other boxes skipping or rendering renumber hits.
 * Characters into an element, because marking and unmarking split and merge text nodes but never change text.
 */
interface Spot {
  readonly within: Element;
  readonly chars: number;
}

interface Held {
  readonly selector: string;
  readonly chars: number;
}

function rangeBefore(node: Node): Range {
  const at = document.createRange();
  at.setStartBefore(node);
  return at;
}

function charsTo(within: Element, at: Range): number {
  const before = document.createRange();
  before.selectNodeContents(within);
  before.setEnd(at.startContainer, at.startOffset);
  return before.toString().length;
}

/** `spot` as a collapsed range over the current text nodes; past the last one, the end of `within`. */
function pointAt(spot: Spot): Range {
  const at = document.createRange();
  const texts = document.createTreeWalker(spot.within, NodeFilter.SHOW_TEXT);
  let left = spot.chars;
  for (let node = texts.nextNode(); node !== null; node = texts.nextNode()) {
    const length = (node as Text).length;
    if (left < length) {
      at.setStart(node, left);
      return at;
    }
    left -= length;
  }
  at.selectNodeContents(spot.within);
  at.collapse(false);
  return at;
}

interface Piece {
  readonly from: number;
  readonly to: number;
  readonly hit: number;
}

/** Find the needle in one run and wrap it, one `hits` entry per occurrence. Offsets index the original text. */
function markRun(run: readonly Text[], needle: Needle, hits: HTMLElement[][]): void {
  const starts: number[] = [];
  let text = "";
  for (const node of run) {
    starts.push(text.length);
    text += node.nodeValue ?? "";
  }
  const found = occurrences(text, needle);
  if (found.length === 0) {
    return;
  }
  const pieces: Piece[][] = run.map(() => []);
  let first = 0;
  for (const at of found) {
    const hit = hits.length;
    hits.push([]);
    const end = at + needle.text.length;
    while (first + 1 < run.length && (starts[first + 1] ?? 0) <= at) {
      first++;
    }
    for (let i = first; i < run.length; i++) {
      const nodeStart = starts[i] ?? 0;
      if (nodeStart >= end) {
        break;
      }
      const nodeEnd = nodeStart + (run[i]?.length ?? 0);
      const from = Math.max(at, nodeStart) - nodeStart;
      const to = Math.min(end, nodeEnd) - nodeStart;
      if (to > from) {
        pieces[i]?.push({ from, to, hit });
      }
    }
  }
  for (let i = 0; i < run.length; i++) {
    const node = run[i];
    const nodePieces = pieces[i];
    if (node !== undefined && nodePieces !== undefined && nodePieces.length > 0) {
      splitNode(node, nodePieces, hits);
    }
  }
}

/** Only text nodes are touched: element nodes and their listeners are never disturbed. */
function splitNode(node: Text, pieces: readonly Piece[], hits: HTMLElement[][]): void {
  const text = node.nodeValue ?? "";
  const frag = document.createDocumentFragment();
  let last = 0;
  for (const p of pieces) {
    if (p.from > last) {
      frag.appendChild(document.createTextNode(text.slice(last, p.from)));
    }
    const mark = el(
      "mark",
      { className: HIT_CLASS, "data-hit": String(p.hit) },
      text.slice(p.from, p.to),
    );
    frag.appendChild(mark);
    hits[p.hit]?.push(mark);
    last = p.to;
  }
  if (last < text.length) {
    frag.appendChild(document.createTextNode(text.slice(last)));
  }
  node.parentNode?.replaceChild(frag, node);
}

/** Merges adjacent text nodes so the DOM returns to its pre-highlight shape. */
function unwrapMark(mark: HTMLElement): void {
  const parent = mark.parentNode;
  if (parent === null) {
    return;
  }
  parent.replaceChild(document.createTextNode(mark.textContent), mark);
  parent.normalize();
}

export class FindEngine {
  /** The element the walker scans; public so a re-rooting caller can tell whether this engine is current. */
  readonly root: HTMLElement;
  /** One entry per hit: its `<mark>` pieces in document order. */
  private hits: HTMLElement[][] = [];
  private current = -1;
  /**
   * Collapsed just before the current hit's first piece. Live, so removing the subtree holding it moves it to where
   * that subtree stood, which is how `refresh` places a replaced hit when no copy of its holder is mounted.
   */
  private anchor: Range | null = null;
  private held: Held | null = null;
  private readonly holderAttrs: readonly string[];
  private lastQuery = "";

  /**
   * `holderAttrs` names the attributes identifying the element a hit lives in: the closest carrying all of them. A
   * holder re-rendered as a copy with the same values keeps, through `refresh`, the current hit's character position
   * in it: the same hit only while the text before it is unchanged. Empty means no holder.
   */
  constructor(root: HTMLElement, holderAttrs: readonly string[] = []) {
    this.root = root;
    this.holderAttrs = holderAttrs;
  }

  get total(): number {
    return this.hits.length;
  }

  get currentIndex(): number {
    return this.current;
  }

  get query(): string {
    return this.lastQuery;
  }

  /** Re-highlight `query` across the root, clearing prior marks; the current match resets to 0 (or -1). Returns the count. */
  search(query: string, caseSensitive = false): number {
    return this.walk(query, caseSensitive, null);
  }

  /**
   * Re-highlight `query` keeping the current hit: found again by where it starts, not its index, and walked even while
   * its `content-visibility: auto` box is skipped, so the hit and its highlight outlast any scroll. Where it starts is
   * counted in its holder while one with its identity is mounted, so a holder re-rendered as a copy keeps that
   * character position; else it is where the hit's element stood. Once it is gone (its turn folded, its holder
   * removed), the first hit from there is current, wrapping like `next`. Returns the count.
   */
  refresh(query: string, caseSensitive = false): number {
    const spot = this.heldSpot() ?? this.anchorSpot();
    const total = this.walk(
      query,
      caseSensitive,
      spot === null ? null : skippableBoxOf(spot.within, this.root),
    );
    if (spot !== null) {
      // With no hit after the spot this is a no-op, and `walk`'s first hit stays current: the wrap.
      this.setCurrent(this.firstFrom(spot));
    }
    return total;
  }

  private walk(query: string, caseSensitive: boolean, keep: Element | null): number {
    this.clear();
    this.lastQuery = query;
    if (query === "") {
      return 0;
    }
    const needle = prepare(query, caseSensitive);
    const hits: HTMLElement[][] = [];
    for (const run of this.collectRuns(keep)) {
      markRun(run, needle, hits);
    }
    this.hits = hits;
    this.current = hits.length > 0 ? 0 : -1;
    this.applyCurrent();
    return hits.length;
  }

  private anchorSpot(): Spot | null {
    const at = this.anchor;
    if (at === null) {
      return null;
    }
    const node = at.startContainer;
    const within = node instanceof Element ? node : node.parentElement;
    return within === null ? null : { within, chars: charsTo(within, at) };
  }

  private heldSpot(): Spot | null {
    if (this.held === null) {
      return null;
    }
    const within = this.root.querySelector(this.held.selector);
    if (within === null) {
      return null;
    }
    const mark = this.currentMark();
    // A mark still in its holder is counted afresh: text inserted before it since would stale the recorded count.
    if (mark !== null && within.contains(mark)) {
      return { within, chars: charsTo(within, rangeBefore(mark)) };
    }
    return { within, chars: this.held.chars };
  }

  private holdOf(mark: HTMLElement, before: Range): Held | null {
    const attrs = this.holderAttrs;
    const holder = attrs.length === 0 ? null : mark.closest(attrs.map((a) => `[${a}]`).join(""));
    if (holder === null) {
      return null;
    }
    const selector = attrs
      .map((a) => `[${a}="${CSS.escape(holder.getAttribute(a) ?? "")}"]`)
      .join("");
    return { selector, chars: charsTo(holder, before) };
  }

  /** The first hit starting at or after `spot`, or -1. */
  private firstFrom(spot: Spot): number {
    const at = pointAt(spot);
    // From the first piece's text: `(mark, 0)` sorts before a point inside the mark's own text.
    return this.hits.findIndex((pieces) => {
      const first = pieces[0];
      return first !== undefined && at.comparePoint(first.firstChild ?? first, 0) >= 0;
    });
  }

  /** Remove all highlight marks and restore the original text nodes. */
  clear(): void {
    for (const pieces of this.hits) {
      for (const mark of pieces) {
        unwrapMark(mark);
      }
    }
    // Sweep strays an external DOM change left behind (e.g. a reconcile replaced a message element).
    for (const mark of [...this.root.querySelectorAll<HTMLElement>(`mark.${HIT_CLASS}`)]) {
      unwrapMark(mark);
    }
    this.hits = [];
    this.current = -1;
    this.anchor = null;
    this.held = null;
    this.lastQuery = "";
  }

  next(): void {
    if (this.hits.length === 0) {
      return;
    }
    this.current = (this.current + 1) % this.hits.length;
    this.applyCurrent();
  }

  prev(): void {
    if (this.hits.length === 0) {
      return;
    }
    this.current = (this.current - 1 + this.hits.length) % this.hits.length;
    this.applyCurrent();
  }

  /** Make hit `index` current; out of range is a no-op. */
  setCurrent(index: number): void {
    if (index < 0 || index >= this.hits.length) {
      return;
    }
    this.current = index;
    this.applyCurrent();
  }

  /** Drop the current hit, keeping every highlight: the cursor moved to a list this engine does not hold. */
  clearCurrent(): void {
    this.current = -1;
    this.applyCurrent();
  }

  /** The current hit's first piece, which is where a scroll lands. */
  currentMark(): HTMLElement | null {
    return this.hits[this.current]?.[0] ?? null;
  }

  private applyCurrent(): void {
    for (let i = 0; i < this.hits.length; i++) {
      for (const mark of this.hits[i] ?? []) {
        mark.classList.toggle(CURRENT_CLASS, i === this.current);
      }
    }
    const mark = this.currentMark();
    if (mark === null) {
      this.anchor = null;
      this.held = null;
      return;
    }
    this.anchor = rangeBefore(mark);
    this.held = this.holdOf(mark, this.anchor);
  }

  /** Runs of searchable text nodes. A block tag ends the run whether or not its subtree is searched. */
  private collectRuns(keep: Element | null): Text[][] {
    const runs: Text[][] = [];
    let run: Text[] = [];
    const flush = (): void => {
      if (run.length > 0) {
        runs.push(run);
        run = [];
      }
    };
    const visit = (node: Node): void => {
      if (node.nodeType === TEXT_NODE) {
        if ((node.nodeValue ?? "").length > 0) {
          run.push(node as Text);
        }
        return;
      }
      if (node.nodeType !== ELEMENT_NODE) {
        return;
      }
      const elem = node as Element;
      const bounds = BLOCK_TAGS.has(elem.tagName);
      if (bounds) {
        flush();
      }
      if (isSearchableElement(elem, keep)) {
        for (const child of elem.childNodes) {
          visit(child);
        }
      }
      if (bounds) {
        flush();
      }
    };
    for (const child of this.root.childNodes) {
      visit(child);
    }
    flush();
    return runs;
  }
}
