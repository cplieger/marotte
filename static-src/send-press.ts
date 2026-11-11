// Who owns a press on Send's or Steer's slot, and for how long: one record per press kind and one transition for
// every input, so a click acts once, on the face its press began on.

/** What a press on Send's or Steer's slot does: `send` is the send path, which steers or queues mid-turn. */
export type Act = "send" | "cancel" | "start" | "stop";

interface Pressed {
  readonly btn: HTMLButtonElement;
  readonly act: Act;
}

interface PointerPress extends Pressed {
  up: boolean;
}

interface KeyPress extends Pressed {
  readonly key: string;
  spent: boolean;
  up: boolean;
}

/** Every input that begins, feeds or ends a press. `on` is the slot button the event reached, null elsewhere. */
type PressInput =
  | { readonly k: "pointerdown"; readonly on: HTMLButtonElement; readonly e: PointerEvent }
  | { readonly k: "pointerup"; readonly e: PointerEvent }
  | { readonly k: "lostpointercapture" | "pointercancel" }
  | { readonly k: "contextmenu"; readonly e: MouseEvent }
  | { readonly k: "keydown"; readonly on: HTMLButtonElement | null; readonly e: KeyboardEvent }
  | { readonly k: "keyup"; readonly e: KeyboardEvent }
  | { readonly k: "windowblur" }
  | { readonly k: "click"; readonly on: HTMLButtonElement; readonly e: MouseEvent };

/** The press owner for a set of slot buttons. `faceOf` is what a press on a button would do on the face it shows
 *  now. Feed it every input; a click's answer is what that click does (null: nothing), any other input's is null. */
export function pressOwner(faceOf: (btn: HTMLButtonElement) => Act): (i: PressInput) => Act | null {
  /** A pointer's record feeds only a pointer's click (detail 1 or more), a key's only a key's (detail 0); a press
   *  of one kind never ends or replaces the other's, since both can be down on one button at once. */
  let pointer: PointerPress | null = null;
  let key: KeyPress | null = null;
  /** What the first click of a multi-click acted on: the rest of the run acts on it too. */
  let run: Pressed | null = null;
  /** Enter and Space keys that went down on a slot button, until their key up: a held one's repeats are swallowed
   *  wherever focus moved (Stop and Cancel move it into the box), since Enter's would click, send or write a new line
   *  and Space's would type. */
  const held = new Set<string>();

  /** A release off the button makes no click, so its press ends there. */
  const pointerUp = (p: PointerPress, e: PointerEvent): void => {
    const r = p.btn.getBoundingClientRect();
    if (
      e.clientX >= r.left &&
      e.clientX <= r.right &&
      e.clientY >= r.top &&
      e.clientY <= r.bottom
    ) {
      // Under a finger the click comes in a later task, after the capture is released.
      p.up = true;
    } else {
      pointer = null;
    }
  };

  /** Fed from the document, so a key press lives from its first key down to its key up wherever its click moved
   *  focus. */
  const keyDown = (on: HTMLButtonElement | null, e: KeyboardEvent): void => {
    if (e.key !== "Enter" && e.key !== " ") {
      return;
    }
    if (e.repeat) {
      if (held.has(e.key)) {
        // On its own button a Space repeat does nothing, and that button clicks at the key's up.
        if (!(e.key === " " && key?.key === " " && on === key.btn)) {
          e.preventDefault();
        }
        e.stopImmediatePropagation();
      } else if (on !== null) {
        // A key held down elsewhere never presses this button.
        e.preventDefault();
      }
      return;
    }
    // The same key down again, not a repeat: its key up never came.
    held.delete(e.key);
    if (key?.key === e.key) {
      key = null;
    }
    if (on === null) {
      return;
    }
    held.add(e.key);
    if (key !== null) {
      // A second key on a button during the press would click it again.
      e.preventDefault();
      return;
    }
    key = { btn: on, act: faceOf(on), key: e.key, spent: false, up: false };
  };

  const keyUp = (e: KeyboardEvent): void => {
    held.delete(e.key);
    const p = key;
    if (p?.key !== e.key) {
      return;
    }
    if (p.spent) {
      key = null;
      return;
    }
    // Space clicks after this key up's listeners, in the same task, and that click ends the press; one whose click
    // never comes (its button disabled meanwhile, or focus moved off it) ends a task later.
    p.up = true;
    setTimeout(() => {
      if (key === p) {
        key = null;
      }
    });
  };

  /** A repaint between press and click (the engine ending, a result, a send clearing the box) never turns Stop into
   *  Send or Send into Dictate. A click with no live press of its own kind (`click()`, an assistive technology's or
   *  an extension's simulated click) acts on the face it lands on. */
  const click = (on: HTMLButtonElement, e: MouseEvent): Act | null => {
    let act: Act | null = null;
    if (e.detail === 0) {
      const p = key?.btn === on ? key : null;
      if (p?.spent === true) {
        return null;
      }
      if (p !== null) {
        p.spent = true;
        if (p.up) {
          key = null;
        }
        act = p.act;
      }
    } else if (pointer?.btn === on) {
      act = pointer.act;
      pointer = null;
    }
    if (e.detail > 1 && run?.btn === on) {
      return run.act;
    }
    act ??= faceOf(on);
    run = { btn: on, act };
    return act;
  };

  return (i) => {
    switch (i.k) {
      case "pointerdown":
        // A right or middle press makes auxclick or contextmenu, never a click.
        if (i.e.button === 0) {
          pointer = { btn: i.on, act: faceOf(i.on), up: false };
        }
        return null;
      case "pointerup":
        if (pointer !== null && i.e.button === 0) {
          pointerUp(pointer, i.e);
        }
        return null;
      case "lostpointercapture":
        // A finger's tap loses capture after its release and before its click.
        if (pointer !== null && !pointer.up) {
          pointer = null;
        }
        return null;
      case "pointercancel":
        pointer = null;
        return null;
      case "contextmenu":
        // A menu takes the primary release, so no click comes: a primary press that opens one (a Mac Ctrl-click, a
        // long press), or a right press chorded into it (https://bugs.chromium.org/p/chromium/issues/detail?id=149091).
        if (!i.e.defaultPrevented) {
          pointer = null;
        }
        return null;
      case "keydown":
        keyDown(i.on, i.e);
        return null;
      case "keyup":
        keyUp(i.e);
        return null;
      case "windowblur":
        // The keys' ups go to another window.
        held.clear();
        key = null;
        return null;
      case "click":
        return click(i.on, i.e);
    }
  };
}
