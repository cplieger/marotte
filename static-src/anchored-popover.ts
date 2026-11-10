// A popover for a panel that can be taller than the window: `@cplieger/ui/popover` places it and
// flips it, but never caps its height, so a tall form anchored mid-page would run off the screen.

import { createPopover, type PopoverController, type PopoverOptions } from "@cplieger/ui/popover";
import { onViewportChange, viewportBox } from "./viewport-frame.js";

const OFFSET = 4;
const MARGIN = 8;

/** The room on the anchor's roomier side, in px, which the flip will pick for a panel that fits
 *  neither side. */
function roomBeside(anchor: HTMLElement): number {
  const r = anchor.getBoundingClientRect();
  const { offsetTop, height } = viewportBox();
  return Math.max(r.top - offsetTop, offsetTop + height - r.bottom) - OFFSET - MARGIN;
}

/** No `setOptions`: the group is joined only while open, which a patch would bypass. */
export type AnchoredPopover = Pick<
  PopoverController,
  "show" | "hide" | "toggle" | "reposition" | "isOpen"
>;

/**
 * `createPopover` under `anchor`, end-aligned, its `--popover-room` the room the flip can give it
 * (the panel's stylesheet caps its block size by it); call `reposition()` after a content swap.
 * A closed one holds nothing outside its anchor's closures: it joins `group` only while open, and
 * a panel it hosted leaves the page once the close has played. An open one closes when its anchor
 * leaves the page.
 */
export function anchoredPopover(
  anchor: HTMLElement,
  panel: HTMLElement,
  opts: Omit<PopoverOptions, "placement" | "align" | "offset" | "margin"> = {},
): AnchoredPopover {
  const { group, ...rest } = opts;
  let hosted = false;
  const fit = (): void => {
    panel.style.setProperty("--popover-room", `${String(Math.max(0, roomBeside(anchor)))}px`);
    // The popover measures the panel's width where it last placed it, and a fixed box shrinks to
    // the room right of its `left`: wider new content would be measured short and end-aligned wrong.
    panel.style.left = "0px";
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
  const anchorWatch = new MutationObserver(() => {
    if (!anchor.isConnected) {
      ctl.hide();
    }
  });
  // The popover hides the panel once its leave transition has settled.
  const leaveWatch = new MutationObserver(() => {
    if (panel.hidden) {
      leaveWatch.disconnect();
      panel.remove();
    }
  });
  let releaseViewport = (): void => undefined;
  const track = (on: boolean): void => {
    if (on) {
      document.addEventListener("scroll", refit, true);
      releaseViewport = onViewportChange(refit);
      anchorWatch.observe(document.body, { childList: true, subtree: true });
      return;
    }
    if (frame !== 0) {
      cancelAnimationFrame(frame);
      frame = 0;
    }
    document.removeEventListener("scroll", refit, true);
    releaseViewport();
    releaseViewport = (): void => undefined;
    anchorWatch.disconnect();
  };
  const ctl = createPopover(anchor, panel, {
    ...rest,
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
      if (group !== undefined) {
        ctl.setOptions({ group: undefined });
      }
      if (hosted) {
        if (panel.hidden) {
          panel.remove();
        } else {
          leaveWatch.observe(panel, { attributes: true, attributeFilter: ["hidden"] });
        }
      }
      opts.onClose?.();
    },
  });
  const open = (): void => {
    leaveWatch.disconnect();
    hosted ||= !panel.isConnected;
    if (group !== undefined) {
      ctl.setOptions({ group });
    }
    fit();
    ctl.show();
  };
  return {
    get isOpen(): boolean {
      return ctl.isOpen;
    },
    show: open,
    hide: () => {
      ctl.hide();
    },
    toggle: () => {
      if (ctl.isOpen) {
        ctl.hide();
      } else {
        open();
      }
    },
    reposition: () => {
      fit();
      ctl.reposition();
    },
  };
}
