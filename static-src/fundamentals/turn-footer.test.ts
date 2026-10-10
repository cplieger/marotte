// The turn card's outcome ledger and the INFO PANEL it discloses. Every section WITHHOLDS on
// absence, or most cards would grow empty headings.
import { describe, it, expect, vi, beforeAll, afterAll } from "vitest";
import { page } from "vitest/browser";

import { loadCSS, mountAppCSS } from "../__test-helpers__/css-rules.js";
import type * as MessageActions from "../actions/messages.js";
import type { FooterExtras, TurnSummaryData } from "./turn-footer.js";
import { projectTurns, turnLedger, type Turn } from "../turns.js";
import type { Entry, TurnState } from "../types.js";

const openFileGitDiff = vi.fn();
const openFile = vi.fn();
vi.mock("../editor-openers.js", () => ({
  // Undefined: present only so real-ESM linking succeeds.
  openFile: (path: string, line?: number) => {
    openFile(path, line);
  },
  openFileDiff: undefined,
  openFileGitDiff: (path: string) => {
    openFileGitDiff(path);
  },
}));

const copied = vi.fn();
vi.mock("../actions/messages.js", async (importOriginal) => ({
  ...(await importOriginal<typeof MessageActions>()),
  copyClipboard: {
    dispatch: (text: string) => {
      copied(text);
      return Promise.resolve();
    },
  },
}));

const { buildTurnFooter, updateTurnFooter, earnsTurnFooter, turnFacts } =
  await import("./turn-footer.js");
const { setWorkspaceRoot, _resetForTest: resetWorkspaceRoot } = await import("../workspace.js");

function line(el: HTMLElement): string {
  return el.querySelector(".turn-ledger-text")?.textContent ?? "";
}

/** The fact slot inside the trigger: the row's lead fact, `turnFacts(d)[0]`. */
function factEl(el: HTMLElement): HTMLElement {
  const f = el.querySelector<HTMLElement>(":scope > .turn-ledger-summary > .turn-fact");
  if (f === null) {
    throw new Error("no .turn-fact");
  }
  return f;
}

function fact(el: HTMLElement): string {
  return factEl(el).textContent;
}

function summary(el: HTMLElement): HTMLButtonElement {
  const b = el.querySelector<HTMLButtonElement>(".turn-ledger-summary");
  if (b === null) {
    throw new Error("no .turn-ledger-summary");
  }
  return b;
}

function panel(el: HTMLElement): HTMLElement {
  const p = el.querySelector<HTMLElement>(":scope > .turn-info-panel");
  if (p === null) {
    throw new Error("no .turn-info-panel");
  }
  return p;
}

/** The panel's section headings in DOM order — which is the order a reader meets
 *  them AND the withholding rule's own observable, since a withheld section is an
 *  absent heading rather than an empty one. */
function sections(el: HTMLElement): string[] {
  return [...panel(el).querySelectorAll(".turn-info-title")].map((h) => h.textContent ?? "");
}

/** One section's rows as `[label, value]` pairs; `[]` for an absent section and for one with no
 *  labelled rows, so withholding cases assert on `sections()`. */
function sectionRows(el: HTMLElement, title: string): [string, string][] {
  for (const s of panel(el).querySelectorAll(".turn-info-section")) {
    if ((s.querySelector(".turn-info-title")?.textContent ?? "") !== title) {
      continue;
    }
    return [...s.querySelectorAll(".turn-info-row")].map((r) => [
      r.querySelector(".turn-info-label")?.textContent ?? "",
      r.querySelector(".turn-info-value")?.textContent ?? "",
    ]);
  }
  return [];
}

function rows(el: HTMLElement): HTMLButtonElement[] {
  return [...el.querySelectorAll<HTMLButtonElement>(".turn-file-row")];
}

/** What the trigger's COMPUTED accessible name carries, as substrings (the numbers move).
 *  `40-a11y.css` must be mounted: `.sr-only`'s `position: absolute` is what makes Chromium insert
 *  the separating space. */
async function expectNameCarries(footer: HTMLElement, parts: string[]): Promise<void> {
  document.body.replaceChildren(footer);
  for (const part of parts) {
    await expect
      .element(page.getByRole("button", { name: part, exact: false }))
      .toBeInTheDocument();
  }
}

const TWO_FILES = {
  "b.ts": { lines_added: 1, lines_removed: 0 },
  "a.ts": { lines_added: 5, lines_removed: 2 },
};

describe("the ledger row", () => {
  it("says EXACTLY the outcome's lead word, for all eight outcomes", () => {
    // TOTAL over `TurnOutcome` and asserted as equality: the numbers live in the panel. `completed`
    // and `running` say NOTHING, asserted against a turn with credits and commands.
    for (const [outcome, word] of [
      ["running", ""],
      ["completed", ""],
      ["cancelled", "Cancelled"],
      ["interrupted", "Interrupted"],
      ["failed", "Failed"],
      ["refused", "Refused"],
      ["unknown", "Outcome unknown"],
      ["empty", "Empty"],
    ] as const) {
      const el = buildTurnFooter({
        outcome,
        credits: 1.5,
        elapsedMs: 92000,
        models: ["sonnet-4"],
        changedFiles: TWO_FILES,
      });
      expect(line(el), `${outcome} says only its lead word`).toBe(word);
    }
  });

  it("defaults the KEY rather than the value when the turn carries no outcome", () => {
    expect(line(buildTurnFooter({ credits: 1 }))).toBe("");
  });

  it("recomputes in place", () => {
    const el = buildTurnFooter({ outcome: "failed", credits: 0.5 });
    expect(line(el)).toBe("Failed");
    updateTurnFooter(el, { outcome: "cancelled", credits: 1, elapsedMs: 3000 });
    expect(line(el)).toBe("Cancelled");
    expect(turnFacts({ outcome: "cancelled", credits: 1, elapsedMs: 3000 })).toContain("3.0s");
    expect(fact(el)).toBe("3.0s");
    expect(sectionRows(el, "Cost")).toEqual([["Credits", "1.00"]]);
  });
});

// The turn's facts, most important first; the row paints the LEAD, so the order is asserted
// exactly. `earnsTurnFooter` reads the same list.

