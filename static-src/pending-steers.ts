import { el, computed, effect, touch, untracked } from "@cplieger/ui";
import { join } from "@cplieger/keyenc";
import { announce } from "@cplieger/ui/announce";
import { reconcile, type ReconcileSpec } from "./reconcile.js";
import { $ } from "./dom.js";
import {
  activeSession,
  get,
  getActiveId,
  hasMessage,
  hasQueued,
  isBusy,
  sessionBusy,
} from "./store.js";
import {
  cancelCompact,
  cancelTurn,
  clearSteers,
  queuePrompt,
  removeSteer,
  sendQueuedNow,
  unqueuePrompt,
} from "./actions/chat.js";
import { setComposerValue } from "./composer-value.js";
import { composerDraft, restoreFailedSend, restoreRefusedEdit } from "./composer-state.js";
import { addAttachmentTo, removeAttachmentFrom } from "./attachments.js";
import { confirm } from "./confirm.js";
import { chatNotice, subjectName } from "./notice-subject.js";
import {
  ICON_ARROW_UP,
  ICON_COMPACT,
  ICON_HOURGLASS,
  ICON_EDIT,
  ICON_QUEUE,
  ICON_TRASH,
} from "./icons.js";
import { iconEl } from "./icon-el.js";
import { sentBy } from "./sent-by.js";
import { wireSignature } from "./paint-sig.js";
import { sessionDrain, turnsPhrase, type QueuedSlot } from "./queue-slots.js";
import { truncate } from "./strings.js";
import { newMessageID } from "./transport.js";
import type { PendingSteer, QueuedPrompt } from "./types.js";
import { readQuoted, splitLeadingQuote } from "./message-quote.js";
import { buildQuoteBand } from "./quote-band.js";

/** The chat's compaction, queued for a turn's end or already running. */
interface CompactSlot {
  /** A turn runs now, so it waits for this turn's end rather than the next one's. */
  readonly afterTurn: boolean;
  /** Its call is in flight: nothing cancels it, so the row has no control. */
  readonly compacting: boolean;
}

/** One dock row: a steer the agent has not read, the queued compaction, or a follow-up the server holds. */
type DockRow =
  | { kind: "steer"; steer: PendingSteer }
  | { kind: "compact"; slot: CompactSlot }
  | { kind: "queued"; slot: QueuedSlot };

export interface SteerStackSource {
  /** What the dock is about (a chat id, or a run step's key) and its rows. Reactive: the stack's
   *  render subscribes through it. Only a chat has a queued compaction. */
  read(): {
    id: string;
    steers: readonly PendingSteer[];
    queued: readonly QueuedSlot[];
    compact?: CompactSlot;
  };
  /** Stop the turn and send this row first; absent where no turn is the reader's to stop. */
  sendNow?: (steerID: string) => Promise<void>;
  /** Take the row out of the turn and hold it as a follow-up; absent where there is no follow-up
   *  queue (a run step). */
  queueInstead?: (steer: PendingSteer) => Promise<void>;
  /** Edit: put the row's words in the box, then delete the row. */
  edit(steer: PendingSteer): Promise<void>;
  /** Edit beside a workflow's row, where the only removal is the whole buffer. */
  editByClear(steer: PendingSteer): Promise<void>;
  remove(steerID: string): Promise<void>;
  discard(waiting: number): Promise<void>;
  /** The live-region words, which name what the rows are waiting for. */
  readonly waitingFor: string;
}

interface StackState {
  waiting: number;
  /** No workflow result is in the dock, so each row can go on its own: the server refuses a
   *  one-row delete beside an agent row (`agent_rows_waiting`). */
  perRow: boolean;
  /** No row is delivering: the server refuses every whole-buffer clear while one is. */
  clearable: boolean;
  /** A compaction runs with no turn, so every steer waits for its end. */
  behindCompaction: boolean;
  src: SteerStackSource;
}

const mountedStacks = new WeakSet<HTMLUListElement>();

/** Called once from app.ts. */
export function initPendingSteers(): void {
  mountSteerStack($.steerStack, chatSource);
}

export function mountSteerStack(stack: HTMLUListElement, src: SteerStackSource): void {
  if (mountedStacks.has(stack)) {
    return;
  }
  mountedStacks.add(stack);
  const announced = { waiting: 0, id: "" };
  const sig = computed(() => {
    const s = src.read();
    const compact = s.compact === undefined ? "" : compactStage(s.compact);
    return join(
      s.id,
      join(...s.steers.map(steerSig)),
      compact,
      join(...s.queued.map(wireSignature)),
    );
  });
  effect(() => {
    touch(sig);
    render(stack, src, announced);
  });
}

