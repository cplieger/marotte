// A card's ordinal and its anchor id both come from `turn_open.n`, so a `#turn-N` fragment names the turn the rail's
// index calls N. Real paint over the real renderer; no geometry is staged.
import { describe, it, expect, vi, beforeEach } from "vitest";
import { FRAME_BUDGET_MS } from "./__test-helpers__/frame-budget.js";
import { makeSession } from "./__test-helpers__/model.js";
import type { TurnState } from "./types.js";
import type { Entry } from "./wire/types.gen.js";

// Nested as the page does: `#messages-wrap` is the scroller inside the positioned wrapper.
const outer = document.createElement("div");
outer.id = "messages-wrap-outer";
const wrap = document.createElement("div");
wrap.id = "messages-wrap";
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

// The rail's index is its own fetch; spy-wrapped, since the graph links a dozen other exports.
vi.mock("./api-client.js", { spy: true });

const store = await import("./store.js");
const messages = await import("./messages.js");
const { apiGet } = await import("./api-client.js");

messages.mountChatView();

function sealed(turnID: string, at: number, kind: Entry["kind"], payload: unknown): Entry {
  return {
    id: `${turnID}-e${String(at)}`,
    turn: turnID,
    kind,
    seq: at,
    ts: at + 1,
    payload,
  } as Entry;
}

/** A reader-opened turn at ordinal `n`: its `turn_open`, one text entry, its close. */
function promptTurn(n: number, request: string): Entry[] {
  const id = `t${String(n)}`;
  return [
    sealed(id, 0, "turn_open", {
      prompt: { id: `${id}-p`, text: request },
      source: "prompt",
      n,
    }),
    sealed(id, 1, "text", { text: `reply ${String(n)}` }),
    sealed(id, 2, "turn_close", { outcome: "completed" }),
  ];
}

/** An agent-initiated turn: no `prompt`, so `turnIsDrawn` admits it on its text entry. */
function agentTurn(n: number): Entry[] {
  const id = `t${String(n)}`;
  return [
    sealed(id, 0, "turn_open", { source: "agent", n }),
    sealed(id, 1, "text", { text: `unprompted ${String(n)}` }),
    sealed(id, 2, "turn_close", { outcome: "completed" }),
  ];
}

let seq = 0;
/** `setActive` is a no-op for the active id, so each case gets a fresh one. */
function nextChat(): string {
  seq += 1;
  return `n${String(seq)}`;
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

/** What the reader sees: each header's `#N`, in document order. */
function renderedNumbers(): string[] {
  return cards().map((c) => c.querySelector(".turn-n")?.textContent ?? "");
}

function anchorIDs(): string[] {
  return cards().map((c) => c.id);
}

/** `turn_count` is the whole chat's, so a three-turn window of fourteen is honest about its left edge. */
async function paint(
  id: string,
  turnEntries: readonly (readonly Entry[])[],
  total: number,
): Promise<void> {
  const turns = new Map<string, TurnState>();
  const order: string[] = [];
  for (const entries of turnEntries) {
    const first = entries[0];
    if (first === undefined) {
      continue;
    }
    turns.set(first.turn, { entries: [...entries], openEntries: new Map() });
    order.push(first.turn);
  }
  store.setSessions([
    {
      ...makeSession({ id, name: id, has_more: order.length < total }),
      turns,
      turn_order: order,
      turn_count: total,
    },
  ]);
  store.setActive(id);
  store.bumpMessages(id, "load");
  await until(
    () => cards().length === order.length,
    `${String(order.length)} cards to mount for ${id}`,
  );
}

beforeEach(() => {
  vi.mocked(apiGet).mockResolvedValue({ turns: [] });
});

describe("a paged transcript's rendered turn numbers", () => {
  it("numbers a window from `turn_open.n`, and the anchor id agrees", async () => {
    // Turns 12-14 of 14: a renderer counting window positions would read #1..#3.
    const id = nextChat();
    await paint(
      id,
      [promptTurn(12, "twelfth"), promptTurn(13, "thirteenth"), promptTurn(14, "last")],
      14,
    );

    expect(renderedNumbers()).toEqual(["#12", "#13", "#14"]);
    // The anchor moves with the number; one paint writes both and nothing else compares them.
    expect(anchorIDs()).toEqual(["turn-12", "turn-13", "turn-14"]);
  });

  it("takes an agent-initiated turn's ordinal from `n` as well", async () => {
    // No `prompt` on the `turn_open`, so the ordinal must come from `n` rather than the prompt.
    const id = nextChat();
    await paint(id, [promptTurn(7, "seventh"), agentTurn(8)], 8);

    expect(renderedNumbers()).toEqual(["#7", "#8"]);
    expect(anchorIDs()).toEqual(["turn-7", "turn-8"]);
    const trigger = cards().map(
      (c) => c.querySelector<HTMLElement>(":scope > .turn-header")?.dataset["trigger"] ?? "",
    );
    expect(trigger).toEqual(["user", "system"]);
  });
});
