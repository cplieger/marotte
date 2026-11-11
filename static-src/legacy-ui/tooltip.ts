/**
 * tooltip.ts — One delegated tooltip controller on `document` for every
 * `data-uip-tooltip` trigger, with native `title` timing by default. A scroll
 * hides a tip rather than moving it. `-anchor` names the ink a tip points at,
 * `-ref` takes the text from a description in the page, and `-toggle` pins it
 * on a tap, since a touch pointer never starts a hover tip.
 * @module
 */

import { el } from "@cplieger/reactive";

import { placeAnchored, type PopoverAnchor } from "./popover.js";
import { cancelTransition, runTransition } from "./transition.js";

/** Tuning for the one delegated controller: the trigger attribute it delegates
 *  on, and the three numbers that decide when a tip appears. The defaults
 *  reproduce native `title` timing, with the warm group inert. */
interface TooltipOptions {
  /** Trigger attribute holding the tooltip text. Default `data-uip-tooltip`.
   *  The MARK attribute below is derived from it, so an app that renames one
   *  renames both. */
  attribute?: string;
  /** Delay (ms) before the first tooltip of a cold group. Default 500, the
   *  hover time a native `title` waits out (Firefox's `ui.tooltipDelay`
   *  default; the Windows mouse-hover time is 400). */
  delayCold?: number;
  /** Delay (ms) before tooltips while the group is warm. Defaults to
   *  `delayCold`, so every hover costs the same wait. Set it lower to opt into
   *  a warm group, where a peer shows faster once one tooltip has. */
  delayWarm?: number;
  /** Warm window (ms) after a tooltip hides. Default 500. Inert while
   *  `delayWarm` equals `delayCold`. */
  cooldown?: number;
}

type TooltipState =
  | { readonly kind: "idle" }
  | {
      readonly kind: "pending";
      readonly anchor: HTMLElement;
      readonly timer: ReturnType<typeof setTimeout>;
    }
  | {
      readonly kind: "visible";
      readonly anchor: HTMLElement;
      readonly tip: HTMLElement;
      /** Opened by a click on a toggle trigger: hover leaving does not close it. */
      readonly pinned: boolean;
      /** The tip's id is in the trigger's aria-describedby (no `-ref`). */
      readonly described: boolean;
    }
  | { readonly kind: "fading"; readonly tip: HTMLElement };

let tipIdSeq = 0;
class TooltipController {
  private state: TooltipState = { kind: "idle" };
  private warmUntil = 0;
  private readonly attribute: string;
  private readonly selector: string;
  private readonly refAttribute: string;
  private readonly toggleAttribute: string;
  private readonly markSelector: string;
  private readonly delayCold: number;
  private readonly delayWarm: number;
  private readonly cooldown: number;

  private readonly onPointerOver = (e: Event): void => {
    // A finger has no hover: a tip from it would cover what it is about to tap.
    if ((e as Partial<PointerEvent>).pointerType === "touch") {
      return;
    }
    this.onEnter(e);
  };
  private readonly onPointerOut = (e: Event): void => {
    if (this.state.kind === "visible" && this.state.pinned) {
      return;
    }
    this.onLeave(e);
  };
  private readonly onClick = (e: Event): void => {
    const anchor = this.closestAnchor(e.target);
    if (anchor?.hasAttribute(this.toggleAttribute) !== true) {
      return;
    }
    const text = this.refText(anchor);
    if (text === null) {
      return;
    }
    if (this.state.kind === "visible" && this.state.pinned && this.state.anchor === anchor) {
      this.hide();
      return;
    }
    this.show(anchor, text, true);
  };
  private readonly onPointerDown = (e: Event): void => {
    if (this.state.kind !== "visible" || !this.state.pinned) {
      return;
    }
    const target = e.target;
    if (
      target instanceof Node &&
      (this.state.anchor.contains(target) || this.state.tip.contains(target))
    ) {
      return;
    }
    this.hide();
  };
  private readonly onFocusIn = (e: Event): void => {
    // :focus-visible gate: a bare focusin also fires on programmatic focus
    // (modal open, focus-trap restore, autofocus), popping tooltips with no
    // hover or keypress.
    if (!isKeyboardFocus(e.target)) {
      return;
    }
    this.onEnter(e);
  };
  private readonly onFocusOut = (e: Event): void => {
    this.onLeave(e);
  };
  private readonly onKeyDown = (e: KeyboardEvent): void => {
    if (e.key === "Escape") {
      this.hide();
    }
  };
  private readonly onScroll = (): void => {
    this.hide();
  };
  private readonly onWindowBlur = (): void => {
    this.hide();
  };

  constructor(opts: TooltipOptions) {
    this.attribute = opts.attribute ?? "data-uip-tooltip";
    this.refAttribute = `${this.attribute}-ref`;
    this.toggleAttribute = `${this.attribute}-toggle`;
    this.selector = `[${this.attribute}], [${this.refAttribute}]`;
    // Derived rather than a second option: one trigger vocabulary, so an app
    // renaming the text attribute cannot end up with a mark attribute in the
    // library's namespace and a trigger attribute in its own.
    this.markSelector = `[${this.attribute}-anchor]`;
    this.delayCold = opts.delayCold ?? 500;
    // Defaults to delayCold, not a constant, so a partial override can't
    // produce a warm path faster than the delay the caller asked for.
    this.delayWarm = opts.delayWarm ?? this.delayCold;
    this.cooldown = opts.cooldown ?? 500;
  }

