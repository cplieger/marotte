// THE CLOSED FOOTER ROW, assembled by `messages.ts` as a chat paints it (ledger, turn actions,
// Rewind), pinned at 320, 390 and 1280 px to the row before the open panel's redesign: each part's
// computed style, read from a base ref by `scripts/capture-closed-row.ts`, and each box relative to
// the footer or to its neighbour. The phone widths take a copy of the card into a coarse-pointer
// iframe: style and geometry need only the markup and the stylesheet.
import { describe, it, expect, vi, beforeAll, afterAll } from "vitest";
import { FRAME_BUDGET_MS } from "./__test-helpers__/frame-budget.js";
import { mountAppCSS } from "./__test-helpers__/css-rules.js";
import type * as StoreLoadModule from "./store-load.js";
import type { TurnState } from "./types.js";
import type { Entry } from "./wire/types.gen.js";

const outer = document.createElement("div");
outer.id = "messages-wrap-outer";
outer.style.cssText = "position:relative;";
const wrap = document.createElement("div");
wrap.id = "messages-wrap";
wrap.style.cssText = "height:600px;overflow-y:auto;overflow-anchor:none;position:relative;";
const messagesEl = document.createElement("div");
messagesEl.id = "messages";
wrap.appendChild(messagesEl);
outer.appendChild(wrap);
document.body.appendChild(outer);
for (const [id, tag] of [
  ["chat-view", "div"],
  ["scroll-bottom", "button"],
  ["send-btn", "button"],
  ["prompt-input", "textarea"],
] as const) {
  const e = document.createElement(tag);
  e.id = id;
  if (id === "scroll-bottom") {
    e.appendChild(document.createElement("span"));
  }
  document.body.appendChild(e);
}

// The rail's index and pagination are network reads these cases do not test.
vi.mock("./api-client.js", { spy: true });
vi.mock("./store-load.js", async (importOriginal) => ({
  ...(await importOriginal<typeof StoreLoadModule>()),
  loadMessages: vi.fn(),
  loadList: vi.fn(),
}));

const store = await import("./store.js");
const { observeStamp } = await import("./subject-versions.js");
const messages = await import("./messages.js");

messages.mountChatView();

let bundle: HTMLStyleElement;
beforeAll(() => {
  bundle = mountAppCSS();
});

afterAll(() => {
  bundle.remove();
});

function turn(n: number, text: string, elapsedMs: number): Entry[] {
  const id = `t${String(n)}`;
  const at = (seq: number, kind: Entry["kind"], payload: unknown): Entry =>
    ({ id: `${id}-e${String(seq)}`, turn: id, kind, seq, ts: 1000, payload }) as Entry;
  return [
    at(0, "turn_open", { prompt: { id: `${id}-p`, text: `prompt ${id}` }, source: "prompt", n }),
    at(1, "text", { text }),
    at(2, "turn_close", { outcome: "completed", elapsed_ms: elapsedMs }),
  ];
}

async function until(pred: () => boolean, what: string): Promise<void> {
  const deadline = Date.now() + FRAME_BUDGET_MS;
  while (!pred()) {
    if (Date.now() > deadline) {
      throw new Error(`timed out after ${String(FRAME_BUDGET_MS)}ms waiting for ${what}`);
    }
    await new Promise((r) => setTimeout(r, 8));
  }
}

function cards(): HTMLElement[] {
  const root = messages.activeTranscriptView();
  return root === null ? [] : [...root.querySelectorAll<HTMLElement>(":scope > .turn")];
}

/** The first of two settled turns: prose and a fact earn it the copy actions, and the turn after
 *  gives it a Rewind target. */
async function paint(): Promise<HTMLElement> {
  const id = "closed";
  const turns = new Map<string, TurnState>();
  for (const entries of [turn(1, "reply", 92_000), turn(2, "reply", 1000)]) {
    const first = entries[0] as Entry;
    turns.set(first.turn, { entries: [...entries], openEntries: new Map(), closeAt: 2 });
  }
  observeStamp({ kind: "chat", ref: id, version: "1" });
  store.setSessions([
    {
      id,
      name: id,
      model: "",
      acp_session_id: "",
      current_mode_id: "",
      turns,
      turn_order: ["t1", "t2"],
      turn_count: 2,
      has_more: false,
      residency: "loaded",
      thinking: false,
      working_label: "",
      usage: { context_pct: 0, context_size: 0, credits: 0, last_turn_ms: 0, has_real_data: false },
    },
  ]);
  store.setActive(id);
  await until(() => cards().length === 2, "2 cards");
  await until(
    () =>
      [...document.querySelectorAll(".turn-footer")].every(
        (f) => getComputedStyle(f).opacity === "1",
      ),
    "footers settle",
  );
  return cards()[0] as HTMLElement;
}

const frames: HTMLIFrameElement[] = [];
const footers = new Map<number, HTMLElement>();