/** Total over `PendingSteer`, so a new field fails the type check here. `kas` is the one field held
 *  empty: a resubmit moves only the id KAS holds a row under, and that must repaint nothing. */
function steerSig(e: PendingSteer): string {
  const parts: Record<keyof PendingSteer, string> = {
    id: e.id,
    text: e.text,
    origin: e.origin,
    pending: e.pending === true ? "1" : "",
    compacted: e.compacted === true ? "1" : "",
    unsent: e.unsent === true ? "1" : "",
    delivering: e.delivering === true ? "1" : "",
    awaitsPrompt: e.awaitsPrompt === true ? "1" : "",
    caller: e.caller === undefined ? "" : sentBy(e.caller),
    kas: "",
  };
  return join(...Object.values(parts));
}

function render(
  stack: HTMLUListElement,
  src: SteerStackSource,
  announced: { waiting: number; id: string },
): void {
  const { id, steers, queued, compact } = untracked(() => src.read());
  const state: StackState = {
    waiting: steers.filter((e) => e.pending !== true).length,
    perRow: steers.every((e) => e.origin === "user"),
    clearable: !steers.some((e) => e.delivering === true),
    behindCompaction: compact?.compacting === true && !compact.afterTurn,
    src,
  };
  const waiting = state.waiting;
  const steerRows = steers.map((steer): DockRow => ({ kind: "steer", steer }));
  const compactRow: DockRow[] = compact === undefined ? [] : [{ kind: "compact", slot: compact }];
  // Top to bottom is send order: a running turn reads its steers before its end compacts, and with
  // no turn the compaction's end sends them (drainSlots).
  const rows: DockRow[] = [
    ...(state.behindCompaction ? [...compactRow, ...steerRows] : [...steerRows, ...compactRow]),
    ...queued.map((slot): DockRow => ({ kind: "queued", slot })),
  ];

  if (rows.length === 0) {
    reconcile(stack, [], rowSpec(state));
    stack.classList.add("hidden");
    // Reset the announce baseline so arriving at a chat that already has steers reads them out
    // fresh, while the empty case stays silent.
    announced.waiting = 0;
    announced.id = id;
    return;
  }
  // Un-hidden BEFORE the rows land, because `.hidden` is `display: none` and an element inserted
  // into a subtree with no box has its first style resolution there — which is the one the entry
  // transition below reads.
  stack.classList.remove("hidden");
  // Arrival order, so the newest sits at the bottom nearest the box it was typed into and every
  // earlier one moves up.
  reconcile(stack, rows, rowSpec(state));

  // Announce only on the same chat, and only the WAITING count — the number the user is waiting to
  // see fall. A pure chat switch is not news.
  if (id === announced.id && waiting !== announced.waiting) {
    announce(
      waiting === 0
        ? `Steering message delivered to ${src.waitingFor}`
        : waiting === 1
          ? `1 steering message waiting for ${src.waitingFor}`
          : `${String(waiting)} steering messages waiting for ${src.waitingFor}`,
    );
  }
  announced.waiting = waiting;
  announced.id = id;
}

/** KEYED BY THE ROW'S ID, which is stable from send to read: `.steer-row`'s `@starting-style`
 *  entry (26-dock.css) re-fires on a REBUILT node, so a re-keyed row would vanish and fade back
 *  in. The spec is built per render because `state` is stack-wide. */
function rowSpec(state: StackState): ReconcileSpec<DockRow> {
  return {
    // Prefixed by kind so the id spaces cannot meet in one keyed list; a chat holds one compaction at most.
    key: (r) => {
      switch (r.kind) {
        case "steer":
          return `s:${r.steer.id}`;
        case "compact":
          return "c:";
        case "queued":
          return `q:${r.slot.row.id}`;
      }
    },
    mount: (r) => {
      switch (r.kind) {
        case "steer":
          return buildRow(r.steer, state);
        case "compact":
          return buildCompactRow(r.slot);
        case "queued":
          return buildQueuedRow(r.slot);
      }
    },
    update: (el, r) => {
      switch (r.kind) {
        case "steer":
          updateRow(el, r.steer, state);
          return;
        case "compact":
          updateCompactRow(el, r.slot);
          return;
        case "queued":
          updateQueuedRow(el, r.slot);
      }
    },
  };
}

