import { announce } from "@cplieger/ui-primitives/announce";
import { join } from "@cplieger/keyenc";
import { messageRunStep } from "./actions/runs.js";
import { noticeAbout } from "./actions/subject.js";
import { wireTouchComposer, touchComposer } from "./composer-touch.js";
import { continueList } from "./list-continue.js";
import { applyEdit } from "./text-edit.js";
import { newMessageID } from "./transport.js";
import { iconEl } from "./icon-el.js";
import { ICON_SEND } from "./icons.js";
import { beginStepMessage, settleStepMessage } from "./run-step-steers.js";
import type { ExecNode } from "./exec-view/model.js";
import type { RunStepMessageRefusal } from "./wire/types.gen.js";

interface RunComposerTarget {
  workflowID: string;
  /** Whether the run itself is still live; a finished run takes no message anywhere. */
  runLive: boolean;
  runPaused: boolean;
  /** The selected step; undefined before the run's tree has a step. */
  step: ExecNode | undefined;
}

/** `verb` is the transport the server will choose, which the display state cannot say: a running step
 *  with an ask shows as `input` and still steers. */
type RunComposerMode =
  | { kind: "message"; verb: "steer" | "prompt"; placeholder: string }
  | { kind: "answer"; verb: "answer"; placeholder: string }
  | { kind: "disabled"; reason: string };

export function composerMode(t: RunComposerTarget): RunComposerMode {
  const step = t.step;
  if (step === undefined) {
    return { kind: "disabled", reason: "This run has no step to message yet" };
  }
  const name = step.label;
  if (step.verb === "answer") {
    return { kind: "answer", verb: "answer", placeholder: `Answer ${name}\u2026` };
  }
  if (!t.runLive) {
    return { kind: "disabled", reason: "This run has finished" };
  }
  if (step.verb === "steer") {
    return { kind: "message", verb: "steer", placeholder: `Message ${name}\u2026` };
  }
  if (step.verb === "prompt") {
    return { kind: "message", verb: "prompt", placeholder: `Message ${name} to resume it\u2026` };
  }
  switch (step.state) {
    case "input":
    case "running":
    case "unknown":
      return { kind: "disabled", reason: `${name} cannot take a message right now` };
    case "waiting":
      // KAS takes a paused step's message only once the whole run has paused.
      return t.runPaused
        ? { kind: "disabled", reason: `Resume the run to message ${name}` }
        : { kind: "disabled", reason: `${name} can take a message once the run pauses` };
    case "pending":
      return t.runPaused
        ? { kind: "disabled", reason: `Resume the run to message ${name}` }
        : { kind: "disabled", reason: `${name} has not started yet` };
    case "ok":
    case "fail":
    case "warn":
    case "skipped":
      return { kind: "disabled", reason: `${name} has finished` };
  }
}

/** Total over the reason, so a new reason needs its words. */
const REFUSAL_TEXT: Record<RunStepMessageRefusal, string> = {
  not_started: "That step has not started yet.",
  finished: "That step has finished, so it cannot take a message.",
  run_paused: "The run is paused. Resume it to message this step.",
  busy: "That step cannot take a message right now. Try again in a moment.",
  full: "Too many unread messages are waiting for that step.",
};

function refusalText(code: string | undefined, fallback: string): string {
  return code !== undefined && Object.hasOwn(REFUSAL_TEXT, code)
    ? REFUSAL_TEXT[code as RunStepMessageRefusal]
    : fallback;
}

interface Mounted {
  form: HTMLFormElement;
  input: HTMLTextAreaElement;
  send: HTMLButtonElement;
}

let mounted: Mounted | undefined;
let target: RunComposerTarget | undefined;
/** Drafts per (run, step), so moving between steps keeps what was typed for each. */
const drafts = new Map<string, string>();
let draftKey = "";
/** Sends whose fate is unknown (no reply, or a server fault), by message id: a retry of the same words
 *  to the same step reuses that id, the action's idempotency key, so the server replays the first
 *  answer rather than delivering the words twice. Keyed by id because drafts are per step: a send
 *  settles only its own entry, so another send, to any step, cannot orphan a restored draft's id. A
 *  refusal drops its entry and the next send mints a fresh id, or the replay would refuse again. */
