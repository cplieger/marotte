/**
 * disclosure.ts — Animated collapsible (show/hide) region wired to a trigger,
 * per the WAI-ARIA disclosure pattern. Headless: it wires two elements you
 * supply — no DOM is created. The trigger gets button semantics
 * (`aria-expanded`, Enter/Space when it isn't a native `<button>`) and is linked
 * to the region via `aria-controls`; the region toggles `aria-hidden`.
 *
 * Height animates 0 <-> auto. Modern engines interpolate the `auto` keyword
 * directly (`interpolate-size: allow-keywords`, set on the region in the base
 * stylesheet); engines without it fall back to a measured `scrollHeight` px
 * target. Both honor `prefers-reduced-motion` by skipping the tween.
 *
 * @module
 */

import { untracked } from "@cplieger/reactive";

import type { Fx } from "./fx.js";
import { createDriver, ownedBinding } from "./owner.js";
import { watchSettle } from "./transition.js";

/** The initial open state, whether height changes tween, and the toggle hook.
 *  All optional: a disclosure with no options starts collapsed, animates, and
 *  reports nothing. */
interface DisclosureOptions {
  /** Initial open state. Default `false`. */
  open?: boolean;
  /** Animate height changes. Default `true`. Ignored under reduced motion. */
  animate?: boolean;
  /** Invoked whenever the open state changes. `source` distinguishes a
   *  user-initiated toggle (trigger click / Enter / Space) from a programmatic
   *  one (`open()` / `close()` / `toggle()` on the controller) — the seam an
   *  auto-collapse state machine needs to latch "the user took over". */
  onToggle?: (open: boolean, source: DisclosureToggleSource) => void;
}

/** How a disclosure toggle was initiated: `"user"` = the wired trigger
 *  (click / Enter / Space); `"api"` = a controller method. */
type DisclosureToggleSource = "user" | "api";

/** Handle on a wired disclosure. Every method here reports as the `"api"`
 *  toggle source, so an `onToggle` consumer can tell a controller call from a
 *  trigger press. The only state driver in region-only mode (`trigger: null`). */
export interface DisclosureController {
  /** Expand the region. Does nothing once `dispose()` has begun, as do
   *  `close()` and `toggle()`. */
  open(): void;
  close(): void;
  toggle(): void;
  readonly isOpen: boolean;
  /** Unwire the trigger. An in-flight tween is cancelled and the height
   *  settled (`auto` when open, `0` when collapsed) so no inline px height is
   *  left frozen; the region keeps its current state, `aria-hidden` and
   *  `inert` included. */
  dispose(): void;
}

let idSeq = 0;

function prefersReducedMotion(): boolean {
  return (
    typeof window !== "undefined" &&
    typeof window.matchMedia === "function" &&
    window.matchMedia("(prefers-reduced-motion: reduce)").matches
  );
}

/** Whether the engine can transition `height` to/from the `auto` keyword. */
function supportsInterpolateSize(): boolean {
  return (
    typeof CSS !== "undefined" &&
    typeof CSS.supports === "function" &&
    CSS.supports("interpolate-size", "allow-keywords")
  );
}

/** Whether the page is rendering `el` at all. Measuring a region it does not
 *  render to animate it is a forced render for a transition that cannot run.
 *  No options object: `contentVisibilityAuto` would also catch a SKIPPED
 *  `content-visibility: auto` region, which the next scroll makes relevant again. */
function isRendered(el: HTMLElement): boolean {
  return typeof el.checkVisibility !== "function" || el.checkVisibility();
}

function cancelHeightTransition(el: HTMLElement): void {
  for (const a of el.getAnimations()) {
    if (a instanceof CSSTransition && a.transitionProperty === "height") {
      a.cancel();
    }
  }
}

/** Wire `trigger` (a button, or any element given button semantics) to `region`
 *  as an animated disclosure. With `trigger: null` nothing is wired (no
 *  aria-expanded, no click or keyboard handling) and only the controller
 *  changes the open state; the region still animates and gets `aria-hidden`
 *  plus `inert`. */
