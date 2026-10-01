// The steer stack: the mid-turn messages KAS is holding and none it has read, a pure
// projection of `activeSession.steers`. A row leaves on its own `steer` ENTRY.
//
// Measured on KAS 2.21.2, and it bounds every control: the only steer verbs are
// `_session/steer` and `_session/steer/clear`, and the clear reads `sessionId` alone.
// So nothing injects and nothing removes ONE row. Shape, history and the rejected
// per-row delete: marotte-client.md "Send discipline, steering & model pill".

import { el, computed, effect, touch } from "@cplieger/reactive";
import { announce } from "@cplieger/ui-primitives/announce";
import { reconcile, type ReconcileSpec } from "./reconcile.js";
import { $ } from "./dom.js";
import { activeSession, getActiveId, pendingSteerCarry } from "./store.js";
import { cancelTurn, clearSteers } from "./actions/chat.js";
import { forgetSteerPreference, preferSteerFirst } from "./steer-resend.js";
import { setComposerValue } from "./composer-value.js";
import { confirm } from "./confirm.js";
import { ICON_ARROW_UP, ICON_HOURGLASS, ICON_EDIT, ICON_TRASH } from "./icons.js";
import { iconEl } from "./icon-el.js";
import type { PendingSteer } from "./types.js";

let bound = false;
let prevWaiting = 0;
let prevId = "";

/** Wire the reactive render. Idempotent. Called once from app.ts. */
export function initPendingSteers(): void {
  if (bound) {
    return;
  }
  bound = true;
  const stack = $.steerStack;
  // A value-deduped key, so every term `render` branches on has to be IN it: `origin`
  // and `compacted` each arrive ALONE, and a key missing either produces no render at
  // all for the write that carries it.
  const sig = computed(() => {
    const s = activeSession.value;
    const steers = s?.steers ?? [];
    return (
      (s?.id ?? "") +
      "\u0001" +
      steers
        .map(
          (e) =>
            (e.pending === true ? "1" : "0") +
            (e.compacted === true ? "1" : "0") +
            "\u0002" +
            e.origin +
            "\u0002" +
            e.text,
        )
        .join("\u0000")
    );
  });
  effect(() => {
    touch(sig);
    render(stack);
  });
}

function render(stack: HTMLUListElement): void {
  const s = activeSession.peek();
  const steers = s?.steers ?? [];
  const id = s?.id ?? "";
  // Every row in the stack is waiting; a confirmed one is what a clear can
  // actually address, which is what decides whether Edit is offerable.
  const waiting = steers.filter((e) => e.pending !== true).length;

  if (steers.length === 0) {
    reconcile(stack, [], rowSpec(waiting));
    stack.classList.add("hidden");
    // Reset the announce baseline so arriving at a chat that already has steers
    // reads them out fresh, while the empty case stays silent.
    prevWaiting = 0;
    prevId = id;
    return;
  }
  // Un-hidden BEFORE the rows land, because `.hidden` is `display: none` and an
  // element inserted into a subtree with no box has its first style resolution
  // there — which is the one the entry transition below reads.
  stack.classList.remove("hidden");
  // Arrival order, so the newest sits at the bottom nearest the box it was typed
  // into and every earlier one moves up.
  reconcile(stack, steers, rowSpec(waiting));

  // Announce only on the same chat, and only the WAITING count — the number the
  // user is waiting to see fall. A pure chat switch is not news.
  if (id === prevId && waiting !== prevWaiting) {
    announce(
      waiting === 0
        ? "Steering message delivered to the agent"
        : waiting === 1
          ? "1 steering message waiting for the agent"
          : `${String(waiting)} steering messages waiting for the agent`,
    );
  }
  prevWaiting = waiting;
  prevId = id;
}

/** KEYED BY THE STEER'S OWN ID, so a row survives its own confirmation: one Send
 *  produces two renders a round trip apart, and `.steer-row`'s `@starting-style`
 *  entry (26-dock.css) re-fires on a REBUILT node, so the row appeared, vanished and
 *  appeared again. `waiting` is stack-wide, so the spec is built per render. */
function rowSpec(waiting: number): ReconcileSpec<PendingSteer> {
  return {
    key: (steer) => steer.id,
    mount: (steer) => buildRow(steer, waiting),
    update: (row, steer) => {
      updateRow(row, steer, waiting);
    },
  };
}

/** Bring an existing row up to date in place: state, compaction marker, message and
 *  controls. `compacted` is a LATCH the store only ever sets, so it is written and
 *  never cleared. */
