// One turn holding two of the agent's messages (a reply, then a second reply a workflow notification woke it for):
// each message is its own block in the body, live and on reload, a minted segment between them bridging neither; the
// folded face shows the turn's whole last message, a drawn entry inside it included; both copies keep a blank line
// between blocks. The real renderer runs through the real store.

import { describe, it, expect, vi, beforeEach } from "vitest";
import type { Session } from "./types.js";
import { makeSession } from "./__test-helpers__/model.js";
import type { Entry, OpenEntry } from "./wire/types.gen.js";
import type * as Scroll from "./scroll.js";
import type * as ActionsMessages from "./actions/messages.js";

// The import graph reaches the DOM registry, which throws on a missing app root.
for (const id of [
  "messages",
  "messages-wrap",
  "messages-wrap-outer",
  "chat-view",
  "scroll-bottom",
  "send-btn",
  "prompt-input",
]) {
  const d = document.createElement(id === "prompt-input" ? "textarea" : "div");
  d.id = id;
  document.body.appendChild(d);
}

vi.mock("./scroll.js", async (importOriginal) => ({
  ...(await importOriginal<typeof Scroll>()),
  ...(await import("./__test-helpers__/scroll-mock.js")).scrollMock,
}));

// What a turn's Copy hands over.
const { copied } = vi.hoisted(() => ({ copied: [] as string[] }));
vi.mock("./actions/messages.js", async (importOriginal) => ({
  ...(await importOriginal<typeof ActionsMessages>()),
  copyClipboard: {
    dispatch: (text: string) => {
      copied.push(text);
      return Promise.resolve();
    },
  },
}));

const store = await import("./store.js");
const messages = await import("./messages.js");
const { setTurnOpen, resetFoldState } = await import("./fold-state.js");
const { KEY_ATTR } = await import("./reconcile.js");

messages.mountChatView();

// --- Fixtures -------------------------------------------------------------------------

const TURN = "t-797";
const NEXT = "t-798";
const PROMPT = "This should not be used exposed only parent agent";

// Two KAS messages, so two says: the reply to the prompt, then the reply to the notification.
const SAY_A = "bda5041c-say";
const SAY_B = "cd281ed9-say";
const REPLY_A =
  "Noted: no designer-effort setting.\n\n" +
  "- **My recommendation:** don't build the override editor.\n" +
  "- **Build it:** an editor for env vars.\n\n" +
  "Open behind this one: the two I recommend skipping, plus 2 other calls.";
const REPLY_B =
  "No change: run-row-paused is still waiting.\n\n" +
  "Still open: decision 21. Do you use an organization MCP registry?";

function sealed(
  turn: string,
  seq: number,
  kind: Entry["kind"],
  payload: unknown,
  id = `${turn}-e${String(seq)}`,
): Entry {
  return { id, turn, kind, seq, ts: seq + 1, payload };
}

function turnOpen(turn: string, n: number, text = PROMPT): Entry {
  return sealed(turn, 0, "turn_open", { prompt: { id: `${turn}-p`, text }, source: "prompt", n });
}

const CALL_ID = "run_command_1";

function callEntry(turn: string, seq: number): Entry {
  return sealed(
    turn,
    seq,
    "tool_call",
    { id: CALL_ID, title: "Record decision 20", kind: "execute", status: "completed", ts: 1 },
    CALL_ID,
  );
}

function resultEntry(turn: string, seq: number): Entry {
  return sealed(turn, seq, "tool_result", { status: "completed" }, `${CALL_ID}:result`);
}

/** Turn #797 as recorded: a command, its result, then two messages back to back. */
function turn797(): Entry[] {
  return [
    turnOpen(TURN, 1),
    callEntry(TURN, 1),
    resultEntry(TURN, 2),
    sealed(TURN, 3, "text", { text: REPLY_A }, SAY_A),
    sealed(TURN, 4, "text", { text: REPLY_B }, SAY_B),
    sealed(TURN, 5, "turn_close", { outcome: "completed" }),
  ];
}

