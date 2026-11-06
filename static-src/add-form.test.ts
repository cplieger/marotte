import { afterEach, describe, expect, it } from "vitest";

import { fill } from "./__test-helpers__/add-forms.js";
import { buildAddForm, wireAddForm } from "./add-form.js";

function row(
  opts: {
    refuse?: () => string | null;
    busy?: () => boolean;
    onSubmit?: () => unknown;
  } = {},
) {
  const required = document.createElement("input");
  const optional = document.createElement("input");
  const form = buildAddForm({
    fields: [
      { label: "Path", control: required },
      { label: "Name", control: optional, optional: true },
    ],
    label: "Add thing",
    ariaLabel: "Add a thing",
    errorId: "thing-error",
  });
  document.body.append(form);
  const submits: string[] = [];
  const ctl = wireAddForm(form, {
    ...opts,
    onSubmit: () => {
      submits.push(required.value);
      return opts.onSubmit?.();
    },
  });
  const error = form.querySelector("output")!;
  return { ctl, form, required, optional, submits, error };
}

afterEach(() => {
  document.body.replaceChildren();
});

describe("the add row's button", () => {
  it("follows the required field: typing enables it, clearing or blanks disable it", () => {
    const { ctl, required } = row();
    expect(ctl.button.disabled).toBe(true);
    fill(required, "docs");
    expect(ctl.button.disabled).toBe(false);
    fill(required, "   ");
    expect(ctl.button.disabled).toBe(true);
    fill(required, "");
    expect(ctl.button.disabled).toBe(true);
  });

  it("ignores the optional field", () => {
    const { ctl, optional } = row();
    fill(optional, "name");
    expect(ctl.button.disabled).toBe(true);
  });

  it("stays disabled while busy, and sync() releases it when the busy state ends", () => {
    let busy = true;
    const { ctl, required } = row({ busy: () => busy });
    fill(required, "docs");
    expect(ctl.button.disabled).toBe(true);
    busy = false;
    ctl.sync();
    expect(ctl.button.disabled).toBe(false);
  });

  it("shows a refusal on the error line, marks the field and disables the button", () => {
    const { ctl, required, error } = row({
      refuse: () => (required.value === "." ? '"." is not a filename.' : null),
    });
    fill(required, ".");
    expect(ctl.button.disabled).toBe(true);
    expect(error.textContent).toBe('"." is not a filename.');
    expect(required.getAttribute("aria-invalid")).toBe("true");
    fill(required, ".env");
    expect(ctl.button.disabled).toBe(false);
    expect(error.textContent).toBe("");
    expect(required.hasAttribute("aria-invalid")).toBe(false);
  });
});

describe("submitting", () => {
  it("submits on Enter through the form, with the typed value", () => {
    const { form, required, submits } = row();
    fill(required, "docs");
    form.requestSubmit();
    expect(submits).toEqual(["docs"]);
  });

  it("holds the button disabled while a returned save is in flight, whatever is typed", async () => {
    let settle = (): void => undefined;
    const { ctl, form, required, optional, submits } = row({
      onSubmit: () =>
        new Promise<void>((resolve) => {
          settle = resolve;
        }),
    });
    fill(required, "docs");
    form.requestSubmit();
    expect(ctl.button.disabled).toBe(true);
    expect(ctl.button.getAttribute("aria-busy")).toBe("true");
    fill(required, "docs2");
    fill(optional, "name");
    expect(ctl.button.disabled).toBe(true);
    form.requestSubmit();
    expect(submits).toEqual(["docs"]);
    settle();
    await Promise.resolve();
    await Promise.resolve();
    expect(ctl.button.disabled).toBe(false);
    expect(ctl.button.hasAttribute("aria-busy")).toBe(false);
  });

  it("does nothing while blocked, even when the form is submitted directly", () => {
    const { form, submits } = row();
    form.requestSubmit();
    expect(submits).toEqual([]);
  });

  it("points the required field, and only it, at the error line", () => {
    const { required, optional } = row();
    expect(required.required).toBe(true);
    expect(required.getAttribute("aria-describedby")).toBe("thing-error");
    expect(optional.required).toBe(false);
    expect(optional.hasAttribute("aria-describedby")).toBe(false);
  });
});

describe("setError", () => {
  it("writes and clears the line, marking only the field it names", () => {
    const { ctl, required, optional, error } = row();
    ctl.setError("refused", optional);
    expect(error.textContent).toBe("refused");
    expect(optional.getAttribute("aria-invalid")).toBe("true");
    expect(required.hasAttribute("aria-invalid")).toBe(false);
    ctl.setError(null);
    expect(error.textContent).toBe("");
    expect(optional.hasAttribute("aria-invalid")).toBe(false);
  });

  it("takes nodes, such as a Retry button", () => {
    const { ctl, error } = row();
    const retry = document.createElement("button");
    ctl.setError(["Saved, but not reloaded. ", retry]);
    expect(error.lastChild).toBe(retry);
  });
});
