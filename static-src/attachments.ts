// The attachment pill row: files attached to the next prompt. The body opens the file in
// its viewer tab, the `×` removes it. The server classifies by extension: a supported
// document is an ACP `resource` block, an image an `image` block, anything else a path.
// The list is the draft's twin: persisted per chat through `set_attachments` on the
// draft's debounce, silent, no retry, with the LOCAL collection authoritative for the UI.

import {
  createCollection,
  bindList,
  computed,
  effect,
  type ReadonlySignal,
} from "@cplieger/reactive";
import { $ } from "./dom.js";
import { buildAttachmentPill } from "./attachment-pill.js";
import { setAttachments } from "./actions/chat.js";
import { debouncedDispatch, type DebouncedDispatch } from "./actions/index.js";

/**
 * Quiet window before a change is persisted: the draft's 600ms, shared so a reload never
 * shows a sentence without the files it mentions.
 */
const SAVE_WAIT = 600;

/** One attached file. */
export interface AttachedFile {
  path: string; // workspace-relative
  name: string; // filename.ext (display)
}

// The LIVE collection: the visible row, always the active chat's. `bindList` binds the one
// #attachment-row once with no unbind, so per-chat state is saved and restored against it
// (`stash` below), as the editor does for its tabs.
const attached = createCollection<AttachedFile>((a) => a.path);

/**
 * Attachments belonging to chats that are not the active one. A chat with no entry has
 * none, so nothing needs cleaning on delete.
 */
const stash = new Map<string, AttachedFile[]>();

/** The chat the live collection currently belongs to. Written only by
 *  stashAttachments / restoreAttachments, so the two cannot disagree about which
 *  chat the visible pills describe. */
let liveChatID = "";

/**
 * Per-chat attachment generation, bumped by every drop. A send's failure restore captures
 * it at take time, so a close in between cannot be undone by the late restore. Monotonic;
 * absent means 0.
 */
const generations = new Map<string, number>();

/**
 * Chats this device has staged into or seeded, so the server's copy is adopted ONCE and
 * never over a local list. `stash` cannot answer it: the on-screen chat has no entry.
 */
const touched = new Set<string>();

let debouncedSave: DebouncedDispatch<{ chatID: string; paths: string[] }> | null = null;

/**
 * Schedule the persist for `chatID`, or send it now when `now` is set. Built lazily so
 * module load puts no dispatch in a test's import graph.
 */
function persist(chatID: string, paths: string[], now = false): void {
  if (chatID === "") {
    return;
  }
  touched.add(chatID);
  debouncedSave ??= debouncedDispatch(setAttachments, { wait: SAVE_WAIT });
  if (now) {
    void debouncedSave.flush({ chatID, paths });
    return;
  }
  debouncedSave({ chatID, paths });
}

/** Persist the LIVE row under the chat it belongs to. The one-argument case,
 *  because every mutation of the visible row is a mutation of that chat. */
function persistLive(): void {
  persist(
    liveChatID,
    attached.items().map((a) => a.path),
  );
}

/** The chat's current attachment generation, for a caller that will hand it back
 *  to addAttachmentTo after an await. */
export function attachmentGeneration(chatID: string): number {
  return generations.get(chatID) ?? 0;
}

let bound = false;
function ensureBound(): void {
  if (bound) {
    return;
  }
  bound = true;
  const row = $.attachmentRow;
  bindList(row, attached, {
    mount: (att) => buildAttachmentPill(att, { onRemove: removeAttachment }),
  });
  effect(() => {
    row.classList.toggle("hidden", attached.ids.value.length === 0);
  });
}

/** Split a workspace path into the record the pill row renders. */
function toAttached(path: string): AttachedFile {
  const parts = path.split("/");
  return { path, name: parts[parts.length - 1] ?? path };
}

/** Add a file to the ACTIVE chat's attachment list. */
export function addAttachment(path: string): void {
  // Deduplicate by path.
  if (attached.has(path)) {
    return;
  }
  ensureBound();
  attached.upsert(toAttached(path));
  persistLive();
}

/**
 * Add a file to a NAMED chat's list (live if on screen, else its stash) and answer whether
 * it was added. A stale `generation` means the chat was dropped meanwhile; omit it for a
 * first-hand add.
 */
export function addAttachmentTo(chatID: string, path: string, generation?: number): boolean {
  if (generation !== undefined && generation !== attachmentGeneration(chatID)) {
    return false;
  }
  if (chatID === "" || chatID === liveChatID) {
    if (attached.has(path)) {
      return false;
    }
    addAttachment(path);
    return true;
  }
  const held = stash.get(chatID) ?? [];
  if (held.some((a) => a.path === path)) {
    return false;
  }
  const next = [...held, toAttached(path)];
  stash.set(chatID, next);
  persist(
    chatID,
    next.map((a) => a.path),
  );
  return true;
}

