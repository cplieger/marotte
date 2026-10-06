// Run-input card: a workflow STEP asked a question and its run is parked until somebody answers it.
// Rendered in the interaction dock, which owns the queue, the settle-once guard and the two hosts
// this card appears in.

import { el } from "@cplieger/reactive";
import { askActions, askEditor, askHead, RUN_INPUT_FALLBACK } from "./dock-ask.js";
import { wireTouchComposer } from "./composer-touch.js";
import type { RunInputNeededPayload } from "./types.js";

/** `null` is "continue without answering"; a string is the answer. */
type SubmitFn = (text: string | null) => void;

/** Hand the question to the agent that launched this run. Rejects when the hand-off did not go
 *  out, which is what re-enables the button. */
type DeferFn = () => void | Promise<void>;

/** Build the dock card for one parked workflow step. `onDefer`'s PRESENCE is the chat-parented
 *  discriminator at this boundary: the card never learns a chat id, and a deferral never routes
 *  through `onSubmit`, because the two verbs disagree — skip lets the step proceed with no
 *  answer, while a deferral leaves the ask open and asks somebody else. */
export function buildRunInputCard(
  payload: RunInputNeededPayload,
  held: string,
  onSubmit: SubmitFn,
  onDefer?: DeferFn,
): HTMLElement {
  // An EMPTY question is the post-restart case rather than a malformed frame, so it gets a sentence
  // of its own instead of a blank heading. Shared with the dock's own one-line label so the card
  // and the run card's alert agree.
  const { body } = askHead(payload.question === "" ? RUN_INPUT_FALLBACK : payload.question);

  const who = stepLabel(payload);
  if (who !== "") {
    body.appendChild(el("p", { className: "run-input-step" }, who));
  }
  if (payload.question === "") {
    // Says WHY there is nothing to read, so an empty card does not look broken.
    body.appendChild(
      el(
        "p",
        { className: "run-input-note" },
        "The question itself was lost when the server restarted. Answer if you know what it " +
          "asked, or let the step carry on without one.",
      ),
    );
  }

  const { editor, input } = askEditor({
    rows: "3",
    placeholder: "Type your answer\u2026",
    label: "Your answer to the workflow step",
    value: held,
  });

  const send = el(
    "button",
    { type: "button", className: "btn-small confirm-allow" },
    "Send answer",
  ) as HTMLButtonElement;
  send.addEventListener("click", () => {
    const text = input.value.trim();
    if (text === "") {
      // Focus rather than a refusal message: the box IS the instruction, and "continue without
      // answering" is a separate button rather than what an empty send means.
      input.focus();
      return;
    }
    onSubmit(text);
  });

  // Return is always a new line here, so the touch label says so.
  input.enterKeyHint = "enter";
  wireTouchComposer(input);

  // Cmd/Ctrl+Enter rather than bare Enter: an answer to a step is prose that may want paragraphs,
  // which is the same call the composer's textarea makes.
  input.addEventListener("keydown", (e) => {
    if (e.key === "Enter" && (e.metaKey || e.ctrlKey)) {
      e.preventDefault();
      send.click();
    }
  });

  const actions = askActions(send);

  if (onDefer !== undefined) {
    actions.appendChild(deferButton(onDefer));
  } else if (payload.node_id !== "") {
    // WITHHELD on an ask with no node id, because the verb behind it cannot be addressed:
    // `set_step_status` takes a node and refuses 400 without one, so the button could only ever
    // produce an error toast — with the card already spliced by the dock's settle, which leaves the
    // reader worse off than not offering it.
    const skip = el(
      "button",
      { type: "button", className: "btn-small" },
      "Continue without answering",
    );
    skip.setAttribute(
      "data-tooltip",
      "The step carries on with no answer from you, using whatever its own instructions say next.",
    );
    skip.addEventListener("click", () => {
      onSubmit(null);
    });
    actions.appendChild(skip);
  }

  return el("div", { className: "dock-card dock-run-input" }, body, editor, actions);
}

/** Ask the launching agent instead, on a CHAT-PARENTED ask. The node-id gate above is Continue's
 *  alone — a deferral is addressed by CHAT, so an ask carrying no node id still gets one.
 *  Hand-rolled rather than `withAsyncFeedback`, which restores the label after ~1200ms: this is
 *  a durable hand-off state that has to last the card's life, because the ask stays open and a
 *  reader who comes back needs to see that the agent was already asked. */
function deferButton(onDefer: DeferFn): HTMLButtonElement {
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
    // The re-entrancy guard, and it is FIRST: the label only changes once the hand-off resolves, so
    // without it a second click posts a second prompt.
    b.disabled = true;
    void Promise.resolve(onDefer()).then(
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

/** Which step is asking, as one line, or "" when the frame could not name one. */
function stepLabel(p: RunInputNeededPayload): string {
  if (p.agent_name !== "" && p.node_id !== "") {
    return `${p.agent_name} \u00b7 step ${p.node_id}`;
  }
  if (p.agent_name !== "") {
    return p.agent_name;
  }
  if (p.node_id !== "") {
    return `step ${p.node_id}`;
  }
  return "";
}
