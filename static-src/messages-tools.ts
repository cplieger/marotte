// Tool-card rendering: the slot registry, per-tool signal effects, terminal output routing and the DOM update
// helpers; messages.ts injects its state via init(). The disable below: the wire decoder marks only required fields
// non-optional and status updates arrive with subsets, so the runtime checks are needed.
/* eslint-disable @typescript-eslint/no-unnecessary-condition */

import type { ToolCall, ToolStatus, ToolDiff, TextSpan } from "./types.js";
import { ensureToolCallSig, toolCallSigs, toolCallSigKey } from "./store-signals.js";
import { effect, el } from "@cplieger/reactive";

import { maybeCollapseGroup } from "./tool-group.js";
import { isToolDone, commandTitle, toolTitleText, type ToolKind } from "./tool-schema.js";
import {
  buildToolCard,
  insertDiffPreview,
  expandToolDetails,
  applyOutcome,
  refreshToolDisclosure,
  syncInteractionFact,
  syncOffloadLink,
  syncSilenceMarker,
} from "./tool-card.js";
import { clearToolSilence, forgetToolSilence } from "./tool-silence.js";
import { toolCardOptsFor } from "./tool-card-opts.js";
import { iconEl } from "./icon-el.js";
import { ICON_SPARKLE } from "./icons.js";
import { windowOutput, windowSpans } from "./strings.js";
import { renderOutput, appendOutput as appendOutputChunk } from "./output-render.js";
import { CHROME_ATTR } from "./chrome-attr.js";
import { bindLoadingState } from "./actions/index.js";

// ---------------------------------------------------------------------------
// Module state (tool-specific)
// ---------------------------------------------------------------------------

/**
 * Tool-call DOM slots, keyed on `toolCallSigKey(chatID, toolID)`: tool ids carry no cross-chat uniqueness and
 * parked views stay resident. A multimap, one slot per mounted render identified by its card, so mounting a second
 * render never evicts the first.
 */
interface ToolSlot {
  chatID: string;
  toolID: string;
  card: HTMLDivElement;
  /** The live effect's disposer, or null while suspended (view parked). */
  cleanup: (() => void) | null;
}
const toolSlots = new Map<string, ToolSlot[]>();

/** Every slot for one composite key. The empty array is never stored. */
function slotsOf(chatID: string, toolID: string): ToolSlot[] {
  return toolSlots.get(toolCallSigKey(chatID, toolID)) ?? [];
}

/**
 * Remove one slot, running its effect; the last slot for a key takes the signal, terminal links and parked buffer
 * with it. Idempotent by card identity, since a render's disposer and the view sweep can both reach it.
 */
function removeSlot(
  chatID: string,
  toolID: string,
  card: HTMLDivElement,
  keepTerminals = false,
): void {
  const key = toolCallSigKey(chatID, toolID);
  const slots = toolSlots.get(key);
  if (slots === undefined) {
    return;
  }
  const i = slots.findIndex((s) => s.card === card);
  if (i === -1) {
    return;
  }
  const [slot] = slots.splice(i, 1);
  slot?.cleanup?.();
  if (slots.length > 0) {
    return;
  }
  toolSlots.delete(key);
  toolCallSigs.clear(toolCallSigKey(chatID, toolID));
  if (keepTerminals) {
    return;
  }
  for (const [termID, link] of [...termToTool]) {
    if (link.chatID === chatID && link.toolID === toolID) {
      termToTool.delete(termID);
      parkedTermBuffers.delete(termID);
    }
  }
}

/**
 * A terminal id's claiming tool call and owning chat (the slot lookup needs both). The key stays the bare
 * terminal id, which marotte mints globally unique.
 */
interface TermLink {
  chatID: string;
  toolID: string;
}

/** terminal_id to its claiming tool call, from ToolCall.terminal_id on the ACP `terminal` content block. */
const termToTool = new Map<string, TermLink>();

/**
 * Live chunks held per terminal id until a card claims it: KAS emits a terminal's first output before the
 * tool_call_update naming it, so without the hold a command's opening lines would appear only at completion.
 */
