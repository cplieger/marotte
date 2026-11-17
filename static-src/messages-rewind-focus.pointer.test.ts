// Where focus lands after a Rewind run with a real mouse and the real confirm: on the composer once the rewind
// lands, on the clicked button with no ring after a refusal. Never on <body>.
import { describe, it, expect, vi, beforeAll, afterAll, beforeEach } from "vitest";
import { userEvent } from "vitest/browser";

import { mountAppCSS } from "./__test-helpers__/css-rules.js";
import { FRAME_BUDGET_MS } from "./__test-helpers__/frame-budget.js";
import { outlineOf } from "./__test-helpers__/focus-ring.js";
import type { TurnState } from "./types.js";
import type { Entry } from "./wire/types.gen.js";
import type * as Toast from "./toast.js";
import type * as StoreLoad from "./store-load.js";

vi.mock("./toast.js", async (importOriginal) => ({
  ...(await importOriginal<typeof Toast>()),
  ...(await import("./__test-helpers__/toast-mock.js")).toastMock(),
}));
// The network seam: the rewind command goes out through `send`.
vi.mock("./transport.js", { spy: true });
vi.mock("./api-client.js", { spy: true });
vi.mock("./store-load.js", async (importOriginal) => ({
  ...(await importOriginal<typeof StoreLoad>()),
  loadMessages: vi.fn(() => Promise.resolve(true)),
  loadList: vi.fn(),
}));

const outer = document.createElement("div");
outer.id = "messages-wrap-outer";
const wrap = document.createElement("div");
wrap.id = "messages-wrap";
wrap.tabIndex = -1;
wrap.style.cssText = "height:600px;overflow-y:auto;position:relative;";
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

const transport = await import("./transport.js");
const { resetActionFramework } = await import("./actions/__test-helpers__/action-test-setup.js");
const store = await import("./store.js");
const { observeStamp } = await import("./subject-versions.js");
const messages = await import("./messages.js");
const mockSend = vi.mocked(transport.send);

messages.mountChatView();

let style: HTMLStyleElement;

beforeAll(() => {
  style = mountAppCSS();
});

afterAll(() => {
  style.remove();
});

function promptTurn(n: number): Entry[] {
  const id = `t${String(n)}`;
  const at = (seq: number, kind: Entry["kind"], payload: unknown): Entry =>
    ({ id: `${id}-e${String(seq)}`, turn: id, kind, seq, ts: 1000, payload }) as Entry;
  return [
    at(0, "turn_open", { prompt: { id: `${id}-p`, text: `prompt ${id}` }, source: "prompt", n }),
    at(1, "text", { text: `reply ${id}` }),
    at(2, "turn_close", { outcome: "completed" }),
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

let seq = 0;

/** A settled chat of three prompt turns, active and painted; returns its id. */
async function paintChat(): Promise<string> {
  seq += 1;
  const id = `rf${String(seq)}`;
  const turns = new Map<string, TurnState>();
  const order: string[] = [];
  for (let n = 1; n <= 3; n++) {
    const entries = promptTurn(n);
    turns.set(`t${String(n)}`, { entries, openEntries: new Map(), closeAt: entries.length - 1 });
    order.push(`t${String(n)}`);
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
  store.setPainted(id);
  await until(() => cards().length === 3, `3 cards for ${id}`);
  return id;
}

function composer(): HTMLTextAreaElement {
  return document.getElementById("prompt-input") as HTMLTextAreaElement;
}

function rewindButton(): HTMLButtonElement {
  const btn = cards()[0]?.querySelector<HTMLButtonElement>(
    ":scope > .turn-footer > .turn-actions-buttons > .turn-rewind",
  );
  if (btn === null || btn === undefined) {
    throw new Error("the first turn has no Rewind button");
  }
  return btn;
}

/** Click the first turn's Rewind (cutting turns 2 and 3) and the confirm's Rewind. */
async function clickRewindAndConfirm(): Promise<void> {
  await userEvent.click(rewindButton());
  const ok = document.querySelector<HTMLButtonElement>("dialog.uip-ask[open] .uip-ask-ok");
  if (ok === null) {
    throw new Error("the click opened no confirm");
  }
  await userEvent.click(ok);
}

/** The server's record of the cut, as the stream delivers it after a landed revert. */
function landRevert(chatID: string): void {
  store.appendEntry(chatID, {
    id: "t2:revert",
    turn: "t1",
    kind: "turn_revert",
    seq: 3,
    ts: 1000,
    payload: { from: "t2", from_n: 2, through: "t3", kas_message_id: "t2-p", cause: "rewind" },
  } as Entry);
}

beforeEach(() => {
  resetActionFramework();
  mockSend.mockReset();
  mockSend.mockResolvedValue({ ok: true, status: 200 });
  composer().value = "";
  composer().blur();
});

describe("a landed rewind", () => {
  it("focuses the composer holding the rewound prompt", async () => {
    await paintChat();
    await clickRewindAndConfirm();
    await until(() => composer().value !== "", "the prefill");

    expect(composer().value).toBe("prompt t2");
    await vi.waitFor(() => {
      expect(document.activeElement).toBe(composer());
    });
  });

  it("focuses the composer holding a draft once the cut removed the clicked button", async () => {
    const chatID = await paintChat();
    composer().value = "my next idea";
    await clickRewindAndConfirm();
    await until(() => mockSend.mock.calls.length === 1, "the rewind command");
    landRevert(chatID);
    await until(() => cards().length === 1, "the cut turns gone");

    expect(composer().value).toBe("my next idea");
    await vi.waitFor(() => {
      expect(document.activeElement).toBe(composer());
    });
  });
});

describe("a refused rewind", () => {
  it("hands focus back to the clicked button with no ring", async () => {
    await paintChat();
    mockSend.mockResolvedValue({ ok: false, status: 409, error: "busy" });
    await clickRewindAndConfirm();
    await until(() => mockSend.mock.calls.length === 1, "the rewind command");
    await until(() => !rewindButton().disabled, "the button re-enabled");

    await vi.waitFor(() => {
      expect(document.activeElement).toBe(rewindButton());
    });
    expect(outlineOf(rewindButton())).toBe("none");
  });
});
