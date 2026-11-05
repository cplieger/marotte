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
 *  re-animating it. The transcript under it is `inert` and `aria-busy` until a release. */
export function holdCover(chatID: string): void {
  const node = ensureCover();
  holder = chatID;
  node.hidden = false;
  delete node.dataset["state"];
  $.messagesWrap.inert = true;
  setBusy($.messagesWrap, true);
}

/** Lift the cover when `chatID` is the chat it stands for; a release for any other chat is a
 *  paint the cover is not waiting on. */
export function releaseCover(chatID: string): void {
  if (holder !== "" && holder === chatID) {
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
