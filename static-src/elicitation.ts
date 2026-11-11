// Elicitation card: an MCP server requests structured input mid-tool-call (forwarded by kiro-cli over ACP), rendered
// in the dock, which owns the queue and settle-once guard.

import { el } from "@cplieger/reactive";
import type {
  ElicitationEnumOption,
  ElicitationNeededPayload,
  ElicitationPropertySchema,
} from "./types.js";
import { isHttpURL } from "./url-safety.js";

type ElicitAction = "accept" | "decline" | "cancel";
type SubmitFn = (action: ElicitAction, content?: Record<string, unknown>) => void;

/** One rendered field: an empty optional one is omitted from the answer rather than sent as "". */
interface Field {
  name: string;
  read: () => { value: unknown; filled: boolean };
  /** The control to report and the element to mark when the field breaks its schema, else null. */
  check: () => { report: HTMLInputElement | HTMLSelectElement | null; mark: HTMLElement } | null;
}

/** Build the dock card for one elicitation request. */
export function buildElicitationCard(
  payload: ElicitationNeededPayload,
  onSubmit: SubmitFn,
): HTMLElement {
  const body = el("div", { className: "elicitation-body" });
  const fieldsEl = el("div", { className: "elicitation-fields" });
  const actions = el("div", { className: "elicitation-actions" });

  body.appendChild(
    el(
      "strong",
      null,
      payload.message !== undefined && payload.message !== "" ? payload.message : "Input requested",
    ),
  );
  if (payload.mcp_tool !== undefined) {
    body.appendChild(
      el(
        "span",
        { className: "elicitation-origin" },
        "from ",
        el("span", { className: "elicitation-origin-server" }, payload.mcp_tool.server_name),
        " MCP integration",
      ),
    );
  }

  const declineBtn = el("button", { type: "button", className: "btn-small btn-danger" }, "Decline");
  declineBtn.addEventListener("click", () => {
    onSubmit("decline");
  });

  if (payload.mode === "url") {
    const url = payload.url ?? "";
    if (!isHttpURL(url)) {
      body.appendChild(
        el(
          "span",
          { className: "elicitation-hint" },
          "This request\u2019s link is not a web address, so it cannot be opened.",
        ),
      );
      actions.append(declineBtn);
      return el("div", { className: "dock-card dock-elicitation" }, body, fieldsEl, actions);
    }
    body.appendChild(linkBlock(new URL(url)));
    const doneBtn = el("button", { type: "button", className: "btn-small confirm-allow" }, "Done");
    doneBtn.addEventListener("click", () => {
      onSubmit("accept");
    });
    actions.append(doneBtn, declineBtn);
    return el("div", { className: "dock-card dock-elicitation" }, body, fieldsEl, actions);
  }

  const schema = payload.requested_schema;
  const required = new Set(schema?.required ?? []);
  const props = schema?.properties ?? {};
  const fields: Field[] = [];
  for (const [name, prop] of Object.entries(props)) {
    fields.push(renderField(fieldsEl, name, prop, required.has(name)));
  }

  const submitBtn = el(
    "button",
    { type: "button", className: "btn-small confirm-allow" },
    "Submit",
  );
  submitBtn.addEventListener("click", () => {
    const content = collect(fields);
    if (content !== null) {
      onSubmit("accept", content);
    }
  });
  actions.append(submitBtn, declineBtn);

  return el("div", { className: "dock-card dock-elicitation" }, body, fieldsEl, actions);
}

/**
 * MCP 2025-11-25 "Safe URL Handling": the full URL and its domain are shown before consent, and what is shown is what
 * opens. `u.host` is punycode for an IDN, which exposes a look-alike domain.
 */
function linkBlock(u: URL): HTMLElement {
  return el(
    "div",
    { className: "elicitation-link" },
    el("span", { className: "elicitation-link-host" }, u.host),
    el("span", { className: "elicitation-link-href" }, u.href),
    el(
      "a",
      {
        className: "elicitation-url btn-small confirm-allow",
        href: u.href,
        target: "_blank",
        rel: "noopener noreferrer",
      },
      "Open link\u2026",
    ),
  );
}

function labelOf(name: string, schema: ElicitationPropertySchema, required: boolean): string {
  return (
    (schema.title !== undefined && schema.title !== "" ? schema.title : name) +
    (required ? " *" : "")
  );
}

function hintOf(schema: ElicitationPropertySchema): HTMLElement | null {
  return schema.description !== undefined && schema.description !== ""
    ? el("span", { className: "elicitation-hint" }, schema.description)
    : null;
}