function rowState(steer: PendingSteer): string {
  if (steer.pending === true) {
    return "sending";
  }
  if (steer.delivering === true) {
    return "delivering";
  }
  return steer.unsent === true ? "unsent" : "sent";
}

/** `compacted` is a latch the store only sets, so it is written and never cleared. */
function updateRow(row: HTMLElement, steer: PendingSteer, state: StackState): void {
  row.dataset["state"] = rowState(steer);
  if (steer.compacted === true) {
    row.dataset["compacted"] = "";
  }
  row.dataset["tooltip"] = readQuoted(steer.text);
  row.setAttribute("aria-label", accessibleName(steer, state.behindCompaction));

  const label = row.querySelector(".steer-state-label");
  if (label !== null) {
    label.textContent = stateWord(steer, state.behindCompaction);
  }

  syncText(row, steer.text);
  syncActions(row, steer, state);
}

/** No path in the store rewrites the text of an id it already holds, so this is the update being
 *  TOTAL over the item rather than a live case. */
function syncText(row: HTMLElement, text: string): void {
  const textEl = row.querySelector<HTMLElement>(".steer-text");
  if (textEl !== null) {
    fillText(textEl, text);
  }
}

const shownText = new WeakMap<HTMLElement, string>();

/** A leading quote as the band a sent prompt draws, then the rest on one line; rebuilt only when the
 *  text changes, so a focused path link in the band survives a repaint. */
function fillText(textEl: HTMLElement, text: string): void {
  if (shownText.get(textEl) === text) {
    return;
  }
  shownText.set(textEl, text);
  const quoted = splitLeadingQuote(text.trim());
  textEl.replaceChildren(
    ...(quoted === null ? [] : [buildQuoteBand(quoted)]),
    oneLine(quoted?.rest ?? text),
  );
}

const actionSig = new WeakMap<HTMLElement, string>();

/** Build, replace or remove the controls, and leave them alone when no input moved: replacing a
 *  button takes focus off one a keyboard reader is on. The signature enumerates EVERY input
 *  `fillActions` branches on. */
function syncActions(row: HTMLElement, steer: PendingSteer, state: StackState): void {
  const sig = [
    rowState(steer),
    steer.origin,
    String(state.waiting),
    state.perRow ? "1" : "0",
    state.clearable ? "1" : "0",
    state.behindCompaction ? "1" : "0",
  ].join("\u0001");
  if (actionSig.get(row) === sig) {
    return;
  }
  actionSig.set(row, sig);
  const column = row.querySelector<HTMLElement>(".steer-actions");
  if (column !== null) {
    fillActions(column, steer, state);
  }
}

function buildRow(steer: PendingSteer, state: StackState): HTMLElement {
  const stateEl = el(
    "span",
    { className: "steer-state" },
    el("span", { className: "steer-state-icon", "aria-hidden": "true" }, iconEl(ICON_HOURGLASS)),
    // The word, not only the glyph. "Sent" is the fact this stack states: the message has left,
    // it is not a draft, and it is waiting.
    el("span", { className: "steer-state-label" }, stateWord(steer, state.behindCompaction)),
  );

  // Collapsed to one line and clipped to four lines in CSS, the quote band counting as one, never
  // truncated here: the whole message stays in the DOM, and it stays reachable through the row's
  // tooltip, its accessible name and Edit.
  const text = el("div", { className: "steer-text" });
  fillText(text, steer.text);

  // Built empty and kept for the row's life: it is what reserves the height a control needs, so the
  // confirmation reveals buttons into a box that was always there rather than growing the row under
  // the reader. See `fillActions`.
  const actions = el("span", { className: "steer-actions" });

  const row = el(
    "li",
    {
      className: "steer-row",
      "data-state": rowState(steer),
      // The WHOLE text, not the clamped one: the visible row is clamped by layout.
      "data-tooltip": readQuoted(steer.text),
      // The state is carried by the glyph AND the label, both visual, so it has to be in the
      // accessible name too.
      "aria-label": accessibleName(steer, state.behindCompaction),
    },
    stateEl,
    text,
    actions,
  );
  if (steer.compacted === true) {
    row.dataset["compacted"] = "";
  }

  // Through the same helper the update path uses, so the signature it compares against is recorded
  // for the row's first paint too.
  syncActions(row, steer, state);
  return row;
}

/** Put the right-hand controls into the column, or empty it. A row still SENDING gets NONE:
 *  nothing on the wire can address it yet. Nor does one the agent is DELIVERING: the server
 *  refuses a delete while KAS holds it. */