function updateRow(row: HTMLElement, steer: PendingSteer, waiting: number): void {
  const sending = steer.pending === true;
  const compacted = steer.compacted === true;
  row.dataset["state"] = sending ? "sending" : "sent";
  if (compacted) {
    row.dataset["compacted"] = "";
  }
  row.dataset["tooltip"] = steer.text;
  row.setAttribute("aria-label", accessibleName(steer.text, sending, compacted));

  const label = row.querySelector(".steer-state-label");
  if (label !== null) {
    label.textContent = stateWord(sending, compacted);
  }

  syncText(row, steer.text);
  syncActions(row, steer, sending, waiting);
}

/** No path in the store rewrites the text of an id it already holds, so this is
 *  the update being TOTAL over the item rather than a live case. */
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

/** The signature the row's controls were last built for. */
const actionSig = new WeakMap<HTMLElement, string>();

/** Build, replace or remove the controls, and leave them alone when no input moved:
 *  replacing a button takes focus off one a keyboard reader is on.
 *
 *  The signature enumerates EVERY input `fillActions` branches on, `origin` included:
 *  a key missing it leaves the arrow standing on a row that no longer earns one. */
function syncActions(
  row: HTMLElement,
  steer: PendingSteer,
  sending: boolean,
  waiting: number,
): void {
  const sig = `${sending ? "1" : "0"}\u0001${steer.origin}\u0001${String(waiting)}`;
  if (actionSig.get(row) === sig) {
    return;
  }
  actionSig.set(row, sig);
  const column = row.querySelector<HTMLElement>(".steer-actions");
  if (column !== null) {
    fillActions(column, steer, sending, waiting);
  }
}

/** One full-width row. `waiting` is the stack-wide confirmed count, which is what
 *  decides whether Edit is offerable — see the header. */
function buildRow(steer: PendingSteer, waiting: number): HTMLElement {
  const sending = steer.pending === true;
  const compacted = steer.compacted === true;

  const state = el(
    "span",
    { className: "steer-state" },
    el("span", { className: "steer-state-icon", "aria-hidden": "true" }, iconEl(ICON_HOURGLASS)),
    // The word, not only the glyph. "Sent" is the fact the user asked this stack
    // to state: the message has left, it is not a draft, and it is waiting.
    el("span", { className: "steer-state-label" }, stateWord(sending, compacted)),
  );

  // Collapsed to one line and clipped to four in CSS, never truncated here: the
  // whole message stays in the DOM, and it stays reachable through the row's
  // tooltip, its accessible name and Edit.
  const text = el("span", { className: "steer-text" }, oneLine(steer.text));

  // Built empty and kept for the row's life: it is what reserves the height a
  // control needs, so the confirmation reveals buttons into a box that was always
  // there rather than growing the row under the reader. See `fillActions`.
  const actions = el("span", { className: "steer-actions" });

  const row = el(
    "li",
    {
      className: "steer-row",
      // Read by CSS, so the in-flight row differs by more than a word without a
      // second class to keep in sync.
      "data-state": sending ? "sending" : "sent",
      // The RAW text, not the clamped one: the visible row is clamped by layout.
      "data-tooltip": steer.text,
      // The state is carried by the glyph AND the label, both visual, so it has
      // to be in the accessible name too.
      "aria-label": accessibleName(steer.text, sending, compacted),
    },
    state,
    text,
    actions,
  );
  if (compacted) {
    row.dataset["compacted"] = "";
  }

  // Through the same helper the update path uses, so the signature it compares
  // against is recorded for the row's first paint too.
  syncActions(row, steer, sending, waiting);
  return row;
}

/** Put the right-hand controls into the column, or empty it. A row still SENDING
 *  gets NONE: nothing on the wire can address a derived id yet.
 *
 *  The COLUMN is there in both states, which is what stops the confirmation moving the
 *  transcript: the bar grows UPWARD, so `.steer-actions` reserves that height while it
 *  is empty (26-dock.css). */
