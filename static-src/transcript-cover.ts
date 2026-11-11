// The transcript's one placeholder: an opaque cover over the chat scroller, held while the chat
// the reader is on has nothing of its own painted under it. Every holder and releaser goes through
// this module, so a burst of switches shares one element and one skeleton.

import { $, setBusy } from "./dom.js";
import { chatSkeleton } from "./skeleton.js";

const COVER_ID = "transcript-cover";

let cover: HTMLDivElement | null = null;
/** The chat the cover stands for; "" while it is down. */
let holder = "";

/** A release deferred to the next task; any hold or drop in between replaces it. */
let pendingLift: object | null = null;

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
 *  tech and refuses focus; the opaque cover takes the pointer. */
export function holdCover(chatID: string): void {
  const node = ensureCover();
  holder = chatID;
  pendingLift = null;
  node.hidden = false;
  delete node.dataset["state"];
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

/** Lift a cover held for any chat but `chatID`: the reader came back to the mounted transcript
 *  before the other chat showed. A cover `chatID` itself holds is still waiting on its paint. */
export function releaseCoverExceptFor(chatID: string): void {
  if (holder !== "" && holder !== chatID) {
    dropCover();
  }
}

/** Whether the cover is up, and stands for `forChat` when one is given. */
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

interface ParkReaders {
  /** Capture what parking the shown transcript view reads, at the current layout. */
  readonly take: () => void;
  /** Discard a capture. */
  readonly drop: () => void;
}

let parkReaders: ParkReaders | null = null;

/** Install the transcript owner's park readers (messages.ts). */
export function registerParkReads(readers: ParkReaders): void {
  parkReaders = readers;
}

/** Capture the shown transcript's park reads before a switch's first write, so the park that
 *  follows reads no layout. A no-op while the transcript is out of layout, whose boxes say nothing.
 *  The capture holds until the park takes it or a held cover drops: only the cover, or the click's
 *  own task, keeps the reader from moving the transcript. */
export function takeParkReads(): void {
  if ($.messagesWrap.closest(".hidden") === null) {
    parkReaders?.take();
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
  $.messagesWrap.removeAttribute("aria-hidden");
  setBusy($.messagesWrap, false);
  if (!wasHeld) {
    return;
  }
  parkReaders?.drop();
  if (cover === null) {
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
