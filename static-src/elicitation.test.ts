// The MCP elicitation card: the link it offers, the control each MCP primitive schema renders as, and what Submit
// refuses or sends.

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
    const onSubmit = vi.fn<Submit>();
    const card = mount(oneField({ type: "string", format: "date-time" }), onSubmit);
    // A UA that offers seconds does so under a seconds step; the default 60 s step refuses them.
    field(card).step = "1";
    field(card).value = "2026-09-08T14:30:45";
    submitBtn(card).click();
    const got = onSubmit.mock.calls[0]?.[1]?.["when"];
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

/** Submit with the field set to value: the content sent, or null when Submit refused it. */
function submitted(
  schema: ElicitationPropertySchema,
  value: string,
): Record<string, unknown> | null {
  const onSubmit = vi.fn<Submit>();
  const card = mount(oneField(schema), onSubmit);
  field(card).value = value;
  submitBtn(card).click();
  return onSubmit.mock.calls[0]?.[1] ?? null;
}

describe("a stated constraint outranks the picker", () => {
  // A date picker writes one fixed shape, which a pattern or a length may refuse.
  it("a date with a pattern becomes a text box that still enforces it", () => {
    const schema = { type: "string", format: "date", pattern: "^2026-" };
    expect(typeOf(mount(oneField(schema), vi.fn<Submit>()))).toBe("text");
    expect(submitted(schema, "2025-01-01")).toBeNull();
    // JSON Schema's pattern is unanchored: the HTML attribute would refuse this whole-value mismatch.
    expect(submitted(schema, "2026-01-01")).toEqual({ when: "2026-01-01" });
  });

  it("a date-time with a minLength becomes a text box that still enforces it", () => {
    const schema = { type: "string", format: "date-time", minLength: 16 };
    expect(typeOf(mount(oneField(schema), vi.fn<Submit>()))).toBe("text");
    expect(submitted(schema, "2026-09-08")).toBeNull();
  });

  it("a date with a maxLength becomes a text box that still enforces it", () => {
    const schema = { type: "string", format: "date", maxLength: 10 };
    expect(typeOf(mount(oneField(schema), vi.fn<Submit>()))).toBe("text");
    expect(submitted(schema, "2026-09-08T")).toBeNull();
    expect(submitted(schema, "2026-09-08")).toEqual({ when: "2026-09-08" });
  });

  // `minLength: 0` constrains nothing; `maxLength: 0` forbids input.
  it("a date with a minLength of 0 keeps its picker", () => {
    const card = mount(oneField({ type: "string", format: "date", minLength: 0 }), vi.fn<Submit>());
    expect(typeOf(card)).toBe("date");
  });

  it("an email keeps its native type AND its pattern", () => {
    const schema = { type: "string", format: "email", pattern: "@example\\.com$" };
    expect(typeOf(mount(oneField(schema), vi.fn<Submit>()))).toBe("email");
    expect(submitted(schema, "a@other.org")).toBeNull();
    expect(submitted(schema, "a@example.com")).toEqual({ when: "a@example.com" });
  });

  it("a uri keeps its native type AND its length bounds", () => {
    const schema = { type: "string", format: "uri", minLength: 12, maxLength: 20 };
    expect(typeOf(mount(oneField(schema), vi.fn<Submit>()))).toBe("url");
    expect(submitted(schema, "https://a.b")).toBeNull();
    expect(submitted(schema, "https://example.com/long")).toBeNull();
    expect(submitted(schema, "https://ab.com")).toEqual({ when: "https://ab.com" });
  });
});

function urlCard(url: string, onSubmit: Submit = vi.fn<Submit>()): HTMLElement {
  return mount({ request_id: 1, mode: "url", message: "Connect your account", url }, onSubmit);
}

function buttonTexts(card: HTMLElement): string[] {
  return [...card.querySelectorAll("button")].map((b) => b.textContent);
}

