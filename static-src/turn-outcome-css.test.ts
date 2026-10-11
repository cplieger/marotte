// Does the transcript paint ONE hue per severity, on every surface? Asserted in BOTH DIRECTIONS:
// every painting severity has a rule, and no surface carries a `[data-outcome=…]` colour rule for an
// outcome the table grades `broken` (per-surface tables once painted `interrupted` red and yellow
// on one card). Source facts: the test page links no app stylesheet (`css-rules.ts`). Meaningful
// only beside `turn-outcome-attr.test.ts`, which pins the writers.
import { describe, it, expect } from "vitest";

import { allRules, loadCSS, ruleContaining } from "./__test-helpers__/css-rules.js";
import { severityOf } from "./turn-severity.js";
import type { TurnOutcome } from "./turns.js";
import type { TurnSeverity } from "./wire/types.gen.js";

const turns = loadCSS("29-turns.css");

/** Every outcome the wire can send. Not imported as a list — the generated union
 *  is a type — so it is spelled here and pinned total by the severity coverage
 *  case at the foot of this file. */
const OUTCOMES: TurnOutcome[] = [
  "running",
  "completed",
  "cancelled",
  "interrupted",
  "refused",
  "unknown",
  "failed",
  "empty",
];

/** The MARK surfaces: the severity arms' selector prefix plus the mark descendant.
 *  `.turn-notice` carries ink, not a mark, and is asserted at the foot. */
const SURFACES = [
  { name: "header dot", sel: (attr: string) => `.turn-header[${attr}] .turn-dot` },
  { name: "footer wash", sel: (attr: string) => `.turn-footer[${attr}]` },
  { name: "footer glyph", sel: (attr: string) => `.turn-footer[${attr}] .turn-ledger-glyph` },
  {
    name: "turn-map mark",
    sel: (attr: string) => `.turn-pill[${attr}] > .turn-pill-link > .turn-pill-mark`,
  },
] as const;

/** Which severities a surface paints a MARK for. `clean` and `running` paint
 *  nothing on the dot, the glyph and the map's lane (absence IS the clean mark, the footer reports
 *  how a turn ENDED, and the map outlines a running bar instead). */
const MARKED: TurnSeverity[] = ["stopped", "broken"];

