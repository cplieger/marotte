// The one add row of Settings (`.add-form`, 17-settings.css): labelled fields, then the submit button, then one error
// line. Static rows are written in index.html in the same shape; built rows come from buildAddForm.

import { el } from "@cplieger/ui";

type Field = HTMLInputElement | HTMLSelectElement;

export interface AddFormOptions {
  /** A returned promise holds the button disabled until it settles, whatever is typed meanwhile. */
  readonly onSubmit: () => unknown;
  /** Why the client refuses the values as typed, or null; shown on the error line while it holds. */
  readonly refuse?: () => string | null;
  /** The field a refusal names, inside the form or not; its edits re-check the row. The first required one when unset. */
  readonly refuses?: Field;
  /** True while the row must not submit for a reason outside its own save, such as a lock. */
  readonly busy?: () => boolean;
}

export interface AddForm {
  readonly form: HTMLFormElement;
  readonly button: HTMLButtonElement;
  /** True when a press would do nothing: a required field empty, a refusal, busy, or a submit in flight. */
  blocked(): boolean;
  /** Repaint the button after a change no input event announces (a value set by code, a lock). */
  sync(): void;
  /** Write the error line, or clear it with null; `field`, the one the message concerns, is marked invalid and
   *  described by the line. */
  setError(message: string | readonly (Node | string)[] | null, field?: Field): void;
}

function fieldsOf(form: HTMLFormElement): Field[] {
  return [...form.querySelectorAll<Field>("input, select")];
}

/** Wire a `.add-form`: Enter and the button submit through the form, and the button is disabled while blocked. */
export function wireAddForm(form: HTMLFormElement, opts: AddFormOptions): AddForm {
  const button = form.querySelector<HTMLButtonElement>("button[type='submit']");
  const error = form.querySelector<HTMLOutputElement>("output.rf-error");
  if (button === null || error === null) {
    throw new Error("an .add-form needs a submit button and an output.rf-error");
  }
  const required = (): Field[] => fieldsOf(form).filter((f) => f.required);
  let pending = false;
  let marked: Field | undefined;
  // Whether marking `marked` added the error id to its aria-describedby, so a clear removes only what it added.
  let linked = false;
  const describedBy = (f: Field): string[] =>
    (f.getAttribute("aria-describedby") ?? "").split(" ").filter((t) => t !== "");
  const unmark = (): void => {
    for (const f of [...fieldsOf(form), marked]) {
      f?.removeAttribute("aria-invalid");
    }
    if (marked !== undefined && linked) {
      const rest = describedBy(marked).filter((t) => t !== error.id);
      if (rest.length === 0) {
        marked.removeAttribute("aria-describedby");
      } else {
        marked.setAttribute("aria-describedby", rest.join(" "));
      }
    }
    marked = undefined;
    linked = false;
  };
  const control: AddForm = {
    form,
    button,
    blocked: () =>
      pending ||
      required().some((f) => f.value.trim() === "") ||
      (opts.refuse?.() ?? null) !== null ||
      (opts.busy?.() ?? false),
    sync: () => {
      button.disabled = control.blocked();
    },
    setError: (message, field) => {
      if (message === null) {
        error.replaceChildren();
      } else if (typeof message === "string") {
        error.textContent = message;
      } else {
        error.replaceChildren(...message);
      }
      unmark();
      if (message === null || field === undefined) {
        return;
      }
      marked = field;
      field.setAttribute("aria-invalid", "true");
      const ids = describedBy(field);
      if (error.id !== "" && !ids.includes(error.id)) {
        field.setAttribute("aria-describedby", [...ids, error.id].join(" "));
        linked = true;
      }
    },
  };
  const onEdit = (): void => {
    control.setError(opts.refuse?.() ?? null, opts.refuses ?? required()[0]);
    control.sync();
  };
  form.addEventListener("input", onEdit);
  form.addEventListener("change", onEdit);
  if (opts.refuses !== undefined && opts.refuses.form !== form) {
    opts.refuses.addEventListener("input", onEdit);
  }
  form.addEventListener("submit", (e) => {
    e.preventDefault();
    if (control.blocked()) {
      return;
    }
    const result = opts.onSubmit();
    if (result instanceof Promise) {
      // aria-busy keeps the disabled button at full ink with a progress cursor (40-a11y.css).
      pending = true;
      button.setAttribute("aria-busy", "true");
      control.sync();
      void result.finally(() => {
        pending = false;
        button.removeAttribute("aria-busy");
        control.sync();
      });
    }
  });
  control.sync();
  return control;
}

interface AddFieldSpec {
  readonly label: string;
  readonly control: Field;
  /** An optional field says so in its label and does not hold the button. */
  readonly optional?: boolean;
  /** The field's share of the row: a short value, or free text that takes the larger share. */
  readonly size?: "compact" | "grow";
}

export interface AddFormSpec {
  readonly fields: readonly AddFieldSpec[];
  /** The button's text: a verb and the noun the row creates, or the list's own verb ("Block tool", "Test"). */
  readonly label: string;
  readonly ariaLabel: string;
  readonly id?: string;
  readonly errorId: string;
}

/** Build a `.add-form` row in the shape index.html writes by hand; wire it with wireAddForm. */
export function buildAddForm(spec: AddFormSpec): HTMLFormElement {
  const labels = spec.fields.map((f) => {
    f.control.classList.add(f.control instanceof HTMLSelectElement ? "rf-select" : "rf-input");
    f.control.required = f.optional !== true;
    if (f.control.required) {
      f.control.setAttribute("aria-describedby", spec.errorId);
    }
    const text = el("span", { className: "rf-label" }, f.label);
    if (f.optional === true) {
      text.append(" ", el("span", { className: "rf-optional" }, "(optional)"));
    }
    const cls = f.size === undefined ? "rf-field" : `rf-field rf-${f.size}`;
    return el("label", { className: cls }, text, f.control);
  });
  const button = el(
    "button",
    { type: "submit", className: "btn" },
    spec.label,
  ) as HTMLButtonElement;
  button.disabled = true;
  const error = el("output", { className: "rf-error", id: spec.errorId });
  return el(
    "form",
    {
      className: "add-form",
      "aria-label": spec.ariaLabel,
      ...(spec.id === undefined ? {} : { id: spec.id }),
    },
    el("div", { className: "add-form-fields" }, ...labels),
    button,
    error,
  ) as HTMLFormElement;
}
