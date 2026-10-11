// The chat transcript's scroller: one `ScrollController` (scroll-controller.ts) over
// `#messages-wrap`, built at load, and the API the transcript's modules call.

import { $ } from "./dom.js";
import {
  ScrollController,
  type ReadingState,
  type ViewAttachHandle,
  type ViewScrollState,
} from "./scroll-controller.js";

let instance: ScrollController | null = null;

function getInstance(): ScrollController {
  if (instance === null) {
    instance = new ScrollController($.messages, $.messagesWrap, () => $.scrollBottom, true);
    instance.init();
  }
  return instance;
}

/** Deferred DOM access — safe to import before DOMContentLoaded. */
export function getScrollEl(): HTMLElement {
  return getInstance().scrollEl;
}

/** How far the transcript can scroll, in px; 0 when it fits its viewport. */
export function scrollableBy(): number {
  return getInstance().scrollableBy();
}

export function setUserScrolledUp(v: boolean): void {
  getInstance().setUserScrolledUp(v);
}
/** Hand the scroller to a transcript view (unpark / fresh view). */
export function attach(handle: ViewAttachHandle): void {
  getInstance().attach(handle);
}
/** Snapshot and release the current view's scroll state (park). */
export function detach(): ViewScrollState {
  return getInstance().detach();
}
export function jumpTo(target: HTMLElement, opts?: ScrollIntoViewOptions): void {
  getInstance().jumpTo(target, opts);
}
/** Move the transcript by `px` as a layout compensation: it sets no reading state and publishes no
 *  reader gesture. */
export function shiftScroll(px: number): void {
  getInstance().shiftBy(px);
}
/** Open a self-scroll epoch: every scroll event until it closes is the controller's own
 *  animation rather than a reader gesture. */
export function beginSelfScroll(): void {
  getInstance().beginSelfScroll();
}
/** Close the open epoch. */
export function endSelfScroll(): void {
  getInstance().endSelfScroll();
}
/** Scroll to an absolute offset inside the open epoch, parking the reader unless the landing is
 *  at the live edge. */
export function scrollToOffset(px: number, behavior: ScrollBehavior): void {
  getInstance().scrollToOffset(px, behavior);
}
/** Px from the scrollport's top to the reading line. */
export function readingLineOffset(): number {
  return getInstance().readingLineOffset();
}
/** The published live-edge verdict, aim-aware. */
export function atLiveEdgeNow(): boolean {
  return getInstance().atLiveEdgeNow();
}
/** Register `cb` for a size change in one of the view's own cards; returns the unregister. */
export function onContentResize(cb: () => void): () => void {
  return getInstance().onContentResize(cb);
}
/** Register `cb` for a view taking the scroller (unpark); returns the unregister. */
export function onAttach(cb: () => void): () => void {
  return getInstance().onAttach(cb);
}
export function scrollToBottom(): void {
  getInstance().scrollToBottom();
}
export function setLoadMore(fn: (() => void) | null, hasMore: boolean): void {
  getInstance().setLoadMore(fn, hasMore);
}
/** Re-measure the older-page pass in flight where the reader stands now; a no-op once the page has landed. A writer
 *  that may land the page calls it just before writing, because the browser can move the scroller before that move's
 *  `scroll` event runs. */
export function rebaseLoadMore(): void {
  getInstance().rebaseLoadMore();
}
export function resetScrollState(): void {
  getInstance().resetScrollState();
}
export function readingState(): ReadingState {
  return getInstance().readingState();
}
export function onReadingStateChange(cb: (s: ReadingState) => void): void {
  getInstance().onReadingStateChange(cb);
}
export function onTranscriptMutate(cb: () => void): () => void {
  return getInstance().onTranscriptMutate(cb);
}
export function onReaderGesture(cb: () => void): () => void {
  return getInstance().onReaderGesture(cb);
}
/** Register `cb` for a scroll that has settled into one frame; returns the unregister. */
export function onViewportChange(cb: () => void): () => void {
  return getInstance().onViewportChange(cb);
}
export function setAnchorProvider(fn: (() => HTMLElement | null) | null): void {
  getInstance().setAnchorProvider(fn);
}
export function setResumeLabel(text: string): void {
  getInstance().setResumeLabel(text);
}
export function deferWhileReading(mutate: () => void): void {
  getInstance().deferWhileReading(mutate);
}
export function fillViewport(): void {
  getInstance().fillViewport();
}
// Init on load.
if (document.readyState === "loading") {
  document.addEventListener("DOMContentLoaded", () => {
    getInstance();
  });
} else {
  getInstance();
}