interface PendingHold {
  chunks: { text: string; spans: TextSpan[]; base: number }[];
  /** Tracked rather than recomputed, so appending is linear. */
  chars: number;
  /** Set once a chunk did not fit; nothing is accepted after, so the hold is always a contiguous prefix. */
  full: boolean;
}
const pendingChunks = new Map<string, PendingHold>();

/** Held characters per terminal; the completion snapshot is authoritative, so the hold covers the opening only. */
const PENDING_CHARS_CAP = 64 * 1024;

/**
 * How many terminals may be held at once. An unlinked terminal is otherwise never evicted, so at the cap the
 * oldest hold drops; the newest most likely still finds its card.
 */
const PENDING_TERMINALS_CAP = 16;

/**
 * The live text a card's output may hold before the completion snapshot. Trimming keeps the tail, since a build's
 * last lines say how it ended.
 */
const LIVE_OUTPUT_CHARS_CAP = 256 * 1024;

/**
 * Whether a card sits inside a parked view, injected by messages.ts, which this module cannot import. Per card,
 * since the subagent page renders the same pairs outside any transcript view.
 */
let _isCardParked: (card: HTMLElement) => boolean = () => false;

/**
 * Live terminal output that arrived while its card's view was parked, drained once at resume. Drop-oldest, as a
 * shell scrollback, unlike the pre-claim hold's prefix: this stands in for a live tail.
 */
interface ParkedTermBuffer {
  chunks: { text: string; spans: TextSpan[]; base: number }[];
  /** Characters held; tracked so overflow trimming is linear. */
  chars: number;
}
const parkedTermBuffers = new Map<string, ParkedTermBuffer>();

/** The opening lines a settled call took while one of its cards was parked; the settle consumed the hold. */
interface OwedPrefix {
  chatID: string;
  toolID: string;
  chunks: { text: string; spans: TextSpan[]; base: number }[];
}
const owedPrefixes = new Map<string, OwedPrefix>();

/** Characters buffered per parked terminal; the buffer only bridges the parked window. */
const PARKED_TERM_CHARS_CAP = 64 * 1024;

function bufferParkedChunk(termID: string, text: string, spans: TextSpan[], base: number): void {
  let buf = parkedTermBuffers.get(termID);
  if (buf === undefined) {
    buf = { chunks: [], chars: 0 };
    parkedTermBuffers.set(termID, buf);
  }
  buf.chunks.push({ text, spans, base });
  buf.chars += text.length;
  // Whole-chunk granularity: spans are rebased per chunk, so splitting one would mean re-deriving offsets.
  while (buf.chars > PARKED_TERM_CHARS_CAP && buf.chunks.length > 1) {
    const dropped = buf.chunks.shift();
    buf.chars -= dropped?.text.length ?? 0;
  }
}

/**
 * Drain `chatID`'s parked buffers into the unparked view's cards, once, writing directly: `appendTerminalChunk`'s
 * fan-out would double-write a card on a live surface.
 */
export function drainParkedTerminals(chatID: string, withinEl: HTMLElement): void {
  for (const [key, owed] of [...owedPrefixes]) {
    if (owed.chatID !== chatID) {
      continue;
    }
    owedPrefixes.delete(key);
    for (const slot of slotsOf(owed.chatID, owed.toolID)) {
      // A card holding output has the prefix or a snapshot that supersedes it.
      if (outputText(slot.card) !== "") {
        continue;
      }
      for (const chunk of owed.chunks) {
        writeChunkToCard(slot.card, chunk.text, chunk.spans, chunk.base);
      }
    }
  }
  for (const [termID, buf] of [...parkedTermBuffers]) {
    const link = termToTool.get(termID);
    if (link?.chatID !== chatID) {
      continue;
    }
    parkedTermBuffers.delete(termID);
    for (const slot of slotsOf(link.chatID, link.toolID)) {
      if (!withinEl.contains(slot.card)) {
        continue;
      }
      for (const chunk of buf.chunks) {
        writeChunkToCard(slot.card, chunk.text, chunk.spans, chunk.base);
      }
    }
  }
}

