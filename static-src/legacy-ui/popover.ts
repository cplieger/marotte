/**
 * popover.ts — An anchored floating panel: placeAnchored (pure positioner) and
 * createPopover (interactive controller), layered on the shared popup
 * lifecycle core (popup-core.ts, internal) which owns reveal/dismiss; popover
 * adds anchored placement, scroll/resize tracking, and the stretch mode.
 *
 * No native `popover`: the top layer re-roots a panel's containing block to the
 * viewport, moving the in-flow panels `/popup` exists for, and is unconditional —
 * no z-index can lower a promoted panel back under the `--uip-z-*` ladder.
 *
 * @module
 */

import { untracked } from "@cplieger/reactive";

import { directFx, type Fx } from "./fx.js";
import {
  POPUP_KEYS,
  createPopupCore,
  normalizePopupKey,
  readOptions,
  type PopupConfig,
} from "./popup-core.js";
import type { PopupOptions } from "./popup-core.js";

/** The anchor side a panel is placed against. A request, not a guarantee: with
 *  `flip` on (the default) a side that doesn't fit becomes its opposite. */
export type PopoverPlacement = "top" | "bottom" | "left" | "right";

/** Cross-axis alignment of the panel against the anchor: leading edges flush
 *  (`"start"`), centres aligned, or trailing edges flush (`"end"`). */
export type PopoverAlign = "start" | "center" | "end";

/** A virtual anchor: anything that can report a bounding rect. Lets a popover
 *  be positioned against a coordinate/right-click point (see pointAnchor) or
 *  any non-DOM rect source, not just an element. */
export interface VirtualAnchor {
  getBoundingClientRect(): DOMRect;
}

/** What a popover can be anchored to: a real element, or a virtual rect source
 *  (e.g. a coordinate via pointAnchor). */
export type PopoverAnchor = HTMLElement | VirtualAnchor;

/** Build a zero-size virtual anchor at a fixed viewport point, such as a
 *  right-click; for a moving point build a new one and reposition. With no
 *  trigger element, a disconnected panel anchored to it is hosted into the
 *  topmost open modal `<dialog>`, else `document.body`; a connected panel
 *  stays where the caller put it. */
export function pointAnchor(x: number, y: number): VirtualAnchor {
  return {
    getBoundingClientRect: (): DOMRect => ({
      x,
      y,
      left: x,
      top: y,
      right: x,
      bottom: y,
      width: 0,
      height: 0,
      toJSON: () => ({}),
    }),
  };
}

/** Everything the positioner needs beyond the panel and the anchor: the side
 *  and cross-axis alignment, the main-axis gap, the two viewport-fit
 *  corrections (`flip`, `clamp`) and the edge `margin` they respect, and the
 *  width modes. All optional, all pure inputs — {@link placeAnchored} reads
 *  them per call and keeps no state. */
interface PlacementOptions {
  /** Side of the anchor the panel sits on. Default `"bottom"`. */
  placement?: PopoverPlacement;
  /** Cross-axis edge alignment. Default `"start"`. */
  align?: PopoverAlign;
  /** Main-axis gap between anchor and panel, in px. Default `4`. */
  offset?: number;
  /** Flip to the opposite side when the chosen side doesn't fit. Default `true`. */
  flip?: boolean;
  /** Clamp the cross-axis coordinate into the viewport. Default `true`. */
  clamp?: boolean;
  /** Set the panel's `min-width` to the anchor width (`true`) or to
   *  `max(anchorWidth, n)` (a number). Default `false`. Ignored when
   *  `stretch: "viewport"` is set (the panel spans the full width instead). */
  matchAnchorWidth?: boolean | number;
  /** Viewport edge margin used by flip + clamp — and, in `stretch: "viewport"`
   *  mode, the inline inset from each viewport edge — in px. Default `8`. */
  margin?: number;
  /** Full-bleed / edge-pinned mode: `"viewport"` spans the panel across the
   *  viewport's inline axis (respecting `margin`) instead of content-sizing
   *  and cross-aligning it — the mobile full-width action-sheet pattern. Only
   *  meaningful for a top/bottom `placement`; `align`, cross-axis `clamp`, and
   *  `matchAnchorWidth` do not apply. Default unset (content-sized). */
  stretch?: "viewport";
}

/** Placement options plus the popup lifecycle options (dismissal, Escape
 *  isolation, single-open `group`, opt-in focus, open/close callbacks,
 *  `haspopup` — advertised on an element anchor; ignored for a virtual/point
 *  anchor, which has no element to annotate). The popup `trigger` is not an
 *  option here: the anchor is the trigger when it is a real element. */
