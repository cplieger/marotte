// THE OPEN PANEL'S GEOMETRY, through the real builder and the shipped stylesheet: one grid whose
// gutter (G), label (S) and value (V) lines hold across every section, controls at the tier's
// floor, and text that wraps inside the panel at 320 px. The closed row's pin is
// `turn-footer-closed-row-css.test.ts`.
// Phone cases run in iframes (coarse tier), desktop in the 1280x720 page (fine tier).
import { describe, it, expect, vi, beforeAll, afterAll } from "vitest";
import { userEvent } from "vitest/browser";

import { mountAppCSS } from "./__test-helpers__/css-rules.js";
import type * as OpenersModule from "./editor-openers.js";
import type { TurnSummaryData } from "./fundamentals/turn-footer.js";

vi.mock("./editor-openers.js", async (importOriginal) => ({
  ...(await importOriginal<typeof OpenersModule>()),
  // No case here clicks through.
  openFile: undefined,
  openFileDiff: undefined,
  openFileGitDiff: undefined,
}));

const { buildTurnFooter } = await import("./fundamentals/turn-footer.js");
const { setWorkspaceRoot, _resetForTest: resetWorkspaceRoot } = await import("./workspace.js");

let style: HTMLStyleElement;
const frames: HTMLIFrameElement[] = [];
const docs = new Map<number, Document>();

function coarseFrame(width: number): Document {
  const frame = document.createElement("iframe");
  frame.width = String(width);
  frame.height = "3000";
  document.body.appendChild(frame);
  frames.push(frame);
  const inner = frame.contentDocument;
  if (inner === null) {
    throw new Error("iframe has no contentDocument");
  }
  inner.documentElement.dataset["pointer"] = "coarse";
  const sheet = inner.createElement("style");
  sheet.textContent = style.textContent;
  inner.head.appendChild(sheet);
  return inner;
}

beforeAll(() => {
  style = mountAppCSS();
  docs.set(1280, document);
  docs.set(390, coarseFrame(390));
  docs.set(320, coarseFrame(320));
  setWorkspaceRoot("/workspace");
});

afterAll(() => {
  for (const f of frames) {
    f.remove();
  }
  style.remove();
  resetWorkspaceRoot();
});

const mounted = new WeakMap<Document, HTMLElement>();

/** A turn card holding one real footer, adopted into `doc` so its listeners come along. */
function mount(doc: Document, d: TurnSummaryData): HTMLElement {
  const card = doc.createElement("div");
  card.className = "turn";
  const footer = doc.adoptNode(buildTurnFooter(d));
  card.appendChild(footer);
  mounted.get(doc)?.remove();
  mounted.set(doc, card);
  doc.body.appendChild(card);
  return footer;
}

function open(footer: HTMLElement): void {
  footer.querySelector<HTMLButtonElement>(":scope > .turn-ledger-summary")?.click();
  // The footer slides open and clips while it does, so a hit test needs the settled box.
  for (const a of footer.getAnimations({ subtree: true })) {
    a.finish();
  }
}

function expandAll(footer: HTMLElement): void {
  for (const b of footer.querySelectorAll<HTMLButtonElement>("button.turn-info-dbtn")) {
    if (b.getAttribute("aria-expanded") !== "true") {
      b.click();
    }
  }
}

function box(el: Element, origin: Element): { x: number; y: number; w: number; h: number } {
  const b = el.getBoundingClientRect();
  const o = origin.getBoundingClientRect();
  const r = (n: number): number => Math.round(n * 100) / 100;
  return { x: r(b.left - o.left), y: r(b.top - o.top), w: r(b.width), h: r(b.height) };
}

const LONG_PATH =
  "internal/server/static-src/fundamentals/a-deliberately-long-file-name-with-no-breaks.ts";
const LONG_STEER = "client:sess_0123456789abcdef0123456789abcdef0123456789abcdef01234567";
const LONG_ITEM =
  "file-name-with-no-break-opportunity-anywhere-in-it-at-all-0123456789abcdef0123456789abcdef0123";

