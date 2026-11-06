// Every add control Settings shows, and the ones it opens, is one shape: a form.add-form whose labelled fields end in
// its submit button, with one error line under the row. The static adders are sliced from the shipped page and the
// built ones come from their real builders, so a fixture cannot keep passing after either regresses.
import { afterAll, beforeAll, describe, expect, it } from "vitest";
import { page as browserPage } from "vitest/browser";

import indexHtml from "../static/index.html?raw";
import { manifestSheets, mountAppCSS } from "./__test-helpers__/css-rules.js";
import { forgeRow } from "./__test-helpers__/forge-rows.js";
import { renderConnectionTarget } from "./forge-auth-connection.js";
import { buildOwnerScopes } from "./forge-auth-owners.js";
import { renderPATForm } from "./forge-auth-pat.js";
import { initKnowledge } from "./knowledge.js";

interface Adder {
  readonly name: string;
  /** The fields in row order; the button follows the last one. */
  readonly fields: readonly string[];
  /** The fields the button stays disabled without. */
  readonly required: readonly string[];
  readonly label: string;
}

const ADDERS: readonly Adder[] = [
  {
    name: "Active policy, Add a rule",
    fields: [
      "#native-rule-scope",
      "#native-rule-capability",
      "#native-rule-effect",
      "#native-rule-match",
      "#native-rule-exclude",
    ],
    required: ["#native-rule-capability"],
    label: "Add rule",
  },
  {
    name: "Active policy, Test a decision",
    fields: ["#native-explain-capability", "#native-explain-resource"],
    required: ["#native-explain-capability"],
    label: "Test",
  },
  {
    name: "Agent ignore files",
    fields: ["#agent-ignore-input"],
    required: ["#agent-ignore-input"],
    label: "Add file",
  },
  {
    name: "Additional directories",
    fields: ["#agent-dirs-input"],
    required: ["#agent-dirs-input"],
    label: "Add directory",
  },
  {
    name: "a forge account's Owners",
    fields: [".forge-account-owners input"],
    required: [".forge-account-owners input"],
    label: "Add owner",
  },
  {
    name: "Sources, Add an account's token",
    fields: ["form[data-forge-token] input[type='password']"],
    required: ["form[data-forge-token] input[type='password']"],
    label: "Connect",
  },
  {
    name: "Knowledge bases",
    fields: ["#knowledge-add-path", "#knowledge-add-name"],
    required: ["#knowledge-add-path"],
    label: "Add knowledge base",
  },
  {
    name: "MCP Disabled tools",
    fields: ["#mcp-disabled-input"],
    required: ["#mcp-disabled-input"],
    label: "Block tool",
  },
];

/** Sections of the shipped page that hold an adder, mounted whole so its hints and lists keep their places. */
const STATIC_SECTIONS = [
  "#native-policy-section",
  "#agent-ignore-section",
  "#agent-dirs-section",
  "#mcp-disabled-tools",
  "#knowledge-section",
] as const;

const WIDTHS = {
  desktop: { viewport: [1280, 900], host: 760, pointer: "fine" },
  phone: { viewport: [390, 844], host: 358, pointer: "coarse" },
} as const;

/** `--ctl-h-dense`, stated so a token retune fails here. */
const CTL_H_DENSE = 32;

const ctx = document.createElement("canvas").getContext("2d")!;
const shipped = new DOMParser().parseFromString(indexHtml, "text/html");
const host = document.createElement("div");
let style: HTMLStyleElement;

beforeAll(() => {
  style = mountAppCSS();
  host.className = "settings-add-controls-host";
  host.style.cssText = "position:absolute;top:0;left:0;";
  for (const sel of STATIC_SECTIONS) {
    const section = shipped.querySelector(sel);
    if (section === null) {
      throw new Error(`static/index.html has no ${sel}`);
    }
    const node = document.importNode(section, true) as HTMLElement;
    node.classList.remove("hidden");
    host.append(node);
  }
  host.querySelector<HTMLDetailsElement>("#native-policy-explain-box")!.open = true;
  const owners = buildOwnerScopes(forgeRow()) as HTMLDetailsElement;
  owners.open = true;
  host.append(owners);
  const pane = document.createElement("div");
  renderPATForm(pane, "github", pane, renderConnectionTarget("github"), {
    connected: () => undefined,
  });
  host.append(pane);
  document.body.append(host);
  initKnowledge();
  document.querySelector<HTMLButtonElement>("#knowledge-add-btn")!.click();
});

