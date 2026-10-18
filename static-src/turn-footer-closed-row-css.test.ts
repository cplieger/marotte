// THE CLOSED FOOTER ROW, assembled by `messages.ts` as a chat paints it, in every combination of
// the dimensions its markup varies on (`DIMENSIONS`, less those `impossible` names), pinned in both
// themes at 320, 390 and 1280 px to the row before the open panel's redesign: every element's style
// the markup and stylesheet decide but the host-dependent few, captured from a ref by
// `scripts/capture-closed-row.ts`, and each box relative to the footer or to its neighbour. Each
// chat's transcript is copied into one iframe per theme and width: style and geometry need only the
// markup and the stylesheet.
import { describe, it, expect, vi, beforeAll, afterAll } from "vitest";
import { FRAME_BUDGET_MS, testTimeoutFor } from "./__test-helpers__/frame-budget.js";
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

/** What the ledger shows for each outcome: the word (none on a clean or running turn, which carry
 *  no mark either) and the lead fact. Keyed by the union, so a new outcome does not compile until
 *  it is pinned. `running` is the state before any close: its fact is the tool call that earns its
 *  footer. */
const OUTCOMES: Record<TurnOutcome, { word: string; fact: string }> = {
  completed: { word: "", fact: "1m 32s" },
  running: { word: "", fact: "1 read" },
  cancelled: { word: "Cancelled", fact: "1m 32s" },
  interrupted: { word: "Interrupted", fact: "1m 32s" },
  failed: { word: "Failed", fact: "1m 32s" },
  refused: { word: "Refused", fact: "1m 32s" },
  unknown: { word: "Outcome unknown", fact: "1m 32s" },
  empty: { word: "Empty", fact: "1m 32s" },
};

/** Every dimension the closed row's markup varies on: the outcome (`data-outcome`,
 *  `data-severity`), Rewind (`mountRewind`: enabled, disabled while the chat is busy, or absent
 *  with no next prompt to address), the card's place in its chat, and the copy actions, which
 *  settled prose earns. */
const DIMENSIONS = {
  outcome: Object.keys(OUTCOMES) as TurnOutcome[],
  rewind: ["enabled", "disabled", "absent"],
  position: ["older", "newest"],
  actions: ["actions", "no-actions"],
} as const;
type Row = { [K in keyof typeof DIMENSIONS]: (typeof DIMENSIONS)[K][number] };

/** Why a combination never paints, or undefined when it does. */
function impossible(r: Row): string | undefined {
  if (r.position === "newest" && r.rewind !== "absent") {
    return "the newest turn has no next prompt to rewind to";
  }
  if (r.outcome === "running" && r.position !== "newest") {
    return "a live turn is its chat's last";
  }
  if (r.outcome === "running" && r.actions === "actions") {
    return "the copy actions wait for the turn to close";
  }
  return undefined;
}

function combinations(): Row[] {
  let out: Record<string, string>[] = [{}];
  for (const [dim, values] of Object.entries(DIMENSIONS)) {
    out = out.flatMap((r) => values.map((v: string) => ({ ...r, [dim]: v })));
  }
  return (out as unknown as Row[]).filter((r) => impossible(r) === undefined);
}

const ROWS = combinations();
const THEMES = ["dark", "light"] as const;
const WIDTHS = [1280, 390, 320] as const;
/** A scenario id's parts, in this order; `Fixture` factors a value by them. */
const FACTORS = ["theme", "width", ...Object.keys(DIMENSIONS)];

function rowKey(r: Row): string {
  return Object.keys(DIMENSIONS)
    .map((k) => r[k as keyof Row])
    .join(" ");
}

/** Scenario ids, theme then width then row, the order the fixture lists them in. */
const SCENARIOS = THEMES.flatMap((theme) =>
  WIDTHS.flatMap((width) => ROWS.map((r) => `${theme} ${String(width)} ${rowKey(r)}`)),
);

let seq = 0;

/** A turn opened by a prompt, or by the agent with none (a turn Rewind cannot address), with prose
 *  or without, closed with `outcome`; a running turn has no close and makes one tool call. */