describe("the turn's facts", () => {
  it("ranks files, commands, delegates, the other kinds, the clock, the credits", () => {
    expect(
      turnFacts({
        changedFiles: TWO_FILES,
        kindCounts: { read: 12, execute: 3, edit: 3 },
        delegateCount: 2,
        elapsedMs: 92000,
        credits: 1.5,
      }),
    ).toEqual([
      "2 files +6 \u22122",
      "3 commands",
      "2 delegates",
      "12 reads",
      "3 edits",
      "1m 32s",
      "1.50 credits",
    ]);
  });

  it("skips every zero or absent value", () => {
    expect(turnFacts({})).toEqual([]);
    expect(
      turnFacts({ credits: 0, elapsedMs: 0, kindCounts: { read: 0 }, changedFiles: {} }),
    ).toEqual([]);
    // A turn with no edits leads with its commands.
    expect(turnFacts({ kindCounts: { execute: 5 }, elapsedMs: 1000 })).toEqual([
      "5 commands",
      "1.0s",
    ]);
  });

  it("bundles the files with their summed line deltas, and omits a zero side", () => {
    expect(turnFacts({ changedFiles: { "a.ts": { lines_added: 5, lines_removed: 0 } } })).toEqual([
      "1 file +5",
    ]);
    expect(turnFacts({ changedFiles: { "a.ts": { lines_added: 0, lines_removed: 3 } } })).toEqual([
      "1 file \u22123",
    ]);
    // A rename-only turn: two files, no line deltas, so the count alone.
    expect(
      turnFacts({
        changedFiles: {
          "a.ts": { lines_added: 0, lines_removed: 0 },
          "b.ts": { lines_added: 0, lines_removed: 0 },
        },
      }),
    ).toEqual(["2 files"]);
  });

  it("names one call and one delegate in the singular", () => {
    expect(turnFacts({ kindCounts: { execute: 1 }, delegateCount: 1 })).toEqual([
      "1 command",
      "1 delegate",
    ]);
  });

  it("hoists every command kind ahead of a higher read count", () => {
    // `shell` and `command` count as commands too, through the shared noun table.
    expect(turnFacts({ kindCounts: { read: 40, shell: 2, command: 1 } })).toEqual([
      "2 shell commands",
      "1 command",
      "40 reads",
    ]);
  });

  it("spells the wall clock the way the panel does", () => {
    // The thresholds are `formatElapsed`'s and did not move: a tenth of a second
    // below a minute, whole seconds above it, floored rather than rounded.
    expect(turnFacts({ elapsedMs: 45500 })).toEqual(["45.5s"]);
    expect(turnFacts({ elapsedMs: 90000 })).toEqual(["1m 30s"]);
    expect(turnFacts({ elapsedMs: 119999 })).toEqual(["1m 59s"]);
    expect(turnFacts({ elapsedMs: 7_200_000 })).toEqual(["2h 0m"]);
  });

  it("shows the first fact in the slot as soon as the footer is built", () => {
    const el = buildTurnFooter({ changedFiles: TWO_FILES, kindCounts: { execute: 3 } });
    expect(fact(el)).toBe("2 files +6 \u22122");
    expect(factEl(el).hidden).toBe(false);
  });

  it("hides the slot when there is nothing to say", () => {
    // A cancel that beat the usage stamp: the footer is earned by the outcome word
    // alone and the slot makes no claim rather than showing an empty box.
    const el = buildTurnFooter({ outcome: "cancelled" });
    expect(fact(el)).toBe("");
    expect(factEl(el).hidden).toBe(true);
  });

  it("clears the slot when a repaint drops every fact, and refills it later", () => {
    // The reachable direction: `updateTurnFooter` runs on every paint and a turn's
    // duration is stamped at turn end, so the footer exists before the value does.
    const el = buildTurnFooter({ elapsedMs: 3000 });
    expect(fact(el)).toBe("3.0s");
    updateTurnFooter(el, { outcome: "cancelled" });
    expect(fact(el)).toBe("");
    expect(factEl(el).hidden).toBe(true);
    updateTurnFooter(el, { outcome: "cancelled", kindCounts: { execute: 2 }, elapsedMs: 3000 });
    expect(fact(el)).toBe("2 commands");
    expect(factEl(el).hidden).toBe(false);
  });

  it("follows the new list's LEAD when a repaint re-ranks it", () => {
    // A live turn's list grows as its tool calls land, and a file changed later
    // outranks the commands that were leading — so the slot has to re-read `[0]`
    // rather than keep whatever it first painted.
    const el = buildTurnFooter({ kindCounts: { execute: 2 }, elapsedMs: 3000 });
    expect(fact(el)).toBe("2 commands");
    updateTurnFooter(el, { changedFiles: TWO_FILES, kindCounts: { execute: 2 }, elapsedMs: 3000 });
    expect(fact(el)).toBe("2 files +6 \u22122");
  });

  it("sits INSIDE the trigger and JOINS its name", async () => {
    // One control over the `i`, the outcome word and this label; NO `aria-hidden`, so a screen reader
    // hears the readout a sighted reader sees.
    const el = buildTurnFooter({ elapsedMs: 1000, kindCounts: { execute: 2 } });
    expect(el.querySelector(":scope > .turn-fact")).toBeNull();
    const inside = el.querySelector<HTMLElement>(":scope > .turn-ledger-summary > .turn-fact");
    expect(inside).not.toBeNull();
    expect(inside?.hasAttribute("aria-hidden")).toBe(false);
    await expectNameCarries(el, ["Turn details", fact(el)]);
  });
});

// ---------------------------------------------------------------------------
// The trigger. Always a disclosure, named by its CONTENT.
// ---------------------------------------------------------------------------

describe("the trigger", () => {
  // Only `.sr-only` matters here, so this is the one slice rather than the whole
  // bundle — see `expectName` for why the rule is load-bearing on the NAME.
  let a11y: HTMLStyleElement;

  beforeAll(() => {
    a11y = document.createElement("style");
    a11y.textContent = loadCSS("40-a11y.css");
    document.head.appendChild(a11y);
  });

  afterAll(() => {
    a11y.remove();
  });

  it("is a disclosure on every footer, however little the turn did", () => {
    // Every footer has a panel, so the trigger is never `disabled` and `aria-expanded` is always written.
    const el = buildTurnFooter({ credits: 1, elapsedMs: 1000 });
    expect(rows(el)).toHaveLength(0);
    expect(summary(el).disabled).toBe(false);
    expect(summary(el).getAttribute("aria-expanded")).toBe("false");
  });

  it("opens and closes the panel", () => {
    const el = buildTurnFooter({ credits: 1 });
    expect(el.dataset["info"]).toBeUndefined();
    summary(el).click();
    expect(el.dataset["info"]).toBe("open");
    expect(summary(el).getAttribute("aria-expanded")).toBe("true");
    summary(el).click();
    expect(el.dataset["info"]).toBeUndefined();
    expect(summary(el).getAttribute("aria-expanded")).toBe("false");
  });

  it("keeps an open panel open across a repaint that empties the ledger", () => {
    // A repaint dropping the turn's last file must not shut the disclosure under whoever
    // opened it: the panel still has five other sections to state.
    const el = buildTurnFooter({ changedFiles: TWO_FILES, elapsedMs: 1000 });
    summary(el).click();
    updateTurnFooter(el, { elapsedMs: 1000 });
    expect(el.dataset["info"]).toBe("open");
    expect(summary(el).getAttribute("aria-expanded")).toBe("true");
  });

  it("names the PURPOSE and the row's readout on a clean turn", async () => {
    // Without the `.sr-only` span a clean turn's trigger has NO accessible name: its text is empty
    // and the caret, `i` and glyph are decorative.
    const el = buildTurnFooter({
      outcome: "completed",
      credits: 1,
      changedFiles: TWO_FILES,
      kindCounts: { execute: 2 },
    });
    expect(summary(el).hasAttribute("aria-label")).toBe(false);
    expect(fact(el)).toBe("2 files +6 \u22122");
    await expectNameCarries(el, ["Turn details", "2 files +6 \u22122"]);
  });

  it("still carries the outcome word in the name of a turn that ended badly", async () => {
    // Why there is no `aria-label`: one would WIN over the button's own text and hide
    // this word AND the fact, which is the exact defect `OUTCOME_LEAD` exists to fix.
    const el = buildTurnFooter({
      outcome: "cancelled",
      credits: 1,
      changedFiles: TWO_FILES,
      kindCounts: { execute: 2 },
    });
    await expectNameCarries(el, ["Turn details", "Cancelled", "2 files +6 \u22122"]);
  });

  it("carries the `i` glyph that says the row is a door", () => {
    const el = buildTurnFooter({ outcome: "completed", credits: 1 });
    expect(summary(el).querySelector(".turn-ledger-info svg")).not.toBeNull();
  });
});

