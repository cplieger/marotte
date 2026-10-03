import type { PreviewHint } from "./wire/types.gen.js";

export type ViewportMode = "fill" | "phone" | "tablet" | "desktop" | "page";

/** A device's pick for one page. `fit` scales a too-wide width down to the
 *  stage; off, the page renders at 100% and the stage scrolls. */
export interface ViewportPick {
  readonly mode: ViewportMode;
  readonly fit: boolean;
}

export const PRESET_WIDTH = { phone: 390, tablet: 820, desktop: 1440 } as const;

/** The app's phone-shaped test (`marotte-ui.md`): on such a screen the switcher
 *  and the scale toggle are hidden, so the page is always shown at Fill. */
export const PHONE_SHAPED_QUERY = "(width <= 48rem), (height <= 30rem)";

const MIN_SCALE = 0.1;

export interface ResolvedViewport {
  readonly mode: ViewportMode;
  /** null is Fill: the frame takes the stage's own width. */
  readonly width: number | null;
  readonly fit: boolean;
}

/** Precedence: a phone-shaped screen is always Fill; otherwise this device's
 *  stored pick, then the page's hint, then Fill. A stored `page` pick needs a
 *  numeric hint to mean anything and falls through without one. */
export function resolvePick(
  stored: ViewportPick | undefined,
  hint: PreviewHint | undefined,
  phoneShaped: boolean,
): ResolvedViewport {
  if (phoneShaped) {
    return { mode: "fill", width: null, fit: true };
  }
  const hintWidth = hint?.width !== undefined && hint.width > 0 ? hint.width : null;
  if (stored !== undefined && (stored.mode !== "page" || hintWidth !== null)) {
    return { mode: stored.mode, width: widthOf(stored.mode, hintWidth), fit: stored.fit };
  }
  const fit = stored?.fit ?? true;
  if (hint?.preset !== undefined) {
    return { mode: hint.preset, width: PRESET_WIDTH[hint.preset], fit };
  }
  if (hintWidth !== null) {
    return { mode: "page", width: hintWidth, fit };
  }
  return { mode: "fill", width: null, fit };
}

function widthOf(mode: ViewportMode, hintWidth: number | null): number | null {
  switch (mode) {
    case "fill":
      return null;
    case "page":
      return hintWidth;
    case "phone":
    case "tablet":
    case "desktop":
      return PRESET_WIDTH[mode];
  }
}

export interface FrameGeometry {
  /** The iframe's own layout box, before any scale. */
  readonly frameW: number;
  readonly frameH: number;
  /** The box the scaled frame occupies in the stage. */
  readonly boxW: number;
  readonly boxH: number;
  readonly scale: number;
  /** True when a 100% frame is wider than the stage, which then scrolls. */
  readonly overflows: boolean;
}

/** Height always follows the stage; pages scroll inside the frame. */
export function frameGeometry(
  stageW: number,
  stageH: number,
  width: number | null,
  fit: boolean,
): FrameGeometry {
  if (width === null || width <= stageW) {
    const w = width ?? stageW;
    return { frameW: w, frameH: stageH, boxW: w, boxH: stageH, scale: 1, overflows: false };
  }
  if (!fit) {
    return { frameW: width, frameH: stageH, boxW: width, boxH: stageH, scale: 1, overflows: true };
  }
  const scale = Math.max(MIN_SCALE, stageW / width);
  return {
    frameW: width,
    frameH: stageH / scale,
    boxW: width * scale,
    boxH: stageH,
    scale,
    overflows: false,
  };
}

export function scaleReadout(scale: number): string {
  return `${String(Math.round(scale * 100))}%`;
}

const MODES: ReadonlySet<string> = new Set<ViewportMode>([
  "fill",
  "phone",
  "tablet",
  "desktop",
  "page",
]);

/** A stored pick, validated: anything else is no pick at all. */
export function parseStoredPick(v: unknown): ViewportPick | undefined {
  if (typeof v !== "object" || v === null) {
    return undefined;
  }
  const rec = v as Record<string, unknown>;
  const mode = rec["mode"];
  const fit = rec["fit"];
  if (typeof mode !== "string" || !MODES.has(mode) || typeof fit !== "boolean") {
    return undefined;
  }
  return { mode: mode as ViewportMode, fit };
}
