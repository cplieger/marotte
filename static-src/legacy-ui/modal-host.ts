// showModal() makes everything outside the topmost modal dialog's SUBTREE
// inert regardless of stacking order, so chrome painted above it (the toast
// stack, a popup panel) is still dead to clicks and hover unless re-hosted inside it.

/** When `open` was last added to each dialog the observer saw. The platform exposes
 *  no top-layer order, and document order is not it (a dialog built early and
 *  opened last is on top), but showModal() adds `open` as the dialog enters the top
 *  layer, and one observer delivers its records in the order they were made. A
 *  dialog with no entry is one whose opening no observer saw: it ranks below every
 *  stamped one. */
const openedAt = new WeakMap<HTMLDialogElement, number>();
let clock = 0;

function rank(records: readonly MutationRecord[]): void {
  let reopened = false;
  for (const r of records) {
    if (r.oldValue === null && r.target instanceof HTMLDialogElement) {
      openedAt.set(r.target, ++clock);
      observeRoot(r.target);
      // A close and showModal() in one record batch leave the dialog :modal, so
      // leftCovering() cannot see the close.
      reopened ||= covering.has(r.target);
      if (pendingClear !== null && isModal(r.target)) {
        cover(r.target);
      }
    }
  }
  if (reopened || leftCovering()) {
    uncover();
  }
}

// Observing a node again replaces its options, so OPEN_RECORDS must cover OWN_OPEN:
// a tracked dialog can also be the root of its tree.
const OWN_OPEN: MutationObserverInit = {
  attributes: true,
  attributeFilter: ["open"],
  attributeOldValue: true,
};
const OPEN_RECORDS: MutationObserverInit = { ...OWN_OPEN, subtree: true };

// Started at load, an import-time effect by design: an opening made before it
// is lost, since no platform API reports it later.
const additions = new MutationObserver(rank);
additions.observe(document, OPEN_RECORDS);

/** The open modals whose closing, reopening or removal uncovers what they hide, while
 *  `pendingClear` waits for that. */
const covering = new Set<HTMLDialogElement>();
let pendingClear: (() => void) | null = null;
// A modal removed from its tree leaves the top layer with no `open` record (HTML, the
// dialog removing steps), so the parent of each node on its path up to the document is
// watched. moveBefore() changes that path and keeps the modal in the top layer (DOM, the
// move algorithm), and its record lands on the old path, so each record walks it again.
const removals = new MutationObserver(() => {
  if (leftCovering()) {
    uncover();
    return;
  }
  for (const dialog of covering) {
    cover(dialog);
  }
});

function cover(dialog: HTMLDialogElement): void {
  covering.add(dialog);
  let node: Node = dialog;
  for (let parent = node.parentNode; parent !== null; parent = node.parentNode) {
    removals.observe(parent, { childList: true });
    node = parent instanceof ShadowRoot ? parent.host : parent;
  }
}

function leftCovering(): boolean {
  for (const dialog of covering) {
    if (!isModal(dialog)) {
      return true;
    }
  }
  return false;
}

function uncover(): void {
  const clear = pendingClear;
  pendingClear = null;
  covering.clear();
  removals.disconnect();
  clear?.();
}

/** The observed shadow roots, whose open dialogs `document` queries miss. */
const shadowRoots = new Set<WeakRef<ShadowRoot>>();
const observed = new WeakSet<Node>();
/** The dialogs trackOpens tracks, wherever they are now. */
const tracked = new Set<WeakRef<HTMLDialogElement>>();
const isTracked = new WeakSet<HTMLDialogElement>();

/** Each target still alive, dropping the collected ones. */
function* alive<T extends object>(refs: Set<WeakRef<T>>): Generator<T> {
  for (const ref of refs) {
    const target = ref.deref();
    if (target === undefined) {
      refs.delete(ref);
    } else {
      yield target;
    }
  }
}