afterAll(() => {
  host.remove();
  style.remove();
  document.documentElement.removeAttribute("data-pointer");
});

function field(sel: string): HTMLInputElement | HTMLSelectElement {
  const f = host.querySelector<HTMLInputElement | HTMLSelectElement>(sel);
  if (f === null) {
    throw new Error(`no ${sel} mounted`);
  }
  return f;
}

/** The form the adder's fields belong to, failing the case when they are not in one `form.add-form`. */
function formOf(a: Adder): HTMLFormElement {
  const forms = new Set(a.fields.map((s) => field(s).form));
  expect(forms.size, `${a.name}: every field submits one form`).toBe(1);
  const form = [...forms][0];
  expect(form, `${a.name}: the fields sit in a <form>, so Enter submits`).toBeInstanceOf(
    HTMLFormElement,
  );
  expect(form!.classList.contains("add-form"), `${a.name}: the form is a .add-form`).toBe(true);
  return form!;
}

function submitOf(form: HTMLFormElement): HTMLButtonElement {
  const buttons = [...form.querySelectorAll<HTMLButtonElement>("button")].filter(
    (b) => b.type === "submit",
  );
  expect(buttons.length, "one submit button").toBe(1);
  return buttons[0]!;
}

function errorOf(form: HTMLFormElement): HTMLOutputElement {
  const last = form.lastElementChild;
  expect(last, "the form ends in its error line").toBeInstanceOf(HTMLOutputElement);
  expect(last!.classList.contains("rf-error"), "the error line is an output.rf-error").toBe(true);
  return last as HTMLOutputElement;
}

describe.each(ADDERS)("$name", (a) => {
  it("is a form.add-form of labelled fields, then its submit button, then its error line", () => {
    const form = formOf(a);
    const button = submitOf(form);
    const fields = a.fields.map((s) => field(s).closest("label"));
    const kids = [...form.children];
    const wrap = kids[0];
    expect(wrap?.classList.contains("add-form-fields"), "the fields lead in .add-form-fields").toBe(
      true,
    );
    expect([...(wrap?.children ?? [])], "the wrapper holds only the fields, in order").toEqual(
      fields,
    );
    for (const [i, label] of fields.entries()) {
      expect(label, `${a.fields[i]!} is the wrapper's child`).toBe(wrap?.children[i]);
    }
    expect(kids[1], "the submit follows the fields").toBe(button);
    expect(kids[2], "the error line follows the submit").toBe(errorOf(form));
    expect(kids.length, "nothing else in the row").toBe(3);
    for (const [i, label] of fields.entries()) {
      expect(label?.classList.contains("rf-field"), `${a.fields[i]!} is in a label.rf-field`).toBe(
        true,
      );
      expect(
        label?.querySelector(".rf-label")?.textContent.trim(),
        `${a.fields[i]!} shows a visible label`,
      ).toBeTruthy();
      const f = field(a.fields[i]!);
      expect(
        f.classList.contains(f instanceof HTMLSelectElement ? "rf-select" : "rf-input"),
        `${a.fields[i]!} takes the shared field class`,
      ).toBe(true);
      const cs = getComputedStyle(f);
      expect(
        `${cs.borderTopStyle} ${cs.borderTopWidth}`,
        `${a.fields[i]!} paints the theme border`,
      ).toBe("solid 1px");
    }
  });

  it(`is a neutral .btn named "${a.label}"`, () => {
    const button = submitOf(formOf(a));
    expect(button.textContent.trim()).toBe(a.label);
    expect(button.classList.contains("btn"), "the neutral .btn").toBe(true);
    for (const other of ["action-pill", "btn-small", "btn-primary"]) {
      expect(button.classList.contains(other), `not a .${other}`).toBe(false);
    }
  });

  it("is disabled while a required field is empty, and marks that field required", () => {
    const form = formOf(a);
    for (const s of a.required) {
      expect(field(s).required, `${s} is required`).toBe(true);
      expect(field(s).value, `${s} mounts empty`).toBe("");
    }
    expect(submitOf(form).disabled).toBe(true);
  });

  it("points its required field at the error line", () => {
    const form = formOf(a);
    const error = errorOf(form);
    expect(error.id, "the error line has an id").not.toBe("");
    const described = (field(a.required[0]!).getAttribute("aria-describedby") ?? "").split(" ");
    expect(described).toContain(error.id);
  });
});

