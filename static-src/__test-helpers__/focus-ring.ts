// Scenes for the focus-ring suites: the transcript scroller as index.html marks it up, and dialogs opened from a
// control the way the app opens them.
import { confirm } from "../confirm.js";
import { createModal, type ModalController } from "../legacy-ui/modal.js";
import indexHtml from "../../static/index.html?raw";

/** The painted outline: `"none"`, or its computed style and width. */
export function outlineOf(el: Element): string {
  const cs = getComputedStyle(el);
  // Chromium reports the declared width of an outline whose style is none, which paints nothing.
  return cs.outlineStyle === "none" ? "none" : `${cs.outlineStyle} ${cs.outlineWidth}`;
}

/** The ring `--focus-ring` paints (css/01-tokens.css). */
export const RING = "solid 2px";

const mounted: Element[] = [];
const modals: ModalController[] = [];

/** Remove every scene this module mounted and close any dialog still open. */
export function unmountScenes(): void {
  for (const m of modals.splice(0)) {
    m.dispose();
  }
  for (const d of document.querySelectorAll<HTMLDialogElement>("dialog[open]")) {
    d.close();
  }
  for (const n of mounted.splice(0)) {
    n.remove();
  }
}

/** `#messages-wrap` with the attributes index.html gives it, inside its outer box, filled past its height. */
export function mountTranscript(): HTMLElement {
  const markup = new DOMParser()
    .parseFromString(indexHtml, "text/html")
    .getElementById("messages-wrap");
  if (markup === null) {
    throw new Error("index.html has no #messages-wrap");
  }
  const outer = document.createElement("div");
  outer.id = "messages-wrap-outer";
  outer.style.cssText = "position:fixed;top:60px;left:20px;width:320px;height:240px;";
  const pane = document.createElement("div");
  for (const a of markup.attributes) {
    pane.setAttribute(a.name, a.value);
  }
  for (let i = 0; i < 40; i++) {
    const p = document.createElement("p");
    p.textContent = `Message ${String(i)}`;
    pane.append(p);
  }
  outer.append(pane);
  document.body.append(outer);
  mounted.push(outer);
  return pane;
}

/** A text field standing in for the composer. */
export function mountComposer(): HTMLTextAreaElement {
  const field = document.createElement("textarea");
  field.id = "focus-ring-composer";
  field.style.cssText = "position:fixed;top:320px;left:20px;width:320px;height:48px;";
  document.body.append(field);
  mounted.push(field);
  return field;
}

function opener(id: string, label: string, top: number, onClick: () => void): HTMLButtonElement {
  const b = document.createElement("button");
  b.type = "button";
  b.id = id;
  b.textContent = label;
  b.style.cssText = `position:fixed;top:${String(top)}px;left:380px;width:120px;height:32px;`;
  b.addEventListener("click", onClick);
  document.body.append(b);
  mounted.push(b);
  return b;
}

/** A turn footer's Rewind button: its click opens the destructive confirm the Rewind action asks. */
export function mountRewindButton(): HTMLButtonElement {
  return opener("focus-ring-rewind", "Rewind", 60, () => {
    void confirm({
      title: "Rewind to this prompt?",
      body: "Removes this prompt and its reply.",
      confirmLabel: "Rewind",
      variant: "destructive",
    });
  });
}

/** A button opening a `createModal` dialog that leaves its first focus to the platform. */
export function mountModalButton(): HTMLButtonElement {
  const content = document.createElement("div");
  const title = document.createElement("span");
  title.className = "modal-title";
  title.id = "focus-ring-modal-title";
  title.textContent = "Import memories";
  const first = document.createElement("button");
  first.type = "button";
  first.className = "btn";
  first.textContent = "Close";
  const second = document.createElement("button");
  second.type = "button";
  second.className = "btn";
  second.textContent = "Import";
  content.append(title, first, second);
  const modal = createModal(content);
  modals.push(modal);
  return opener("focus-ring-modal", "Import", 110, () => {
    modal.open();
  });
}

/** The open confirm's dialog, once its opening click has run. */
export function openConfirm(): HTMLDialogElement {
  const d = document.querySelector<HTMLDialogElement>("dialog.uip-ask[open]");
  if (d === null) {
    throw new Error("no confirm is open");
  }
  return d;
}

/** The open `createModal` dialog. */
export function openModal(): HTMLDialogElement {
  const d = document.querySelector<HTMLDialogElement>("dialog.uip-modal[open]");
  if (d === null) {
    throw new Error("no modal is open");
  }
  return d;
}

/** The focused element, which must sit inside `within`. */
export function focusedIn(within: Element): HTMLElement {
  const a = document.activeElement;
  if (!(a instanceof HTMLElement) || !within.contains(a)) {
    throw new Error(`focus is on ${a?.tagName ?? "nothing"}, outside the dialog`);
  }
  return a;
}
