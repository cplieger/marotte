// Status indicators and the context bar.

import { $ } from "./dom.js";
import { formatTokens, formatMetering } from "./status-format.js";
import { humanName } from "./strings.js";
import { checkRuntimeHealth, runtimeStatusLine } from "./runtime-health.js";
import { contextStroke, tokensUsed, wedgeDash } from "./context-ring.js";
import type { CompactionPoint } from "./context-ring.js";
import { versionsSignal } from "./versions.js";
import { el, effect, touch } from "@cplieger/reactive";
import { announce } from "@cplieger/ui-primitives/announce";
import type { MeteringItem, ConnectionStatus } from "./types.js";

/** Options for updateContextBar — named fields prevent argument-order bugs. */
interface ContextBarUpdate {
  pct: number;
  contextSize: number;
  /** The effective compaction point, resolved by context-ui.ts: where the band starts (null when
   *  nothing compacts automatically) and the ramp's T. */
  compaction: CompactionPoint;
  credits: number;
  turnCount: number;
  lastTurnMs: number;
  model: string;
  /** The reasoning tier to name beside the model, or "" when there is none to name. Already
   *  resolved by context-ui.ts: empty means the model advertises no reasoning effort, or no
   *  level resolved at all. This module renders it and decides nothing about it. */
  effort?: string;
  /** The model pick awaiting its apply, from the header's `pending_model`, or "" when none is
   *  pending. Every device carries it, and it clears when a header arrives with the field empty
   *  — so the badge is a READ rather than a local queue's memory. */
  pendingModel?: string;
  metering?: MeteringItem[];
  entryCount?: number;
  toolCount?: number;
  summarizedCount?: number;
}

class ContextBarController {
  private contextBarQueued = false;
  private contextBarArgs: ContextBarUpdate | null = null;

  update(opts: ContextBarUpdate): void {
    this.contextBarArgs = opts;
    if (!this.contextBarQueued) {
      this.contextBarQueued = true;
      requestAnimationFrame(() => {
        this.contextBarQueued = false;
        if (this.contextBarArgs !== null) {
          this.updateImpl(this.contextBarArgs);
        }
      });
    }
  }

  private updateImpl(opts: ContextBarUpdate): void {
    const { pct, contextSize, compaction, credits, turnCount, lastTurnMs, model } = opts;
    const metering = opts.metering ?? [];
    const entryCount = opts.entryCount ?? 0;
    const toolCount = opts.toolCount ?? 0;
    const summarizedCount = opts.summarizedCount ?? 0;
    const clamped = Math.min(100, Math.max(0, pct));

    // pathLength="100" on the element makes the dash pattern speak in percent, so the offset IS the
    // unused remainder. The hardcoded 50.27 circumference this replaced was the ring's one magic
    // constant.
    $.contextRingFill.style.strokeDashoffset = String(100 - clamped);
    $.contextRingFill.style.stroke = contextStroke(clamped, compaction.t);
    // A band from 100% is zero-length, so a withheld band draws nothing.
    const wedge = wedgeDash(compaction.band ?? 100);
    $.contextRingWedge.style.strokeDasharray = wedge.dasharray;
    $.contextRingWedge.style.strokeDashoffset = wedge.dashoffset;
    $.contextIndicator.setAttribute("data-tooltip", ringTooltip(compaction.band));
    $.contextLabel.textContent = `${pct.toFixed(0)}%`;

    // Empty model = server-side default; label it "auto" rather than blank.
    const modelLabel = model === "" ? "auto" : humanName(model);
    const pending = opts.pendingModel ?? "";
    $.switchModelBtn.classList.toggle("pending", pending !== "");
    $.switchModelBtn.setAttribute(
      "data-tooltip",
      pending === "" ? "Switch model" : `Switch to ${humanName(pending)} after current turn`,
    );
    $.ctxModelPill.textContent = modelLabel;

    // The tier rides its OWN element, not the model label, for two reasons. The label is capped at
    // 10rem with an ellipsis, so a concatenated tier is the half that gets clipped.
    const effort = opts.effort ?? "";
    $.ctxEffortPill.textContent = effort === "" ? "" : `· ${effort}`;
    $.ctxEffortPill.classList.toggle("hidden", effort === "");
    // The button's aria-label wins over its own text, so the current selection reaches assistive
    // tech only from here. Spelled in words rather than with the separator, which a screen reader
    // reads out.
    const current =
      effort === ""
        ? `Switch model, currently ${modelLabel}`
        : `Switch model, currently ${modelLabel} at ${effort} reasoning effort`;
    $.switchModelBtn.setAttribute(
      "aria-label",
      pending === "" ? current : `${current}, switching to ${humanName(pending)} after this turn`,
    );
    $.ctxTokens.textContent =
      contextSize > 0
        ? `${formatTokens(Math.round(tokensUsed(pct, contextSize)))} / ${formatTokens(contextSize)}`
        : `${pct.toFixed(1)}%`;
    $.ctxCredits.textContent = credits > 0 ? `${credits.toFixed(2)} cr` : "0.00 cr";
    $.ctxTurns.textContent = String(turnCount);
    $.ctxLastTurn.textContent = lastTurnMs > 0 ? `${(lastTurnMs / 1000).toFixed(1)}s` : "-";
    $.ctxEntries.textContent =
      summarizedCount > 0
        ? `${String(entryCount)} (${String(summarizedCount)} summarized)`
        : String(entryCount);
    $.ctxTools.textContent = String(toolCount);

    renderMetering(metering);
  }
}