/** A newer turn, so #797 is not the newest and folds by default. */
function nextTurn(): Entry[] {
  return [
    turnOpen(NEXT, 2, "next"),
    sealed(NEXT, 1, "text", { text: "ok" }, "next-say"),
    sealed(NEXT, 2, "turn_close", { outcome: "completed" }),
  ];
}

function load(chatID: string, turns: readonly (readonly Entry[])[]): void {
  const s: Session = makeSession({ id: chatID, name: chatID });
  for (const entries of turns) {
    const first = entries[0];
    if (first === undefined) {
      continue;
    }
    s.turns.set(first.turn, { entries: [...entries], openEntries: new Map() });
    s.turn_order.push(first.turn);
  }
  s.turn_count = s.turn_order.length;
  store.setSessions([s]);
  store.setActive(chatID);
  store.bumpMessages(chatID, "load");
}

function viewRoot(): HTMLElement {
  return messages.activeTranscriptView() ?? document.getElementById("messages")!;
}

function card(turnID: string): HTMLElement {
  for (const child of viewRoot().children) {
    if (child.getAttribute(KEY_ATTR) === turnID) {
      return child as HTMLElement;
    }
  }
  throw new Error(`no card for turn ${turnID}`);
}

/** The prose blocks under `scope` of `turnID`'s card, as rendered text. */
function blocks(turnID: string, scope: ".turn-body" | ".turn-face"): string[] {
  return [
    ...card(turnID).querySelectorAll<HTMLElement>(`:scope > ${scope} .message.assistant`),
  ].map((b) => b.textContent ?? "");
}

function copy(turnID: string, label: "Copy as text" | "Copy as markdown"): string {
  const btn = card(turnID).querySelector<HTMLButtonElement>(
    `.turn-action-btn[aria-label="${label}"]`,
  );
  if (btn === null) {
    throw new Error(`no ${label} button on turn ${turnID}`);
  }
  copied.length = 0;
  btn.click();
  return copied[copied.length - 1] ?? "";
}

beforeEach(() => {
  messages.teardownAll();
  localStorage.clear();
  resetFoldState();
  store.setSessions([]);
  store.setActive("");
  copied.length = 0;
});

// --- The body ---------------------------------------------------------------------------

