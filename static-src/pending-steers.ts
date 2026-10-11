import { el, computed, effect, touch, untracked } from "@cplieger/reactive";
import { join } from "@cplieger/keyenc";
import { announce } from "@cplieger/ui-primitives/announce";
import { reconcile, type ReconcileSpec } from "./reconcile.js";
import { $ } from "./dom.js";
import { activeSession, getActiveId } from "./store.js";
import { cancelTurn, clearSteers, removeSteer, unqueuePrompt } from "./actions/chat.js";
import { setComposerValue } from "./composer-value.js";
import { composerDraft, restoreRefusedEdit } from "./composer-state.js";
import { addAttachmentTo, removeAttachmentFrom } from "./attachments.js";
import { confirm } from "./confirm.js";
import { chatNotice, subjectName } from "./notice-subject.js";
import { ICON_ARROW_UP, ICON_HOURGLASS, ICON_EDIT, ICON_TRASH } from "./icons.js";
import { iconEl } from "./icon-el.js";
import { wireSignature } from "./paint-sig.js";
import type { PendingSteer, QueuedPrompt } from "./types.js";

/** One dock row: a steer the agent has not read, or a follow-up the server holds. */
type DockRow = { kind: "steer"; steer: PendingSteer } | { kind: "queued"; row: QueuedPrompt };

export interface SteerStackSource {
  /** What the dock is about (a chat id, or a run step's key) and its rows. Reactive: the stack's
   *  render subscribes through it. */
  read(): { id: string; steers: readonly PendingSteer[]; queued: readonly QueuedPrompt[] };
  /** Stop the turn and send this row first; absent where no turn is the reader's to stop. */
  sendNow?: (steerID: string) => Promise<void>;
  /** Edit: put the row's words in the box, then delete the row. */
  edit(steer: PendingSteer): Promise<void>;
  /** Edit beside a workflow's row, where the only removal is the whole buffer. */
  editByClear(text: string): Promise<void>;
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
    return join(s.id, join(...s.steers.map(steerSig)), join(...s.queued.map(wireSignature)));
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
    kas: "",
  };
  return join(...Object.values(parts));
}

function render(
  stack: HTMLUListElement,
  src: SteerStackSource,
  announced: { waiting: number; id: string },
): void {
  const { id, steers, queued } = untracked(() => src.read());
  const state: StackState = {
    waiting: steers.filter((e) => e.pending !== true).length,
    perRow: steers.every((e) => e.origin === "user"),
    src,
  };
  const waiting = state.waiting;
  const rows: DockRow[] = [
    ...steers.map((steer): DockRow => ({ kind: "steer", steer })),
    // Below the steers: a steer is read inside this turn, a follow-up after it.
    ...queued.map((row): DockRow => ({ kind: "queued", row })),
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
    // Prefixed by kind so the two id spaces cannot meet in one keyed list.
    key: (r) => (r.kind === "steer" ? `s:${r.steer.id}` : `q:${r.row.id}`),
    mount: (r) => (r.kind === "steer" ? buildRow(r.steer, state) : buildQueuedRow(r.row)),
    update: (el, r) => {
      if (r.kind === "steer") {
        updateRow(el, r.steer, state);
      } else {
        updateQueuedRow(el, r.row);
      }
    },
  };
}

function rowState(steer: PendingSteer): string {
  if (steer.pending === true) {
    return "sending";
  }
  return steer.unsent === true ? "unsent" : "sent";
}

/** `compacted` is a latch the store only sets, so it is written and never cleared. */
function updateRow(row: HTMLElement, steer: PendingSteer, state: StackState): void {
  row.dataset["state"] = rowState(steer);
  if (steer.compacted === true) {
    row.dataset["compacted"] = "";
  }
  row.dataset["tooltip"] = steer.text;
  row.setAttribute("aria-label", accessibleName(steer));

  const label = row.querySelector(".steer-state-label");
  if (label !== null) {
    label.textContent = stateWord(steer);
  }

  syncText(row, steer.text);
  syncActions(row, steer, state);
}

