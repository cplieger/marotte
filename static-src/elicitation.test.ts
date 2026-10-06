// The MCP elicitation form card's string fields: which native input a JSON-Schema `format` picks, and what it reads back.

import { describe, it, expect, vi, beforeEach } from "vitest";

import { buildElicitationCard } from "./elicitation.js";
import type { ElicitationNeededPayload, ElicitationPropertySchema } from "./types.js";

type Submit = (action: string, content?: Record<string, unknown>) => void;

function oneField(schema: ElicitationPropertySchema, required = false): ElicitationNeededPayload {
  return {
    request_id: 1,
    mode: "form",
    message: "Fill this in",
    requested_schema: { properties: { when: schema }, required: required ? ["when"] : [] },
  };
}

function mount(p: ElicitationNeededPayload, onSubmit: Submit): HTMLElement {
  const card = buildElicitationCard(p, onSubmit);
  document.body.replaceChildren(card);
  return card;
}

function field(card: HTMLElement): HTMLInputElement {
  const inp = card.querySelector<HTMLInputElement>("input.elicitation-input");
  if (inp === null) {
    throw new Error("no elicitation input rendered");
  }
  return inp;
}

function submitBtn(card: HTMLElement): HTMLButtonElement {
  const btn = [...card.querySelectorAll<HTMLButtonElement>("button")].find(
    (b) => b.textContent === "Submit",
  );
  if (btn === undefined) {
    throw new Error("no submit button rendered");
  }
  return btn;
}

/** Read off the content attribute, so an unimplemented type still reports what was asked. */
function typeOf(card: HTMLElement): string | null {
  return field(card).getAttribute("type");
}

function answer(
  schema: ElicitationPropertySchema,
  value: string,
): Record<string, unknown> | undefined {
  const onSubmit = vi.fn<Submit>();
  const card = mount(oneField(schema), onSubmit);
  field(card).value = value;
  submitBtn(card).click();
  expect(onSubmit).toHaveBeenCalledTimes(1);
  return onSubmit.mock.calls[0]?.[1];
}

/** RFC 3339 `date-time` (full date, `T`, time with seconds, offset), as a shape so it holds in every `TZ`. */
const RFC3339 = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:\d{2})$/;

beforeEach(() => {
  document.body.replaceChildren();
});

describe("the format → native input type map", () => {
  it.each([
    ["email", "email"],
    ["uri", "url"],
    ["date", "date"],
    ["date-time", "datetime-local"],
  ])("a format of %s renders type=%s", (format, want) => {
    expect(typeOf(mount(oneField({ type: "string", format }), vi.fn<Submit>()))).toBe(want);
  });

  it.each([["duration"], ["ipv6"], ["hostname"], [""]])(
    "an unmapped format of %s falls back to a text box",
    (format) => {
      expect(typeOf(mount(oneField({ type: "string", format }), vi.fn<Submit>()))).toBe("text");
    },
  );

  it("a schema stating no format at all is a text box", () => {
    expect(typeOf(mount(oneField({ type: "string" }), vi.fn<Submit>()))).toBe("text");
  });

  // `format` is arbitrary wire text: `table["constructor"]` on a record yields a function, so inherited members must
  // answer like absent ones.
  it.each([["constructor"], ["toString"]])(
    "a format of %s is a text box, not a prototype member",
    (format) => {
      expect(typeOf(mount(oneField({ type: "string", format }), vi.fn<Submit>()))).toBe("text");
    },
  );
});

