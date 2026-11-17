// WebKit matches `:focus-visible` after a pointer press for the focus `showModal()` places, the focus a dialog's
// close restores, and a script's `focus()` from <body> right after a close, so the option is passed explicitly:
// https://html.spec.whatwg.org/multipage/interaction.html#focusing-steps (`focusVisible`); support:
// https://developer.mozilla.org/en-US/docs/Web/API/HTMLElement/focus#browser_compatibility (Chrome 145, Firefox 104,
// Safari 18.4). Chrome 132-144 ignore it: their own heuristic still draws no ring after a pointer press, but the
// keyboard ring forced on a focus returned after a pointer close is not drawn.

/** A press's kind: a pointer's (mouse, pen or touch) or a key's. */
export type Press = "pointer" | "key";
let last: Press | null = null;

const doc = (globalThis as { readonly document?: Document }).document;
doc?.addEventListener(
  "pointerdown",
  () => {
    last = "pointer";
  },
  { capture: true, passive: true },
);
doc?.addEventListener(
  "keydown",
  () => {
    last = "key";
  },
  { capture: true, passive: true },
);

// A text field always matches `:focus-visible` (HTML's text-entry rule): its ring shows where typing goes.
const TEXT_ENTRY =
  "textarea, [contenteditable]:not([contenteditable='false']), input:not([type='button'], [type='submit'], " +
  "[type='reset'], [type='checkbox'], [type='radio'], [type='range'], [type='color'], [type='file'], [type='image'])";

export function lastPress(): Press | null {
  return last;
}

/** The options for a scripted `el.focus()`: no ring after a pointer press, the engine's heuristic otherwise. */
export function focusOptions(el: Element): FocusOptions {
  return last === "pointer" && !el.matches(TEXT_ENTRY) ? { focusVisible: false } : {};
}

/** Give the focused control the ring of `press`, the press that opened what put focus there: none for a pointer's,
 *  the ring for a key's, whichever input closed it. A focused element's `focus()` is a no-op, so it is blurred and
 *  focused again with the option. */
export function matchPlatformFocus(press: Press): void {
  const el = document.activeElement;
  const ring = press === "key";
  if (
    el instanceof HTMLElement &&
    el !== document.body &&
    !el.matches(TEXT_ENTRY) &&
    el.matches(":focus-visible") !== ring
  ) {
    el.blur();
    el.focus({ focusVisible: ring, preventScroll: true });
  }
}

/** Focus `el` with the ring of `press` (`matchPlatformFocus`), or the engine's heuristic when `press` is unset. */
export function focusAs(el: HTMLElement, press?: Press | null): void {
  if (press === undefined || press === null) {
    el.focus();
    return;
  }
  el.focus(el.matches(TEXT_ENTRY) ? {} : { focusVisible: press === "key" });
  if (document.activeElement === el) {
    matchPlatformFocus(press);
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