// ---------------------------------------------------------------------------
// The info panel: six sections, each withheld when it has nothing to state.
// ---------------------------------------------------------------------------

describe("the info panel", () => {
  // The panel is never empty on the production path; through `turnLedger`, since a BARE
  // `{outcome}` compiles yet is a shape nothing builds.
  it("is never empty for a turn built the way production builds one", () => {
    const entries: Entry[] = [
      {
        id: "t1-e0",
        turn: "t1",
        lane: "",
        kind: "turn_open",
        seq: 0,
        ts: 1_700_000_000_000,
        payload: { prompt: { id: "u1", text: "hi" }, source: "prompt", n: 1 },
      },
      {
        id: "t1-e1",
        turn: "t1",
        lane: "",
        kind: "turn_close",
        seq: 1,
        ts: 1_700_000_000_500,
        payload: { outcome: "cancelled" },
      },
    ];
    const state: TurnState = { entries, openEntries: new Map() };
    const turn = projectTurns({ turns: new Map([["t1", state]]), turn_order: ["t1"] })[0];
    expect(turn).toBeDefined();
    const led = turnLedger(turn as Turn);
    const footer = buildTurnFooter({ ...led, outcome: (turn as Turn).outcome });
    const infoRows = footer.querySelectorAll(".turn-info-row").length;
    expect(infoRows, "the cancelled turn that carries nothing else").toBeGreaterThan(0);
  });

  it("withholds every section on a turn with nothing to state", () => {
    // A cancel that beat the usage stamp: the footer is still earned (the reader
    // needs to know the turn was cancelled) and the panel has no fact to show, so it
    // renders no headings rather than six empty ones.
    const el = buildTurnFooter({ outcome: "cancelled" });
    expect(sections(el)).toEqual([]);
  });

  it("orders its sections timings, work, delegates, cost, model, diagnostics", () => {
    const el = buildTurnFooter({
      outcome: "failed",
      elapsedMs: 92000,
      toolMs: 30000,
      changedFiles: TWO_FILES,
      kindCounts: { read: 2 },
      delegateCount: 1,
      delegateMs: 9000,
      credits: 1.5,
      models: ["sonnet-4"],
      stopReasonRaw: "max_tokens",
      truncated: true,
    });
    expect(sections(el)).toEqual(["Timings", "Work", "Delegates", "Cost", "Model", "Diagnostics"]);
  });

  it("states the wall clock, the tool time, and the model time between them", () => {
    const el = buildTurnFooter({ elapsedMs: 92000, toolMs: 30000 });
    expect(sectionRows(el, "Timings")).toEqual([
      ["Wall clock", "1m 32s"],
      ["Tool time", "30.0s"],
      ["Model time", "1m 2s"],
    ]);
  });

  it("withholds model time when the tool calls overran the wall clock", () => {
    // Tool calls overlap, so Σ `duration_ms` can exceed the wall clock: the panel withholds rather
    // than clamping. Both the greater and the equal boundary.
    expect(sectionRows(buildTurnFooter({ elapsedMs: 30000, toolMs: 45000 }), "Timings")).toEqual([
      ["Wall clock", "30.0s"],
      ["Tool time", "45.0s"],
    ]);
    expect(sectionRows(buildTurnFooter({ elapsedMs: 30000, toolMs: 30000 }), "Timings")).toEqual([
      ["Wall clock", "30.0s"],
      ["Tool time", "30.0s"],
    ]);
  });

  it("withholds model time when nothing measured any tool time", () => {
    // With no tool time there is nothing to subtract, so the row would restate the
    // wall clock verbatim under a second name.
    expect(sectionRows(buildTurnFooter({ elapsedMs: 30000, toolMs: 0 }), "Timings")).toEqual([
      ["Wall clock", "30.0s"],
    ]);
    expect(sectionRows(buildTurnFooter({ elapsedMs: 30000 }), "Timings")).toEqual([
      ["Wall clock", "30.0s"],
    ]);
  });

  it("renders the start and the end as machine-readable stamps", () => {
    const started = Date.UTC(2026, 0, 2, 9, 5, 0);
    const ended = started + 92000;
    const el = buildTurnFooter({ startedAt: started, endedAt: ended });
    expect(sectionRows(el, "Timings").map(([label]) => label)).toEqual(["Started", "Ended"]);
    const stamps = [...panel(el).querySelectorAll<HTMLTimeElement>("time")];
    expect(stamps).toHaveLength(2);
    expect(stamps[0]?.dateTime).toBe("2026-01-02T09:05:00.000Z");
    expect(stamps[1]?.dateTime).toBe("2026-01-02T09:06:32.000Z");
    // The reader's locale and 12h/24h preference decide the text, so it is pinned as a shape.
    expect(stamps[0]?.textContent).toMatch(/\d{1,2}:\d{2}/u);
  });

  it("withholds a stamp nobody made", () => {
    // A running delegate has a start and no end, which is the reachable half.
    const el = buildTurnFooter({ startedAt: 1000 });
    expect(sectionRows(el, "Timings")).toHaveLength(1);
    expect(
      sectionRows(buildTurnFooter({ endedAt: 0, startedAt: 0, elapsedMs: 1000 }), "Timings"),
    ).toEqual([["Wall clock", "1.0s"]]);
  });

  it("withholds Ended while the turn is still running", () => {
    // A live turn's `endedAt` is the last body message's `ts` (`turnLedger`), which reads like Started
    // at minute precision, so the section states Started alone and the Ended row is ABSENT.
    const started = Date.UTC(2026, 0, 2, 9, 5, 0);
    const el = buildTurnFooter({ outcome: "running", startedAt: started, endedAt: started + 40 });
    expect(sectionRows(el, "Timings").map(([label]) => label)).toEqual(["Started"]);
    expect(panel(el).querySelectorAll("time")).toHaveLength(1);
    // And it comes back on the settle, in place: the withholding is a property of the
    // OUTCOME rather than of this footer instance.
    updateTurnFooter(el, { outcome: "completed", startedAt: started, endedAt: started + 92000 });
    expect(sectionRows(el, "Timings").map(([label]) => label)).toEqual(["Started", "Ended"]);
  });

  it("puts the file rows and one row per tool kind under Work", () => {
    // Sorted by count then kind, so repaints cannot reshuffle the arrival-ordered `kindCounts`.
    const el = buildTurnFooter({
      changedFiles: TWO_FILES,
      kindCounts: { read: 12, execute: 3, edit: 3 },
    });
    expect(sections(el)).toEqual(["Work"]);
    expect(sectionRows(el, "Work")).toEqual([
      ["reads", "12"],
      ["edits", "3"],
      ["commands", "3"],
    ]);
    // The file `<ul>` is nested in this section, so the rows are still built and the
    // Review-changes row still lands under them.
    expect(panel(el).querySelector(".turn-info-section .turn-ledger-files")).not.toBeNull();
    expect(rows(el)).toHaveLength(2);
  });

  it("names a single call in the singular, through the shared kind vocabulary", () => {
    // `tool-kind-noun.ts` is the one owner of these words, read here and by a tool
    // group's mixed summary, so the two surfaces cannot call one kind two things.
    expect(sectionRows(buildTurnFooter({ kindCounts: { read: 1, mcp: 1 } }), "Work")).toEqual([
      ["integration call", "1"],
      ["read", "1"],
    ]);
  });

  it("withholds Work on a turn that touched no file and ran no tool", () => {
    expect(sections(buildTurnFooter({ elapsedMs: 1000, kindCounts: {} }))).toEqual(["Timings"]);
  });

  it("counts the delegates the turn dispatched, and their time", () => {
    expect(
      sectionRows(buildTurnFooter({ delegateCount: 2, delegateMs: 9000 }), "Delegates"),
    ).toEqual([
      ["Dispatched", "2"],
      ["Time", "9.0s"],
    ]);
  });

  it("withholds Delegates on a turn that dispatched none", () => {
    const el = buildTurnFooter({ elapsedMs: 1000, delegateCount: 0, delegateMs: 0 });
    expect(sections(el)).toEqual(["Timings"]);
  });

  it("states the credits to two places", () => {
    expect(sectionRows(buildTurnFooter({ credits: 1.5 }), "Cost")).toEqual([["Credits", "1.50"]]);
    expect(sectionRows(buildTurnFooter({ credits: 0.5 }), "Cost")).toEqual([["Credits", "0.50"]]);
  });

  it("withholds Cost on a turn nothing metered", () => {
    expect(sections(buildTurnFooter({ elapsedMs: 1000, credits: 0 }))).toEqual(["Timings"]);
  });

  it("names the model, and both models with an arrow on a mid-turn switch", () => {
    expect(sectionRows(buildTurnFooter({ models: ["sonnet-4"] }), "Model")).toEqual([
      ["Answered by", "sonnet-4"],
    ]);
    expect(sectionRows(buildTurnFooter({ models: ["sonnet-4", "opus-4"] }), "Model")).toEqual([
      ["Answered by", "sonnet-4 \u2192 opus-4"],
    ]);
  });

  it("withholds Model rather than inventing one", () => {
    // Absent on every turn persisted before the field existed, so this is the common
    // case rather than an edge.
    expect(sections(buildTurnFooter({ elapsedMs: 1000 }))).toEqual(["Timings"]);
    expect(sections(buildTurnFooter({ elapsedMs: 1000, models: [] }))).toEqual(["Timings"]);
  });

  it("states the raw stop reason and the truncation on a turn that ended badly", () => {
    // VERBATIM, and nothing branches on it: the wire declares that enum OPEN, so
    // `outcome` is what any decision reads.
    const el = buildTurnFooter({
      outcome: "failed",
      stopReasonRaw: "max_tokens",
      truncated: true,
    });
    expect(sectionRows(el, "Diagnostics")).toEqual([
      ["Stop reason", "max_tokens"],
      ["Truncated", "yes"],
    ]);
  });

  it("withholds Diagnostics on a clean turn and on one still running", () => {
    // On a clean turn the stop reason is the ordinary one, so the section would be a
    // heading over the absence of news; a running turn has not ended at all, so it
    // has no verdict to diagnose.
    for (const outcome of ["completed", "running"] as const) {
      const el = buildTurnFooter({
        outcome,
        elapsedMs: 1000,
        stopReasonRaw: "max_tokens",
        truncated: true,
      });
      expect(sections(el), `${outcome} has no diagnostics`).toEqual(["Timings"]);
    }
  });

  it("rebuilds its sections in place rather than stacking them", () => {
    const el = buildTurnFooter({ credits: 1 });
    updateTurnFooter(el, { credits: 1, elapsedMs: 1000 });
    updateTurnFooter(el, { credits: 1, elapsedMs: 1000 });
    expect(sections(el)).toEqual(["Timings", "Cost"]);
    expect(panel(el).querySelectorAll(".turn-info-section")).toHaveLength(2);
  });
});

