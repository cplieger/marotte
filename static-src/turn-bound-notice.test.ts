// A completed turn the model stopped at a bound says which bound under its body, in the stop ink, without
// the info panel being opened. Through the real paint: the notice is card-level and sits above the ledger.
import { describe, it, expect, vi } from "vitest";
import { FRAME_BUDGET_MS } from "./__test-helpers__/frame-budget.js";
import type { TurnState } from "./types.js";
import type { Entry } from "./wire/types.gen.js";
import { makeSession } from "./__test-helpers__/model.js";
import type * as Scroll from "./scroll.js";
import type * as ApiClient from "./api-client.js";

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

const { mountChatView, activeTranscriptView } = await import("./messages.js");
const { setSessions, setActive, bumpMessages } = await import("./store.js");

mountChatView();

function sealed(seq: number, kind: Entry["kind"], payload: unknown): Entry {
  return {
    id: `t1-e${String(seq)}`,
    turn: "t1",
    lane: "",
    kind,
    seq,
    ts: seq + 1,
    payload,
  } as Entry;
}

async function paint(chatID: string, close: Record<string, unknown>): Promise<HTMLElement> {
  const entries = [
    sealed(0, "turn_open", { prompt: { id: "t1-p", text: "go" }, source: "prompt", n: 1 }),
    sealed(1, "text", { text: "Here is the first half" }),
    sealed(2, "turn_close", { elapsed_ms: 4000, ...close }),
  ];
  const turns = new Map<string, TurnState>([["t1", { entries, openEntries: new Map() }]]);
  setSessions([
    { ...makeSession({ id: chatID, name: chatID }), turns, turn_order: ["t1"], turn_count: 1 },
  ]);
  setActive(chatID);
  bumpMessages(chatID, "load");
  const deadline = Date.now() + FRAME_BUDGET_MS;
  for (;;) {
    const card = activeTranscriptView()?.querySelector<HTMLElement>(":scope > .turn");
    if (card?.querySelector(":scope > .turn-footer") != null) {
      return card;
    }
    if (Date.now() > deadline) {
      throw new Error("timed out waiting for the card");
    }
    await new Promise((r) => setTimeout(r, 8));
  }
}

const notice = (card: HTMLElement): HTMLElement | null =>
  card.querySelector<HTMLElement>(":scope > .turn-notice");

describe("a turn stopped at a bound", () => {
  it("names the output limit above the ledger, in the stop ink", async () => {
    const card = await paint("c-max-tokens", {
      outcome: "completed",
      stop_reason_raw: "max_tokens",
      truncated: true,
    });
    const n = notice(card);
    expect(n?.textContent).toBe("The answer was cut off at the output limit.");
    expect(n?.dataset["severity"]).toBe("stopped");
    expect(n?.nextElementSibling?.classList.contains("turn-footer")).toBe(true);
  });

  it("names the request limit", async () => {
    const card = await paint("c-max-requests", {
      outcome: "completed",
      stop_reason_raw: "max_turn_requests",
      truncated: true,
    });
    expect(notice(card)?.textContent).toBe("The turn stopped at its request limit.");
  });

  it("shows no notice on a clean turn", async () => {
    const card = await paint("c-clean", { outcome: "completed", stop_reason_raw: "end_turn" });
    expect(notice(card)).toBeNull();
  });
});
