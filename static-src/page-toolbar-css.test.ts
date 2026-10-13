// The page chrome the Settings, Docs, Git and History pages share: ONE toolbar row (full width,
// actions at the inline end, filled buttons), a page switcher a rung taller than a control with a
// mouse, one width for a short select and a small number field, and a row action that hovers in a
// single paint. Measured against the shipped markup and stylesheet at both pointer tiers.
import { describe, it, expect, beforeAll, afterAll, afterEach } from "vitest";

import indexHtml from "../static/index.html?raw";
import { mountAppCSS } from "./__test-helpers__/css-rules.js";

type Tier = "fine" | "coarse";
const TIERS: readonly Tier[] = ["fine", "coarse"];
const BTN_H: Record<Tier, number> = { fine: 36, coarse: 44 };
const BAR_H: Record<Tier, number> = { fine: 40, coarse: 44 };

let style: HTMLStyleElement;
let shell: HTMLElement;

beforeAll(() => {
  style = mountAppCSS();
  const doc = new DOMParser().parseFromString(indexHtml, "text/html");
  shell = document.createElement("div");
  shell.style.width = "900px";
  for (const id of ["docs-view", "git-view", "settings-view"]) {
    const view = doc.getElementById(id);
    expect(view, id).not.toBeNull();
    const copy = document.importNode(view!, true) as HTMLElement;
    for (const n of [copy, ...copy.querySelectorAll("[data-git-panel], [data-settings-panel]")]) {
      n.classList.remove("hidden");
    }
    shell.appendChild(copy);
  }
  document.body.appendChild(shell);
});

afterAll(() => {
  style.remove();
  shell.remove();
});

afterEach(() => {
  delete document.documentElement.dataset["pointer"];
});

function el<T extends HTMLElement>(sel: string): T {
  const e = shell.querySelector<T>(sel);
  expect(e, sel).not.toBeNull();
  return e!;
}

function rect(e: Element): DOMRect {
  return e.getBoundingClientRect();
}

/** The docs slot is empty on a tab that cannot create anything; a button stands in for the creatable ones. */
function withDocsButton(): HTMLButtonElement {
  const slot = el("#docs-new-slot");
  const btn = document.createElement("button");
  btn.type = "button";
  btn.className = "action-pill docs-new-btn";
  btn.innerHTML = '<svg class="ic-ui" viewBox="0 0 24 24"></svg>';
  slot.append(btn);
  return btn;
}

const TOOLBARS: readonly { name: string; toolbar: string; bar: string | null }[] = [
  { name: "docs", toolbar: "#docs-new-slot", bar: "#docs-tab-bar" },
  { name: "git changes", toolbar: '[data-git-panel="changes"] .page-toolbar', bar: "#git-tab-bar" },
  {
    name: "git pull requests",
    toolbar: '[data-git-panel="prs"] .page-toolbar',
    bar: "#git-tab-bar",
  },
  {
    name: "git sources",
    toolbar: '[data-git-panel="sources"] .page-toolbar',
    bar: "#git-tab-bar",
  },
  { name: "settings tools", toolbar: '[data-settings-panel="tools"] .page-toolbar', bar: null },
];

describe("the page toolbar markup", () => {
  it("is the only toolbar vocabulary: no page keeps its own row class", () => {
    expect(
      shell.querySelector(".git-tab-toolbar, .docs-head-row, .section-actions-row"),
    ).toBeNull();
  });

  it.each(TOOLBARS)("$name holds filled pills, never bare icon buttons", ({ toolbar }) => {
    const row = el(toolbar);
    withDocsButton();
    const buttons = [...row.querySelectorAll("button")];
    expect(buttons.length).toBeGreaterThan(0);
    for (const b of buttons) {
      expect(b.classList.contains("action-pill"), b.id || b.className).toBe(true);
      expect(b.classList.contains("icon-btn"), b.id || b.className).toBe(false);
    }
    el("#docs-new-slot").replaceChildren();
  });

  it("Docs' slot sits under the switcher, not beside it", () => {
    const slot = el("#docs-new-slot");
    const header = el("#docs-view .page-header");
    expect(header.contains(slot)).toBe(false);
    expect(header.nextElementSibling).toBe(slot);
  });
});