describe("the per-file rows", () => {
  it("renders one row per file with its own line counts", () => {
    const el = buildTurnFooter({ changedFiles: TWO_FILES });
    const r = rows(el);
    expect(r).toHaveLength(2);
    // Sorted by path, so a repaint cannot reshuffle rows under the cursor.
    expect(r[0]?.querySelector(".turn-file-path")?.textContent).toBe("a.ts");
    expect(r[0]?.querySelector(".turn-file-add")?.textContent).toBe("+5");
    expect(r[0]?.querySelector(".turn-file-del")?.textContent).toBe("\u22122");
    expect(r[1]?.querySelector(".turn-file-path")?.textContent).toBe("b.ts");
    // No deletions on b.ts, so no deletion span at all rather than a "−0".
    expect(r[1]?.querySelector(".turn-file-del")).toBeNull();
  });

  it("badges a file the turn created", () => {
    const el = buildTurnFooter({
      changedFiles: {
        "new.ts": { lines_added: 9, lines_removed: 0, is_new_file: true },
        "old.ts": { lines_added: 1, lines_removed: 1 },
      },
    });
    const r = rows(el);
    expect(r[0]?.querySelector(".turn-file-badge")?.textContent).toBe("new");
    expect(r[1]?.querySelector(".turn-file-badge")).toBeNull();
  });

  // Every footer-earning turn has a panel (see `updateTurnFooter`'s `aria-expanded` write), so
  // `summary.disabled` is never set and asserting on it would prove nothing.
  it("keeps its rows across a repaint that leaves the files alone", () => {
    const el = buildTurnFooter({ changedFiles: TWO_FILES });
    summary(el).click();
    updateTurnFooter(el, { changedFiles: TWO_FILES, credits: 2 });
    expect(el.dataset["info"]).toBe("open");
    expect(rows(el)).toHaveLength(2);
  });

  it("drops its rows when a repaint drops the files", () => {
    const el = buildTurnFooter({ changedFiles: TWO_FILES, elapsedMs: 1000 });
    updateTurnFooter(el, { elapsedMs: 1000 });
    expect(rows(el)).toHaveLength(0);
    expect(sections(el)).toEqual(["Timings"]);
  });
});

