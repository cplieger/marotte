// A popover for a panel that can be taller than the window: `@cplieger/ui/popover` places it and
// flips it, but never caps its height, so a tall form anchored mid-page would run off the screen.

import { createPopover, type PopoverController, type PopoverOptions } from "@cplieger/ui/popover";

const OFFSET = 4;
const MARGIN = 8;

/** The room on the anchor's roomier side, in px, which the flip will pick for a panel that fits
 *  neither side. */
function roomBeside(anchor: HTMLElement): number {
  const r = anchor.getBoundingClientRect();
  const vv = window.visualViewport;
  const top = vv?.offsetTop ?? 0;
  const bottom = top + (vv?.height ?? window.innerHeight);
  return Math.max(r.top - top, bottom - r.bottom) - OFFSET - MARGIN;
}

/**
 * `createPopover` under `anchor`, end-aligned, with the panel's `--popover-room` set to the room
 * the flip can give it; the panel's stylesheet caps its block size by it and scrolls. The caller
 * calls `reposition()` after swapping the panel's content.
 */
export function anchoredPopover(
  anchor: HTMLElement,
  panel: HTMLElement,
  opts: Omit<PopoverOptions, "placement" | "align" | "offset" | "margin"> = {},
): PopoverController {
  const fit = (): void => {
    panel.style.setProperty("--popover-room", `${String(Math.max(0, roomBeside(anchor)))}px`);
  };
  let frame = 0;
  // The popover re-places on these too, but from the old cap: this pass re-fits, then re-places.
  const refit = (): void => {
    if (frame !== 0) {
      return;
    }
    frame = requestAnimationFrame(() => {
      frame = 0;
      fit();
      ctl.reposition();
    });
  };
  const track = (on: boolean): void => {
    const method = on ? "addEventListener" : "removeEventListener";
    if (!on && frame !== 0) {
      cancelAnimationFrame(frame);
      frame = 0;
    }
    document[method]("scroll", refit, true);
    window[method]("resize", refit);
    window.visualViewport?.[method]("resize", refit);
  };
  const ctl = createPopover(anchor, panel, {
    ...opts,
    placement: "bottom",
    align: "end",
    offset: OFFSET,
    margin: MARGIN,
    onOpen: () => {
      track(true);
      opts.onOpen?.();
    },
    onClose: () => {
      track(false);
      opts.onClose?.();
    },
  });
  return {
    ...ctl,
    get isOpen(): boolean {
      return ctl.isOpen;
    },
    get el(): HTMLElement {
      return ctl.el;
    },
    show: () => {
      fit();
      ctl.show();
    },
    toggle: () => {
      fit();
      ctl.toggle();
    },
    reposition: () => {
      fit();
      ctl.reposition();
    },
    dispose: () => {
      track(false);
      ctl.dispose();
    },
  };
}
