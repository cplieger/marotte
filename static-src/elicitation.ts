// Elicitation card: an MCP server requests structured input mid-tool-call (forwarded by kiro-cli over ACP), rendered
// in the dock, which owns the queue and settle-once guard.

import { el } from "@cplieger/reactive";
import type { ElicitationNeededPayload, ElicitationPropertySchema } from "./types.js";

type ElicitAction = "accept" | "decline" | "cancel";
type SubmitFn = (action: ElicitAction, content?: Record<string, unknown>) => void;

// Each field registers a reader; an empty optional field is omitted rather than sent as "".
type FieldReader = () => { name: string; value: unknown; filled: boolean };

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

  const isURL = payload.mode === "url" && payload.url !== undefined && payload.url !== "";
  const readers: FieldReader[] = [];

  if (isURL) {
    body.appendChild(
      el(
        "a",
        {
          className: "elicitation-url btn-small confirm-allow",
          href: payload.url ?? "",
          target: "_blank",
          rel: "noopener noreferrer",
        },
        "Open link\u2026",
      ),
    );
  } else {
    const schema = payload.requested_schema;
    const required = new Set(schema?.required ?? []);
    const props = schema?.properties ?? {};
    for (const name of Object.keys(props)) {
      // eslint-disable-next-line @typescript-eslint/no-non-null-assertion
      readers.push(renderField(fieldsEl, name, props[name]!, required.has(name)));
    }
  }

  const submitBtn = el(
    "button",
    { type: "button", className: "btn-small confirm-allow" },
    isURL ? "Done" : "Submit",
  );
  submitBtn.addEventListener("click", () => {
    const content = isURL ? undefined : collect(readers, fieldsEl);
    if (!isURL && content === null) {
      return; // a required field is empty; collect marked it.
    }
    onSubmit("accept", content ?? undefined);
  });

  const declineBtn = el(
    "button",
    { type: "button", className: "btn-small confirm-danger" },
    "Decline",
  );
  declineBtn.addEventListener("click", () => {
    onSubmit("decline");
  });

  actions.append(submitBtn, declineBtn);

  return el("div", { className: "dock-card dock-elicitation" }, body, fieldsEl, actions);
}

function renderField(
  container: HTMLElement,
  name: string,
  schema: ElicitationPropertySchema,
  required: boolean,
): FieldReader {
  const wrap = el("label", { className: "elicitation-field" });

  const labelText = el(
    "span",
    { className: "elicitation-label" },
    (schema.title !== undefined && schema.title !== "" ? schema.title : name) +
      (required ? " *" : ""),
  );
  wrap.appendChild(labelText);

  if (schema.description !== undefined && schema.description !== "") {
    const hint = el("span", { className: "elicitation-hint" }, schema.description);
    wrap.appendChild(hint);
  }

  const control = buildControl(name, schema);
  wrap.appendChild(control.el);
  container.appendChild(wrap);

  return () => {
    const { value, filled } = control.read();
    return { name, value, filled };
  };
}

interface Control {
  el: HTMLElement;
  read: () => { value: unknown; filled: boolean };
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

function buildControl(name: string, schema: ElicitationPropertySchema): Control {
  if (schema.enum !== undefined && schema.enum.length > 0) {
    const sel = el(
      "select",
      { className: "elicitation-input", name },
      el("option", { value: "" }, "\u2014"),
    ) as HTMLSelectElement;
    for (const opt of schema.enum) {
      sel.appendChild(el("option", { value: opt }, opt));
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
      if (schema.type === "integer") {
        inp.step = "1";
      }
      if (typeof schema.default === "number") {
        inp.value = String(schema.default);
      }
      return {
        el: inp,
        read: () => {
          if (inp.value === "") {
            return { value: undefined, filled: false };
          }
          const n = schema.type === "integer" ? parseInt(inp.value, 10) : parseFloat(inp.value);
          return { value: Number.isNaN(n) ? undefined : n, filled: !Number.isNaN(n) };
        },
      };
    }
    case "array": {
      // The wire schema has no structured items: comma-separated input becomes a string[].
      const inp = el("input", {
        type: "text",
        className: "elicitation-input",
        name,
        placeholder: "comma,separated,values",
      }) as HTMLInputElement;
      return {
        el: inp,
        read: () => {
          const parts = inp.value
            .split(",")
            .map((s) => s.trim())
            .filter((s) => s !== "");
          return { value: parts, filled: parts.length > 0 };
        },
      };
    }
    default: {
      // `minLength: 0` constrains nothing; `maxLength: 0` forbids any input.
      const stated =
        (schema.pattern !== undefined && schema.pattern !== "") ||
        (typeof schema.minLength === "number" && schema.minLength > 0) ||
        typeof schema.maxLength === "number";
      const mapped = FORMAT_INPUT_TYPES.get(schema.format ?? "");
      // A stated constraint outranks the picker: types ignoring `pattern`/`minLength`/`maxLength` drop them silently, so
      // such a date falls back to a text box. `email` and `uri` honour all three.
      const inp = el("input", {
        type: mapped === undefined || (stated && !mapped.constrained) ? "text" : mapped.type,
        className: "elicitation-input",
        name,
      }) as HTMLInputElement;
      if (schema.pattern !== undefined && schema.pattern !== "") {
        inp.pattern = schema.pattern;
      }
      if (typeof schema.minLength === "number") {
        inp.minLength = schema.minLength;
      }
      if (typeof schema.maxLength === "number") {
        inp.maxLength = schema.maxLength;
      }
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
          return { value, filled: raw.trim() !== "" };
        },
      };
    }
  }
}

/** All filled values, or null (marking the field) when a required one is empty. */
function collect(readers: FieldReader[], container: HTMLElement): Record<string, unknown> | null {
  const required = new Set<string>();
  for (const labelEl of container.querySelectorAll<HTMLElement>(".elicitation-label")) {
    if (labelEl.textContent.endsWith(" *")) {
      // Strip the trailing " *" and recover the name from its input.
      const input = labelEl.parentElement?.querySelector<HTMLElement>("[name]");
      const n = input?.getAttribute("name");
      if (n !== null && n !== undefined) {
        required.add(n);
      }
    }
  }

  const out: Record<string, unknown> = {};
  let missing: HTMLElement | null = null;
  for (const read of readers) {
    const { name, value, filled } = read();
    if (!filled) {
      if (required.has(name) && missing === null) {
        missing = container.querySelector<HTMLElement>(`[name="${CSS.escape(name)}"]`);
      }
      continue;
    }
    out[name] = value;
  }

  if (missing !== null) {
    missing.classList.add("elicitation-invalid");
    missing.focus();
    return null;
  }
  return out;
}
