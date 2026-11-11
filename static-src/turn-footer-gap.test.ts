// THE FOOTER'S TRAILING ICON ROW, painted by `messages.ts`: Rewind sits in the actions container, and every
// adjacent pair of controls is exactly that container's gap apart, at desktop and phone width on both pointer
// tiers, on an older turn, a turn with changed files (panel shut and open), a busy chat and the newest turn.
import { describe, it, expect, vi, beforeAll, afterAll, afterEach } from "vitest";
import { page } from "vitest/browser";
import { FRAME_BUDGET_MS } from "./__test-helpers__/frame-budget.js";
import { mountAppCSS } from "./__test-helpers__/css-rules.js";
import type * as StoreLoad from "./store-load.js";
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
  ...(await importOriginal<typeof StoreLoad>()),
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

afterEach(async () => {
  document.documentElement.removeAttribute("data-pointer");
  await page.viewport(1280, 720);
});

const FILES = {
  "a.ts": { lines_added: 3, lines_removed: 1 },
  "b.ts": { lines_added: 2, lines_removed: 0 },
};

function promptTurn(n: number, files?: typeof FILES): Entry[] {
  const id = `t${String(n)}`;
  const at = (seq: number, kind: Entry["kind"], payload: unknown): Entry =>
    ({ id: `${id}-e${String(seq)}`, turn: id, kind, seq, ts: 1000, payload }) as Entry;
  return [
    at(0, "turn_open", { prompt: { id: `${id}-p`, text: `prompt ${id}` }, source: "prompt", n }),
    at(1, "text", { text: "reply" }),
    at(2, "turn_close", {
      outcome: "completed",
      ...(files === undefined ? {} : { changed_files: files }),
    }),
  ];
}

/** An agent-started turn still open with nothing streamed, which makes the chat busy. */
function wakeTurn(n: number): Entry[] {
  const id = `t${String(n)}`;
  return [
    {
      id: `${id}-e0`,
      turn: id,
      kind: "turn_open",
      seq: 0,
      ts: 1000,
      payload: { source: "wire_turn_start", n },
    } as Entry,
  ];
}

let seq = 0;

async function until(pred: () => boolean, what: string): Promise<void> {
  const deadline = Date.now() + FRAME_BUDGET_MS;
  while (!pred()) {
    if (Date.now() > deadline) {
      throw new Error(`timed out after ${String(FRAME_BUDGET_MS)}ms waiting for ${what}`);
    }
    await new Promise((r) => setTimeout(r, 8));
  }
}

function frames(): Promise<void> {
  return new Promise((resolve) => {
    requestAnimationFrame(() => {
      requestAnimationFrame(() => {
        resolve();
      });
    });
  });
}

function cards(): HTMLElement[] {
  const root = messages.activeTranscriptView();
  return root === null ? [] : [...root.querySelectorAll<HTMLElement>(":scope > .turn")];
}

async function paint(turnEntries: readonly Entry[][], expected: number): Promise<HTMLElement[]> {
  seq += 1;
  const id = `gap${String(seq)}`;
  const turns = new Map<string, TurnState>();
  const order: string[] = [];
  for (const entries of turnEntries) {
    const first = entries[0] as Entry;
    const closeAt = entries.find((e) => e.kind === "turn_close")?.seq;
    turns.set(first.turn, {
      entries: [...entries],
      openEntries: new Map(),
      ...(closeAt === undefined ? {} : { closeAt }),
    });
    order.push(first.turn);
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
      turn_order: order,
      turn_count: order.length,
      has_more: false,
      residency: "loaded",
      thinking: false,
      working_label: "",
      usage: { context_pct: 0, context_size: 0, credits: 0, last_turn_ms: 0, has_real_data: false },
    },
  ]);
  store.setActive(id);
  store.setPainted(id);
  await until(() => cards().length === expected, `${String(expected)} cards for ${id}`);
  await until(
    () =>
      [...document.querySelectorAll(".turn-footer")].every(
        (f) => getComputedStyle(f).opacity === "1",
      ),
    "the footers' entry transition to settle",
  );
  return cards();
}

function footerOf(card: HTMLElement | undefined): HTMLElement {
  const footer = card?.querySelector<HTMLElement>(":scope > .turn-footer");
  if (footer === null || footer === undefined) {
    throw new Error("the turn has no footer");
  }
  return footer;
}

/** The painted trailing controls in reading order: the actions, `…` when the fit shows it, Rewind. */
function trailing(footer: HTMLElement): HTMLElement[] {
  return [
    ...footer.querySelectorAll<HTMLElement>(
      ".turn-actions-buttons :is(button, summary), .turn-rewind",
    ),
  ].filter((c) => c.checkVisibility());
}

