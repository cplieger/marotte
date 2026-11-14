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

/** A collapse or expand the reader pressed: park on the content above it until the press's layout settles. */
export function pressHold(): void {
  getInstance().pressHold();
}
/** Hand the scroller to a transcript view (unpark / fresh view); its position lands in
 *  `applyPendingRestore`. */
export function attach(handle: ViewAttachHandle): void {
  getInstance().attach(handle);
}
/** Land the attached view on its saved position, a Following one on its follow position: after
 *  the task's last DOM write, since the scroll write forces a layout. */
export function applyPendingRestore(): void {
  getInstance().applyPendingRestore();
}
/** The current view's park state from layout reads alone; `detach` takes it to park with. */
export function snapshot(): ViewScrollState {
  return getInstance().snapshot();
}
/** Release the current view's scroll state (park) with `taken`, by default a snapshot now. */
export function detach(taken?: ViewScrollState): ViewScrollState {
  return getInstance().detach(taken);
}
export function jumpTo(target: HTMLElement, opts?: ScrollIntoViewOptions): void {
  getInstance().jumpTo(target, opts);
}
/** Move the transcript by `px` as a layout compensation: it sets no reading state and publishes no
 *  reader gesture. */
export function shiftScroll(px: number): void {
  getInstance().shiftBy(px);
}
/** A smooth jump has not landed yet; it re-lands on its target's current position when it does. */
export function jumpInFlight(): boolean {
  return getInstance().jumpInFlight();
}
/** The element the scroll owner holds on the reading line, or null when it holds none (Following, a press window,
 *  a jump in flight). */
export function readerAt(): Element | null {
  return getInstance().readerAt();
}
/** Run a residency DOM change as one hold of the reader's text, in this task. */
export function heldMutation(fn: () => void): void {
  getInstance().heldMutation(fn);
}
/** Jump to an absolute offset, parking the reader unless it lands on the end. Resolves true once it landed, false
 *  when the reader or another jump took the scroller first. A smooth jump lands instantly under reduced motion. */
export function jumpToOffset(px: number, behavior: "instant" | "smooth"): Promise<boolean> {
  return getInstance().jumpToOffset(px, behavior);
}
/** The live anchor may have changed: while Following, the next frame lands on its new position. */
export function followTargetChanged(): void {
  getInstance().followTargetChanged();
}
/** Px from the scrollport's top to the reading line. */
export function readingLineOffset(): number {
  return getInstance().readingLineOffset();
}
/** Register `cb` for a size change in one of the view's own cards; returns the unregister. */
export function onContentResize(cb: () => void): () => void {
  return getInstance().onContentResize(cb);
}
/** Run `cb` once the transcript's boxes inserted so far are laid out at their real size. */
export function whenNearSettled(cb: () => void): void {
  getInstance().whenNearSettled(cb);
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
/** @internal Test seam: publishes what the history-edge observer would, which fake geometry cannot produce. */
// deadset:ignore DS1102 -- test seam: the history edge's published position
export function noteHistoryEdge(near: boolean): void {
  getInstance().noteHistoryEdge(near);
}
export function resetScrollState(): void {
  getInstance().resetScrollState();
}
export function readingState(): ReadingState {
  return getInstance().readingState();
}
export function landingTop(): number {
  return getInstance().landingTop();
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
export function onLiveEdgeRequest(cb: () => void): () => void {
  return getInstance().onLiveEdgeRequest(cb);
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
/** Whether a reader gesture is open on the chat's scroller. */
export function readerGestureOpen(): boolean {
  return getInstance().gestureOpen();
}
// Init on load.
if (document.readyState === "loading") {
  document.addEventListener("DOMContentLoaded", () => {
    getInstance();
  });
} else {
  getInstance();
}
