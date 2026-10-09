import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { userEvent } from "vitest/browser";

import { mountAppCSS } from "./__test-helpers__/css-rules.js";
import type { TurnSummary } from "./turn-rail.js";
import type * as ApiClient from "./api-client.js";
import type * as StoreLoad from "./store-load.js";

vi.mock("./api-client.js", async (importOriginal) => ({
  ...(await importOriginal<typeof ApiClient>()),
  apiGet: vi.fn(),
  apiGetTyped: vi.fn(),
}));
vi.mock("./store-load.js", async (importOriginal) => ({
  ...(await importOriginal<typeof StoreLoad>()),
  loadMessages: vi.fn(),
  loadList: vi.fn(),
}));

mountAppCSS();
document.body.style.margin = "0";

// The page's nesting, built before the scroll controller resolves it at import: `#chat-area` is
// the container the map's width query keys on, and the scroller has room to scroll.
const area = document.createElement("div");
area.id = "chat-area";
area.style.cssText = "position:fixed;top:0;left:0;margin:0;width:1120px;height:700px";
const view = document.createElement("div");
view.id = "chat-view";
view.style.cssText = "display:flex;flex-direction:column;flex:1;min-height:0";
const outer = document.createElement("div");
outer.id = "messages-wrap-outer";
const wrap = document.createElement("div");
wrap.id = "messages-wrap";
const messagesEl = document.createElement("div");
messagesEl.id = "messages";
const filler = document.createElement("div");
filler.style.blockSize = "5000px";
messagesEl.appendChild(filler);
wrap.appendChild(messagesEl);
const resume = document.createElement("button");
resume.id = "scroll-bottom";
resume.appendChild(document.createElement("span"));
outer.append(wrap, resume);
view.appendChild(outer);
area.appendChild(view);
document.body.appendChild(area);
for (const [id, tag] of [
  ["send-btn", "button"],
  ["prompt-input", "textarea"],
] as const) {
  const e = document.createElement(tag);
  e.id = id;
  document.body.appendChild(e);
}

const rail = await import("./turn-rail.js");
const { apiGet, apiGetTyped } = await import("./api-client.js");
const { runServerSearch, resetServerSearch } = await import("./chat-search.js");

rail.mountTurnRail(outer);

function turns(count: number): TurnSummary[] {
  return Array.from({ length: count }, (_, i) => ({
    id: `u${String(i + 1)}`,
    n: i + 1,
    outcome: "completed" as const,
    ts: (i + 1) * 60_000,
    first_line: `request ${String(i + 1)}`,
  }));
}

const links = (): HTMLAnchorElement[] => [
  ...outer.querySelectorAll<HTMLAnchorElement>(".turn-map .turn-pill-link"),
];

async function settle(): Promise<void> {
  const root = outer.querySelector(".turn-map");
  let last = "";
  let stable = 0;
  for (let i = 0; i < 120 && stable < 3; i++) {
    await new Promise((r) => requestAnimationFrame(() => setTimeout(r, 0)));
    const now = root?.innerHTML ?? "";
    stable = now === last ? stable + 1 : 0;
    last = now;
  }
}

async function show(id: string, count: number): Promise<void> {
  vi.mocked(apiGet).mockResolvedValue({ turns: turns(count) } as never);
  await rail.loadTurnRail(id);
  await settle();
}

beforeEach(() => {
  rail.resetTurnRail();
});

afterEach(() => {
  resetServerSearch();
  document.querySelector(".uip-tooltip")?.remove();
});

describe("the box the map bins against", () => {
  it("bins a long chat on its first paint after a short one", async () => {
    await show("c-short", 3);
    expect(links()).toHaveLength(3);

    vi.mocked(apiGet).mockResolvedValue({ turns: turns(1000) } as never);
    await rail.loadTurnRail("c-long");
    const first = links().map((a) => a.getAttribute("aria-label"));
    await settle();
    const settled = links().map((a) => a.getAttribute("aria-label"));

    expect(settled.length).toBeLessThan(1000);
    expect(first, "the first paint already has the settled bins").toEqual(settled);
  });
});

describe("the rendered pitch", () => {
  it("draws 500 turns binned in pairs at 2px a row", async () => {
    await show("c-pitch", 500);
    const track = outer.querySelector(".turn-map-track")?.getBoundingClientRect().height ?? 0;
    expect(track, "a track that bins 500 turns in pairs").toBeGreaterThanOrEqual(500);
    expect(track, "a track that bins 500 turns in pairs").toBeLessThan(1000);
    const rows = links().map((a) => a.getBoundingClientRect().height);
    expect(rows).toHaveLength(250);
    expect(rows.every((h) => Math.abs(h - 2) < 0.5)).toBe(true);
    const stack = outer.querySelector(".turn-map-stack")?.getBoundingClientRect().height ?? 0;
    expect(Math.abs(stack - 500)).toBeLessThan(0.5);
  });
});

