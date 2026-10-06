// User-input card: the agent's structured mid-turn question (kiro-cli v3 `_kiro/userInput`), in the
// interaction dock, which owns the queue and settle-once. Answers are plain strings (the wire
// contract): an option's title, the TUI's "Title [Sub1, Sub2]", or typed text; Skip advances the
// agent. A non-modal region, not a <dialog>: it must not cover the transcript or trap focus.

import { el } from "@cplieger/reactive";
import { askActions, askEditor, askHead } from "./dock-ask.js";
import type { UserInputNeededPayload, UserInputOption } from "./types.js";

type UserInputAction = "answered" | "dismissed";
type SubmitFn = (action: UserInputAction, answer?: string) => void;

/** Build the dock card for one agent question. The reporter is threaded through every stage, NOT
 *  module state: the answered card stays on screen through its advance animation beside the
 *  incoming one, and a shared reporter would report against the INCOMING decision, which `settle`'s
 *  guard cannot catch. */
export function buildUserInputCard(
  payload: UserInputNeededPayload,
  onSubmit: SubmitFn,
): HTMLElement {
  const { body } = askHead(payload.question !== "" ? payload.question : "The agent has a question");

  // All three regions always exist, so a stage switch replaces children in place; an EMPTY one
  // collapses in CSS (`:empty`).
  const optionsEl = el("div", { className: "user-input-options" });
  const editorEl = el("div", { className: "dock-ask-editor" });
  const actions = askActions();

  renderOptionsStage(optionsEl, editorEl, actions, payload.options ?? [], onSubmit);

  return el("div", { className: "dock-card dock-user-input" }, body, optionsEl, editorEl, actions);
}

/** Stage 1: the choice cards (or the free-form editor when no options). */
function renderOptionsStage(
  optionsEl: HTMLElement,
  editorEl: HTMLElement,
  actions: HTMLElement,
  options: readonly UserInputOption[],
  submit: SubmitFn,
): void {
  optionsEl.replaceChildren();
  editorEl.replaceChildren();
  actions.replaceChildren();

  for (const opt of options) {
    optionsEl.appendChild(
      optionCard(opt, () => {
        if ((opt.sub_options ?? []).length > 0) {
          renderSubOptionsStage(optionsEl, editorEl, actions, opt, options, submit);
        } else {
          submit("answered", opt.title);
        }
      }),
    );
  }

  // Send goes into `actions` BEFORE Skip, so the row reads primary-then-secondary
  // exactly as the run-input card's does.
  renderEditor(editorEl, actions, options.length > 0, submit);
  actions.appendChild(dismissButton(submit));
}

/** One selectable answer card: title + optional description + badge. */
function optionCard(opt: UserInputOption, onPick: () => void): HTMLElement {
  const title = el("span", { className: "user-input-option-title" }, opt.title);
  const head = el("span", { className: "user-input-option-head" }, title);
  if (opt.recommended === true) {
    head.appendChild(el("span", { className: "user-input-recommended" }, "Recommended"));
  }
  const card = el(
    "button",
    { type: "button", className: "user-input-option" },
    head,
  ) as HTMLButtonElement;
  if (opt.description !== undefined && opt.description !== "") {
    card.appendChild(el("span", { className: "user-input-option-desc" }, opt.description));
  }
  if ((opt.sub_options ?? []).length > 0) {
    card.appendChild(
      el(
        "span",
        { className: "user-input-option-more" },
        opt.sub_options_label ?? "Choose details\u2026",
      ),
    );
  }
  card.addEventListener("click", onPick);
  return card;
}

/** Stage 2 for an option with sub-options: a pre-checked multi-select.
 *  Confirm answers with the TUI's "Title [Sub1, Sub2]" format; Back
 *  returns to the options stage. */
function renderSubOptionsStage(
  optionsEl: HTMLElement,
  editorEl: HTMLElement,
  actions: HTMLElement,
  opt: UserInputOption,
  all: readonly UserInputOption[],
  submit: SubmitFn,
): void {
  optionsEl.replaceChildren();
  editorEl.replaceChildren();
  actions.replaceChildren();

  optionsEl.appendChild(
    el("div", { className: "user-input-sub-label" }, opt.sub_options_label ?? opt.title),
  );

  const boxes: { box: HTMLInputElement; title: string }[] = [];
  for (const sub of opt.sub_options ?? []) {
    const box = el("input", { type: "checkbox" }) as HTMLInputElement;
    box.checked = true; // TUI parity: all pre-selected, untick to exclude
    boxes.push({ box, title: sub.title });
    const label = el(
      "label",
      { className: "user-input-sub-option" },
      box,
      el("span", { className: "user-input-option-title" }, sub.title),
    );
    if (sub.description !== undefined && sub.description !== "") {
      label.appendChild(el("span", { className: "user-input-option-desc" }, sub.description));
    }
    optionsEl.appendChild(label);
  }

  const confirm = el(
    "button",
    { type: "button", className: "btn-small confirm-allow" },
    "Confirm",
  ) as HTMLButtonElement;
  confirm.addEventListener("click", () => {
    const picked = boxes.filter((b) => b.box.checked).map((b) => b.title);
    submit("answered", `${opt.title} [${picked.join(", ")}]`);
  });
  const back = el(
    "button",
    { type: "button", className: "btn-small" },
    "Back",
  ) as HTMLButtonElement;
  back.addEventListener("click", () => {
    renderOptionsStage(optionsEl, editorEl, actions, all, submit);
  });
  actions.append(confirm, back, dismissButton(submit));
}

/** The typed-answer editor: primary for a free-form question, compact under option cards. The box
 *  goes in `editorEl` and Send in `actions`, the shared ask card's one wrapping button row. */
function renderEditor(
  editorEl: HTMLElement,
  actions: HTMLElement,
  hasOptions: boolean,
  submit: SubmitFn,
): void {
  const { input } = askEditor({
    rows: hasOptions ? "1" : "3",
    placeholder: hasOptions ? "Or type your own answer\u2026" : "Type your answer\u2026",
    label: "Your answer",
  });
  const send = el(
    "button",
    { type: "button", className: "btn-small confirm-allow" },
    "Send",
  ) as HTMLButtonElement;
  send.addEventListener("click", () => {
    const text = input.value.trim();
    if (text !== "") {
      submit("answered", text);
    } else {
      input.focus();
    }
  });
  input.addEventListener("keydown", (e) => {
    if (e.key === "Enter" && !e.shiftKey) {
      e.preventDefault();
      send.click();
    }
  });
  editorEl.appendChild(input);
  actions.appendChild(send);
}

function dismissButton(submit: SubmitFn): HTMLButtonElement {
  const btn = el(
    "button",
    { type: "button", className: "btn-small confirm-danger" },
    "Skip",
  ) as HTMLButtonElement;
  btn.addEventListener("click", () => {
    // Explicit `undefined` rather than a one-argument call: the wire contract is
    // an action with no answer, and the arity is what `user-input.test.ts`
    // asserts.
    submit("dismissed", undefined);
  });
  return btn;
}

/** Reset module state for test isolation. Production never calls this. A NO-OP: this module holds
 *  no state; kept for `user-input.test.ts`'s import. */
export function _resetForTest(): void {
  // Nothing to reset.
}