describe("two messages in one turn's body", () => {
  it("render as two blocks after a reload, not one paragraph", async () => {
    const chat = "tlm-reload";
    setTurnOpen(chat, TURN, true);
    load(chat, [turn797(), nextTurn()]);

    await vi.waitFor(() => {
      expect(blocks(TURN, ".turn-body")).toHaveLength(2);
    });
    const [a, b] = blocks(TURN, ".turn-body");
    expect(a?.endsWith("plus 2 other calls.")).toBe(true);
    expect(b?.startsWith("No change:")).toBe(true);
  });

  it("render as two blocks while the second streams, and after it seals", async () => {
    const chat = "tlm-live";
    store.setSessions([makeSession({ id: chat, name: chat, thinking: true })]);
    store.setActive(chat);
    store.openTurn(chat, turnOpen(TURN, 1));
    store.appendEntry(chat, callEntry(TURN, 1));
    store.appendEntry(chat, resultEntry(TURN, 2));
    const open = (id: string, text: string): OpenEntry => ({
      turn: TURN,
      id,
      kind: "text",
      text,
      n: 1,
    });
    store.openEntry(chat, open(SAY_A, REPLY_A));
    store.sealEntry(chat, TURN, SAY_A, "", 3, 4, 1);
    store.openEntry(chat, open(SAY_B, "No change: run-row"));

    await vi.waitFor(() => {
      expect(blocks(TURN, ".turn-body")).toHaveLength(2);
    });
    expect(blocks(TURN, ".turn-body")[0]?.endsWith("plus 2 other calls.")).toBe(true);
    // One caret, on the message being written.
    const streaming = card(TURN).querySelectorAll(".message.assistant.streaming");
    expect(streaming).toHaveLength(1);
    expect(streaming[0]?.textContent.startsWith("No change")).toBe(true);

    store.applyDelta(chat, TURN, SAY_B, "", 2, REPLY_B.slice("No change: run-row".length));
    store.sealEntry(chat, TURN, SAY_B, "", 4, 5, 2);
    store.appendEntry(chat, sealed(TURN, 5, "turn_close", { outcome: "completed" }));

    await vi.waitFor(() => {
      expect(blocks(TURN, ".turn-body")[1]?.endsWith("organization MCP registry?")).toBe(true);
    });
    expect(blocks(TURN, ".turn-body")).toHaveLength(2);
  });

  it("keep ONE message's segments in one block, live and on reload", async () => {
    // A `tool_result` landing mid-message seals the say into `<say>#k` segments; nothing shows between them.
    const chat = "tlm-segments";
    store.setSessions([makeSession({ id: chat, name: chat, thinking: true })]);
    store.setActive(chat);
    store.openTurn(chat, turnOpen(TURN, 1));
    store.appendEntry(chat, callEntry(TURN, 1));
    store.openEntry(chat, { turn: TURN, id: SAY_A, kind: "text", text: "one **tw", n: 1 });
    store.sealEntry(chat, TURN, SAY_A, "", 2, 3, 1);
    store.appendEntry(chat, resultEntry(TURN, 3));
    store.openEntry(chat, { turn: TURN, id: `${SAY_A}#2`, kind: "text", text: "o** three", n: 1 });

    await vi.waitFor(() => {
      expect(blocks(TURN, ".turn-body")).toEqual(["one two three"]);
    });

    const s: Session = makeSession({ id: "tlm-segments-2", name: "r" });
    s.turns.set(TURN, {
      entries: [
        turnOpen(TURN, 1),
        callEntry(TURN, 1),
        sealed(TURN, 2, "text", { text: "one **tw" }, SAY_A),
        resultEntry(TURN, 3),
        sealed(TURN, 4, "text", { text: "o** three" }, `${SAY_A}#2`),
        sealed(TURN, 5, "turn_close", { outcome: "completed" }),
      ],
      openEntries: new Map(),
    });
    s.turn_order = [TURN];
    s.turn_count = 1;
    store.setSessions([s]);
    store.setActive(s.id);
    store.bumpMessages(s.id, "load");
    await vi.waitFor(() => {
      expect(card(TURN).querySelector(".turn-body .message.assistant strong")?.textContent).toBe(
        "two",
      );
    });
    expect(blocks(TURN, ".turn-body")).toEqual(["one two three"]);
  });
});

// --- The folded face ---------------------------------------------------------------------

describe("the folded turn's face", () => {
  it("shows the whole last message, both replies of it, with no opener", async () => {
    const chat = "tlm-face";
    load(chat, [turn797(), nextTurn()]);

    await vi.waitFor(() => {
      expect(card(TURN).hasAttribute("data-folded")).toBe(true);
    });
    const face = blocks(TURN, ".turn-face");
    expect(face).toHaveLength(2);
    expect(face[0]?.startsWith("Noted: no designer-effort setting.")).toBe(true);
    expect(face[0]?.endsWith("plus 2 other calls.")).toBe(true);
    expect(face[1]?.startsWith("No change:")).toBe(true);
    // The face is the answer in full: no clamp, so no "Show more" to open.
    expect(card(TURN).querySelector(".turn-face button")).toBeNull();
  });

  it("leaves prose BEFORE the last tool call to the body", async () => {
    const chat = "tlm-face-tool";
    const entries = [
      turnOpen(TURN, 1),
      sealed(TURN, 1, "text", { text: "Let me check." }, "early-say"),
      callEntry(TURN, 2),
      resultEntry(TURN, 3),
      sealed(TURN, 4, "text", { text: REPLY_B }, SAY_B),
      sealed(TURN, 5, "turn_close", { outcome: "completed" }),
    ];
    load(chat, [entries, nextTurn()]);

    await vi.waitFor(() => {
      expect(blocks(TURN, ".turn-face")).toHaveLength(1);
    });
    expect(blocks(TURN, ".turn-face")[0]?.startsWith("No change:")).toBe(true);
  });
});

// --- One message split by a drawn entry ---------------------------------------------------

