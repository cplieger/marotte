// The footer's extras: neither is a summary field, and each control is mounted by this module or
// `messages-turn-actions.ts` into a footer `buildTurnFooter` already returned.
import { describe, it, expect, vi, beforeAll, afterAll } from "vitest";
import { FRAME_BUDGET_MS } from "./__test-helpers__/frame-budget.js";
import { mountAppCSS } from "./__test-helpers__/css-rules.js";
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
vi.mock("./store-load.js", () => ({ loadMessages: vi.fn(), loadList: vi.fn() }));

const store = await import("./store.js");
const { observeStamp } = await import("./subject-versions.js");
const messages = await import("./messages.js");

messages.mountChatView();

let bundle: HTMLStyleElement;

beforeAll(() => {
  // The whole cascade: `.turn-actions-more { display: contents }` and its `::details-content` override put the buttons
  // inline. `:root` declares the fine values, and the coarse fallback never matches at 1280px.
  bundle = mountAppCSS();
});

afterAll(() => {
  bundle.remove();
});

/**
 * Settled, its `turn_close` carrying the outcome only. An empty `text` leaves `settledProse` false while the turn is
 * still drawn by `turn_open.source`.
 */
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
function nextChat(): string {
  seq += 1;
  return `x${String(seq)}`;
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

/** `compactionWatermark` names an entry; every turn up to the one holding it is summarized, so it has no Branch. */
async function paint(
  turnEntries: readonly (readonly Entry[])[],
  expected = turnEntries.length,
  compactionWatermark?: string,
  id = nextChat(),
): Promise<HTMLElement[]> {
  const turns = new Map<string, TurnState>();
  const order: string[] = [];
  for (const entries of turnEntries) {
    const first = entries[0];
    if (first === undefined) {
      continue;
    }
    // `closeAt` as the store seats it, or the turn reads as still running.
    const closeAt = entries.find((e) => e.kind === "turn_close")?.seq;
    turns.set(first.turn, {
      entries: [...entries],
      openEntries: new Map(),
      ...(closeAt === undefined ? {} : { closeAt }),
    });
    order.push(first.turn);
  }
  // A held `chat` version marks the window fresh, or the activation refetches into the mocked loader.
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
      ...(compactionWatermark === undefined ? {} : { compaction_watermark: compactionWatermark }),
      thinking: false,
      working_label: "",
      usage: {
        context_pct: 0,
        context_size: 0,
        credits: 0,
        last_turn_ms: 0,
        has_real_data: false,
      },
    },
  ]);
  store.setActive(id);
  // A repaint of the active chat: `setSessions` alone bumps no transcript version.
  store.bumpMessages(id);
  await until(() => cards().length === expected, `${String(expected)} cards for ${id}`);
  // The footer fades in from `opacity: 0`, so a control read before that settles shows the pre-transition value.
  await until(
    () =>
      [...document.querySelectorAll(".turn-footer")].every(
        (f) => getComputedStyle(f).opacity === "1",
      ),
    "the footers' entry transition to settle",
  );
  return cards();
}

function footerOf(card: HTMLElement): HTMLElement | null {
  return card.querySelector<HTMLElement>(":scope > .turn-footer");
}

/** On screen means a painted box plus an accessible name; opacity is read only after the footer's entry transition. */
function controlShows(el: HTMLElement | null): boolean {
  if (el === null || el.hidden || !el.checkVisibility({ opacityProperty: true })) {
    return false;
  }
  const box = el.getBoundingClientRect();
  const name = el.getAttribute("aria-label") ?? el.textContent ?? "";
  return box.width > 0 && box.height > 0 && name.trim() !== "";
}

/** The footer module's own two channels, so a case can say the extra earned the footer through its control. */
function footerPaintsText(footer: HTMLElement): boolean {
  const fact = footer.querySelector<HTMLElement>(":scope > .turn-ledger-summary > .turn-fact");
  const word = footer.querySelector<HTMLElement>(
    ":scope > .turn-ledger-summary > .turn-ledger-text",
  );
  for (const el of [fact, word]) {
    if (el !== null && !el.hidden && el.checkVisibility() && (el.textContent ?? "") !== "") {
      return true;
    }
  }
  return false;
}

