// `geometrySkipped` matches selectors that claim to be every `content-visibility: hidden` rule. Nothing links the
// two, and a drift is a silent performance fault, so a source guard (the CSS moved) and a DOM guard (the matching
// broke) both pin it. A computed-style check cannot replace either.

import { describe, it, expect, vi } from "vitest";
import { loadCSS, ruleContaining } from "./__test-helpers__/css-rules.js";

// `scroll.ts` resolves `#messages` at module load and `byId` throws on a missing element, so the hosts exist first.
for (const id of [
  "messages",
  "messages-wrap",
  "messages-wrap-outer",
  "chat-view",
  "scroll-bottom",
]) {
  const d = document.createElement("div");
  d.id = id;
  document.body.appendChild(d);
}

vi.mock("./scroll.js", () => import("./__test-helpers__/scroll-mock.js").then((m) => m.scrollMock));

const { geometrySkipped } = await import("./messages-blocks.js");

/** The selector strings are the predicate's own, so a drift on either side fails here. */
const SKIPPED = [
  { sheet: "29-turns.css", selector: ".turn[data-folded] > .turn-body" },
  { sheet: "13-messages.css", selector: ".transcript-view:not(.is-active)" },
  { sheet: "14-tools.css", selector: ".subagent-block.collapsed > .subagent-body" },
] as const;

/** Builds the shape for real, so the predicate walks a genuine ancestor chain. */
function mountSkipped(selector: string): HTMLElement {
  const host = document.createElement("div");
  if (selector.startsWith(".turn[")) {
    host.className = "turn";
    host.setAttribute("data-folded", "");
    host.innerHTML = `<div class="turn-body"><div class="message assistant">prose</div></div>`;
  } else if (selector.startsWith(".subagent-block")) {
    host.className = "subagent-block collapsed";
    host.innerHTML = `<div class="subagent-body"><div class="message assistant">prose</div></div>`;
  } else if (selector.startsWith(".transcript-view")) {
    host.className = "transcript-view";
    host.innerHTML = `<div class="turn-body"><div class="message assistant">prose</div></div>`;
  } else {
    // A fall-through once let an unwritten shape match the parked-view rule and pass for the wrong predicate.
    throw new Error(`no shape mounted for ${selector}`);
  }
  document.body.appendChild(host);
  const leaf = host.querySelector<HTMLElement>(".message");
  if (leaf === null) {
    throw new Error(`no leaf mounted for ${selector}`);
  }
  return leaf;
}

describe("geometrySkipped tracks the stylesheets it speaks for", () => {
  for (const { sheet, selector } of SKIPPED) {
    it(`${selector} is still where ${sheet} skips rendering`, () => {
      const rule = ruleContaining(loadCSS(sheet), selector, "top");
      expect(rule.body).toContain("content-visibility: hidden");
    });

    it(`answers true inside ${selector}`, () => {
      expect(geometrySkipped(mountSkipped(selector))).toBe(true);
    });
  }

  it("answers false for a block the page is rendering", () => {
    // The control: without it a predicate returning true unconditionally satisfies every case above.
    const host = document.createElement("div");
    host.className = "transcript-view is-active";
    host.innerHTML = `<div class="turn"><div class="turn-body"><div class="message assistant">prose</div></div></div>`;
    document.body.appendChild(host);
    const leaf = host.querySelector<HTMLElement>(".message");
    expect(leaf).not.toBeNull();
    expect(geometrySkipped(leaf as HTMLElement)).toBe(false);
  });
});