beforeAll(async () => {
  const card = await paint();
  footers.set(1280, card.querySelector(":scope > .turn-footer") as HTMLElement);
  for (const width of [390, 320]) {
    const frame = document.createElement("iframe");
    frame.width = String(width);
    frame.height = "400";
    document.body.appendChild(frame);
    frames.push(frame);
    const inner = frame.contentDocument as Document;
    inner.documentElement.dataset["pointer"] = "coarse";
    const sheet = inner.createElement("style");
    sheet.textContent = bundle.textContent;
    inner.head.appendChild(sheet);
    const host = inner.createElement("div");
    host.id = "messages";
    host.appendChild(inner.importNode(card, true));
    inner.body.appendChild(host);
    footers.set(width, host.querySelector(".turn-footer") as HTMLElement);
  }
});

afterAll(() => {
  for (const f of frames) {
    f.remove();
  }
});

interface Box {
  x?: number;
  /** From the footer's right edge to the box's: the trailing controls sit on the end. */
  end?: number;
  y: number;
  w?: number;
  h: number;
}

function box(e: Element, footer: Element, want: Box): Box {
  const b = e.getBoundingClientRect();
  const o = footer.getBoundingClientRect();
  const r = (n: number): number => Math.round(n * 100) / 100;
  const got: Box = { y: r(b.top - o.top), h: r(b.height) };
  if (want.x !== undefined) {
    got.x = r(b.left - o.left);
  }
  if (want.end !== undefined) {
    got.end = r(o.right - b.right);
  }
  if (want.w !== undefined) {
    got.w = r(b.width);
  }
  return got;
}

/** Rewind's width less its word: padding, glyph and gap, or the whole glyph-only square. */
function rewindChrome(rewind: Element): number {
  const range = rewind.ownerDocument.createRange();
  range.selectNodeContents(rewind.querySelector(".turn-rewind-label") as Element);
  const label = range.getBoundingClientRect().width;
  return Math.round((rewind.getBoundingClientRect().width - label) * 100) / 100;
}

/** The space between each shown control and the next. */
function gaps(controls: readonly Element[]): number[] {
  return controls.slice(1).map((c, i) => {
    const prev = (controls[i] as Element).getBoundingClientRect();
    return Math.round((c.getBoundingClientRect().left - prev.right) * 100) / 100;
  });
}

// Measured on the live app before the redesign: the inline actions and the worded Rewind on a
// mouse, the `…` overflow and the glyph-only Rewind on a phone. `system-ui` resolves through the
// host's fonts, so nothing here depends on a glyph's metrics: the fact is held by its start and
// its centre, the worded Rewind by its width less the word, the copy actions by their spacing.
const FINE = {
  band: 32,
  ledger: { x: 8, y: 4, h: 24 },
  info: { x: 12, y: 8, w: 16, h: 16 },
  factX: 36,
  controls: [
    ["Copy as text", { y: 4, w: 24, h: 24 }],
    ["Copy as markdown", { y: 4, w: 24, h: 24 }],
    ["Copy link to this turn", { y: 4, w: 24, h: 24 }],
    ["Rewind to this point", { end: 8, y: 4, h: 24 }],
  ],
  gaps: [2, 2, 8],
  rewindChrome: 36,
} as const;
const COARSE = {
  band: 44,
  ledger: { x: 8, y: 4, h: 36 },
  info: { x: 12, y: 12, w: 20, h: 20 },
  factX: 40,
  controls: [
    ["More turn actions", { end: 52, y: 4, w: 36, h: 36 }],
    ["Rewind to this point", { end: 8, y: 4, w: 36, h: 36 }],
  ],
  gaps: [8],
  rewindChrome: 36,
} as const;
const PIN = { 1280: FINE, 390: COARSE, 320: COARSE } as const;

/** What a part looks like. Computed values are the stylesheet's, so no cell reads a host font. */
const LOOK = [
  "display",
  "align-items",
  "gap",
  "padding",
  "margin",
  "font-family",
  "font-size",
  "font-weight",
  "line-height",
  "letter-spacing",
  "color",
] as const;

type Find = (footer: HTMLElement) => Element | null;
const select =
  (selector: string): Find =>
  (footer) =>
    footer.querySelector(selector);

/** Each part the style table holds; the `i` and its glyph add their size. */
const PARTS: readonly (readonly [string, Find, readonly string[]])[] = [
  ["row", (footer) => footer, LOOK],
  ["ledger", select(":scope > .turn-ledger-summary"), LOOK],
  ["i", select(".turn-ledger-info"), [...LOOK, "width", "height"]],
  ["i glyph", select(".turn-ledger-info > svg"), [...LOOK, "width", "height"]],
  ["outcome", select(".turn-ledger-text"), LOOK],
  ["fact", select(".turn-fact"), LOOK],
  ["actions", select(":scope > .turn-actions-buttons"), LOOK],
  [
    "first shown action",
    (footer) =>
      [...footer.querySelectorAll(":scope > .turn-actions-buttons :is(button, summary)")].find(
        (c) => c.checkVisibility(),
      ) ?? null,
    LOOK,
  ],
  ["rewind", select(":scope > .turn-rewind"), LOOK],
  ["rewind word", select(".turn-rewind-label"), LOOK],
];