/** No path in the store rewrites the text of an id it already holds, so this is the update being
 *  TOTAL over the item rather than a live case. */
function syncText(row: HTMLElement, text: string): void {
  const textEl = row.querySelector<HTMLElement>(".steer-text");
  if (textEl === null) {
    return;
  }
  const body = oneLine(text);
  if (textEl.textContent === body) {
    return;
  }
  textEl.textContent = body;
}

const actionSig = new WeakMap<HTMLElement, string>();

/** Build, replace or remove the controls, and leave them alone when no input moved: replacing a
 *  button takes focus off one a keyboard reader is on. The signature enumerates EVERY input
 *  `fillActions` branches on. */
function syncActions(row: HTMLElement, steer: PendingSteer, state: StackState): void {
  const sig = [rowState(steer), steer.origin, String(state.waiting), state.perRow ? "1" : "0"].join(
    "\u0001",
  );
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
    el("span", { className: "steer-state-label" }, stateWord(steer)),
  );

  // Collapsed to one line and clipped to four in CSS, never truncated here: the whole message stays
  // in the DOM, and it stays reachable through the row's tooltip, its accessible name and Edit.
  const text = el("span", { className: "steer-text" }, oneLine(steer.text));

  // Built empty and kept for the row's life: it is what reserves the height a control needs, so the
  // confirmation reveals buttons into a box that was always there rather than growing the row under
  // the reader. See `fillActions`.
  const actions = el("span", { className: "steer-actions" });

  const row = el(
    "li",
    {
      className: "steer-row",
      "data-state": rowState(steer),
      // The RAW text, not the clamped one: the visible row is clamped by layout.
      "data-tooltip": steer.text,
      // The state is carried by the glyph AND the label, both visual, so it has to be in the
      // accessible name too.
      "aria-label": accessibleName(steer),
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
 *  nothing on the wire can address it yet. */
function fillActions(column: HTMLElement, steer: PendingSteer, state: StackState): void {
  if (steer.pending === true) {
    column.replaceChildren();
    return;
  }
  const { waiting, perRow, src } = state;
  const user = steer.origin === "user";
  const controls: HTMLElement[] = [];
  // Send now LEADS, and the destructive control stays last. Not on an unsent row: it has no turn to
  // stop, and Edit already sends it.
  const sendNow = src.sendNow;
  if (user && steer.unsent !== true && sendNow !== undefined) {
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
  if (perRow) {
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
  // A workflow result is waiting beside the user's rows, and the server cannot resend one, so the
  // only removal is the whole buffer.
  if (waiting === 1) {
    controls.push(
      actionButton(
        ICON_EDIT,
        "Edit this message",
        "Take it back and put it in the message box",
        () => {
          void src.editByClear(steer.text);
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
  await cancelTurn.dispatch({ chatID, lead: steerID }).outcome;
}

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
async function editSteerByClear(text: string): Promise<void> {
  const chatID = getActiveId();
  if (chatID === "") {
    return;
  }
  setComposerValue(text);
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
    const ok = await confirm(
      `Discard all ${String(waiting)} messages the agent has not read yet? A workflow result is waiting beside them, so they clear together.`,
      "Discard all",
      "destructive",
    );
    if (!ok) {
      return;
    }
  }
  announce("Discarding messages the agent has not read");
  // The stack repaints from the `steer` entry the clear produces, not from this reply, so every
  // device agrees.
  await clearSteers.dispatch({ chatID });
}

const chatSource: SteerStackSource = {
  read: () => {
    const s = activeSession.value;
    return { id: s?.id ?? "", steers: s?.steers ?? [], queued: s?.queued ?? [] };
  },
  sendNow: sendSteerNow,
  edit: editSteer,
  editByClear: editSteerByClear,
  remove: deleteSteer,
  discard: discardSteers,
  waitingFor: "the agent",
};

/** A queued row's state in words, and the ONE spelling of it. A held row outranks a carried one:
 *  after a restart nothing is sent automatically, carried or not. */
function queuedWord(row: QueuedPrompt): string {
  if (row.held === true) {
    return "Not sent \u00b7 the server restarted";
  }
  return (row.resends ?? []).length > 0 ? "Unread \u00b7 sends next" : "After this turn";
}

function queuedName(row: QueuedPrompt): string {
  return `${queuedWord(row)}: ${queuedShown(row)}`;
}

function queuedShown(row: QueuedPrompt): string {
  return oneLine(row.label ?? row.text);
}

/** What Discard does to this row, which differs by kind: a carried row was already sent once as
 *  a steer the agent never read. */
function discardHint(row: QueuedPrompt): string {
  return (row.resends ?? []).length > 0 ? "It will not be sent again" : "It has not been sent";
}

/** A carried row can absorb a later carry under the same id, so its text changes in place and the
 *  controls read it from here. */
const queuedRowOf = new WeakMap<HTMLElement, QueuedPrompt>();

/** Edit and Discard on every row: each is one `unqueue_prompt` addressing this row alone. */
function buildQueuedRow(row: QueuedPrompt): HTMLElement {
  const li = el(
    "li",
    { className: "steer-row", "data-state": "queued" },
    el(
      "span",
      { className: "steer-state" },
      el("span", { className: "steer-state-icon", "aria-hidden": "true" }, iconEl(ICON_HOURGLASS)),
      el("span", { className: "steer-state-label" }),
    ),
    el("span", { className: "steer-text" }),
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
  updateQueuedRow(li, row);
  return li;
}

/** The id never changes under one key, and neither does the row's kind, so the update is the
 *  text, the held mark and the state word. */
function updateQueuedRow(li: HTMLElement, row: QueuedPrompt): void {
  queuedRowOf.set(li, row);
  li.dataset["tooltip"] = row.text;
  li.setAttribute("aria-label", queuedName(row));
  if (row.held === true) {
    li.dataset["held"] = "";
  } else {
    delete li.dataset["held"];
  }
  const label = li.querySelector(".steer-state-label");
  if (label !== null) {
    label.textContent = queuedWord(row);
  }
  const text = li.querySelector(".steer-text");
  if (text !== null) {
    text.textContent = queuedShown(row);
  }
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

// The row's state in words, because the glyph and the label's styling are both visual, and the
// WHOLE message collapsed to one line: the visible row clamps to fit and a reader who cannot see it
// is not subject to that.
function accessibleName(steer: PendingSteer): string {
  const steerText = oneLine(steer.text);
  if (steer.pending === true) {
    return `Sending, not in the agent's buffer yet: ${steerText}`;
  }
  if (steer.unsent === true) {
    return `${stateWord(steer)}: ${steerText}`;
  }
  return `${stateWord(steer)}, waiting for the agent: ${steerText}`;
}

// stateWord is the row's state in words, and the ONE spelling of it: the label and the accessible
// name both read it. The compaction marker extends the SENT word alone.
function stateWord(steer: PendingSteer): string {
  if (steer.pending === true) {
    return "Sending";
  }
  if (steer.unsent === true) {
    return "Unread \u00b7 sends next";
  }
  return steer.compacted === true ? "Sent, context compacted since" : "Sent";
}

// oneLine collapses whitespace without shortening. A steer is one message however the user typed
// it, and the row is a single clamped block.
function oneLine(text: string): string {
  return text.replace(/\s+/g, " ").trim();
}

const PREVIEW_CHARS = 40;

function preview(text: string): string {
  const line = oneLine(text);
  return line.length <= PREVIEW_CHARS ? line : `${line.slice(0, PREVIEW_CHARS - 1)}…`;
}