describe("the click target", () => {
  it("opens the clicked file's diff", () => {
    openFileGitDiff.mockClear();
    const el = buildTurnFooter({ changedFiles: TWO_FILES });
    rows(el)[1]?.click();
    expect(openFileGitDiff).toHaveBeenCalledWith("b.ts");
  });

  it("names the file in each row's hover text so the target is never a mystery", () => {
    // `data-tooltip`, not `title`: the styled tooltip system is what every other
    // hover in the app uses, and a UA tooltip beside it reads as foreign chrome.
    const el = buildTurnFooter({ changedFiles: TWO_FILES });
    expect(rows(el)[0]?.getAttribute("data-tooltip")).toBe("Open the diff for a.ts");
  });
});

describe("earnsTurnFooter", () => {
  it("is false for an empty / zero summary", () => {
    expect(earnsTurnFooter({})).toBe(false);
    expect(earnsTurnFooter({ credits: 0, elapsedMs: 0 })).toBe(false);
    expect(earnsTurnFooter({ changedFiles: {} })).toBe(false);
  });

  it("is true when any ledger dimension is present", () => {
    expect(earnsTurnFooter({ credits: 0.1 })).toBe(true);
    expect(earnsTurnFooter({ elapsedMs: 1 })).toBe(true);
    expect(
      earnsTurnFooter({ changedFiles: { "a.ts": { lines_added: 1, lines_removed: 0 } } }),
    ).toBe(true);
  });

  it("is NOT made true by a kind map that counts nothing", () => {
    // What a real turn carries: the counter AND its kind map, which earns and paints.
    expect(earnsTurnFooter({ kindCounts: { execute: 1 } })).toBe(true);
    expect(earnsTurnFooter({ kindCounts: { read: 7 } })).toBe(true);
  });

  // The files are on disk regardless, and a cancel is exactly when a reader
  // needs to know what landed — so a non-clean outcome earns a footer even when
  // the cancel beat the usage stamp and there are no numbers to show.
  it("is true for a non-clean outcome with nothing else to show", () => {
    expect(earnsTurnFooter({ outcome: "interrupted" })).toBe(true);
    expect(earnsTurnFooter({ outcome: "failed" })).toBe(true);
  });

  it("is not made true by a clean or running outcome alone", () => {
    expect(earnsTurnFooter({ outcome: "completed" })).toBe(false);
    expect(earnsTurnFooter({ outcome: "running" })).toBe(false);
  });

  // NOT widened for the model: every completed turn has one, so it would put a footer on every
  // turn. The model rides a footer that already earned its place.
  it("is not made true by a model alone", () => {
    expect(earnsTurnFooter({ models: ["sonnet-4"] })).toBe(false);
    expect(earnsTurnFooter({ models: ["sonnet-4"], outcome: "completed" })).toBe(false);
  });

  // The fields `turnFacts` emits nothing for: no fact, so no lead, so no footer.
  it("is not made true by a panel field the row cannot paint", () => {
    expect(
      earnsTurnFooter({
        toolMs: 9000,
        delegateMs: 9000,
        startedAt: 1000,
        endedAt: 9000,
        stopReasonRaw: "max_tokens",
        truncated: true,
      }),
    ).toBe(false);
  });

  it("IS made true by a kind count or a delegate count, which the row does paint", () => {
    // The gate and the projection agreeing: each of these emits a fact, so each paints.
    expect(earnsTurnFooter({ kindCounts: { search: 3 } })).toBe(true);
    expect(turnFacts({ kindCounts: { search: 3 } })).toEqual(["3 searches"]);
    expect(earnsTurnFooter({ delegateCount: 1 })).toBe(true);
    expect(turnFacts({ delegateCount: 1 })).toEqual(["1 delegate"]);
  });

  it("still shows the model on a footer something else earned", () => {
    expect(earnsTurnFooter({ models: ["sonnet-4"], credits: 0.1 })).toBe(true);
    expect(sectionRows(buildTurnFooter({ models: ["sonnet-4"], credits: 0.1 }), "Model")).toEqual([
      ["Answered by", "sonnet-4"],
    ]);
  });

  // The footer also carries the turn ACTIONS and Rewind, so an unstamped ledger must not cost the
  // buttons. Parameters, since a delegate card passes neither.
  it("is true for a rewind target with no ledger at all", () => {
    expect(earnsTurnFooter({}, { rewindable: true })).toBe(true);
  });

  it("is true when there is settled prose for the turn actions to act on", () => {
    expect(earnsTurnFooter({}, { settledProse: true })).toBe(true);
  });

  it("is false when both extra reasons are explicitly absent", () => {
    expect(earnsTurnFooter({}, { rewindable: false, settledProse: false })).toBe(false);
  });
});

// ---------------------------------------------------------------------------
// `Review changes` — the multi-file seam
// ---------------------------------------------------------------------------

describe("Review changes", () => {
  it("is absent on a single-file turn, where the row above already opens it", () => {
    const footer = buildTurnFooter({
      outcome: "completed",
      changedFiles: { "a.ts": { lines_added: 1, lines_removed: 0 } },
    });
    document.body.replaceChildren(footer);
    summary(footer).click();
    expect(footer.querySelector(".turn-review-all")).toBeNull();
  });

  it("appears once for a multi-file turn and names the count", () => {
    const footer = buildTurnFooter({
      outcome: "completed",
      changedFiles: {
        "a.ts": { lines_added: 1, lines_removed: 0 },
        "b.ts": { lines_added: 2, lines_removed: 1 },
      },
    });
    document.body.replaceChildren(footer);
    summary(footer).click();
    const all = footer.querySelectorAll(".turn-review-all");
    expect(all).toHaveLength(1);
    expect(all[0]?.textContent).toContain("2 files");
  });
});

// The hover text says only what the click does, through the app's styled tooltip attribute.

