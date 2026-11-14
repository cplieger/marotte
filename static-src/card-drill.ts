// A card's drill-in: one panel slides out along the inline axis as the next slides in, and the card's height follows
// to the new panel's own. Web Animations rather than `interpolate-size` or an element-scoped view transition, which
// only Chromium ships; both happen at once under prefers-reduced-motion.

/** The cleanup each card's running slide owns, so a slide started over it cannot hide the panel it shows. */
const running = new WeakMap<HTMLElement, () => void>();

/** Show `to` in place of `from` inside `card`. `forward` drills in (the new panel enters from the inline end); false
 *  comes back. Returns at once: the panels' `hidden` state is final when it does. */
export function slidePanel(
  card: HTMLElement,
  from: HTMLElement,
  to: HTMLElement,
  forward: boolean,
): void {
  running.get(card)?.();
  const before = card.getBoundingClientRect().height;
  from.hidden = true;
  to.hidden = false;
  if (matchMedia("(prefers-reduced-motion: reduce)").matches) {
    return;
  }
  const after = card.getBoundingClientRect().height;
  const cs = getComputedStyle(card);
  const timing: KeyframeAnimationOptions = {
    duration: durationMs(cs.getPropertyValue("--dur-standard")),
    easing: cs.getPropertyValue("--ease-standard").trim() || "ease",
  };
  const out = (forward ? -100 : 100) * (cs.direction === "rtl" ? -1 : 1);
  // The leaving panel keeps painting out of flow, so the card's own height is the entering panel's.
  from.hidden = false;
  from.classList.add("model-panel-leaving");
  const anims = [
    card.animate(
      [{ blockSize: `${String(before)}px` }, { blockSize: `${String(after)}px` }],
      timing,
    ),
    from.animate([{ translate: "0 0" }, { translate: `${String(out)}% 0` }], timing),
    to.animate([{ translate: `${String(-out)}% 0` }, { translate: "0 0" }], timing),
  ];
  const done = (): void => {
    if (running.get(card) !== done) {
      return;
    }
    running.delete(card);
    for (const a of anims) {
      a.cancel();
    }
    from.classList.remove("model-panel-leaving");
    from.hidden = true;
  };
  running.set(card, done);
  void Promise.allSettled(anims.map((a) => a.finished)).then(done);
}

/** Stop `card`'s slide where it ends, so its panels' state is the one `slidePanel` returned with. */
export function settlePanels(card: HTMLElement): void {
  running.get(card)?.();
}

/** A CSS `<time>` in milliseconds; 0 for anything else. */
function durationMs(time: string): number {
  const t = time.trim();
  const n = Number.parseFloat(t);
  if (!Number.isFinite(n)) {
    return 0;
  }
  return t.endsWith("ms") ? n : n * 1000;
}
