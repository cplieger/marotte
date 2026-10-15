// The full-screen view of one diagram: a ui-primitives modal over the whole viewport, where pele's zoom is on (pinch,
// wheel, double-click, + / - / 0 / arrows). Built per open and disposed on close, so nothing outlives the view.

import { el } from "@cplieger/reactive";
import { createModal } from "@cplieger/ui-primitives/modal";
import type { Mounted } from "pele/lazy";
import { makeCopyButton } from "./code-blocks.js";
import { iconEl } from "./icon-el.js";
import {
  ICON_CLOSE_UI,
  ICON_DOWNLOAD,
  ICON_MINUS_UI,
  ICON_PLUS_UI,
  ICON_REFRESH,
} from "./icons.js";
import { adoptSvg, downloadSvg, nextIdPrefix, RENDER_OPTIONS, type Pele } from "./mermaid-svg.js";

/** pele's own zoom-control step, so a press here moves as far as one there would. */
const ZOOM_STEP = 1.5625;

interface OverlayOptions {
  /** The modal's accessible name: the diagram's `accTitle`, else a generic one. */
  readonly title: string;
  /** Focused once the view has closed. */
  readonly returnFocus: HTMLElement;
}

function headButton(label: string, icon: string, onClick: () => void): HTMLButtonElement {
  const btn = el(
    "button",
    { type: "button", className: "icon-btn", "data-tooltip": label, "aria-label": label },
    iconEl(icon),
  ) as HTMLButtonElement;
  btn.addEventListener("click", onClick);
  return btn;
}

/** Scale the drawing to the stage, never past its natural size; pele's zoom starts from the size set here. */
function fitToStage(stage: HTMLElement, mounted: Mounted | undefined): void {
  const svg = stage.querySelector<SVGSVGElement>(":scope > svg");
  if (svg === null) {
    return;
  }
  const width = Number(svg.getAttribute("width"));
  const height = Number(svg.getAttribute("height"));
  const cs = getComputedStyle(stage);
  const roomW = stage.clientWidth - parseFloat(cs.paddingLeft) - parseFloat(cs.paddingRight);
  const roomH = stage.clientHeight - parseFloat(cs.paddingTop) - parseFloat(cs.paddingBottom);
  if (!(width > 0 && height > 0 && roomW > 0 && roomH > 0)) {
    return;
  }
  const scale = Math.min(1, roomW / width, roomH / height);
  svg.style.width = `${String(width * scale)}px`;
  svg.style.height = `${String(height * scale)}px`;
  mounted?.zoom?.refresh();
}

/** CSS pixels counted per wheel line (`deltaMode` 1). An approximation: UI Events gives a line no pixel size
 *  (https://w3c.github.io/uievents/split/wheel-events.html#dom-wheelevent-dom_delta_line), and pele treats every
 *  delta as pixels. */
const LINE_PX = 16;

/**
 * Zoom on a plain wheel. pele zooms only on a ctrl- or meta-wheel and leaves a plain one to scroll the page; nothing
 * scrolls behind this view, so the plain wheel is re-sent with the modifier set and pele zooms around the pointer.
 * pele reads the delta whatever its `deltaMode`, so it is re-sent in pixels.
 */
function plainWheelZooms(stage: HTMLElement, e: WheelEvent): void {
  if (e.ctrlKey || e.metaKey) {
    return;
  }
  e.preventDefault();
  e.stopPropagation();
  const unit =
    e.deltaMode === WheelEvent.DOM_DELTA_LINE
      ? LINE_PX
      : e.deltaMode === WheelEvent.DOM_DELTA_PAGE
        ? stage.clientHeight
        : 1;
  stage.dispatchEvent(
    new WheelEvent("wheel", {
      deltaX: e.deltaX * unit,
      deltaY: e.deltaY * unit,
      deltaMode: WheelEvent.DOM_DELTA_PIXEL,
      clientX: e.clientX,
      clientY: e.clientY,
      screenX: e.screenX,
      screenY: e.screenY,
      ctrlKey: true,
      bubbles: true,
      cancelable: true,
    }),
  );
}

/** Open `source` full screen. The caller has already drawn it once, so a throw here is a pele defect, not the
 *  diagram's: it propagates rather than showing a second failure notice. */
export function openMermaidOverlay(pele: Pele, source: string, opts: OverlayOptions): void {
  const prefix = nextIdPrefix();
  const titleId = `${prefix}overlay-title`;
  const stage = el("div", { className: "mermaid-stage" });
  let mounted: Mounted | undefined;

  const zoomBy = (factor: number): void => {
    mounted?.zoom?.zoomBy(factor);
  };
  const close = headButton("Close", ICON_CLOSE_UI, () => {
    ctrl.close();
  });
  const actions = el(
    "div",
    { className: "mermaid-overlay-actions" },
    headButton("Zoom out", ICON_MINUS_UI, () => {
      zoomBy(1 / ZOOM_STEP);
    }),
    headButton("Zoom in", ICON_PLUS_UI, () => {
      zoomBy(ZOOM_STEP);
    }),
    headButton("Reset", ICON_REFRESH, () => {
      mounted?.zoom?.reset();
    }),
    makeCopyButton(() => source, "icon-btn"),
    headButton("Download SVG", ICON_DOWNLOAD, () => {
      if (mounted !== undefined) {
        downloadSvg(mounted.result.svg, stage);
      }
    }),
    close,
  );
  const content = el(
    "div",
    { className: "mermaid-overlay" },
    el(
      "div",
      { className: "modal-header-row mermaid-overlay-head" },
      el("span", { id: titleId, className: "modal-title mermaid-overlay-title" }, opts.title),
      actions,
    ),
    stage,
  );

  const resize = new ResizeObserver(() => {
    fitToStage(stage, mounted);
  });
  const ctrl = createModal(content, {
    labelledBy: titleId,
    initialFocus: close,
    onClose: () => {
      resize.disconnect();
      mounted?.destroy();
      ctrl.dispose();
      if (opts.returnFocus.isConnected) {
        opts.returnFocus.focus();
      }
    },
  });
  ctrl.el.classList.add("mermaid-overlay-dialog");
  // Capture, so the first Escape closes the view before pele's focused-drawing handler spends it resetting the zoom;
  // stopped, so the app's document-level Escape does not act on it too.
  ctrl.el.addEventListener(
    "keydown",
    (e) => {
      if (e.key === "Escape") {
        e.preventDefault();
        e.stopPropagation();
        ctrl.close();
      }
    },
    { capture: true },
  );
  stage.addEventListener(
    "wheel",
    (e) => {
      plainWheelZooms(stage, e);
    },
    { capture: true, passive: false },
  );
  ctrl.open();
  try {
    mounted = pele.mount(stage, source, {
      ...RENDER_OPTIONS,
      zoom: { always: true, controls: false },
      onRender: () => {
        adoptSvg(stage, prefix);
        fitToStage(stage, mounted);
      },
    });
  } catch (err) {
    ctrl.dispose();
    throw err;
  }
  resize.observe(stage);
}