const unknownSends = new Map<string, { key: string; text: string }>();
/** Draft keys with a send awaiting its reply. While a key is here its draft cannot change (the box
 *  is held read-only and Edit takes nothing into it), so a refusal restores without overwriting. */
const inFlight = new Set<string>();

function messageIDFor(key: string, text: string): string {
  for (const [id, sent] of unknownSends) {
    if (sent.key === key && sent.text === text) {
      return id;
    }
  }
  return newMessageID();
}

/** Whether a failed send may have landed: no HTTP answer, or a server fault the replay cache keeps
 *  out of its window. */
function fateUnknown(status: number | undefined): boolean {
  const s = status ?? 0;
  return s === 0 || s >= 500;
}

function keyOf(t: RunComposerTarget | undefined): string {
  return t?.step === undefined ? "" : join(t.workflowID, t.step.path);
}

export function mountRunComposer(
  form: HTMLFormElement,
  input: HTMLTextAreaElement,
  send: HTMLButtonElement,
): void {
  if (mounted !== undefined) {
    return;
  }
  mounted = { form, input, send };
  send.replaceChildren(iconEl(ICON_SEND));
  form.addEventListener("submit", (e) => {
    e.preventDefault();
    void submit();
  });
  input.addEventListener("keydown", (e) => {
    if (e.key !== "Enter" || e.ctrlKey || e.altKey || e.isComposing) {
      return;
    }
    if (e.shiftKey) {
      const edit =
        input.selectionStart === input.selectionEnd
          ? continueList(input.value, input.selectionStart)
          : null;
      if (edit !== null) {
        e.preventDefault();
        applyEdit(input, edit);
      }
      return;
    }
    // Under a finger Return is a new line and only Send sends, as in the main composer.
    if (!e.metaKey && touchComposer()) {
      return;
    }
    e.preventDefault();
    void submit();
  });
  input.addEventListener("input", () => {
    if (draftKey !== "") {
      drafts.set(draftKey, input.value);
    }
  });
  input.addEventListener("focus", () => {
    input.enterKeyHint = touchComposer() ? "enter" : "send";
  });
  wireTouchComposer(input);
  paint();
}

export function setRunComposerTarget(next: RunComposerTarget): void {
  const nextKey = keyOf(next);
  target = next;
  if (mounted !== undefined && nextKey !== draftKey) {
    if (draftKey !== "") {
      drafts.set(draftKey, mounted.input.value);
    }
    draftKey = nextKey;
    mounted.input.value = drafts.get(nextKey) ?? "";
  }
  paint();
}

function paint(): void {
  if (mounted === undefined) {
    return;
  }
  const { form, input, send } = mounted;
  const mode = target === undefined ? undefined : composerMode(target);
  const disabled = mode === undefined || mode.kind === "disabled";
  // Read-only rather than disabled: disabling the focused box would blur it and close a phone's
  // keyboard on every send.
  const sending = !disabled && inFlight.has(draftKey);
  const reason = mode?.kind === "disabled" ? mode.reason : "";
  const placeholder =
    mode === undefined
      ? ""
      : mode.kind === "disabled"
        ? mode.reason
        : sending
          ? `Sending to ${target?.step?.label ?? ""}\u2026`
          : mode.placeholder;
  if (input.placeholder !== placeholder) {
    input.placeholder = placeholder;
  }
  input.disabled = disabled;
  input.readOnly = sending;
  send.disabled = disabled || sending;
  form.ariaBusy = sending ? "true" : null;
  form.dataset["mode"] = mode?.kind ?? "disabled";
  const tip = disabled
    ? reason
    : sending
      ? "Sending"
      : mode.kind === "answer"
        ? "Send your answer"
        : "Send";
  if (send.getAttribute("data-tooltip") !== tip) {
    send.setAttribute("data-tooltip", tip);
    // `disabled` already says it is unavailable; the reason is the tooltip's.
    send.setAttribute("aria-label", send.disabled ? "Send" : tip);
  }
  // Named for the step, not the send: the box keeps focus while its send is out.
  const name = mode === undefined || mode.kind === "disabled" ? placeholder : mode.placeholder;
  input.setAttribute(
    "aria-label",
    name === "" ? "Message the selected step" : name.replace(/\u2026$/, ""),
  );
}

