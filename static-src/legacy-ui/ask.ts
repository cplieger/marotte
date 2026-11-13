/**
 * ask.ts — ONE Promise-shaped question dialog, replacing window.confirm and
 * window.prompt. Two shapes (boolean / input) share one preemption domain and
 * the `.uip-ask` class family, each over its own lazily-created reused dialog.
 *
 * @module
 */

import { el, untracked } from "@cplieger/reactive";

import { listenBackdrop } from "./dialog.js";
import { dialogEngine, type DialogEngine, type DialogFacet } from "./dialog-engine.js";

/** Input configuration for an input-collecting ask. */
interface AskInput {
  /** Input type. Default `"text"`. */
  type?: "text" | "password";
  /** Pre-filled value, focused and selected on open (like window.prompt). */
  initialValue?: string;
  /** Input placeholder. */
  placeholder?: string;
  /** Maximum input length (the native `maxlength` constraint). */
  maxLength?: number;
  /** Autocomplete hint (e.g. `"new-password"` for a password-manager-aware
   *  password ask). Default `"off"`. */
  autocomplete?: string;
}

/** Presentation and shape of one ask: heading, button labels, destructive
 *  treatment, and whether a value is collected. The presence of `input` decides
 *  what the promise resolves to — `boolean` without it, `string | null` with
 *  it — so the two are not independent choices. */
export interface AskOptions {
  /** Optional concise heading. With a title, the dialog is labelled by it and
   *  described by the message; without one, the message is the label. */
  title?: string;
  /** OK button text. Default `"Confirm"` (boolean ask) / `"OK"` (input ask). */
  confirmLabel?: string;
  /** Cancel button text. Default `"Cancel"`. */
  cancelLabel?: string;
  /** `"destructive"` upgrades to `role="alertdialog"`, marks OK
   *  `is-destructive`, and (boolean ask only) focuses Cancel. */
  variant?: "normal" | "destructive";
  /** Collect a value: the ask gains an input (the message becomes its
   *  `<label>`) and resolves `string | null` instead of `boolean`. Pass a
   *  configuration object or `true` for a default text input. */
  input?: AskInput | true;
  /** Facts shown under the message as a label and value list, in order, as
   *  text. An empty list shows nothing. A non-array, or an entry whose label
   *  or value is not a string, throws a `TypeError` before the dialog opens. */
  details?: readonly { label: string; value: string }[];
}

interface AskRefs {
  dialog: HTMLDialogElement;
  title: HTMLElement;
  message: HTMLElement;
  /** Kept out of the document while the question has no details, so its id
   *  never dangles. */
  details: HTMLElement;
  ok: HTMLButtonElement;
  cancel: HTMLButtonElement;
}

interface AskInputRefs extends AskRefs {
  input: HTMLInputElement;
  form: HTMLFormElement;
}

const INPUT_ID = "uip-ask-input";

type Shape = "boolean" | "input";
type Answer = boolean | string | null;

/** Everything one ask shows, read once from the caller's options. */
interface View {
  readonly message: string;
  readonly title: string;
  readonly confirmLabel: string;
  readonly cancelLabel: string;
  readonly destructive: boolean;
  readonly details: readonly { readonly label: string; readonly value: string }[];
  readonly input: {
    readonly type: "text" | "password";
    readonly initialValue: string;
    readonly placeholder: string | undefined;
    readonly maxLength: number | undefined;
    readonly autocomplete: string;
  } | null;
}

interface Question {
  readonly id: number;
  readonly view: View;
  readonly cancelValue: boolean | null;
  readonly resolve: (value: Answer) => void;
  readonly reject: (error: unknown) => void;
}

interface Slot {
  readonly refs: AskRefs | AskInputRefs;
  readonly engine: DialogEngine;
  readonly facet: DialogFacet;
  /** The last question shown on this dialog. */
  question: Question | null;
}

let questionSeq = 0;
/** The one live question across both shapes; only it is unsettled. */
let current: Question | null = null;
const slots: Record<Shape, Slot | null> = { boolean: null, input: null };