describe("the trigger's hover text", () => {
  function tip(footer: HTMLElement): string | null {
    return summary(footer).getAttribute("data-tooltip");
  }

  it("is never a native title", () => {
    const footer = buildTurnFooter({
      outcome: "completed",
      changedFiles: { "a.ts": { lines_added: 3, lines_removed: 1 } },
    });
    expect(summary(footer).hasAttribute("title")).toBe(false);
  });

  it("names the disclosure action, and tracks the open state", () => {
    const footer = buildTurnFooter({ outcome: "completed", elapsedMs: 1000 });
    document.body.replaceChildren(footer);
    expect(tip(footer)).toBe("Show turn details");
    summary(footer).click();
    expect(tip(footer)).toBe("Hide turn details");
  });

  it("advertises the disclosure on a turn that changed no files too", () => {
    // Every footer discloses a panel, so a footer with no tooltip would be a door with
    // nothing saying so.
    expect(tip(buildTurnFooter({ outcome: "completed", kindCounts: { execute: 2 } }))).toBe(
      "Show turn details",
    );
  });

  it("carries NO outcome clause at all, because the row names every outcome now", () => {
    // With OUTCOME_LEAD total the row names every outcome, so the tooltip adds no explanation.
    for (const outcome of ["cancelled", "refused", "unknown", "failed", "interrupted"] as const) {
      const footer = buildTurnFooter({ outcome });
      expect(line(footer), `${outcome} names itself in the row`).not.toBe("");
      expect(tip(footer), `${outcome} adds no hover clause`).toBe("Show turn details");
    }
  });

  it("puts the styled tooltip on the file row too, not a native title", () => {
    const footer = buildTurnFooter({
      outcome: "completed",
      changedFiles: { "src/a.ts": { lines_added: 1, lines_removed: 0 } },
    });
    document.body.replaceChildren(footer);
    summary(footer).click();
    const row = footer.querySelector<HTMLElement>(".turn-file-row");
    expect(row?.hasAttribute("title")).toBe(false);
    expect(row?.getAttribute("data-tooltip")).toBe("Open the diff for src/a.ts");
  });
});

// THE PAINT INVARIANT: an earned footer paints something, one case per reason, through the real
// builders. The table is a `Record` over `keyof TurnSummaryData` (a new field needs a probe) and
// each `earns` is a LITERAL.

describe("every reason a footer is earned paints something", () => {
  let bundle: HTMLStyleElement;

  beforeAll(() => {
    // The assembled cascade: `.turn-ledger-text:empty` and the running glyph are hidden by CSS.
    bundle = mountAppCSS();
  });

  afterAll(() => {
    bundle.remove();
  });

  /** Whether one channel is on screen: not `hidden`, not `display: none` (own or ancestor), and
   *  carrying text. OPACITY is not read: the `@starting-style` entry transition starts at 0. */
  function shows(el: HTMLElement | null): boolean {
    if (el === null || el.hidden || !el.checkVisibility()) {
      return false;
    }
    return (el.textContent ?? "") !== "";
  }

  /** The two channels the FOOTER owns. The glyph carries no text and every glyphed outcome also gets
   *  a word, so the word stands for it. */
  function paints(footer: HTMLElement): boolean {
    document.body.replaceChildren(footer);
    return (
      shows(footer.querySelector<HTMLElement>(":scope > .turn-ledger-summary > .turn-fact")) ||
      shows(footer.querySelector<HTMLElement>(":scope > .turn-ledger-summary > .turn-ledger-text"))
    );
  }

  /** One single-field summary per `TurnSummaryData` field with the verdict the gate owes it.
   *  `outcome` probes an EARNING value; `completed`/`running` are covered by `earnsTurnFooter`'s block. */
  const PROBES: Record<
    keyof TurnSummaryData,
    { readonly d: TurnSummaryData; readonly earns: boolean }
  > = {
    changedFiles: {
      d: { changedFiles: { "a.ts": { lines_added: 1, lines_removed: 0 } } },
      earns: true,
    },
    kindCounts: { d: { kindCounts: { search: 3 } }, earns: true },
    delegateCount: { d: { delegateCount: 2 }, earns: true },
    elapsedMs: { d: { elapsedMs: 3000 }, earns: true },
    credits: { d: { credits: 0.5 }, earns: true },
    outcome: { d: { outcome: "failed" }, earns: true },
    // The aggregates carry no kind map, which no producer emits — see the
    // `earnsTurnFooter` block. Neither earns and neither paints.
    // Panel-only, and the row has no expression for any of them.
    toolMs: { d: { toolMs: 9000 }, earns: false },
    delegateMs: { d: { delegateMs: 9000 }, earns: false },
    startedAt: { d: { startedAt: 1_700_000_000_000 }, earns: false },
    endedAt: { d: { endedAt: 1_700_000_009_000 }, earns: false },
    stopReasonRaw: { d: { stopReasonRaw: "max_tokens" }, earns: false },
    truncated: { d: { truncated: true }, earns: false },
    models: { d: { models: ["sonnet-4"] }, earns: false },
    asks: { d: { asks: { allowed: 1 } }, earns: false },
    requestIds: { d: { requestIds: ["req-1"] }, earns: false },
    throughput: {
      d: { throughput: { estimated_tokens: 400, active_streaming_ms: 2000 } },
      earns: false,
    },
    recoveries: { d: { recoveries: ["empty"] }, earns: false },
    steering: { d: { steering: ["file:///workspace/.kiro/steering/a.md"] }, earns: false },
    engineErrorClass: { d: { engineErrorClass: "ModelOverloaded" }, earns: false },
    // Panel-only: a breakdown alone does not earn a footer.
    contextBreakdown: {
      d: {
        contextBreakdown: {
          total_chars: 900,
          model_calls: 1,
          categories: [{ key: "history", chars: 900, percent: 100 }],
        },
      },
      earns: false,
    },
  };

  it("holds for a footer BUILT from that reason", () => {
    for (const [field, { d, earns }] of Object.entries(PROBES)) {
      expect(earnsTurnFooter(d), `${field}: the gate's verdict`).toBe(earns);
      expect(paints(buildTurnFooter(d)), `${field}: earned ${String(earns)}, so paints`).toBe(
        earns,
      );
    }
  });

  it("holds for a footer REPAINTED into that reason", () => {
    // The live path: a running turn's footer is built before its numbers exist, so the row must
    // acquire its channel on a repaint.
    for (const [field, { d, earns }] of Object.entries(PROBES)) {
      const el = buildTurnFooter({});
      expect(paints(el), `${field}: an empty footer paints nothing to start with`).toBe(false);
      updateTurnFooter(el, d);
      expect(paints(el), `${field}: repainted into ${field}, so paints`).toBe(earns);
    }
  });

  it("earns a footer for each EXTRA, whose channel is a control this module never builds", () => {
    // The `Record` closes the population the same way `PROBES` does, so an extra added
    // later fails the type check until it has a row. Its CONTROL is asserted where the
    // caller mounts it: `messages-footer-extras.test.ts`.
    const extras: Record<keyof FooterExtras, FooterExtras> = {
      rewindable: { rewindable: true },
      settledProse: { settledProse: true },
    };
    for (const [name, extra] of Object.entries(extras)) {
      expect(earnsTurnFooter({}, extra), `${name} earns a footer`).toBe(true);
      expect(paints(buildTurnFooter({})), `${name}: the footer's own channels stay empty`).toBe(
        false,
      );
    }
  });
});

