// WHEN THE FOOTER'S `…` MENU SHOWS, painted by `messages.ts`: the actions sit inline whenever they fit, `…` holds
// only actions that do not fit and never just one, and a footer with three actions or fewer never shows it.
import { describe, it, expect, vi, beforeAll, afterAll, afterEach } from "vitest";
import { page } from "vitest/browser";
import { FRAME_BUDGET_MS } from "./__test-helpers__/frame-budget.js";
import { mountAppCSS } from "./__test-helpers__/css-rules.js";
import type * as StoreLoad from "./store-load.js";
import type { TurnState } from "./types.js";
import type { Entry, TurnOutcome } from "./wire/types.gen.js";

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

function promptTurn(n: number, text: string, outcome: TurnOutcome = "completed"): Entry[] {
  const id = `t${String(n)}`;
  const at = (seq: number, kind: Entry["kind"], payload: unknown): Entry =>
    ({ id: `${id}-e${String(seq)}`, turn: id, kind, seq, ts: 1000, payload }) as Entry;
  const body: Entry[] = text === "" ? [] : [at(1, "text", { text })];
  return [
    at(0, "turn_open", { prompt: { id: `${id}-p`, text: `prompt ${id}` }, source: "prompt", n }),
    ...body,
    at(body.length + 1, "turn_close", { outcome }),
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

function frame(): Promise<void> {
  return new Promise((resolve) => {
    requestAnimationFrame(() => {
      resolve();
    });
  });
}

/** Long enough for a resize to reach every footer's layout, which may take a frame of its own. */
async function settle(): Promise<void> {
  for (let i = 0; i < 4; i++) {
    await frame();
  }
}

function cards(): HTMLElement[] {
  const root = messages.activeTranscriptView();
  return root === null ? [] : [...root.querySelectorAll<HTMLElement>(":scope > .turn")];
}

/** `watermark` names an entry; the turn holding it and every turn before it are summarized, so none has Branch. */
async function paint(turnEntries: readonly Entry[][], watermark?: string): Promise<HTMLElement[]> {
  seq += 1;
  const id = `ovf${String(seq)}`;
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
      ...(watermark === undefined ? {} : { compaction_watermark: watermark }),
      thinking: false,
      working_label: "",
      usage: { context_pct: 0, context_size: 0, credits: 0, last_turn_ms: 0, has_real_data: false },
    },
  ]);
  store.setActive(id);
  await until(() => cards().length === turnEntries.length, `${String(turnEntries.length)} cards`);
  await until(
    () =>
      [...document.querySelectorAll(".turn-footer")].every(
        (f) => getComputedStyle(f).opacity === "1",
      ),
    "the footers' entry transition to settle",
  );
  await settle();
  return cards();
}

function footerOf(card: HTMLElement | undefined): HTMLElement {
  const footer = card?.querySelector<HTMLElement>(":scope > .turn-footer");
  if (footer === null || footer === undefined) {
    throw new Error("the turn has no footer");
  }
  return footer;
}

const ACTION = ".turn-action-btn:not(.turn-action-more), .turn-rewind";

/** What the reader sees of the footer's actions: the painted ones, the `…` opener and what its menu holds. */
function readRow(footer: HTMLElement): {
  inline: HTMLElement[];
  opener: HTMLElement | null;
  held: HTMLElement[];
  total: number;
} {
  const all = [...footer.querySelectorAll<HTMLElement>(ACTION)];
  const menu = footer.querySelector<HTMLDetailsElement>(".turn-actions-more");
  const summary = menu?.querySelector<HTMLElement>(":scope > .turn-action-more") ?? null;
  const opener = summary?.checkVisibility() === true ? summary : null;
  const held =
    opener === null ? [] : all.filter((b) => menu?.contains(b) === true && !b.checkVisibility());
  return { inline: all.filter((b) => b.checkVisibility()), opener, held, total: all.length };
}

/** Every painted control inside the footer's content box, clear of the outcome word wherever that is painted. */
function expectRowFits(footer: HTMLElement, where: string): void {
  const fs = getComputedStyle(footer);
  const fr = footer.getBoundingClientRect();
  const right = fr.right - Number.parseFloat(fs.paddingRight);
  const word = footer.querySelector<HTMLElement>(".turn-ledger-text");
  const wordBox = word?.getBoundingClientRect();
  const wordPainted =
    wordBox !== undefined && wordBox.width > 1 && (word?.textContent ?? "") !== "";
  const { inline, opener } = readRow(footer);
  for (const c of opener === null ? inline : [...inline, opener]) {
    const b = c.getBoundingClientRect();
    expect(
      b.right,
      `${where}: ${c.getAttribute("aria-label") ?? "…"} inside the footer`,
    ).toBeLessThanOrEqual(right + 0.5);
    if (wordPainted) {
      expect(b.left, `${where}: clear of the outcome word`).toBeGreaterThanOrEqual(
        (wordBox as DOMRect).right - 0.5,
      );
    }
  }
  if (wordPainted) {
    const ledger = footer.querySelector<HTMLElement>(
      ":scope > .turn-ledger-summary",
    ) as HTMLElement;
    expect(
      (wordBox as DOMRect).right,
      `${where}: the outcome word is whole inside its button`,
    ).toBeLessThanOrEqual(ledger.getBoundingClientRect().right + 0.5);
  }
}