  init(): void {
    document.addEventListener("pointerover", this.onPointerOver);
    document.addEventListener("pointerout", this.onPointerOut);
    document.addEventListener("focusin", this.onFocusIn);
    document.addEventListener("focusout", this.onFocusOut);
    document.addEventListener("keydown", this.onKeyDown);
    document.addEventListener("scroll", this.onScroll, true);
    document.addEventListener("click", this.onClick);
    document.addEventListener("pointerdown", this.onPointerDown, true);
    window.addEventListener("blur", this.onWindowBlur);
  }

  dispose(): void {
    document.removeEventListener("pointerover", this.onPointerOver);
    document.removeEventListener("pointerout", this.onPointerOut);
    document.removeEventListener("focusin", this.onFocusIn);
    document.removeEventListener("focusout", this.onFocusOut);
    document.removeEventListener("keydown", this.onKeyDown);
    document.removeEventListener("scroll", this.onScroll, true);
    document.removeEventListener("click", this.onClick);
    document.removeEventListener("pointerdown", this.onPointerDown, true);
    window.removeEventListener("blur", this.onWindowBlur);
    this.teardown();
  }

  private closestAnchor(target: EventTarget | null): HTMLElement | null {
    if (!(target instanceof Element)) {
      return null;
    }
    const anchor = target.closest(this.selector);
    return anchor instanceof HTMLElement ? anchor : null;
  }

  /** The text a `-ref` trigger names, or null for a trigger without one or a
   *  reference to nothing or to an empty element. */
  private refText(anchor: HTMLElement): string | null {
    const id = anchor.getAttribute(this.refAttribute);
    if (id === null) {
      return null;
    }
    const root = anchor.getRootNode();
    const target =
      root instanceof Document || root instanceof ShadowRoot ? root.getElementById(id) : null;
    const text = target?.textContent ?? "";
    return text === "" ? null : text;
  }

  private onEnter(e: Event): void {
    const anchor = this.closestAnchor(e.target);
    if (anchor === null) {
      return;
    }
    const text = anchor.hasAttribute(this.refAttribute)
      ? (this.refText(anchor) ?? "")
      : (anchor.getAttribute(this.attribute) ?? "");
    if (text === "") {
      return;
    }
    if (
      (this.state.kind === "pending" || this.state.kind === "visible") &&
      this.state.anchor === anchor
    ) {
      return;
    }
    this.teardown();
    const delay = Date.now() < this.warmUntil ? this.delayWarm : this.delayCold;
    const timer = setTimeout(() => {
      this.show(anchor, text);
    }, delay);
    this.state = { kind: "pending", anchor, timer };
  }

  private onLeave(e: Event): void {
    const anchor = this.closestAnchor(e.target);
    if (anchor === null) {
      return;
    }
    if (this.state.kind === "idle" || this.state.kind === "fading") {
      return;
    }
    if (this.state.anchor !== anchor) {
      return;
    }
    const related = (e as { relatedTarget?: EventTarget | null }).relatedTarget ?? null;
    if (related instanceof Node && anchor.contains(related)) {
      return;
    }
    if (
      this.state.kind === "visible" &&
      this.state.pinned &&
      related instanceof Node &&
      this.state.tip.contains(related)
    ) {
      return;
    }
    this.hide();
  }

  private show(anchor: HTMLElement, text: string, pinned = false): void {
    if (!anchor.isConnected) {
      this.state = { kind: "idle" };
      return;
    }
    this.teardown();
    // A referenced text is already the trigger's description: the painted
    // copy stays out of the tree, and the host's aria-describedby is left as set.
    const described = !anchor.hasAttribute(this.refAttribute);

    const tipId = `uip-tip-${(++tipIdSeq).toString()}`;
    const tip = el("div", { className: "uip-tooltip", role: "tooltip", id: tipId });
    tip.append(...renderLines(text));
    // Shares the dialog's top-layer context; body-appended would paint behind a modal.
    const host: Element = anchor.closest("dialog[open]") ?? document.body;
    if (!described) {
      tip.setAttribute("aria-hidden", "true");
    }
    host.appendChild(tip);
    if (described) {
      addDescribedBy(anchor, tipId);
    }

    this.position(anchor, tip);

    this.state = { kind: "visible", anchor, tip, pinned, described };
    // Keep the group warm long enough to cover a cold-delay hover of a peer.
    this.warmUntil = Date.now() + this.cooldown + this.delayCold;
  }

  private position(anchor: HTMLElement, tip: HTMLElement): void {
    placeAnchored(tip, this.positionAnchor(anchor), {
      placement: "top",
      align: "center",
      offset: 6,
      margin: 4,
    });
  }

