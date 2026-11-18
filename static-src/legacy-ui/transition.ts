/**
 * transition.ts — Run one CSS transition on an element and finalize it once it
 * ends. A transition needs its start state flushed before the change, or the
 * browser coalesces both writes into one frame and nothing animates;
 * `runTransition` does that flush. The primitives' fades settle through
 * `watchSettle`.
 * @module
 */

/**
 * Ceiling (ms) on how long a settle waits for `transitionend` before assuming
 * no transition ran at all. Not an estimate of any duration: every duration in
 * this library is the app's, set through a `--uip-*` custom property, so the
 * library cannot know the real one. A transition still running at the ceiling
 * (a long one, or a short one whose frames a busy page delivers late) settles
 * when it ends instead.
 */
const SETTLE_FALLBACK_MS = 400;

/** One state change plus what finalizes it. Handed to {@link runTransition},
 *  which flushes the pre-change style before invoking `change` so the engine
 *  has a start state to animate from. */
interface TransitionSpec {
  /** Writes the state to animate TO. Whatever the caller wrote BEFORE calling
   *  `runTransition` is the state it animates FROM. */
  change: () => void;
  /** Runs exactly once when the transition on the element ends, or after the
   *  settle fallback ceiling (`SETTLE_FALLBACK_MS`) if none ever starts. Omit
   *  it when there is nothing to finalize; then nothing is armed and nothing
   *  needs cancelling. */
  settled?: () => void;
}

/** Pending settles, keyed by element: the cancel that detaches one. A WeakMap
 *  so a settle armed against a node that is then dropped retains nothing. */
const inFlight = new WeakMap<HTMLElement, () => void>();

/** Whether a transition of `property` still runs on `el`. A finished transition's
 *  `transitionend` is dispatched after its `finished` promise's reactions run
 *  (https://drafts.csswg.org/web-animations-1/#update-animations-and-send-events), so a settle
 *  armed by a write in one of them would otherwise take that end for its own. */
function stillRunning(el: HTMLElement, property: string): boolean {
  return el
    .getAnimations()
    .some((a) => a instanceof CSSTransition && a.transitionProperty === property);
}

/** @internal Arm one settle on `el`: the first `transitionend` whose target is
 *  `el` and whose property no newer transition still runs, or the fallback
 *  ceiling, whichever comes first; at the ceiling, the end of every transition
 *  still running on `el`, or the page hiding. Fires once; returns its cancel. */
export function watchSettle(el: HTMLElement, onSettled: () => void): () => void {
  let armed = true;
  let timer: ReturnType<typeof setTimeout> | null = null;
  const detach = (): void => {
    armed = false;
    el.removeEventListener("transitionend", onEnd);
    document.removeEventListener("visibilitychange", onHide);
    if (timer !== null) {
      clearTimeout(timer);
      timer = null;
    }
  };
  // Detaching (not a latch) is what makes this fire once: one change can end
  // several transitions, and by the second event the listener is gone.
  const fire = (): void => {
    detach();
    onSettled();
  };
  const onEnd = (e: TransitionEvent): void => {
    if (e.target === el && !stillRunning(el, e.propertyName)) {
      fire();
    }
  };
  // A hidden page runs no rendering update, so a running transition's `finished` would wait
  // until the page is shown: hidden, the ceiling settles at once.
  const onHide = (): void => {
    if (document.visibilityState === "hidden") {
      fire();
    }
  };
  // A cancelled transition rejects `finished`, and one cancelled for a replacement leaves that
  // one running, so each round settles every promise and reads what runs now.
  const onCeiling = (): void => {
    timer = null;
    if (!armed) {
      return;
    }
    const running = el.getAnimations().filter((a) => a instanceof CSSTransition);
    if (running.length === 0 || document.visibilityState === "hidden") {
      fire();
      return;
    }
    document.addEventListener("visibilitychange", onHide);
    void Promise.allSettled(running.map((a) => a.finished)).then(onCeiling);
  };
  el.addEventListener("transitionend", onEnd);
  timer = setTimeout(onCeiling, SETTLE_FALLBACK_MS);
  return detach;
}

/**
 * Drop the settle pending on `el` WITHOUT running it. For a caller that is
 * taking the element somewhere the pending callback must not follow: a reopen
 * during a fade, a dispose mid-animation, a node about to be removed. A no-op
 * when nothing is pending.
 */
export function cancelTransition(el: HTMLElement): void {
  const cancel = inFlight.get(el);
  if (cancel !== undefined) {
    inFlight.delete(el);
    cancel();
  }
}

/**
 * Run one CSS transition on `el`: `settled` runs once, on the settle
 * `watchSettle` defines, or after the fallback ceiling. `change` and
 * `settled` are read once, first, and any settle pending on `el` is dropped.
 * A `cancelTransition` or `runTransition` on `el` from inside `change` takes
 * the element and this call arms nothing; a throw from `change` arms nothing.
 */
export function runTransition(el: HTMLElement, spec: TransitionSpec): void {
  const change = spec.change;
  const settled = spec.settled;
  cancelTransition(el);

  // Reading a layout property forces the engine to resolve the style written
  // so far; without it the change below lands in the same frame.
  el.getBoundingClientRect();

  const reservation = (): void => undefined;
  inFlight.set(el, reservation);
  try {
    change();
  } catch (error) {
    if (inFlight.get(el) === reservation) {
      inFlight.delete(el);
    }
    throw error;
  }
  if (inFlight.get(el) !== reservation) {
    return;
  }
  if (settled === undefined) {
    inFlight.delete(el);
    return;
  }
  const cancel = watchSettle(el, () => {
    if (inFlight.get(el) === cancel) {
      inFlight.delete(el);
    }
    settled();
  });
  inFlight.set(el, cancel);
}
