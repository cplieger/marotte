// Run-ask card: a workflow STEP waits on a person. The interaction dock renders one per ask the
// run-asks store holds, so the store decides whether the card exists; this card owns its words and
// its draft, and calls the three verbs `handlers/run.ts` registers.

import { el } from "@cplieger/reactive";
import { announce } from "@cplieger/ui-primitives/announce";
import { askActions, askEditor, askHead, RUN_INPUT_FALLBACK } from "./dock-ask.js";
import { wireTouchComposer } from "./composer-touch.js";
import { runAskPosition } from "./run-asks.js";
import type { RunAsk, RunAskBlocker, RunAskText } from "./wire/types.gen.js";

/** One ask as the dock hands it over: the run's envelope chat (`run:<id>` for a parentless run)
 *  and whether marotte's last read of the run succeeded. */
export interface RunAskCardInput {
  readonly workflowID: string;
  readonly chatID: string;
  readonly reachable: boolean;
  readonly ask: RunAsk;
}

/** What the card's buttons do. Registered rather than imported, so the dock (read by every surface
 *  that counts asks) does not pull the action and send graphs in. */
export interface RunAskVerbs {
  /** Resolves true once the server took the answer; a refusal is the verb's own toast. */
  answer(workflowID: string, askID: string, text: string): Promise<boolean>;
  continueStep(workflowID: string, nodeID: string, askID: string): Promise<void>;
  /** Rejects when the prompt to the launching chat was refused. */
  defer(chatID: string, workflowID: string): Promise<void>;
}

let verbs: RunAskVerbs | null = null;

export function registerRunAskVerbs(v: RunAskVerbs): void {
  verbs = v;
}

/** THROWS when nothing is registered: a silent dead button is the defect the seam must not hide. */
function registered(): RunAskVerbs {
  if (verbs === null) {
    throw new Error("run-input: no verbs registered; handlers/run.ts registers them at import");
  }
  return verbs;
}

/** Total over the wire enum, so a new source needs its words. */
const SOURCE_LABEL: Readonly<Record<RunAskText, string>> = {
  asked: "The step asks",
  last_message: "The step's last message",
  unseen: "marotte did not see what this step said",
};

/** Why Send is off, total over the wire enum. */
const BLOCKED_TEXT: Readonly<Record<RunAskBlocker, string>> = {
  stopping: "This run is stopping.",
  pending: "An answer to this run is being delivered.",
  recovering:
    "This run's process ended. marotte resumes it once kiro-cli releases it, within about three minutes.",
  branch_running: "Another step of this run is still running. Answer once it stops.",
  unsettled: "This run looks busy but nothing is moving. Open the run to check it.",
  settling: "This run is moving. The answer can go once it settles.",
};

const UNREACHABLE_TEXT = "marotte could not read this run just now.";

/** The typed words and the last words sent, per wait position, so a re-park or a rebuilt card
 *  keeps them. They live as long as the run, not its asks: a run reads `asks: []` between an
 *  admitted answer and the step's next park, the gap "Restore last message" exists to cross. */
interface Draft {
  text: string;
  sent: string;
}
const drafts = new Map<string, Map<string, Draft>>();

function draftOf(d: RunAskCardInput): Draft {
  const byPosition = drafts.get(d.workflowID) ?? new Map<string, Draft>();
  drafts.set(d.workflowID, byPosition);
  const position = runAskPosition(d.workflowID, d.ask);
  let draft = byPosition.get(position);
  if (draft === undefined) {
    draft = { text: "", sent: "" };
    byPosition.set(position, draft);
  }
  return draft;
}

/** Forget the drafts of a run that has ended. */
export function dropRunAskDrafts(workflowID: string): void {
  drafts.delete(workflowID);
}

/** Keep only the drafts of the runs a pending set names; a run it omits has ended. */
export function keepRunAskDrafts(named: ReadonlySet<string>): void {
  for (const workflowID of [...drafts.keys()]) {
    if (!named.has(workflowID)) {
      drafts.delete(workflowID);
    }
  }
}

/** A chat-parented run's ask is the one the launching agent can be asked to answer. */
function launchingChat(chatID: string): string {
  return chatID.startsWith("run:") ? "" : chatID;
}

/** The parts `updateRunAskCard` rewrites in place, so a blocker turning on or off keeps the
 *  reader's typing, focus and the Defer button's state. */
interface CardParts {
  readonly note: HTMLElement;
  readonly send: HTMLButtonElement;
  readonly skip: HTMLButtonElement | undefined;
  readonly actions: HTMLElement;
  readonly defer: HTMLButtonElement | undefined;
  readonly restore: HTMLButtonElement;
}

const parts = new WeakMap<HTMLElement, CardParts>();