export interface PopoverOptions extends PlacementOptions, Omit<PopupOptions, "trigger"> {}

/** A merge-patch for `setOptions`: keys PRESENT in the patch override the
 *  current value — including an explicit `undefined`, which clears the option
 *  back to its default (e.g. `{ stretch: undefined }` leaves full-bleed mode).
 *  Keys absent from the patch are left unchanged. */
type PopoverOptionsPatch = {
  [K in keyof PopoverOptions]?: PopoverOptions[K] | undefined;
};

/** Handle on an anchored popover: the popup lifecycle plus placement. Position
 *  is recomputed on show and, while open, on scroll and resize; a content
 *  change is the one thing it cannot observe, so `reposition()` is the caller's
 *  to call. */
export interface PopoverController {
  show(): void;
  hide(): void;
  toggle(): void;
  /** Recompute placement. Call after the panel's content changes size. */
  reposition(): void;
  readonly isOpen: boolean;
  /** The panel element (the caller's, never one this controller created). */
  readonly el: HTMLElement;
  /** Merge-patch the options on the LIVE controller — the seam for responsive
   *  placement (e.g. flip `stretch` / `offset` / `margin` on a breakpoint
   *  change instead of disposing and rebuilding). Keys present in the patch
   *  override (an explicit `undefined` clears back to the default); absent
   *  keys are unchanged. An open popover repositions immediately and updates
   *  its `is-stretched` marker; dismissal listeners re-arm under the new
   *  flags. The anchor is constructor-bound and cannot be patched. */
  setOptions(patch: PopoverOptionsPatch): void;
  dispose(): void;
}

/** The six members of an anchor's rect, read once each. */
interface Rect {
  readonly top: number;
  readonly left: number;
  readonly right: number;
  readonly bottom: number;
  readonly width: number;
  readonly height: number;
}

/** Read each member once: a VirtualAnchor's rect may compute them in getters. */
function snapshotRect(r: DOMRect): Rect {
  return {
    top: r.top,
    left: r.left,
    right: r.right,
    bottom: r.bottom,
    width: r.width,
    height: r.height,
  };
}

interface ViewportBox {
  readonly left: number;
  readonly top: number;
  readonly width: number;
  readonly height: number;
}

/** The visible viewport box. Uses `window.visualViewport` when present (so the
 *  box tracks pinch-zoom and stays above the mobile on-screen keyboard), else
 *  the layout viewport with zero offsets. */
function viewportBox(): ViewportBox {
  const vv = window.visualViewport;
  if (vv != null) {
    return { left: vv.offsetLeft, top: vv.offsetTop, width: vv.width, height: vv.height };
  }
  return { left: 0, top: 0, width: window.innerWidth, height: window.innerHeight };
}

/** Cross-axis coordinate for `align` along an axis whose anchor extent is
 *  `[start, start + size]` and whose panel extent is `panelSize`. */
function alignCoord(align: PopoverAlign, start: number, size: number, panelSize: number): number {
  if (align === "center") {
    return start + size / 2 - panelSize / 2;
  }
  if (align === "end") {
    return start + size - panelSize;
  }
  return start;
}

/** Resolve the effective placement, flipping to the opposite side when the
 *  chosen side overflows its viewport edge (beyond `margin`) and the opposite
 *  side has more room. */
function resolvePlacement(
  placement: PopoverPlacement,
  rect: Rect,
  panelW: number,
  panelH: number,
  offset: number,
  margin: number,
  vp: ViewportBox,
): PopoverPlacement {
  const vpRight = vp.left + vp.width;
  const vpBottom = vp.top + vp.height;
  const spaceAbove = rect.top - vp.top;
  const spaceBelow = vpBottom - rect.bottom;
  const spaceLeft = rect.left - vp.left;
  const spaceRight = vpRight - rect.right;

  if (placement === "bottom") {
    const overflows = rect.bottom + offset + panelH > vpBottom - margin;
    return overflows && spaceAbove > spaceBelow ? "top" : "bottom";
  }
  if (placement === "top") {
    const overflows = rect.top - offset - panelH < vp.top + margin;
    return overflows && spaceBelow > spaceAbove ? "bottom" : "top";
  }
  if (placement === "right") {
    const overflows = rect.right + offset + panelW > vpRight - margin;
    return overflows && spaceLeft > spaceRight ? "left" : "right";
  }
  const overflows = rect.left - offset - panelW < vp.left + margin;
  return overflows && spaceRight > spaceLeft ? "right" : "left";
}

