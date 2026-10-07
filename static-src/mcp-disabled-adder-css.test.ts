// The MCP server dialog's Disabled tools adder, measured against the assembled cascade on both pointer tiers: the
// field takes the theme's box and the height of the pill beside it, not the UA text input's chrome.
import { afterAll, afterEach, beforeAll, describe, expect, it } from "vitest";
import indexHtml from "../static/index.html?raw";

import { mountAppCSS } from "./__test-helpers__/css-rules.js";

/** `--btn-h` on each tier, stated so a token retune fails here. */
const TIER_PX = { fine: 36, coarse: 44 } as const;

let style: HTMLStyleElement;
let pointerWas: string | null;
let host: HTMLElement | null = null;

/** Sliced from the shipped page, so the measured field carries the class the dialog really renders. */
function adderMarkup(): string {
  const field = indexHtml.indexOf('id="mcp-disabled-input"');
  expect(field, 'static/index.html has no id="mcp-disabled-input"').toBeGreaterThan(-1);
  const open = indexHtml.lastIndexOf("<div", field);
  const close = indexHtml.indexOf("</div>", field);
  expect(indexHtml.slice(open, field), "the field sits in a .chip-adder").toContain(
    'class="chip-adder"',
  );
  return indexHtml.slice(open, close + "</div>".length);
}

function mountAdder(): { field: HTMLInputElement; pill: HTMLElement } {
  host = document.createElement("div");
  host.innerHTML = adderMarkup();
  document.body.append(host);
  const field = host.querySelector<HTMLInputElement>('[id="mcp-disabled-input"]');
  const pill = host.querySelector<HTMLElement>('[id="mcp-disabled-add"]');
  if (field === null || pill === null) {
    throw new Error("the adder's field or pill did not mount");
  }
  return { field, pill };
}

const h = (el: HTMLElement): number => el.getBoundingClientRect().height;

beforeAll(() => {
  style = mountAppCSS();
  pointerWas = document.documentElement.getAttribute("data-pointer");
});

afterEach(() => {
  host?.remove();
  host = null;
});

afterAll(() => {
  style.remove();
  if (pointerWas === null) {
    document.documentElement.removeAttribute("data-pointer");
  } else {
    document.documentElement.setAttribute("data-pointer", pointerWas);
  }
});

describe.each(["fine", "coarse"] as const)("the Disabled tools field on a %s pointer", (tier) => {
  it("takes --btn-h, the height of the pill beside it", () => {
    document.documentElement.setAttribute("data-pointer", tier);
    const { field, pill } = mountAdder();
    expect(h(field), "the field is one control height").toBeCloseTo(TIER_PX[tier], 0);
    expect(h(field), "and agrees with its pill").toBeCloseTo(h(pill), 0);
  });

  it("holds that height itself rather than borrowing the row's stretch", () => {
    // The adder is a flex row, so the pill's height alone stretches the field; only the field's own floor keeps it once
    // the row wraps or the pill's sizing moves.
    document.documentElement.setAttribute("data-pointer", tier);
    const { field } = mountAdder();
    expect(getComputedStyle(field).minBlockSize).toBe(`${String(TIER_PX[tier])}px`);
  });

  it("paints the theme's 1px border rather than the UA field's", () => {
    document.documentElement.setAttribute("data-pointer", tier);
    const { field } = mountAdder();
    const cs = getComputedStyle(field);
    expect(cs.borderTopStyle).toBe("solid");
    expect(cs.borderTopWidth).toBe("1px");
  });
});
