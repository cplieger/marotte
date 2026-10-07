// One hover gate for every transcript disclosure trigger: an ungated `:hover` latches under a finger (the tap ends
// with the finger on the header). `14-tools.css` states the rule at `.tool-group-header`; this asserts it across the
// population. `any-hover`, never `hover`.
import { describe, it, expect } from "vitest";

import { manifestSheets } from "./__test-helpers__/css-rules.js";

interface Trigger {
  /** The stylesheet the rule lives in, as `css/MANIFEST` spells it. */
  readonly file: string;
  /** The selector EXACTLY as authored, so a member of a list is findable. */
  readonly selector: string;
  /** What the reader is hovering, for a failure message that names the surface. */
  readonly what: string;
}

const TRIGGERS: readonly Trigger[] = [
  {
    file: "13-messages.css",
    selector: ".reasoning-summary:hover",
    what: "the reasoning trace's <summary>",
  },
  {
    file: "13-messages.css",
    selector: ".code-refs-summary:hover",
    what: "the code-references footnote's <summary>",
  },
  {
    file: "13-messages.css",
    selector: ".compaction-head:hover",
    what: "the compaction break's <summary>",
  },
  {
    file: "14-tools.css",
    selector: ".tool-group-header:hover",
    what: "a tool group's header",
  },
  {
    file: "14-tools.css",
    selector: ".tool-summary.has-disclosure:hover",
    what: "a tool card's summary row",
  },
  {
    // Shares its rule with the leaf below; listed twice because a split leaving one behind would pass on the other.
    file: "14-tools.css",
    selector: ".subagent-container.has-disclosure > .subagent-header:hover",
    what: "a delegate pipeline container's header",
  },
  {
    file: "14-tools.css",
    selector: "a.subagent-header:hover",
    what: "a delegate leaf card's head",
  },
  {
    file: "27-run-card.css",
    selector: ".run-head:hover",
    what: "a run card's head",
  },
  {
    file: "27-run-card.css",
    selector: ".run-step-head:hover",
    what: "a run card's step row",
  },
  {
    file: "29-turns.css",
    selector: ".turn:not([data-running], [data-no-fold]) > .turn-header:hover",
    what: "the turn card's band",
  },
];

const ANY_HOVER = /^@media\s*\(\s*any-hover\s*:\s*hover\s*\)$/u;
const PRIMARY_ONLY = /\(\s*hover\s*:\s*hover\s*\)/u;

/** Blanked, not deleted, so nothing inside a comment is read as CSS. */
function stripComments(css: string): string {
  return css.replace(/\/\*[\s\S]*?\*\//gu, (m) => " ".repeat(m.length));
}

/** Split at top-level commas only: `:not(a, b)` holds a comma, and a naive split silently finds nothing. */
function members(prelude: string): string[] {
  const out: string[] = [];
  let depth = 0;
  let start = 0;
  for (let i = 0; i < prelude.length; i++) {
    const ch = prelude[i];
    if (ch === "(" || ch === "[") {
      depth++;
    } else if (ch === ")" || ch === "]") {
      depth--;
    } else if (ch === "," && depth === 0) {
      out.push(prelude.slice(start, i).trim());
      start = i + 1;
    }
  }
  out.push(prelude.slice(start).trim());
  return out.filter((s) => s !== "");
}

/** A brace walk over the source: an unmatched `@media` is absent from the matched CSSOM rules. */
function rulesNaming(css: string, selector: string): { readonly gates: string[] }[] {
  const text = stripComments(css);
  const found: { gates: string[] }[] = [];
  const open: string[] = [];
  let start = 0;
  for (let i = 0; i < text.length; i++) {
    const ch = text[i];
    if (ch === "{") {
      const prelude = text.slice(start, i).trim();
      if (!prelude.startsWith("@") && members(prelude).includes(selector)) {
        found.push({ gates: open.filter((p) => p.startsWith("@")) });
      }
      open.push(prelude);
      start = i + 1;
    } else if (ch === "}") {
      open.pop();
      start = i + 1;
    } else if (ch === ";") {
      start = i + 1;
    }
  }
  return found;
}

describe("the disclosure trigger hover gate", () => {
  it("gates every transcript disclosure trigger on any-hover, never on hover", () => {
    const sheets = new Map(manifestSheets().map((s) => [s.name, s.css]));
    const offenders: string[] = [];

    for (const { file, selector, what } of TRIGGERS) {
      const css = sheets.get(file);
      if (css === undefined) {
        offenders.push(`${file}: not in css/MANIFEST (${what})`);
        continue;
      }

      const rules = rulesNaming(css, selector);
      if (rules.length !== 1) {
        // Zero means the selector moved; more than one means two rules disagree about the gate.
        offenders.push(`${file} ${selector}: ${rules.length} rules name it (${what})`);
        continue;
      }

      const [only] = rules;
      const gates = only?.gates ?? [];
      if (!gates.some((g) => ANY_HOVER.test(g))) {
        offenders.push(
          `${file} ${selector}: NOT gated on @media (any-hover: hover) — ${what} latches its hover after a tap` +
            (gates.length > 0 ? ` (enclosed by ${gates.join(" / ")})` : " (at top level)"),
        );
      }
      if (gates.some((g) => PRIMARY_ONLY.test(g))) {
        offenders.push(
          `${file} ${selector}: gated on the PRIMARY-input query, which drops the rule on every touch-primary device (${what})`,
        );
      }
    }

    expect(offenders, offenders.join("\n")).toEqual([]);
  });
});