/** Replace the live question, settling the outgoing one exactly once. */
function replaceCurrent(
  next: Question | null,
  outcome?: { readonly value: Answer } | { readonly error: unknown },
): void {
  const previous = current;
  current = next;
  if (previous === null) {
    return;
  }
  if (outcome !== undefined && "error" in outcome) {
    previous.reject(outcome.error);
  } else {
    previous.resolve(outcome === undefined ? previous.cancelValue : outcome.value);
  }
}

function detailsOf(value: unknown): View["details"] {
  if (value === undefined) {
    return [];
  }
  if (!Array.isArray(value)) {
    throw new TypeError("legacy-ui/ask: details is not an array");
  }
  // Array.from, not map: a hole is an entry too, and is refused.
  return Array.from(value as readonly unknown[], (entry, i) => {
    const { label, value: text } = (entry ?? {}) as {
      readonly label?: unknown;
      readonly value?: unknown;
    };
    if (typeof label !== "string" || typeof text !== "string") {
      throw new TypeError(`legacy-ui/ask: details[${String(i)}] needs a string label and value`);
    }
    return { label, value: text };
  });
}

function snapshot(message: string, opts: AskOptions | undefined): View {
  // First, so a refused list throws before any other option getter runs.
  const details = detailsOf(opts?.details);
  const inputOpt = opts?.input;
  const input = inputOpt === undefined ? null : inputOpt === true ? {} : inputOpt;
  return {
    message,
    title: opts?.title ?? "",
    confirmLabel: opts?.confirmLabel ?? (input !== null ? "OK" : "Confirm"),
    cancelLabel: opts?.cancelLabel ?? "Cancel",
    destructive: (opts?.variant ?? "normal") === "destructive",
    details,
    input:
      input === null
        ? null
        : {
            type: input.type ?? "text",
            initialValue: input.initialValue ?? "",
            placeholder: input.placeholder,
            maxLength: input.maxLength,
            autocomplete: input.autocomplete ?? "off",
          },
  };
}

function writeView(refs: AskRefs, input: AskInputRefs | null, view: View): void {
  const describedBy: string[] = [];
  if (view.title !== "") {
    // With a title, the dialog's accessible NAME is the concise title and its
    // DESCRIPTION is the message body (alertdialog wants a short name + body).
    refs.title.textContent = view.title;
    refs.title.hidden = false;
    refs.dialog.setAttribute("aria-labelledby", refs.title.id);
    describedBy.push(refs.message.id);
  } else {
    refs.title.textContent = "";
    refs.title.hidden = true;
    refs.dialog.setAttribute("aria-labelledby", refs.message.id);
  }
  refs.message.textContent = view.message;
  refs.details.replaceChildren(
    ...view.details.flatMap((d) => [el("dt", null, d.label), el("dd", null, d.value)]),
  );
  if (view.details.length > 0) {
    refs.message.after(refs.details);
    describedBy.push(refs.details.id);
  } else {
    refs.details.remove();
  }
  if (describedBy.length > 0) {
    refs.dialog.setAttribute("aria-describedby", describedBy.join(" "));
  } else {
    refs.dialog.removeAttribute("aria-describedby");
  }
  refs.ok.textContent = view.confirmLabel;
  refs.cancel.textContent = view.cancelLabel;
  if (view.destructive) {
    refs.dialog.setAttribute("role", "alertdialog");
    refs.ok.classList.add("is-destructive");
  } else {
    refs.dialog.removeAttribute("role");
    refs.ok.classList.remove("is-destructive");
  }
  if (input !== null && view.input !== null) {
    input.input.type = view.input.type;
    input.input.value = view.input.initialValue;
    // The attribute, not the IDL property: the property's `AutoFill` union
    // does not cover every legal token list the autocomplete grammar allows.
    input.input.setAttribute("autocomplete", view.input.autocomplete);
    if (view.input.placeholder !== undefined) {
      input.input.placeholder = view.input.placeholder;
    } else {
      input.input.removeAttribute("placeholder");
    }
    if (view.input.maxLength !== undefined) {
      input.input.maxLength = view.input.maxLength;
    } else {
      input.input.removeAttribute("maxlength");
    }
  }
}

