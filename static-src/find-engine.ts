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
 * invisible (except `display: contents`).
 */
function isSearchableElement(elem: Element): boolean {
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
    return rendersWithoutBox(elem);
  }
  return true;
}

/** `display: contents` is the one shape `checkVisibility` calls invisible that is not hidden, so the walker descends it. */
function rendersWithoutBox(elem: Element): boolean {
  return getComputedStyle(elem).display === "contents";
}

/** One slice of a text node that belongs to a hit. */
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
  private lastQuery = "";

  constructor(root: HTMLElement) {
    this.root = root;
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
    this.clear();
    this.lastQuery = query;
    if (query === "") {
      return 0;
    }
    const needle = prepare(query, caseSensitive);
    const hits: HTMLElement[][] = [];
    for (const run of this.collectRuns()) {
      markRun(run, needle, hits);
    }
    this.hits = hits;
    this.current = hits.length > 0 ? 0 : -1;
    this.applyCurrentClass();
    return hits.length;
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
    this.lastQuery = "";
  }

  next(): void {
    if (this.hits.length === 0) {
      return;
    }
    this.current = (this.current + 1) % this.hits.length;
    this.applyCurrentClass();
  }

  prev(): void {
    if (this.hits.length === 0) {
      return;
    }
    this.current = (this.current - 1 + this.hits.length) % this.hits.length;
    this.applyCurrentClass();
  }

  /** Restore the current index after a live re-run, so streaming does not jump back to match 1. Clamped; out of range is a no-op. */
  setCurrent(index: number): void {
    if (index < 0 || index >= this.hits.length) {
      return;
    }
    this.current = index;
    this.applyCurrentClass();
  }

  /** Drop the current hit, keeping every highlight: the cursor moved to a list this engine does not hold. */
  clearCurrent(): void {
    this.current = -1;
    this.applyCurrentClass();
  }

  /** The current hit's first piece, which is where a scroll lands. */
  currentMark(): HTMLElement | null {
    return this.hits[this.current]?.[0] ?? null;
  }

  private applyCurrentClass(): void {
    for (let i = 0; i < this.hits.length; i++) {
      for (const mark of this.hits[i] ?? []) {
        mark.classList.toggle(CURRENT_CLASS, i === this.current);
      }
    }
  }

  /** Runs of searchable text nodes. A block tag ends the run whether or not its subtree is searched. */
  private collectRuns(): Text[][] {
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
      if (isSearchableElement(elem)) {
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