// `updateTurnFooter` runs on every paint of its turn card, so on a live turn it runs at
// chunk cadence, and the panel is full of real controls: each file row is a button with
// a tooltip that opens a diff.
describe("the info panel repaints only when its data moved", () => {
  /** Element-by-element IDENTITY: `toEqual` compares DOM nodes structurally and passes for a
   *  rebuilt row. */
  function sameElements(after: readonly Element[], before: readonly Element[]): void {
    expect(after).toHaveLength(before.length);
    for (const [i, el] of before.entries()) {
      expect(after[i], `element ${String(i)} was replaced`).toBe(el);
    }
  }

  const data = {
    elapsedMs: 4200,
    changedFiles: {
      "b.go": { lines_added: 3, lines_removed: 1 },
      "a.go": { lines_added: 5, lines_removed: 0 },
    },
  };

  it("keeps every file row across a repaint with the same summary", () => {
    const el = buildTurnFooter(data);
    // IN the document, or `focus()` is a no-op on a detached tree and the focus half
    // of this case passes for the wrong reason.
    document.body.appendChild(el);
    try {
      const before = rows(el);
      expect(before).toHaveLength(2);
      const first = before[0];
      first?.focus();
      expect(document.activeElement).toBe(first);

      updateTurnFooter(el, { ...data, changedFiles: { ...data.changedFiles } });

      // Identity, because content cannot tell a kept row from a rebuilt one — which is
      // exactly why this went unnoticed.
      sameElements(rows(el), before);
      expect(document.activeElement).toBe(first);
    } finally {
      el.remove();
    }
  });

  // A `Record`'s insertion order is the order the paths happened to arrive in, so an
  // unsorted signature would move for a set that did not change. The rows themselves
  // are sorted by path, so the signature is too.
  it("keeps them when the same paths arrive in a different order", () => {
    const el = buildTurnFooter(data);
    const before = rows(el);

    updateTurnFooter(el, {
      ...data,
      changedFiles: {
        "a.go": { lines_added: 5, lines_removed: 0 },
        "b.go": { lines_added: 3, lines_removed: 1 },
      },
    });

    sameElements(rows(el), before);
  });

  it("repaints when a file's line counts move", () => {
    const el = buildTurnFooter(data);
    const before = rows(el);

    updateTurnFooter(el, {
      ...data,
      changedFiles: { ...data.changedFiles, "a.go": { lines_added: 9, lines_removed: 0 } },
    });

    expect(rows(el)[0]).not.toBe(before[0]);
    expect(sectionRows(el, "Timings").length).toBeGreaterThan(0);
  });

  it("repaints when a file is added to the set", () => {
    const el = buildTurnFooter(data);
    expect(rows(el)).toHaveLength(2);

    updateTurnFooter(el, {
      ...data,
      changedFiles: { ...data.changedFiles, "c.go": { lines_added: 1, lines_removed: 0 } },
    });

    expect(rows(el)).toHaveLength(3);
  });

  // On the recorded SIGNATURE: `commands` and `reads` render in the fact slot, so a moved signature
  // costs them one identical repaint (the safe direction).
  it("moves its signature for every field of the summary, one at a time", () => {
    const probes: Record<keyof typeof FIELD_PROBES, Record<string, unknown>> = FIELD_PROBES;
    for (const [field, over] of Object.entries(probes)) {
      const el = buildTurnFooter(data);
      const before = panel(el).getAttribute("data-sig");
      expect(before, `${field}: the first paint recorded no signature`).not.toBeNull();

      updateTurnFooter(el, { ...data, ...over });

      expect(
        panel(el).getAttribute("data-sig"),
        `${field} moved and the panel's signature did not`,
      ).not.toBe(before);
    }
  });

  // The rendering half, over the fields the panel genuinely paints. Separate from the
  // case above so a failure names one thing: a stale signature and a section that
  // stopped rendering are different defects.
  it("repaints the rendered sections when their own fields move", () => {
    for (const field of RENDERED_FIELDS) {
      const el = buildTurnFooter(data);
      const before = panel(el).innerHTML;

      updateTurnFooter(el, { ...data, ...FIELD_PROBES[field] });

      expect(panel(el).innerHTML, `${field} moved and the panel did not repaint`).not.toBe(before);
    }
  });
});

/** One moved field each over the WHOLE summary type; `satisfies` keeps keys literal while a
 *  forgotten new field is a type error. */
const FIELD_PROBES = {
  elapsedMs: { elapsedMs: 9999 },
  credits: { credits: 1.5 },
  models: { models: ["opus", "sonnet"] },
  toolMs: { toolMs: 1200 },
  kindCounts: { kindCounts: { search: 3 } },
  delegateCount: { delegateCount: 2 },
  delegateMs: { delegateMs: 500 },
  startedAt: { startedAt: 1_700_000_000_000 },
  endedAt: { endedAt: 1_700_000_009_000 },
  outcome: { outcome: "failed" as const },
  stopReasonRaw: { outcome: "failed" as const, stopReasonRaw: "max_tokens" },
  truncated: { outcome: "failed" as const, truncated: true },
  changedFiles: { changedFiles: { "z.go": { lines_added: 1, lines_removed: 0 } } },
  asks: { asks: { rejected: 2 } },
  requestIds: { requestIds: ["req-1", "req-2"] },
  throughput: { throughput: { estimated_tokens: 900, active_streaming_ms: 3000 } },
  recoveries: { recoveries: ["streamError"] },
  steering: { steering: ["file:///workspace/.kiro/steering/b.md"] },
  engineErrorClass: { engineErrorClass: "ModelOverloaded" },
  contextBreakdown: {
    contextBreakdown: {
      total_chars: 900,
      model_calls: 1,
      categories: [{ key: "history", chars: 900, percent: 100 }],
    },
  },
} satisfies Record<keyof TurnSummaryData, Partial<TurnSummaryData>>;

/** The fields a single-field probe can observe in the panel: `commands` and `reads` render in the
 *  fact slot, and `outcome` alone only gates Diagnostics. */
