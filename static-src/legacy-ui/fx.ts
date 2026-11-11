// fx.ts — INTERNAL: the effect vocabulary a driver's realization uses. Every
// call that can run code outside the library is one method here, which writes
// only what differs and then checks the pass, so a pass made stale by that
// code restarts before its next effect.

/** The inline style properties realization writes. */
export type StyleProp =
  "height" | "position" | "left" | "right" | "top" | "minWidth" | "width" | "overflow";

/** `showModal`'s outcome, read from the element after the call. */
type ShowResult = { readonly open: true } | { readonly open: false; readonly error: unknown };

type Reflected = "hidden" | "inert" | "id" | "tabIndex";

/** The checked effects of one pass. */
export interface Fx {
  setAttr(el: Element, name: string, value: string): void;
  removeAttr(el: Element, name: string): void;
  /** Adds only the missing tokens, in one write. */
  addClass(el: Element, ...tokens: string[]): void;
  removeClass(el: Element, ...tokens: string[]): void;
  toggleClass(el: Element, token: string, on: boolean): void;
  setProp<E extends HTMLElement, K extends Reflected>(el: E, key: K, value: E[K]): void;
  /** Compared with the value the browser would serialize, so `-0px` reaches a fixed point. */
  setStyle(el: HTMLElement, prop: StyleProp, value: string): void;
  /** Skipped when `child` is already a child of `parent`. */
  append(parent: Node, child: Node): void;
  /** Skipped when detached. */
  remove(node: ChildNode): void;
  /** Skipped when `el` is focused or detached. */
  focus(el: HTMLElement): void;
  /** Skipped unless `el` is focused. */
  blur(el: HTMLElement): void;
  click(el: HTMLElement): void;
  select(input: HTMLInputElement): void;
  /** Skipped when already open. Never throws: a platform throw comes back as `error`. */
  showModal(dialog: HTMLDialogElement): ShowResult;
  /** Skipped when not open. */
  close(dialog: HTMLDialogElement): void;
  /** A user hook or another driver's flush: a throw is collected and yields undefined. */
  call<R>(fn: () => R): R | undefined;
  /** `call` for reading the outside facts a kick reports. The pass marks the kick
   *  count just before `fn` runs, so a kick from inside `fn` makes it stale. */
  measure<R>(fn: () => R): R | undefined;
  /** Collect an error for the drive's edge. */
  report(error: unknown): void;
}

/** What a pass gives its `Fx`. */
interface Pass {
  /** After an external call: throw to restart when the pass went stale. */
  check(): void;
  /** Before a measurement's reads. */
  measured(): void;
  /** Keep a hook's error for the drive's edge, or rethrow it. */
  collect(error: unknown): void;
}

/** Read through a call: TypeScript keeps `dialog.open` narrowed across the platform
 *  call that changes it (microsoft/TypeScript#9998). */
function isOpen(dialog: HTMLDialogElement): boolean {
  return dialog.open;
}

let scratch: HTMLElement | null = null;

/** `value` as the browser serializes it for `prop`. */
function serialized(prop: StyleProp, value: string): string {
  scratch ??= document.createElement("div");
  scratch.style[prop] = value;
  const out = scratch.style[prop];
  scratch.style[prop] = "";
  return out;
}

/** Chromium blurs a focused node before it moves or removes it, and a blur handler
 *  that moves the node, or the reference node, makes the call throw NotFoundError.
 *  The node has blurred by then, so a second try completes, with the reference read
 *  again where the call has one. */
function movedByBlurHandler(error: unknown): boolean {
  return error instanceof DOMException && error.name === "NotFoundError";
}

/** The `Fx` of one pass: every method diffs, performs, then checks `pass`. */
export function fxAt(pass: Pass): Fx {
  const fx: Fx = {
    setAttr(el, name, value) {
      if (el.getAttribute(name) !== value) {
        el.setAttribute(name, value);
        pass.check();
      }
    },
    removeAttr(el, name) {
      if (el.hasAttribute(name)) {
        el.removeAttribute(name);
        pass.check();
      }
    },
    addClass(el, ...tokens) {
      const missing = tokens.filter((t) => !el.classList.contains(t));
      if (missing.length > 0) {
        el.classList.add(...missing);
        pass.check();
      }
    },
    removeClass(el, ...tokens) {
      const present = tokens.filter((t) => el.classList.contains(t));
      if (present.length > 0) {
        el.classList.remove(...present);
        pass.check();
      }
    },
    toggleClass(el, token, on) {
      if (on) {
        fx.addClass(el, token);
      } else {
        fx.removeClass(el, token);
      }
    },
    setProp(el, key, value) {
      if (el[key] !== value) {
        el[key] = value;
        pass.check();
      }
    },
    setStyle(el, prop, value) {
      if (el.style[prop] !== serialized(prop, value)) {
        el.style[prop] = value;
        pass.check();
      }
    },
    append(parent, child) {
      if (child.parentNode !== parent) {
        try {
          parent.appendChild(child);
        } catch (error) {
          if (!movedByBlurHandler(error)) {
            throw error;
          }
          parent.appendChild(child);
        }
        pass.check();
      }
    },
    remove(node) {
      if (node.parentNode !== null) {
        try {
          node.remove();
        } catch (error) {
          if (!movedByBlurHandler(error)) {
            throw error;
          }
          node.remove();
        }
        pass.check();
      }
    },
    focus(el) {
      if (el.isConnected && document.activeElement !== el) {
        el.focus();
        pass.check();
      }
    },
    blur(el) {
      if (document.activeElement === el) {
        el.blur();
        pass.check();
      }
    },
    click(el) {
      el.click();
      pass.check();
    },
    select(input) {
      input.select();
      pass.check();
    },
    showModal(dialog) {
      if (dialog.open) {
        return { open: true };
      }
      let error: unknown = undefined;
      try {
        dialog.showModal();
      } catch (e) {
        error = e;
      }
      pass.check();
      return isOpen(dialog) ? { open: true } : { open: false, error };
    },
    close(dialog) {
      if (dialog.open) {
        dialog.close();
        pass.check();
      }
    },
    call(fn) {
      let out: ReturnType<typeof fn> | undefined;
      try {
        out = fn();
      } catch (error) {
        pass.collect(error);
      }
      pass.check();
      return out;
    },
    measure(fn) {
      pass.measured();
      return fx.call(fn);
    },
    report(error) {
      pass.collect(error);
    },
  };
  return fx;
}

/** The same diffs with no pass: nothing is checked and every throw propagates.
 *  Only the public `placeAnchored` uses it. */
export const directFx: Fx = fxAt({
  check: () => undefined,
  measured: () => undefined,
  collect: (error) => {
    throw error;
  },
});