describe("hue comes off the severity table, on every surface", () => {
  it("gives each surface a rule for every severity that paints a mark", () => {
    for (const surface of SURFACES) {
      for (const severity of MARKED) {
        const rule = ruleContaining(turns, surface.sel(`data-severity="${severity}"`));
        // A ring is an inset `box-shadow` (the turn map's stopped mark).
        expect(rule.body, `${surface.name} / ${severity} declares a colour`).toMatch(
          /(background|border-color|color|box-shadow):/u,
        );
      }
    }
  });

  it("re-colours a BROKEN turn away from the resting ink on every surface, and never hides it", () => {
    // The failure DIRECTION: `display: none`, a green ring or tertiary ink would all read as success.
    const dot = ruleContaining(turns, '.turn-header[data-severity="broken"] .turn-dot');
    expect(dot.body).not.toMatch(/display:\s*none/u);
    expect(dot.body).not.toMatch(/var\(--c-text-tertiary\)/u);

    const glyph = ruleContaining(turns, '.turn-footer[data-severity="broken"] .turn-ledger-glyph');
    expect(glyph.body, "a broken turn still paints a mark").not.toMatch(/display:\s*none/u);
    expect(glyph.body, "a broken turn does not keep the clean green ring").not.toMatch(
      /--c-green/u,
    );
    expect(glyph.body, "a broken glyph is filled, not a ring").toMatch(/background:/u);

    const mark = ruleContaining(
      turns,
      '.turn-pill[data-severity="broken"] > .turn-pill-link > .turn-pill-mark',
    );
    expect(mark.body, "a broken turn-map mark is a filled red disc").toMatch(
      /background:\s*var\(--c-red\)/u,
    );

    const wash = ruleContaining(turns, '.turn-footer[data-severity="broken"]');
    // The tinted-band token, never a `color-mix()` of the ink into the band: a mix
    // moves the band's lightness and the info panel's hint ink sits on it.
    expect(wash.body).toMatch(/background:\s*var\(--c-band-broken\)/u);
  });

  it("never paints broken and stopped the same, on any surface", () => {
    // The two are different verdicts, and a reader scanning a transcript separates
    // them by hue before reading a word. Equal bodies would mean the partition
    // exists in name only.
    for (const surface of SURFACES) {
      const broken = ruleContaining(turns, surface.sel('data-severity="broken"'));
      const stopped = ruleContaining(turns, surface.sel('data-severity="stopped"'));
      expect(broken.body.trim(), `${surface.name} separates broken from stopped`).not.toBe(
        stopped.body.trim(),
      );
    }
  });

  it("paints no settled mark for a clean or a running turn", () => {
    // The header dot and the footer glyph HIDE, which is `clean`'s whole treatment —
    // a mark on every row of the transcript communicates nothing. `running` joins
    // `clean` on the glyph because the footer reports how a turn ENDED.
    expect(ruleContaining(turns, '.turn-header[data-severity="clean"] .turn-dot').body).toMatch(
      /display:\s*none/u,
    );

    const glyph = ruleContaining(turns, '.turn-footer[data-severity="clean"] .turn-ledger-glyph');
    expect(glyph.body).toMatch(/display:\s*none/u);
    expect(glyph.selector).toContain('.turn-footer[data-severity="running"] .turn-ledger-glyph');
    expect(glyph.body).not.toMatch(/border-color:/u);

    // The wash and the turn map have no clean arm at all: both fall through to a resting
    // state authored for exactly that case, so an arm restating it would be dead.
    expect(
      allRules(turns).filter((r) => r.selector.includes('.turn-footer[data-severity="clean"]:not')),
    ).toEqual([]);
  });

  it("keeps the in-flight marks on the TAB STRIP's ink", () => {
    // One fact, one violet: both running marks share the tab dot's working colour.
    const dot = ruleContaining(turns, '.turn-header[data-severity="running"] .turn-dot');
    expect(dot.body).toContain("var(--c-dot-working)");
    expect(dot.body).not.toContain("--c-accent");

    const bar = ruleContaining(
      turns,
      '.turn-pill[data-severity="running"]:not([data-current]) > .turn-pill-link::before',
    );
    expect(bar.body).toContain("var(--c-dot-working)");
    expect(bar.body).not.toContain("--c-accent");
    expect(bar.body, "no beat on the map").not.toContain("animation");
  });

  it("keeps the header dot breathing, since motion is its second channel", () => {
    // The dot renders only below 48rem (strip off-canvas), the off-screen-work pulse case
    // (13-messages.css); motion separates `running` without hue.
    const dot = ruleContaining(turns, '.turn-header[data-severity="running"] .turn-dot');
    expect(dot.body).toContain("vk-dot-beat");
    expect(dot.body).toContain("--beat-peak: 0.4");
  });
});

// ---------------------------------------------------------------------------
// THE MISSING DIRECTION: does anything OVERRIDE the partition?
// ---------------------------------------------------------------------------

/** Every `[data-outcome="…"]` colour rule left, as (outcome, selector, body). Scoped to the three
 *  properties the partition owns; `data-outcome` keys other things too. */
function outcomeColourRules(): { outcome: string; selector: string; body: string }[] {
  const out: { outcome: string; selector: string; body: string }[] = [];
  for (const rule of allRules(turns)) {
    if (!/(background|border-color|color)\s*:/u.test(rule.body)) {
      continue;
    }
    for (const member of rule.selector.split(",").map((s) => s.trim())) {
      const hit = /\[data-outcome="([a-z_]+)"\]/u.exec(member);
      if (hit?.[1] !== undefined) {
        out.push({ outcome: hit[1], selector: member, body: rule.body });
      }
    }
  }
  return out;
}