describe.each(TIERS)("the page toolbar at the %s tier", (tier) => {
  it.each(TOOLBARS)("$name: buttons end at the row's inline end, one height, filled", (t) => {
    document.documentElement.dataset["pointer"] = tier;
    const added = t.toolbar === "#docs-new-slot" ? withDocsButton() : null;
    const row = el(t.toolbar);
    const buttons = [...row.querySelectorAll<HTMLElement>("button")].filter(
      (b) => rect(b).width > 0,
    );
    expect(buttons.length).toBeGreaterThan(0);
    const last = buttons.at(-1)!;
    expect(rect(last).right).toBeCloseTo(rect(row).right, 0);
    for (const b of buttons) {
      expect(rect(b).height, b.id || b.className).toBe(BTN_H[tier]);
      expect(getComputedStyle(b).backgroundColor, b.id || b.className).not.toBe("rgba(0, 0, 0, 0)");
    }
    expect(getComputedStyle(row).display).toBe("flex");
    added?.remove();
  });

  it.each(TOOLBARS.filter((t) => t.bar !== null))(
    "$name: the toolbar and the switcher are the same width",
    (t) => {
      document.documentElement.dataset["pointer"] = tier;
      const added = t.toolbar === "#docs-new-slot" ? withDocsButton() : null;
      expect(rect(el(t.toolbar)).width).toBeCloseTo(rect(el(t.bar!)).width, 0);
      added?.remove();
    },
  );

  it("the page switcher is a rung over a control with a mouse and a control with a finger", () => {
    document.documentElement.dataset["pointer"] = tier;
    for (const bar of ["#docs-tab-bar", "#git-tab-bar", "#settings-tab-bar"]) {
      expect(rect(el(bar)).height, bar).toBe(BAR_H[tier]);
    }
  });
});

describe.each(TIERS)("the empty Docs slot at the %s tier", (tier) => {
  it("keeps a filled slot's block size, so switching to a tab that cannot create moves nothing", () => {
    document.documentElement.dataset["pointer"] = tier;
    const slot = el("#docs-new-slot");
    expect(slot.childElementCount).toBe(0);
    const emptyHeight = rect(slot).height;
    const next = slot.nextElementSibling;
    const emptyTop = next ? rect(next).top : 0;
    const added = withDocsButton();
    expect(emptyHeight).toBeGreaterThan(0);
    expect(rect(slot).height).toBe(emptyHeight);
    expect(next ? rect(next).top : 0).toBe(emptyTop);
    added.remove();
  });
});

describe("a bar that is not a page's switcher keeps the control height", () => {
  it("the MCP modal's mode switcher is --btn-h, not --bar-h", () => {
    document.documentElement.dataset["pointer"] = "fine";
    const bar = document.createElement("nav");
    bar.className = "seg-bar";
    bar.innerHTML = '<button class="seg">One</button><button class="seg">Two</button>';
    shell.appendChild(bar);
    expect(rect(bar).height).toBe(BTN_H.fine);
    bar.remove();
  });
});

describe("settings fields", () => {
  it.each(TIERS)("a short select is as wide as the small number field (%s)", (tier) => {
    document.documentElement.dataset["pointer"] = tier;
    const host = document.createElement("div");
    host.innerHTML = `
      <select class="rf-select"><option>Default</option><option>On</option></select>
      <input type="number" class="rf-input rf-number" value="1">`;
    shell.appendChild(host);
    const select = host.querySelector<HTMLElement>("select")!;
    const number = host.querySelector<HTMLElement>("input")!;
    expect(rect(select).width).toBe(rect(number).width);
    expect(rect(select).height).toBe(rect(number).height);
    host.remove();
  });
});

/**
 * Every `:hover` rule writing `props` for `node`, with `:hover` counted as matching. A CSSOM walk, because a synthetic
 * hover drives no recalc.
 */