// MCP 2025-11-25 "Safe URL Handling": show the full URL and its domain before consent, open only a web link.
describe("the URL card", () => {
  it("shows the host and the full URL it opens", () => {
    const card = urlCard("https://Example.com/connect?x=1");
    expect(card.querySelector(".elicitation-link-host")?.textContent).toBe("example.com");
    expect(card.querySelector(".elicitation-link-href")?.textContent).toBe(
      "https://example.com/connect?x=1",
    );
    expect(card.querySelector("a")?.getAttribute("href")).toBe("https://example.com/connect?x=1");
  });

  it("shows an internationalised host in the punycode that tells a look-alike apart", () => {
    const card = urlCard("https://ex\u0430mple.com/");
    expect(card.querySelector(".elicitation-link-host")?.textContent).toBe("xn--exmple-4nf.com");
  });

  it("answers accept with no content on Done", () => {
    const onSubmit = vi.fn<Submit>();
    const card = urlCard("https://example.com/", onSubmit);
    [...card.querySelectorAll("button")].find((b) => b.textContent === "Done")?.click();
    expect(onSubmit).toHaveBeenCalledWith("accept");
  });

  it.each([
    ["vscode://localhost/open"],
    ["javascript://localhost/%0Aalert(1)"],
    ["file:///etc/passwd"],
    [""],
  ])("never offers %s", (url) => {
    const card = urlCard(url);
    expect(card.querySelector("a[href]")).toBeNull();
    expect(buttonTexts(card)).toEqual(["Decline"]);
  });
});

// MCP 2025-11-25 Security Considerations: the client MUST show which server requests the input.
describe("the card names the asking server", () => {
  const github = { server_name: "github", tool_name: "create_issue" };

  it.each([
    ["a form", { ...oneField({ type: "string" }), mcp_tool: github }],
    ["a link", { request_id: 1, mode: "url", url: "https://example.com/", mcp_tool: github }],
  ] as const)("on %s", (_, p) => {
    const card = mount(p, vi.fn<Submit>());
    expect(card.querySelector(".elicitation-origin")?.textContent).toBe(
      "from github MCP integration",
    );
    expect(card.querySelector(".elicitation-origin-server")?.textContent).toBe("github");
  });

  it("states no origin when the server sent none", () => {
    const card = mount(oneField({ type: "string" }), vi.fn<Submit>());
    expect(card.querySelector(".elicitation-origin")).toBeNull();
  });
});

function form(
  properties: Record<string, ElicitationPropertySchema>,
  required: string[] = [],
): ElicitationNeededPayload {
  return {
    request_id: 1,
    mode: "form",
    message: "Fill this in",
    requested_schema: { properties, required },
  };
}

describe("choice fields render as pickers", () => {
  it("a titled single-select is a select of its consts, labelled by title", () => {
    const onSubmit = vi.fn<Submit>();
    const card = mount(
      form({
        size: {
          type: "string",
          oneOf: [
            { const: "s", title: "Small" },
            { const: "l", title: "Large" },
          ],
        },
      }),
      onSubmit,
    );
    const sel = card.querySelector("select");
    if (sel === null) {
      throw new Error("no select rendered");
    }
    expect([...sel.options].slice(1).map((o) => [o.value, o.textContent])).toEqual([
      ["s", "Small"],
      ["l", "Large"],
    ]);
    sel.value = "l";
    submitBtn(card).click();
    expect(onSubmit).toHaveBeenCalledWith("accept", { size: "l" });
  });

  it("legacy enumNames label the enum values", () => {
    const card = mount(
      form({ c: { type: "string", enum: ["r", "g"], enumNames: ["Red", "Green"] } }),
      vi.fn<Submit>(),
    );
    const opts = [...(card.querySelector("select")?.options ?? [])].slice(1);
    expect(opts.map((o) => [o.value, o.textContent])).toEqual([
      ["r", "Red"],
      ["g", "Green"],
    ]);
  });

  it.each([
    ["items.enum", { type: "string", enum: ["a", "b", "c"] }],
    [
      "items.anyOf",
      {
        anyOf: [
          { const: "a", title: "A" },
          { const: "b", title: "B" },
          { const: "c", title: "C" },
        ],
      },
    ],
  ])("a multi-select over %s is one checkbox per option, sent as an array", (_name, items) => {
    const onSubmit = vi.fn<Submit>();
    const card = mount(form({ tags: { type: "array", items, default: ["a"] } }), onSubmit);
    const boxes = [...card.querySelectorAll<HTMLInputElement>("fieldset input[type=checkbox]")];
    expect(boxes.map((b) => [b.value, b.checked])).toEqual([
      ["a", true],
      ["b", false],
      ["c", false],
    ]);
    boxes[2]?.click();
    submitBtn(card).click();
    expect(onSubmit).toHaveBeenCalledWith("accept", { tags: ["a", "c"] });
  });

  it("a multi-select's legend is no label, so clicking it checks nothing", () => {
    const card = mount(form({ tags: { type: "array", items: { enum: ["a"] } } }), vi.fn<Submit>());
    card.querySelector<HTMLElement>("legend")?.click();
    expect(card.querySelector<HTMLInputElement>("input[type=checkbox]")?.checked).toBe(false);
  });
});

