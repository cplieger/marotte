// The effort slider: a caption naming the dimension and live tier over a bar the knob slides inside; the caption is
// why the knob carries no text. `model-switcher.ts` owns state and dispatch.
import { el } from "@cplieger/reactive";
import { effortLabel } from "./effort.js";
import type { SessionEffortLevel } from "./types.js";

export interface EffortSliderHandle {
  /** The `.effort-row` element, for the card to append and remove. */
  readonly el: HTMLDivElement;
  /** Rebuild the ARIA range for a new tier vocabulary. */
  readonly setLevels: (levels: readonly SessionEffortLevel[]) => void;
  /** Put the knob on `id`; an unoffered tier resolves to the lowest (a slider has no "unchosen" state). */
  readonly setActive: (id: string) => void;
}

/**
 * Build the slider. `onPick` fires on every completed gesture, including one landing where the knob sits:
 * `model-switcher.setEffort` drops a repeat of the chat's choice.
 */
export function buildEffortSlider(opts: { onPick: (level: string) => void }): EffortSliderHandle {
  let levels: readonly SessionEffortLevel[] = [];
  /** The tier last synced from the store, for a cancelled drag to fall back to. */
  let synced = "";

  /**
   * Separate from the static "Effort:" so the writer replaces the value alone. Not a live region: the knob's name stays
   * stable, and `aria-valuetext` carries the value.
   */
  const value = el("span", { className: "effort-value" });
  const knob = el("div", {
    className: "effort-knob",
    role: "slider",
    tabindex: "0",
    "aria-label": "Reasoning effort",
    "aria-valuemin": "0",
    "aria-valuemax": "0",
    "aria-valuenow": "0",
    "aria-valuetext": "",
  }) as HTMLDivElement;
  /** The marks' layer spans the track, so each dot takes the knob's travel arithmetic and lands where the knob does. */
  const stops = el("div", { className: "effort-stops", "aria-hidden": "true" }) as HTMLDivElement;
  const track = el("div", { className: "effort-track" }, stops, knob) as HTMLDivElement;
  const row = el(
    "div",
    { className: "effort-row" },
    el("span", { className: "effort-label" }, "Effort: ", value),
    track,
  ) as HTMLDivElement;

  /**
   * The one writer of position, both ARIA channels and the caption. Position is a custom property, so a growing card
   * needs no ResizeObserver. `frac` diverges from the index only while a finger is down.
   */
  function apply(index: number, frac?: number): void {
    const n = levels.length;
    const i = n === 0 ? 0 : Math.min(n - 1, Math.max(0, index));
    const level = levels[i];
    const text = level === undefined ? "" : effortLabel(level);
    const snapped = n <= 1 ? 0 : i / (n - 1);
    // On the track: the bar is its `::before`, so this is the only element both readers see.
    track.style.setProperty("--effort-frac", String(frac ?? snapped));
    knob.setAttribute("aria-valuenow", String(i));
    knob.setAttribute("aria-valuetext", text);
    knob.dataset["level"] = level?.id ?? "";
    value.textContent = text;
  }

  function shownIndex(): number {
    return Number(knob.getAttribute("aria-valuenow") ?? "0");
  }

  function pick(index: number): void {
    apply(index);
    const id = knob.dataset["level"] ?? "";
    if (id !== "") {
      opts.onPick(id);
    }
  }

  /**
   * The knob's size is read from layout, never its rect: it scales on hover and press (15-input.css), and
   * `getBoundingClientRect` would move the mapping under the pointer.
   */
  function fracAt(clientX: number): number {
    const t = track.getBoundingClientRect();
    const pad = knob.offsetLeft;
    const kw = knob.offsetWidth;
    const travel = t.width - 2 * pad - kw;
    if (travel <= 0) {
      return 0;
    }
    return Math.min(1, Math.max(0, (clientX - t.left - pad - kw / 2) / travel));
  }

  function dragging(): boolean {
    return track.dataset["dragging"] !== undefined;
  }

  function follow(clientX: number): void {
    const frac = fracAt(clientX);
    const n = levels.length;
    apply(n <= 1 ? 0 : Math.round(frac * (n - 1)), frac);
  }

  // One track handler serves both gestures. No `preventDefault()`: Firefox ties `:active` to the mousedown default.
  track.addEventListener("pointerdown", (e: PointerEvent) => {
    e.stopPropagation();
    track.setPointerCapture(e.pointerId);
    track.dataset["dragging"] = "";
    follow(e.clientX);
  });
  track.addEventListener("pointermove", (e: PointerEvent) => {
    if (dragging()) {
      follow(e.clientX);
    }
  });
  track.addEventListener("pointerup", (e: PointerEvent) => {
    if (!dragging()) {
      return;
    }
    delete track.dataset["dragging"];
    if (track.hasPointerCapture(e.pointerId)) {
      track.releasePointerCapture(e.pointerId);
    }
    // Focused here, not on pointerdown: a tap on bare track lets mousedown's default clear focus to `<body>`.
    knob.focus();
    // The snap: no `frac` and `data-dragging` gone, so the transition animates onto the tier.
    pick(shownIndex());
  });
  track.addEventListener("pointercancel", () => {
    delete track.dataset["dragging"];
    setActive(synced);
  });

  // The six keys are stopped: the card's `rovingFocus` reads no target and would move focus into the model list.
  // Everything else propagates so Escape reaches the popup.
  knob.addEventListener("keydown", (e: KeyboardEvent) => {
    const n = levels.length;
    if (n === 0) {
      return;
    }
    const cur = shownIndex();
    let next: number;
    switch (e.key) {
      case "ArrowLeft":
      case "ArrowDown":
        next = cur - 1;
        break;
      case "ArrowRight":
      case "ArrowUp":
        next = cur + 1;
        break;
      case "Home":
        next = 0;
        break;
      case "End":
        next = n - 1;
        break;
      default:
        return;
    }
    e.preventDefault();
    e.stopPropagation();
    pick(next);
  });

  function setLevels(next: readonly SessionEffortLevel[]): void {
    levels = [...next];
    track.dataset["tiers"] = String(levels.length);
    knob.setAttribute("aria-valuemax", String(Math.max(0, levels.length - 1)));
    // One dot per tier, on the positions the knob lands on: the release snaps, so the marks show where a drag ends.
    stops.replaceChildren(
      ...(levels.length <= 1
        ? []
        : levels.map((_, i) => {
            const dot = el("div", { className: "effort-stop" }) as HTMLDivElement;
            dot.style.setProperty("--effort-stop-frac", String(i / (levels.length - 1)));
            return dot;
          })),
    );
  }

  function setActive(id: string): void {
    synced = id;
    const i = levels.findIndex((l) => l.id === id);
    apply(i < 0 ? 0 : i);
  }

  return { el: row, setLevels, setActive };
}
