// WHO OWNS THE PRESS when one control sits inside another.

import { describe, it, expect, beforeAll, afterAll } from "vitest";

import { loadCSS, mountAppCSS, ruleBody, ruleContaining } from "./__test-helpers__/css-rules.js";

/** Stands in for `:active` in the mounted bundle. One class, so specificity is unchanged
 *  wherever it lands. */
const PRESS = "vk-test-press";

let style: HTMLStyleElement;
let host: HTMLElement;

beforeAll(() => {
  style = mountAppCSS();
  const shipped = style.textContent ?? "";
  const swapped = shipped.replaceAll(":active", `.${PRESS}`);
  // The substitution is the premise of every computed assertion below, so it is checked rather than
  // assumed: a bundle that stopped containing `:active` would otherwise make the whole file pass by
  // describing nothing.
  expect(
    shipped.split(":active").length - 1,
    "the bundle must carry the :active rules this file substitutes",
  ).toBeGreaterThan(20);
  expect(swapped, "no :active may survive the substitution").not.toContain(":active");
  style.textContent = swapped;

  host = document.createElement("div");
  document.body.appendChild(host);
});

afterAll(() => {
  style.remove();
  host.remove();
});

/** One token's computed value, so every comparison is computed-against-computed: the engine
 *  normalises a colour on the way out, so a token's authored `color-mix(...)` text is not what
 *  any element resolves to. */
function tokenColor(token: string): string {
  const probe = document.createElement("span");
  probe.style.setProperty("background-color", `var(${token})`);
  document.body.appendChild(probe);
  const value = getComputedStyle(probe).backgroundColor;
  probe.remove();
  return value;
}

interface Row {
  readonly row: HTMLElement;
  readonly close: HTMLElement;
}

/** A selected tab row, optionally with the press mark on the row, on the × (which marks the row
 *  too, the way the engine propagates it), or on neither. */
function tab(pressed: "none" | "row" | "close"): Row {
  const row = document.createElement("div");
  row.className = "tab active";
  row.setAttribute("role", "tab");
  const name = document.createElement("span");
  name.className = "tab-name";
  name.textContent = "a chat";
  const close = document.createElement("span");
  close.className = "tab-close";
  row.append(name, close);
  if (pressed !== "none") {
    row.classList.add(PRESS);
  }
  if (pressed === "close") {
    close.classList.add(PRESS);
  }
  host.appendChild(row);
  return { row, close };
}

const bg = (el: HTMLElement): string => getComputedStyle(el).backgroundColor;

describe("the row's press", () => {
  it("paints the selected press fill when the row itself is pressed", () => {
    // The premise, and the behaviour that must not regress: this is the press the reader already
    // has and likes. Without it every assertion below would pass for the trivial reason that the
    // substitution reached nothing.
    const resting = tab("none");
    const pressed = tab("row");
    expect(bg(resting.row)).toBe(tokenColor("--c-selected-bg"));
    expect(bg(pressed.row)).toBe(tokenColor("--c-selected-bg-press"));
  });

  it("is not painted when the press is on the ×", () => {
    const pressed = tab("close");
    expect(
      bg(pressed.row),
      "a press on the close affordance must not paint the row's press fill",
    ).not.toBe(tokenColor("--c-selected-bg-press"));
  });

  it("steps back one rung on the selected ramp rather than to the resting fill", () => {
    // The selected fill is a three-rung ramp — resting, `-hover` at 12% of the ink, `-press` at 22%
    // — and a row whose child is taking the press wants the rung one short of its own press, not
    // nothing at all: dropping to resting under the pointer reads as an un-hover, which is a change
    // in the wrong direction.
    const resting = tab("none");
    const pressed = tab("close");
    expect(bg(pressed.row)).toBe(tokenColor("--c-selected-bg-hover"));
    expect(bg(pressed.row), "one rung short of the press, not zero rungs").not.toBe(
      bg(resting.row),
    );
  });
});

describe("the ×'s own press", () => {
  it("answers a press with the app's press wash", () => {
    const resting = tab("none");
    const pressed = tab("close");
    // Transparent at rest — the × paints nothing until it is hovered or pressed, so this pair is
    // what says the press is visible at all.
    expect(bg(resting.close)).toBe("rgba(0, 0, 0, 0)");
    expect(bg(pressed.close)).toBe(tokenColor("--c-press"));
  });
});

