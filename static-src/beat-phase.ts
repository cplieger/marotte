/**
 * Aligns every dot beat to one origin, so dots that start at different moments still
 * breathe together (see `03-base.css` "THE DOT BEAT").
 */

/** Every keyframe set that beats on `--dot-beat-dur`: the dots' opacity beat and the
 *  square marks' closing hole and its seal (03-base.css). */
const NAMES: ReadonlySet<string> = new Set(["vk-dot-beat", "vk-mark-close", "vk-mark-seal"]);

/** Must equal `--dot-beat-dur` (01-tokens.css). Read from the document rather than
 *  restated, so a retuned token cannot leave the phase grid on the old period. */
function periodMs(): number {
  const raw = getComputedStyle(document.documentElement).getPropertyValue("--dot-beat-dur");
  const s = raw.trim();
  const n = Number.parseFloat(s);
  if (!Number.isFinite(n) || n <= 0) {
    return 0;
  }
  return s.endsWith("ms") ? n : n * 1000;
}

/**
 * The beat `startTime` this element was last stamped FOR, keyed weakly. Not a flag:
 * re-inserting a node recreates its animation (`tabs-drag.ts`, every drop), so a dot can
 * need stamping again. `startTime` tells a real restart from our own write's echo, and
 * keying on the instance makes a re-firing engine harmless.
 */
const stampedFor = new WeakMap<Element, number>();

/**
 * This element's beat, BY NAME: `getAnimations({subtree:true})` also returns other
 * animations. `undefined` while PENDING, which only a new one can be, so that stamps.
 */
function beatStartTime(el: Element): number | undefined {
  for (const a of el.getAnimations({ subtree: true })) {
    if (!NAMES.has((a as CSSAnimation).animationName)) {
      continue;
    }
    return a.startTime === null ? undefined : Number(a.startTime);
  }
  return undefined;
}

function stamp(el: Element): void {
  if (!(el instanceof HTMLElement)) {
    return;
  }
  const period = periodMs();
  if (period === 0) {
    return;
  }
  const start = beatStartTime(el);
  if (start !== undefined) {
    if (stampedFor.get(el) === start) {
      return;
    }
    stampedFor.set(el, start);
  }
  // Negative, so a new animation behaves as though it began at the last boundary of one
  // grid anchored at the performance origin.
  el.style.setProperty("--beat-phase", `${String(-(performance.now() % period))}ms`);
}

let attached: AbortController | undefined;

/** Idempotent. */
export function initBeatPhase(): void {
  if (attached !== undefined) {
    return;
  }
  attached = new AbortController();
  // Delegated, capture phase: one listener serves every dot. The event targets the
  // originating element even for a pseudo-element's animation, and a pseudo cannot be
  // styled from script.
  document.addEventListener(
    "animationstart",
    (e: AnimationEvent) => {
      if (!NAMES.has(e.animationName) || e.target === null) {
        return;
      }
      stamp(e.target as Element);
    },
    { capture: true, passive: true, signal: attached.signal },
  );
}

/** Test seam. Detaches the listener too, so the next init cannot add a second one. */
// deadset:ignore DS1004 -- test seam: resets the attached phase listener
export function resetBeatPhaseForTest(): void {
  attached?.abort();
  attached = undefined;
}
