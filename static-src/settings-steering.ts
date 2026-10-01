// ---------------------------------------------------------------------------
// Settings → Instructions: the global-instructions document, auto-saved.
//
// Two entry points with different LIFETIMES: the listeners must exist before the
// panel can be typed into, the read must not — it is a boot-path file read for a
// panel nobody has opened. settings.ts wires them from `initUI` and the tab loader.
//
// THE BOX IS READ-ONLY UNTIL IT HOLDS THE DOCUMENT: a save PUTs `textarea.value` as
// the WHOLE document and the server REMOVES the file when it trims to empty, so a
// keystroke into an unloaded box commits the fragment as the whole thing.
//
// THE READ CARRIES A VALIDATOR AND THE SAVE SENDS IT BACK, because `custom.md` has
// two other writers — a second device and an agent following the generated
// `environment.md` — so a whole-document PUT is last-writer-wins over both.
// ---------------------------------------------------------------------------

import { apiGetWithHeaders } from "./api-client.js";
import { $ } from "./dom.js";
import { showToast } from "./toast.js";
import { showSaving, showSaved, showError, STEERING_SAVE_KEY } from "./save-indicator.js";
import { saveSteering } from "./actions/settings.js";
import {
  registerCleanup,
  debouncedDispatch,
  subscribeByName,
  type DebouncedDispatch,
} from "./actions/index.js";

/** Whether a read is in flight. There is no terminal state: the tab's loader runs
 *  on every activation, and re-seeding on one is the only invalidation this box
 *  has — `_kiro/steering/documents_changed` reaches no handler and no endpoint
 *  carries this file's content on any other channel. */
let reading = false;

/** The validator for the document the box was last seeded from, or "" when the
 *  server answered none. Sent as `If-Match` on every save. */
let etag = "";

/** The text last agreed with the server: what a seed wrote into the box, or what
 *  a save landed. `textarea.value !== seeded` is therefore "the reader has typed
 *  something the server has not got", which is the one state a re-seed may not
 *  overwrite — and it is focus-independent, so the retry below still works on a
 *  box a failed read left locked and empty. */
let seeded = "";

/** Hoisted out of `initSteeringEditor` so the read can ask whether a save is
 *  still owed before it decides to re-seed. */
let debouncedSave: DebouncedDispatch<{ content: string; etag: string }> | null = null;

/** The content a lifecycle event's dispatch carried. `RegistryListener` types its
 *  instance's args as `unknown`, so the shape is narrowed rather than asserted. */
function sentContent(args: unknown): string | null {
  if (typeof args !== "object" || args === null || !("content" in args)) {
    return null;
  }
  const { content } = args;
  return typeof content === "string" ? content : null;
}

/** The validator a successful save answered with, narrowed for the same reason.
 *  An empty string is the server's "the token could not be read back", which is
 *  not a token to adopt — the caller keeps the one it had. */
function freshETag(result: unknown): string | null {
  return typeof result === "string" && result !== "" ? result : null;
}

/** Read the document into its textarea and hand the box over to the reader.
 *  Fired on the Instructions panel's activation via the settings-tabs loader map,
 *  by a focus on a box a failed read left locked, and by the 409 arm, which needs
 *  the document as well as the token. A SUCCESSFUL save reads nothing: its own
 *  200 carries the fresh validator. */
export function loadSteeringDoc(): void {
  if (reading) {
    return;
  }
  reading = true;
  const textarea = $.steeringInput;
  void apiGetWithHeaders<{ content?: string }>("/api/steering").then(({ data, headers }) => {
    reading = false;
    if (typeof data?.content !== "string") {
      // The box stays locked, because an empty box is not an empty document and
      // the save cannot tell them apart. A focus retries.
      showError(STEERING_SAVE_KEY);
      return;
    }
    const fresh = headers?.get("ETag") ?? "";
    const dirty = textarea.value !== seeded || debouncedSave?.isPending() === true;
    if (dirty && data.content !== seeded) {
      // Local edits AND the file moved: adopting either half would decide the
      // conflict silently, so neither is taken and the next save's `If-Match`
      // takes the 409 that tells the reader.
      return;
    }
    etag = fresh;
    if (dirty) {
      return;
    }
    textarea.value = data.content;
    seeded = data.content;
    textarea.readOnly = false;
  });
}

/** Wire the auto-save. Called from `initUI`, before any panel can be opened. */
export function initSteeringEditor(): void {
  const textarea = $.steeringInput;
  // Locked here rather than in the markup so the module that owns the save owns
  // the lock too: `loadSteeringDoc` is the only thing that opens the box.
  textarea.readOnly = true;
  // debouncedDispatch coalesces rapid keystrokes into a single trailing
  // dispatch after the quiet window (replaces the manual clearTimeout +
  // setTimeout(600) + saveGen pattern). saveGen is no longer needed: the
  // action has scope:"settings", so dispatches serialize (ordered
  // resolution), and the indicator is driven by the action's own
  // lifecycle events below rather than a per-dispatch .then().
  const save = debouncedDispatch(saveSteering, { wait: 600 });
  debouncedSave = save;

  const unsub = subscribeByName("settings.save_steering", (inst) => {
    if (inst.status === "success") {
      showSaved(STEERING_SAVE_KEY);
      // What the WRITE carried, never the box's current text: a keystroke landing
      // between the dispatch and its answer would otherwise be recorded as
      // agreed, and the save it is still owed would then take a 409 for a
      // conflict with nobody.
      seeded = sentContent(inst.args) ?? seeded;
      // The write invalidated the validator and its own 200 carries the
      // replacement, so the next save in a debounced run needs no read. Keeping
      // the old token when the server could not read one back is what makes the
      // next save take a 409 — which re-seeds the box — rather than a 428.
      etag = freshETag(inst.result) ?? etag;
      return;
    }
    if (inst.status !== "error") {
      return;
    }
    showError(STEERING_SAVE_KEY);
    if (inst.error?.status === 409) {
      showToast(
        "Your global instructions were not saved — the file changed elsewhere. The box now shows the current version.",
        "error",
      );
      // Force the re-seed: the reader's text lost, and leaving it in the box over
      // a document it does not describe is the worse of the two.
      seeded = textarea.value;
      loadSteeringDoc();
      return;
    }
    if (inst.error?.status === 428) {
      console.error("steering: the save carried no If-Match; the read answered no ETag");
    }
  });

  textarea.addEventListener("input", () => {
    showSaving(STEERING_SAVE_KEY);
    save({ content: textarea.value, etag });
  });

  // The retry for a read that failed: a focus is the reader's own next attempt to
  // type, and it is the one event a read-only textarea still fires.
  textarea.addEventListener("focus", loadSteeringDoc);

  registerCleanup(() => {
    // Stop touching the indicator (mirrors the original cleanup, which
    // flushed without updating it), then flush any pending edit so an
    // unsaved change still persists on teardown.
    unsub();
    if (save.isPending()) {
      void save.flush({ content: textarea.value, etag });
    }
  });
}

/** Drop the read state, so one test's document is not the next test's answer.
 *  Production never needs it: the state's lifetime is the page. */
export function _resetSteeringForTest(): void {
  reading = false;
  etag = "";
  seeded = "";
  debouncedSave = null;
}