describe("Submit refuses what the schema forbids", () => {
  it.each<[string, ElicitationPropertySchema, string]>([
    ["an email that is not one", { type: "string", format: "email" }, "not-an-email"],
    ["a uri that is not one", { type: "string", format: "uri" }, "not a url"],
    ["a pattern mismatch", { type: "string", pattern: "^2026-" }, "2025-01-01"],
    ["an integer given a fraction", { type: "integer" }, "1.5"],
    ["a number below its minimum", { type: "number", minimum: 5 }, "3"],
    ["a number above its maximum", { type: "number", maximum: 5 }, "6"],
    ["a value set below minLength", { type: "string", minLength: 3 }, "ab"],
    [
      "one astral rune against minLength 2, counted in code points",
      { type: "string", minLength: 2 },
      "\u{1F600}",
    ],
  ])("%s", (_name, schema, value) => {
    expect(submitted(schema, value)).toBeNull();
  });

  it("marks and focuses the refused field, and an edit clears the mark", () => {
    const card = mount(oneField({ type: "string", minLength: 3 }), vi.fn<Submit>());
    const inp = field(card);
    inp.value = "ab";
    submitBtn(card).click();
    expect(inp.classList.contains("elicitation-invalid")).toBe(true);
    expect(document.activeElement).toBe(inp);
    inp.value = "abc";
    inp.dispatchEvent(new Event("input"));
    expect(inp.classList.contains("elicitation-invalid")).toBe(false);
    expect(inp.validationMessage).toBe("");
  });

  it("one astral rune fits maxLength 1", () => {
    expect(submitted({ type: "string", maxLength: 1 }, "\u{1F600}")).toEqual({ when: "\u{1F600}" });
  });

  it("sends a number with a fraction, since a number has no step", () => {
    expect(submitted({ type: "number" }, "2.5")).toEqual({ when: 2.5 });
  });

  it("sends an integer as a number", () => {
    expect(submitted({ type: "integer" }, "3")).toEqual({ when: 3 });
  });

  it("refuses a multi-select below its minItems", () => {
    const onSubmit = vi.fn<Submit>();
    const card = mount(
      form({ tags: { type: "array", items: { enum: ["a", "b"] }, minItems: 2 } }),
      onSubmit,
    );
    card.querySelector<HTMLInputElement>("input[type=checkbox]")?.click();
    submitBtn(card).click();
    expect(onSubmit).not.toHaveBeenCalled();
    expect(card.querySelector("fieldset")?.classList.contains("elicitation-invalid")).toBe(true);
  });

  it("refuses a required multi-select with nothing checked", () => {
    const onSubmit = vi.fn<Submit>();
    const card = mount(
      form({ tags: { type: "array", items: { enum: ["a"] } } }, ["tags"]),
      onSubmit,
    );
    submitBtn(card).click();
    expect(onSubmit).not.toHaveBeenCalled();
  });

  it("refuses a required field left empty", () => {
    const onSubmit = vi.fn<Submit>();
    const card = mount(oneField({ type: "string" }, true), onSubmit);
    submitBtn(card).click();
    expect(onSubmit).not.toHaveBeenCalled();
  });

  it("never requires a boolean, whose unchecked box is the answer false", () => {
    const onSubmit = vi.fn<Submit>();
    const card = mount(form({ ok: { type: "boolean" } }, ["ok"]), onSubmit);
    submitBtn(card).click();
    expect(onSubmit).toHaveBeenCalledWith("accept", { ok: false });
  });
});

describe("required comes from the schema, not the label", () => {
  it("a field titled with a trailing star but not required may stay empty", () => {
    const onSubmit = vi.fn<Submit>();
    const card = mount(oneField({ type: "string", title: "Rating *" }), onSubmit);
    submitBtn(card).click();
    expect(onSubmit).toHaveBeenCalledWith("accept", {});
  });
});