const ACK_ID = "steer-m-1:ack";
const HEAD = "Your instinct is mostly right. Should I launch that?\n\n[";
const ACK = "Folded in as the start-of-work discovery check.";
const TAIL = "\n\nSTEERING steer-m-2: Compared your design.]";

/** A real shape: one say whose head and tail a steer_ack splits, then a trailing hook call. */
function ackSplit(): Entry[] {
  return [
    turnOpen(TURN, 1),
    callEntry(TURN, 1),
    resultEntry(TURN, 2),
    sealed(TURN, 3, "text", { text: HEAD }, SAY_A),
    sealed(TURN, 4, "steer_ack", { steer_id: "steer-m-1", text: ACK }, ACK_ID),
    sealed(TURN, 5, "text", { text: TAIL }, `${SAY_A}#2`),
    sealed(TURN, 6, "turn_close", { outcome: "completed" }),
  ];
}

/** One say split by two tool calls, as a seal at each call leaves it. */
function toolSplit(): Entry[] {
  const call = (seq: number, id: string): Entry =>
    sealed(
      TURN,
      seq,
      "tool_call",
      { id, title: "Run Command", kind: "execute", status: "completed", ts: 1 },
      id,
    );
  return [
    turnOpen(TURN, 1),
    sealed(TURN, 1, "text", { text: "Checking both." }, SAY_A),
    call(2, "c-1"),
    sealed(TURN, 3, "tool_result", { status: "completed" }, "c-1:result"),
    call(4, "c-2"),
    sealed(TURN, 5, "tool_result", { status: "completed" }, "c-2:result"),
    sealed(TURN, 6, "text", { text: "Both pass." }, `${SAY_A}#2`),
    sealed(TURN, 7, "turn_close", { outcome: "completed" }),
  ];
}

describe("one message a drawn entry splits", () => {
  it("shows all of it on the face when a steer_ack splits it", async () => {
    load("tlm-ack-face", [ackSplit(), nextTurn()]);
    await vi.waitFor(() => {
      expect(card(TURN).hasAttribute("data-folded")).toBe(true);
    });
    // A bubble's textContent runs its own paragraphs together ("that?[").
    expect(blocks(TURN, ".turn-face")).toEqual([
      "Your instinct is mostly right. Should I launch that?[",
      ACK,
      "STEERING steer-m-2: Compared your design.]",
    ]);
    expect(copy(TURN, "Copy as text")).toBe(
      `${PROMPT}\n\nYour instinct is mostly right. Should I launch that?[\n\n${ACK}\n\nSTEERING steer-m-2: Compared your design.]`,
    );
    expect(copy(TURN, "Copy as markdown")).toBe(`> ${PROMPT}\n\n${HEAD}\n\n${ACK}\n\n${TAIL}`);
  });

  it("shows all of it on the face when tool calls split it, and copies it open or folded", async () => {
    load("tlm-tool-face", [toolSplit(), nextTurn()]);
    await vi.waitFor(() => {
      expect(card(TURN).hasAttribute("data-folded")).toBe(true);
    });
    expect(blocks(TURN, ".turn-face")).toEqual(["Checking both.", "Both pass."]);
    expect(copy(TURN, "Copy as text")).toBe(`${PROMPT}\n\nChecking both.\n\nBoth pass.`);
    expect(copy(TURN, "Copy as markdown")).toBe(`> ${PROMPT}\n\nChecking both.\n\nBoth pass.`);

    const open = "tlm-tool-open";
    setTurnOpen(open, TURN, true);
    load(open, [toolSplit(), nextTurn()]);
    await vi.waitFor(() => {
      expect(blocks(TURN, ".turn-body")).toEqual(["Checking both.", "Both pass."]);
    });
    expect(copy(TURN, "Copy as text")).toBe(`${PROMPT}\n\nChecking both.\n\nBoth pass.`);
    expect(copy(TURN, "Copy as markdown")).toBe(`> ${PROMPT}\n\nChecking both.\n\nBoth pass.`);
  });
});

// --- A minted segment between two messages ------------------------------------------------

const MINTED = `${TURN}:e9`;

