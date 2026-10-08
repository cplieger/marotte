// The composer's two rows are one band height on every pointer tier. Source half: the height is one derived value and
// no width query overrides it (a measurement cannot see the axis it was keyed on). Measurement half: the rows match
// at four (width, tier) combinations, wide-coarse being the one a width query cannot see.

import { describe, it, expect, beforeAll, afterAll, afterEach } from "vitest";
import { page } from "vitest/browser";
import indexHtml from "../static/index.html?raw";

import { buildAttachmentPill } from "./attachment-pill.js";
import { ICON_SEND } from "./icons.js";
import { iconEl } from "./icon-el.js";
import { allRules, loadCSS, mountAppCSS, ruleContaining } from "./__test-helpers__/css-rules.js";

/** Sliced from the shipped page: the pill row's height is the sum of its real controls. */
function composerMarkup(): string {
  const open = indexHtml.indexOf('<form id="prompt-form"');
  expect(open, 'static/index.html has no <form id="prompt-form">').toBeGreaterThan(-1);
  const close = indexHtml.indexOf("</form>", open);
  expect(close, "the prompt form is not closed").toBeGreaterThan(open);
  return indexHtml.slice(open, close + "</form>".length);
}

/** Through the real builder: `#attachment-row` ships empty, and a hand-written chip would not carry the composer's `×`. */
function stageAttachments(): void {
  const row = document.getElementById("attachment-row");
  if (row === null) {
    throw new Error("the composer subtree did not mount");
  }
  row.classList.remove("hidden");
  for (const name of ["screenshot.png", "15-input.css"]) {
    row.appendChild(
      buildAttachmentPill(
        { path: `/workspace/${name}`, name },
        {
          onRemove: () => {
            /* the row only has to render */
          },
        },
      ),
    );
  }
}

/** Paren-balanced, because `--composer-ctl-h` is a `max()` over a token and `[^)]+` stops at the first inner `)`. */
function declaration(body: string, property: string): string | null {
  const at = body.indexOf(`${property}:`);
  if (at === -1) {
    return null;
  }
  let depth = 0;
  for (let i = at + property.length + 1; i < body.length; i += 1) {
    const ch = body[i];
    if (ch === "(") {
      depth += 1;
    } else if (ch === ")") {
      depth -= 1;
    } else if (ch === ";" && depth === 0) {
      return body.slice(at + property.length + 1, i).trim();
    }
  }
  return null;
}