/** The narrowest viewport the app lays out for, a 375px phone, a tablet and a desktop window, between them. */
const WIDTHS = [320, 340, 352, 375, 390, 414, 480, 560, 640, 720, 896, 1024, 1280];
const TIERS = ["fine", "coarse"] as const;

async function at(width: number, tier: (typeof TIERS)[number]): Promise<void> {
  document.documentElement.dataset["pointer"] = tier;
  await page.viewport(width, 800);
  await settle();
}

describe("the footer's … menu", () => {
  for (const tier of TIERS) {
    it(`never shows for the last turn's lone Branch at 375px on the ${tier} tier`, async () => {
      await at(375, tier);
      const [, last] = await paint([promptTurn(1, "reply"), promptTurn(2, "")]);
      const row = readRow(footerOf(last));
      expect(row.total, "Branch is the turn's one action").toBe(1);
      expect(row.opener, "no … for one action").toBeNull();
      expect(row.inline.map((b) => b.getAttribute("aria-label"))).toEqual(["Branch from here"]);
      expectRowFits(footerOf(last), `375 ${tier} lone Branch`);
    });

    it(`never shows for three actions at 375px on the ${tier} tier`, async () => {
      await at(375, tier);
      // The watermark in the newest turn summarizes both, so neither has Branch.
      const [, last] = await paint([promptTurn(1, "reply"), promptTurn(2, "reply")], "t2-e1");
      const row = readRow(footerOf(last));
      expect(row.total, "the three copy actions").toBe(3);
      expect(row.opener, "no … for three actions").toBeNull();
      expect(row.inline).toHaveLength(3);
      expectRowFits(footerOf(last), `375 ${tier} three copies`);
    });
  }

  it("holds only what does not fit, at least two, when two or more do not fit", async () => {
    // A coarse 352px row beside "Outcome unknown" has room for three of the five slots.
    await at(352, "coarse");
    const [first] = await paint([promptTurn(1, "reply", "unknown"), promptTurn(2, "reply")]);
    const footer = footerOf(first);
    const row = readRow(footer);
    expect(row.total, "copy ×3, Branch and Rewind").toBe(5);
    expect(row.opener, "the … opener").not.toBeNull();
    expect(row.held.length, "the menu holds at least two").toBeGreaterThanOrEqual(2);
    expect(row.inline.length + row.held.length, "every action is reachable").toBe(row.total);
    expectRowFits(footer, "352 coarse five actions");
    // Nothing that fits is held: the row has no room left for one more action and its gap.
    const slot = footer.querySelector<HTMLElement>(":scope > .turn-actions-buttons") as HTMLElement;
    const gap = Number.parseFloat(getComputedStyle(slot).columnGap);
    const step = (row.opener as HTMLElement).getBoundingClientRect().width + gap;
    const word = footer.querySelector<HTMLElement>(".turn-ledger-text") as HTMLElement;
    const free = slot.getBoundingClientRect().left - word.getBoundingClientRect().right;
    const lead = Number.parseFloat(getComputedStyle(slot).marginInlineStart);
    const ledgerPad = Number.parseFloat(
      getComputedStyle(footer.querySelector(".turn-ledger-summary") as HTMLElement)
        .paddingInlineEnd,
    );
    expect(free - lead - ledgerPad, "no room left for one more inline action").toBeLessThan(step);
    // The kept actions, `…` and Rewind share the container's one gap, as the inline row does.
    const painted = [...slot.querySelectorAll<HTMLElement>(":is(button, summary)")].filter((c) =>
      c.checkVisibility(),
    );
    const spacing = painted.slice(1).map((c, i) => {
      const prev = (painted[i] as HTMLElement).getBoundingClientRect();
      return Math.round((c.getBoundingClientRect().left - prev.right) * 100) / 100;
    });
    console.log(
      `[gap] 352 coarse menu row: ${painted.map((c) => c.getAttribute("aria-label")).join(" | ")} gaps=${JSON.stringify(spacing)} container=${String(gap)}`,
    );
    expect(painted.at(-1)?.getAttribute("aria-label"), "Rewind keeps the far edge").toBe("Rewind");
    for (const g of spacing) {
      expect(Math.abs(g - gap), `gaps ${JSON.stringify(spacing)}`).toBeLessThanOrEqual(0.5);
    }
  });

  // Measured: beside "Outcome unknown" five actions are one slot short from 322 to 346px fine and from 376 to
  // 418px coarse, and fit once the word is hidden.
  for (const [tier, width] of [
    ["fine", 336],
    ["coarse", 396],
  ] as const) {
    it(`hides the outcome word rather than hold one action, at ${String(width)}px on the ${tier} tier`, async () => {
      await at(1280, tier);
      const [first] = await paint([promptTurn(1, "reply", "unknown"), promptTurn(2, "reply")]);
      const footer = footerOf(first);
      const ledger = footer.querySelector<HTMLElement>(
        ":scope > .turn-ledger-summary",
      ) as HTMLElement;
      const word = footer.querySelector<HTMLElement>(".turn-ledger-text") as HTMLElement;
      expect(footer.hasAttribute("data-actions-tight"), "wide: the word has room").toBe(false);
      expect(word.getBoundingClientRect().width, "wide: the word is painted").toBeGreaterThan(1);

      await page.viewport(width, 800);
      await settle();
      const where = `${String(width)} ${tier} one slot short`;
      expect(footer.hasAttribute("data-actions-tight"), `${where}: tight`).toBe(true);
      const row = readRow(footer);
      expect(row.total, "copy ×3, Branch and Rewind").toBe(5);
      expect(row.opener, `${where}: no …`).toBeNull();
      expect(row.inline, `${where}: every action inline`).toHaveLength(5);
      expect(
        word.getBoundingClientRect().width,
        `${where}: the word is hidden`,
      ).toBeLessThanOrEqual(1);
      expectRowFits(footer, where);
      await expect
        .element(page.elementLocator(ledger), { timeout: FRAME_BUDGET_MS })
        .toHaveAccessibleName(/Outcome unknown/);
    });
  }

  it("measures nothing on a repaint that changed no input, and refits when the outcome changes", async () => {
    await at(336, "fine");
    const [first] = await paint([promptTurn(1, "reply", "unknown"), promptTurn(2, "reply")]);
    const footer = footerOf(first);
    expect(footer.hasAttribute("data-actions-tight"), "premise: the word is hidden").toBe(true);
    const id = store.getActiveId();
    const repaint = async (outcome: TurnOutcome): Promise<void> => {
      const s = store.get(id);
      const t = s?.turns.get("t1");
      if (s === undefined || t === undefined) {
        throw new Error("the chat has no first turn");
      }
      const entries = promptTurn(1, "reply", outcome);
      const close = entries.at(-1) as Entry;
      s.turns.set("t1", { ...t, entries, closeAt: close.seq });
      store.bumpMessages(id);
      await settle();
    };

    // A fit measures the shown word through a Range.
    const ranges = vi.spyOn(Range.prototype, "getBoundingClientRect");
    await repaint("unknown");
    const unchanged = ranges.mock.calls.length;
    ranges.mockRestore();
    await repaint("completed");

    expect(unchanged, "an unchanged repaint measures no ledger word").toBe(0);
    expect(footer.isConnected, "premise: the same footer").toBe(true);
    expect(
      footer.hasAttribute("data-actions-tight"),
      "the shorter word fits beside the actions",
    ).toBe(false);
    expectRowFits(footer, "336 fine after the word changed");
  });

  const CHATS = {
    // Five actions behind the two longest outcome words, five behind none, and the last turn's lone Branch.
    mixed: (): Promise<HTMLElement[]> =>
      paint([
        promptTurn(1, "reply", "unknown"),
        promptTurn(2, "reply", "interrupted"),
        promptTurn(3, "reply"),
        promptTurn(4, ""),
      ]),
    // Three copy actions on each turn, neither branchable; the first also has Rewind.
    summarized: (): Promise<HTMLElement[]> =>
      paint([promptTurn(1, "reply"), promptTurn(2, "reply")], "t2-e1"),
  };

  for (const tier of TIERS) {
    for (const [chat, open] of Object.entries(CHATS)) {
      it(`never holds exactly one action, at any width, in the ${chat} chat on the ${tier} tier`, async () => {
        await at(1280, tier);
        await open();
        for (const width of WIDTHS) {
          await page.viewport(width, 800);
          await settle();
          for (const [i, card] of cards().entries()) {
            const footer = footerOf(card);
            const where = `${chat} turn ${String(i + 1)} at ${String(width)} ${tier}`;
            const row = readRow(footer);
            if (row.total <= 3) {
              expect(row.opener, `${where}: no … for ${String(row.total)} actions`).toBeNull();
            }
            if (row.opener === null) {
              expect(row.inline, `${where}: every action painted`).toHaveLength(row.total);
            } else {
              expect(row.held.length, `${where}: … holds two or more`).toBeGreaterThanOrEqual(2);
              expect(row.inline.length + row.held.length, `${where}: all reachable`).toBe(
                row.total,
              );
            }
            expectRowFits(footer, where);
          }
        }
      });
    }
  }
});