describe("the hover preview over the map", () => {
  it("lets the rows it covers take the pointer", async () => {
    await show("c-tip", 30);
    const target = links()[10];
    if (target === undefined) {
      throw new Error("no row 11");
    }
    const r = target.getBoundingClientRect();
    // The library's element, placed the way a preview anchored above row 12 lands over its
    // neighbours in the stack's column.
    const tip = document.createElement("div");
    tip.className = "uip-tooltip";
    tip.setAttribute("role", "tooltip");
    tip.textContent = "#12 · Completed\nrequest 12";
    tip.style.cssText = `inset-block-start:${String(r.top - 20)}px;inset-inline-start:${String(r.left - 120)}px;inline-size:240px;block-size:60px`;
    document.body.appendChild(tip);

    const hit = document.elementFromPoint(r.left + r.width / 2, r.top + r.height / 2);
    expect(hit?.closest(".turn-pill-link")).toBe(target);
  });
});

describe("a swipe over the map", () => {
  it("grows the rows under the pointer and writes nothing to the map", async () => {
    await show("c-swipe", 30);
    const rows = [...outer.querySelectorAll<HTMLElement>(".turn-map .turn-pill")];
    const stack = outer.querySelector(".turn-map-stack");
    if (stack === null) {
      throw new Error("no stack");
    }
    const writes: MutationRecord[] = [];
    const watch = new MutationObserver((records) => {
      writes.push(...records);
    });
    // Every attribute `paintMap` and `paintPill` write, plus the rows themselves.
    watch.observe(stack, {
      subtree: true,
      childList: true,
      attributes: true,
      attributeFilter: [
        "style",
        "class",
        "href",
        "tabindex",
        "aria-current",
        "aria-label",
        "data-tooltip",
        "data-severity",
        "data-trigger",
        "data-in-view",
        "data-current",
        "data-hit",
      ],
    });

    for (const i of [8, 9, 10, 11, 12]) {
      const target = rows[i]?.querySelector(".turn-pill-link");
      if (!(target instanceof HTMLElement)) {
        throw new Error(`no row ${String(i)}`);
      }
      await userEvent.hover(target);
    }
    for (const a of document.getAnimations()) {
      a.finish();
    }
    const scale = (i: number): number => {
      const link = rows[i]?.querySelector(".turn-pill-link");
      return link instanceof HTMLElement
        ? new DOMMatrixReadOnly(getComputedStyle(link, "::before").transform).a
        : Number.NaN;
    };
    expect(scale(12)).toBe(1);
    expect(scale(11)).toBeCloseTo(16 / 24, 4);
    expect(scale(13)).toBeCloseTo(16 / 24, 4);
    expect(scale(10)).toBeCloseTo(12 / 24, 4);
    expect(scale(14)).toBeCloseTo(12 / 24, 4);
    expect(scale(8)).toBeCloseTo(10 / 24, 4);

    writes.push(...watch.takeRecords());
    watch.disconnect();
    expect(writes, "no render reached the map during the swipe").toEqual([]);
    const after = [...outer.querySelectorAll<HTMLElement>(".turn-map .turn-pill")];
    expect(after).toHaveLength(rows.length);
    after.forEach((li, i) => {
      expect(li, `row ${String(i)} kept its element`).toBe(rows[i]);
    });
    await userEvent.hover(area, { position: { x: 1, y: 1 } });
  });
});

describe("a binned row's search mark", () => {
  it("marks the row when its only hit is a member after the first", async () => {
    await show("c-hit", 1000);
    const rows = links();
    const row = rows[Math.floor(rows.length / 2)];
    const range = /^Turns (\d+) to (\d+)$/u.exec(row?.getAttribute("aria-label") ?? "");
    if (row === undefined || range === null) {
      throw new Error("no binned middle row");
    }
    const first = Number(range[1]);
    const last = Number(range[2]);
    expect(last, "the bin holds more than one turn").toBeGreaterThan(first);

    vi.mocked(apiGetTyped).mockResolvedValue({
      matches: [
        {
          turn_id: "",
          entry_id: "e1",
          excerpt: "x",
          segment_kind: "text",
          turn: first + 1,
          offset: 0,
          segment_len: 1,
        },
      ],
      scanned: 1,
      matched: 1,
    } as never);
    await runServerSearch("c-hit", "x");
    rail.setResidentTurns([]);
    await settle();

    const li = row.closest<HTMLElement>(".turn-pill");
    expect(li?.hasAttribute("data-hit")).toBe(true);
    expect(row.getAttribute("data-tooltip")).toBe(
      `Turns ${String(first)}-${String(last)} · Completed · Search match`,
    );
    const marked = links().filter((a) => a.closest(".turn-pill")?.hasAttribute("data-hit"));
    expect(marked).toEqual([row]);
  });
});