export function initToolViewCallbacks(cbs: { isCardParked: (card: HTMLElement) => boolean }): void {
  _isCardParked = cbs.isCardParked;
}

export function disposeAllToolEffects(): void {
  for (const slots of toolSlots.values()) {
    for (const slot of slots) {
      slot.cleanup?.();
      toolCallSigs.clear(toolCallSigKey(slot.chatID, slot.toolID));
    }
  }
  toolSlots.clear();
  termToTool.clear();
  pendingChunks.clear();
  parkedTermBuffers.clear();
  owedPrefixes.clear();
  clearToolSilence();
}

/** Dispose one render's slot for a tool call, by card identity, against that render's lifetime. */
export function disposeToolSlot(chatID: string, toolID: string, card: HTMLDivElement): void {
  removeSlot(chatID, toolID, card);
}

/**
 * The view-level sweep of one chat's tool state inside `withinEl`; a card on another surface is its own render's
 * to dispose. Idempotent with the per-render disposers.
 */
export function disposeToolEffectsForChat(chatID: string, withinEl?: HTMLElement): void {
  for (const slots of [...toolSlots.values()]) {
    for (const slot of [...slots]) {
      if (slot.chatID !== chatID) {
        continue;
      }
      if (withinEl !== undefined && !withinEl.contains(slot.card)) {
        continue;
      }
      removeSlot(slot.chatID, slot.toolID, slot.card);
    }
  }
}

/**
 * The rebuild's sweep: cards go, but terminal owners, parked tails and live text survive for the remount. Call the
 * returned function after the remount to move the text and release unclaimed terminals.
 */
export function detachToolEffectsForRebuild(chatID: string, withinEl: HTMLElement): () => void {
  const linked = new Set<string>();
  for (const link of termToTool.values()) {
    if (link.chatID === chatID) {
      linked.add(link.toolID);
    }
  }
  const kept = new Map<string, HTMLPreElement>();
  for (const slots of [...toolSlots.values()]) {
    for (const slot of [...slots]) {
      if (slot.chatID !== chatID || !withinEl.contains(slot.card)) {
        continue;
      }
      const pre = slot.card.querySelector<HTMLPreElement>(".tool-output pre");
      if (linked.has(slot.toolID) && pre !== null && !kept.has(slot.toolID)) {
        kept.set(slot.toolID, pre);
      }
      removeSlot(slot.chatID, slot.toolID, slot.card, true);
    }
  }
  return () => {
    for (const [toolID, pre] of kept) {
      const slot = slotsOf(chatID, toolID).find((s) => withinEl.contains(s.card));
      const out = slot?.card.querySelector(".tool-output") ?? null;
      if (slot === undefined || out === null || outputText(slot.card) !== "") {
        continue;
      }
      out.querySelector("pre")?.remove();
      out.appendChild(pre);
      refreshToolDisclosure(slot.card);
    }
    for (const [termID, link] of [...termToTool]) {
      if (link.chatID === chatID && slotsOf(chatID, link.toolID).length === 0) {
        termToTool.delete(termID);
      }
    }
    for (const termID of [...parkedTermBuffers.keys()]) {
      if (!termToTool.has(termID)) {
        parkedTermBuffers.delete(termID);
      }
    }
  };
}

/**
 * Suspend the live effects for the named calls in the parking view: the slot, card and signal stay, so updates
 * land in the store and resume re-reads the latest snapshot.
 */
export function suspendToolEffectsFor(
  chatID: string,
  toolIDs: readonly string[],
  withinEl: HTMLElement,
): void {
  for (const toolID of toolIDs) {
    for (const slot of slotsOf(chatID, toolID)) {
      if (slot.cleanup === null || !withinEl.contains(slot.card)) {
        continue; // already suspended, or another surface's slot
      }
      slot.cleanup();
      slot.cleanup = null;
    }
  }
}

/**
 * Re-arm suspended effects: apply the current snapshot in one write, then subscribe. Already-live slots are left
 * alone, so a resume is idempotent.
 */
