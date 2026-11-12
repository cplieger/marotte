// The transcript's one placeholder: an opaque cover over the chat scroller, held while the chat
// the reader is on has nothing of its own painted under it, and shown only once that has lasted
// `COVER_SHOW_DELAY_MS`. Every holder and releaser goes through this module, so a burst of switches
// shares one element and one skeleton.

import { skeletonTiming, type SkeletonTimingController } from "@cplieger/ui/skeleton";

import { $, setBusy } from "./dom.js";
import { chatSkeleton } from "./skeleton.js";

const COVER_ID = "transcript-cover";

/** How long a held cover stays off screen, so a chat that paints sooner shows no placeholder.
 *  Above the design language's usual 200: a switch whose window is already in the store repaints
 *  in about a quarter second. No minimum once shown: the skeleton has the latest turn's shape, and
 *  the release fade carries the swap. */
const COVER_SHOW_DELAY_MS = 250;

let cover: HTMLDivElement | null = null;
/** The chat the cover stands for; "" while it is down. */
let holder = "";

/** A release deferred to the next task; any hold or drop in between replaces it. */
let pendingLift: object | null = null;

/** The held cover's pending show; null once it shows or while none is held. */
let reveal: SkeletonTimingController | null = null;

/** The chat whose switch keeps the outgoing view on screen until its cover shows or its turns
 *  paint; "" when no swap waits. */
let swapDue = "";
/** The waiting switch's view reveal (tabs.ts), run at the swap. */
let deferredReveal: (() => void) | null = null;

/** Show `node` once the window passes, restarting a window already running. */
function scheduleReveal(node: HTMLDivElement): void {
  reveal?.cancel();
  reveal = skeletonTiming(
    () => {
      reveal = null;
      if (cover !== node || !node.isConnected) {
        return;
      }
      const due = swapDue;
      if (due !== "") {
        takeOutgoingReads();
      }
      node.hidden = false;
      delete node.dataset["state"];
      if (due !== "") {
        runSwap();
        owner?.swap(due);
      }
    },
    { showDelayMs: COVER_SHOW_DELAY_MS },
  );
}

/** Retake the outgoing view's park reads at the swap's layout: the reader could still scroll it. */
function takeOutgoingReads(): void {
  if ($.messagesWrap.closest(".hidden") === null) {
    owner?.take();
  }
}

function runSwap(): void {
  swapDue = "";
  const run = deferredReveal;
  deferredReveal = null;
  run?.();
}

/** Whether `chatID`'s switch still keeps the outgoing view on screen. */
export function swapDeferred(chatID: string): boolean {
  return swapDue !== "" && swapDue === chatID;
}

/** Run the switch's view reveal now, or at the swap while one waits; a later switch's hold
 *  discards it. */
export function deferReveal(fn: () => void): void {
  if (swapDue === "") {
    fn();
    return;
  }
  deferredReveal = fn;
}

/** The waiting switch's target has turns to show: take the park reads and reveal its view, before
 *  its paint parks the outgoing view. A no-op when no swap waits. */
export function swapTranscript(): void {
  if (swapDue === "") {
    return;
  }
  takeOutgoingReads();
  runSwap();
}

/** Whether the cover is on screen (not hidden, not fading out). */
export function coverShown(): boolean {
  return cover !== null && !cover.hidden && cover.dataset["state"] !== "released";
}

function cancelReveal(): void {
  reveal?.cancel();
  reveal = null;
}

interface InputScheduling {
  isInputPending?: () => boolean;
}

/** Input the browser has received and not yet dispatched, where it can tell (Chromium); elsewhere
 *  false, and a click queued behind the paint shows the superseded chat until it is dispatched. */
function inputPending(): boolean {
  const scheduling = (navigator as Navigator & { scheduling?: InputScheduling }).scheduling;
  return scheduling?.isInputPending?.() === true;
}

function reducedMotion(): boolean {
  return matchMedia("(prefers-reduced-motion: reduce)").matches;
}

function ensureCover(): HTMLDivElement {
  // Re-created when a test or a remount replaced the shell, since a detached cover covers nothing.
  if (cover?.isConnected === true) {
    return cover;
  }
  const node = document.createElement("div");
  node.id = COVER_ID;
  node.className = "transcript-cover";
  node.setAttribute("aria-hidden", "true");
  node.hidden = true;
  const body = document.createElement("div");
  body.className = "transcript-cover-body";
  body.appendChild(chatSkeleton());
  node.appendChild(body);
  node.addEventListener("animationend", (e) => {
    if (e.target === node && node.dataset["state"] === "released") {
      node.hidden = true;
      delete node.dataset["state"];
    }
  });
  $.messagesWrap.after(node);
  cover = node;
  return node;
}

/** Move focus off the transcript before it is covered: a hidden subtree must not hold focus, and
 *  the composer is the app's focus home. Reads no layout. */
export function moveFocusOffTranscript(): void {
  const focused = document.activeElement;
  if (focused !== null && $.messagesWrap.contains(focused)) {
    $.promptInput.focus({ preventScroll: true });
  }
}

let focusGuarded = false;

/** Turn focus that enters the covered transcript (Tab, a script) to the composer. */
function guardCoveredFocus(e: FocusEvent): void {
  if (holder !== "" && e.target instanceof Node && $.messagesWrap.contains(e.target)) {
    $.promptInput.focus({ preventScroll: true });
  }
}