const contextBar = new ContextBarController();

/** The ring's hover text, naming where the band sits or saying there is none. */
function ringTooltip(band: number | null): string {
  const lead = "Context usage: tokens, credits and turns for this chat.";
  return band === null
    ? `${lead} Automatic compaction is off, so the conversation is compacted only when you compact it.`
    : `${lead} The grey band marks ${String(Math.round(band))}%, where the conversation is compacted.`;
}

function renderMetering(items: MeteringItem[]): void {
  const box = $.ctxMetering;
  if (items.length <= 1) {
    box.classList.add("hidden");
    box.replaceChildren();
    return;
  }
  box.classList.remove("hidden");
  const rows: HTMLElement[] = items.map((item) =>
    el(
      "span",
      { className: "pill-metering-row" },
      el(
        "span",
        { className: "pill-metering-label" },
        item.value === 1 ? item.unit_singular : item.unit_plural,
      ),
      el("span", { className: "pill-metering-value" }, formatMetering(item.value)),
    ),
  );
  box.replaceChildren(...rows);
}

let statusAnnounceTimer: ReturnType<typeof setTimeout> | undefined;

/** Maps each ConnectionStatus to its CSS class, custom property colour, and the phrase the
 *  TRIGGER publishes as its description. */
const STATUS_STYLES: Readonly<
  Record<ConnectionStatus, { cls: string | null; color: string; tip: string }>
> = {
  connected: { cls: "connected", color: "var(--c-green)", tip: "Connected" },
  disconnected: { cls: "error", color: "var(--c-red)", tip: "Disconnected" },
  connecting: { cls: null, color: "var(--c-yellow)", tip: "Connecting…" },
};

export function setStatus(s: ConnectionStatus): void {
  const dot = $.statusDot;
  dot.classList.remove("connected", "error");
  const style = STATUS_STYLES[s];
  if (style.cls) {
    dot.classList.add(style.cls);
  }
  // The expanded card tints its border and background from --status-color. It is the dot's sibling,
  // not its child (15-input.css .pill-slot), so the value lands on the card: on the dot it would
  // reach nothing.
  $.statusCard.style.setProperty("--status-color", style.color);
  // THE STATE IS THE DESCRIPTION, NEVER THE NAME. The trigger's name comes from its contents (the
  // address plus the `.sr-only` subject) and stays that shape in every state; this is the state
  // channel beside it.
  $.accountBtn.dataset["tooltip"] = style.tip;
  lastStatus = s;
  paintConnectionLine();

  // Debounced screen-reader announcement (2s stable).
  clearTimeout(statusAnnounceTimer);
  statusAnnounceTimer = setTimeout(() => {
    announce(`Connection ${s}`);
  }, 2000);
}

/** The status the transport last reported, held so the version arriving later can repaint the
 *  line without a second status change. */
let lastStatus: ConnectionStatus = "connecting";

/** The connection line: the status, plus WHICH server, once its version is known. */
function paintConnectionLine(): void {
  const build = versionsSignal().value.marotte;
  $.stWs.textContent =
    lastStatus === "connected" && build !== "" ? `connected to marotte ${build}` : lastStatus;
}

/** Repaint both card lines when the version pair lands. */
export function initStatusVersions(): void {
  effect(() => {
    // Read INSIDE the effect so it subscribes; paintConnectionLine reads it again for its own
    // value. `void` rather than a bare expression, matching forge-auth.ts: the read is the whole
    // point and the value is not wanted.
    touch(versionsSignal());
    paintConnectionLine();
    $.stKiro.textContent = runtimeStatusLine();
  });
}

export function updateContextBar(opts: ContextBarUpdate): void {
  contextBar.update(opts);
}

/** Paint the status card's agent-runtime line, then re-probe so an open card is never as stale
 *  as the last transport gap (the boot probe and the gap probe are the only other times
 *  /api/health is read). Painted twice on purpose: the cached line lands in the same frame the
 *  card opens in, and the fresh one replaces it when the probe answers. */
export function refreshRuntimeLine(): Promise<void> {
  $.stKiro.textContent = runtimeStatusLine();
  return checkRuntimeHealth().then(() => {
    $.stKiro.textContent = runtimeStatusLine();
  });
}

// The send button's face has a single owner, prompt-input.ts, and nothing here writes the
// composer's `disabled` props: a second writer fought its send-state machine on every turn
// boundary.
