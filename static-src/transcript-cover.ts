// The transcript's one placeholder: an opaque cover over the chat scroller, held while the chat
// the reader is on has nothing of its own painted under it. Every holder and releaser goes through
// this module, so a burst of switches shares one element and one skeleton.

import { $, setBusy } from "./dom.js";
import { afterNextPaint } from "./next-paint.js";
import { chatSkeleton } from "./skeleton.js";

const COVER_ID = "transcript-cover";

let cover: HTMLDivElement | null = null;
/** The chat the cover stands for; "" while it is down. */
let holder = "";

/** A paint's release, waiting for that paint to reach the screen; replaced by any hold or drop. */
interface PendingRelease {
  /** `gestureGen` when the release was asked for, moved forward on each re-check. */
  gestureGen: number;
}
let pendingRelease: PendingRelease | null = null;

/** The last lift a release made, until the next gesture: a gesture the browser received before
 *  `at` was made over the cover and only dispatched after it, queued behind a long task. */
let lifted: { readonly chat: string; readonly at: number } | null = null;

/** Bumped in the capture phase by every press and key, before any door's own handler runs. */
let gestureGen = 0;
/** Primary-button presses still down: each may end in a click that switches again. */
const pressed = new Set<number>();
let watching = false;

function watchGestures(): void {
  if (watching) {
    return;
  }
  watching = true;
  const opts = { capture: true, passive: true } as const;
  window.addEventListener(
    "pointerdown",
    (e) => {
      onGesture(e);
      if (e.button === 0) {
        pressed.add(e.pointerId);
      }
    },
    opts,
  );
  const ended = (e: PointerEvent): void => {
    if (pressed.delete(e.pointerId) && pressed.size === 0) {
      recheckSoon();
    }
  };
  window.addEventListener("pointerup", ended, opts);
  window.addEventListener("pointercancel", ended, opts);
  window.addEventListener("keydown", onGesture, opts);
  // A press released outside a blurred window delivers no pointerup.
  window.addEventListener("blur", () => {
    if (pressed.size > 0) {
      pressed.clear();
      recheckSoon();
    }
  });
}

/** Count the gesture, and put a lifted cover back when the gesture predates the lift (`timeStamp`
 *  is when the browser received the input), so its own handler decides before any frame shows
 *  the superseded chat. */
function onGesture(e: Event): void {
  gestureGen += 1;
  const last = lifted;
  lifted = null;
  if (last !== null && holder === "" && e.timeStamp < last.at) {
    holdCover(last.chat);
    pendingRelease = { gestureGen };
    recheckSoon();
  }
}

/** Settle the pending release after the next paint; a press still down defers it to its end. */
function recheckSoon(): void {
  const p = pendingRelease;
  if (p !== null) {
    afterNextPaint(() => {
      settleRelease(p);
    });
  }
}

/** Lift the cover for `p` unless a hold or drop replaced it, a press is down, or a gesture arrived
 *  since the last check, whose handler may switch again: then check once more a task later. */
function settleRelease(p: PendingRelease): void {
  if (pendingRelease !== p || pressed.size > 0) {
    return;
  }
  if (p.gestureGen !== gestureGen) {
    p.gestureGen = gestureGen;
    setTimeout(() => {
      settleRelease(p);
    }, 0);
    return;
  }
  const chat = holder;
  dropCover();
  lifted = { chat, at: performance.now() };
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

/** Move focus off the transcript before it is covered: an `inert` subtree drops focus to <body>,
 *  and the composer is the app's focus home. Reads no layout. */
export function moveFocusOffTranscript(): void {
  const focused = document.activeElement;
  if (focused !== null && $.messagesWrap.contains(focused)) {
    $.promptInput.focus({ preventScroll: true });
  }
}

/** Cover the transcript for `chatID`, or hand an existing cover to it without re-creating or
 *  re-animating it. The transcript under it is `aria-busy` until a release; `inertUnderCover`
 *  makes it `inert`. */
export function holdCover(chatID: string): void {
  watchGestures();
  const node = ensureCover();
  holder = chatID;
  pendingRelease = null;
  lifted = null;
  node.hidden = false;
  delete node.dataset["state"];
  setBusy($.messagesWrap, true);
}

/** Make the transcript under a held cover `inert` until a release; a no-op with no cover. Apart
 *  from `holdCover` because `inert` restyles every resident turn, which in a gesture's frame costs
 *  a 4x-throttled CPU about 50 ms of style; until it runs, the opaque cover takes the pointer. */
export function inertUnderCover(): void {
  // A cover whose release is pending lifts within a frame; `inert` now would restyle the turns twice.
  if (holder !== "" && pendingRelease === null) {
    $.messagesWrap.inert = true;
  }
}

/** Lift the cover when `chatID` is the chat it stands for, once the paint that called this is on
 *  screen and no newer gesture (a press, a click, a key) has switched again; a hold or drop in
 *  between cancels it. A release for any other chat is a paint the cover is not waiting on. */
export function releaseCover(chatID: string): void {
  if (holder === "" || holder !== chatID || pendingRelease !== null) {
    return;
  }
  pendingRelease = { gestureGen };
  recheckSoon();
}

/** Lift a cover held for any chat but `chatID`: the reader came back to the mounted transcript
 *  before the other chat showed. A cover `chatID` itself holds is still waiting on its paint. */
export function releaseCoverExceptFor(chatID: string): void {
  if (holder !== "" && holder !== chatID) {
    dropCover();
  }
}

/** Lift the cover whoever holds it: the view is not a chat, the strip went empty, or the load
 *  failed and the reader must reach what is painted instead. */
export function dropCover(): void {
  const wasHeld = holder !== "";
  holder = "";
  pendingRelease = null;
  lifted = null;
  $.messagesWrap.inert = false;
  setBusy($.messagesWrap, false);
  if (cover === null || !wasHeld) {
    return;
  }
  // The fade-out is opacity only and stops taking pointer input at once (13-messages.css); its
  // `animationend` hides the element.
  cover.dataset["state"] = "released";
}