function gapsOf(controls: readonly HTMLElement[]): number[] {
  return controls.slice(1).map((c, i) => {
    const prev = (controls[i] as HTMLElement).getBoundingClientRect();
    return Math.round((c.getBoundingClientRect().left - prev.right) * 100) / 100;
  });
}

const VIEWPORTS = [
  ["desktop", 1280, 720],
  ["phone", 390, 844],
] as const;
const TIERS = ["fine", "coarse"] as const;

/** Every adjacent gap equals the container's own `column-gap` and each other within 0.5 px. */
function expectEvenRow(footer: HTMLElement, where: string, rewinds: boolean): void {
  const slot = footer.querySelector<HTMLElement>(":scope > .turn-actions-buttons");
  expect(slot, `${where}: the actions container`).not.toBeNull();
  const rewind = footer.querySelector<HTMLElement>(".turn-rewind");
  const controls = trailing(footer);
  const gaps = gapsOf(controls);
  const want = Number.parseFloat(getComputedStyle(slot as HTMLElement).columnGap);
  console.log(
    `[gap] ${where}: ${controls.map((c) => c.getAttribute("aria-label")).join(" | ")} gaps=${JSON.stringify(gaps)} container=${String(want)}`,
  );
  // Every row here paints at least two controls, the newest turn's inline actions included.
  expect(gaps.length, `${where}: adjacent pairs`).toBeGreaterThanOrEqual(1);
  for (const [i, g] of gaps.entries()) {
    expect(
      Math.abs(g - want),
      `${where}: gap ${String(i)} (${String(g)}px) vs the container's ${String(want)}px`,
    ).toBeLessThanOrEqual(0.5);
  }
  expect(
    gaps.length === 0 ? 0 : Math.max(...gaps) - Math.min(...gaps),
    `${where}: gaps ${JSON.stringify(gaps)}`,
  ).toBeLessThanOrEqual(0.5);
  if (rewinds) {
    expect(rewind?.parentElement, `${where}: Rewind is a child of the actions container`).toBe(
      slot,
    );
    expect(controls.at(-1), `${where}: Rewind keeps the far edge`).toBe(rewind);
  } else {
    expect(rewind, `${where}: no Rewind`).toBeNull();
  }
}

describe("the footer's trailing icons are evenly spaced, Rewind included", () => {
  for (const [name, w, h] of VIEWPORTS) {
    for (const tier of TIERS) {
      it(`at ${name} width on the ${tier} tier`, async () => {
        document.documentElement.dataset["pointer"] = tier;
        await page.viewport(w, h);
        const [older, withFiles, newest] = await paint(
          [promptTurn(1), promptTurn(2, FILES), promptTurn(3)],
          3,
        );
        await frames();
        expectEvenRow(footerOf(older), `${name} ${tier} older turn`, true);
        expectEvenRow(footerOf(withFiles), `${name} ${tier} changed-files turn`, true);
        expectEvenRow(footerOf(newest), `${name} ${tier} newest turn`, false);

        // The changed-file rows open beneath the row and leave its spacing alone.
        const filesFooter = footerOf(withFiles);
        filesFooter.querySelector<HTMLButtonElement>(":scope > .turn-ledger-summary")?.click();
        await until(() => filesFooter.dataset["info"] === "open", "the info panel to open");
        await frames();
        const rows = [...filesFooter.querySelectorAll<HTMLElement>(".turn-file-row")].filter((r) =>
          r.checkVisibility(),
        );
        expect(rows, "both changed files listed").toHaveLength(2);
        const rowBottom = Math.max(
          ...trailing(filesFooter).map((c) => c.getBoundingClientRect().bottom),
        );
        const fb = filesFooter.getBoundingClientRect();
        for (const r of rows) {
          const b = r.getBoundingClientRect();
          expect(b.top, "a file row sits below the icon row").toBeGreaterThanOrEqual(
            rowBottom - 0.5,
          );
          expect(b.left, "inside the footer").toBeGreaterThanOrEqual(fb.left - 0.5);
          expect(b.right, "inside the footer").toBeLessThanOrEqual(fb.right + 0.5);
        }
        expectEvenRow(filesFooter, `${name} ${tier} changed-files turn, panel open`, true);
      });

      it(`with Rewind disabled mid-turn at ${name} width on the ${tier} tier`, async () => {
        document.documentElement.dataset["pointer"] = tier;
        await page.viewport(w, h);
        const [first] = await paint([promptTurn(1), promptTurn(2), wakeTurn(3)], 2);
        await frames();
        const footer = footerOf(first);
        expect(footer.querySelector<HTMLButtonElement>(".turn-rewind")?.disabled, "busy").toBe(
          true,
        );
        expectEvenRow(footer, `${name} ${tier} busy chat`, true);
      });
    }
  }
});