/** Main + cross axis coordinates for a resolved placement. */
function computeCoords(
  placement: PopoverPlacement,
  align: PopoverAlign,
  rect: Rect,
  panelW: number,
  panelH: number,
  offset: number,
): { left: number; top: number } {
  if (placement === "top" || placement === "bottom") {
    const top = placement === "bottom" ? rect.bottom + offset : rect.top - panelH - offset;
    return { left: alignCoord(align, rect.left, rect.width, panelW), top };
  }
  const left = placement === "right" ? rect.right + offset : rect.left - panelW - offset;
  return { left, top: alignCoord(align, rect.top, rect.height, panelH) };
}

/** Clamp a coordinate so the panel stays within `[margin, size - margin]` of the
 *  viewport box on that axis. Pins to the leading margin when the panel is
 *  larger than the available space. */
function clampCoord(
  coord: number,
  panelSize: number,
  vpStart: number,
  vpSize: number,
  margin: number,
): number {
  const lo = vpStart + margin;
  const hi = vpStart + vpSize - panelSize - margin;
  if (hi < lo) {
    return lo;
  }
  return Math.max(lo, Math.min(coord, hi));
}

/** Every PlacementOptions key, defaults resolved, frozen. */
interface Placement {
  readonly placement: PopoverPlacement;
  readonly align: PopoverAlign;
  readonly offset: number;
  readonly flip: boolean;
  readonly clamp: boolean;
  readonly matchAnchorWidth: boolean | number;
  readonly margin: number;
  readonly stretch: "viewport" | null;
}

/** The normalized value of one placement option, or `undefined` for a key that is not one. */
function normalizePlacementKey(key: string, value: unknown): unknown {
  switch (key) {
    case "placement":
      return value ?? "bottom";
    case "align":
      return value ?? "start";
    case "offset":
      return value ?? 4;
    case "flip":
    case "clamp":
      return value ?? true;
    case "matchAnchorWidth":
      return value ?? false;
    case "margin":
      return value ?? 8;
    case "stretch":
      return value ?? null;
    default:
      return undefined;
  }
}

const PLACEMENT_KEYS = [
  "placement",
  "align",
  "offset",
  "flip",
  "clamp",
  "matchAnchorWidth",
  "margin",
  "stretch",
] as const;

function normalizePlacement(opts: PlacementOptions | undefined): Placement {
  return readOptions(opts, PLACEMENT_KEYS, normalizePlacementKey) as unknown as Placement;
}

function isStretched(p: Placement): boolean {
  return p.stretch === "viewport" && (p.placement === "top" || p.placement === "bottom");
}

/** Place `panel` against `anchor` through `fx`: the anchor is measured once,
 *  and every panel write is one checked call. A throwing anchor leaves the
 *  last placement in place. */
function placeWith(fx: Fx, panel: HTMLElement, anchor: PopoverAnchor, p: Placement): void {
  const raw = fx.call(() => anchor.getBoundingClientRect());
  if (raw === undefined) {
    return;
  }
  const rect = fx.measure(() => snapshotRect(raw));
  if (rect === undefined) {
    return;
  }
  fx.setStyle(panel, "position", "fixed");
  const vp = viewportBox();
  const { placement, margin, offset } = p;

  if (isStretched(p)) {
    // Pin both inline edges. In fixed positioning `right` is measured from the
    // viewport's inline-end edge, so left + right together span the width minus
    // `margin` on each side, robust to the panel's box-sizing and padding. A
    // min-width a prior content-sized placement set would widen the pinned panel.
    fx.setStyle(panel, "left", `${margin.toString()}px`);
    fx.setStyle(panel, "right", `${margin.toString()}px`);
    fx.setStyle(panel, "minWidth", "");
    // panelW is unused by the top/bottom flip test.
    const panelH = panel.offsetHeight;
    const effective = p.flip
      ? resolvePlacement(placement, rect, 0, panelH, offset, margin, vp)
      : placement;
    const top = effective === "bottom" ? rect.bottom + offset : rect.top - panelH - offset;
    fx.setStyle(panel, "top", `${top.toString()}px`);
    return;
  }

  // Content-sized: an inline-end pin a prior stretch placement wrote would fight the panel's own width.
  fx.setStyle(panel, "right", "");
  // matchAnchorWidth first, so the measured size reflects it; cleared when off,
  // so turning it off does not leave the previous min-width stuck.
  const matchWidth = p.matchAnchorWidth;
  fx.setStyle(
    panel,
    "minWidth",
    matchWidth === false
      ? ""
      : `${(matchWidth === true ? rect.width : Math.max(rect.width, matchWidth)).toString()}px`,
  );

  const panelW = panel.offsetWidth;
  const panelH = panel.offsetHeight;
  const effective = p.flip
    ? resolvePlacement(placement, rect, panelW, panelH, offset, margin, vp)
    : placement;
  const { left, top } = computeCoords(effective, p.align, rect, panelW, panelH, offset);

  let finalLeft = left;
  let finalTop = top;
  if (p.clamp) {
    if (effective === "top" || effective === "bottom") {
      finalLeft = clampCoord(left, panelW, vp.left, vp.width, margin);
    } else {
      finalTop = clampCoord(top, panelH, vp.top, vp.height, margin);
    }
  }
  fx.setStyle(panel, "left", `${finalLeft.toString()}px`);
  fx.setStyle(panel, "top", `${finalTop.toString()}px`);
}

