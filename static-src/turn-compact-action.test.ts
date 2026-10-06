// A context-overflowed turn offers ONE way out: "Compact the context" under its notice, on the
// newest turn only, until a compaction lands after it. Through the real paint: the conditions span
// the turn, its chat and the entries after its close.
import { describe, it, expect, vi } from "vitest";
import { FRAME_BUDGET_MS } from "./__test-helpers__/frame-budget.js";
import type { TurnState } from "./types.js";
import type { Entry } from "./wire/types.gen.js";
import { makeSession } from "./__test-helpers__/model.js";
import type * as Scroll from "./scroll.js";
import type * as ApiClient from "./api-client.js";
import type * as ChatActions from "./actions/chat.js";

for (const id of [
  "messages",
  "messages-wrap",
  "messages-wrap-outer",
  "chat-view",
  "scroll-bottom",
]) {
  const d = document.createElement("div");
  d.id = id;
  document.body.appendChild(d);
}

vi.mock("./scroll.js", async (importOriginal) => ({
  ...(await importOriginal<typeof Scroll>()),
  ...(await import("./__test-helpers__/scroll-mock.js")).scrollMock,
}));
vi.mock("./api-client.js", async (importOriginal) => ({
  ...(await importOriginal<typeof ApiClient>()),
  apiGet: vi.fn(() => Promise.resolve(null)),
}));

const dispatch = vi.hoisted(() => vi.fn());
vi.mock("./actions/chat.js", async (importOriginal) => ({
  ...(await importOriginal<typeof ChatActions>()),
  compactChat: { dispatch },
}));

const { mountChatView, activeTranscriptView } = await import("./messages.js");
const { setSessions, setActive, bumpMessages } = await import("./store.js");

mountChatView();

const OVERFLOW =
  "This chat reached the model's context limit. Compact the context, then send the prompt again.";

function sealed(turnID: string, seq: number, kind: Entry["kind"], payload: unknown): Entry {
  return {
    id: `${turnID}-e${String(seq)}`,
    turn: turnID,
    lane: "",
    kind,
    seq,
    ts: seq + 1,
    payload,
  } as Entry;
}

function turnEntries(
  id: string,
  n: number,
  close: Record<string, unknown>,
  after: Entry["kind"][] = [],
): Entry[] {
  const rows = [
    sealed(id, 0, "turn_open", { prompt: { id: `${id}-p`, text: "go" }, source: "prompt", n }),
    sealed(id, 1, "text", { text: "Working." }),
    sealed(id, 2, "turn_close", close),
  ];
  after.forEach((kind, i) => rows.push(sealed(id, 3 + i, kind, { summary: "s" })));
  return rows;
}

const overflowed = { outcome: "failed", failure_reason: OVERFLOW, failure_kind: "context_limit" };
const otherFailure = { outcome: "failed", failure_reason: "ACP bridge exited" };

function cards(): HTMLElement[] {
  const root = activeTranscriptView();
  return root === null ? [] : [...root.querySelectorAll<HTMLElement>(":scope > .turn")];
}

async function mount(chatID: string, turnRows: Entry[][]): Promise<HTMLElement[]> {
  const turns = new Map<string, TurnState>();
  const order: string[] = [];
  for (const entries of turnRows) {
    const first = entries[0];
    if (first !== undefined) {
      turns.set(first.turn, { entries: [...entries], openEntries: new Map() });
      order.push(first.turn);
    }
  }
  setSessions([
    {
      ...makeSession({ id: chatID, name: chatID }),
      turns,
      turn_order: order,
      turn_count: order.length,
    },
  ]);
  setActive(chatID);
  bumpMessages(chatID, "load");
  const deadline = Date.now() + FRAME_BUDGET_MS;
  while (cards().length !== order.length) {
    if (Date.now() > deadline) {
      throw new Error("timed out waiting for cards");
    }
    await new Promise((r) => setTimeout(r, 8));
  }
  return cards();
}

const button = (card: HTMLElement | undefined): HTMLButtonElement | null =>
  card?.querySelector<HTMLButtonElement>(":scope > .turn-notice-actions > button") ?? null;

describe("the overflow notice's Compact button", () => {
  it("is offered on the newest turn and compacts that chat", async () => {
    const [card] = await mount("c-overflow", [turnEntries("t1", 1, overflowed)]);
    const btn = button(card);
    expect(btn?.textContent).toBe("Compact the context");
    btn?.click();
    expect(dispatch).toHaveBeenCalledWith({ chatID: "c-overflow" });
  });

  it("is not offered for another failure", async () => {
    const [card] = await mount("c-other", [turnEntries("t1", 1, otherFailure)]);
    expect(card?.querySelector(":scope > .turn-notice")).not.toBeNull();
    expect(button(card)).toBeNull();
  });

  it("is not offered on an older turn", async () => {
    const [older] = await mount("c-older", [
      turnEntries("t1", 1, overflowed),
      turnEntries("t2", 2, { outcome: "completed" }),
    ]);
    expect(older?.querySelector(":scope > .turn-notice")).not.toBeNull();
    expect(button(older)).toBeNull();
  });

  it("is withdrawn once a compaction lands after the turn", async () => {
    const [card] = await mount("c-compacted", [turnEntries("t1", 1, overflowed, ["compaction"])]);
    expect(card?.querySelector(":scope > .turn-notice")).not.toBeNull();
    expect(button(card)).toBeNull();
  });
});