export function resumeToolEffectsFor(
  chatID: string,
  toolCalls: readonly ToolCall[],
  withinEl: HTMLElement,
): void {
  for (const tc of toolCalls) {
    for (const slot of slotsOf(chatID, tc.id)) {
      if (slot.cleanup !== null || !withinEl.contains(slot.card)) {
        continue; // already live, or another surface's slot
      }
      const card = slot.card;
      const sig = ensureToolCallSig(chatID, tc.id, tc);
      let lastApplied = sig.peek();
      applyToolCallUpdate(card, lastApplied, chatID);
      slot.cleanup = effect(() => {
        const next = sig.value;
        if (next === lastApplied) {
          return;
        }
        applyToolCallUpdate(card, next, chatID);
        lastApplied = next;
      });
    }
  }
}

/**
 * Record a tool call's terminal link and settle any hold. Only an in-flight card becomes the live sink, never
 * displacing another in-flight owner. A hold is flushed only when the call carries no output, since that output is
 * the whole-stream snapshot; it is dropped either way.
 */
function linkTerminal(chatID: string, tc: ToolCall): void {
  const termID = tc.terminal_id;
  if (termID === undefined || termID === "") {
    return;
  }
  const existing = termToTool.get(termID);
  const owns = existing?.chatID === chatID && existing.toolID === tc.id;
  if (!toolInFlight(tc.status)) {
    if (owns) {
      termToTool.delete(termID);
    }
    settleHold(chatID, tc, termID);
    return;
  }
  if (existing !== undefined) {
    return;
  }
  termToTool.set(termID, { chatID, toolID: tc.id });
  const held = pendingChunks.get(termID);
  if (held === undefined) {
    return;
  }
  pendingChunks.delete(termID);
  if (tc.output !== undefined && tc.output !== "") {
    return;
  }
  for (const chunk of held.chunks) {
    appendTerminalChunk(termID, chunk.text, chunk.spans, chunk.base);
  }
}

function toolInFlight(status: ToolCall["status"]): boolean {
  return status === "pending" || status === "in_progress";
}

/** A settled card claims no sink but still takes a held prefix when it carries no snapshot. */
function settleHold(chatID: string, tc: ToolCall, termID: string): void {
  const held = pendingChunks.get(termID);
  if (held === undefined) {
    return;
  }
  pendingChunks.delete(termID);
  if (tc.output !== undefined && tc.output !== "") {
    return;
  }
  let parked = false;
  for (const slot of slotsOf(chatID, tc.id)) {
    if (_isCardParked(slot.card)) {
      parked = true;
      continue;
    }
    for (const chunk of held.chunks) {
      writeChunkToCard(slot.card, chunk.text, chunk.spans, chunk.base);
    }
  }
  if (!parked) {
    return;
  }
  const key = toolCallSigKey(chatID, tc.id);
  owedPrefixes.delete(key);
  while (owedPrefixes.size >= PENDING_TERMINALS_CAP) {
    const oldest = owedPrefixes.keys().next();
    if (oldest.done === true) {
      break;
    }
    owedPrefixes.delete(oldest.value);
  }
  owedPrefixes.set(key, { chatID, toolID: tc.id, chunks: held.chunks });
}

function outputText(card: HTMLDivElement): string {
  return card.querySelector(".tool-output pre")?.textContent ?? "";
}

/**
 * Forget a terminal on terminal_exited, releasing an unclaimed hold. A parked buffer is kept: its view drains it
 * at resume.
 */
export function forgetTerminal(termID: string): void {
  pendingChunks.delete(termID);
}

/**
 * Append one live terminal_output chunk to the card that spawned it. Provisional: at completion the server's
 * full output replaces it, so a lost chunk costs smoothness, never the record.
 */
export function appendTerminalChunk(
  termID: string,
  text: string,
  spans: TextSpan[],
  base: number,
): void {
  const link = termToTool.get(termID);
  if (link === undefined) {
    holdChunk(termID, text, spans, base);
    return;
  }
  // Fan out to every mounted render. A parked card must not move; a live one takes the chunk now. Buffer once when
  // any card was parked, since the unpark drain writes those cards directly.
  let parked = false;
  for (const slot of slotsOf(link.chatID, link.toolID)) {
    if (_isCardParked(slot.card)) {
      parked = true;
      continue;
    }
    writeChunkToCard(slot.card, text, spans, base);
  }
  if (parked) {
    bufferParkedChunk(termID, text, spans, base);
  }
}

