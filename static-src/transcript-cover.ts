// The transcript's one placeholder: an opaque cover over the chat scroller, held while the chat
// the reader is on has nothing of its own painted under it. Every holder and releaser goes through
// this module, so a burst of switches shares one element and one skeleton.

import { $, setBusy } from "./dom.js";
import { chatSkeleton } from "./skeleton.js";

const COVER_ID = "transcript-cover";

let cover: HTMLDivElement | null = null;
/** The chat the cover stands for; "" while it is down. */
let holder = "";

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
  const node = ensureCover();
  holder = chatID;
  node.hidden = false;
  delete node.dataset["state"];
  setBusy($.messagesWrap, true);
}

/** Make the transcript under a held cover `inert` until a release; a no-op with no cover. Apart
 *  from `holdCover` because `inert` restyles every resident turn, which in a gesture's frame costs
 *  a 4x-throttled CPU about 50 ms of style; until it runs, the opaque cover takes the pointer. */
export function inertUnderCover(): void {
  if (holder !== "") {
    $.messagesWrap.inert = true;
  }
}

/** Lift the cover when `chatID` is the chat it stands for; a release for any other chat is a
 *  paint the cover is not waiting on. */
export function releaseCover(chatID: string): void {
  if (holder !== "" && holder === chatID) {
    dropCover();
  }
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
  $.messagesWrap.inert = false;
  setBusy($.messagesWrap, false);
  if (cover === null || !wasHeld) {
    return;
  }
  // The fade-out is opacity only and stops taking pointer input at once (13-messages.css); its
  // `animationend` hides the element.
  cover.dataset["state"] = "released";
}