/** Settle the live question `id` with `value` and fade its dialog. */
function answer(slot: Slot, id: number, value: Answer): void {
  if (current?.id !== id) {
    return;
  }
  replaceCurrent(null, { value });
  slot.engine.close();
  slot.engine.flush();
}

function createSlot(shape: Shape): Slot {
  const input = shape === "input" ? buildInput() : null;
  const refs: AskRefs = input ?? buildBoolean();
  let appliedId = 0;
  let armedFor = 0;
  let focusedId = 0;
  let selectedId = 0;
  let listeners: AbortController | null = null;
  const disarm = (): void => {
    listeners?.abort();
    listeners = null;
    armedFor = 0;
  };
  const arm = (q: Question): void => {
    disarm();
    const controller = new AbortController();
    const { signal } = controller;
    listeners = controller;
    armedFor = q.id;
    if (input !== null) {
      input.form.addEventListener(
        "submit",
        () => {
          answer(slot, q.id, input.input.value);
        },
        { signal },
      );
    } else {
      refs.ok.addEventListener(
        "click",
        () => {
          answer(slot, q.id, true);
        },
        { signal },
      );
    }
    refs.cancel.addEventListener(
      "click",
      () => {
        answer(slot, q.id, q.cancelValue);
      },
      { signal },
    );
    refs.dialog.addEventListener(
      "cancel",
      (e) => {
        // A descendant's bubbling `cancel` is not an Escape (see createDialog).
        if (e.target !== refs.dialog) {
          return;
        }
        e.preventDefault();
        answer(slot, q.id, q.cancelValue);
      },
      { signal },
    );
    const removeBackdrop = listenBackdrop(refs.dialog, () => {
      answer(slot, q.id, q.cancelValue);
    });
    signal.addEventListener("abort", removeBackdrop);
  };

  const facet: DialogFacet = {
    prepare(fx, d) {
      const q = slot.question;
      if (!refs.dialog.isConnected) {
        // Mounted again when the page cleared the body.
        fx.append(document.body, refs.dialog);
      }
      if (q !== null && appliedId !== d.subject && q.id === d.subject) {
        appliedId = q.id;
        writeView(refs, input, q.view);
      }
      if (q !== null && q === current) {
        if (armedFor !== q.id) {
          arm(q);
        }
      } else {
        disarm();
      }
    },
    follow(fx, d) {
      const q = slot.question;
      if (d.phase !== "open" || q === null || q !== current) {
        return;
      }
      // Native showModal() traps Tab and restores focus; only the initial
      // placement is ours: the input, or Cancel for a destructive question.
      if (focusedId !== q.id) {
        focusedId = q.id;
        fx.focus(input?.input ?? (q.view.destructive ? refs.cancel : refs.ok));
      }
      if (input !== null && selectedId !== q.id) {
        selectedId = q.id;
        fx.select(input.input);
      }
    },
    notify(_fx, d) {
      // A close the browser made, or a question never shown, cancels it.
      if (d.phase === "closed" && slot.question !== null && slot.question === current) {
        replaceCurrent(null);
      }
    },
    cleanup() {
      disarm();
    },
    refused(subject, error) {
      if (current?.id === subject) {
        replaceCurrent(null, error === undefined ? undefined : { error });
      }
    },
  };
  const slot: Slot = { refs, engine: dialogEngine(refs.dialog), facet, question: null };
  slot.engine.attach(facet);
  return slot;
}