function renderField(
  container: HTMLElement,
  name: string,
  schema: ElicitationPropertySchema,
  required: boolean,
): Field {
  if (schema.type === "array") {
    return renderChoiceGroup(container, name, schema, required);
  }
  const wrap = el(
    "label",
    { className: "elicitation-field" },
    el("span", { className: "elicitation-label" }, labelOf(name, schema, required)),
    hintOf(schema),
  );
  const control = buildControl(name, schema);
  // A required checkbox would have to be checked; false is an answer.
  if (!(control.el instanceof HTMLInputElement && control.el.type === "checkbox")) {
    control.el.required = required;
  }
  wrap.appendChild(control.el);
  container.appendChild(wrap);
  const clear = (): void => {
    control.el.setCustomValidity("");
    control.el.classList.remove("elicitation-invalid");
  };
  control.el.addEventListener("input", clear);
  control.el.addEventListener("change", clear);
  return {
    name,
    read: control.read,
    check: () => {
      control.el.setCustomValidity(control.problem?.() ?? "");
      return control.el.checkValidity() ? null : { report: control.el, mark: control.el };
    },
  };
}

/** A multi-select: one labelled checkbox per option, in a fieldset so a legend click toggles nothing. */
function renderChoiceGroup(
  container: HTMLElement,
  name: string,
  schema: ElicitationPropertySchema,
  required: boolean,
): Field {
  const group = el(
    "fieldset",
    { className: "elicitation-field elicitation-choices" },
    el("legend", { className: "elicitation-label" }, labelOf(name, schema, required)),
    hintOf(schema),
  );
  const checked = new Set(Array.isArray(schema.default) ? schema.default : []);
  const boxes: HTMLInputElement[] = [];
  for (const opt of multiSelectOptions(schema)) {
    const box = el("input", {
      type: "checkbox",
      className: "elicitation-checkbox",
      name,
    }) as HTMLInputElement;
    box.value = opt.const;
    box.checked = checked.has(opt.const);
    boxes.push(box);
    group.appendChild(el("label", { className: "elicitation-choice" }, box, opt.title));
  }
  container.appendChild(group);
  group.addEventListener("change", () => {
    boxes[0]?.setCustomValidity("");
    group.classList.remove("elicitation-invalid");
  });
  const values = (): string[] => boxes.filter((b) => b.checked).map((b) => b.value);
  return {
    name,
    read: () => {
      const v = values();
      return { value: v, filled: v.length > 0 };
    },
    check: () => {
      const problem = choiceCountProblem(values().length, schema, required);
      const first = boxes[0] ?? null;
      first?.setCustomValidity(problem);
      return problem === "" ? null : { report: first, mark: group };
    },
  };
}

/** Item counts apply to an answer that is sent; an untouched optional group is omitted. */
function choiceCountProblem(
  n: number,
  schema: ElicitationPropertySchema,
  required: boolean,
): string {
  if (n === 0) {
    return required ? "Select at least one option." : "";
  }
  if (typeof schema.minItems === "number" && n < schema.minItems) {
    return `Select at least ${String(schema.minItems)} options.`;
  }
  if (typeof schema.maxItems === "number" && n > schema.maxItems) {
    return `Select at most ${String(schema.maxItems)} options.`;
  }
  return "";
}

function multiSelectOptions(schema: ElicitationPropertySchema): ElicitationEnumOption[] {
  const items = schema.items;
  if (items?.anyOf !== undefined && items.anyOf.length > 0) {
    return items.anyOf;
  }
  return (items?.enum ?? []).map((v) => ({ const: v, title: v }));
}

/** A single-select's options, or null for a free-text field. Legacy enumNames label the values when they pair up. */
function singleSelectOptions(schema: ElicitationPropertySchema): ElicitationEnumOption[] | null {
  if (schema.oneOf !== undefined && schema.oneOf.length > 0) {
    return schema.oneOf;
  }
  const values = schema.enum;
  if (values === undefined || values.length === 0) {
    return null;
  }
  const names = schema.enumNames?.length === values.length ? schema.enumNames : undefined;
  return values.map((v, i) => ({ const: v, title: names?.[i] ?? v }));
}

interface Control {
  el: HTMLInputElement | HTMLSelectElement;
  read: () => { value: unknown; filled: boolean };
  /** What native validity cannot see, as the custom-validity message; "" when fine. */
  problem?: () => string;
}

/** JSON-Schema `format` values with a native input type. `date-time` is the schema spelling, `datetime-local` the HTML one. */
const FORMAT_INPUT_TYPES = new Map<string, { type: string; constrained: boolean }>([
  ["email", { type: "email", constrained: true }],
  ["uri", { type: "url", constrained: true }],
  ["date", { type: "date", constrained: false }],
  ["date-time", { type: "datetime-local", constrained: false }],
]);

/** A `datetime-local` value: `YYYY-MM-DDTHH:mm`, seconds only where `step` asks. */
const LOCAL_DATE_TIME = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}(:\d{2}(?:\.\d+)?)?$/;

/**
 * A `datetime-local` value as RFC 3339 `date-time`: JSON Schema's `date-time` requires seconds and an offset, which
 * the control's value lacks.
 */
function toRFC3339(value: string): string {
  const shape = LOCAL_DATE_TIME.exec(value);
  const at = new Date(value);
  // Unreadable input is answered as typed; "" must stay "" rather than become now.
  if (shape === null || Number.isNaN(at.getTime())) {
    return value;
  }
  const withSeconds = shape[1] === undefined ? `${value}:00` : value;
  // getTimezoneOffset() is negative east of UTC: UTC+02:00 reports -120 and renders "+02:00".
  const offset = at.getTimezoneOffset();
  const magnitude = Math.abs(offset);
  const hh = String(Math.floor(magnitude / 60)).padStart(2, "0");
  const mm = String(magnitude % 60).padStart(2, "0");
  return `${withSeconds}${offset <= 0 ? "+" : "-"}${hh}:${mm}`;
}

