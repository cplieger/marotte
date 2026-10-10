// How the 16px context indicator is drawn, DOM-free. The band and the colour ramp share one compaction point T (a
// percentage of the window); `compactionPoint` is its owner.

import { signal } from "@cplieger/reactive";

/**
 * The percentage at which KAS summarizes. Used only for a chat whose session never loaded: session/new and the usage
 * channel carry no threshold.
 */
export const KAS_SUMMARIZATION_PCT = 80;

/** The slider value at which marotte adds nothing to KAS's own compaction. */
export const DEFAULT_AUTO_COMPACT_PCT = 80;

/** How far below T the fill stops being green, and how far below T it is fully
 *  red, so red always arrives BEFORE the band. */
const GREEN_BELOW_T = 30;
const RED_BELOW_T = 10;

/** The ramp's T when nothing compacts: the window's own end. */
const NO_COMPACTION_T = 100;

/** The compaction settings the ring reads, mirrored from EffectiveSettings. */
interface CompactionPolicy {
  readonly enabled: boolean;
  readonly pct: number;
}

/** Compaction policy written by settings.ts on every payload and local change; a leaf so no reader imports settings.ts. */
export const compactionPolicy = signal<CompactionPolicy>({
  enabled: true,
  pct: DEFAULT_AUTO_COMPACT_PCT,
});

/** Where the ring draws its band (null: nothing compacts automatically, so no
 *  band) and the T its colour ramp is keyed on. */
export interface CompactionPoint {
  readonly band: number | null;
  readonly t: number;
}

/**
 * The effective compaction point: at the default slider value KAS's reported threshold decides, otherwise marotte's
 * value. Switched off: no band, and the ramp keys on the window's end.
 */
export function compactionPoint(
  policy: CompactionPolicy,
  reportedPct: number | undefined,
): CompactionPoint {
  if (!policy.enabled) {
    return { band: null, t: NO_COMPACTION_T };
  }
  const t =
    policy.pct === DEFAULT_AUTO_COMPACT_PCT ? (reportedPct ?? KAS_SUMMARIZATION_PCT) : policy.pct;
  return { band: t, t };
}

/** One decimal: two nearby percentages resolve to different strokes. */
const MIX_DECIMALS = 1;

function clamp(n: number, lo: number, hi: number): number {
  return Math.min(hi, Math.max(lo, n));
}

/** Token operands only, so the ramp is theme-correct. */
function mix(toToken: string, fromToken: string, ratio: number): string {
  const pct = (clamp(ratio, 0, 1) * 100).toFixed(MIX_DECIMALS);
  return `color-mix(in oklch, var(${toToken}) ${pct}%, var(${fromToken}))`;
}

/** Dash pattern for the band from `thresholdPct` to 100%. The period is exactly 100, so it cannot repeat on the path. */
export function wedgeDash(thresholdPct: number): { dasharray: string; dashoffset: string } {
  const band = 100 - clamp(thresholdPct, 0, 100);
  return { dasharray: `${String(band)} ${String(100 - band)}`, dashoffset: String(band) };
}

/**
 * Tokens used at `pct` of the window, for the card's readout. `contextSize` is 0 when the wire is silent, so the card
 * falls back to the percentage.
 */
export function tokensUsed(pct: number, contextSize: number): number {
  return (contextSize * clamp(pct, 0, 100)) / 100;
}

/**
 * Stroke at `pct` for compaction point `t`: green to `t - 30`, a continuous ramp through `--c-yellow`, saturating at
 * `--c-red` from `t - 10`. Percentages only, so one number is one colour whatever the window.
 */
export function contextStroke(pct: number, t: number): string {
  const green = t - GREEN_BELOW_T;
  const red = t - RED_BELOW_T;
  // Derived rather than declared, so the hand-off is not a third number to keep
  // in step with the two thresholds.
  const yellow = (green + red) / 2;
  const clamped = clamp(pct, 0, 100);
  if (clamped <= green) {
    return "var(--c-green)";
  }
  if (clamped >= red) {
    return "var(--c-red)";
  }
  return clamped < yellow
    ? mix("--c-yellow", "--c-green", (clamped - green) / (yellow - green))
    : mix("--c-red", "--c-yellow", (clamped - yellow) / (red - yellow));
}