function turn(outcome: TurnOutcome, prose: boolean, prompt: boolean): Entry[] {
  const n = ++seq;
  const id = `t${String(n)}`;
  const at = (s: number, kind: Entry["kind"], payload: unknown): Entry =>
    ({ id: `${id}-e${String(s)}`, turn: id, kind, seq: s, ts: 1000, payload }) as Entry;
  const open = prompt
    ? { prompt: { id: `${id}-p`, text: `prompt ${id}` }, source: "prompt", n }
    : { source: "wire_turn_start", n };
  const entries = [at(0, "turn_open", open), ...(prose ? [at(1, "text", { text: "reply" })] : [])];
  return outcome === "running"
    ? [
        ...entries,
        at(2, "tool_call", {
          id: `${id}-c`,
          title: "read",
          kind: "read",
          status: "in_progress",
          ts: 1000,
        }),
      ]
    : [...entries, at(2, "turn_close", { outcome, elapsed_ms: 92_000 })];
}

/** A chat's turns, each naming the row its footer pins, if any. */
type Chat = { entries: Entry[]; row?: Row }[];

/** Chats that seat every row where production paints it, per settled outcome: the older rows with
 *  an enabled or absent Rewind in an idle chat (an absent one followed by a promptless turn), ended
 *  by its first newest row; each further newest row alone; and the disabled rows ahead of a running
 *  tail, which makes the chat busy. The first such tail pins the running row. */