describe.each(Object.entries(WIDTHS))("at %s width", (_width, w) => {
  beforeAll(async () => {
    await browserPage.viewport(w.viewport[0], w.viewport[1]);
    document.documentElement.setAttribute("data-pointer", w.pointer);
    host.style.inlineSize = `${String(w.host)}px`;
  });

  describe.each(ADDERS)("$name", (a) => {
    it("leaves every field room for its whole placeholder", () => {
      formOf(a);
      for (const s of a.fields) {
        const f = field(s);
        if (!(f instanceof HTMLInputElement) || f.placeholder === "") {
          continue;
        }
        const cs = getComputedStyle(f);
        ctx.font = cs.font;
        const room = f.clientWidth - parseFloat(cs.paddingLeft) - parseFloat(cs.paddingRight);
        expect(room, `${s} shows "${f.placeholder}"`).toBeGreaterThanOrEqual(
          ctx.measureText(f.placeholder).width,
        );
      }
    });

    it("paints its button --ctl-h-dense tall, beside its last field and centred on it", () => {
      const form = formOf(a);
      const button = submitOf(form).getBoundingClientRect();
      const last = field(a.fields.at(-1)!).getBoundingClientRect();
      expect(button.height, "the dense control height on every tier").toBeCloseTo(CTL_H_DENSE, 0);
      expect(button.left, "the button follows its last field").toBeGreaterThanOrEqual(last.right);
      expect(
        button.top + button.height / 2,
        "the button is centred on its last field's line",
      ).toBeCloseTo(last.top + last.height / 2, 0);
    });

    it("collapses its error line while empty and shows it as a full row under the fields", () => {
      const form = formOf(a);
      const error = errorOf(form);
      expect(error.getBoundingClientRect().height, "an empty line takes no room").toBe(0);
      error.textContent = "That did not work.";
      try {
        const box = error.getBoundingClientRect();
        const rowBottom = Math.max(
          ...a.fields.map((s) => field(s).getBoundingClientRect().bottom),
          submitOf(form).getBoundingClientRect().bottom,
        );
        expect(box.top, "the line sits under the row").toBeGreaterThanOrEqual(rowBottom);
        expect(box.width, "the line spans the form").toBeCloseTo(
          form.getBoundingClientRect().width,
          0,
        );
      } finally {
        error.textContent = "";
      }
    });
  });
});

describe("ADDERS", () => {
  const describeNode = (n: Element): string =>
    `${n.tagName.toLowerCase()}#${n.id}.${[...n.classList].join(".")} "${n.textContent.trim().slice(0, 40)}"`;

  it("names every form and every submit button the mounted surfaces show", () => {
    const forms = new Set(ADDERS.map(formOf));
    const submits = new Set([...forms].map(submitOf));
    const strayForms = [...host.querySelectorAll("form")].filter((f) => !forms.has(f));
    const straySubmits = [...host.querySelectorAll("button")].filter(
      (b) => b.type === "submit" && !submits.has(b),
    );
    expect(strayForms.map(describeNode), "a form outside ADDERS").toEqual([]);
    expect(straySubmits.map(describeNode), "a submit button outside ADDERS").toEqual([]);
  });
});

describe("the empty hints", () => {
  it.each(["agent-ignore", "agent-dirs"])("%s puts its empty hint above its add row", (key) => {
    const hint = host.querySelector(`#${key}-empty-hint`);
    const form = field(`#${key}-input`).form;
    expect(hint).not.toBeNull();
    expect(form, "the field sits in a form").not.toBeNull();
    expect(
      hint!.compareDocumentPosition(form!) & Node.DOCUMENT_POSITION_FOLLOWING,
      "the hint precedes the form",
    ).toBeTruthy();
  });
});

describe("the stylesheet", () => {
  // A per-adder layout rule is how the three Permissions adders drifted into three shapes.
  const FORBIDDEN = [
    /\[data-rule-form/,
    /\.rule-form\b/,
    /\.chip-adder\b/,
    /\.chip-input\b/,
    /\.knowledge-add-form\b/,
    /\.knowledge-add-error\b/,
    /\.agent-dirs-error\b/,
    /\.forge-kind-add-btn\b/,
    /\.rf-submit\b/,
    /knowledge-add-(path|name)/,
    /mcp-disabled-(input|add)/,
  ];

  it("keeps no per-adder layout or button rule", () => {
    const hits = manifestSheets().flatMap(({ name, css }) =>
      FORBIDDEN.filter((re) => re.test(css)).map((re) => `${name}: ${re.source}`),
    );
    expect(hits).toEqual([]);
  });

  it("leaves no data-rule-form column plan in the shipped page", () => {
    expect(indexHtml).not.toContain("data-rule-form");
  });
});