/** Build the dock card for one waiting step. */
export function buildRunAskCard(d: RunAskCardInput): HTMLElement {
  const { ask } = d;
  const { body } = askHead(ask.text === "" ? RUN_INPUT_FALLBACK : ask.text);
  body.appendChild(el("p", { className: "run-input-step" }, headline(ask)));
  const note = el("p", { className: "run-input-note" });
  body.appendChild(note);

  const draft = draftOf(d);
  const { editor, input } = askEditor({
    rows: "3",
    placeholder: "Type your answer\u2026",
    label: "Your answer to the workflow step",
    value: draft.text,
  });
  // Return is always a new line here, so the touch label says so.
  input.enterKeyHint = "enter";
  wireTouchComposer(input);

  const card = el("div", { className: "dock-card dock-run-input" }, body, editor);

  const restore = el(
    "button",
    { type: "button", className: "btn-small" },
    "Restore last message",
  ) as HTMLButtonElement;
  restore.setAttribute("data-tooltip", "Put the words you last sent to this step back in the box.");
  const syncRestore = (): void => {
    restore.classList.toggle("hidden", input.value !== "" || draft.sent === "");
  };
  restore.addEventListener("click", () => {
    input.value = draft.sent;
    draft.text = draft.sent;
    syncRestore();
    input.focus();
  });
  input.addEventListener("input", () => {
    draft.text = input.value;
    syncRestore();
  });
  syncRestore();

  const send = el(
    "button",
    { type: "button", className: "btn-small confirm-allow" },
    "Send answer",
  ) as HTMLButtonElement;
  send.addEventListener("click", () => {
    const text = input.value.trim();
    if (text === "") {
      // The box IS the instruction; "continue without answering" is its own button.
      input.focus();
      return;
    }
    void sendAnswer(card, d.workflowID, ask.ask_id, text, () => {
      draft.text = "";
      draft.sent = text;
      input.value = "";
      syncRestore();
    });
  });
  // Cmd/Ctrl+Enter rather than bare Enter: an answer is prose that may want paragraphs.
  input.addEventListener("keydown", (e) => {
    if (e.key === "Enter" && (e.metaKey || e.ctrlKey)) {
      e.preventDefault();
      send.click();
    }
  });

  const chat = launchingChat(d.chatID);
  const defer = chat === "" ? undefined : deferButton(chat, d.workflowID);
  const skip = chat === "" ? continueButton(card, d.workflowID, ask) : undefined;
  const actions = askActions(restore, send);
  card.appendChild(actions);
  parts.set(card, { note, send, skip, actions, defer, restore });
  updateRunAskCard(card, d);
  return card;
}

/** Re-state a card's blocker and Continue offer for the same ask, in place. */
export function updateRunAskCard(card: HTMLElement, d: RunAskCardInput): void {
  const p = parts.get(card);
  if (p === undefined) {
    return;
  }
  const blocked = d.ask.blocked;
  const reason =
    blocked === undefined ? (d.reachable ? "" : UNREACHABLE_TEXT) : BLOCKED_TEXT[blocked];
  p.note.textContent = reason;
  p.note.classList.toggle("hidden", reason === "");
  p.send.disabled = blocked !== undefined;
  const offered: HTMLElement[] = [p.restore, p.send];
  if (p.defer !== undefined) {
    offered.push(p.defer);
  } else if (p.skip !== undefined && d.ask.can_continue && blocked === undefined) {
    offered.push(p.skip);
  }
  p.actions.replaceChildren(...offered);
}

/** Who is waiting and what the text is, as one line. */
function headline(ask: RunAsk): string {
  const who =
    ask.agent_name !== "" && ask.node_id !== ""
      ? `${ask.agent_name} \u00b7 step ${ask.node_id}`
      : ask.agent_name !== ""
        ? ask.agent_name
        : ask.node_id !== ""
          ? `step ${ask.node_id}`
          : "";
  const source = SOURCE_LABEL[ask.text_source];
  return who === "" ? source : `${source} \u00b7 ${who}`;
}

/** The card is inert while its answer is out: a second Send before the reply would be a second
 *  answer. A refusal keeps the words in the box; the server's sentence is the action's toast. */
async function sendAnswer(
  card: HTMLElement,
  workflowID: string,
  askID: string,
  text: string,
  onSent: () => void,
): Promise<void> {
  setBusy(card, true);
  const sent = await registered()
    .answer(workflowID, askID, text)
    .finally(() => {
      setBusy(card, false);
    });
  if (sent) {
    onSent();
    announce("Answer sent");
  }
}

function setBusy(card: HTMLElement, on: boolean): void {
  card.inert = on;
  card.toggleAttribute("aria-busy", on);
}

/** Continue without answering, offered on a parentless run's ask (a chat-parented one offers
 *  Defer in its place) whenever the server says Continue would reach this very execution. */
function continueButton(card: HTMLElement, workflowID: string, ask: RunAsk): HTMLButtonElement {
  const b = el(
    "button",
    { type: "button", className: "btn-small" },
    "Continue without answering",
  ) as HTMLButtonElement;
  b.setAttribute(
    "data-tooltip",
    "The step carries on with no answer from you, using whatever its own instructions say next.",
  );
  b.addEventListener("click", () => {
    setBusy(card, true);
    void registered()
      .continueStep(workflowID, ask.node_id, ask.ask_id)
      .finally(() => {
        setBusy(card, false);
      });
  });
  return b;
}

/** Ask the launching agent instead. Hand-rolled rather than `withAsyncFeedback`, which restores
 *  the label after ~1200ms: the hand-off state has to last the card's life, because the question
 *  stays open and a reader who comes back needs to see that the agent was already asked. */
function deferButton(chatID: string, workflowID: string): HTMLButtonElement {
  const b = el(
    "button",
    { type: "button", className: "btn-small" },
    "Defer to parent agent",
  ) as HTMLButtonElement;
  b.setAttribute(
    "data-tooltip",
    "The agent that launched this run is asked to answer instead. The question stays open, " +
      "so you can still answer it yourself.",
  );
  b.addEventListener("click", () => {
    // FIRST: the label changes only once the hand-off resolves, so a second click would post a
    // second prompt.
    b.disabled = true;
    void registered()
      .defer(chatID, workflowID)
      .then(
        () => {
          b.textContent = "Asked the agent";
          b.setAttribute(
            "data-tooltip",
            "The agent that launched this run has been asked to answer. The question is still " +
              "open, so you can still answer it yourself.",
          );
        },
        () => {
          b.disabled = false;
        },
      );
  });
  return b;
}

/** Reset the drafts between tests. */
// deadset:ignore DS1004 -- test seam: resets the per-position drafts
export function _resetRunAskDraftsForTest(): void {
  drafts.clear();
}