function fillActions(
  column: HTMLElement,
  steer: PendingSteer,
  sending: boolean,
  waiting: number,
): void {
  if (sending) {
    column.replaceChildren();
    return;
  }
  const controls: HTMLElement[] = [];
  // Send now LEADS, and the destructive control stays last. Only on a row the resend
  // would carry: nothing carries an agent's own notice, so offering it there would be
  // a button that cannot act.
  if (steer.origin === "user") {
    controls.push(
      actionButton(
        ICON_ARROW_UP,
        "Send this message now",
        // Names what it does AND what it costs: no wire verb injects mid-turn.
        waiting === 1
          ? "Stops the turn and sends this message as a new one"
          : "Stops the turn and sends this message first, then the others",
        () => {
          void sendSteerNow(steer.id);
        },
      ),
    );
  }
  // Edit is discard-plus-retype, so it is only offered when discarding cannot
  // take anything else with it.
  if (waiting === 1) {
    controls.push(
      actionButton(
        ICON_EDIT,
        "Edit this message",
        "Take it back and put it in the message box",
        () => {
          void editSteer(steer.text);
        },
      ),
    );
  }
  controls.push(
    actionButton(
      ICON_TRASH,
      waiting === 1 ? "Discard this message" : `Discard all ${String(waiting)} unread messages`,
      // Naming the all-or-nothing behaviour, because the wire has no per-message
      // clear and a reader would reasonably assume otherwise.
      waiting === 1
        ? "The agent has not read it yet"
        : "The agent's buffer clears all at once; there is no per-message removal",
      () => {
        void discardSteers(waiting);
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
    // The row carries a tooltip and is not itself interactive, but stop here
    // anyway so a future row-level affordance cannot fire off a button.
    e.stopPropagation();
    onClick();
  });
  return btn;
}

/** Stop the running turn and send this message as a new one: the pressed row LEADS
 *  and every other carried row follows, because the same cancel drains KAS's whole
 *  buffer and sending one alone would lose the rest. It records an ORDER and cancels;
 *  the boundary reads the text (`steer-resend.ts`). */
async function sendSteerNow(steerID: string): Promise<void> {
  const chatID = getActiveId();
  if (chatID === "") {
    return;
  }
  // Click-time read for the announcement and the no-op guard only, never the payload.
  const carried = pendingSteerCarry(chatID);
  if (!carried.some((e) => e.id === steerID)) {
    return;
  }
  preferSteerFirst(chatID, steerID);
  announce(
    carried.length === 1
      ? "Stopping the turn and sending this message"
      : `Stopping the turn and sending ${String(carried.length)} messages`,
  );
  const outcome = await cancelTurn.dispatch(chatID).outcome;
  if (outcome.status !== "success") {
    forgetSteerPreference(chatID);
  }
}

/** Take the only unread steer back and put its text in the composer. The box is
 *  filled BEFORE the clear is dispatched, so a failed clear leaves the user holding
 *  the text. On KAS 2.21.4 the clear leaves every live execution's read cursor where
 *  it was, so the retyped message is invisible to the running turn until as many
 *  messages have arrived as that execution had read (kirodotdev/Kiro#11449). */
async function editSteer(text: string): Promise<void> {
  const chatID = getActiveId();
  if (chatID === "") {
    return;
  }
  setComposerValue(text);
  $.promptInput.focus();
  announce("Message taken back for editing");
  await clearSteers.dispatch({ chatID });
}

/** Drop every steer the agent has not read.
 *
 *  Confirms only when more than one would go: with a single unread message the
 *  button's label and its effect already agree, and a dialog for the common case
 *  is a click that teaches nothing. */
async function discardSteers(waiting: number): Promise<void> {
  const chatID = getActiveId();
  if (chatID === "") {
    return;
  }
  if (waiting > 1) {
    const ok = await confirm(
      `Discard all ${String(waiting)} messages the agent has not read yet? They clear together — the wire has no way to remove just one.`,
      "Discard all",
      "destructive",
    );
    if (!ok) {
      return;
    }
  }
  announce("Discarding messages the agent hasn't read");
  // Fire and forget: the stack repaints from the `steer` entry the clear produces,
  // not from this reply, so every device agrees and a reconnect cannot leave a row
  // behind for a message that is gone.
  await clearSteers.dispatch({ chatID });
}

// The row's state in words, because the glyph and the label's styling are both
// visual, and the WHOLE message collapsed to one line: the visible row clamps to fit
// and a reader who cannot see it is not subject to that.
function accessibleName(text: string, sending: boolean, compacted: boolean): string {
  const steerText = oneLine(text);
  return sending
    ? `Sending, not in the agent's buffer yet: ${steerText}`
    : `${stateWord(false, compacted)}, waiting for the agent: ${steerText}`;
}

// stateWord is the row's state in words, and the ONE spelling of it: the label and
// the accessible name both read it, so they cannot say different things. The marker
// extends the SENT word alone, because a row still sending is one round trip from
// `sent`; `data-compacted` is written in both states, so nothing keyed on it has a gap.
function stateWord(sending: boolean, compacted: boolean): string {
  if (sending) {
    return "Sending";
  }
  return compacted ? "Sent, context compacted since" : "Sent";
}

// oneLine collapses whitespace without shortening. A steer is one message
// however the user typed it, and the row is a single clamped block.
function oneLine(text: string): string {
  return text.replace(/\s+/g, " ").trim();
}