/**
 * Position `panel` (set to `position: fixed`) relative to `anchor` per `opts`.
 * Every option is read once, before the anchor is measured or the panel
 * written. Idempotent; safe to call repeatedly on scroll/resize or after the
 * panel's content changes size. A throwing anchor or option getter reaches the caller.
 */
export function placeAnchored(
  panel: HTMLElement,
  anchor: PopoverAnchor,
  opts?: PlacementOptions,
): void {
  placeWith(directFx, panel, anchor, normalizePlacement(opts));
}

type PopoverConfig = PopupConfig & Placement;

// The anchor is the trigger and is constructor-bound, so `trigger` is never read.
const POPOVER_KEYS = [...POPUP_KEYS.filter((k) => k !== "trigger"), ...PLACEMENT_KEYS];

/** Read each popover option once into a frozen config whose trigger is `anchorEl`. */
function normalizePopoverOptions(
  opts: PopoverOptions | undefined,
  anchorEl: HTMLElement | null,
): PopoverConfig {
  const read = readOptions(opts, POPOVER_KEYS, normalizePopoverKey);
  return Object.freeze({ ...read, trigger: anchorEl }) as unknown as PopoverConfig;
}

function normalizePopoverKey(key: string, value: unknown): unknown {
  if ((PLACEMENT_KEYS as readonly string[]).includes(key)) {
    return normalizePlacementKey(key, value);
  }
  // The anchor is the trigger: no patch changes it.
  return key === "trigger" ? undefined : normalizePopupKey(key, value);
}

/** Wire `anchor` and a caller-supplied `panel` into an anchored, dismissible
 *  popover: it reveals and positions the panel, tracks the anchor on scroll
 *  and resize, and dismisses on outside click or Escape. The caller owns the
 *  panel element, which the controller never builds or removes. */
export function createPopover(
  anchor: PopoverAnchor,
  panel: HTMLElement,
  opts?: PopoverOptions,
): PopoverController {
  // A virtual or point anchor has no element to annotate or hit-test.
  const anchorEl = anchor instanceof HTMLElement ? anchor : null;
  const config = untracked(() => normalizePopoverOptions(opts, anchorEl));

  let trackingFrame: number | null = null;
  let kick = (): void => undefined;
  // Bursts of scroll and resize coalesce into one placement per frame.
  const scheduleReposition = (): void => {
    if (trackingFrame !== null) {
      return;
    }
    trackingFrame = requestAnimationFrame(() => {
      trackingFrame = null;
      kick();
    });
  };
  // Capture-phase scroll catches scrolling in any ancestor, resize covers
  // layout changes, and visualViewport events cover pinch-zoom and the mobile keyboard.
  const track = (on: boolean): void => {
    const method = on ? "addEventListener" : "removeEventListener";
    if (!on && trackingFrame !== null) {
      cancelAnimationFrame(trackingFrame);
      trackingFrame = null;
    }
    document[method]("scroll", scheduleReposition, true);
    window[method]("resize", scheduleReposition);
    const vv = window.visualViewport;
    if (vv != null) {
      vv[method]("resize", scheduleReposition);
      vv[method]("scroll", scheduleReposition);
    }
  };

  const core = createPopupCore<PopoverConfig>(
    panel,
    config,
    {
      stateClass: "uip-popover",
      shown(fx, c) {
        fx.toggleClass(panel, "is-stretched", isStretched(c));
        placeWith(fx, panel, anchor, c);
      },
      leaveEnd(fx) {
        fx.removeClass(panel, "is-stretched");
      },
      listeners: track,
    },
    normalizePopoverKey,
  );
  kick = core.kick;
  const popup = core.controller;

  return {
    show: () => {
      popup.show();
    },
    hide: () => {
      popup.hide();
    },
    toggle: () => {
      popup.toggle();
    },
    reposition: core.kick,
    get isOpen(): boolean {
      return popup.isOpen;
    },
    get el(): HTMLElement {
      return panel;
    },
    setOptions(patch: PopoverOptionsPatch): void {
      popup.setOptions(patch);
    },
    dispose: () => {
      popup.dispose();
    },
  };
}