function fillActions(column: HTMLElement, steer: PendingSteer, state: StackState): void {
  if (steer.pending === true || steer.delivering === true) {
    column.replaceChildren();
    return;
  }
  const { waiting, perRow, clearable, src } = state;
  const user = steer.origin === "user";
  const controls: HTMLElement[] = [];
  // Send now LEADS, and the destructive control stays last. Not on an unsent row (Edit already sends
  // it) nor behind a compaction: neither has a turn to stop.
  const sendNow = src.sendNow;
  if (user && steer.unsent !== true && !state.behindCompaction && sendNow !== undefined) {
    controls.push(
      actionButton(
        ICON_ARROW_UP,
        "Send this message now",
        // Names what it does AND what it costs: no wire verb injects mid-turn.
        waiting === 1
          ? "Stops the turn and sends this message as a new one"
          : "Stops the turn and sends this message first, then the others",
        () => {
          void sendNow(steer.id);
        },
      ),
    );
  }
  // While a row is delivering the server refuses any delete that would clear KAS's buffer; only a row
  // the server holds itself (unsent) still goes on its own.
  if (perRow && (clearable || steer.unsent === true)) {
    // An unsent row already goes as the next prompt, which is what queueing it would do.
    const queueInstead = src.queueInstead;
    if (steer.unsent !== true && queueInstead !== undefined) {
      controls.push(
        actionButton(
          ICON_QUEUE,
          "Queue this message instead",
          "Take it out of this turn and send it after the turn ends",
          () => {
            void queueInstead(steer);
          },
        ),
      );
    }
    controls.push(
      actionButton(
        ICON_EDIT,
        "Edit this message",
        "Take it back and put it in the message box",
        () => {
          void src.edit(steer);
        },
      ),
      actionButton(
        ICON_TRASH,
        `Delete "${preview(steer.text)}"`,
        "Delete this message",
        () => {
          void src.remove(steer.id);
        },
        "steer-act-danger",
      ),
    );
    column.replaceChildren(...controls);
    return;
  }
  if (!clearable) {
    column.replaceChildren(...controls);
    return;
  }
  // A workflow result is waiting beside the user's rows, and the server cannot resend one, so the
  // only removal is the whole buffer.
  if (waiting === 1) {
    controls.push(
      actionButton(
        ICON_EDIT,
        "Edit this message",
        "Take it back and put it in the message box",
        () => {
          void src.editByClear(steer);
        },
      ),
    );
  }
  controls.push(
    actionButton(
      ICON_TRASH,
      waiting === 1 ? "Discard this message" : `Discard all ${String(waiting)} unread messages`,
      waiting === 1
        ? "The agent has not read it yet"
        : "A workflow result is waiting beside them, so they clear together",
      () => {
        void src.discard(waiting);
      },
      "steer-act-danger",
    ),
  );
  column.replaceChildren(...controls);
}

function actionButton(
  icon: string,
  label: string,
  tooltip: string,
  onClick: () => void,
  extraClass = "",
): HTMLElement {
  const btn = el(
    "button",
    {
      type: "button",
      className: extraClass === "" ? "steer-act" : `steer-act ${extraClass}`,
      "aria-label": label,
      "data-tooltip": tooltip,
    },
    iconEl(icon),
  );
  btn.addEventListener("click", (e: Event) => {
    // The row carries a tooltip and is not itself interactive, but stop here anyway so a future
    // row-level affordance cannot fire off a button.
    e.stopPropagation();
    onClick();
  });
  return btn;
}

/** Stop the running turn so the unread rows go out as the next prompt, this one first. The
 *  server owns the order (`cancel {lead}`), so nothing is held here. */
async function sendSteerNow(steerID: string): Promise<void> {
  const chatID = getActiveId();
  if (chatID === "") {
    return;
  }
  const waiting = (activeSession.peek()?.steers ?? []).filter((e) => e.origin === "user").length;
  announce(
    waiting <= 1
      ? "Stopping the turn and sending this message"
      : `Stopping the turn and sending ${String(waiting)} messages`,
  );
  const outcome = await cancelTurn.dispatch({ chatID, lead: steerID }).outcome;
  if (outcome.status === "success" && outcome.value) {
    announce(NOTHING_STOPPED);
  }
}

/** Replaces a stop announcement the server's skip made false: the turn runs on. */
const NOTHING_STOPPED = "The turn keeps running: the message already left the queue";

