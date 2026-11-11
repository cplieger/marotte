// Who owns a press on Send's or Steer's slot, and for how long: one record and one transition for every input, so a
// click acts once, on the face its press began on.

/** What a press on Send's or Steer's slot does: `send` is the send path, which steers or queues mid-turn. */
export type Act = "send" | "cancel" | "start" | "stop";

interface Pressed {
  readonly btn: HTMLButtonElement;
  readonly act: Act;
}

/** A pointer's record feeds only a pointer's click (detail 1 or more), a key's only a key's (detail 0). */
type Press = Pressed &
  (
    | { readonly by: "pointer"; up: boolean }
    | { readonly by: "key"; readonly key: string; spent: boolean; up: boolean }
  );

/** Every input that begins, feeds or ends a press. `on` is the slot button the event reached, null elsewhere. */
type PressInput =
  | { readonly k: "pointerdown"; readonly on: HTMLButtonElement; readonly e: PointerEvent }
  | { readonly k: "pointerup"; readonly e: PointerEvent }
  | { readonly k: "lostpointercapture" | "pointercancel" }
  | { readonly k: "contextmenu"; readonly e: MouseEvent }
  | { readonly k: "keydown"; readonly on: HTMLButtonElement | null; readonly e: KeyboardEvent }
  | { readonly k: "keyup"; readonly e: KeyboardEvent }
  | { readonly k: "blur"; readonly on: HTMLButtonElement }
  | { readonly k: "windowblur" }
  | { readonly k: "click"; readonly on: HTMLButtonElement; readonly e: MouseEvent };

/** The press owner for a set of slot buttons. `faceOf` is what a press on a button would do on the face it shows
 *  now. Feed it every input; a click's answer is what that click does (null: nothing), any other input's is null. */
export function pressOwner(faceOf: (btn: HTMLButtonElement) => Act): (i: PressInput) => Act | null {
  let press: Press | null = null;
  /** What the first click of a multi-click acted on: the rest of the run acts on it too. */
  let run: Pressed | null = null;
  /** Enter and Space keys that went down on a slot button, until their key up: a held one's repeats are swallowed
   *  wherever focus moved (Stop and Cancel move it into the box), since Enter's would click, send or write a new line
   *  and Space's would type. */
  const held = new Set<string>();

  /** A release off the button makes no click, so its press ends there. */
  const pointerUp = (p: Press & { by: "pointer" }, e: PointerEvent): void => {
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
      press = null;
    }
  };

  /** Fed from the document, so a key press lives from its first key down to its key up wherever its click moved
   *  focus. */
  const keyDown = (on: HTMLButtonElement | null, e: KeyboardEvent): void => {
    if (e.key !== "Enter" && e.key !== " ") {
      return;
    }
    const p = press?.by === "key" ? press : null;
    if (e.repeat) {
      if (held.has(e.key)) {
        // On its own button a Space repeat does nothing, and that button clicks at the key's up.
        if (!(e.key === " " && p?.key === " " && on === p.btn)) {
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
    if (p?.key === e.key) {
      press = null;
    }
    if (on === null) {
      return;
    }
    held.add(e.key);
    if (press?.by === "key") {
      // A second key on a button during the press would click it again.
      e.preventDefault();
      return;
    }
    press = { btn: on, act: faceOf(on), by: "key", key: e.key, spent: false, up: false };
  };

  const keyUp = (e: KeyboardEvent): void => {
    held.delete(e.key);
    const p = press;
    if (p?.by !== "key" || p.key !== e.key) {
      return;
    }
    if (p.spent) {
      press = null;
      return;
    }
    // Space clicks after this key up's listeners, in the same task, and that click ends the press; one whose click
    // never comes (its button disabled meanwhile) ends a task later.
    p.up = true;
    setTimeout(() => {
      if (press === p) {
        press = null;
      }
    });
  };

  /** A repaint between press and click (the engine ending, a result, a send clearing the box) never turns Stop into
   *  Send or Send into Dictate. A click with no live press of its own kind (`click()`, an assistive technology's or
   *  an extension's simulated click) acts on the face it lands on. */
  const click = (on: HTMLButtonElement, e: MouseEvent): Act | null => {
    const live = press;
    const p = live?.btn === on && (live.by === "key") === (e.detail === 0) ? live : null;
    if (p?.by === "key") {
      if (p.spent) {
        return null;
      }
      p.spent = true;
      if (p.up) {
        press = null;
      }
    } else if (p !== null) {
      press = null;
    }
    if (e.detail > 1 && run?.btn === on) {
      return run.act;
    }
    const act = p?.act ?? faceOf(on);
    run = { btn: on, act };
    return act;
  };

  return (i) => {
    switch (i.k) {
      case "pointerdown":
        // A right or middle press makes auxclick or contextmenu, never a click, and one chorded into a held primary
        // press leaves that press's click to come.
        if (i.e.button === 0) {
          press = { btn: i.on, act: faceOf(i.on), by: "pointer", up: false };
        }
        return null;
      case "pointerup":
        if (press?.by === "pointer" && i.e.button === 0) {
          pointerUp(press, i.e);
        }
        return null;
      case "lostpointercapture":
        // A finger's tap loses capture after its release and before its click.
        if (press?.by === "pointer" && !press.up) {
          press = null;
        }
        return null;
      case "pointercancel":
        if (press?.by === "pointer") {
          press = null;
        }
        return null;
      case "contextmenu":
        // A primary press that opens a menu (a Mac Ctrl-click, a long press) makes no click.
        if (press?.by === "pointer" && i.e.button === 0) {
          press = null;
        }
        return null;
      case "keydown":
        keyDown(i.on, i.e);
        return null;
      case "keyup":
        keyUp(i.e);
        return null;
      case "blur":
        // Space clicks only a button that kept focus; the press lives on to swallow its repeats.
        if (press?.by === "key" && press.btn === i.on) {
          press.spent = true;
        }
        return null;
      case "windowblur":
        // The keys' ups go to another window.
        held.clear();
        if (press?.by === "key") {
          press = null;
        }
        return null;
      case "click":
        return click(i.on, i.e);
    }
  };
}