/** Cover the transcript for `chatID`, or hand an existing cover to it without re-creating or
 *  re-animating it. Until a release the transcript under it is `aria-busy`, hidden from assistive
 *  tech and refuses focus; once the cover shows, it takes the pointer. A hold before the cover
 *  shows starts its window again. */
export function holdCover(chatID: string): void {
  const node = ensureCover();
  holder = chatID;
  pendingLift = null;
  deferredReveal = null;
  if (node.hidden || node.dataset["state"] === "released") {
    swapDue = chatID;
    scheduleReveal(node);
  } else {
    swapDue = "";
  }
  // Not `inert`: setting it restyles every resident turn, 30 to 140 ms each way in WebKit.
  $.messagesWrap.setAttribute("aria-hidden", "true");
  setBusy($.messagesWrap, true);
  if (!focusGuarded) {
    document.addEventListener("focusin", guardCoveredFocus, true);
    focusGuarded = true;
  }
}

/** Lift the cover in this task when `chatID` is the chat it stands for, unless the browser reports
 *  input waiting, which may request another switch: then in the next task, unless a hold or drop
 *  came first. A release for any other chat is a paint the cover is not waiting on. */
export function releaseCover(chatID: string): void {
  if (holder === "" || holder !== chatID || pendingLift !== null) {
    return;
  }
  if (!inputPending()) {
    dropCover();
    return;
  }
  const p = {};
  pendingLift = p;
  setTimeout(() => {
    if (pendingLift === p) {
      dropCover();
    }
  }, 0);
}

/** `chatID`'s rows are painted and its cover waits only on their builds: it is never shown from
 *  here, since a skeleton over painted rows would flash. */
export function coverContentPainted(chatID: string): void {
  if (holder === chatID) {
    cancelReveal();
  }
}

/** Whether the cover is held, shown or not yet, and stands for `forChat` when one is given. */
export function coverHeld(forChat?: string): boolean {
  return holder !== "" && (forChat === undefined || holder === forChat);
}

/** Take the transcript scroller out of layout (display: none), so a frame that paints the cover
 *  over a chat view coming back from another view kind lays out none of the turns under it.
 *  Chromium, Firefox and WebKit keep a scroller's offset across display: none and fire no scroll
 *  event; `content-visibility: hidden` on the scroller loses the offset in Chromium and WebKit. */
export function hideTranscript(): void {
  if (!$.messagesWrap.classList.contains("hidden")) {
    $.messagesWrap.classList.add("hidden");
  }
}

export function transcriptHidden(): boolean {
  return $.messagesWrap.classList.contains("hidden");
}

interface TranscriptOwner {
  /** Capture what parking the shown transcript view reads, at the current layout. */
  readonly take: () => void;
  /** Discard a capture. */
  readonly drop: () => void;
  /** Whether `chatID` has a resident view holding its turns, which it can show with no load. */
  readonly painted: (chatID: string) => boolean;
  /** Swap the transcript to `chatID`'s view, if it is still the painted chat and not yet active. */
  readonly swap: (chatID: string) => void;
}

let owner: TranscriptOwner | null = null;
let parkReadsTaken = false;

/** Install the transcript owner (messages.ts). Until then no chat counts as painted. */
export function registerTranscriptOwner(next: TranscriptOwner): void {
  owner = next;
}

/** Whether a switch to `chatID` can show its own turns with no cover. */
export function transcriptPainted(chatID: string): boolean {
  return owner?.painted(chatID) === true;
}

/** Capture the shown transcript's park reads before a switch's first write, so the park that
 *  follows reads no layout. A no-op while the transcript is out of layout, whose boxes say nothing.
 *  The capture holds until the park takes it, a held cover drops or the next switch's gesture
 *  drops it: only the cover, or the click's own task, keeps the reader from moving the transcript. */
export function takeParkReads(): void {
  if ($.messagesWrap.closest(".hidden") === null) {
    owner?.take();
    parkReadsTaken = true;
  }
}

/** Discard a capture an earlier switch's gesture took. */
export function dropParkReads(): void {
  if (parkReadsTaken) {
    parkReadsTaken = false;
    owner?.drop();
  }
}

export function showTranscript(): void {
  if ($.messagesWrap.classList.contains("hidden")) {
    $.messagesWrap.classList.remove("hidden");
  }
}

/** Lift the cover whoever holds it: the view is not a chat, the strip went empty, or the load
 *  failed and the reader must reach what is painted instead. */
export function dropCover(): void {
  const wasHeld = holder !== "";
  holder = "";
  pendingLift = null;
  cancelReveal();
  $.messagesWrap.removeAttribute("aria-hidden");
  setBusy($.messagesWrap, false);
  if (!wasHeld) {
    return;
  }
  dropParkReads();
  const due = swapDue;
  if (due !== "") {
    // A failed, missing or abandoned switch still owes its view the swap the cover was waiting on.
    takeOutgoingReads();
    runSwap();
    owner?.swap(due);
  }
  if (cover === null || cover.hidden) {
    return;
  }
  // Under a hidden view no animation runs, so no `animationend` would come before the view shows
  // again and plays the fade-out over its content. Under reduced motion the 40-a11y sweep's
  // near-zero fade still ends a frame or two after the paint that shows the chat.
  if (cover.closest(".hidden") !== null || reducedMotion()) {
    cover.hidden = true;
    return;
  }
  // The fade-out is opacity only and stops taking pointer input at once (13-messages.css); its
  // `animationend` hides the element.
  cover.dataset["state"] = "released";
}
