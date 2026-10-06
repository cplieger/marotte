// shell-viewport — publishes `--shell-shortfall`, the distance the visual viewport extends below
// the layout viewport, so the shell's bottom-anchored boxes reach the screen edge while iOS hands a
// standalone app a transiently short containing block on a cold launch.

import { isStandalone } from "./platform.js";

export const SHORTFALL_PROP = "--shell-shortfall";
/** A home-indicator band plus window chrome; a larger reading is a broken one. */
export const SHORTFALL_CAP_PX = 160;

const WINDOW_EVENTS = ["resize", "pageshow", "orientationchange", "focus"] as const;
const VIEWPORT_EVENTS = ["resize", "scroll"] as const;

export function computeShortfall(icb: number, vv: VisualViewport | null): number {
  if (vv?.scale !== 1) {
    return 0;
  }
  const raw = Math.round(vv.offsetTop + vv.height - icb);
  return raw > 0 && raw <= SHORTFALL_CAP_PX ? raw : 0;
}

/** `standalone` is a test seam; production takes the platform's answer. */
export function initShellViewport(standalone: boolean = isStandalone): () => void {
  if (!standalone) {
    return () => undefined;
  }
  const root = document.documentElement;
  const vv = window.visualViewport;
  const debug = wantsDebug() ? mountDebug(standalone) : null;
  let frame = 0;

  const recompute = (): void => {
    frame = 0;
    const shortfall = computeShortfall(root.clientHeight, vv);
    if (shortfall > 0) {
      root.style.setProperty(SHORTFALL_PROP, `${shortfall}px`);
    } else {
      root.style.removeProperty(SHORTFALL_PROP);
    }
    debug?.paint(shortfall);
  };
  const schedule = (): void => {
    if (frame === 0) {
      frame = requestAnimationFrame(recompute);
    }
  };

  for (const t of WINDOW_EVENTS) {
    window.addEventListener(t, schedule);
  }
  document.addEventListener("visibilitychange", schedule);
  for (const t of VIEWPORT_EVENTS) {
    vv?.addEventListener(t, schedule);
  }
  recompute();

  return () => {
    for (const t of WINDOW_EVENTS) {
      window.removeEventListener(t, schedule);
    }
    document.removeEventListener("visibilitychange", schedule);
    for (const t of VIEWPORT_EVENTS) {
      vv?.removeEventListener(t, schedule);
    }
    if (frame !== 0) {
      cancelAnimationFrame(frame);
      frame = 0;
    }
    root.style.removeProperty(SHORTFALL_PROP);
    debug?.remove();
  };
}

function wantsDebug(): boolean {
  return (
    new URLSearchParams(location.search).get("vpdebug") === "1" || location.hash === "#vpdebug"
  );
}

interface DebugOverlay {
  paint(shortfall: number): void;
  remove(): void;
}

function mountDebug(standalone: boolean): DebugOverlay {
  const out = document.createElement("pre");
  out.id = "vpdebug";
  out.style.cssText =
    "position:fixed;top:0;left:0;z-index:2147483647;margin:0;padding:4px 6px;" +
    "font:11px/1.3 ui-monospace,monospace;color:#fff;background:rgb(0 0 0 / 70%);" +
    "pointer-events:none;white-space:pre";
  // Reads env() the way the stylesheet does; Chromium reports 0 for all four.
  const probe = document.createElement("div");
  probe.style.cssText =
    "position:fixed;visibility:hidden;padding:env(safe-area-inset-top) " +
    "env(safe-area-inset-right) env(safe-area-inset-bottom) env(safe-area-inset-left)";
  document.body.append(probe, out);

  const bottomOf = (sel: string): string => {
    const el = document.querySelector(sel);
    return el === null ? "n/a" : el.getBoundingClientRect().bottom.toFixed(1);
  };

  return {
    paint(shortfall: number): void {
      const vv = window.visualViewport;
      const p = getComputedStyle(probe);
      out.textContent = [
        `standalone ${standalone ? "yes" : "no"}`,
        `clientHeight ${document.documentElement.clientHeight}`,
        `innerHeight ${window.innerHeight}  outerHeight ${window.outerHeight}`,
        `screen.height ${screen.height}`,
        `vv.height ${vv?.height ?? "n/a"}  vv.offsetTop ${vv?.offsetTop ?? "n/a"}  vv.scale ${vv?.scale ?? "n/a"}`,
        `shortfall ${shortfall}`,
        `safe-area t/r/b/l ${p.paddingTop} ${p.paddingRight} ${p.paddingBottom} ${p.paddingLeft}`,
        `#app bottom ${bottomOf("#app")}`,
        `#prompt-form bottom ${bottomOf("#prompt-form")}`,
        `#sidebar bottom ${bottomOf("#sidebar")}`,
        `.sidebar-footer bottom ${bottomOf(".sidebar-footer")}`,
      ].join("\n");
    },
    remove(): void {
      probe.remove();
      out.remove();
    },
  };
}
