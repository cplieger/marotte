// Pins the one disclosure-chevron vocabulary: builders use `chevronEl()`, stylesheets grow no other technique.
// The source half reads the shipped sheets because the test page loads none (see __test-helpers__/css-rules.ts).

import { vi, describe, it, expect } from "vitest";
import { loadCSS } from "./__test-helpers__/css-rules.js";

vi.mock("./scroll.js", () => ({
  setUserScrolledUp: vi.fn(),
  preserveReadingPosition: (fn: () => void) => {
    fn();
  },
}));

import { chevronEl } from "./chevron.js";
import { buildToolGroupShell } from "./tool-group.js";
import { buildSubagentContainer } from "./fundamentals/subagent-block.js";
import { buildReasoning } from "./fundamentals/reasoning.js";
import { buildTurnHeader } from "./fundamentals/turn-header.js";
import { buildTurnFooter } from "./fundamentals/turn-footer.js";

/** Every stylesheet that styles a disclosure; the angle check reads only these, so a missing sheet hides a composed rotation. */
const SHEETS = [
  "10-shell-app.css",
  "14-tools.css",
  "17-settings.css",
  "22-git-multirepo.css",
  "27-run-card.css",
  "29-turns.css",
  "31-exec-view.css",
  "61-mcp-tools.css",
];

function stripComments(css: string): string {
  return css.replace(/\/\*[\s\S]*?\*\//gu, "");
}

describe("chevronEl", () => {
  it("is one span carrying the shared class, one svg, and no accessible name", () => {
    const c = chevronEl();
    expect(c.tagName).toBe("SPAN");
    expect(c.classList.contains("disclosure-chevron")).toBe(true);
    expect(c.getAttribute("aria-hidden")).toBe("true");
    expect(c.querySelectorAll("svg")).toHaveLength(1);
    expect(c.textContent).toBe("");
  });
});

describe("every disclosure builder emits the shared chevron", () => {
  it("tool group header", () => {
    const g = buildToolGroupShell();
    expect(g.querySelectorAll(".disclosure-chevron")).toHaveLength(1);
    g.classList.add("tool-group-collapsed");
    expect(g.querySelectorAll(".disclosure-chevron")).toHaveLength(1);
  });

  // A delegate card is absent: it renders none of its delegate's output, so it is not a disclosure.
  it("pipeline container, and it survives a status flip", async () => {
    const sa = buildSubagentContainer("orchestrate", "in_progress");
    // Needs a stage: a container with an empty body withdraws its control, chevron included.
    sa.body.appendChild(document.createElement("div")).textContent = "stage";
    await new Promise<void>((resolve) => {
      setTimeout(resolve, 0);
    });
    expect(sa.root.querySelectorAll(".disclosure-chevron")).toHaveLength(1);
    sa.setStatus("completed");
    expect(sa.root.querySelectorAll(".disclosure-chevron")).toHaveLength(1);
  });

  it("reasoning summary, and sealing rewrites the LABEL not the summary", () => {
    const r = buildReasoning("thinking about it", true, true);
    expect(r.root.querySelectorAll(".disclosure-chevron")).toHaveLength(1);
    expect(r.root.querySelectorAll(".reasoning-count")).toHaveLength(1);
    // Guards against `summary.textContent = …` deleting the glyph.
    r.seal();
    expect(r.root.querySelectorAll(".disclosure-chevron")).toHaveLength(1);
    expect(r.root.querySelector(".reasoning-label")?.textContent).toBe("Thinking completed");
    expect(r.root.querySelector(".reasoning-count")?.textContent).toBe("3 words");
  });

  it("turn header fold toggle", () => {
    const h = buildTurnHeader({
      n: 4,
      outcome: "completed",
      ts: Date.now(),
      request: "converge the chevrons",
      attachments: [],
    });
    expect(h.querySelectorAll(".turn-fold-toggle > .disclosure-chevron")).toHaveLength(1);
  });

  // The turn footer's trigger is an `i`, not a chevron; asserted as an absence so a chevron cannot reappear beside it.
  it("the turn footer carries no chevron at all", () => {
    const f = buildTurnFooter({ changedFiles: {} });
    expect(f.querySelectorAll(".disclosure-chevron")).toHaveLength(0);
    expect(f.querySelectorAll(".turn-ledger-info")).toHaveLength(1);
  });
});

// Disclosure chevrons lead, navigating ones trail: rotation alone cannot tell a closed disclosure from navigation.
// Asserted on DOM order, not geometry, because the boxes lay out differently.
describe("position carries the interaction type", () => {
  function chevronEnd(header: Element): "leading" | "trailing" | "absent" {
    const kids = [...header.children];
    const i = kids.findIndex(
      (k) =>
        k.querySelector(".disclosure-chevron") !== null ||
        k.classList.contains("disclosure-chevron"),
    );
    if (i === -1) {
      return "absent";
    }
    return i === 0 ? "leading" : i === kids.length - 1 ? "trailing" : "absent";
  }

  it("the tool group's disclosure leads", () => {
    const g = buildToolGroupShell();
    expect(chevronEnd(g.querySelector(".tool-group-header")!)).toBe("leading");
  });

  it("the pipeline container's disclosure leads", async () => {
    const sa = buildSubagentContainer("orchestrate", "in_progress");
    sa.body.appendChild(document.createElement("div")).textContent = "stage";
    await new Promise<void>((resolve) => {
      setTimeout(resolve, 0);
    });
    expect(chevronEnd(sa.root.querySelector(".subagent-header")!)).toBe("leading");
  });

  it("a delegate LEAF's navigation chevron trails, in the same header class", async () => {
    const { buildSubagentCard } = await import("./fundamentals/subagent-block.js");
    const leaf = buildSubagentCard("context-gatherer", "completed", {
      open: { href: "/chat/c-1/subagent/s-1", open: () => undefined },
    });
    const head = leaf.root.querySelector(".subagent-header")!;
    expect(head.tagName).toBe("A");
    expect(chevronEnd(head)).toBe("trailing");
  });

  it("the run card's disclosure leads", async () => {
    const { buildRunCard } = await import("./fundamentals/run-card.js");
    const rc = buildRunCard("wf-1", "recipe", () => undefined);
    expect(chevronEnd(rc.root.querySelector(".run-head")!)).toBe("leading");
  });

  it("the reasoning trace's disclosure leads", () => {
    const r = buildReasoning("thinking", false, false);
    expect(chevronEnd(r.root.querySelector(".reasoning-summary")!)).toBe("leading");
  });

  it("the turn fold leads", () => {
    const h = buildTurnHeader({
      n: 4,
      outcome: "completed",
      ts: Date.now(),
      request: "converge the chevrons",
      attachments: [],
    });
    // The header's children are the meta row and `.turn-req`; the toggle sits in the row.
    expect(chevronEnd(h.querySelector(".turn-head-row")!)).toBe("leading");
  });

  it("the tool card's disclosure leads, by CSS rather than by DOM order", async () => {
    // DOM order cannot answer here: the button is appended last and placed by `inset-inline-start`, so the sheet owns it.
    const body = stripComments(loadCSS("14-tools.css"));
    const rule = /\.tool-disclosure\s*\{([^}]*)\}/u.exec(body)?.[1] ?? "";
    expect(rule).toMatch(/inset-inline-start:/u);
    expect(rule).not.toMatch(/inset-inline-end:/u);
  });
});