/** The statuses that say the delete did not happen and will not: the row still stands, or the
 *  agent already has the words. Anything else (no reply, a 500) may have deleted it, so the
 *  composer keeps the text. */
export const DEFINITE_REFUSALS: ReadonlySet<number> = new Set([400, 404, 409, 502]);

/** The box is filled BEFORE the delete is dispatched, so a lost reply loses no text; a definite
 *  refusal puts back the draft it replaced, unless that chat's composer has moved on since. */
async function editSteer(steer: PendingSteer): Promise<void> {
  const chatID = getActiveId();
  if (chatID === "") {
    return;
  }
  const name = subjectName(chatID);
  const before = composerDraft(chatID);
  setComposerValue(steer.text);
  $.promptInput.focus();
  announce("Message taken back for editing");
  const outcome = await removeSteer.dispatch({ chatID, steerID: steer.id }).outcome;
  if (outcome.status !== "error") {
    return;
  }
  const status = outcome.error.status ?? 0;
  if (DEFINITE_REFUSALS.has(status)) {
    restoreRefusedEdit(chatID, steer.text, before);
    reportRefusal(chatID, name, outcome.error.message);
    return;
  }
  reportRefusal(chatID, name, "Could not confirm the message was taken back");
}

/** Delete the steer, then queue its words under a new id: the steer's own id is keyed on its steer
 *  entries. Once the delete may have happened, every failure leaves the words in the chat's
 *  composer, except a queued row that landed despite a lost reply. */
async function queueSteerInstead(steer: PendingSteer): Promise<void> {
  const chatID = getActiveId();
  if (chatID === "") {
    return;
  }
  const name = subjectName(chatID);
  announce("Moving the message to the queue");
  const removed = await removeSteer.dispatch({ chatID, steerID: steer.id }).outcome;
  if (removed.status === "error") {
    if (DEFINITE_REFUSALS.has(removed.error.status ?? 0)) {
      reportRefusal(chatID, name, removed.error.message);
      return;
    }
    restoreFailedSend(chatID, steer.text);
    reportRefusal(chatID, name, "Could not confirm the message was moved");
    return;
  }
  const messageID = newMessageID();
  const queued = await queuePrompt.dispatch({ chatID, text: steer.text, messageID }).outcome;
  if (queued.status !== "error") {
    return;
  }
  if (queued.error.code === "no_turn") {
    // The turn ended between the two calls, so the words go as the next prompt. Imported lazily so
    // the run tab, which mounts this stack too, does not load the send pipeline.
    const { submitPrompt } = await import("./submit.js");
    await submitPrompt(chatID, steer.text, { attachments: [] });
    return;
  }
  if (hasQueued(chatID, messageID) || hasMessage(chatID, messageID)) {
    return;
  }
  restoreFailedSend(chatID, steer.text);
  reportRefusal(chatID, name, queued.error.message);
}

async function deleteSteer(steerID: string): Promise<void> {
  const chatID = getActiveId();
  if (chatID === "") {
    return;
  }
  const name = subjectName(chatID);
  announce("Deleting the message");
  const outcome = await removeSteer.dispatch({ chatID, steerID }).outcome;
  if (outcome.status === "error") {
    const status = outcome.error.status ?? 0;
    reportRefusal(
      chatID,
      name,
      DEFINITE_REFUSALS.has(status) ? outcome.error.message : "Could not delete the message",
    );
  }
}

/** `name` is the chat's name when the operation began. */
function reportRefusal(chatID: string, name: string, message: string): void {
  announce(message);
  chatNotice(chatID, message, "error", name);
}

/** Edit beside a workflow result: the only removal is the whole buffer. The box is filled BEFORE
 *  the clear is dispatched, so a failed clear leaves the text there. */
async function editSteerByClear(steer: PendingSteer): Promise<void> {
  const chatID = getActiveId();
  if (chatID === "") {
    return;
  }
  setComposerValue(steer.text);
  $.promptInput.focus();
  announce("Message taken back for editing");
  await clearSteers.dispatch({ chatID });
}

/** Drop every steer the agent has not read. Confirms only when more than one would go: with a
 *  single unread message the button's label and its effect already agree. */