/** Two named messages with a minted (id-less) segment between them, which rides the first. */
function mintedBridge(): Entry[] {
  return [
    turnOpen(TURN, 1),
    callEntry(TURN, 1),
    resultEntry(TURN, 2),
    sealed(TURN, 3, "text", { text: "First reply." }, SAY_A),
    sealed(TURN, 4, "text", { text: " Still first." }, MINTED),
    sealed(TURN, 5, "text", { text: "Second reply." }, SAY_B),
    sealed(TURN, 6, "turn_close", { outcome: "completed" }),
  ];
}

describe("a minted segment between two messages", () => {
  it("bridges neither on a reload, nor in either copy", async () => {
    const chat = "tlm-minted";
    setTurnOpen(chat, TURN, true);
    load(chat, [mintedBridge(), nextTurn()]);
    await vi.waitFor(() => {
      expect(blocks(TURN, ".turn-body")).toEqual(["First reply. Still first.", "Second reply."]);
    });
    expect(copy(TURN, "Copy as text")).toBe(
      `${PROMPT}\n\nFirst reply. Still first.\n\nSecond reply.`,
    );
    expect(copy(TURN, "Copy as markdown")).toBe(
      `> ${PROMPT}\n\nFirst reply. Still first.\n\nSecond reply.`,
    );
  });

  it("bridges neither while the second message streams", async () => {
    const chat = "tlm-minted-live";
    store.setSessions([makeSession({ id: chat, name: chat, thinking: true })]);
    store.setActive(chat);
    store.openTurn(chat, turnOpen(TURN, 1));
    store.appendEntry(chat, callEntry(TURN, 1));
    store.appendEntry(chat, resultEntry(TURN, 2));
    const open = (id: string, text: string): OpenEntry => ({
      turn: TURN,
      id,
      kind: "text",
      text,
      n: 1,
    });
    store.openEntry(chat, open(SAY_A, "First reply."));
    store.sealEntry(chat, TURN, SAY_A, "", 3, 4, 1);
    store.openEntry(chat, open(MINTED, " Still first."));
    store.sealEntry(chat, TURN, MINTED, "", 4, 5, 1);
    store.openEntry(chat, open(SAY_B, "Second reply, still streaming"));

    // A live bubble paints its text over frames, so the words are awaited too.
    await vi.waitFor(() => {
      expect(blocks(TURN, ".turn-body")).toHaveLength(2);
      expect(blocks(TURN, ".turn-body")[0]).toBe("First reply. Still first.");
    });
    await vi.waitFor(() => {
      const streaming = card(TURN).querySelectorAll(".message.assistant.streaming");
      expect(streaming).toHaveLength(1);
      expect(streaming[0]?.textContent.startsWith("Second reply")).toBe(true);
    });
  });
});

// --- The copies --------------------------------------------------------------------------

describe("copying a turn holding two messages", () => {
  it("puts a blank line between them in copy-as-markdown", async () => {
    const chat = "tlm-md";
    setTurnOpen(chat, TURN, true);
    load(chat, [turn797(), nextTurn()]);
    await vi.waitFor(() => {
      expect(blocks(TURN, ".turn-body").length).toBeGreaterThan(0);
    });

    expect(copy(TURN, "Copy as markdown")).toBe(`> ${PROMPT}\n\n${REPLY_A}\n\n${REPLY_B}`);
  });

  it("puts a blank line between them in copy-as-text, open and folded", async () => {
    const chat = "tlm-text";
    setTurnOpen(chat, TURN, true);
    load(chat, [turn797(), nextTurn()]);
    await vi.waitFor(() => {
      expect(blocks(TURN, ".turn-body").length).toBeGreaterThan(0);
    });
    expect(copy(TURN, "Copy as text")).toContain("plus 2 other calls.\n\nNo change:");

    const folded = "tlm-text-folded";
    load(folded, [turn797(), nextTurn()]);
    await vi.waitFor(() => {
      expect(card(TURN).hasAttribute("data-folded")).toBe(true);
    });
    const text = copy(TURN, "Copy as text");
    expect(text).toContain("plus 2 other calls.\n\nNo change:");
    expect(text.startsWith(`${PROMPT}\n\nNoted:`)).toBe(true);
  });
});
