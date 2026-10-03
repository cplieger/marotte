import { $ } from "./dom.js";
import { setSidebarWidth, sidebarWidth } from "./device-view.js";
import { applyStoredSidebarWidth, writeSidebarPref } from "./sidebar-width.js";
import { attachSplitter } from "./splitter.js";

interface Limits {
  readonly min: number;
  readonly max: number;
  /** The token's own max, which is below `min` wherever the window is too narrow
   *  to widen anything. */
  readonly rawMax: number;
}

function readLimits(): Limits {
  const cs = getComputedStyle(document.documentElement);
  const min = Math.round(parseFloat(cs.getPropertyValue("--sidebar-w-min")));
  const rawMax = Math.round(parseFloat(cs.getPropertyValue("--sidebar-w-max")));
  return { min, max: Math.max(min, rawMax), rawMax };
}

function measure(): number {
  return $.sidebar.getBoundingClientRect().width;
}

function clampToLimits(px: number): number {
  const { min, max } = readLimits();
  return Math.round(Math.min(Math.max(px, min), max));
}

export function initSidebarResize(): void {
  // Repeats prepaint.js's apply, so a page whose prepaint.js failed still converges here.
  applyStoredSidebarWidth();
  const handle = $.sidebarResize;

  const restoreStored = (): void => {
    const w = sidebarWidth();
    writeSidebarPref(w > 0 ? w : null);
  };

  const splitter = attachSplitter({
    handle,
    axis: "x",
    direction: 1,
    orientation: "vertical",
    label: "Resize sidebar",
    controls: "sidebar",
    keys: { grow: "ArrowRight", shrink: "ArrowLeft" },
    step: () => parseFloat(getComputedStyle(document.documentElement).fontSize),
    measure,
    limits: () => {
      const { min, max } = readLimits();
      return { min, max };
    },
    apply: (px) => {
      const clamped = clampToLimits(px);
      writeSidebarPref(clamped);
      return clamped;
    },
    commit: () => {
      const { min, rawMax } = readLimits();
      // The RENDERED width, not the pointer's: a drag past the max and back must not
      // store a width the reader never saw.
      const w = Math.round(measure());
      // A release that renders what the stored preference already renders chose
      // nothing: a press that never moved, or an endpoint the window's clamp forced.
      // Storing it would turn a temporary clamp into the preference.
      const stored = sidebarWidth();
      const storedRenders = clampToLimits(stored > 0 ? stored : min);
      if (Math.abs(w - storedRenders) <= 0.5 || rawMax <= min || !handle.checkVisibility()) {
        restoreStored();
        return;
      }
      // At the minimum the token is the answer, so a reader who never widened keeps
      // no stored px copy of it.
      if (w - min <= 0.5) {
        setSidebarWidth(0);
        writeSidebarPref(null);
      } else {
        setSidebarWidth(w);
        writeSidebarPref(w);
      }
    },
    frame: "animation",
    homeEnd: true,
    onReset: () => {
      setSidebarWidth(0);
      writeSidebarPref(null);
      splitter.syncAria(Math.round(measure()));
    },
    valueText: (n) => `${String(Math.round(n))} pixels`,
    onDragStart: () => {
      document.body.classList.add("sidebar-resizing");
    },
    onDragEnd: () => {
      document.body.classList.remove("sidebar-resizing");
    },
  });

  let frame = 0;
  window.addEventListener("resize", () => {
    if (frame !== 0) {
      return;
    }
    frame = requestAnimationFrame(() => {
      frame = 0;
      splitter.syncAria(Math.round(measure()));
    });
  });
  splitter.syncAria(Math.round(measure()));
}