/** Append to the card's output region and keep the tail under the live cap. */
function writeChunkToCard(
  card: HTMLDivElement,
  text: string,
  spans: TextSpan[],
  base: number,
): void {
  const out = card.querySelector(".tool-output");
  if (out === null) {
    return;
  }
  // The snapshot rendering owns the <pre>; create one only if no update has built it.
  const existing = out.querySelector("pre");
  const pre = existing ?? (el("pre") as HTMLPreElement);
  if (existing === null) {
    out.appendChild(pre);
  }
  appendOutputChunk(pre, text, spans, base);
  trimToLiveCap(pre);
  refreshToolDisclosure(card);
}

function holdChunk(termID: string, text: string, spans: TextSpan[], base: number): void {
  let held = pendingChunks.get(termID);
  if (held === undefined) {
    // Map iterates in insertion order, so the first key is the oldest terminal.
    while (pendingChunks.size >= PENDING_TERMINALS_CAP) {
      const oldest = pendingChunks.keys().next();
      if (oldest.done === true) {
        break;
      }
      pendingChunks.delete(oldest.value);
    }
    held = { chunks: [], chars: 0, full: false };
    pendingChunks.set(termID, held);
  }
  if (held.full || held.chars + text.length > PENDING_CHARS_CAP) {
    // Refuse everything after the first chunk that did not fit, keeping a contiguous prefix: chunks are rebased by
    // their own `base`, so a hole would render its two sides as adjacent.
    held.full = true;
    return;
  }
  held.chunks.push({ text, spans, base });
  held.chars += text.length;
}

/** Drop leading nodes until under the live cap, keeping the tail. Whole nodes keep each span's styling intact. */
function trimToLiveCap(pre: HTMLElement): void {
  let total = pre.textContent?.length ?? 0;
  while (total > LIVE_OUTPUT_CHARS_CAP) {
    const first = pre.firstChild;
    if (first === null) {
      return;
    }
    total -= first.textContent?.length ?? 0;
    first.remove();
  }
}

// ---------------------------------------------------------------------------
// Callbacks injected by messages.ts at init time
// ---------------------------------------------------------------------------

let _pushBind: (key: string, unbind: () => void) => void = () => {
  /* default until init */
};
let _refreshGroupHeader: (group: HTMLElement) => void = () => {
  /* default until init */
};
let _explainError: (errorText: string, toolTitle: string) => Promise<string> = () =>
  Promise.resolve("");

export function initToolCallbacks(cbs: {
  pushBind: (key: string, unbind: () => void) => void;
  refreshGroupHeader: (group: HTMLElement) => void;
  explainError: (errorText: string, toolTitle: string) => Promise<string>;
}): void {
  _pushBind = cbs.pushBind;
  _refreshGroupHeader = cbs.refreshGroupHeader;
  _explainError = cbs.explainError;
}

// ---------------------------------------------------------------------------
// Tool-card mounting
// ---------------------------------------------------------------------------

/**
 * Mount one tool call's card and register this render's slot; subagent grouping is handled in messages-blocks.ts.
 * Takes the chat because the signal is keyed on (chat, call), so mount and writer must agree or the card never
 * updates. The caller disposes the slot (`disposeToolSlot`); the view sweep is the backstop.
 */