async function submit(): Promise<void> {
  const t = target;
  if (mounted === undefined || t?.step === undefined) {
    return;
  }
  const mode = composerMode(t);
  if (mode.kind === "disabled") {
    return;
  }
  const key = keyOf(t);
  if (inFlight.has(key)) {
    return;
  }
  const { input } = mounted;
  const text = input.value.trim();
  if (text === "") {
    input.focus();
    return;
  }
  const workflowID = t.workflowID;
  const nodePath = t.step.path;
  const messageID = messageIDFor(key, text);
  input.value = "";
  drafts.delete(key);
  inFlight.add(key);
  paint();
  // Every send's settlement goes through run-step-steers.ts by id; a predicted steer also draws its
  // dock row on Send, the one optimistic surface.
  const send = beginStepMessage(workflowID, nodePath, messageID, text, mode.verb === "steer");
  const outcome = await messageRunStep.dispatch({
    workflowID,
    nodePath,
    text,
    message_id: messageID,
  }).outcome;
  inFlight.delete(key);
  paint();
  if (outcome.status === "success") {
    unknownSends.delete(messageID);
    settleStepMessage(
      send,
      outcome.value.verb === "steer"
        ? { kind: "steered", steerID: outcome.value.steer_id ?? "" }
        : { kind: "other" },
    );
    announce(outcome.value.verb === "answer" ? "Answer sent" : "Message sent");
    return;
  }
  const held = settleStepMessage(send, { kind: "failed" });
  if (outcome.status !== "error") {
    return;
  }
  if (held) {
    // The server already confirmed the row, so the words are delivered whatever the reply said.
    unknownSends.delete(messageID);
    announce("Message sent");
    return;
  }
  if (fateUnknown(outcome.error.status)) {
    unknownSends.set(messageID, { key, text });
  } else {
    unknownSends.delete(messageID);
  }
  drafts.set(key, text);
  if (draftKey === key) {
    mounted.input.value = text;
  }
  const message = refusalText(
    outcome.error.code,
    outcome.error.message || "Could not send the message",
  );
  announce(message);
  noticeAbout(`run:${workflowID}`, message, "error");
}

/** One Edit's take: the step's draft it wrote, the words, and the draft they replaced. */
interface RunComposerEdit {
  readonly draftKey: string;
  readonly taken: string;
  readonly before: string;
}

/** Put text in the box for the step on screen, the run tab's Edit. Undefined, writing nothing, while
 *  that step's send is out: the caller then takes nothing back. */
export function takeIntoRunComposer(text: string): RunComposerEdit | undefined {
  if (inFlight.has(draftKey)) {
    return undefined;
  }
  if (mounted === undefined) {
    return { draftKey: "", taken: text, before: "" };
  }
  const edit = { draftKey, taken: text, before: mounted.input.value };
  mounted.input.value = text;
  if (draftKey !== "") {
    drafts.set(draftKey, text);
  }
  mounted.input.focus();
  return edit;
}

/** Undo an Edit the server refused: put `before` back as its step's draft, but only while that draft
 *  is still the taken words. The box is touched only while that step is the one selected. */
export function restoreRunComposer(edit: RunComposerEdit): void {
  if (edit.draftKey === "") {
    return;
  }
  const box = mounted !== undefined && draftKey === edit.draftKey ? mounted.input : undefined;
  if ((box === undefined ? drafts.get(edit.draftKey) : box.value) !== edit.taken) {
    return;
  }
  drafts.set(edit.draftKey, edit.before);
  if (box !== undefined) {
    box.value = edit.before;
  }
}
