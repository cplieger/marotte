// The conflict overlay's actions are the shared `btn-small` at the dense control tier. Measured, because
// `padding-block: 0` must beat 14-tools.css's later-slice shorthand and `max(--ctl-h-dense, --hit-floor)` resolves per
// pointer tier. The overlay is hand-built (`renderConflictOverlay` needs the `$` registry), so the TS class is not pinned.

import { describe, it, expect, beforeAll, afterAll, afterEach } from "vitest";

import { loadCSS, mountAppCSS, ruleContaining } from "./__test-helpers__/css-rules.js";

let style: HTMLStyleElement;

beforeAll(() => {
  style = mountAppCSS();
});

afterAll(() => {
  style.remove();
});

afterEach(() => {
  document.body.replaceChildren();
  document.documentElement.removeAttribute("data-pointer");
});

/** One hunk row as editor-conflict.ts builds it, inside the overlay wrapper whose padding the tier is sized against. */
function mountRow(pointer: "fine" | "coarse"): {
  buttons: HTMLButtonElement[];
  suggest: HTMLButtonElement;
} {
  document.documentElement.setAttribute("data-pointer", pointer);
  const overlay = document.createElement("div");
  overlay.className = "editor-conflict-overlay";
  const row = document.createElement("div");
  row.className = "conflict-hunk-row";
  row.setAttribute("role", "group");
  const title = document.createElement("span");
  title.className = "conflict-hunk-title";
  title.textContent = "Line 12: HEAD vs incoming";
  row.appendChild(title);
  const buttons: HTMLButtonElement[] = [];
  for (const label of ["Ours", "Theirs", "Both"]) {
    const b = document.createElement("button");
    b.className = "btn-small conflict-btn";
    b.textContent = label;
    row.appendChild(b);
    buttons.push(b);
  }
  const suggest = document.createElement("button");
  suggest.className = "btn-small conflict-btn conflict-btn-suggest";
  suggest.textContent = "Suggest";
  row.appendChild(suggest);
  buttons.push(suggest);
  overlay.appendChild(row);
  document.body.replaceChildren(overlay);
  return { buttons, suggest };
}

describe("the shared skin reaches a conflict action", () => {
  // Against a control `.btn-small` outside the row, so a local copy re-introduced on `.conflict-btn` fails here.
  // Height is excluded: the dense tier is a legitimate divergence.
  it("resolves the same box as a btn-small elsewhere on the page", () => {
    const { buttons } = mountRow("fine");
    const control = document.createElement("button");
    control.className = "btn-small";
    control.textContent = "Control";
    document.body.appendChild(control);

    const shape = (b: Element): string => {
      const s = getComputedStyle(b);
      return [
        s.borderTopWidth,
        s.borderTopStyle,
        s.borderTopColor,
        s.borderRadius,
        s.backgroundColor,
        s.color,
        s.fontSize,
        s.paddingLeft,
        s.paddingRight,
      ].join(" / ");
    };
    const want = shape(control);
    // Non-initial by construction, or two unstyled buttons would compare equal.
    expect(want).toContain("1px / solid");
    // Suggest legitimately differs in ink and border, so the three mechanical actions are the population.
    for (const b of buttons.slice(0, 3)) {
      expect(shape(b), b.textContent ?? "").toBe(want);
    }
  });
});

describe("the dense tier, per pointer tier", () => {
  // `--ctl-h-dense` 2rem/2.5rem and `--hit-floor` 1.5rem/2.75rem, so the max() is 32px fine and 44px coarse.
  it.each([
    ["fine", 32],
    ["coarse", 44],
  ] as const)("holds a %s-pointer action at %ipx", (pointer, expected) => {
    const { buttons } = mountRow(pointer);
    for (const b of buttons) {
      expect(Math.round(b.getBoundingClientRect().height), b.textContent ?? "").toBe(expected);
    }
  });

  it("zeroes the block padding so the floor can bind", () => {
    const { buttons } = mountRow("fine");
    const [first] = buttons;
    expect(first).toBeDefined();
    const cs = getComputedStyle(first!);
    // Cross-slice: `padding-block: 0` in 60-mcp.css against the shorthand in 14-tools.css.
    expect(cs.paddingTop).toBe("0px");
    expect(cs.paddingBottom).toBe("0px");
    expect(cs.paddingLeft).not.toBe("0px");
  });
});

describe("the Suggest variant", () => {
  it("differs from its three neighbours in ink AND border, not fill", () => {
    const { buttons, suggest } = mountRow("fine");
    const [neighbour] = buttons;
    expect(neighbour).toBeDefined();
    const base = getComputedStyle(neighbour!);
    const cs = getComputedStyle(suggest);
    // Against a sibling, not the token: the variant must read differently from the actions beside it.
    expect(cs.color).not.toBe(base.color);
    expect(cs.borderTopColor).not.toBe(base.borderTopColor);
    expect(cs.color).toBe(cs.borderTopColor);
    expect(cs.backgroundColor).toBe(base.backgroundColor);
  });

  // Source, not computed: a synthetic hover drives no style recalc. `.btn-small`'s `&:hover` ties this rule's
  // specificity, so without the restatement the accent ink drops on hover.
  it("re-states its ink on hover so the shared rule cannot take it", () => {
    const hover = ruleContaining(loadCSS("60-mcp.css"), ".conflict-btn-suggest:hover", "top");
    expect(hover.body).toMatch(/color:\s*var\(--c-accent\)/);
  });
});
