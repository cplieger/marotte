// THE CLOSED FOOTER ROW, assembled by `messages.ts` as a chat paints it (ledger, turn actions,
// Rewind) in every state it can close in, pinned at 320, 390 and 1280 px to the row before the open
// panel's redesign: every element's style the markup and stylesheet decide but the host-dependent
// few, captured from a ref by `scripts/capture-closed-row.ts`, and each box relative to the footer
// or to its neighbour. The phone widths take a copy of the cards into a coarse-pointer iframe:
// style and geometry need only the markup and the stylesheet.
import { describe, it, expect, vi, beforeAll, afterAll } from "vitest";
import { FRAME_BUDGET_MS } from "./__test-helpers__/frame-budget.js";
import { mountAppCSS } from "./__test-helpers__/css-rules.js";
import type * as StoreLoadModule from "./store-load.js";
import type { TurnOutcome } from "./turns.js";
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

/** Every closed row the footer paints, one per outcome the wire can send; keyed by the union, so a
 *  new outcome does not compile until it is pinned. Each says what the row shows: the outcome word
 *  (none on a clean or running turn, which carry no mark either), the lead fact, and whether settled
 *  prose earns the copy actions. */
const ROWS: Record<TurnOutcome, { word: string; fact: string; actions: boolean }> = {
  completed: { word: "", fact: "1m 32s", actions: true },
  running: { word: "", fact: "", actions: false },
  cancelled: { word: "Cancelled", fact: "1m 32s", actions: true },
  interrupted: { word: "Interrupted", fact: "1m 32s", actions: true },
  failed: { word: "Failed", fact: "1m 32s", actions: true },
  refused: { word: "Refused", fact: "1m 32s", actions: true },
  unknown: { word: "Outcome unknown", fact: "1m 32s", actions: true },
  empty: { word: "Empty", fact: "1m 32s", actions: true },
};
const STATES = Object.keys(ROWS) as TurnOutcome[];
const WIDTHS = [1280, 390, 320] as const;

/** A turn with prose, closed with `outcome` after `elapsedMs`; a running turn has no close. */
function turn(n: number, outcome: TurnOutcome, elapsedMs: number): Entry[] {
  const id = `t${String(n)}`;
  const at = (seq: number, kind: Entry["kind"], payload: unknown): Entry =>
    ({ id: `${id}-e${String(seq)}`, turn: id, kind, seq, ts: 1000, payload }) as Entry;
  const entries = [
    at(0, "turn_open", { prompt: { id: `${id}-p`, text: `prompt ${id}` }, source: "prompt", n }),
    at(1, "text", { text: "reply" }),
  ];
  return outcome === "running"
    ? entries
    : [...entries, at(2, "turn_close", { outcome, elapsed_ms: elapsedMs })];
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

/** One turn per state, in `STATES` order, then a settled turn that gives the last a Rewind target. */
async function paint(): Promise<HTMLElement[]> {
  const id = "closed";
  const all = [
    ...STATES.map((s, i) => turn(i + 1, s, 92_000)),
    turn(STATES.length + 1, "completed", 1000),
  ];
  const turns = new Map<string, TurnState>();
  for (const entries of all) {
    const first = entries[0] as Entry;
    const closeAt = entries.findIndex((e) => e.kind === "turn_close");
    turns.set(first.turn, {
      entries: [...entries],
      openEntries: new Map(),
      ...(closeAt === -1 ? {} : { closeAt }),
    });
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
      turn_order: [...turns.keys()],
      turn_count: turns.size,
      has_more: false,
      residency: "loaded",
      thinking: false,
      working_label: "",
      usage: { context_pct: 0, context_size: 0, credits: 0, last_turn_ms: 0, has_real_data: false },
    },
  ]);
  store.setActive(id);
  await until(() => cards().length === all.length, `${String(all.length)} cards`);
  await until(
    () =>
      [...document.querySelectorAll(".turn-footer")].every(
        (f) => getComputedStyle(f).opacity === "1",
      ),
    "footers settle",
  );
  return cards();
}

const frames: HTMLIFrameElement[] = [];
/** State, then width. */
const footers = new Map<TurnOutcome, Map<number, HTMLElement>>();