function part(footer: HTMLElement, width: number, name: string, find: Find): Element {
  const e = find(footer);
  if (e === null) {
    throw new Error(`${String(width)} px: no ${name}`);
  }
  return e;
}

/** One cell per width, part and property, keyed so a failing diff line names all three. */
function styleTable(): Record<string, string> {
  const cells: Record<string, string> = {};
  for (const width of [1280, 390, 320] as const) {
    const footer = footers.get(width) as HTMLElement;
    const view = footer.ownerDocument.defaultView as Window;
    for (const [name, find, props] of PARTS) {
      const style = view.getComputedStyle(part(footer, width, name, find));
      for (const p of props) {
        cells[`${String(width)} ${name} ${p}`] = style.getPropertyValue(p);
      }
    }
  }
  return cells;
}

describe("the closed footer row", () => {
  it("keeps each part's computed style at 320, 390 and 1280 px", async () => {
    await expect(
      `${JSON.stringify(styleTable(), null, 2)}\n`,
      "the closed row's style differs from its capture; an intended change recaptures with `npm run capture:closed-row`",
    ).toMatchFileSnapshot("./testdata/turn-footer-closed-row-styles.json");
    for (const [width, footer] of footers) {
      const view = footer.ownerDocument.defaultView as Window;
      const card = view.getComputedStyle(footer.closest(".turn") as Element).fontFamily;
      for (const [name, find] of PARTS) {
        const family = view.getComputedStyle(part(footer, width, name, find)).fontFamily;
        expect(family, `${String(width)} px ${name}: the card's font`).toBe(card);
      }
    }
  });

  for (const width of [320, 390, 1280] as const) {
    it(`keeps its items, their order and their boxes at ${String(width)} px`, () => {
      const footer = footers.get(width) as HTMLElement;
      const pin = PIN[width];
      expect(footer.dataset["info"], "closed").not.toBe("open");
      expect(
        [...footer.children].map((c) => c.className),
        "the row's items in order",
      ).toEqual(["turn-ledger-summary", "turn-info-panel", "turn-actions-buttons", "turn-rewind"]);
      const ledger = footer.querySelector(":scope > .turn-ledger-summary") as HTMLElement;
      expect(
        [...ledger.children].map((c) => c.className),
        "the ledger's parts in order",
      ).toEqual([
        "sr-only",
        "turn-ledger-info",
        "turn-ledger-glyph",
        "turn-ledger-text",
        "turn-fact",
      ]);
      expect(footer.getBoundingClientRect().height, "band").toBeCloseTo(pin.band, 1);
      expect(box(ledger, footer, pin.ledger), "ledger").toEqual(pin.ledger);
      const info = ledger.querySelector(":scope > .turn-ledger-info") as Element;
      expect(box(info, footer, pin.info), "`i`").toEqual(pin.info);
      const fact = ledger.querySelector(":scope > .turn-fact") as Element;
      expect(fact.textContent, "the lead fact").not.toBe("");
      const factBox = fact.getBoundingClientRect();
      const ledgerBox = ledger.getBoundingClientRect();
      expect(factBox.left - footer.getBoundingClientRect().left, "fact start").toBeCloseTo(
        pin.factX,
        2,
      );
      expect(
        Math.abs(factBox.top + factBox.height / 2 - (ledgerBox.top + ledgerBox.height / 2)),
        "fact centred on the ledger",
      ).toBeLessThanOrEqual(0.5);

      const shown = [
        ...footer.querySelectorAll<HTMLElement>(
          ":scope > .turn-actions-buttons :is(button, summary), :scope > .turn-rewind",
        ),
      ].filter((c) => c.checkVisibility());
      expect(
        shown.map((c) => c.getAttribute("aria-label")),
        "the trailing controls",
      ).toEqual(pin.controls.map(([label]) => label));
      shown.forEach((c, i) => {
        const want = pin.controls[i]?.[1] as Box;
        expect(box(c, footer, want), c.getAttribute("aria-label") ?? "").toEqual(want);
      });
      // Within a layout unit: a box after a word starts on a 1/64 px grid, not a whole pixel.
      const between = gaps(shown);
      expect(between, "the space between the trailing controls").toHaveLength(pin.gaps.length);
      between.forEach((g, i) => {
        expect(g, `the space after control ${String(i)}`).toBeCloseTo(pin.gaps[i] as number, 1);
      });
      const rewind = footer.querySelector(":scope > .turn-rewind") as Element;
      expect(rewindChrome(rewind), "Rewind less its word").toBeCloseTo(pin.rewindChrome, 1);
    });
  }
});