function hoverWriters(node: Element, props: string[]): { selector: string; value: string }[] {
  const out: { selector: string; value: string }[] = [];
  const walk = (list: CSSRuleList): void => {
    for (const r of list) {
      const group = r as CSSRule & { cssRules?: CSSRuleList };
      if (!(r instanceof CSSStyleRule)) {
        if (group.cssRules !== undefined) {
          walk(group.cssRules);
        }
        continue;
      }
      const value = props.map((p) => r.style.getPropertyValue(p)).find((v) => v !== "") ?? "";
      if (value === "" || !r.selectorText.includes(":hover")) {
        continue;
      }
      try {
        if (node.matches(r.selectorText.replaceAll(":hover", ""))) {
          out.push({ selector: r.selectorText, value });
        }
      } catch {
        /* a selector this engine cannot parse writes nothing here */
      }
    }
  };
  for (const sheet of document.styleSheets) {
    walk(sheet.cssRules);
  }
  return out;
}

describe("a row's hovered action paints once", () => {
  function rowWith(...actions: string[]): HTMLElement {
    const row = document.createElement("div");
    row.className = "entry";
    row.innerHTML = `<span class="entry-actions">${actions.join("")}</span>`;
    shell.appendChild(row);
    return row;
  }

  it.each(["icon-btn", "btn-small", "action-pill"])(
    "a %s takes one raised colour and no wash image",
    (cls) => {
      const row = rowWith(`<button class="${cls}">x</button>`);
      const btn = row.querySelector("button")!;
      // The image lands in the first frame while the fill fades in: two states in succession.
      expect(hoverWriters(btn, ["background-image"])).toEqual([]);
      expect(hoverWriters(btn, ["background"]).map((w) => w.value)).toContain(
        "var(--c-hover-raised)",
      );
      row.remove();
    },
  );

  it("a member with no fill of its own still takes the wash image", () => {
    const row = rowWith('<label class="toggle toggle-inline"><input type="checkbox"></label>');
    const toggle = row.querySelector("label")!;
    expect(hoverWriters(toggle, ["background-image"]).map((w) => w.value)).toEqual([
      "var(--layer-hover)",
    ]);
    row.remove();
  });

  it("the raised colour is the wash flattened over the rung it hovers to", () => {
    const probe = document.createElement("div");
    shell.appendChild(probe);
    const resolved = (css: string): string => {
      probe.style.background = css;
      return getComputedStyle(probe).backgroundColor;
    };
    const canvas = document.createElement("canvas");
    canvas.width = canvas.height = 1;
    const ctx = canvas.getContext("2d", { willReadFrequently: true })!;
    const pixel = (...layers: string[]): number[] => {
      ctx.clearRect(0, 0, 1, 1);
      for (const c of layers) {
        ctx.fillStyle = c;
        ctx.fillRect(0, 0, 1, 1);
      }
      return [...ctx.getImageData(0, 0, 1, 1).data];
    };
    const rung = resolved("var(--c-bg-tertiary)");
    const wash = resolved("var(--c-hover)");
    const flattened = pixel(rung, wash);
    const raised = pixel(resolved("var(--c-hover-raised)"));
    probe.remove();
    expect(flattened).not.toEqual(pixel(rung));
    raised.forEach((channel, i) =>
      expect(Math.abs(channel - flattened[i]!)).toBeLessThanOrEqual(2),
    );
  });
});

describe("the Permissions rule forms", () => {
  const permissions = (() => {
    const doc = new DOMParser().parseFromString(indexHtml, "text/html");
    return [...doc.querySelectorAll<HTMLElement>(".rule-form")];
  })();

  it.each(TIERS)("a select fits its field's column and leaves the column gap (%s)", (tier) => {
    document.documentElement.dataset["pointer"] = tier;
    const host = document.createElement("div");
    host.style.width = "1000px";
    for (const form of permissions) {
      host.appendChild(document.importNode(form, true));
    }
    shell.appendChild(host);
    expect(host.querySelectorAll(".rf-select").length).toBeGreaterThan(0);
    for (const select of host.querySelectorAll<HTMLElement>(".rf-select")) {
      const field = select.closest(".rf-field")!;
      expect(rect(select).right, select.id).toBeLessThanOrEqual(rect(field).right + 0.5);
    }
    host.remove();
  });
});