function footerOf(card: Element | undefined): HTMLElement {
  return card?.querySelector(":scope > .turn-footer") as HTMLElement;
}

beforeAll(async () => {
  const list = await paint();
  STATES.forEach((s, i) => footers.set(s, new Map([[1280, footerOf(list[i])]])));
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
    for (const card of list) {
      host.appendChild(inner.importNode(card, true));
    }
    inner.body.appendChild(host);
    STATES.forEach((s, i) => footers.get(s)?.set(width, footerOf(host.children[i])));
    // A copy fades in from `@starting-style` like a painted card; a snapshot mid-fade reads the clock.
    await until(
      () =>
        [...host.querySelectorAll(".turn-footer")].every(
          (f) => inner.defaultView?.getComputedStyle(f).opacity === "1",
        ),
      `${String(width)} px footers settle`,
    );
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

/** The space between each shown box and the next. */
function gaps(boxes: readonly Element[]): number[] {
  return boxes.slice(1).map((c, i) => {
    const prev = (boxes[i] as Element).getBoundingClientRect();
    return Math.round((c.getBoundingClientRect().left - prev.right) * 100) / 100;
  });
}

// Measured on the row before the redesign: the inline actions and the worded Rewind on a
// mouse, the `…` overflow and the glyph-only Rewind on a phone. `system-ui` resolves through the
// host's fonts, so nothing here depends on a glyph's metrics: the ledger's parts are held by the
// `i` and the outcome mark, their spacing and their centres, the worded Rewind by its width less
// the word, the copy actions by their spacing.
const FINE = {
  band: 32,
  ledger: { x: 8, y: 4, h: 24 },
  info: { x: 12, y: 8, w: 16, h: 16 },
  mark: { x: 36, y: 12, w: 8, h: 8 },
  ledgerGap: 8,
  actions: [
    ["Copy as text", { y: 4, w: 24, h: 24 }],
    ["Copy as markdown", { y: 4, w: 24, h: 24 }],
    ["Copy link to this turn", { y: 4, w: 24, h: 24 }],
  ],
  rewind: ["Rewind to this point", { end: 8, y: 4, h: 24 }],
  gaps: [2, 2, 8],
  rewindChrome: 36,
  glyphs: { actions: 3, size: 16 },
} as const;
const COARSE = {
  band: 44,
  ledger: { x: 8, y: 4, h: 36 },
  info: { x: 12, y: 12, w: 20, h: 20 },
  mark: { x: 40, y: 18, w: 8, h: 8 },
  ledgerGap: 8,
  actions: [["More turn actions", { end: 52, y: 4, w: 36, h: 36 }]],
  rewind: ["Rewind to this point", { end: 8, y: 4, w: 36, h: 36 }],
  gaps: [8],
  rewindChrome: 36,
  glyphs: { actions: 0, size: 20 },
} as const;
const PIN = { 1280: FINE, 390: COARSE, 320: COARSE } as const;

/** Computed properties the snapshot leaves out, each host-dependent; the boxes cases pin geometry. */
const HOST_DEPENDENT = new Map([
  ["width", "a used size: a part holding text is as wide as the host font sets it"],
  ["height", "a used size: a text box is as tall as the host font's line"],
  ["inline-size", "the logical twin of width"],
  ["block-size", "the logical twin of height"],
  ["transform-origin", "resolves to the centre of the used box"],
  ["perspective-origin", "resolves to the centre of the used box"],
  ["grid-template-columns", "the row's resolved tracks are its items' used widths"],
  ["right", "an `auto` offset on a positioned box resolves from its container's used width"],
  ["inset-inline-end", "the logical twin of right"],
]);

/** State, then width, then element path in document order, then property in name order. */
type Snapshot = Record<string, Record<string, Record<string, Record<string, string>>>>;

/** The footer and every element under it but the open panel's subtree, which the redesign owns;
 *  a path names each element by tag, classes and label, so a diff line says which part moved. */
function rowElements(footer: Element): [string, Element][] {
  const out: [string, Element][] = [];
  const walk = (e: Element, path: string): void => {
    out.push([path, e]);
    const seen = new Map<string, number>();
    for (const c of e.children) {
      if (c.classList.contains("turn-info-panel")) {
        continue;
      }
      const classes = [...c.classList].map((k) => `.${k}`).join("");
      const label = c.getAttribute("aria-label");
      const name = `${c.tagName.toLowerCase()}${classes}${label === null ? "" : `[${label}]`}`;
      const n = (seen.get(name) ?? 0) + 1;
      seen.set(name, n);
      walk(c, `${path} > ${name}${n === 1 ? "" : `:${String(n)}`}`);
    }
  };
  walk(footer, "footer");
  return out;
}

/** Every enumerated property but custom ones (tokens paint only through the properties that read
 *  them) and the host-dependent few, copied: a computed style is live. */
function read(style: CSSStyleDeclaration): Map<string, string> {
  return new Map(
    [...style]
      .filter((p) => !p.startsWith("--") && !HOST_DEPENDENT.has(p))
      .map((p) => [p, style.getPropertyValue(p)]),
  );
}

/** `e`'s style and each used pseudo-element's, every property in it that differs from a stand-in:
 *  the same tag in the same parent with every author rule reverted, so the stand-in inherits what
 *  `e` inherits and holds what the user agent alone gives the tag. What remains is what the markup
 *  and the stylesheet decide, and a property a browser adds or drops reads alike on both. */
function decided(e: Element): [string, Record<string, string>][] {
  const view = e.ownerDocument.defaultView as Window;
  const own = view.getComputedStyle(e);
  const pseudos = [
    "",
    ...["::before", "::after", "::marker"].filter((p) =>
      p === "::marker"
        ? own.display === "list-item"
        : view.getComputedStyle(e, p).content !== "none",
    ),
  ];
  // Read before the stand-in joins the parent, where it would move `e`'s structural matches.
  const mine = pseudos.map((p) => read(view.getComputedStyle(e, p)));
  const stand = e.ownerDocument.createElementNS(e.namespaceURI, e.localName);
  stand.setAttribute("style", "all: revert");
  e.after(stand);
  const base = pseudos.map((p) => read(view.getComputedStyle(stand, p)));
  stand.remove();
  return pseudos.map((p, i) => [
    p,
    Object.fromEntries(
      [...(mine[i] as Map<string, string>)]
        .filter(([prop, v]) => base[i]?.get(prop) !== v)
        .sort(([a], [b]) => (a < b ? -1 : a > b ? 1 : 0)),
    ),
  ]);
}

function snapshot(): Snapshot {
  const out: Snapshot = {};
  for (const state of STATES) {
    const byWidth: Snapshot[string] = {};
    for (const width of WIDTHS) {
      const parts: Record<string, Record<string, string>> = {};
      for (const [path, e] of rowElements(footers.get(state)?.get(width) as HTMLElement)) {
        for (const [pseudo, style] of decided(e)) {
          parts[`${path}${pseudo}`] = style;
        }
      }
      byWidth[String(width)] = parts;
    }
    out[state] = byWidth;
  }
  return out;
}

/** The snapshot as committed, by width then path: `states` names the states that render the path
 *  when not all do (one string, which Prettier leaves as `JSON.stringify` wrote it), and a property
 *  is written once where all of those agree on it, per state otherwise (a state left out of it has
 *  the stand-in's value), so a recapture's diff shows a change once and not on every state it
 *  repeats on. */
type Fixture = Record<
  string,
  Record<string, { states?: string; style: Record<string, string | Record<string, string>> }>
>;

function encode(live: Snapshot): Fixture {
  const out: Fixture = {};
  for (const width of WIDTHS.map(String)) {
    const paths = [...new Set(STATES.flatMap((s) => Object.keys(live[s]?.[width] ?? {})))];
    out[width] = Object.fromEntries(
      paths.map((path) => {
        const rendered = STATES.filter((s) => live[s]?.[width]?.[path] !== undefined);
        const of = (s: string): Record<string, string> => live[s]?.[width]?.[path] ?? {};
        const props = [...new Set(rendered.flatMap((s) => Object.keys(of(s))))].sort();
        const style = Object.fromEntries(
          props.map((p) => {
            const values = rendered.flatMap((s) => {
              const v = of(s)[p];
              return v === undefined ? [] : [[s, v] as const];
            });
            const one = values[0]?.[1];
            const agreed = values.length === rendered.length && values.every(([, v]) => v === one);
            return [p, agreed ? (one as string) : Object.fromEntries(values)];
          }),
        );
        return [
          path,
          rendered.length === STATES.length ? { style } : { states: rendered.join(" "), style },
        ];
      }),
    );
  }
  return out;
}

function decode(fixture: Fixture): Snapshot {
  const out: Snapshot = {};
  for (const [width, paths] of Object.entries(fixture)) {
    for (const [path, { states, style }] of Object.entries(paths)) {
      for (const s of states?.split(" ") ?? STATES) {
        const byWidth = (out[s] ??= {});
        const parts = (byWidth[width] ??= {});
        const own: Record<string, string> = {};
        for (const [p, v] of Object.entries(style)) {
          const value = typeof v === "string" ? v : v[s];
          if (value !== undefined) {
            own[p] = value;
          }
        }
        parts[path] = own;
      }
    }
  }
  return out;
}

/** One line per property whose value differs, so a red run names the state, the part and the
 *  property. */
function differences(want: Snapshot, got: Snapshot): string[] {
  const lines: string[] = [];
  const keys = (a: object, b: object): string[] =>
    [...new Set([...Object.keys(a), ...Object.keys(b)])].sort();
  for (const state of keys(want, got)) {
    const ws = want[state] ?? {};
    const gs = got[state] ?? {};
    for (const width of keys(ws, gs)) {
      const w = ws[width] ?? {};
      const g = gs[width] ?? {};
      for (const path of keys(w, g)) {
        const wp = w[path];
        const gp = g[path];
        const at = `${state} ${width} ${path}`;
        if (wp === undefined || gp === undefined) {
          lines.push(`${at}: ${wp === undefined ? "added" : "removed"}`);
          continue;
        }
        for (const prop of keys(wp, gp)) {
          if (wp[prop] !== gp[prop]) {
            lines.push(
              `${at} ${prop}: ${wp[prop] ?? "(as the tag)"} -> ${gp[prop] ?? "(as the tag)"}`,
            );
          }
        }
      }
    }
  }
  return lines;
}

// Absent on a base ref that predates the fixture, which `capture:closed-row` then writes.
const captured = Object.values(
  import.meta.glob<string>("./testdata/turn-footer-closed-row-styles.json", {
    eager: true,
    query: "?raw",
    import: "default",
  }),
)[0];

describe("the closed footer row", () => {
  it("keeps every element's decided style in every state at 320, 390 and 1280 px", async () => {
    const live = snapshot();
    const diff =
      captured === undefined ? [] : differences(decode(JSON.parse(captured) as Fixture), live);
    await expect(
      `${JSON.stringify(encode(live), null, 2)}\n`,
      [
        "the closed row's style differs from its capture; an intended change recaptures with",
        "`npm run capture:closed-row -- HEAD` once committed (`-- origin/dev` re-takes the base):",
        ...diff,
      ].join("\n"),
    ).toMatchFileSnapshot("./testdata/turn-footer-closed-row-styles.json");
    for (const [state, byWidth] of footers) {
      for (const [width, footer] of byWidth) {
        const view = footer.ownerDocument.defaultView as Window;
        const card = view.getComputedStyle(footer.closest(".turn") as Element).fontFamily;
        for (const [path, e] of rowElements(footer)) {
          const family = view.getComputedStyle(e).fontFamily;
          expect(family, `${state} ${String(width)} px ${path}: the card's font`).toBe(card);
        }
      }
    }
  });

  for (const state of STATES) {
    for (const width of [320, 390, 1280] as const) {
      it(`keeps the ${state} row's items, their order and their boxes at ${String(width)} px`, () => {
        const footer = footers.get(state)?.get(width) as HTMLElement;
        const pin = PIN[width];
        const row = ROWS[state];
        expect(footer.dataset["info"], "closed").not.toBe("open");
        expect(
          [...footer.children].map((c) => c.className),
          "the row's items in order",
        ).toEqual([
          "turn-ledger-summary",
          "turn-info-panel",
          ...(row.actions ? ["turn-actions-buttons"] : []),
          "turn-rewind",
        ]);
        expect(
          footer.querySelector(":scope > .turn-info-panel")?.checkVisibility(),
          "the info panel, shut",
        ).toBe(false);
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
        const part = (name: string): HTMLElement =>
          ledger.querySelector(`:scope > .${name}`) as HTMLElement;
        expect(part("turn-ledger-text").textContent, "the outcome word").toBe(row.word);
        expect(part("turn-fact").textContent, "the lead fact").toBe(row.fact);
        const shownParts = [
          "turn-ledger-info",
          ...(row.word === "" ? [] : ["turn-ledger-glyph", "turn-ledger-text"]),
          ...(row.fact === "" ? [] : ["turn-fact"]),
        ];
        expect(
          [...ledger.children].filter((c) => c.checkVisibility()).map((c) => c.className),
          "the ledger's shown parts",
        ).toEqual(["sr-only", ...shownParts]);
        expect(footer.getBoundingClientRect().height, "band").toBeCloseTo(pin.band, 1);
        expect(box(ledger, footer, pin.ledger), "ledger").toEqual(pin.ledger);
        expect(box(part("turn-ledger-info"), footer, pin.info), "`i`").toEqual(pin.info);
        const mark = part("turn-ledger-glyph");
        expect(
          mark.checkVisibility() ? box(mark, footer, pin.mark) : null,
          "the outcome mark",
        ).toEqual(row.word === "" ? null : pin.mark);
        const shown = shownParts.map(part);
        const spacing = gaps(shown);
        spacing.forEach((g, i) => {
          expect(g, `the space after ledger part ${String(i)}`).toBeCloseTo(pin.ledgerGap, 1);
        });
        const ledgerBox = ledger.getBoundingClientRect();
        for (const p of shown) {
          const b = p.getBoundingClientRect();
          expect(
            Math.abs(b.top + b.height / 2 - (ledgerBox.top + ledgerBox.height / 2)),
            `${p.className} centred on the ledger`,
          ).toBeLessThanOrEqual(0.5);
        }

        const controls = [
          ...footer.querySelectorAll<HTMLElement>(
            ":scope > .turn-actions-buttons :is(button, summary), :scope > .turn-rewind",
          ),
        ].filter((c) => c.checkVisibility());
        const want = [...(row.actions ? pin.actions : []), pin.rewind];
        expect(
          controls.map((c) => c.getAttribute("aria-label")),
          "the trailing controls",
        ).toEqual(want.map(([label]) => label));
        controls.forEach((c, i) => {
          const b = want[i]?.[1] as Box;
          expect(box(c, footer, b), c.getAttribute("aria-label") ?? "").toEqual(b);
        });
        // Within a layout unit: a box after a word starts on a 1/64 px grid, not a whole pixel.
        const between = gaps(controls);
        const pinned = row.actions ? pin.gaps : [];
        expect(between, "the space between the trailing controls").toHaveLength(pinned.length);
        between.forEach((g, i) => {
          expect(g, `the space after control ${String(i)}`).toBeCloseTo(pinned[i] as number, 1);
        });
        const rewind = footer.querySelector(":scope > .turn-rewind") as Element;
        expect(rewindChrome(rewind), "Rewind less its word").toBeCloseTo(pin.rewindChrome, 1);
        const glyphs = [...footer.querySelectorAll("svg")].filter((g) => g.checkVisibility());
        expect(glyphs.length, "the row's shown glyphs: the `i`, the actions', Rewind's").toBe(
          2 + (row.actions ? pin.glyphs.actions : 0),
        );
        glyphs.forEach((g, i) => {
          const b = g.getBoundingClientRect();
          expect([b.width, b.height], `glyph ${String(i)}`).toEqual([
            pin.glyphs.size,
            pin.glyphs.size,
          ]);
        });
      });
    }
  }
});
