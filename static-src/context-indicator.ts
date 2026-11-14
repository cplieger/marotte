// How the context pill reads, DOM-free. The "Compacts at" figure and the share's colour ramp share one compaction
// point T (a percentage of the window); `compactionPoint` is its owner.

import { signal } from "@cplieger/reactive";

/** The slider's default, KAS's own threshold; it seeds the policy until the first settings payload. */
const DEFAULT_AUTO_COMPACT_PCT = 80;

/** How far below T the fill stops being green, and how far below T it is fully
 *  red, so red always arrives BEFORE the compaction point. */
const GREEN_BELOW_T = 30;
const RED_BELOW_T = 10;

/** The ramp's T when nothing compacts: the window's own end. */
const NO_COMPACTION_T = 100;

/** The compaction settings the pill reads, mirrored from EffectiveSettings. */
interface CompactionPolicy {
  readonly enabled: boolean;
  readonly pct: number;
}

/** Compaction policy written by settings.ts on every payload and local change; a leaf so no reader imports settings.ts. */
export const compactionPolicy = signal<CompactionPolicy>({
  enabled: true,
  pct: DEFAULT_AUTO_COMPACT_PCT,
});

/** Where the conversation is compacted (null: nothing compacts automatically) and the T its colour ramp is keyed on. */
export interface CompactionPoint {
  readonly at: number | null;
  readonly t: number;
}

/**
 * The effective compaction point: the slider's value, where marotte queues a compaction for the turn's end. Switched off:
 * nothing compacts, and the ramp keys on the window's end.
 */
export function compactionPoint(policy: CompactionPolicy): CompactionPoint {
  if (!policy.enabled) {
    return { at: null, t: NO_COMPACTION_T };
  }
  return { at: policy.pct, t: policy.pct };
}

/** One decimal: two nearby percentages resolve to different inks. */
const MIX_DECIMALS = 1;

function clamp(n: number, lo: number, hi: number): number {
  return Math.min(hi, Math.max(lo, n));
}

/** Token operands only, so the ramp is theme-correct and the pill's state picks the shades. */
function mix(toToken: string, fromToken: string, ratio: number): string {
  const pct = (clamp(ratio, 0, 1) * 100).toFixed(MIX_DECIMALS);
  return `color-mix(in oklch, var(${toToken}) ${pct}%, var(${fromToken}))`;
}

/**
 * Tokens used at `pct` of the window, for the card's readout. `contextSize` is 0 when the wire is silent, so the card
 * falls back to the percentage.
 */
export function tokensUsed(pct: number, contextSize: number): number {
  return (contextSize * clamp(pct, 0, 100)) / 100;
}

/**
 * The share's ink at `pct` for compaction point `t`: green to `t - 30`, a continuous ramp through yellow, saturating at
 * red from `t - 10`. Percentages only, so one number is one colour whatever the window. The `--ctx-ink-*` shades are
 * the pill's (15-input.css `.pill-context`), text-safe on each of its fills.
 */
export function contextInk(pct: number, t: number): string {
  const green = t - GREEN_BELOW_T;
  const red = t - RED_BELOW_T;
  // Derived rather than declared, so the hand-off is not a third number to keep
  // in step with the two thresholds.
  const yellow = (green + red) / 2;
  const clamped = clamp(pct, 0, 100);
  if (clamped <= green) {
    return "var(--ctx-ink-green)";
  }
  if (clamped >= red) {
    return "var(--ctx-ink-red)";
  }
  return clamped < yellow
    ? mix("--ctx-ink-yellow", "--ctx-ink-green", (clamped - green) / (yellow - green))
    : mix("--ctx-ink-red", "--ctx-ink-yellow", (clamped - yellow) / (red - yellow));
}