/** The shared action row (Cancel, OK). */
function buildActions(): { ok: HTMLButtonElement; cancel: HTMLButtonElement; row: HTMLElement } {
  const cancel = el("button", {
    type: "button",
    className: "uip-ask-cancel",
  }) as HTMLButtonElement;
  // In the input shape OK is the form's submit button (Enter in the input
  // activates it); in the boolean shape it is a plain button.
  const ok = el("button", { type: "button", className: "uip-ask-ok" }) as HTMLButtonElement;
  const row = el("div", { className: "uip-ask-actions" }, cancel, ok);
  return { ok, cancel, row };
}

function buildBoolean(): AskRefs {
  const title = el("h2", { className: "uip-ask-title", id: "uip-ask-title" });
  const message = el("p", { className: "uip-ask-msg", id: "uip-ask-msg" });
  const details = el("dl", { className: "uip-ask-details", id: "uip-ask-details" });
  const { ok, cancel, row } = buildActions();
  const dialog = el("dialog", { className: "uip-ask" }, title, message, row) as HTMLDialogElement;
  return { dialog, title, message, details, ok, cancel };
}

function buildInput(): AskInputRefs {
  const title = el("h2", { className: "uip-ask-title", id: "uip-ask-input-title" });
  // The message is the input's real <label>, so the association is native
  // form semantics rather than ARIA plumbing.
  const message = el("label", {
    className: "uip-ask-msg",
    id: "uip-ask-input-msg",
    for: INPUT_ID,
  }) as HTMLLabelElement;
  const details = el("dl", { className: "uip-ask-details", id: "uip-ask-input-details" });
  const input = el("input", { className: "uip-ask-input", id: INPUT_ID }) as HTMLInputElement;
  const { ok, cancel, row } = buildActions();
  ok.type = "submit";
  const form = el("form", { className: "uip-ask-form" }, message, input, row) as HTMLFormElement;
  // Never the form's own submission: a submit after the answer, while the
  // dialog fades out, would otherwise reload the page.
  form.addEventListener("submit", (e) => {
    e.preventDefault();
  });
  const dialog = el(
    "dialog",
    { className: "uip-ask uip-ask--input" },
    title,
    form,
  ) as HTMLDialogElement;
  return { dialog, title, message, details, input, form, ok, cancel };
}

/** Show a question dialog. Without `input`, resolves `true` on confirm and
 *  `false` on cancel, Escape, backdrop click, or preemption by a later
 *  `ask()`. */
export function ask(message: string, opts?: AskOptions & { input?: undefined }): Promise<boolean>;
/** Show a single-input dialog (`input` present). Resolves the input's value
 *  on OK / Enter — as-is, not trimmed; an empty submission resolves `""`,
 *  distinct from the `null` of cancellation — or `null` on Cancel, Escape,
 *  backdrop click, or preemption by a later `ask()`. */
export function ask(
  message: string,
  opts: AskOptions & { input: AskInput | true },
): Promise<string | null>;
export function ask(message: string, opts?: AskOptions): Promise<boolean | string | null> {
  // Every option is read here, once, before any shared state: an ask() from a
  // getter runs to its own commit and is then preempted by this one.
  const view = untracked(() => snapshot(message, opts));
  const shape: Shape = view.input === null ? "boolean" : "input";
  return new Promise<Answer>((resolve, reject) => {
    const mine = (slots[shape] ??= createSlot(shape));
    const other = slots[shape === "input" ? "boolean" : "input"];
    const q: Question = {
      id: ++questionSeq,
      view,
      cancelValue: view.input === null ? false : null,
      resolve,
      reject,
    };
    replaceCurrent(q);
    mine.question = q;
    mine.engine.open(q.id);
    other?.engine.close();
    other?.engine.flush();
    mine.engine.flush();
  });
}

/** Test-only: settle the live question, remove both shared dialogs and reset internal state. */
// deadset:ignore DS1102 -- test seam: settles the live question and removes both shared dialogs
export function _resetForTest(): void {
  replaceCurrent(null);
  for (const shape of ["boolean", "input"] as const) {
    const slot = slots[shape];
    if (slot !== null) {
      slot.engine.retire(slot.facet);
      slot.engine.flush();
      slot.refs.dialog.remove();
      slots[shape] = null;
    }
  }
}