describe("a footer earned by an extra mounts that extra's control", () => {
  it("mounts a visible Rewind for a turn earned by `rewindable` alone", async () => {
    // Turn 1 did nothing; its only reason is the trigger turn 2 gives it.
    const [first, second] = await paint([promptTurn(1, ""), promptTurn(2, "reply")]);
    expect(first).toBeDefined();
    expect(second).toBeDefined();
    const footer = footerOf(first as HTMLElement);
    expect(footer, "the turn earned a footer").not.toBeNull();
    expect(footerPaintsText(footer as HTMLElement), "and it earned it by nothing else").toBe(false);
    expect(
      controlShows(
        (footer as HTMLElement).querySelector<HTMLElement>(
          ":scope > .turn-actions-buttons > .turn-rewind",
        ),
      ),
      "so Rewind is what paints",
    ).toBe(true);
  });

  it("mounts visible turn actions for a turn earned by `settledProse` alone", async () => {
    // Turn 2 is last, so no rewind target, and its prose has no ledger.
    const [, second] = await paint([promptTurn(1, ""), promptTurn(2, "reply")]);
    expect(second).toBeDefined();
    const footer = footerOf(second as HTMLElement);
    expect(footer, "the turn earned a footer").not.toBeNull();
    expect(footerPaintsText(footer as HTMLElement), "and it earned it by nothing else").toBe(false);
    expect(
      (footer as HTMLElement).querySelector(":scope > .turn-actions-buttons > .turn-rewind"),
      "the last turn has no rewind target",
    ).toBeNull();
    const actions = (footer as HTMLElement).querySelector<HTMLElement>(
      ":scope > .turn-actions-buttons",
    );
    expect(controlShows(actions), "so the actions row is what paints").toBe(true);
    // `:not(.turn-action-more)` excludes the `…` trigger, unpainted while the actions fit.
    const buttons = [
      ...(actions?.querySelectorAll<HTMLElement>(".turn-action-btn:not(.turn-action-more)") ?? []),
    ];
    expect(buttons.length, "and it carries real buttons").toBeGreaterThan(0);
    expect(
      buttons.every((b) => controlShows(b)),
      "every one of them visible",
    ).toBe(true);
  });

  it("mounts a visible Branch for a turn earned by `branchable` alone", async () => {
    // The last turn, no prose and no ledger: only its own prompt gives it a footer.
    const [only] = await paint([promptTurn(1, "")]);
    expect(only).toBeDefined();
    const footer = footerOf(only as HTMLElement);
    expect(footer, "the turn earned a footer").not.toBeNull();
    expect(footerPaintsText(footer as HTMLElement), "and it earned it by nothing else").toBe(false);
    const branch = (footer as HTMLElement).querySelector<HTMLElement>(
      '.turn-actions-buttons .turn-action-btn[aria-label="Branch from here"]',
    );
    expect(controlShows(branch), "so Branch is what paints").toBe(true);
  });
});

describe("Rewind beside a turn the agent started", () => {
  it("is enabled once every turn has closed", async () => {
    const [first] = await paint([promptTurn(1, ""), promptTurn(2, "reply")]);
    const btn = footerOf(first as HTMLElement)?.querySelector<HTMLButtonElement>(
      ":scope > .turn-actions-buttons > .turn-rewind",
    );
    expect(btn?.disabled).toBe(false);
  });

  it("is disabled while that turn is open, before anything streams", async () => {
    // `thinking` stays false: only a streamed entry latches it, and this turn has none yet.
    const wake: Entry[] = [
      {
        id: "t3-e0",
        turn: "t3",
        kind: "turn_open",
        seq: 0,
        ts: 1000,
        payload: { source: "wire_turn_start", n: 3 },
      } as Entry,
    ];
    // An open turn with nothing in it draws no card yet.
    const [first] = await paint([promptTurn(1, ""), promptTurn(2, "reply"), wake], 2);
    const btn = footerOf(first as HTMLElement)?.querySelector<HTMLButtonElement>(
      ":scope > .turn-actions-buttons > .turn-rewind",
    );
    expect(btn, "turn 1 has a rewind target").toBeDefined();
    expect(btn?.disabled, "KAS refuses a revert while a turn runs").toBe(true);
    expect(btn?.getAttribute("data-tooltip")).toBe(
      "Cannot rewind while the agent is running. Cancel the turn first",
    );
  });

  it("leaves no empty actions container once the turn stops being a rewind target", async () => {
    // Cancelled keeps the footer once Rewind goes; no prose and the watermark keep the container Rewind's alone.
    const first = promptTurn(1, "", "cancelled");
    const id = nextChat();
    const [before] = await paint([first, promptTurn(2, "reply")], 2, "t1-e1", id);
    const footer = footerOf(before as HTMLElement);
    expect(
      footer?.querySelector(":scope > .turn-actions-buttons > :only-child.turn-rewind"),
      "Rewind is the container's only control",
    ).not.toBeNull();
    // A rewind past turn 2 leaves turn 1 newest, with no next trigger to target.
    const [after] = await paint([first], 1, "t1-e1", id);
    expect(footerOf(after as HTMLElement), "the same footer repainted in place").toBe(footer);
    expect(footer?.querySelector(":scope > .turn-actions-buttons")).toBeNull();
  });
});