describe("no hue may be set per OUTCOME behind the severity partition", () => {
  it("carries no colour rule for any outcome the table grades broken", () => {
    // A per-outcome fill on a `broken` outcome.
    const broken = OUTCOMES.filter((o) => severityOf(o) === "broken");
    expect(broken, "the population this case is about").toEqual([
      "interrupted",
      "refused",
      "failed",
    ]);
    const offenders = outcomeColourRules().filter((r) => broken.includes(r.outcome as TurnOutcome));
    expect(
      offenders.map((r) => r.selector),
      "a broken outcome's hue comes from data-severity and nowhere else",
    ).toEqual([]);
  });

  it("leaves `unknown` as the ONLY per-outcome colour rule, and states its ink", () => {
    // The one exception: `unknown` and `cancelled` are both `stopped`, but an end marotte could not read
    // has no honest hue. FOUR rules, one per surface. The turn map takes none: it grades by
    // severity alone, so `unknown` draws the stopped ring and its words say which stop it was.
    const rules = outcomeColourRules();
    expect(
      [...new Set(rules.map((r) => r.outcome))].sort(),
      "unknown is the only outcome with a colour rule of its own",
    ).toEqual(["unknown"]);

    const bySelector = new Map(rules.map((r) => [r.selector, r.body]));
    expect(
      [...bySelector.keys()].sort(),
      "one unknown override per surface, all four named",
    ).toEqual([
      '.turn-footer[data-outcome="unknown"]',
      '.turn-footer[data-outcome="unknown"] .turn-ledger-glyph',
      '.turn-header[data-outcome="unknown"] .turn-dot',
      '.turn-notice[data-outcome="unknown"]',
    ]);

    // Neutral ink everywhere except the wash, which restates the base background (untinted).
    for (const [selector, body] of bySelector) {
      if (selector === '.turn-footer[data-outcome="unknown"]') {
        expect(body).toMatch(/background:\s*var\(--c-bg-tertiary\)/u);
        continue;
      }
      expect(body, `${selector} paints the neutral ink`).toMatch(/var\(--c-text-tertiary\)/u);
    }
  });

  it("places each other unknown override AFTER its surface's severity rules", () => {
    // Same tie, other direction: a (0,3,0) `unknown` override written BEFORE the
    // (0,3,0) severity arm would lose, and the exception would silently stop
    // applying.
    const rules = allRules(turns).map((r) => r.selector);
    const idx = (needle: string): number => {
      const at = rules.findIndex((s) => s.includes(needle));
      expect(at, `${needle} exists`).toBeGreaterThan(-1);
      return at;
    };
    expect(idx('.turn-header[data-outcome="unknown"] .turn-dot')).toBeGreaterThan(
      idx('.turn-header[data-severity="stopped"] .turn-dot'),
    );
    expect(idx('.turn-footer[data-outcome="unknown"]')).toBeGreaterThan(
      idx('.turn-footer[data-severity="stopped"]'),
    );
    expect(idx('.turn-footer[data-outcome="unknown"] .turn-ledger-glyph')).toBeGreaterThan(
      idx('.turn-footer[data-severity="stopped"] .turn-ledger-glyph'),
    );
    expect(idx('.turn-notice[data-outcome="unknown"]')).toBeGreaterThan(
      idx('.turn-notice[data-severity="stopped"]'),
    );
  });

  it("grades every outcome, so the sweep above is a partition rather than a sample", () => {
    // What makes OUTCOMES total: a ninth member added to the generated union with
    // no entry here would silently drop out of every case in this file.
    const graded = new Map<TurnSeverity, TurnOutcome[]>();
    for (const o of OUTCOMES) {
      const s = severityOf(o);
      graded.set(s, [...(graded.get(s) ?? []), o]);
    }
    expect(Object.fromEntries(graded)).toEqual({
      running: ["running"],
      clean: ["completed"],
      stopped: ["cancelled", "unknown", "empty"],
      broken: ["interrupted", "refused", "failed"],
    });
  });
});

// ---------------------------------------------------------------------------
// The turn's own failure notice: a CARD-level sibling, not part of the face, so an OPEN turn shows
// it; a broken turn never auto-folds.
// ---------------------------------------------------------------------------

describe("the failure notice", () => {
  it("is styled at all, and reads as prose rather than a badge", () => {
    const rule = ruleContaining(turns, ".turn-notice");
    // pre-wrap because the text is upstream prose that may carry its own newlines,
    // and no clamp for the face's own reason: the fold hides a turn's WORK, never
    // what it has to say.
    expect(rule.body).toMatch(/white-space:\s*pre-wrap/u);
    expect(rule.body).not.toMatch(/-webkit-line-clamp/u);
  });

  it("tints by severity, so it never calls a cancel a failure", () => {
    // Red by default; `stopped` takes yellow. No current `stopped` outcome reaches the arm, so this pins
    // the FALLBACK's direction: without it a future `stopped` outcome would paint as a failure.
    expect(ruleContaining(turns, ".turn-notice").body).toMatch(/color:\s*var\(--c-red\)/u);
    expect(ruleContaining(turns, '.turn-notice[data-severity="stopped"]').body).toMatch(
      /color:\s*var\(--c-yellow\)/u,
    );
  });

  it("is not inside the collapsed face, which is what makes it reach an open turn", () => {
    // The whole structural point, asserted against the SELECTOR: a rule scoped
    // under `.turn-face` would be unreachable for an unfolded card, because
    // `syncTurnFace` early-returns for one.
    const rule = ruleContaining(turns, ".turn-notice");
    expect(rule.selector).not.toContain(".turn-face ");
  });
});
