// Per-chat composer state: the unsent text plus the staged attachments, saved and restored as the active chat changes
// because one textarea and one pill row serve every chat. Persisted on the chat record server-side (attachments via
// `attachments.ts`). This module holds the local working copy, authoritative for the UI; the server only seeds it
// (`seedComposerState`), except for a chat this device is not typing in (`adoptRemoteComposerState`).

import { $ } from "./dom.js";
import { writeComposerSilently } from "./composer-value.js";
import { get } from "./store.js";
import { setDraft } from "./actions/chat.js";
import { debouncedDispatch, registerCleanup, type DebouncedDispatch } from "./actions/index.js";
import {
  stashAttachments,
  restoreAttachments,
  dropAttachments,
  seedAttachments,
  adoptRemoteAttachments,
  flushAttachments,
  _resetAttachmentsForTest,
} from "./attachments.js";

/** Same 600ms as the steering textarea: one autosave cadence. */
const DRAFT_SAVE_WAIT = 600;

/** A chat with no entry has never been typed into on this device and takes the server's draft on first load. */
const drafts = new Map<string, string>();

/** The chat the textarea belongs to; "" between a save and its restore, so `noteComposerText` no-ops on "". */
let liveChatID = "";

let debouncedSave: DebouncedDispatch<{ chatID: string; text: string }> | null = null;

/**
 * Wire the autosave, blur flush and unload flush. Called from app.ts, not prompt-input, to avoid an import cycle
 * (send-state imports prompt-input, transport imports send-state); the two meet on the element via `input` events.
 */
export function initComposerState(): void {
  if (debouncedSave !== null) {
    return;
  }
  debouncedSave = debouncedDispatch(setDraft, { wait: DRAFT_SAVE_WAIT });
  const el = $.promptInput;
  const onInput = (): void => {
    noteComposerText(el.value);
  };
  const onBlur = (): void => {
    flushComposerDraft();
    flushAttachments();
  };
  // pagehide, not beforeunload: beforeunload does not fire when iOS discards a backgrounded tab. Best-effort; the short
  // debounce means most drafts are already saved.
  const onPageHide = (): void => {
    flushComposerDraft();
    flushAttachments();
  };
  el.addEventListener("input", onInput);
  el.addEventListener("blur", onBlur);
  window.addEventListener("pagehide", onPageHide);
  registerCleanup(() => {
    el.removeEventListener("input", onInput);
    el.removeEventListener("blur", onBlur);
    window.removeEventListener("pagehide", onPageHide);
    flushComposerDraft();
    flushAttachments();
    debouncedSave?.cancel();
  });
}

/** Record the composer's text for the active chat and schedule its save; the one write path into the draft map. */
export function noteComposerText(text: string): void {
  if (liveChatID === "") {
    return;
  }
  if (drafts.get(liveChatID) === text) {
    return;
  }
  drafts.set(liveChatID, text);
  debouncedSave?.({ chatID: liveChatID, text });
}

/**
 * Read from the map, never the element: ArrowUp shows history in the box without an `input` event, so reading the box
 * would send history to the server and overwrite the draft.
 */
function liveDraft(): string {
  return drafts.get(liveChatID) ?? "";
}

/** Flush a pending draft save now (blur, switch, close, unload). No-op when nothing is pending. */
export function flushComposerDraft(): void {
  if (liveChatID === "" || debouncedSave?.isPending() !== true) {
    return;
  }
  void debouncedSave.flush({ chatID: liveChatID, text: liveDraft() });
}

/**
 * Park the composer's contents under their chat. MUST run before the store's active chat changes, or the outgoing id
 * is gone.
 */
export function saveComposerState(): void {
  flushComposerDraft();
  stashAttachments();
  liveChatID = "";
}

/** Put `chatID`'s composer contents on screen; a chat with no draft gets an empty box, never the previous one's. */
export function restoreComposerState(chatID: string): void {
  liveChatID = chatID;
  writeComposerSilently(drafts.get(chatID) ?? "");
  restoreAttachments(chatID);
}

/**
 * Point the composer at `chatID`, parking what it held first: save-and-restore as one call for sites that move the
 * active chat without `activateChatView` (`createSession`, `openTangentChat`, `removeChat`). Without it a keystroke
 * in that window is filed under the chat the user just left. Idempotent, so it can run ahead of the activation.
 */
export function retargetComposer(chatID: string): void {
  saveComposerState();
  restoreComposerState(chatID);
}

/**
 * Adopt the server's stored draft and attachments for `chatID` once its record loads. Loses to anything local: a chat
 * already typed into keeps its copy, and the box is written only when empty.
 */
export function seedComposerState(chatID: string): void {
  seedAttachments(chatID, get(chatID)?.attachments ?? []);
  if (drafts.has(chatID)) {
    return;
  }
  const text = get(chatID)?.draft ?? "";
  drafts.set(chatID, text);
  if (text === "" || chatID !== liveChatID) {
    return;
  }
  if ($.promptInput.value === "") {
    writeComposerSilently(text);
  }
}

/**
 * Put a failed send's text back under the chat that sent it. Loses to a live draft (the user may be typing the next
 * message), and the box is written only when empty (it may show a history preview). Persisted like a keystroke.
 */
export function restoreFailedSend(chatID: string, text: string): void {
  if (chatID === "" || text === "") {
    return;
  }
  if ((drafts.get(chatID) ?? "") !== "") {
    return;
  }
  drafts.set(chatID, text);
  debouncedSave?.({ chatID, text });
  if (chatID === liveChatID && $.promptInput.value === "") {
    writeComposerSilently(text);
  }
}

/** The draft recorded for `chatID`: the map, never the box, which can hold history. */
export function composerDraft(chatID: string): string {
  return drafts.get(chatID) ?? "";
}

/** Undo an Edit the server refused: put `before` back as `chatID`'s draft, but only
 *  while that draft is still the `taken` text the Edit wrote. Anything typed since,
 *  or a closed chat, is newer and wins; the box is touched only for the live chat. */
export function restoreRefusedEdit(chatID: string, taken: string, before: string): void {
  if (chatID === "" || drafts.get(chatID) !== taken) {
    return;
  }
  drafts.set(chatID, before);
  if (chatID === liveChatID) {
    debouncedSave?.({ chatID, text: before });
    if ($.promptInput.value === taken) {
      writeComposerSilently(before);
    }
    return;
  }
  // One debounce slot serves every chat, so a pending live save must go out first.
  flushComposerDraft();
  void debouncedSave?.flush({ chatID, text: before });
}

/**
 * Adopt a `draft_changed` frame for a chat this device is NOT typing in; ignored for the live chat, whose map entry
 * is authoritative. Unlike the seed it beats the local copy: the frame came from a write the server accepted.
 */
export function adoptRemoteComposerState(chatID: string, text: string, paths: string[]): void {
  adoptRemoteAttachments(chatID, paths);
  if (chatID === "" || chatID === liveChatID) {
    return;
  }
  drafts.set(chatID, text);
}

/**
 * Forget a closed or deleted chat's composer state, locally only (the record keeps its draft). Clears the box for the
 * live chat: on the close that empties the strip nothing activates after, and Send would post it to an unrelated row.
 */
export function dropComposerState(chatID: string): void {
  drafts.delete(chatID);
  dropAttachments(chatID);
  if (chatID === liveChatID) {
    liveChatID = "";
    writeComposerSilently("");
  }
}

/** Test seam: reset the module between cases. */
export function _resetComposerStateForTest(): void {
  debouncedSave?.cancel();
  debouncedSave = null;
  drafts.clear();
  liveChatID = "";
  _resetAttachmentsForTest();
}