describe("the height's declarations, read from source", () => {
  const css = loadCSS("15-input.css");

  it("declares one control height for the box, and it takes ONE rung on touch", () => {
    const form = ruleContaining(css, '[id="prompt-form"]', "top");
    // The painted box takes one rung up on coarse (`--ctl-h-sm`: 32 mouse, 36 finger) and stops short of the target floor.
    expect(form.body).toMatch(/--composer-ctl-h:\s*max\(2rem,\s*var\(--ctl-h-sm\)\)/);
    // The floor must not be read here, or the painted box takes the 44px target's measure ("grown buttons look empty").
    expect(form.body).not.toMatch(/--composer-ctl-h:[^;]*--hit-floor/);
    // One declaration, so no second selector re-decides the tier.
    const writers = allRules(css)
      .filter((r) => /--composer-ctl-h:/.test(r.body))
      .map((r) => r.selector);
    expect(writers).toEqual(['[id="prompt-form"]']);
  });

  it("derives the textarea's resting band from that height and the row's own inset", () => {
    const input = ruleContaining(css, '[id="prompt-input"]', "top");
    // The row's padding pays for the expander's block reach (`--composer-pill-pad`, 6px coarse vs `--pill-inset`'s 4).
    expect(input.body).toMatch(
      /--composer-rest-h:\s*calc\(var\(--composer-ctl-h\)\s*\+\s*2\s*\*\s*var\(--composer-pill-pad\)\)/,
    );
  });

  it("gives the two controls that height instead of a literal", () => {
    for (const selector of [".pill", ".send-btn"]) {
      const rule = ruleContaining(css, selector, "top");
      // Cut at the first nested block: both rules carry a nested `& svg` glyph sizing.
      const brace = rule.body.indexOf("{");
      const own = brace === -1 ? rule.body : rule.body.slice(0, brace);
      // Either spelling (`height` on `.pill`, `block-size` on `.send-btn`). Only the block axis is shared.
      expect(own, `${selector} reads the shared height`).toMatch(
        /(?:block-size|height):\s*var\(--composer-ctl-h\)/,
      );
      // A literal here is what let the row disagree with the textarea above it.
      expect(own, `${selector} declares no literal height`).not.toMatch(/(^|[^-])height:\s*\d/);
    }
  });

  it("takes Send's width from ONE mechanism, so it cannot drift from the height", () => {
    // Send's width comes from the same padding `.pill` takes, declared once per tier, so there is no second literal to
    // drift (the original 42x32 vs 44x44 mismatch).
    const rule = ruleContaining(css, ".send-btn", "top");
    const brace = rule.body.indexOf("{");
    const own = brace === -1 ? rule.body : rule.body.slice(0, brace);
    expect(own).toMatch(/block-size:\s*var\(--composer-ctl-h\)/);
    expect(own, "no literal inline size").not.toMatch(/inline-size:\s*\d/);
    expect(own, "no aspect-ratio re-squaring it").not.toMatch(/aspect-ratio/);
    // Both icon-only controls derive inline padding from one token; Send adds half its glyph deficit on top.
    expect(own, "Send derives its padding from the row's token").toMatch(
      /padding-inline:\s*max\(\s*var\(--pill-pad-inline\)/,
    );
    const pill = ruleContaining(css, ".pill", "top");
    expect(pill.body, ".pill spends the same token").toMatch(
      /padding:\s*0\s+var\(--pill-pad-inline\)/,
    );
    // No rule may re-pad either with a literal: a phone override on `.pill` alone made Send 32 wide beside a 46px sibling.
    const rePadders = allRules(css)
      .filter((r) =>
        r.selector
          .split(",")
          .map((sel) => sel.trim())
          .some((sel) => sel === ".pill" || sel === ".send-btn"),
      )
      .filter((r) => /padding(-inline)?:[^;]*var\(--sp-/.test(r.body))
      .map((r) => r.selector);
    expect(rePadders, "no rule re-pads either control off a spacing token").toEqual([]);
    expect(own, "physical, or the floor's own min-width wins by source order").toMatch(
      /min-width:\s*0/,
    );
    const widthWriters = allRules(css)
      .filter((r) => /\.send-btn/.test(r.selector) && /min-width:\s*var\(--btn-h\)/.test(r.body))
      .map((r) => r.selector);
    expect(widthWriters).toEqual([]);
  });

  it("lets no rule override the resting band, least of all a width query", () => {
    // The tier is the pointer, not the width (01-tokens.css): a width-keyed override missed wide coarse viewports.
    const writers = allRules(css)
      .filter((r) => /--composer-rest-h:/.test(r.body))
      .map((r) => r.selector);
    expect(writers).toEqual(['[id="prompt-input"]']);
  });
});

describe("the composer, measured at real viewport sizes", () => {
  // Sits last and restores the entry size, read off the frame (`page.viewport` has no getter).
  let entry: { readonly width: number; readonly height: number } | null = null;
  let styleEl: HTMLStyleElement | null = null;

  beforeAll(() => {
    entry = { width: window.innerWidth, height: window.innerHeight };
    styleEl = mountAppCSS();
  });

  afterEach(() => {
    document.body.innerHTML = "";
    document.documentElement.removeAttribute("data-pointer");
  });

  afterAll(async () => {
    styleEl?.remove();
    if (entry !== null) {
      await page.viewport(entry.width, entry.height);
    }
  });

  /**
   * `band` is the pill row minus its `border-block-start`, the boundary between the rows. The resize is asserted, or a
   * stuck viewport makes every case vacuous.
   */
  async function bandsAt(
    width: number,
    height: number,
    pointer: "coarse" | "fine" | null,
  ): Promise<{ textarea: number; band: number; row: number; control: number }> {
    await page.viewport(width, height);
    expect([window.innerWidth, window.innerHeight], "viewport actually resized").toEqual([
      width,
      height,
    ]);
    if (pointer === null) {
      document.documentElement.removeAttribute("data-pointer");
    } else {
      document.documentElement.setAttribute("data-pointer", pointer);
    }
    document.body.innerHTML = composerMarkup();
    const input = document.getElementById("prompt-input");
    const pills = document.querySelector<HTMLElement>(".prompt-pills");
    const pill = document.getElementById("chat-options-btn");
    if (input === null || pills === null || pill === null) {
      throw new Error("the composer subtree did not mount");
    }
    const row = pills.getBoundingClientRect().height;
    const divider = Number.parseFloat(getComputedStyle(pills).borderTopWidth);
    return {
      textarea: input.getBoundingClientRect().height,
      band: row - divider,
      row,
      control: pill.getBoundingClientRect().height,
    };
  }

  it("matches the two rows on a touch DESKTOP, which is the case a width query cannot reach", async () => {
    // Wide coarse: a width query cannot see the tier, so the textarea could sit over a taller pill row.
    const { textarea, band, control } = await bandsAt(1440, 900, "coarse");
    expect(control, "one rung up on a coarse pointer, still short of the 44px floor").toBe(36);
    expect(textarea).toBe(44);
    expect(band).toBe(44);
  });

  it("matches them on a mouse desktop, at the size that did not change", async () => {
    const { textarea, band, control } = await bandsAt(1440, 900, "fine");
    expect(control).toBe(32);
    expect(textarea).toBe(40);
    expect(band).toBe(40);
  });

  it("matches them on a phone", async () => {
    const { textarea, band } = await bandsAt(390, 844, "coarse");
    expect(textarea).toBe(44);
    expect(band).toBe(44);
  });

  it("holds every control's TARGET at the hit floor, and lets none reach the textarea", async () => {
    // A hit test, not a style read: the 44px target and the no-overhang rule are properties of what `elementFromPoint`
    // answers, and a source assertion that looked right is how the earlier defect survived. The row is at its fullest:
    // mid-turn, with the second Send shown beside Cancel.
    await bandsAt(390, 844, "coarse");
    withMidTurnSend();
    const row = document.querySelector<HTMLElement>(".prompt-pills");
    const input = document.getElementById("prompt-input");
    if (row === null || input === null) {
      throw new Error("the composer subtree did not mount");
    }
    const root = getComputedStyle(document.documentElement);
    // Authored in `rem`, so resolved against the root size rather than restated.
    const floor =
      Number.parseFloat(root.getPropertyValue("--hit-floor")) * Number.parseFloat(root.fontSize);
    expect(floor, "the coarse floor, in px").toBe(44);

    const controls = [...row.querySelectorAll<HTMLElement>(".pill:not(.hidden), .send-btn")];
    expect(controls.length, "the row renders its controls").toBeGreaterThan(3);

    const owns = (el: Element, x: number, y: number): boolean => {
      const hit = document.elementFromPoint(x, y);
      return hit === el || (hit !== null && el.contains(hit));
    };
    const inputBottom = input.getBoundingClientRect().bottom;
    const short: string[] = [];
    const theft: string[] = [];
    for (const el of controls) {
      const r = el.getBoundingClientRect();
      const cx = r.left + r.width / 2;
      const cy = r.top + r.height / 2;
      let up = 0;
      let down = 0;
      for (let t = 0.25; t < 60; t += 0.25) {
        if (!owns(el, cx, cy - t)) {
          break;
        }
        up = t;
      }
      for (let t = 0.25; t < 60; t += 0.25) {
        if (!owns(el, cx, cy + t)) {
          break;
        }
        down = t;
      }
      const name = el.id === "" ? el.className : el.id;
      // The scan resolves the boundary to a quarter pixel.
      if (up + down + 0.25 < floor - 0.5) {
        short.push(`${name} target ${String(up + down)}px tall`);
      }
      // The row's own padding pays for the expander's upward reach.
      if (cy - up < inputBottom) {
        theft.push(`${name} reaches ${String(inputBottom - (cy - up))}px into the textarea`);
      }
    }
    expect(short, "every control clears the coarse floor by hit test").toEqual([]);
    expect(theft, "no target overhangs into #prompt-input's box").toEqual([]);
  });

  /** `static/index.html` ships Send empty (`prompt-input.ts` fills it at boot), so the production glyph is driven here. */
  function withSendGlyph(): HTMLElement {
    const send = document.getElementById("send-btn");
    if (send === null) {
      throw new Error("the composer subtree did not mount");
    }
    send.replaceChildren(iconEl(ICON_SEND));
    return send;
  }

  function withMidTurnSend(): { midTurn: HTMLElement; cancel: HTMLElement } {
    const cancel = withSendGlyph();
    const midTurn = document.getElementById("midturn-send-btn");
    if (midTurn === null) {
      throw new Error("the composer subtree did not mount");
    }
    midTurn.replaceChildren(iconEl(ICON_SEND));
    midTurn.hidden = false;
    return { midTurn, cancel };
  }

  it("fits the mid-turn Send beside Cancel at phone widths, with Cancel keeping its corner", async () => {
    // The row does not wrap, so a control that does not fit would overflow the box. The worst case carries the task-list
    // pill and a model name long enough to need its ellipsis.
    for (const [w, h, tier] of [
      [320, 568, "coarse"],
      [360, 740, "coarse"],
      [390, 844, "coarse"],
      [1440, 900, "fine"],
    ] as const) {
      await bandsAt(w, h, tier);
      document.getElementById("task-list-pill")?.classList.remove("hidden");
      const model = document.getElementById("ctx-model-pill");
      if (model !== null) {
        model.textContent = "claude-opus-4.5-thinking";
      }
      const row = document.querySelector<HTMLElement>(".prompt-pills");
      if (row === null) {
        throw new Error("the composer subtree did not mount");
      }
      const at = `${String(w)}x${String(h)} ${tier}`;
      const before = row.getBoundingClientRect().height;
      const { midTurn, cancel } = withMidTurnSend();
      const m = midTurn.getBoundingClientRect();
      const c = cancel.getBoundingClientRect();
      const r = row.getBoundingClientRect();
      expect(row.scrollWidth, `${at}: nothing overflows the row`).toBeLessThanOrEqual(
        row.clientWidth,
      );
      expect(r.height, `${at}: the row keeps its height`).toBe(before);
      expect(m.right, `${at}: the second Send sits left of Cancel`).toBeLessThanOrEqual(c.left);
      expect(c.right, `${at}: Cancel keeps the row's end`).toBeCloseTo(
        r.right - Number.parseFloat(getComputedStyle(row).paddingRight),
        0,
      );
      expect(m.height, `${at}: one control height`).toBe(c.height);
      expect(m.width, `${at}: the same box as Cancel`).toBeCloseTo(c.width, 1);
    }
  });

  it("keeps Send wider than tall on every tier and width", async () => {
    // Send is not square: a square read as a different size class. Only the height is compared to the sibling (box-identical
    // cost 6px of a row with 4px of slack); the surplus width is free touch area, so the direction is asserted.
    for (const [w, h, tier] of [
      [320, 568, "coarse"],
      [390, 844, "coarse"],
      [430, 932, "coarse"],
      [844, 390, "coarse"],
      [1440, 900, "fine"],
      [390, 844, "fine"],
    ] as const) {
      await bandsAt(w, h, tier);
      const send = withSendGlyph();
      const sibling = document.getElementById("chat-options-btn");
      if (sibling === null) {
        throw new Error("the composer subtree did not mount");
      }
      const s = send.getBoundingClientRect();
      const o = sibling.getBoundingClientRect();
      const at = `${String(w)}x${String(h)} ${tier}`;
      expect(s.height, `${at}: Send's height is the row's`).toBe(o.height);
      expect(s.width, `${at}: wider than tall, which is the touch area`).toBeGreaterThan(s.height);
    }
  });

  it("sizes Send's glyph box with the same token as the control beside it", async () => {
    // Send swaps faces, so no per-path ink correction can live on it: every face takes `--icon-ui`.
    await bandsAt(390, 844, "coarse");
    withSendGlyph();
    const glyph = (id: string): DOMRect => {
      const svg = document.querySelector(`#${id} svg`);
      if (!(svg instanceof SVGGraphicsElement)) {
        throw new Error(`${id} has no glyph`);
      }
      return svg.getBoundingClientRect();
    };
    const send = glyph("send-btn");
    const sibling = glyph("chat-options-btn");
    expect(send.width).toBeCloseTo(sibling.width, 1);
    expect(send.height).toBeCloseTo(sibling.height, 1);
  });

  it("matches them on a wide viewport with no pointer tier resolved yet", async () => {
    // The no-JS fallback is width-gated, so a wide window keeps mouse sizing until `pointer-tier.ts` classifies it.
    const { textarea, band } = await bandsAt(1440, 900, null);
    expect(textarea).toBe(40);
    expect(band).toBe(40);
  });

  it("centres the composer on its own divider, which is what the match buys", async () => {
    // With equal bands, box top to text centre equals controls' centre to box bottom; counting the divider into the
    // textarea's band puts this half a pixel out.
    const boxOf = (): DOMRect => {
      const box = document.getElementById("prompt-box");
      if (box === null) {
        throw new Error("no prompt box");
      }
      return box.getBoundingClientRect();
    };
    for (const tier of ["fine", "coarse"] as const) {
      await bandsAt(1440, 900, tier);
      const box = boxOf();
      const input = document.getElementById("prompt-input");
      const pill = document.getElementById("chat-options-btn");
      if (input === null || pill === null) {
        throw new Error("the composer subtree did not mount");
      }
      const cs = getComputedStyle(input);
      const textCentre =
        input.getBoundingClientRect().y +
        Number.parseFloat(cs.paddingTop) +
        Number.parseFloat(cs.lineHeight) / 2;
      const pillRect = pill.getBoundingClientRect();
      expect(textCentre - box.y, `${tier}: text centre from the top edge`).toBeCloseTo(
        box.y + box.height - (pillRect.y + pillRect.height / 2),
        1,
      );
    }
  });
});

// The staged-attachment chip is the box's third row. Its height came from its `<button>` children, so the hit floor
// put the 44px target into the painted box (50.78px around a 15px label); the same two halves apply.

describe("the attachment chip's declarations, read from source", () => {
  const css = loadCSS("15-input.css");

  it("gives the chip the box's control height, with the second home's fallback", () => {
    const rule = ruleContaining(css, ".attachment-pill", "top");
    const brace = rule.body.indexOf("{");
    const own = brace === -1 ? rule.body : rule.body.slice(0, brace);
    expect(declaration(own, "block-size")).toMatch(/^var\(--composer-ctl-h,/);
    expect(declaration(own, "padding-block")).toBe("0");
    expect(own, "no literal height beside the derived one").not.toMatch(/(^|[^-])height:\s*\d/);
  });

  it("keeps that fallback the same EXPRESSION the box declares, not the same number", () => {
    // Expression, not value: the chip's other home is a sent turn's header, outside `#prompt-form`, where
    // `--composer-ctl-h` does not resolve; a `var()` with nothing behind it invalidates the declaration.
    const form = ruleContaining(css, '[id="prompt-form"]', "top");
    const declared = declaration(form.body, "--composer-ctl-h");
    const chip = ruleContaining(css, ".attachment-pill", "top");
    const size = declaration(chip.body, "block-size") ?? "";
    const fallback = /^var\(--composer-ctl-h,\s*(.*)\)$/s.exec(size)?.[1]?.trim();
    expect(declared, "the box declares a control height").not.toBeNull();
    expect(fallback).toBe(declared);
  });

  it("pays for the chips' target reach out of the row's own padding", () => {
    // Without the row's padding term a chip's expander overhangs `#prompt-input` above and the pill targets below.
    const rule = ruleContaining(css, ".attachment-row", "top");
    expect(declaration(rule.body, "padding")).toBe("var(--composer-pill-pad)");
    const writers = allRules(css)
      .filter((r) => /\.attachment-row/.test(r.selector) && /padding:/.test(r.body))
      .map((r) => r.selector);
    expect(writers, "one padding writer, on no width axis").toEqual([".attachment-row"]);
  });

  it("opts both chip buttons out of the floor PHYSICALLY and grows their target instead", () => {
    for (const selector of [".attachment-open", ".attachment-close"]) {
      const rule = ruleContaining(css, selector, "top");
      const brace = rule.body.indexOf("{");
      const own = brace === -1 ? rule.body : rule.body.slice(0, brace);
      // Physical, or the floor's `min-height` wins by source order (61-mcp-tools sorts later; a logical property is a
      // different property to it).
      expect(declaration(own, "min-height"), `${selector} drops the floor's min-height`).toBe("0");
      expect(declaration(own, "min-width"), `${selector} drops the floor's min-width`).toBe("0");
      expect(declaration(own, "position"), `${selector} is the expander's containing block`).toBe(
        "relative",
      );
    }
    const expander = ruleContaining(css, ".attachment-open::after", "top");
    expect(expander.selector).toContain(".attachment-close::after");
    expect(declaration(expander.body, "inset-block")).toBe(
      "min(0px, calc((100% - var(--hit-floor)) / 2))",
    );
    // Block only: an inline expander on the `×` would put a destructive target over the filename's last characters.
    expect(
      declaration(expander.body, "inset-inline"),
      "no inline reach into the sibling control",
    ).toBe("0");
  });
});

describe("the attachment chip, measured at real viewport sizes", () => {
  let entry: { readonly width: number; readonly height: number } | null = null;
  let styleEl: HTMLStyleElement | null = null;

  beforeAll(() => {
    entry = { width: window.innerWidth, height: window.innerHeight };
    styleEl = mountAppCSS();
  });

  afterEach(() => {
    document.body.innerHTML = "";
    document.documentElement.removeAttribute("data-pointer");
  });

  afterAll(async () => {
    styleEl?.remove();
    if (entry !== null) {
      await page.viewport(entry.width, entry.height);
    }
  });

  async function chipsAt(
    width: number,
    height: number,
    pointer: "coarse" | "fine",
  ): Promise<{ chip: number; control: number; floor: number }> {
    await page.viewport(width, height);
    expect([window.innerWidth, window.innerHeight], "viewport actually resized").toEqual([
      width,
      height,
    ]);
    document.documentElement.setAttribute("data-pointer", pointer);
    document.body.innerHTML = composerMarkup();
    stageAttachments();
    const chip = document.querySelector<HTMLElement>("#attachment-row .attachment-pill");
    const control = document.getElementById("chat-options-btn");
    if (chip === null || control === null) {
      throw new Error("the composer subtree did not mount");
    }
    const root = getComputedStyle(document.documentElement);
    return {
      chip: chip.getBoundingClientRect().height,
      control: control.getBoundingClientRect().height,
      floor:
        Number.parseFloat(root.getPropertyValue("--hit-floor")) * Number.parseFloat(root.fontSize),
    };
  }

  it("matches the chip to the pill row's controls on a phone, which is the reported defect", async () => {
    const { chip, control, floor } = await chipsAt(390, 844, "coarse");
    expect(floor, "the coarse floor, in px").toBe(44);
    expect(chip, "a 50.78px chip before the fix").toBe(36);
    expect(chip).toBe(control);
  });

  it("matches it on a touch DESKTOP, the tier a width query cannot see", async () => {
    const { chip, control } = await chipsAt(1440, 900, "coarse");
    expect(chip).toBe(36);
    expect(chip).toBe(control);
  });

  it("matches it on a mouse, where the chip was 30.78 against a 32px row", async () => {
    const { chip, control } = await chipsAt(1440, 900, "fine");
    expect(chip).toBe(32);
    expect(chip).toBe(control);
  });

  it("holds every chip button's TARGET at the floor, and lets none reach a neighbour", async () => {
    const { floor } = await chipsAt(390, 844, "coarse");
    const input = document.getElementById("prompt-input");
    const control = document.getElementById("chat-options-btn");
    if (input === null || control === null) {
      throw new Error("the composer subtree did not mount");
    }
    const owns = (el: Element, x: number, y: number): boolean => {
      const hit = document.elementFromPoint(x, y);
      return hit === el || (hit !== null && el.contains(hit));
    };
    const reach = (el: Element, dir: -1 | 1): number => {
      const r = el.getBoundingClientRect();
      const cx = r.left + r.width / 2;
      const cy = r.top + r.height / 2;
      let last = 0;
      for (let t = 0.25; t < 80; t += 0.25) {
        if (!owns(el, cx, cy + dir * t)) {
          break;
        }
        last = t;
      }
      return last;
    };

    const buttons = [
      ...document.querySelectorAll<HTMLElement>(
        "#attachment-row .attachment-open, #attachment-row .attachment-close",
      ),
    ];
    expect(buttons.length, "two chips, each with a label and a remove").toBe(4);

    const inputBottom = input.getBoundingClientRect().bottom;
    // The pill row's controls grow upward too, so the boundary is that target's top, not the control's box.
    const controlRect = control.getBoundingClientRect();
    const controlTargetTop = controlRect.top + controlRect.height / 2 - reach(control, -1);
    const short: string[] = [];
    const theft: string[] = [];
    for (const el of buttons) {
      const r = el.getBoundingClientRect();
      const cy = r.top + r.height / 2;
      const up = reach(el, -1);
      const down = reach(el, 1);
      const name = el.className;
      if (up + down + 0.25 < floor - 0.5) {
        short.push(`${name} target ${String(up + down)}px tall`);
      }
      if (cy - up < inputBottom - 1) {
        theft.push(`${name} reaches ${String(inputBottom - (cy - up))}px into the textarea`);
      }
      if (cy + down > controlTargetTop + 1) {
        theft.push(`${name} reaches into the pill row's own target`);
      }
    }
    expect(short, "every chip button clears the coarse floor by hit test").toEqual([]);
    expect(theft, "no chip target overhangs a neighbour").toEqual([]);
  });

  it("paints nothing that could reveal the remove button's asymmetric target", async () => {
    // The `×` target is floor-tall and 24px wide, acceptable only while nothing paints the difference. The background half
    // walks the CSSOM, since computed style answers for one state and a test page cannot force pseudo-states.
    await chipsAt(390, 844, "coarse");
    const close = document.querySelector<HTMLElement>("#attachment-row .attachment-close");
    const label = document.querySelector<HTMLElement>("#attachment-row .attachment-open");
    if (close === null || label === null) {
      throw new Error("the chip did not mount");
    }

    /**
     * Every `background*` writer whose selector matches once pseudo-classes are stripped, in document order. Descends into
     * nested rules: a `CSSStyleRule` carries `cssRules` but is not a `CSSGroupingRule` (Chromium 152).
     */
    const fillWriters = (el: Element, pseudo: string): { selector: string; value: string }[] => {
      const out: { selector: string; value: string }[] = [];
      const resolve = (selector: string, parents: readonly string[]): string[] =>
        parents.length === 0
          ? [selector]
          : parents.map((p) => selector.replaceAll("&", `:is(${p})`));

      const walk = (list: CSSRuleList, parents: readonly string[]): void => {
        for (const rule of list) {
          if (rule instanceof CSSGroupingRule && !(rule instanceof CSSStyleRule)) {
            walk(rule.cssRules, parents);
            continue;
          }
          if (!(rule instanceof CSSStyleRule)) {
            continue;
          }
          const selectors = rule.selectorText
            .split(",")
            .flatMap((part) => resolve(part.trim(), parents));
          const value =
            rule.style.getPropertyValue("background") ||
            rule.style.getPropertyValue("background-color");
          if (value !== "") {
            for (const raw of selectors) {
              // Keep only selectors aimed at the pseudo-element under test, then strip the state so a `:hover` rule counts.
              const wantsPseudo = raw.includes("::after") || raw.includes("::before");
              if (pseudo === "" ? wantsPseudo : !raw.includes(pseudo)) {
                continue;
              }
              const bare = raw
                .replaceAll("::after", "")
                .replaceAll("::before", "")
                .replace(/:(?:hover|active|focus|focus-visible|focus-within)\b/g, "")
                .trim();
              if (bare === "") {
                continue;
              }
              let matches: boolean;
              try {
                matches = el.matches(bare);
              } catch {
                // A selector this browser cannot parse cannot be one it applies.
                matches = false;
              }
              if (matches) {
                out.push({ selector: raw, value });
                break;
              }
            }
          }
          walk(rule.cssRules, selectors);
        }
      };
      for (const sheet of document.styleSheets) {
        try {
          walk(sheet.cssRules, []);
        } catch {
          continue;
        }
      }
      return out;
    };

    // 1. The painted box: every fill carries a state and paints the button's own square box, so the asymmetric target
    // stays invisible. `.icon-btn`'s `&:hover` fill is not gated by `any-hover`, and still paints only that square.
    const onElement = fillWriters(close, "");
    expect(onElement.length, "the sweep reaches the nested state rules").toBeGreaterThan(1);
    const resting = onElement.filter(
      (w) => !/:(?:hover|active|focus)/.test(w.selector) && !/^none$/.test(w.value.trim()),
    );
    expect(
      resting.map((w) => `${w.selector} { background: ${w.value} }`),
      "no stateless rule fills the remove button",
    ).toEqual([]);
    const r = close.getBoundingClientRect();
    expect(r.width, "the painted box is square, so no state fill can be asymmetric").toBe(r.height);
    expect(getComputedStyle(close).backgroundColor, "transparent at rest").toBe("rgba(0, 0, 0, 0)");

    // 2. The target: the expander is unpainted in every state and declares no border; the label's expander likewise.
    for (const el of [close, label]) {
      expect(fillWriters(el, "::after"), "nothing fills the expander, in any state").toEqual([]);
      const after = getComputedStyle(el, "::after");
      expect(after.backgroundColor).toBe("rgba(0, 0, 0, 0)");
      expect(after.borderTopWidth).toBe("0px");
      expect(after.borderLeftWidth).toBe("0px");
    }
  });

  it("never lets the remove button's target cover the filename", async () => {
    // `×` is destructive, so its target may not reach the label's; they touch across the chip's 4px gap.
    await chipsAt(390, 844, "coarse");
    const label = document.querySelector<HTMLElement>("#attachment-row .attachment-open");
    const close = document.querySelector<HTMLElement>("#attachment-row .attachment-close");
    if (label === null || close === null) {
      throw new Error("the chip did not mount");
    }
    const lr = label.getBoundingClientRect();
    const cr = close.getBoundingClientRect();
    for (const x of [lr.right - 1, lr.right - 4, lr.left + lr.width * 0.75]) {
      const hit = document.elementFromPoint(x, lr.top + lr.height / 2);
      expect(
        hit !== null && close.contains(hit),
        `the remove button owns the label at x=${String(Math.round(x - lr.left))}`,
      ).toBe(false);
    }
    // The remove button's target still clears WCAG 2.5.8's 24px on the axis it may not grow.
    expect(cr.width).toBe(24);
  });
});