export function mountToolCallCard(
  chatID: string,
  tc: ToolCall,
  detailsOpen = false,
): HTMLDivElement {
  // A previewed call's bulk is addressed by (chat, call): `GET /api/chats/{id}/tools/{toolCallID}`.
  const opts = toolCardOptsFor(tc, true, chatID);
  if (detailsOpen) {
    // Not in `toolCardOptsFor`: an open card is a fact about the render, not the tool call.
    opts.detailsOpen = true;
  }
  const card = buildToolCard(opts);
  const key = toolCallSigKey(chatID, tc.id);
  const slots = toolSlots.get(key) ?? [];
  toolSlots.set(key, slots);
  const slot: ToolSlot = { chatID, toolID: tc.id, card, cleanup: null };
  slots.push(slot);
  // After the build and the slot registration: a held chunk is appended to the slots' cards, and whether it
  // flushes depends on the output this build rendered.
  linkTerminal(chatID, tc);

  const sig = ensureToolCallSig(chatID, tc.id, tc);
  let lastApplied = tc;
  slot.cleanup = effect(() => {
    const next = sig.value;
    // Before the early return: the effect must subscribe to the silence ticker, since a quiet call sends no frame.
    syncSilenceMarker(card, chatID, tc.id);
    if (next === lastApplied) {
      return;
    }
    applyToolCallUpdate(card, next, chatID);
    lastApplied = next;
  });
  return card;
}

// ---------------------------------------------------------------------------
// Public helpers
// ---------------------------------------------------------------------------

/** Apply a ToolCall snapshot to a mounted card. Reads no signal; `chatID` stamps the terminal link's owner. */
export function updateToolCall(el: HTMLElement, tc: ToolCall, chatID: string): void {
  applyToolCallUpdate(el as HTMLDivElement, tc, chatID);
}

// ---------------------------------------------------------------------------
// Internal helpers
// ---------------------------------------------------------------------------

/** Apply a ToolCall snapshot's updatable fields to its DOM card. Idempotent. */
function applyToolCallUpdate(el: HTMLDivElement, tc: ToolCall, chatID: string): void {
  linkTerminal(chatID, tc);
  if (tc.title !== undefined) {
    applyTitleUpdate(el, tc);
  }
  // Before the status, since `applyOutcome` reads it off the dataset on the same frame. Only ever set: the wire field
  // is `omitempty`, so absent means unchanged.
  if (tc.declined === true) {
    el.dataset["declined"] = "1";
  }
  // Status is applied last: the Explain gate and the bare-disclosure predicate read `.tool-output` back from the
  // DOM, and a terminal frame often carries both. The gate trims, like both of them.
  if (tc.output !== undefined && tc.output.trim() !== "") {
    applyOutputUpdate(el, tc.output, tc.output_spans ?? []);
  }
  if (tc.diffs !== undefined && tc.diffs.length > 0) {
    applyDiffUpdate(el, tc.diffs);
  }
  syncOffloadLink(el, tc.offload);
  syncInteractionFact(el, tc.interaction);
  if (tc.status !== undefined) {
    applyStatusUpdate(el, tc.status, tc.id);
    if (isToolDone(tc.status)) {
      // A settle is the one event saying no further frame is coming; another surface may still hold a card.
      forgetToolSilence(chatID, tc.id);
    }
  }
}