/** Every row kind the panel can hold, with strings that carry no break opportunity. */
const FULL: TurnSummaryData = {
  outcome: "failed",
  elapsedMs: 92_000,
  toolMs: 30_000,
  startedAt: Date.UTC(2026, 0, 2, 9, 5, 0),
  endedAt: Date.UTC(2026, 0, 2, 9, 6, 32),
  changedFiles: {
    [LONG_PATH]: { lines_added: 120, lines_removed: 4, is_new_file: true },
    "a.ts": { lines_added: 5, lines_removed: 2 },
  },
  kindCounts: { hook: 32, execute: 13, other: 4 },
  asks: { allowed: 4, always_allowed: 1 },
  delegateCount: 2,
  delegateMs: 9000,
  credits: 1.5,
  models: ["claude-opus-4.5", "claude-sonnet-4.5"],
  throughput: { estimated_tokens: 4000, active_streaming_ms: 20_000 },
  steering: ["file:///workspace/.kiro/steering/web.md", LONG_STEER],
  contextBreakdown: {
    total_chars: 141_883,
    model_calls: 6,
    categories: [
      {
        key: "toolIO",
        chars: 90_000,
        percent: 63,
        parts: [
          { key: "input", chars: 50_000 },
          { key: "output", chars: 40_000 },
        ],
        items: [
          { name: LONG_ITEM, chars: 9000, percent: 6, inclusion: "auto" },
          {
            name: "ui-qa",
            chars: 900,
            percent: 1,
            inclusion: "skill",
            uri: "file:///workspace/.kiro/skills/ui-qa/SKILL.md",
          },
        ],
        omitted_count: 3,
        omitted_chars: 1200,
      },
      { key: "history", chars: 51_883, percent: 37 },
    ],
  },
  stopReasonRaw: "max_tokens",
  truncated: true,
  recoveries: ["streamError"],
  engineErrorClass: "ModelOverloadedErrorWithAVeryLongClassNameThatHasNoBreaks",
  requestIds: ["0f1e2d3c-4b5a-6978-8796-a5b4c3d2e1f0", "req_0123456789abcdef0123456789abcdef0123"],
};

describe("the open panel's lines", () => {
  for (const width of [320, 390, 1280] as const) {
    it(`puts every title on G, every label on S and every value on V at ${String(width)} px`, () => {
      const doc = docs.get(width) ?? document;
      // A clean turn, so the closed row's text after the `i` is the fact.
      const footer = mount(doc, { ...FULL, outcome: "completed" });
      const info = footer.querySelector(".turn-ledger-info") as Element;
      const fact = footer.querySelector(":scope > .turn-ledger-summary > .turn-fact") as Element;
      const factX = box(fact, footer).x;
      open(footer);
      expandAll(footer);
      const xs = (sel: string): number[] => [
        ...new Set([...footer.querySelectorAll(sel)].map((e) => Math.round(box(e, footer).x))),
      ];
      expect(xs(".turn-info-title"), "G").toEqual([Math.round(box(info, footer).x)]);
      expect(xs(".turn-info-label"), "S").toEqual([Math.round(factX)]);
      expect(xs(".turn-file-path, .turn-review-label"), "file rows on S").toEqual([
        Math.round(factX),
      ]);
      const v = xs(".turn-info-value");
      expect(v, "V").toHaveLength(1);
      const rem = parseFloat(getComputedStyle(doc.documentElement).fontSize) || 16;
      expect((v[0] ?? 0) - factX, "the label floor").toBeGreaterThanOrEqual(7 * rem - 0.5);
    });

    it(`puts V at one x on every panel of a chat at ${String(width)} px`, () => {
      const doc = docs.get(width) ?? document;
      const valueX = (d: TurnSummaryData): number => {
        const footer = mount(doc, d);
        open(footer);
        const value = footer.querySelector(".turn-info-value") as Element;
        return Math.round(box(value, footer).x);
      };
      // The widest label the builder writes ("Output (estimated)") against a one-row panel.
      expect(valueX(FULL)).toBe(valueX({ outcome: "completed", elapsedMs: 1000 }));
    });
  }
});