async function discardSteers(waiting: number): Promise<void> {
  const chatID = getActiveId();
  if (chatID === "") {
    return;
  }
  if (waiting > 1) {
    const ok = await confirm({
      title: `Discard all ${String(waiting)} messages the agent has not read yet?`,
      body: "A workflow result is waiting beside them, so they clear together.",
      confirmLabel: "Discard all",
      variant: "destructive",
    });
    if (!ok) {
      return;
    }
  }
  announce("Discarding messages the agent has not read");
  // The stack repaints from the `removed` frames the clear produces, not from this reply, so every
  // device agrees.
  await clearSteers.dispatch({ chatID });
}

const chatSource: SteerStackSource = {
  read: () => {
    const s = activeSession.value;
    if (s === undefined) {
      return { id: "", steers: [], queued: [] };
    }
    return {
      id: s.id,
      steers: s.steers ?? [],
      queued: sessionDrain(s).rows,
      ...(s.compact_queued === true && {
        compact: { afterTurn: sessionBusy(s), compacting: s.compact_running === true },
      }),
    };
  },
  sendNow: sendSteerNow,
  queueInstead: queueSteerInstead,
  edit: editSteer,
  editByClear: editSteerByClear,
  remove: deleteSteer,
  discard: discardSteers,
  waitingFor: "the agent",
};

const COMPACT_TEXT = "Compact the context";
const COMPACTING_TEXT = "Compacting the context\u2026";

function compactStage(slot: CompactSlot): string {
  return slot.compacting ? "compacting" : slot.afterTurn ? "running" : "idle";
}

/** With no turn running and no call in flight, the row is left for the next turn's end. */
function compactWord(slot: CompactSlot): string {
  if (slot.compacting) {
    return "Running";
  }
  return slot.afterTurn ? `After ${turnsPhrase(1, true)}` : "Pending";
}

/** The compaction's row: its wait, what it does, and while queued Cancel, the one control (it has no
 *  words to edit and never stops a turn). The key stays the same from queued to running, so the
 *  row changes in place rather than fading back in. */
function buildCompactRow(slot: CompactSlot): HTMLElement {
  const li = el(
    "li",
    { className: "steer-row", "data-kind": "compact" },
    el(
      "span",
      { className: "steer-state" },
      el("span", { className: "steer-state-icon", "aria-hidden": "true" }),
      el("span", { className: "steer-state-label" }),
    ),
    el("span", { className: "steer-text" }),
    el("span", { className: "steer-actions" }),
  );
  updateCompactRow(li, slot);
  return li;
}

const compactShown = new WeakMap<HTMLElement, boolean>();

/** The icon and the control are rebuilt only when the stage flips, so a repaint never takes focus
 *  off the Cancel button. */
function updateCompactRow(li: HTMLElement, slot: CompactSlot): void {
  const word = compactWord(slot);
  const text = slot.compacting ? COMPACTING_TEXT : COMPACT_TEXT;
  li.dataset["state"] = slot.compacting ? "compacting" : "queued";
  li.dataset["tooltip"] = text;
  li.setAttribute("aria-label", `${word}: ${text}`);
  const label = li.querySelector(".steer-state-label");
  if (label !== null) {
    label.textContent = word;
  }
  const textEl = li.querySelector(".steer-text");
  if (textEl !== null) {
    textEl.textContent = text;
  }
  if (compactShown.get(li) === slot.compacting) {
    return;
  }
  compactShown.set(li, slot.compacting);
  li.querySelector(".steer-state-icon")?.replaceChildren(
    slot.compacting ? el("span", { className: "steer-spinner" }) : iconEl(ICON_COMPACT),
  );
  li.querySelector(".steer-actions")?.replaceChildren(
    ...(slot.compacting
      ? []
      : [
          actionButton(
            ICON_TRASH,
            "Cancel the queued compaction",
            "The context stays as it is",
            () => {
              void cancelQueuedCompaction();
            },
            "steer-act-danger",
          ),
        ]),
  );
}

async function cancelQueuedCompaction(): Promise<void> {
  const chatID = getActiveId();
  if (chatID === "") {
    return;
  }
  announce("Cancelling the queued compaction");
  await cancelCompact.dispatch({ chatID }).outcome;
}

/** A queued row's state in words, and the ONE spelling of it, led by who sent the row when an API
 *  caller did, as stateWord leads a steer's. */
function queuedWord(slot: QueuedSlot): string {
  const word = queuedState(slot);
  const { caller } = slot.row;
  if (caller === undefined) {
    return word;
  }
  return slot.wait.kind === "sent"
    ? sentBy(caller)
    : `${sentBy(caller)} \u00b7 ${word.charAt(0).toLowerCase()}${word.slice(1)}`;
}

