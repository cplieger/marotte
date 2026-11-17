// scripted-focus.ts — INTERNAL: a focus the library or the app moves draws a ring only when the keyboard led to it.
// Chromium and Firefox keep a focus scripted after a pointer press off `:focus-visible`; WebKit matches it,
// for a script's `focus()` and for the focus `showModal()` and a dialog's close place, so the option is passed
// explicitly (`focusVisible`, HTML "focusing steps"; Firefox 104, Safari 18.4, Chrome 145; an engine that
// ignores it is one whose own heuristic already gives no ring here).

/** The last press was a pointer's (mouse, pen or touch), not a key's. */
let pointerLed = false;

const doc = (globalThis as { readonly document?: Document }).document;
doc?.addEventListener(
  "pointerdown",
  () => {
    pointerLed = true;
  },
  { capture: true, passive: true },
);
doc?.addEventListener(
  "keydown",
  () => {
    pointerLed = false;
  },
  { capture: true, passive: true },
);

// A text field always matches `:focus-visible` (HTML's text-entry rule): its ring shows where typing goes.
const TEXT_ENTRY =
  "textarea, [contenteditable]:not([contenteditable='false']), input:not([type='button'], [type='submit'], " +
  "[type='reset'], [type='checkbox'], [type='radio'], [type='range'], [type='color'], [type='file'], [type='image'])";

/** The options for a scripted `el.focus()`: no ring after a pointer press, the engine's heuristic otherwise. */
export function focusOptions(el: Element): FocusOptions {
  return pointerLed && !el.matches(TEXT_ENTRY) ? { focusVisible: false } : {};
}

/** Take the ring off the focused element when a platform step placed it there after a pointer press. A focused
 *  element's `focus()` is a no-op, so it is blurred and focused again with the option. */
export function quietPlatformFocus(): void {
  const el = document.activeElement;
  if (
    pointerLed &&
    el instanceof HTMLElement &&
    el.matches(":focus-visible") &&
    !el.matches(TEXT_ENTRY)
  ) {
    el.blur();
    el.focus({ focusVisible: false, preventScroll: true });
  }
}

const FOCUSABLE =
  "button:not(:disabled), a[href], input:not(:disabled, [type='hidden']), select:not(:disabled), " +
  "textarea:not(:disabled), summary, [tabindex]:not([tabindex='-1'])";

/** One ancestor of a focused element and the sibling that followed the element's branch in it (null when the
 *  branch was its last child). */
export interface FocusPlace {
  readonly box: Element;
  readonly after: Element | null;
}

/** The places around `el`, nearest first, for `standInFor` once `el` may be gone. */
export function placesOf(el: Element): FocusPlace[] {
  const places: FocusPlace[] = [];
  for (let branch = el; branch.parentElement !== null; branch = branch.parentElement) {
    places.push({ box: branch.parentElement, after: branch.nextElementSibling });
  }
  return places;
}

/** The rendered, enabled control nearest a removed element's place: in the nearest of `places` still in the
 *  document that holds one, the first at or after the sibling that followed it, else the last before it; the
 *  first in that box when that sibling has left it too. */
export function standInFor(places: readonly FocusPlace[]): HTMLElement | null {
  for (const { box, after } of places) {
    if (!box.isConnected) {
      continue;
    }
    const usable = [...box.querySelectorAll<HTMLElement>(FOCUSABLE)].filter(
      (el) => el.closest("[inert]") === null && el.checkVisibility(),
    );
    if (usable.length === 0) {
      continue;
    }
    if (after !== null && after.parentElement !== box) {
      return usable[0] ?? null;
    }
    const at = after === null ? -1 : usable.findIndex((el) => after === el || isBefore(after, el));
    return at === -1 ? (usable.at(-1) ?? null) : (usable[at] ?? null);
  }
  return null;
}

/** `a` precedes `b` in document order or contains it. */
function isBefore(a: Element, b: Element): boolean {
  return (
    (a.compareDocumentPosition(b) &
      (Node.DOCUMENT_POSITION_FOLLOWING | Node.DOCUMENT_POSITION_CONTAINED_BY)) !==
    0
  );
}