/**
 * A string answer's schema checks, kept off the native attributes: JSON Schema counts length in code points (the
 * browser's minLength/maxLength count UTF-16 units and flag only a user edit), and its `pattern` is an unanchored
 * ECMA-262 regex (the HTML attribute anchors it to the whole value).
 */
function stringProblem(value: string, schema: ElicitationPropertySchema): string {
  if (value === "") {
    return "";
  }
  const n = Array.from(value).length;
  if (typeof schema.minLength === "number" && n < schema.minLength) {
    return `Enter at least ${String(schema.minLength)} characters.`;
  }
  if (typeof schema.maxLength === "number" && n > schema.maxLength) {
    return `Enter at most ${String(schema.maxLength)} characters.`;
  }
  const pattern = compilePattern(schema.pattern);
  if (pattern !== null && !pattern.test(value)) {
    return "Enter a value in the requested format.";
  }
  return "";
}

/** A pattern this browser cannot compile constrains nothing here; the MCP server still checks it. */
function compilePattern(pattern: string | undefined): RegExp | null {
  if (pattern === undefined || pattern === "") {
    return null;
  }
  try {
    return new RegExp(pattern, "u");
  } catch {
    return null;
  }
}

function buildControl(name: string, schema: ElicitationPropertySchema): Control {
  const options = singleSelectOptions(schema);
  if (options !== null) {
    const sel = el(
      "select",
      { className: "elicitation-input", name },
      el("option", { value: "" }, "\u2014"),
    ) as HTMLSelectElement;
    for (const opt of options) {
      sel.appendChild(el("option", { value: opt.const }, opt.title));
    }
    if (typeof schema.default === "string") {
      sel.value = schema.default;
    }
    return { el: sel, read: () => ({ value: sel.value, filled: sel.value !== "" }) };
  }

  switch (schema.type) {
    case "boolean": {
      const box = el("input", {
        type: "checkbox",
        className: "elicitation-checkbox",
        name,
      }) as HTMLInputElement;
      if (schema.default === true) {
        box.checked = true;
      }
      // A checkbox is always filled: false is an answer.
      return { el: box, read: () => ({ value: box.checked, filled: true }) };
    }
    case "number":
    case "integer": {
      const inp = el("input", {
        type: "number",
        className: "elicitation-input",
        name,
      }) as HTMLInputElement;
      // The default step of 1 would refuse 2.5 for a number.
      inp.step = schema.type === "integer" ? "1" : "any";
      if (typeof schema.minimum === "number") {
        inp.min = String(schema.minimum);
      }
      if (typeof schema.maximum === "number") {
        inp.max = String(schema.maximum);
      }
      if (typeof schema.default === "number") {
        inp.value = String(schema.default);
      }
      return {
        el: inp,
        read: () => ({ value: Number(inp.value), filled: inp.value !== "" }),
      };
    }
    default: {
      // `minLength: 0` constrains nothing; `maxLength: 0` forbids any input.
      const stated =
        (schema.pattern !== undefined && schema.pattern !== "") ||
        (typeof schema.minLength === "number" && schema.minLength > 0) ||
        typeof schema.maxLength === "number";
      const mapped = FORMAT_INPUT_TYPES.get(schema.format ?? "");
      // A stated constraint outranks the picker: a date or date-time picker writes one fixed shape the constraint may
      // refuse, so such a field falls back to a text box. `email` and `uri` are typed text and keep their type.
      const inp = el("input", {
        type: mapped === undefined || (stated && !mapped.constrained) ? "text" : mapped.type,
        className: "elicitation-input",
        name,
      }) as HTMLInputElement;
      // `datetime-local` sanitization accepts no offset, so an RFC 3339 default would render an empty picker.
      if (typeof schema.default === "string") {
        inp.value = schema.default;
      }
      return {
        el: inp,
        read: () => {
          const raw = inp.value;
          // Read off the control: a UA without the picker reports `text`, where the user typed the whole string.
          const value = inp.type === "datetime-local" ? toRFC3339(raw) : raw;
          return { value, filled: raw !== "" };
        },
        problem: () => stringProblem(inp.value, schema),
      };
    }
  }
}

/** Every filled value, or null once the first field breaking its schema is reported and marked. */
function collect(fields: Field[]): Record<string, unknown> | null {
  for (const f of fields) {
    const bad = f.check();
    if (bad !== null) {
      bad.mark.classList.add("elicitation-invalid");
      bad.report?.reportValidity();
      bad.report?.focus();
      return null;
    }
  }
  const out: Record<string, unknown> = {};
  for (const f of fields) {
    const { value, filled } = f.read();
    if (filled) {
      out[f.name] = value;
    }
  }
  return out;
}