/** A held row, carried or not, is one no turn end sends: after a restart nothing is sent
 *  automatically. */
function queuedState(slot: QueuedSlot): string {
  const { wait } = slot;
  switch (wait.kind) {
    case "sent":
      return "Sent";
    case "held":
      return "Not sent \u00b7 the server restarted";
    case "compaction":
      return `After ${turnsPhrase(0, false)}`;
    case "turns":
      return `After ${turnsPhrase(wait.turns, wait.running)}`;
  }
}

function queuedName(slot: QueuedSlot): string {
  return `${queuedWord(slot)}: ${queuedShown(slot.row)}`;
}

function queuedShown(row: QueuedPrompt): string {
  return oneLine(row.label ?? readQuoted(row.text));
}

/** What Discard does to this row, which differs by kind: a carried row was already sent once as
 *  a steer the agent never read. */
function discardHint(row: QueuedPrompt): string {
  return (row.resends ?? []).length > 0 ? "It will not be sent again" : "It has not been sent";
}

/** A carried row can absorb a later carry under the same id, so its text changes in place and the
 *  controls read it from here. */
const queuedRowOf = new WeakMap<HTMLElement, QueuedPrompt>();

/** Edit and Discard on every row: each is one `unqueue_prompt` addressing this row alone. The
 *  send-now arrow leads them on a row a turn end sends (syncQueuedArrow). */
function buildQueuedRow(slot: QueuedSlot): HTMLElement {
  const { row } = slot;
  const li = el(
    "li",
    { className: "steer-row", "data-state": "queued" },
    el(
      "span",
      { className: "steer-state" },
      el("span", { className: "steer-state-icon", "aria-hidden": "true" }, iconEl(ICON_HOURGLASS)),
      el("span", { className: "steer-state-label" }),
    ),
    el("div", { className: "steer-text" }),
    el(
      "span",
      { className: "steer-actions" },
      actionButton(
        ICON_EDIT,
        "Edit this follow-up",
        "Take it back and put it in the message box",
        () => {
          void editQueued(queuedRowOf.get(li) ?? row);
        },
      ),
      actionButton(
        ICON_TRASH,
        "Discard this follow-up",
        discardHint(row),
        () => {
          void discardQueued(row.id);
        },
        "steer-act-danger",
      ),
    ),
  );
  updateQueuedRow(li, slot);
  return li;
}

/** The id never changes under one key, and neither does the row's kind, so the update is the
 *  text, the held mark and the state word. */
function updateQueuedRow(li: HTMLElement, slot: QueuedSlot): void {
  const { row } = slot;
  queuedRowOf.set(li, row);
  li.dataset["tooltip"] = readQuoted(row.text);
  li.setAttribute("aria-label", queuedName(slot));
  if (row.held === true) {
    li.dataset["held"] = "";
  } else {
    delete li.dataset["held"];
  }
  const label = li.querySelector(".steer-state-label");
  if (label !== null) {
    label.textContent = queuedWord(slot);
  }
  const text = li.querySelector<HTMLElement>(".steer-text");
  if (text !== null) {
    fillText(text, row.label ?? row.text);
  }
  syncQueuedArrow(li, slot);
}

const queuedArrowOf = new WeakMap<HTMLElement, HTMLElement>();

/** Only the arrow comes and goes, so a wait change never rebuilds Edit or Discard under the
 *  reader's focus. A held or sent row gets none: no turn end sends it, or its turn already opened;
 *  nor does the row a running compaction's end sends, which nothing sends sooner. */
function syncQueuedArrow(li: HTMLElement, slot: QueuedSlot): void {
  const { wait } = slot;
  const arrow = queuedArrowOf.get(li);
  if (wait.kind !== "turns") {
    arrow?.remove();
    queuedArrowOf.delete(li);
    return;
  }
  const tooltip = wait.afterCompaction
    ? "Sends this follow-up first once the compaction ends"
    : wait.running
      ? "Stops the turn and sends this follow-up next"
      : "Sends this follow-up now";
  if (arrow !== undefined) {
    arrow.dataset["tooltip"] = tooltip;
    return;
  }
  const btn = actionButton(ICON_ARROW_UP, "Send this follow-up now", tooltip, () => {
    void sendQueuedRowNow((queuedRowOf.get(li) ?? slot.row).id);
  });
  li.querySelector(".steer-actions")?.prepend(btn);
  queuedArrowOf.set(li, btn);
}