/** Observe the tree `dialog` is in now when that is not the document (a shadow root,
 *  or a detached subtree), which the document's registration does not reach. */
function observeRoot(dialog: HTMLDialogElement): void {
  const root = dialog.getRootNode();
  if (root === document || observed.has(root)) {
    return;
  }
  observed.add(root);
  additions.observe(root, OPEN_RECORDS);
  if (root instanceof ShadowRoot) {
    shadowRoots.add(new WeakRef(root));
  }
}

/** Rank every opening of `dialog` from now on, wherever it moves, count it among the
 *  open dialogs chrome can host into, and rank the later openings of any dialog in
 *  the tree it is in now. Idempotent. */
export function trackOpens(dialog: HTMLDialogElement): void {
  if (!isTracked.has(dialog)) {
    isTracked.add(dialog);
    tracked.add(new WeakRef(dialog));
    // An element's own registration follows it into any tree. A dialog observed as a
    // root already has the options that cover it.
    if (!observed.has(dialog)) {
      additions.observe(dialog, OWN_OPEN);
    }
  }
  observeRoot(dialog);
}

function isModal(dialog: HTMLDialogElement): boolean {
  return dialog.matches(":modal");
}

/** Whether `x` opened after `y`; of two whose openings no observer saw, the later in
 *  tree order (a fixed, arbitrary order across trees). */
function openedAfter(x: HTMLDialogElement, y: HTMLDialogElement): boolean {
  const ax = openedAt.get(x) ?? 0;
  const ay = openedAt.get(y) ?? 0;
  return ax !== ay
    ? ax > ay
    : (y.compareDocumentPosition(x) & Node.DOCUMENT_POSITION_FOLLOWING) !== 0;
}

/** The newest-opened of `dialogs`, or `null` for none. */
function newest(dialogs: readonly HTMLDialogElement[]): HTMLDialogElement | null {
  let top: HTMLDialogElement | null = null;
  for (const dialog of dialogs) {
    if (top === null || openedAfter(dialog, top)) {
      top = dialog;
    }
  }
  return top;
}

/** Every open, connected dialog the library knows of: the document's, those of the
 *  observed shadow roots, and the tracked ones wherever they are. */
function knownOpen(): HTMLDialogElement[] {
  const open = new Set(document.querySelectorAll<HTMLDialogElement>("dialog[open]"));
  for (const root of alive(shadowRoots)) {
    if (root.host.isConnected) {
      for (const dialog of root.querySelectorAll<HTMLDialogElement>("dialog[open]")) {
        open.add(dialog);
      }
    }
  }
  for (const dialog of alive(tracked)) {
    if (dialog.open && dialog.isConnected) {
      open.add(dialog);
    }
  }
  return [...open];
}

/** The open `<dialog>` chrome should host into, or `null` when none is open: of
 *  every open dialog the library knows of, the `:modal` one that opened last, the
 *  one subtree showModal() leaves live; else, when none is `:modal`, the open one
 *  that opened last. */
export function topmostOpenDialog(): HTMLDialogElement | null {
  rank(additions.takeRecords());
  const open = knownOpen();
  return newest(open.filter(isModal)) ?? newest(open);
}

/** Every dialog trackOpens tracks that is showing as a modal now. */
export function trackedModals(): HTMLDialogElement[] {
  return [...alive(tracked)].filter(isModal);
}

/** Call `clear` once when a modal open now, or opened later, closes, opens again or leaves
 *  its tree, before the browser exposes what that modal hid; a later call replaces `clear`.
 *  A modal no observer sees, one the helpers do not drive in a shadow root they never
 *  reached, is not watched. */
export function whenUncovered(clear: () => void): void {
  // Called in the change's own task (as under fake timers), records not yet delivered
  // belong to the callback this one replaces.
  rank(additions.takeRecords());
  pendingClear = clear;
  for (const dialog of knownOpen()) {
    if (isModal(dialog)) {
      cover(dialog);
    }
  }
}