function applyStatusUpdate(card: HTMLDivElement, status: ToolStatus, toolId: string): void {
  // The outcome glyph, through the one writer that owns that vocabulary.
  applyOutcome(card, status, card.dataset["title"] ?? "", {
    kind: (card.dataset["kind"] ?? "other") as ToolKind,
    writesFile: false,
    filePath: card.dataset["filePath"] ?? "",
    fileBasename: card.dataset["filename"] ?? "",
    diffSources: null,
    mcp: null,
    // From the DOM on this path: applyOutcome reads dataset.denied, and a disclosed claim was written at build.
    disclosed: null,
    denial: null,
  });
  refreshToolDisclosure(card);
  const done = isToolDone(status);
  if (done) {
    card.querySelector(".tool-spinner")?.remove();
    // `data-start-ms` means in flight, and `autoCollapseGroup` refuses to fold a group holding one, so every settle
    // drops it.
    delete card.dataset["startMs"];
    maybeCollapseGroup(card);
    const group = card.closest(".tool-group");
    if (group !== null) {
      _refreshGroupHeader(group as HTMLElement);
    }
  }
  if (card.dataset["declined"] === "1") {
    // A refusal's reason is its output, so the region opens; no Explain button, since nothing broke.
    expandToolDetails(card);
  }
  if (status === "failed") {
    // Failed tools open their details; `expandToolDetails` refuses a bare card, so there is no gate here.
    expandToolDetails(card);
    if (card.querySelector(".tool-explain-btn") === null) {
      const output = card.querySelector(".tool-output")?.textContent ?? "";
      if (output.trim() !== "") {
        // Icon-only, so the name and hover text share one local. `data-tooltip`, not `title`: the delegated tooltip
        // publishes `aria-describedby`, where a UA `title` reaches mouse users alone.
        const label = "Explain this error";
        const btn = el(
          "button",
          {
            type: "button",
            className: "tool-explain-btn",
            "aria-label": label,
            "data-tooltip": label,
            [CHROME_ATTR]: "",
          },
          // The sparkle every model-asking button carries; its export records why it is a path.
          iconEl(ICON_SPARKLE),
        ) as HTMLButtonElement;
        _pushBind(
          toolId,
          bindLoadingState("messages.explain_error", btn, { pendingClass: "btn-loading" }),
        );
        btn.addEventListener("click", () => {
          void _explainError(output, card.dataset["title"] ?? "").then((explanation) => {
            if (explanation !== "") {
              btn.textContent = explanation;
              btn.className = "tool-explain-result";
              // `aria-label` wins over content, so one left on the result would announce "Explain this error" instead.
              btn.removeAttribute("aria-label");
              btn.removeAttribute("data-tooltip");
            }
          });
        });
        card.appendChild(btn);
      }
    }
  }
}

function applyTitleUpdate(el: HTMLDivElement, tc: ToolCall): void {
  // A disclosed skill/agent names the document that entered the prompt; keep that through routine title frames.
  if (el.dataset["disclosed"] !== undefined) {
    return;
  }
  const t = el.querySelector(".tool-title");
  if (t !== null) {
    const display = commandTitle(tc.title, tc.kind, tc.input) ?? toolTitleText(tc.title);
    t.textContent = display;
    const header = t.closest<HTMLElement>(".tool-header");
    if (header !== null) {
      header.title = display;
    }
    // Status updates read this for the accessible outcome label.
    el.dataset["title"] = display;
  }
}

/**
 * Replace the card's output with the latest cumulative output: kiro-cli sends the full output-so-far on every
 * tool_call_update, so appending would compound it. Exported for unit testing.
 */
export function applyOutputUpdate(
  card: HTMLDivElement,
  output: string,
  spans: readonly TextSpan[] = [],
): void {
  const out = card.querySelector(".tool-output");
  if (out === null) {
    return;
  }
  // Windowed here too: streaming the middle of a long build would undo the window, and the reveal re-offers it.
  const windowed = card.dataset["depth1"] === "output";
  const shown = windowed
    ? windowOutput(output)
    : { text: output, elided: 0, kept: [{ from: 0, to: output.length, at: 0 }] };

  const existingPre = out.querySelector("pre");
  const pre = existingPre ?? el("pre");
  const paint = (text: string, s: readonly TextSpan[]): void => {
    renderOutput(pre, text, s);
  };
  paint(shown.text, windowSpans(spans, shown.kept));
  if (existingPre === null) {
    out.appendChild(pre);
  }

  // Rebuild the reveal so its count tracks the growing output.
  out.querySelector(".tool-output-reveal")?.remove();
  if (shown.elided > 0) {
    const reveal = el(
      "button",
      { type: "button", className: "tool-output-reveal", [CHROME_ATTR]: "" },
      `Show ${String(shown.elided)} more line${shown.elided === 1 ? "" : "s"}`,
    );
    reveal.addEventListener("click", (e: Event) => {
      e.stopPropagation();
      paint(output, spans);
      reveal.remove();
    });
    out.appendChild(reveal);
  }

  refreshToolDisclosure(card);
}

function applyDiffUpdate(el: HTMLDivElement, diffs: ToolDiff[]): void {
  if (el.querySelector(".tool-diff-preview") !== null) {
    return;
  }
  const d = diffs[0];
  if (d === undefined) {
    return;
  }
  insertDiffPreview(el, d.path, { oldText: d.old_text ?? "", newText: d.new_text });
}