/** `editSteer`'s discipline: the box is filled BEFORE the unqueue is dispatched, so a lost reply
 *  loses nothing, and a definite refusal puts back the draft and drops the files this Edit staged. */
async function editQueued(row: QueuedPrompt): Promise<void> {
  const chatID = getActiveId();
  if (chatID === "") {
    return;
  }
  const name = subjectName(chatID);
  const before = composerDraft(chatID);
  const staged = (row.attachments ?? [])
    .map((a) => a.path)
    .filter((path) => addAttachmentTo(chatID, path));
  setComposerValue(row.text);
  $.promptInput.focus();
  announce("Follow-up taken back for editing");
  const outcome = await unqueuePrompt.dispatch({ chatID, messageID: row.id }).outcome;
  if (outcome.status !== "error") {
    return;
  }
  const status = outcome.error.status ?? 0;
  if (DEFINITE_REFUSALS.has(status)) {
    restoreRefusedEdit(chatID, row.text, before);
    for (const path of staged) {
      removeAttachmentFrom(chatID, path);
    }
    reportRefusal(chatID, name, outcome.error.message);
    return;
  }
  reportRefusal(chatID, name, "Couldn't confirm the follow-up was taken back");
}

async function discardQueued(messageID: string): Promise<void> {
  const chatID = getActiveId();
  if (chatID === "") {
    return;
  }
  announce("Discarding the follow-up");
  await unqueuePrompt.dispatch({ chatID, messageID }).outcome;
}

/** The server owns the order (`send_queued_now`): it moves the row to the head, then stops a
 *  running turn, whose close sends it, or sends it at once. */
async function sendQueuedRowNow(messageID: string): Promise<void> {
  const chatID = getActiveId();
  if (chatID === "") {
    return;
  }
  const name = subjectName(chatID);
  announce(
    isBusy(chatID)
      ? "Stopping the turn and sending this follow-up"
      : get(chatID)?.compact_running === true
        ? "Sending this follow-up first once the compaction ends"
        : "Sending this follow-up",
  );
  const outcome = await sendQueuedNow.dispatch({ chatID, messageID }).outcome;
  if (outcome.status === "success" && outcome.value) {
    announce(NOTHING_STOPPED);
  }
  if (outcome.status === "error") {
    const status = outcome.error.status ?? 0;
    reportRefusal(
      chatID,
      name,
      DEFINITE_REFUSALS.has(status)
        ? outcome.error.message
        : "Couldn't confirm the follow-up was sent",
    );
  }
}

// The row's state in words, because the glyph and the label's styling are both visual, and the
// WHOLE message collapsed to one line: the visible row clamps to fit and a reader who cannot see it
// is not subject to that.
function accessibleName(steer: PendingSteer, behindCompaction: boolean): string {
  const steerText = oneLine(readQuoted(steer.text));
  if (steer.pending === true) {
    return `Sending, not in the agent's buffer yet: ${steerText}`;
  }
  if (steer.delivering === true) {
    return `${stateWord(steer, behindCompaction)}, the agent is reading or dropping it: ${steerText}`;
  }
  if (steer.unsent === true || behindCompaction) {
    return `${stateWord(steer, behindCompaction)}: ${steerText}`;
  }
  return `${stateWord(steer, behindCompaction)}, waiting for the agent: ${steerText}`;
}

// stateWord is the row's state in words, and the ONE spelling of it: the label and the accessible
// name both read it. The compaction marker extends the SENT word alone.
function stateWord(steer: PendingSteer, behindCompaction: boolean): string {
  if (steer.pending === true) {
    return "Sending";
  }
  if (steer.delivering === true) {
    return "Delivering";
  }
  const sent = steer.caller === undefined ? "Sent" : sentBy(steer.caller);
  if (behindCompaction) {
    const wait = turnsPhrase(0, false);
    return steer.caller === undefined ? `After ${wait}` : `${sent} \u00b7 after ${wait}`;
  }
  if (steer.unsent === true) {
    return steer.caller === undefined
      ? "Unread \u00b7 sends next"
      : `${sent} \u00b7 unread \u00b7 sends next`;
  }
  return steer.compacted === true ? `${sent}, context compacted since` : sent;
}

// oneLine collapses whitespace without shortening. A steer is one message however the user typed
// it, and the row is a single clamped block.
function oneLine(text: string): string {
  return text.replace(/\s+/g, " ").trim();
}

const PREVIEW_CHARS = 40;

function preview(text: string): string {
  return truncate(oneLine(readQuoted(text)), PREVIEW_CHARS);
}