describe("format: date-time reads back as RFC 3339", () => {
  it("supplies the seconds and the offset a datetime-local omits", () => {
    expect(answer({ type: "string", format: "date-time" }, "2026-09-08T14:30")?.["when"]).toMatch(
      RFC3339,
    );
  });

  it("keeps the seconds a step-bearing control already supplied", () => {
    const got = answer({ type: "string", format: "date-time" }, "2026-09-08T14:30:45")?.["when"];
    // Anchored on the literal local part: an unconditional `:00` append would satisfy an expectation built from `got`.
    expect(got).toMatch(/^2026-09-08T14:30:45(?:Z|[+-]\d{2}:\d{2})$/);
  });

  // Catches a wrong offset: `getTimezoneOffset()` is negative east of UTC, so an inverted sign lands 2× off.
  it("names the same instant the local value named", () => {
    const local = "2026-09-08T14:30";
    const got = String(answer({ type: "string", format: "date-time" }, local)?.["when"]);
    expect(new Date(got).getTime()).toBe(new Date(local).getTime());
  });

  // Only has teeth where the two dates' offsets differ (Europe/Paris, America/New_York); in UTC, where CI runs, it is inert.
  it("resolves a date on the other side of a DST boundary from its own offset", () => {
    const winter = "2026-01-08T14:30";
    const got = String(answer({ type: "string", format: "date-time" }, winter)?.["when"]);
    expect(new Date(got).getTime()).toBe(new Date(winter).getTime());
  });

  // `filled` reads the raw value, so the key's absence is the whole observable surface.
  it("omits an empty control from the answer", () => {
    const onSubmit = vi.fn<Submit>();
    const card = mount(oneField({ type: "string", format: "date-time" }), onSubmit);
    submitBtn(card).click();
    expect(onSubmit).toHaveBeenCalledWith("accept", {});
  });
});

describe("format: date needs no normalizing", () => {
  // A date input's value is RFC 3339 `full-date` already.
  it("passes its value through untouched", () => {
    expect(answer({ type: "string", format: "date" }, "2026-09-08")?.["when"]).toBe("2026-09-08");
  });
});

describe("a stated constraint outranks the picker", () => {
  // HTML applies `pattern`, `minLength` and `maxLength` to text-ish types only, so a date picker drops them silently.
  it("a date with a pattern becomes a text box that still enforces it", () => {
    const card = mount(
      oneField({ type: "string", format: "date", pattern: "^2026-" }),
      vi.fn<Submit>(),
    );
    expect(typeOf(card)).toBe("text");
    expect(field(card).pattern).toBe("^2026-");
  });

  it("a date-time with a minLength becomes a text box that still enforces it", () => {
    const card = mount(
      oneField({ type: "string", format: "date-time", minLength: 16 }),
      vi.fn<Submit>(),
    );
    expect(typeOf(card)).toBe("text");
    expect(field(card).minLength).toBe(16);
  });

  it("a date with a maxLength becomes a text box that still enforces it", () => {
    const card = mount(
      oneField({ type: "string", format: "date", maxLength: 10 }),
      vi.fn<Submit>(),
    );
    expect(typeOf(card)).toBe("text");
    expect(field(card).maxLength).toBe(10);
  });

  // `minLength: 0` constrains nothing; `maxLength: 0` forbids input.
  it("a date with a minLength of 0 keeps its picker", () => {
    const card = mount(oneField({ type: "string", format: "date", minLength: 0 }), vi.fn<Submit>());
    expect(typeOf(card)).toBe("date");
  });

  // `email` and `uri` honour all three attributes, so they keep their native type.
  it("an email keeps its native type AND its pattern", () => {
    const card = mount(
      oneField({ type: "string", format: "email", pattern: ".+@example\\.com" }),
      vi.fn<Submit>(),
    );
    expect(typeOf(card)).toBe("email");
    expect(field(card).pattern).toBe(".+@example\\.com");
  });

  it("a uri keeps its native type AND its length bounds", () => {
    const card = mount(
      oneField({ type: "string", format: "uri", minLength: 8, maxLength: 200 }),
      vi.fn<Submit>(),
    );
    expect(typeOf(card)).toBe("url");
    expect(field(card).minLength).toBe(8);
    expect(field(card).maxLength).toBe(200);
  });
});