function chats(): Chat[] {
  const out: Chat[] = [];
  const seat = (r: Row): Chat[number] => ({
    entries: turn(r.outcome, r.actions === "actions", true),
    row: r,
  });
  const live = ROWS.filter((r) => r.outcome === "running");
  for (const o of DIMENSIONS.outcome.filter((x) => x !== "running")) {
    const of = ROWS.filter((r) => r.outcome === o);
    const older = of
      .filter((r) => r.position === "older" && r.rewind !== "disabled")
      .flatMap((r) =>
        r.rewind === "absent" ? [seat(r), { entries: turn("completed", true, false) }] : [seat(r)],
      );
    const [tail, ...lone] = of.filter((r) => r.position === "newest").map(seat);
    out.push([...older, ...(tail === undefined ? [] : [tail])]);
    for (const c of lone) {
      out.push([c]);
    }
    const running = live.shift();
    out.push([
      ...of.filter((r) => r.rewind === "disabled").map(seat),
      {
        entries: turn("running", true, true),
        ...(running === undefined ? {} : { row: running }),
      },
    ]);
  }
  return out;
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

function cards(root: Element | null): HTMLElement[] {
  return root === null ? [] : [...root.querySelectorAll<HTMLElement>(":scope > .turn")];
}

/** Every chat painted in turn as the active one, which `mountRewind` reads; each transcript view
 *  in chat order. */
async function paint(list: readonly Chat[]): Promise<HTMLElement[]> {
  store.setSessions(
    list.map((chat, i) => {
      const id = `closed-${String(i)}`;
      const turns = new Map<string, TurnState>();
      for (const { entries } of chat) {
        const closeAt = entries.findIndex((e) => e.kind === "turn_close");
        turns.set((entries[0] as Entry).turn, {
          entries: [...entries],
          openEntries: new Map(),
          ...(closeAt === -1 ? {} : { closeAt }),
        });
      }
      observeStamp({ kind: "chat", ref: id, version: "1" });
      return {
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
        usage: {
          context_pct: 0,
          context_size: 0,
          credits: 0,
          last_turn_ms: 0,
          has_real_data: false,
        },
      };
    }),
  );
  const views: HTMLElement[] = [];
  for (const [i, chat] of list.entries()) {
    store.setActive(`closed-${String(i)}`);
    await until(
      () => cards(messages.activeTranscriptView()).length === chat.length,
      `chat ${String(i)}'s ${String(chat.length)} cards`,
    );
    views.push((messages.activeTranscriptView() as HTMLElement).cloneNode(true) as HTMLElement);
  }
  return views;
}

const STAND_IN = "data-closed-row-stand-in";
/** Reverts every stylesheet rule on a stand-in and its pseudo-elements: `all` leaves out
 *  `direction` and `unicode-bidi` (CSS Cascade 4, the `all` shorthand), and an important
 *  declaration in the first-declared layer outranks every other author declaration a stylesheet
 *  holds (CSS Cascade 5, cascade layers), so this sheet goes ahead of the bundle. */
const RESET = `@layer closed-row-stand-in {
  [${STAND_IN}], [${STAND_IN}]::before, [${STAND_IN}]::after, [${STAND_IN}]::marker {
    all: revert !important;
    direction: revert !important;
    unicode-bidi: revert !important;
  }
}`;

const frames: HTMLIFrameElement[] = [];
/** Scenario id to its footer. */
const footers = new Map<string, HTMLElement>();
/** A row's footer in the dark theme, which the geometry cases read. */
const darkFooter = (r: Row, width: number): HTMLElement =>
  footers.get(`dark ${String(width)} ${rowKey(r)}`) as HTMLElement;

// 27 waits (21 chats painted, 6 copies settled), each settling within a few frames: one bound.
beforeAll(
  async () => {
    const list = chats();
    const views = await paint(list);
    for (const theme of THEMES) {
      for (const width of WIDTHS) {
        const frame = document.createElement("iframe");
        frame.width = String(width);
        frame.height = "400";
        document.body.appendChild(frame);
        frames.push(frame);
        const inner = frame.contentDocument as Document;
        inner.documentElement.dataset["theme"] = theme;
        inner.documentElement.dataset["pointer"] = width === 1280 ? "fine" : "coarse";
        const reset = inner.createElement("style");
        reset.textContent = RESET;
        const sheet = inner.createElement("style");
        sheet.textContent = bundle.textContent;
        inner.head.append(reset, sheet);
        const host = inner.createElement("div");
        host.id = "messages";
        inner.body.appendChild(host);
        for (const [i, view] of views.entries()) {
          const copy = inner.importNode(view, true);
          host.appendChild(copy);
          cards(copy).forEach((card, j) => {
            const row = list[i]?.[j]?.row;
            if (row !== undefined) {
              const footer = card.querySelector(":scope > .turn-footer") as HTMLElement;
              footers.set(`${theme} ${String(width)} ${rowKey(row)}`, footer);
            }
          });
        }
        // A copy fades in from `@starting-style` like a painted card; a snapshot mid-fade reads the clock.
        await until(
          () =>
            [...host.querySelectorAll(".turn-footer")].every(
              (f) => inner.defaultView?.getComputedStyle(f).opacity === "1",
            ),
          `${theme} ${String(width)} px footers settle`,
        );
      }
    }
  },
  testTimeoutFor(FRAME_BUDGET_MS * 2),
);

afterAll(() => {
  for (const f of frames) {
    f.remove();
  }
});

interface Box {
  x?: number;
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
// the word, the trailing controls by their spacing and the last one's distance from the end.
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
  actionGaps: [2, 2],
  rewind: ["Rewind to this point", { y: 4, h: 24 }],
  rewindGap: 8,
  end: 8,
  rewindChrome: 36,
  glyphs: { actions: 3, size: 16 },
} as const;
const COARSE = {
  band: 44,
  ledger: { x: 8, y: 4, h: 36 },
  info: { x: 12, y: 12, w: 20, h: 20 },
  mark: { x: 40, y: 18, w: 8, h: 8 },
  ledgerGap: 8,
  actions: [["More turn actions", { y: 4, w: 36, h: 36 }]],
  actionGaps: [],
  rewind: ["Rewind to this point", { y: 4, w: 36, h: 36 }],
  rewindGap: 8,
  end: 8,
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

/** Scenario id, then element path in document order, then property in name order. */
type Snapshot = Record<string, Record<string, Record<string, string>>>;

/** The footer and every element under it but the open panel's subtree, which the redesign owns;
 *  a path names each element by tag, classes and label, so a diff line says which part moved, and
 *  each comes with its depth below the footer. */
function rowElements(footer: Element): [string, Element, number][] {
  const out: [string, Element, number][] = [];
  const walk = (e: Element, path: string, depth: number): void => {
    out.push([path, e, depth]);
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
      walk(c, `${path} > ${name}${n === 1 ? "" : `:${String(n)}`}`, depth + 1);
    }
  };
  walk(footer, "footer", 0);
  return out;
}

/** Each document's enumerated properties but custom ones (tokens paint only through the
 *  properties that read them) and the host-dependent few: one list, which every element's computed
 *  style enumerates alike and which costs a binding call per name to rebuild. */
const PROPERTIES = new WeakMap<Document, string[]>();

/** Those properties' values, copied: a computed style is live. */
function read(e: Element, pseudo: string): Map<string, string> {
  const doc = e.ownerDocument;
  const style = (doc.defaultView as Window).getComputedStyle(e, pseudo);
  let names = PROPERTIES.get(doc);
  if (names === undefined) {
    names = [...style].filter((p) => !p.startsWith("--") && !HOST_DEPENDENT.has(p));
    PROPERTIES.set(doc, names);
  }
  return new Map(names.map((p) => [p, style.getPropertyValue(p)]));
}

/** Each row element's style and each used pseudo-element's, every property in it that differs from
 *  a stand-in's: the same tag just ahead of the element, so a structural match counted from the
 *  start is the element's, it and its pseudo-elements under `RESET`, so it inherits what the element
 *  inherits and holds what the user agent alone gives the tag there. What remains is what the markup
 *  and the stylesheet decide, and a property a browser adds or drops reads alike on both. Stand-ins
 *  join in waves of one depth and sibling index, so no two share a parent or nest, and a wave costs
 *  one style and layout pass where one stand-in at a time would cost one each. */
function snapshot(): Snapshot {
  const out: Snapshot = {};
  const waves = new Map<
    string,
    { id: string; path: string; e: Element; pseudos: string[]; mine: Map<string, string>[] }[]
  >();
  // Every element read before any stand-in joins the tree, where it moves later siblings' matches.
  for (const id of SCENARIOS) {
    const parts: Record<string, Record<string, string>> = {};
    for (const [path, e, depth] of rowElements(footers.get(id) as HTMLElement)) {
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
      for (const p of pseudos) {
        parts[`${path}${p}`] = {};
      }
      const index = [...(e.parentElement as Element).children].indexOf(e);
      const wave = `${String(depth)} ${String(index)}`;
      const list = waves.get(wave) ?? [];
      list.push({
        id,
        path,
        e,
        pseudos,
        mine: pseudos.map((p) => read(e, p)),
      });
      waves.set(wave, list);
    }
    out[id] = parts;
  }
  for (const wave of waves.values()) {
    const stands = wave.map(({ e }) => {
      const stand = e.ownerDocument.createElementNS(e.namespaceURI, e.localName);
      stand.setAttribute(STAND_IN, "");
      e.before(stand);
      return stand;
    });
    wave.forEach(({ id, path, pseudos, mine }, i) => {
      pseudos.forEach((p, j) => {
        const base = read(stands[i] as Element, p);
        (out[id] as Snapshot[string])[`${path}${p}`] = Object.fromEntries(
          [...(mine[j] as Map<string, string>)]
            .filter(([prop, v]) => base.get(prop) !== v)
            .sort(([a], [b]) => (a < b ? -1 : a > b ? 1 : 0)),
        );
      });
    });
    for (const stand of stands) {
      stand.remove();
    }
  }
  return out;
}

/** A value written once where every scenario agrees, otherwise by the fewest `FACTORS` that decide
 *  it: `by` names them, and `is` maps each combination of theme, width or row values to the value
 *  (a combination left out has the stand-in's). */
type Factored = string | { by: string; is: Record<string, string> };

/** The snapshot as committed, by element path: `where` (factored the same way, its `in` the
 *  combinations that render the path, `, `-separated) when not every scenario renders it, and each
 *  property, so a recapture's diff shows a change once and not on every scenario it repeats on. */
type Fixture = Record<
  string,
  { where?: { by: string; in: string }; style: Record<string, Factored> }
>;

/** Every set of factor indices, fewest first, then in mask order. */
const SUBSETS = [...Array(2 ** FACTORS.length).keys()]
  .sort((a, b) => a.toString(2).split("1").length - b.toString(2).split("1").length || a - b)
  .map((mask) => FACTORS.flatMap((_, i) => ((mask >> i) & 1 ? [i] : [])));

/** The id's value of each factor in `by`. */
function keyOf(id: string, by: readonly number[]): string {
  const parts = id.split(" ");
  return by.map((i) => parts[i]).join(" ");
}

/** The fewest factors, the earliest on a tie, that `value` is a function of over `ids`. */
function factor(ids: readonly string[], value: (id: string) => string | undefined): number[] {
  for (const by of SUBSETS) {
    const seen = new Map<string, string | undefined>();
    if (
      ids.every((id) => {
        const k = keyOf(id, by);
        const v = value(id);
        if (!seen.has(k)) {
          seen.set(k, v);
        }
        return seen.get(k) === v;
      })
    ) {
      return by;
    }
  }
  throw new Error("every factor together names one scenario, so the full set always fits");
}

function names(by: readonly number[]): string {
  return by.map((i) => FACTORS[i]).join(" ");
}

function encode(live: Snapshot): Fixture {
  const out: Fixture = {};
  const paths = [...new Set(SCENARIOS.flatMap((id) => Object.keys(live[id] ?? {})))];
  for (const path of paths) {
    const rendered = SCENARIOS.filter((id) => live[id]?.[path] !== undefined);
    const of = (id: string): Record<string, string> => live[id]?.[path] ?? {};
    const style: Record<string, Factored> = {};
    for (const p of [...new Set(rendered.flatMap((id) => Object.keys(of(id))))].sort()) {
      const by = factor(rendered, (id) => of(id)[p]);
      if (by.length === 0) {
        style[p] = of(rendered[0] as string)[p] as string;
        continue;
      }
      const is: Record<string, string> = {};
      for (const id of rendered) {
        const v = of(id)[p];
        if (v !== undefined) {
          is[keyOf(id, by)] = v;
        }
      }
      style[p] = { by: names(by), is };
    }
    if (rendered.length === SCENARIOS.length) {
      out[path] = { style };
      continue;
    }
    const by = factor(SCENARIOS, (id) => (live[id]?.[path] === undefined ? undefined : ""));
    const where = [...new Set(rendered.map((id) => keyOf(id, by)))].join(", ");
    out[path] = { where: { by: names(by), in: where }, style };
  }
  return out;
}

function decode(fixture: Fixture): Snapshot {
  const indices = (by: string): number[] => by.split(" ").map((f) => FACTORS.indexOf(f));
  const out: Snapshot = {};
  for (const id of SCENARIOS) {
    const parts: Record<string, Record<string, string>> = {};
    for (const [path, { where, style }] of Object.entries(fixture)) {
      if (where !== undefined && !where.in.split(", ").includes(keyOf(id, indices(where.by)))) {
        continue;
      }
      const own: Record<string, string> = {};
      for (const [p, v] of Object.entries(style)) {
        const value = typeof v === "string" ? v : v.is[keyOf(id, indices(v.by))];
        if (value !== undefined) {
          own[p] = value;
        }
      }
      parts[path] = own;
    }
    out[id] = parts;
  }
  return out;
}

/** One line per property whose value differs, so a red run names the scenario, the part and the
 *  property. */
function differences(want: Snapshot, got: Snapshot): string[] {
  const lines: string[] = [];
  const keys = (a: object, b: object): string[] =>
    [...new Set([...Object.keys(a), ...Object.keys(b)])].sort();
  for (const id of SCENARIOS) {
    const w = want[id] ?? {};
    const g = got[id] ?? {};
    for (const path of keys(w, g)) {
      const wp = w[path];
      const gp = g[path];
      const at = `${id} ${path}`;
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
  it("seats every possible combination of its dimensions in a chat that paints it", () => {
    expect(
      ROWS.length,
      "8 outcomes x 3 Rewinds x 2 places x 2 action sets, less `impossible`",
    ).toBe(57);
    expect(
      SCENARIOS.filter((id) => !footers.has(id)),
      "scenarios without a painted footer",
    ).toEqual([]);
  });

  // Every property of each row element and of its stand-in in 342 footers: about 7 s on an idle host.
  it(
    "keeps every element's decided style in both themes at 320, 390 and 1280 px",
    { timeout: 60_000 },
    async () => {
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
      for (const [id, footer] of footers) {
        const view = footer.ownerDocument.defaultView as Window;
        const card = view.getComputedStyle(footer.closest(".turn") as Element).fontFamily;
        for (const [path, e] of rowElements(footer)) {
          const family = view.getComputedStyle(e).fontFamily;
          expect(family, `${id} ${path}: the card's font`).toBe(card);
        }
      }
    },
  );

  for (const row of ROWS) {
    for (const width of [320, 390, 1280] as const) {
      it(`keeps the ${rowKey(row)} row's items, their order and their boxes at ${String(width)} px`, () => {
        const footer = darkFooter(row, width);
        const pin = PIN[width];
        const { word, fact } = OUTCOMES[row.outcome];
        const actions = row.actions === "actions";
        const rewinds = row.rewind !== "absent";
        expect(footer.dataset["outcome"], "the outcome").toBe(row.outcome);
        expect(footer.dataset["info"], "closed").not.toBe("open");
        expect(
          [...footer.children].map((c) => c.className),
          "the row's items in order",
        ).toEqual([
          "turn-ledger-summary",
          "turn-info-panel",
          ...(actions ? ["turn-actions-buttons"] : []),
          ...(rewinds ? ["turn-rewind"] : []),
        ]);
        const rewind = footer.querySelector<HTMLButtonElement>(":scope > .turn-rewind");
        expect(rewind?.disabled, "Rewind's state").toBe(
          rewinds ? row.rewind === "disabled" : undefined,
        );
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
        expect(part("turn-ledger-text").textContent, "the outcome word").toBe(word);
        expect(part("turn-fact").textContent, "the lead fact").toBe(fact);
        const shownParts = [
          "turn-ledger-info",
          ...(word === "" ? [] : ["turn-ledger-glyph", "turn-ledger-text"]),
          "turn-fact",
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
        ).toEqual(word === "" ? null : pin.mark);
        const shown = shownParts.map(part);
        gaps(shown).forEach((g, i) => {
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
        const want = [...(actions ? pin.actions : []), ...(rewinds ? [pin.rewind] : [])];
        expect(
          controls.map((c) => c.getAttribute("aria-label")),
          "the trailing controls",
        ).toEqual(want.map(([label]) => label));
        controls.forEach((c, i) => {
          const b = want[i]?.[1] as Box;
          expect(box(c, footer, b), c.getAttribute("aria-label") ?? "").toEqual(b);
        });
        const last = controls.at(-1)?.getBoundingClientRect().right;
        const end = footer.getBoundingClientRect().right;
        expect(
          last === undefined ? null : Math.round((end - last) * 100) / 100,
          "the last control's distance from the footer's end",
        ).toBe(want.length === 0 ? null : pin.end);
        // Within a layout unit: a box after a word starts on a 1/64 px grid, not a whole pixel.
        const pinned = [
          ...(actions ? pin.actionGaps : []),
          ...(actions && rewinds ? [pin.rewindGap] : []),
        ];
        const between = gaps(controls);
        expect(between, "the space between the trailing controls").toHaveLength(pinned.length);
        between.forEach((g, i) => {
          expect(g, `the space after control ${String(i)}`).toBeCloseTo(pinned[i] as number, 1);
        });
        expect(
          rewind === null ? null : Math.round(rewindChrome(rewind) * 10) / 10,
          "Rewind less its word",
        ).toBe(rewinds ? pin.rewindChrome : null);
        const glyphs = [...footer.querySelectorAll("svg")].filter((g) => g.checkVisibility());
        expect(glyphs.length, "the row's shown glyphs: the `i`, the actions', Rewind's").toBe(
          1 + (actions ? pin.glyphs.actions : 0) + (rewinds ? 1 : 0),
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