describe("the panel's control rows", () => {
  for (const width of [320, 390] as const) {
    it(`gives every control a 44 px target in both dimensions at ${String(width)} px`, () => {
      const doc = docs.get(width) ?? document;
      const footer = mount(doc, FULL);
      open(footer);
      expandAll(footer);
      const panel = footer.querySelector(".turn-info-panel") as Element;
      const controls = [...panel.querySelectorAll<HTMLElement>("button")];
      expect(controls.length).toBeGreaterThan(5);
      for (const c of controls) {
        // A document link's target is its row, through the expander.
        const host = c.matches(".turn-info-link") ? (c.closest("li") as HTMLElement) : c;
        const b = host.getBoundingClientRect();
        const name = c.textContent;
        expect(b.height, `${name} height`).toBeGreaterThanOrEqual(44 - 0.5);
        expect(b.width, `${name} width`).toBeGreaterThanOrEqual(44);
        const mid = { x: b.left + b.width / 2, y: b.top + b.height / 2 };
        for (const [x, y] of [
          [b.left + 1, mid.y],
          [b.right - 1, mid.y],
          [mid.x, b.top + 1],
          [mid.x, b.bottom - 1],
        ] as const) {
          const hit = doc.elementFromPoint(x, y);
          expect(
            hit !== null && c.contains(hit),
            `${name} answers at ${String(x)},${String(y)}`,
          ).toBe(true);
        }
      }
    });
  }

  it("runs plain rows and controls at one 24 px rhythm on a mouse", () => {
    const footer = mount(document, FULL);
    open(footer);
    const heights = [
      ...footer.querySelectorAll(".turn-info-row:not(.turn-info-drow), button.turn-info-dbtn"),
      ...footer.querySelectorAll(".turn-file-row, .turn-review-all"),
    ].map((e) => Math.round(e.getBoundingClientRect().height));
    expect(new Set(heights.filter((h) => h <= 24))).toEqual(new Set([24]));
    // A wrapped value may take more lines, never less than the rhythm.
    expect(Math.min(...heights)).toBe(24);
  });

  for (const [width, floor] of [
    [1280, 24],
    [390, 44],
  ] as const) {
    it(`sizes a document link's row to the ${String(floor)} px floor at ${String(width)} px`, () => {
      const footer = mount(docs.get(width) ?? document, FULL);
      open(footer);
      expandAll(footer);
      const rows = [...footer.querySelectorAll(".turn-info-doc")];
      // The steering document and the skill item.
      expect(rows).toHaveLength(2);
      for (const row of rows) {
        expect(row.getBoundingClientRect().height, row.textContent).toBeCloseTo(floor, 1);
      }
    });
  }

  it("centres a one-line row's text in its 24 px on a mouse, plain rows and controls alike", () => {
    const footer = mount(document, FULL);
    open(footer);
    expandAll(footer);
    const rows = [
      ...footer.querySelectorAll(
        ".turn-info-row:not(.turn-info-drow), .turn-info-dbtn, .turn-info-item, .turn-file-row, .turn-review-all",
      ),
    ].filter((e) => Math.round(e.getBoundingClientRect().height) === 24);
    expect(rows.length).toBeGreaterThan(10);
    for (const row of rows) {
      const text = document.createTreeWalker(row, NodeFilter.SHOW_TEXT).nextNode();
      const range = document.createRange();
      range.selectNodeContents(text as Node);
      const line = range.getClientRects()[0] as DOMRect;
      const b = row.getBoundingClientRect();
      // Within a px: a request id's smaller mono line sits on the row's sans baseline.
      expect(
        Math.abs(line.top + line.height / 2 - b.top - 12),
        row.textContent,
      ).toBeLessThanOrEqual(1);
    }
  });

  it("puts a hovered disclosure's label, value and chevron on the row's ink", async () => {
    const footer = mount(document, FULL);
    open(footer);
    const btn = footer.querySelector<HTMLButtonElement>("button.turn-info-dbtn") as HTMLElement;
    await userEvent.hover(btn);
    for (const a of footer.getAnimations({ subtree: true })) {
      a.finish();
    }
    const ink = getComputedStyle(btn).color;
    for (const sel of [".turn-info-label", ".turn-info-value", ".turn-info-twist"]) {
      expect(getComputedStyle(btn.querySelector(sel) as Element).color, sel).toBe(ink);
    }
    await userEvent.unhover(btn);
  });

  it("adds no height under a closed disclosure", () => {
    for (const doc of [document, docs.get(390) ?? document]) {
      const footer = mount(doc, FULL);
      open(footer);
      const drows = [...footer.querySelectorAll(".turn-info-drow")];
      expect(drows).toHaveLength(3);
      for (const row of drows) {
        const btn = row.querySelector("button.turn-info-dbtn") as Element;
        expect(row.getBoundingClientRect().height, btn.textContent).toBeCloseTo(
          btn.getBoundingClientRect().height,
          1,
        );
      }
    }
  });
});

describe("the panel at 320 px", () => {
  it("wraps every line of text inside the panel, closed lists and open", () => {
    const doc = docs.get(320) ?? document;
    for (const expanded of [false, true]) {
      const footer = mount(doc, FULL);
      open(footer);
      if (expanded) {
        expandAll(footer);
      }
      const panel = footer.querySelector(".turn-info-panel") as HTMLElement;
      const p = panel.getBoundingClientRect();
      const walker = doc.createTreeWalker(panel, NodeFilter.SHOW_TEXT);
      let seen = 0;
      for (let n = walker.nextNode(); n !== null; n = walker.nextNode()) {
        const range = doc.createRange();
        range.selectNodeContents(n);
        for (const r of range.getClientRects()) {
          if (r.width === 0) {
            continue;
          }
          seen++;
          expect(r.right, `"${n.textContent ?? ""}" crosses the panel edge`).toBeLessThanOrEqual(
            p.right + 0.5,
          );
          expect(r.left).toBeGreaterThanOrEqual(p.left - 0.5);
        }
      }
      expect(seen, `text measured, expanded ${String(expanded)}`).toBeGreaterThan(20);
    }
  });
});