describe("the stylesheets carry exactly one chevron technique", () => {
  it("declares the base rule once, in the components layer", () => {
    const hits = SHEETS.filter((s) =>
      /^\s*\.disclosure-chevron\s*\{/mu.test(stripComments(loadCSS(s))),
    );
    expect(hits).toEqual(["10-shell-app.css"]);
  });

  it("has no font-glyph triangle left as CSS content", () => {
    const found: string[] = [];
    for (const sheet of SHEETS) {
      const body = stripComments(loadCSS(sheet));
      // `▸ ▾ ▴ ◂` and their \25Bx / \25Cx escapes, as a `content` value.
      if (/content:[^;}]*(?:[\u25B0-\u25CF]|\\25[BC][0-9A-Fa-f])/u.test(body)) {
        found.push(sheet);
      }
    }
    expect(found).toEqual([]);
  });

  it("draws no chevron from a pair of rotated borders", () => {
    const found: string[] = [];
    for (const sheet of SHEETS) {
      const body = stripComments(loadCSS(sheet));
      // Border-pair chevron: adjacent border-right + border-bottom on a tiny box.
      if (
        /border-right:[^;}]*solid currentcolor;\s*border-bottom:[^;}]*solid currentcolor/u.test(
          body,
        )
      ) {
        found.push(sheet);
      }
    }
    expect(found).toEqual([]);
  });

  it("states the open angle as 0deg everywhere, and the closed angle only once", () => {
    const opens: string[] = [];
    let closedDecls = 0;
    for (const sheet of SHEETS) {
      const body = stripComments(loadCSS(sheet));
      for (const m of body.matchAll(/--chev-turn:\s*(-?[\d.]+deg)/gu)) {
        const v = m[1];
        if (v === "0deg") {
          opens.push(sheet);
        } else if (v === "-90deg") {
          closedDecls++;
        } else {
          opens.push(`${sheet}:UNEXPECTED ${String(v)}`);
        }
      }
    }
    expect(opens.filter((o) => o.includes("UNEXPECTED"))).toEqual([]);
    expect(opens.length).toBeGreaterThanOrEqual(6);
    expect(closedDecls).toBe(1);
  });
});