/** A selected file-browser listing row, built the way `files.ts` `entryRow` does: a
 *  `div[role="listitem"]` with no listener of its own, whose three activations all belong to
 *  children. `pressed` marks the row the way the engine would for a press anywhere inside it. */
function fbRow(pressed: boolean): { row: HTMLElement; letter: HTMLElement } {
  const row = document.createElement("div");
  row.className = "fb-row fb-row-selected";
  row.setAttribute("role", "listitem");
  const check = document.createElement("input");
  check.type = "checkbox";
  check.className = "fb-check";
  const name = document.createElement("span");
  name.className = "fb-name fb-name-link";
  name.textContent = "main.go";
  const letter = document.createElement("span");
  letter.className = "fb-git-letter git-st-m fb-git-clickable";
  letter.setAttribute("role", "button");
  letter.textContent = "M";
  const meta = document.createElement("span");
  meta.className = "fb-meta";
  meta.textContent = "1.2 kB";
  row.append(check, name, letter, meta);
  if (pressed) {
    row.classList.add(PRESS);
  }
  host.appendChild(row);
  return { row, letter };
}

describe("a row that is not a control has no press to give away", () => {
  it("leaves a selected file row on its resting fill under a press", () => {
    // `files.ts` `entryRow` attaches nothing to the row: the name span navigates, the checkbox
    // selects, and the git letter opens that file's diff.
    const resting = fbRow(false);
    const pressed = fbRow(true);
    expect(bg(pressed.row)).toBe(bg(resting.row));
    expect(bg(pressed.row)).toBe(tokenColor("--c-selected-bg"));
  });

  it("keeps the fill and the hover, which claim something else", () => {
    // Neither rung says the row can be activated: the selected fill states which rows an action
    // would apply to, and the hover states which row the pointer is on. So the removal is surgical
    // rather than the surface leaving the vocabulary.
    const sel = loadCSS("70-selection.css");
    expect(ruleContaining(sel, ".fb-row.fb-row-selected", "top").body).toMatch(
      /background:\s*var\(--c-selected-bg\)/u,
    );
    expect(ruleContaining(sel, ".fb-row.fb-row-selected:hover", "any-hover").body).toMatch(
      /background:\s*var\(--c-selected-bg-hover\)/u,
    );
  });

  it("still gives the git letter inside it a press of its own", () => {
    // The letter is `role="button"`, so 03-base.css's wash reaches it with no rule here — which is
    // why the row needed removing rather than a `:has()` yield: the control was never the half that
    // was missing.
    const { letter } = fbRow(false);
    letter.classList.add(PRESS);
    expect(getComputedStyle(letter).boxShadow).toContain("inset");
  });
});

describe("read as source", () => {
  it("declares the ×'s press one rung deeper on the hover's own axis", () => {
    const shell = loadCSS("10-shell-app.css");
    // Same PROPERTY as the hover a few lines above it, which is what makes the two read as two
    // depths of one wash — and what lets the press animate, since `.tab-close`'s transition list
    // names `background` and not `box-shadow`.
    expect(ruleBody(shell, ".tab-close:hover")).toMatch(/background: color-mix\(/u);
    expect(ruleBody(shell, ".tab-close:active")).toMatch(/background: var\(--c-press\)/u);
    expect(ruleBody(shell, ".tab-close"), "the press must animate, so it must transition").toMatch(
      /transition:[^;]*\bbackground\b/u,
    );
  });

  it("keeps the row's yield in the same rule that names the state", () => {
    // The association a pair of file-wide `toContain`s cannot make: the selector and the
    // declaration have to be one rule, or the yield is two facts about one file that happen to sit
    // near each other.
    const sel = loadCSS("70-selection.css");
    const yielded = ruleContaining(sel, ".tab.active:has(.tab-close:active)", "top");
    expect(yielded.body).toMatch(/background:\s*var\(--c-selected-bg-hover\)/u);
    // Ungated on purpose.
    expect(yielded.body, "no media gate: the reason for the hover rule's is absent").not.toContain(
      "@media",
    );
  });

  it("still lets the row's own press win over the resting fill", () => {
    // `.tab.active:active` scores (0,3,0) and the yield (0,4,0), so the yield wins on specificity
    // wherever either file sits. Asserted as the pairing rather than as arithmetic: the press
    // declaration has to stay on the rule that lists the tab.
    const sel = loadCSS("70-selection.css");
    expect(ruleContaining(sel, ".tab.active:active", "top").body).toMatch(
      /background:\s*var\(--c-selected-bg-press\)/u,
    );
  });
});