/** Remove a file from a NAMED chat's list, live or stashed: the undo of an
 *  `addAttachmentTo` that returned true. */
export function removeAttachmentFrom(chatID: string, path: string): void {
  if (chatID === "" || chatID === liveChatID) {
    if (attached.has(path)) {
      removeAttachment(path);
    }
    return;
  }
  const held = stash.get(chatID) ?? [];
  const next = held.filter((a) => a.path !== path);
  if (next.length === held.length) {
    return;
  }
  if (next.length === 0) {
    stash.delete(chatID);
  } else {
    stash.set(chatID, next);
  }
  persist(
    chatID,
    next.map((a) => a.path),
  );
}

/** Remove an attachment by path. */
function removeAttachment(path: string): void {
  attached.remove(path);
  persistLive();
}

/** Whether the visible row holds a staged attachment; an effect reading it follows staging and
 *  removal. */
export const stagedAttachment: ReadonlySignal<boolean> = computed(
  () => attached.ids.value.length > 0,
);

/** An untracked read of `stagedAttachment`. */
export function hasAttachments(): boolean {
  return stagedAttachment.peek();
}

/** The paths staged while the composer belongs to no chat, which the next
 *  stash would discard. Empty once the row has an owner. */
export function unownedAttachmentPaths(): string[] {
  return liveChatID === "" ? attached.items().map((a) => a.path) : [];
}

/**
 * Take all attachments (clears the list) for the prompt payload. The clear persists
 * IMMEDIATELY: a STEER clears nothing server-side, and a debounced clear would race the
 * send's response.
 */
export function takeAttachments(): AttachedFile[] {
  const out = attached.items();
  attached.clear();
  if (out.length > 0) {
    persist(liveChatID, [], true);
  }
  return out;
}

/**
 * Park the live row's attachments under their chat and empty it. Call before the active
 * chat changes: the outgoing id is unrecoverable afterwards.
 */
export function stashAttachments(): void {
  if (liveChatID !== "") {
    const items = attached.items();
    if (items.length > 0) {
      stash.set(liveChatID, items);
    } else {
      stash.delete(liveChatID);
    }
  }
  // Flush under the OUTGOING id: the debounce would fire against `liveChatID === ""`.
  flushAttachments();
  liveChatID = "";
  attached.clear();
}

/**
 * Send a pending attachment save now (chat switch, tab close, unload); nothing pending
 * means the server already holds the list.
 */
export function flushAttachments(): void {
  if (liveChatID === "" || debouncedSave?.isPending() !== true) {
    return;
  }
  void debouncedSave.flush({
    chatID: liveChatID,
    paths: attached.items().map((a) => a.path),
  });
}

/** Put `chatID`'s attachments on the live row and make it that chat's. */
export function restoreAttachments(chatID: string): void {
  liveChatID = chatID;
  const held = stash.get(chatID);
  attached.clear();
  if (held === undefined || held.length === 0) {
    return;
  }
  stash.delete(chatID);
  ensureBound();
  for (const a of held) {
    attached.upsert(a);
  }
}

/**
 * Forget a closed or deleted chat's parked attachments, bumping the generation so an
 * in-flight send's failure cannot recreate the entry.
 */
export function dropAttachments(chatID: string): void {
  stash.delete(chatID);
  touched.delete(chatID);
  generations.set(chatID, attachmentGeneration(chatID) + 1);
  if (chatID === liveChatID) {
    liveChatID = "";
    attached.clear();
  }
}

/**
 * Adopt the server's stored list for `chatID` once its record loads. It loses to anything
 * local (the fetch can land after staging began), so it only fills the reload case.
 */
export function seedAttachments(chatID: string, paths: readonly string[]): void {
  if (chatID === "" || touched.has(chatID)) {
    return;
  }
  touched.add(chatID);
  if (paths.length === 0) {
    return;
  }
  const held = paths.map(toAttached);
  if (chatID !== liveChatID) {
    stash.set(chatID, held);
    return;
  }
  // Write the live row only when EMPTY: a row the user filled is newer than any fetch.
  if (attached.ids.peek().length > 0) {
    return;
  }
  ensureBound();
  for (const a of held) {
    attached.upsert(a);
  }
}

/**
 * Adopt a `draft_changed` frame's list for a chat this device is NOT staging into; the
 * live row ignores it (a pill mid-gesture). It beats a local copy: the frame is newer.
 */
export function adoptRemoteAttachments(chatID: string, paths: readonly string[]): void {
  if (chatID === "" || chatID === liveChatID) {
    return;
  }
  touched.add(chatID);
  if (paths.length === 0) {
    stash.delete(chatID);
    return;
  }
  stash.set(chatID, paths.map(toAttached));
}

/** Test seam: reset the module between cases. */
export function _resetAttachmentsForTest(): void {
  debouncedSave?.cancel();
  debouncedSave = null;
  stash.clear();
  touched.clear();
  generations.clear();
  liveChatID = "";
  attached.clear();
}