  /** What the tip is placed against: the trigger's marked ink intersected with
   *  the trigger's rect, so clipped ink (an ellipsised name, a mark scrolled
   *  out of an overflow-hidden trigger) cannot drag the tip off the control.
   *  No mark, or an empty intersection, falls back to the trigger. */
  private positionAnchor(anchor: HTMLElement): PopoverAnchor {
    const mark = this.markInside(anchor);
    if (mark === null) {
      return anchor;
    }
    const a = anchor.getBoundingClientRect();
    const m = mark.getBoundingClientRect();
    const left = Math.max(a.left, m.left);
    const top = Math.max(a.top, m.top);
    const width = Math.min(a.right, m.right) - left;
    const height = Math.min(a.bottom, m.bottom) - top;
    if (width <= 0 || height <= 0) {
      return anchor;
    }
    const rect = new DOMRect(left, top, width, height);
    return { getBoundingClientRect: (): DOMRect => rect };
  }

  /** The trigger's own marked ink: the first mark in document order that belongs
   *  to THIS trigger and has a box. A mark inside a nested trigger is that
   *  trigger's, and one that renders nothing (a glyph slot with no content yet, a
   *  `display: none` mark) is not ink to point at. */
  private markInside(anchor: HTMLElement): HTMLElement | null {
    for (const mark of anchor.querySelectorAll<HTMLElement>(this.markSelector)) {
      if (mark.closest(this.selector) !== anchor) {
        continue;
      }
      const r = mark.getBoundingClientRect();
      if (r.width > 0 && r.height > 0) {
        return mark;
      }
    }
    return null;
  }

  private hide(): void {
    if (this.state.kind === "pending") {
      clearTimeout(this.state.timer);
      this.state = { kind: "idle" };
      this.warmUntil = Date.now() + this.cooldown;
      return;
    }
    if (this.state.kind !== "visible") {
      return;
    }
    const { anchor, tip, described } = this.state;
    if (described) {
      removeDescribedBy(anchor, tip.id);
    }
    this.state = { kind: "fading", tip };
    this.warmUntil = Date.now() + this.cooldown;

    runTransition(tip, {
      change: () => {
        tip.classList.add("is-leaving");
      },
      settled: () => {
        this.state = { kind: "idle" };
        tip.remove();
      },
    });
  }

  private teardown(): void {
    switch (this.state.kind) {
      case "pending":
        clearTimeout(this.state.timer);
        break;
      case "visible":
        if (this.state.described) {
          removeDescribedBy(this.state.anchor, this.state.tip.id);
        }
        this.state.tip.remove();
        break;
      case "fading":
        cancelTransition(this.state.tip);
        this.state.tip.remove();
        break;
      case "idle":
        break;
    }
    this.state = { kind: "idle" };
  }
}

/** Append `id` to the anchor's `aria-describedby` token list, preserving any
 *  tokens the app already set. */
function addDescribedBy(anchor: HTMLElement, id: string): void {
  const current = anchor.getAttribute("aria-describedby");
  const tokens = current === null ? [] : current.split(/\s+/).filter((t) => t !== "");
  if (!tokens.includes(id)) {
    tokens.push(id);
  }
  anchor.setAttribute("aria-describedby", tokens.join(" "));
}

/** Remove only `id` from the anchor's `aria-describedby`, restoring the rest.
 *  Removes the attribute entirely only when nothing else remains. */
function removeDescribedBy(anchor: HTMLElement, id: string): void {
  const current = anchor.getAttribute("aria-describedby");
  if (current === null) {
    return;
  }
  const tokens = current.split(/\s+/).filter((t) => t !== "" && t !== id);
  if (tokens.length > 0) {
    anchor.setAttribute("aria-describedby", tokens.join(" "));
  } else {
    anchor.removeAttribute("aria-describedby");
  }
}

/** True when the focus landing on `target` should count as keyboard-driven:
 *  the element matches `:focus-visible`. Engines without the selector (or a
 *  non-Element target) fall back to `true`, preserving the old show-on-focus
 *  behavior rather than silently disabling focus tooltips. */
function isKeyboardFocus(target: EventTarget | null): boolean {
  if (!(target instanceof Element)) {
    return false;
  }
  try {
    return target.matches(":focus-visible");
  } catch {
    return true;
  }
}

/** Split tooltip text on newlines into text nodes separated by <br>. */
function renderLines(text: string): (string | Node)[] {
  const lines = text.split("\n");
  const children: (string | Node)[] = [];
  for (let i = 0; i < lines.length; i++) {
    if (i > 0) {
      children.push(el("br"));
    }
    children.push(lines[i] ?? "");
  }
  return children;
}

let controller: TooltipController | null = null;

/** Install the delegated tooltip controller once. Idempotent — later calls
 *  (including with different options) are no-ops until reset. */
export function initTooltips(opts?: TooltipOptions): void {
  if (controller !== null) {
    return;
  }
  controller = new TooltipController(opts ?? {});
  controller.init();
}

/** Test-only: remove the controller's listeners and any live tooltip. */
// deadset:ignore DS1102 -- test seam: disposes the shared tooltip controller
export function _resetForTest(): void {
  if (controller !== null) {
    controller.dispose();
    controller = null;
  }
}