export function createDisclosure(
  trigger: HTMLElement | null,
  region: HTMLElement,
  opts?: DisclosureOptions,
): DisclosureController {
  const o = untracked(() => ({
    animate: opts?.animate ?? true,
    onToggle: opts?.onToggle,
    open: opts?.open ?? false,
  }));

  interface State {
    readonly open: boolean;
    readonly source: DisclosureToggleSource;
    /** The id of the open tween whose settle has fired. */
    readonly settled: number;
  }

  return ownedBinding((binding) => {
    const regionId = region.id === "" ? `uip-disclosure-${(++idSeq).toString()}` : region.id;
    const isNativeButton = trigger instanceof HTMLButtonElement;
    let listening = false;
    let reported = o.open;
    let tweenSeq = 0;
    // The height is a sequence: `shown` is the state it was last driven
    // toward, and `tween.step` advances after each write, so a restart resumes
    // the step that did not finish.
    const height: {
      shown: boolean;
      tween: {
        readonly id: number;
        readonly toOpen: boolean;
        step: "from" | "to" | "armed";
      } | null;
      watch: (() => void) | null;
    } = { shown: o.open, tween: null, watch: null };

    const toggleTo = (next: boolean, source: DisclosureToggleSource): void => {
      if (binding.ended) {
        return;
      }
      driver.commit(
        driver.state.open === next ? driver.state : { ...driver.state, open: next, source },
      );
    };
    const onClick = (): void => {
      toggleTo(!driver.state.open, "user");
    };
    const onKeyDown = (e: KeyboardEvent): void => {
      if (e.key === "Enter" || e.key === " " || e.key === "Spacebar") {
        // Space would scroll the page; Enter/Space both activate a button.
        e.preventDefault();
        toggleTo(!driver.state.open, "user");
      }
    };
    const listen = (on: boolean): void => {
      if (trigger === null || listening === on) {
        return;
      }
      listening = on;
      const method = on ? "addEventListener" : "removeEventListener";
      trigger[method]("click", onClick);
      if (!isNativeButton) {
        trigger[method]("keydown", onKeyDown as EventListener);
      }
    };
    const cancelWatch = (): void => {
      height.watch?.();
      height.watch = null;
    };
    const settle = (id: number): void => {
      driver.commit({ ...driver.state, settled: id });
    };

    const driver = createDriver<State>({
      initial: { open: o.open, source: "api", settled: 0 },
      realize(fx, d) {
        listen(true);
        fx.addClass(region, "uip-disclosure-region");
        if (region.id === "") {
          fx.setProp(region, "id", regionId);
        }
        if (trigger !== null) {
          fx.setAttr(trigger, "aria-controls", region.id);
          if (!isNativeButton) {
            if (!trigger.hasAttribute("role")) {
              fx.setAttr(trigger, "role", "button");
            }
            if (!trigger.hasAttribute("tabindex")) {
              fx.setAttr(trigger, "tabindex", "0");
            }
          }
        }
        reflect(fx, d.open);
        if (height.shown !== d.open) {
          cancelWatch();
          height.shown = d.open;
          const animated = o.animate && !prefersReducedMotion() && isRendered(region);
          height.tween = animated ? { id: ++tweenSeq, toOpen: d.open, step: "from" } : null;
        }
        const tween = height.tween;
        if (tween?.step === "from") {
          // auto is not an animatable start on the fallback path, so measure first.
          fx.setStyle(
            region,
            "height",
            tween.toOpen ? "0px" : `${region.scrollHeight.toString()}px`,
          );
          tween.step = "to";
        }
        if (tween?.step === "to") {
          // The start height is committed before the target, or both land in one frame.
          region.getBoundingClientRect();
          fx.setStyle(
            region,
            "height",
            tween.toOpen
              ? supportsInterpolateSize()
                ? "auto"
                : `${region.scrollHeight.toString()}px`
              : "0px",
          );
          if (tween.toOpen) {
            tween.step = "armed";
          } else {
            height.tween = null;
          }
        }
        if (height.tween?.step === "armed") {
          const armed = height.tween;
          if (d.settled === armed.id) {
            cancelWatch();
            height.tween = null;
          } else {
            height.watch ??= watchSettle(region, () => {
              settle(armed.id);
            });
          }
        }
        if (height.tween === null) {
          const next = d.open ? "" : "0px";
          const changed = region.style.height !== next;
          fx.setStyle(region, "height", next);
          if (changed) {
            // A write outside a tween is instant (no motion allowed, a region created closed over
            // content painted open); the region's CSS height transition would play it.
            cancelHeightTransition(region);
          }
        }
      },
      notify(fx, d) {
        if (reported !== d.open) {
          reported = d.open;
          const onToggle = o.onToggle;
          if (onToggle !== undefined) {
            fx.call(() => {
              onToggle(d.open, d.source);
            });
          }
        }
      },
      terminal(fx, d) {
        listen(false);
        cancelWatch();
        height.tween = null;
        reflect(fx, d.open);
        fx.setStyle(region, "height", d.open ? "" : "0px");
      },
    });

    const reflect = (fx: Fx, open: boolean): void => {
      if (trigger !== null) {
        fx.setAttr(trigger, "aria-expanded", open ? "true" : "false");
      }
      fx.setAttr(region, "aria-hidden", open ? "false" : "true");
      // height:0 clips paint but keeps descendants keyboard-focusable, and
      // aria-hidden on focusable content is invalid; `inert` removes both.
      fx.setProp(region, "inert", !open);
    };

    return {
      controller: {
        get isOpen(): boolean {
          return driver.state.open;
        },
        open(): void {
          toggleTo(true, "api");
        },
        close(): void {
          toggleTo(false, "api");
        },
        toggle(): void {
          toggleTo(!driver.state.open, "api");
        },
        dispose: binding.dispose,
      },
      start: () => {
        driver.flush();
      },
      release: () => {
        driver.release();
      },
    };
  });
}
