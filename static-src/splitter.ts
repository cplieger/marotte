export interface SplitterOptions {
  readonly handle: HTMLElement;
  readonly axis: "x" | "y";
  /** +1: moving toward larger client coordinates grows the pane; -1: shrinks it. */
  readonly direction: 1 | -1;
  readonly orientation: "horizontal" | "vertical";
  readonly label: string;
  readonly controls?: string;
  readonly keys: { readonly grow: string; readonly shrink: string };
  readonly step: () => number;
  readonly measure: () => number;
  readonly limits: () => { min: number; max: number };
  /** Clamp and write a size; returns the size applied. */
  readonly apply: (size: number) => number;
  /** Persist; called once per drag release and once per key step. */
  readonly commit: (applied: number) => void;
  /** "animation" writes at most once per frame; "immediate" on every move. */
  readonly frame: "immediate" | "animation";
  readonly homeEnd?: boolean;
  readonly onReset?: () => void;
  readonly valueText?: (size: number) => string;
  readonly onDragStart?: () => void;
  readonly onDragEnd?: () => void;
}

export interface Splitter {
  /** Reads the limits at call time; a host whose limits move with the window re-syncs. */
  syncAria(size: number): void;
}

interface PointerLike {
  readonly pointerId: number;
  readonly isPrimary: boolean;
  readonly clientX: number;
  readonly clientY: number;
}

export function attachSplitter(opts: SplitterOptions): Splitter {
  const { handle } = opts;
  handle.setAttribute("role", "separator");
  handle.setAttribute("aria-orientation", opts.orientation);
  handle.setAttribute("aria-label", opts.label);
  if (opts.controls !== undefined) {
    handle.setAttribute("aria-controls", opts.controls);
  }
  handle.tabIndex = 0;

  const coord = (e: PointerLike): number => (opts.axis === "x" ? e.clientX : e.clientY);

  let startCoord = 0;
  let start = 0;
  let last = 0;
  let pending: number | null = null;
  let frameID = 0;

  const syncAria = (size: number): void => {
    const { min, max } = opts.limits();
    handle.setAttribute("aria-valuenow", String(size));
    handle.setAttribute("aria-valuemin", String(min));
    handle.setAttribute("aria-valuemax", String(max));
    if (opts.valueText !== undefined) {
      handle.setAttribute("aria-valuetext", opts.valueText(size));
    }
  };

  const applyNow = (candidate: number): void => {
    last = opts.apply(candidate);
    syncAria(last);
  };

  const flush = (): void => {
    frameID = 0;
    if (pending !== null) {
      const candidate = pending;
      pending = null;
      applyNow(candidate);
    }
  };

  handle.addEventListener("pointerdown", (e) => {
    if (!e.isPrimary) {
      return;
    }
    startCoord = coord(e);
    start = opts.measure();
    last = Math.round(start);
    handle.setPointerCapture(e.pointerId);
    handle.classList.add("dragging");
    opts.onDragStart?.();
    e.preventDefault();
  });

  handle.addEventListener("pointermove", (e) => {
    if (!handle.hasPointerCapture(e.pointerId)) {
      return;
    }
    const candidate = start + opts.direction * (coord(e) - startCoord);
    if (opts.frame === "immediate") {
      applyNow(candidate);
      return;
    }
    pending = candidate;
    if (frameID === 0) {
      frameID = requestAnimationFrame(flush);
    }
  });

  const end = (e: PointerEvent): void => {
    if (!handle.hasPointerCapture(e.pointerId)) {
      return;
    }
    // The pending frame is flushed first, so `commit` sees the last pointer position.
    if (frameID !== 0) {
      cancelAnimationFrame(frameID);
      flush();
    }
    handle.releasePointerCapture(e.pointerId);
    handle.classList.remove("dragging");
    opts.onDragEnd?.();
    opts.commit(last);
  };
  handle.addEventListener("pointerup", end);
  handle.addEventListener("pointercancel", end);

  handle.addEventListener("keydown", (e: KeyboardEvent) => {
    let target: number;
    if (e.key === opts.keys.grow) {
      target = opts.measure() + opts.step();
    } else if (e.key === opts.keys.shrink) {
      target = opts.measure() - opts.step();
    } else if (opts.homeEnd === true && e.key === "Home") {
      target = opts.limits().min;
    } else if (opts.homeEnd === true && e.key === "End") {
      target = opts.limits().max;
    } else {
      return;
    }
    e.preventDefault();
    const next = opts.apply(target);
    syncAria(next);
    opts.commit(next);
  });

  if (opts.onReset !== undefined) {
    const reset = opts.onReset;
    handle.addEventListener("dblclick", () => {
      reset();
    });
  }

  return { syncAria };
}
