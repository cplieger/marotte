// A rewind hands the rewound prompt's text back to an empty composer. Every door (the footer button, `/rewind` and
// the composer chord through `rewindLatestTurn`, the refusal callout) runs the one flow driven here.
import { describe, it, expect, vi, beforeEach } from "vitest";
import { FRAME_BUDGET_MS } from "./__test-helpers__/frame-budget.js";
import type { TurnState } from "./types.js";
import type { Entry } from "./wire/types.gen.js";
import type * as Confirm from "./confirm.js";
import { confirmText } from "./__test-helpers__/confirm-text.js";
import type * as Toast from "./toast.js";
import type * as StoreLoad from "./store-load.js";

const mocks = vi.hoisted(() => ({
  confirm: vi.fn((_q: Confirm.Confirmation) => Promise.resolve(true)),
}));

vi.mock("./confirm.js", async (importOriginal) => ({
  ...(await importOriginal<typeof Confirm>()),
  confirm: mocks.confirm,
}));
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

/** Prompt turn `n`; each `calls` element is one edit tool call and the paths it touched. */
function promptTurn(n: number, calls: readonly (readonly string[])[] = []): Entry[] {
  const id = `t${String(n)}`;
  const at = (seq: number, kind: Entry["kind"], payload: unknown): Entry =>
    ({ id: `${id}-e${String(seq)}`, turn: id, kind, seq, ts: 1000, payload }) as Entry;
  const tools = calls.map((paths, i) =>
    at(2 + i, "tool_call", {
      id: `${id}-c${String(i)}`,
      title: "Edit",
      kind: "edit",
      status: "completed",
      ts: 1000,
      locations: paths.map((path) => ({ path })),
    }),
  );
  return [
    at(0, "turn_open", { prompt: { id: `${id}-p`, text: `prompt ${id}` }, source: "prompt", n }),
    at(1, "text", { text: `reply ${id}` }),
    ...tools,
    at(2 + tools.length, "turn_close", { outcome: "completed" }),
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

function cards(): HTMLElement[] {
  const root = messages.activeTranscriptView();
  return root === null ? [] : [...root.querySelectorAll<HTMLElement>(":scope > .turn")];
}

/** A settled chat of `count` prompt turns, active and painted, turn 2 making `calls`; returns its id. */
async function paintChat(
  count: number,
  calls: readonly (readonly string[])[] = [],
): Promise<string> {
  seq += 1;
  const id = `rw${String(seq)}`;
  const turns = new Map<string, TurnState>();
  const order: string[] = [];
  for (let n = 1; n <= count; n++) {
    const entries = promptTurn(n, n === 2 ? calls : []);
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
  await until(() => cards().length === count, `${String(count)} cards for ${id}`);
  return id;
}

function composer(): HTMLTextAreaElement {
  return document.getElementById("prompt-input") as HTMLTextAreaElement;
}

function rewindButtonOf(card: HTMLElement | undefined): HTMLButtonElement {
  const btn = card?.querySelector<HTMLButtonElement>(
    ":scope > .turn-footer > .turn-actions-buttons > .turn-rewind",
  );
  if (btn === null || btn === undefined) {
    throw new Error("the turn has no Rewind button");
  }
  return btn;
}

/** Click the first turn's Rewind (whose target is turn 2's prompt) and let the flow settle. */
async function rewindFirstTurn(): Promise<void> {
  rewindButtonOf(cards()[0]).click();
  await until(() => mocks.confirm.mock.calls.length > 0, "the confirm");
  await new Promise((r) => setTimeout(r, 0));
}

beforeEach(() => {
  resetActionFramework();
  mocks.confirm.mockReset();
  mocks.confirm.mockResolvedValue(true);
  mockSend.mockReset();
  mockSend.mockResolvedValue({ ok: true, status: 200 });
  composer().value = "";
  composer().blur();
});

describe("the footer's Rewind control", () => {
  it("is an icon-only button named Rewind, like the turn's other actions", async () => {
    await paintChat(2);
    const btn = rewindButtonOf(cards()[0]);

    expect(btn.getAttribute("aria-label")).toBe("Rewind");
    expect(btn.textContent.trim(), "no visible word").toBe("");
    expect([...btn.children].map((c) => c.getAttribute("class"))).toEqual(["ic-ui"]);
    expect(btn.getAttribute("data-tooltip"), "its tooltip is its name, as theirs are").toBe(
      "Rewind",
    );
  });
});

describe("a confirmed rewind", () => {
  it("puts the rewound prompt's text in an empty composer, focused with the caret at the end", async () => {
    await paintChat(3);
    await rewindFirstTurn();
    await until(() => composer().value !== "", "the prefill");

    expect(composer().value).toBe("prompt t2");
    expect(document.activeElement).toBe(composer());
    expect(composer().selectionStart).toBe("prompt t2".length);
    expect(composer().selectionEnd).toBe("prompt t2".length);
  });

  it("announces the prefill to the draft layer with an input event", async () => {
    await paintChat(3);
    const seen: string[] = [];
    const onInput = (): void => {
      seen.push(composer().value);
    };
    composer().addEventListener("input", onInput);
    try {
      await rewindFirstTurn();
      await until(() => seen.length > 0, "the input event");
    } finally {
      composer().removeEventListener("input", onInput);
    }
    expect(seen).toEqual(["prompt t2"]);
  });

  it("never overwrites a draft in the composer", async () => {
    await paintChat(3);
    composer().value = "my next idea";
    await rewindFirstTurn();
    await until(() => mockSend.mock.calls.length === 1, "the rewind command");
    await new Promise((r) => setTimeout(r, 0));

    expect(composer().value).toBe("my next idea");
  });

  it("from `rewindLatestTurn` (the /rewind and Ctrl/Cmd+Up door) gives back the newest prompt", async () => {
    const chatID = await paintChat(3);
    expect(await messages.rewindLatestTurn(chatID)).toBe(true);

    expect(composer().value).toBe("prompt t3");
  });
});

describe("a rewind that does not happen prefills nothing", () => {
  it("when the confirm is cancelled", async () => {
    await paintChat(3);
    mocks.confirm.mockResolvedValue(false);
    await rewindFirstTurn();

    expect(mockSend).not.toHaveBeenCalled();
    expect(composer().value).toBe("");
  });

  it("when the server refuses the revert", async () => {
    await paintChat(3);
    mockSend.mockResolvedValue({ ok: false, status: 409, error: "busy" });
    await rewindFirstTurn();
    await until(() => mockSend.mock.calls.length === 1, "the rewind command");
    await new Promise((r) => setTimeout(r, 0));

    expect(composer().value).toBe("");
  });

  it("when another chat became active while the revert was in flight", async () => {
    await paintChat(3);
    let reply: (r: { ok: boolean; status: number }) => void = () => undefined;
    mockSend.mockImplementation(
      () =>
        new Promise((resolve) => {
          reply = resolve;
        }),
    );
    await rewindFirstTurn();
    await until(() => mockSend.mock.calls.length === 1, "the rewind command");
    await paintChat(1);
    reply({ ok: true, status: 200 });
    await new Promise((r) => setTimeout(r, 0));
    await new Promise((r) => setTimeout(r, 0));

    expect(composer().value).toBe("");
  });
});

describe("the rewind confirm", () => {
  it("says the prompt's text comes back and its attachments do not, and never asks for a retype", async () => {
    await paintChat(3);
    mocks.confirm.mockResolvedValue(false);
    await rewindFirstTurn();

    const text = confirmText(mocks.confirm.mock.calls[0]?.[0]);
    expect(text).toMatch(/prompt's text comes back/);
    expect(text).toMatch(/Attachments are not restored/);
    expect(text).not.toMatch(/retype/);
    expect(text).toMatch(/This cannot be undone/);
  });

  it("asks a destructive question with a Rewind button", async () => {
    await paintChat(3);
    mocks.confirm.mockResolvedValue(false);
    await rewindFirstTurn();

    const q = mocks.confirm.mock.calls[0]?.[0];
    expect([q?.title, q?.confirmLabel, q?.variant]).toEqual([
      "Rewind to this turn?",
      "Rewind",
      "destructive",
    ]);
  });

  // Rewinding from the first card targets turn 2, so a chat of N turns drops N - 2 later ones.
  it.each([
    [2, "Removes this prompt and its reply."],
    [3, "Removes this prompt, its reply and the turn after it."],
    [4, "Removes this prompt, its reply and the 2 later turns."],
  ])("counts the later turns it removes in a chat of %i", async (count, removes) => {
    await paintChat(count);
    mocks.confirm.mockResolvedValue(false);
    await rewindFirstTurn();

    expect(mocks.confirm.mock.calls[0]?.[0].body?.split("\n")).toContain(removes);
  });

  it.each([
    ["no tool call", [], undefined],
    ["one call on one file", [["/w/src/a.ts"]], "Undoes 1 tool call, touching 1 file: a.ts."],
    [
      "calls on five files, listing four",
      [
        ["/w/a.ts", "/w/b.ts"],
        ["/w/c.ts", "/w/d.ts", "/w/e.ts"],
      ],
      "Undoes 2 tool calls, touching 5 files: a.ts, b.ts, c.ts, d.ts, \u2026",
    ],
  ] as const)("states the work it undoes for %s", async (_, calls, undoes) => {
    await paintChat(2, calls);
    mocks.confirm.mockResolvedValue(false);
    await rewindFirstTurn();

    const lines = mocks.confirm.mock.calls[0]?.[0].body?.split("\n") ?? [];
    expect(lines.find((l) => l.startsWith("Undoes"))).toBe(undoes);
  });
});
