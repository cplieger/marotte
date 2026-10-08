// Three surfaces, one smd-parser. createMarkdownStream (live turn): blocks decorate and animate as they complete, and
// large flushes yield across tasks. renderMarkdownInto (replay): decorate, no animation. renderMarkdown (tests,
// previews): pure parser output.

import { el } from "@cplieger/reactive";
import { parser, parser_end, parser_write } from "./smd-parser.js";
import type { Parser } from "./smd-parser.js";
import { domRenderer } from "./smd-renderer.js";
import { linkifyPaths } from "./linkify.js";
import { decorateCodeBlocks, decorateStreamingCodeTail } from "./code-blocks.js";
import { renderSvgBlock } from "./svg-block.js";

/** Buffered deltas reach the append-only parser at most once per interval; nothing re-parses. */
const FLUSH_INTERVAL_MS = 200;

/** Bytes parsed per task slice, well under a frame budget on a slow device. */
const PARSE_SLICE_BYTES = 4096;

/** MessageChannel.postMessage yields to the browser as a fresh macro-task, allowing paint between callbacks. */
const yieldChannel = new MessageChannel();
const yieldQueue: (() => void)[] = [];
const MAX_DRAIN_PER_TICK = 4;
yieldChannel.port1.onmessage = () => {
  const count = Math.min(MAX_DRAIN_PER_TICK, yieldQueue.length);
  const fns = yieldQueue.splice(0, count);
  for (const fn of fns) {
    fn();
  }
  if (yieldQueue.length > 0) {
    yieldChannel.port2.postMessage(null);
  }
};
function nextTick(fn: () => void): void {
  const wasEmpty = yieldQueue.length === 0;
  yieldQueue.push(fn);
  if (wasEmpty) {
    yieldChannel.port2.postMessage(null);
  }
}

/**
 * Decorate a freshly completed block. Idempotent. Returns the element now in the document: an `svg` fence is replaced
 * by its diagram.
 */
function decorate(block: HTMLElement): HTMLElement {
  if (block.tagName === "PRE") {
    // Before decorateCodeBlocks: a converted diagram is no longer a code block and must not get code chrome.
    const diagram = renderSvgBlock(block);
    if (diagram !== null) {
      return diagram;
    }
    if (block.parentElement !== null) {
      decorateCodeBlocks(block.parentElement);
    }
    return block;
  }
  linkifyPaths(block);
  return block;
}

/** `parser_end` leaves the last top-level block open, so no per-block callback reaches it; a fence
 *  there is `decorateCodeBlocks`'s. Idempotent, because `linkifyPaths` skips its own buttons. */
function linkifyTail(host: HTMLElement): void {
  const tail = host.lastElementChild;
  if (tail instanceof HTMLElement && tail.tagName !== "PRE") {
    linkifyPaths(tail);
  }
}

function decorateAndAnimate(block: HTMLElement): void {
  // The tag goes on what decorate left in the document, or a replaced `<pre>` animates detached and the diagram does not.
  decorate(block).setAttribute("data-vk-block-enter", "");
}

export interface MarkdownStream {
  /** Append a markdown fragment. Returns immediately; parsing may complete asynchronously. */
  writeDelta(delta: string): void;
  /**
   * Parse everything written so far, now, without finalizing, for a write the reader is already looking at. The first
   * 4KB lands synchronously; the rest yields across tasks.
   */
  flush(): void;
  /** Finalize: flush and drain synchronously, then end the parser. Idempotent. */
  end(): void;
}

export interface MarkdownStreamOptions {
  /**
   * Milliseconds to buffer a `writeDelta` before parsing; defaults to FLUSH_INTERVAL_MS, `0` parses on write. A caller
   * pacing its own writes (`reveal.ts`) wants 0, or the buffer re-lumps what it spread.
   */
  flushIntervalMs?: number;
}

/**
 * Streaming renderer for live assistant bubbles: its own write buffer, flush schedule, and per-block decoration and
 * animation. Large writes split across tasks.
 */
export function createMarkdownStream(
  host: HTMLElement,
  options: MarkdownStreamOptions = {},
): MarkdownStream {
  const flushAfter = options.flushIntervalMs ?? FLUSH_INTERVAL_MS;
  const p: Parser = parser(
    domRenderer(host, {
      onBlockComplete: decorateAndAnimate,
      animateText: true,
    }),
  );
  let buffer = "";
  let pendingParse = ""; // Text queued for chunked parsing.
  let flushTimer: ReturnType<typeof setTimeout> | undefined;
  let draining = false;
  let ended = false;

  const drain = (): void => {
    if (pendingParse === "") {
      draining = false;
      return;
    }
    const sliceEnd = Math.min(PARSE_SLICE_BYTES, pendingParse.length);
    const slice = pendingParse.slice(0, sliceEnd);
    pendingParse = pendingParse.slice(sliceEnd);
    parser_write(p, slice);
    // An open fence reaches no per-block callback, so this sweep gives a streaming block its label and Copy button.
    decorateStreamingCodeTail(host);
    if (pendingParse !== "") {
      nextTick(drain);
    } else {
      draining = false;
    }
  };

  const flush = (): void => {
    if (buffer === "") {
      return;
    }
    pendingParse += buffer;
    buffer = "";
    if (flushTimer !== undefined) {
      clearTimeout(flushTimer);
      flushTimer = undefined;
    }
    if (!draining) {
      draining = true;
      drain();
    }
  };

  return {
    writeDelta(delta: string): void {
      if (ended || delta === "") {
        return;
      }
      buffer += delta;
      if (flushAfter <= 0) {
        flush();
        return;
      }
      flushTimer ??= setTimeout(flush, flushAfter);
    },
    flush(): void {
      if (ended) {
        return;
      }
      flush();
    },
    end(): void {
      if (ended) {
        return;
      }
      ended = true;
      if (buffer !== "") {
        pendingParse += buffer;
        buffer = "";
      }
      if (flushTimer !== undefined) {
        clearTimeout(flushTimer);
        flushTimer = undefined;
      }
      // end() is a complete-now contract; yielding only applies while streaming.
      while (pendingParse !== "") {
        const sliceEnd = Math.min(PARSE_SLICE_BYTES, pendingParse.length);
        parser_write(p, pendingParse.slice(0, sliceEnd));
        pendingParse = pendingParse.slice(sliceEnd);
      }
      draining = false;
      parser_end(p);
      linkifyTail(host);
      // parser_end does not close an open fence, so finalize it here: highlighting and Run are withheld while streaming.
      decorateCodeBlocks(host);
    },
  };
}

/** Replay render: parse the full markdown into `host` with decoration, no entry animation. Synchronous. */
export function renderMarkdownInto(host: HTMLElement, md: string): void {
  const p = parser(domRenderer(host, { onBlockComplete: decorate }));
  parser_write(p, md);
  parser_end(p);
  linkifyTail(host);
  // An unterminated fence in stored content never reaches the per-block callback either.
  decorateCodeBlocks(host);
}

/** Pure parser output, no decoration or animation, for tests, hover previews and other structural uses. */
export function renderMarkdown(md: string): string {
  const tmp = el("div");
  const p = parser(domRenderer(tmp));
  parser_write(p, md);
  parser_end(p);
  return tmp.innerHTML;
}