const RENDERED_FIELDS = [
  "elapsedMs",
  "credits",
  "models",
  "toolMs",
  "kindCounts",
  "delegateCount",
  "delegateMs",
  "startedAt",
  "endedAt",
  "stopReasonRaw",
  "truncated",
  "changedFiles",
  "asks",
  "requestIds",
  "throughput",
  "recoveries",
  "steering",
  "engineErrorClass",
  "contextBreakdown",
] as const satisfies readonly (keyof typeof FIELD_PROBES)[];

describe("the panel's context breakdown", () => {
  const breakdown = {
    total_chars: 9000,
    model_calls: 2,
    compacted: true,
    media: { images: 1, image_bytes: 10, documents: 0, document_bytes: 0 },
    categories: [
      {
        key: "history",
        chars: 5000,
        percent: 55.6,
        parts: [
          { key: "assistant", chars: 3500 },
          { key: "user", chars: 1000 },
        ],
      },
      {
        key: "steering",
        chars: 3000,
        percent: 33.3,
        items: [{ name: "<b>go.md</b>", chars: 2000, percent: 22, inclusion: "fileMatch" }],
        omitted_count: 4,
        omitted_chars: 120,
      },
      { key: "futureCategory", chars: 1000, percent: 11.1 },
    ],
  };

  it("states the request's size and each category's share under Context, beside the steering added", () => {
    const el = buildTurnFooter({
      steering: ["file:///elsewhere/a.md"],
      contextBreakdown: breakdown,
    });
    // A disclosed category's value is its summary line; the items sit behind it.
    const rowsShown = [...panel(el).querySelectorAll(".turn-info-row")].map((r) => [
      r.querySelector(".turn-info-label")?.textContent ?? "",
      (r.querySelector(".turn-info-value summary") ?? r.querySelector(".turn-info-value"))
        ?.textContent ?? "",
    ]);
    expect(rowsShown).toEqual([
      ["Steering added", "a.md"],
      ["Last request", "9,000 characters over 2 model calls \u00b7 compacted \u00b7 1 image"],
      ["Conversation", "56% \u00b7 5,000 characters"],
      ["Steering", "33% \u00b7 3,000 characters"],
      ["futureCategory", "11% \u00b7 1,000 characters"],
    ]);
  });

  it("discloses a category's parts, top items and the rest it left out", () => {
    const el = buildTurnFooter({ contextBreakdown: breakdown });
    const details = [
      ...panel(el).querySelectorAll<HTMLDetailsElement>("details.ctx-breakdown-category"),
    ];
    expect(details).toHaveLength(2);
    expect(details[0]?.open).toBe(false);
    const lines = (d: HTMLDetailsElement | undefined): string[] =>
      [...(d?.querySelectorAll(".ctx-breakdown-items > li") ?? [])].map(
        (li) => li.textContent ?? "",
      );
    expect(lines(details[0])).toEqual(["replies 3,500 \u00b7 your messages 1,000"]);
    expect(lines(details[1])).toEqual(["<b>go.md</b>fileMatch22%", "4 more, 120 characters"]);
    expect(details[1]?.querySelector("b")).toBeNull();
  });

  it("withholds the rows on a turn KAS measured nothing for", () => {
    expect(sections(buildTurnFooter({ elapsedMs: 1000 }))).not.toContain("Context");
  });
});

describe("the panel states the turn's KAS facts", () => {
  it("tallies the turn's asks in a fixed order, and withholds the section without one", () => {
    const el = buildTurnFooter({ asks: { rejected: 1, allowed: 2, skipped: 1 } });
    expect(sectionRows(el, "Approvals")).toEqual([
      ["Allowed", "2"],
      ["Rejected", "1"],
      ["Skipped", "1"],
    ]);
    expect(sections(buildTurnFooter({ elapsedMs: 1000 }))).not.toContain("Approvals");
  });

  it("states the request count and the output estimate under Model", () => {
    const el = buildTurnFooter({
      requestIds: ["a", "b", "c"],
      throughput: { estimated_tokens: 400, active_streaming_ms: 2000 },
    });
    expect(sectionRows(el, "Model")).toEqual([
      ["Requests", "3"],
      ["Output, estimated", "\u2248400 tokens \u00b7 \u2248200 tokens/s"],
    ]);
  });

  it("withholds the rate when no streaming time was measured", () => {
    const el = buildTurnFooter({ throughput: { estimated_tokens: 50, active_streaming_ms: 0 } });
    expect(sectionRows(el, "Model")).toEqual([["Output, estimated", "\u224850 tokens"]]);
  });

  it("states recoveries, the engine class and the request ids on a CLEAN turn", () => {
    const el = buildTurnFooter({
      outcome: "completed",
      recoveries: ["empty", "constructor", "newMechanism"],
      engineErrorClass: "ModelOverloaded",
      requestIds: ["req-1", "req-2"],
    });
    expect(sectionRows(el, "Diagnostics")).toEqual([
      ["Recovered", "retried an empty response"],
      ["Recovered", "constructor"],
      ["Recovered", "newMechanism"],
      ["Engine error", "ModelOverloaded"],
      ["Request IDs", "req-1req-2Copy all"],
    ]);
  });

  it("copies every request id with one control", () => {
    copied.mockClear();
    const el = buildTurnFooter({ requestIds: ["req-1", "req-2"] });
    el.querySelector<HTMLButtonElement>(".turn-info-copy")?.click();
    expect(copied).toHaveBeenCalledWith("req-1\nreq-2");
  });

  describe("steering added", () => {
    afterAll(() => {
      resetWorkspaceRoot();
    });

    it("links a workspace steering document into the editor and names any other id", () => {
      setWorkspaceRoot("/workspace");
      openFile.mockClear();
      const el = buildTurnFooter({
        steering: ["file:///workspace/.kiro/steering/my%20doc.md", "builtin-guide"],
      });
      expect(sectionRows(el, "Context")).toEqual([["Steering added", "my doc.mdbuiltin-guide"]]);
      const link = el.querySelector<HTMLButtonElement>(".turn-info-link");
      expect(link?.dataset["tooltip"]).toBe("/workspace/.kiro/steering/my doc.md");
      link?.click();
      expect(openFile).toHaveBeenCalledWith("/workspace/.kiro/steering/my doc.md", undefined);
    });

    it("names a global steering document as text, because the editor cannot open it", () => {
      setWorkspaceRoot("/workspace");
      const el = buildTurnFooter({
        steering: ["file:///config/home/.kiro/steering/global.md"],
      });
      expect(sectionRows(el, "Context")).toEqual([["Steering added", "global.md"]]);
      expect(el.querySelector(".turn-info-link")).toBeNull();
    });

    it("names every steering document as text before the workspace root is known", () => {
      resetWorkspaceRoot();
      const el = buildTurnFooter({
        steering: ["file:///workspace/.kiro/steering/a.md"],
      });
      expect(sectionRows(el, "Context")).toEqual([["Steering added", "a.md"]]);
      expect(el.querySelector(".turn-info-link")).toBeNull();
    });
  });
});
