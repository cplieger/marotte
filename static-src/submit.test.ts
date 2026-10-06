// What Send MEANS, over the REAL store with the send primitive, steer action and attachment row
// mocked: a mid-turn message reaches the RUNNING turn, a plain 409 steers while 409-starting does
// not, nothing shows before the server's frame, attachments degrade to paths, a failed send is
// recoverable in place.

import { describe, it, expect, vi, beforeEach } from "vitest";

// Declared via vi.hoisted so they exist when the hoisted vi.mock factories run.
const {
  mockSendPromptTo,
  mockSteer,
  mockQueue,
  mockTakeAttachments,
  mockHasAttachments,
  mockAddAttachmentTo,
  mockAttachmentGeneration,
  mockTypedCommand,
  mockInvokesCatalog,
  mockClearAgentDown,
  mockReportSendRefused,
  mockRestoreFailedSend,
  mockChatNotice,
} = vi.hoisted(() => ({
  mockSendPromptTo: vi.fn(),
  mockSteer: vi.fn(),
  mockQueue: vi.fn(),
  mockTakeAttachments: vi.fn(() => [] as unknown[]),
  mockHasAttachments: vi.fn(() => false),
  mockAddAttachmentTo: vi.fn(),
  mockAttachmentGeneration: vi.fn(() => 0),
  mockTypedCommand: vi.fn(() => false),
  mockInvokesCatalog: vi.fn(() => false),
  mockClearAgentDown: vi.fn(),
  mockReportSendRefused: vi.fn(),
  mockRestoreFailedSend: vi.fn(),
  mockChatNotice: vi.fn(),
}));

vi.mock("./chat-commands.js", () => ({ sendPromptTo: mockSendPromptTo }));
vi.mock("./notice-subject.js", () => ({ chatNotice: mockChatNotice }));
vi.mock("./actions/chat.js", () => ({
  steerChat: { dispatch: mockSteer },
  queuePrompt: { dispatch: mockQueue },
}));
vi.mock("./typed-commands.js", () => ({ handleTypedCommand: mockTypedCommand }));
vi.mock("./slash-menu.js", () => ({ invokesCatalogCommand: mockInvokesCatalog }));
vi.mock("./attachments.js", () => ({
  takeAttachments: mockTakeAttachments,
  hasAttachments: mockHasAttachments,
  addAttachmentTo: mockAddAttachmentTo,
  attachmentGeneration: mockAttachmentGeneration,
}));
// Both are mocked for the same reason the others are: they own real DOM, and
// send-state's module-level effect paints the send button on import.
vi.mock("./send-state.js", () => ({
  // Undefined: present only so real-ESM linking succeeds.
  setAgentDown: undefined,
  setSSEStatus: undefined,
  clearAgentDown: mockClearAgentDown,
  reportSendRefused: mockReportSendRefused,
}));
vi.mock("./composer-state.js", () => ({
  restoreFailedSend: mockRestoreFailedSend,
  // Present-but-inert so real-ESM linking succeeds: the tab projection reaches
  // this module now (the close rollback), so every export has to resolve.
  retargetComposer: vi.fn(),
  saveComposerState: vi.fn(),
  restoreComposerState: vi.fn(),
  flushComposerDraft: vi.fn(),
  dropComposerState: vi.fn(),
  seedComposerState: vi.fn(),
  adoptRemoteComposerState: vi.fn(),
  noteComposerText: vi.fn(),
  initComposerState: vi.fn(),
  _resetComposerStateForTest: vi.fn(),
}));

import { submitPrompt } from "./submit.js";
import {
  get,
  setSessions,
  setActive,
  setThinking,
  steerCount,
  openTurn,
  setChatInterruptMode,
} from "./store.js";
import type { Session } from "./types.js";
import type { Entry } from "./wire/types.gen.js";

function makeSession(id: string): Session {
  return {
    id,
    name: "test",
    model: "claude",
    acp_session_id: "",
    current_mode_id: "",
    supervised_mode: false,
    usage: {
      context_pct: 0,
      context_size: 0,
      credits: 0,
      last_turn_ms: 0,
      has_real_data: false,
    },
    turns: new Map(),
    turn_order: [],
    turn_count: 0,
    has_more: false,
    thinking: false,
    working_label: "Thinking",
  };
}

function resetStore(id: string): void {
  setSessions([makeSession(id)]);
  setActive(id);
}

/** The echo the server's own append produces: `CmdPrompt` writes the turn's `turn_open`
 *  before the ACP call, and the client-minted message id survives on the log as that
 *  entry's `prompt.id` and nowhere else — which is what `hasMessage` reads. */
function echoTurnOpen(chatID: string, messageID: string, text: string): void {
  const entry: Entry = {
    id: `${messageID}-open`,
    turn: `turn-${messageID}`,
    kind: "turn_open",
    seq: 0,
    ts: 1,
    payload: { source: "prompt", n: 1, prompt: { id: messageID, text } },
  };
  openTurn(chatID, entry);
}

/** The steer text passed to the action on the Nth dispatch. */
function steeredText(call = 0): string {
  return (mockSteer.mock.calls[call]?.[0] as { text: string } | undefined)?.text ?? "";
}

/** A DispatchHandle-shaped return: submit.ts reads ONLY `outcome`, since the never-rejecting
 *  result cannot tell a void success from a failure's null. */
function steerHandle(outcome: unknown): { outcome: Promise<unknown> } {
  return { outcome: Promise.resolve(outcome) };
}

function steerOk(): { outcome: Promise<unknown> } {
  return steerHandle({ status: "success", value: undefined });
}

function steerRefused(message: string, code?: string): { outcome: Promise<unknown> } {
  return steerHandle({
    status: "error",
    error: { message, ...(code === undefined ? {} : { code }) },
  });
}

/** The args the queue action was dispatched with on the Nth call. */
function queuedArgs(call = 0): Record<string, unknown> | undefined {
  return mockQueue.mock.calls[call]?.[0] as Record<string, unknown> | undefined;
}

/** The message id the send primitive was called with on the Nth dispatch. */
function sentMessageID(call = 0): string {
  return (
    (mockSendPromptTo.mock.calls[call]?.[2] as { messageID: string } | undefined)?.messageID ?? ""
  );
}

beforeEach(() => {
  vi.clearAllMocks();
  mockTakeAttachments.mockReturnValue([]);
  mockHasAttachments.mockReturnValue(false);
  mockAttachmentGeneration.mockReturnValue(0);
  mockTypedCommand.mockReturnValue(false);
  mockSteer.mockReturnValue(steerOk());
  mockQueue.mockReturnValue(steerOk());
});

describe("submitPrompt on an idle chat", () => {
  it("sends a prompt and never steers", async () => {
    resetStore("c1");
    mockSendPromptTo.mockResolvedValue("sent");

    expect(await submitPrompt("c1", "hello")).toBe("sent");
    expect(mockSendPromptTo).toHaveBeenCalledTimes(1);
    expect(mockSteer).not.toHaveBeenCalled();
  });

  it("restores the text and the attachments to the input when the send hard-fails", async () => {
    resetStore("c1");
    mockTakeAttachments.mockReturnValue([{ path: "a.ts" }, { path: "b.ts" }]);
    mockAttachmentGeneration.mockReturnValue(7);
    mockSendPromptTo.mockResolvedValue("failed");

    expect(await submitPrompt("c1", "hello")).toBe("failed");
    // Chat-scoped, not "the composer on screen": the send is asynchronous, so by
    // the time a failure lands the reader may be in another conversation.
    expect(mockRestoreFailedSend).toHaveBeenCalledWith("c1", "hello");
    // The generation the send took with it rides the restore, so a chat closed
    // while the request was in flight can refuse it.
    expect(mockAddAttachmentTo).toHaveBeenCalledWith("c1", "a.ts", 7);
    expect(mockAddAttachmentTo).toHaveBeenCalledWith("c1", "b.ts", 7);
  });

  // An ACCEPTED prompt that then lost its turn must not return to the composer: `CmdPrompt` persists
  // the user row before the ACP call and never rolls it back.
  describe("a prompt the server already accepted", () => {
    /** Fail the send, but echo the user row first, the way the server does. */
    function failAfterEcho(): void {
      mockSendPromptTo.mockImplementation(
        async (chatID: string, text: string, opts: { messageID: string }) => {
          echoTurnOpen(chatID, opts.messageID, text);
          return "failed";
        },
      );
    }

    it("is not handed back to the composer", async () => {
      resetStore("c1");
      failAfterEcho();

      expect(await submitPrompt("c1", "hello")).toBe("failed");
      expect(mockRestoreFailedSend).not.toHaveBeenCalled();
    });

    it("does not get its attachments back either", async () => {
      // They rode the accepted prompt, and the server records them on the message.
      resetStore("c1");
      mockTakeAttachments.mockReturnValue([{ path: "a.ts" }]);
      failAfterEcho();

      await submitPrompt("c1", "hello");
      expect(mockAddAttachmentTo).not.toHaveBeenCalled();
    });

    it("still reuses its message id, so a retype lands on the row it persisted", async () => {
      resetStore("c1");
      failAfterEcho();
      await submitPrompt("c1", "hello");
      const first = sentMessageID(0);

      await submitPrompt("c1", "hello");
      expect(sentMessageID(1)).toBe(first);
    });
  });

  it("hands back the generation read BEFORE the send, not the one after it", async () => {
    // A close during the request is exactly what bumps it, so reading the token
    // again on the failure path would compare the new state against itself and
    // restore into a chat that had just forgotten its files.
    resetStore("c1");
    mockTakeAttachments.mockReturnValue([{ path: "a.ts" }]);
    mockAttachmentGeneration.mockReturnValue(3);
    mockSendPromptTo.mockImplementation(async () => {
      mockAttachmentGeneration.mockReturnValue(4); // the chat was closed meanwhile
      return "failed";
    });

    await submitPrompt("c1", "hello");
    expect(mockAddAttachmentTo).toHaveBeenCalledWith("c1", "a.ts", 3);
  });

  it("keeps the input untouched when the send succeeds", async () => {
    resetStore("c1");
    mockSendPromptTo.mockResolvedValue("sent");

    await submitPrompt("c1", "hello");
    expect(mockRestoreFailedSend).not.toHaveBeenCalled();
  });

  it("lets a typed command through without minting a send or a steer", async () => {
    resetStore("c1");
    mockTypedCommand.mockReturnValue(true);

    expect(await submitPrompt("c1", "/compact")).toBe("sent");
    expect(mockSendPromptTo).not.toHaveBeenCalled();
    expect(mockSteer).not.toHaveBeenCalled();
    // A command is not a prompt, so it must not consume the attachment row.
    expect(mockTakeAttachments).not.toHaveBeenCalled();
  });
});

describe("submitPrompt during a turn", () => {
  it("holds a saved-prompt command instead of steering it, and hands the text back", async () => {
    resetStore("c1");
    setThinking("c1", true);
    mockInvokesCatalog.mockReturnValue(true);

    expect(await submitPrompt("c1", "/review a.ts")).toBe("held");
    expect(mockSteer).not.toHaveBeenCalled();
    expect(mockSendPromptTo).not.toHaveBeenCalled();
    expect(mockTakeAttachments).not.toHaveBeenCalled();
    expect(mockRestoreFailedSend).toHaveBeenCalledWith("c1", "/review a.ts");
    expect(mockChatNotice).toHaveBeenLastCalledWith(
      "c1",
      "That command runs as a new turn. Send it when the agent is idle.",
    );
    mockInvokesCatalog.mockReturnValue(false);
  });

  it("holds a message carrying a context reference, which only a new turn resolves", async () => {
    resetStore("c1");
    setThinking("c1", true);

    expect(await submitPrompt("c1", "look at #[[file:a.ts]] too")).toBe("held");
    expect(mockSteer).not.toHaveBeenCalled();
    expect(mockRestoreFailedSend).toHaveBeenCalledWith("c1", "look at #[[file:a.ts]] too");
  });

  // The chat read idle, so the hold above did not apply, and a turn started before
  // the POST landed. The 409 must not carry the reference into a steer or a queued row.
  it.each(["steer", "queue"] as const)(
    "holds a context reference a 409 would turn into a busy verb (%s mode)",
    async (mode) => {
      resetStore("c1");
      setChatInterruptMode("c1", mode);
      mockTakeAttachments.mockReturnValue([{ path: "a.ts" }]);
      mockAttachmentGeneration.mockReturnValue(5);
      mockSendPromptTo.mockResolvedValue("queued");

      expect(await submitPrompt("c1", "see #[[file:b.ts]]")).toBe("held");
      expect(mockSteer).not.toHaveBeenCalled();
      expect(mockQueue).not.toHaveBeenCalled();
      expect(mockRestoreFailedSend).toHaveBeenCalledWith("c1", "see #[[file:b.ts]]");
      expect(mockAddAttachmentTo).toHaveBeenCalledWith("c1", "a.ts", 5);
      expect(mockChatNotice).toHaveBeenLastCalledWith("c1", expect.stringContaining("new turn"));
    },
  );

  // The whole reason this module changed: the message has to reach the turn
  // that is running, not the one after it.
  it("steers instead of sending a prompt", async () => {
    resetStore("c1");
    setThinking("c1", true);

    expect(await submitPrompt("c1", "actually use tabs")).toBe("steered");
    expect(mockSendPromptTo).not.toHaveBeenCalled();
    expect(mockSteer).toHaveBeenCalledTimes(1);
    expect(steeredText()).toBe("actually use tabs");
  });

  // A turn can start between reading `thinking` and the POST landing; the server
  // answers that with 409, which is precisely the case a steer exists for.
  it("steers when the server reports 409 busy", async () => {
    resetStore("c1");
    mockSendPromptTo.mockResolvedValue("queued");

    expect(await submitPrompt("c1", "wait, stop")).toBe("steered");
    expect(mockSendPromptTo).toHaveBeenCalledTimes(1);
    expect(steeredText()).toBe("wait, stop");
  });

  // Submit writes no store rows: the optimistic chip is the action's, mocked here.
  it("records nothing locally", async () => {
    resetStore("c1");
    setThinking("c1", true);

    await submitPrompt("c1", "hello");
    expect(steerCount("c1")).toBe(0);
  });

  it("reports failure and restores the text and attachments when the steer is refused", async () => {
    resetStore("c1");
    setThinking("c1", true);
    mockTakeAttachments.mockReturnValue([{ path: "a.ts" }]);
    mockSteer.mockReturnValue(
      steerRefused("the session is still loading its history — send this again in a moment"),
    );

    expect(await submitPrompt("c1", "hello")).toBe("failed");
    expect(mockRestoreFailedSend).toHaveBeenCalledWith("c1", "hello");
    expect(mockAddAttachmentTo).toHaveBeenCalledWith("c1", "a.ts", 0);
    // The action carries error:false, so the send-error face is the ONE
    // failure surface, and it speaks the server's own words.
    expect(mockReportSendRefused).toHaveBeenCalledWith(
      "the session is still loading its history — send this again in a moment",
    );
  });
});

// `no_turn`: the chat was idle when the steer landed, so submit converts it into a prompt.
describe("a steer refused with no_turn", () => {
  const noTurn = (): { outcome: Promise<unknown> } =>
    steerRefused("nothing is running to steer, so send this as a prompt instead", "no_turn");

  it("is retried as the prompt it should have been", async () => {
    resetStore("c1");
    setThinking("c1", true); // stale: the server knows better
    mockSteer.mockReturnValue(noTurn());
    mockSendPromptTo.mockResolvedValue("sent");

    expect(await submitPrompt("c1", "hello")).toBe("sent");
    expect(mockSteer).toHaveBeenCalledTimes(1);
    expect(mockSendPromptTo).toHaveBeenCalledTimes(1);
    // The conversion is silent: nothing failed from the user's seat.
    expect(mockRestoreFailedSend).not.toHaveBeenCalled();
    expect(mockReportSendRefused).not.toHaveBeenCalled();
  });

  it("rides the double race: the retry prompt meets a NEW turn and steers into it", async () => {
    resetStore("c1");
    setThinking("c1", true);
    mockSteer.mockReturnValueOnce(noTurn()).mockReturnValueOnce(steerOk());
    mockSendPromptTo.mockResolvedValue("queued");

    expect(await submitPrompt("c1", "hello")).toBe("steered");
    expect(mockSteer).toHaveBeenCalledTimes(2);
    expect(mockSendPromptTo).toHaveBeenCalledTimes(1);
  });

  it("stops converting once the budget is spent, at the error face", async () => {
    // Busy/idle flapping must terminate: one budget's worth of hops, then a failure the next Send retries.
    resetStore("c1");
    setThinking("c1", true);
    mockSteer.mockReturnValue(noTurn());
    mockSendPromptTo.mockResolvedValue("queued");

    expect(await submitPrompt("c1", "hello")).toBe("failed");
    expect(mockSteer).toHaveBeenCalledTimes(2);
    expect(mockSendPromptTo).toHaveBeenCalledTimes(1);
    expect(mockReportSendRefused).toHaveBeenCalled();
  });

  it("converts on the idle path too: prompt → 409 → steer → no turn → prompt", async () => {
    resetStore("c1");
    mockSendPromptTo.mockResolvedValueOnce("queued").mockResolvedValueOnce("sent");
    mockSteer.mockReturnValue(noTurn());

    expect(await submitPrompt("c1", "hello")).toBe("sent");
    expect(mockSendPromptTo).toHaveBeenCalledTimes(2);
    expect(mockSteer).toHaveBeenCalledTimes(1);
  });
});

// 409 "starting": the holder cannot take a steer. The turn opens AFTER admission, so the refusal
// appended nothing and the text is handed back like any pre-persist failure.
describe("submitPrompt on a 409-starting refusal", () => {
  /** Refuse with "starting" and append NOTHING, which is what the server does now: no
   *  `turn_open` carries this id, so `hasMessage` answers false and the text comes back. */
  function starting(): void {
    mockSendPromptTo.mockResolvedValue("starting");
  }

  it("reports failure and never attempts a steer", async () => {
    resetStore("c1");
    starting();

    expect(await submitPrompt("c1", "hello")).toBe("failed");
    expect(mockSteer).not.toHaveBeenCalled();
  });

  it("renders the holder-neutral busy face through send-state's error surface", async () => {
    resetStore("c1");
    starting();

    await submitPrompt("c1", "hello");
    expect(mockReportSendRefused).toHaveBeenCalledWith(
      "The chat is busy right now. Send again to retry",
    );
  });

  it("hands the text and the attachments back, because nothing was persisted", async () => {
    // A turn opens after the admission slot, so this is a pre-persist failure: keeping the text would lose it.
    resetStore("c1");
    mockTakeAttachments.mockReturnValue([{ path: "a.ts" }]);
    starting();

    await submitPrompt("c1", "hello");
    expect(mockRestoreFailedSend).toHaveBeenCalledWith("c1", "hello");
    expect(mockAddAttachmentTo).toHaveBeenCalledWith("c1", "a.ts", 0);
  });

  it("re-sends the same text under the same id, so the server dedupes the append", async () => {
    // The id is recorded whatever the restore gate decides: a retry has to land on the row
    // the attempt persisted IF it persisted one, and this refusal is the case where it did
    // not — the same slot serves both, so a later attempt can never mint a second row.
    resetStore("c1");
    starting();
    await submitPrompt("c1", "hello");
    const first = sentMessageID(0);
    expect(first).not.toBe("");

    // The retry is an ordinary send (the face said "send again to retry").
    mockSendPromptTo.mockResolvedValue("sent");
    await submitPrompt("c1", "hello");
    expect(sentMessageID(1)).toBe(first);
  });

  it("retries as a PROMPT, not a steer", async () => {
    // The action retracts the optimistic thinking (actions/chat-prompt.test.ts), so the retry goes down
    // the send path.
    resetStore("c1");
    starting();
    await submitPrompt("c1", "hello");

    mockSendPromptTo.mockResolvedValue("sent");
    expect(await submitPrompt("c1", "hello")).toBe("sent");
    expect(mockSendPromptTo).toHaveBeenCalledTimes(2);
    expect(mockSteer).not.toHaveBeenCalled();
  });
});

// A failure sets a signal nothing on the failure path ever clears (no
// turn_closed is emitted for a prompt that died at session/prompt), so without
// this the stale reason decorates every later send in the thread.
describe("retrying in the same thread", () => {
  it("clears the previous failure before each new attempt", async () => {
    resetStore("c1");
    mockSendPromptTo.mockResolvedValue("failed");
    await submitPrompt("c1", "first try");
    expect(mockClearAgentDown).toHaveBeenCalledTimes(1);

    mockSendPromptTo.mockResolvedValue("sent");
    expect(await submitPrompt("c1", "second try")).toBe("sent");
    expect(mockClearAgentDown).toHaveBeenCalledTimes(2);
    // The retry is an ordinary send: no special path, no cancel, no new chat.
    expect(mockSendPromptTo).toHaveBeenCalledTimes(2);
  });

  it("clears it for a typed command too, which is also an attempt", async () => {
    resetStore("c1");
    mockTypedCommand.mockReturnValue(true);

    await submitPrompt("c1", "/compact");
    expect(mockClearAgentDown).toHaveBeenCalledTimes(1);
  });

  it("does not clear it when the guards refuse the submit outright", async () => {
    resetStore("c1");
    await submitPrompt("", "hello");
    await submitPrompt("c1", "");
    expect(mockClearAgentDown).not.toHaveBeenCalled();
  });

  // The server persists the user row BEFORE the ACP call and never rolls it
  // back, so a retry under a fresh id appends a second identical row and hands
  // KAS a second messageId. This is what keeps one failed prompt to one row.
  it("retries a failed send under the id that attempt already used", async () => {
    resetStore("c1");
    mockSendPromptTo.mockResolvedValue("failed");
    await submitPrompt("c1", "same text");
    const first = sentMessageID(0);
    expect(first).not.toBe("");

    mockSendPromptTo.mockResolvedValue("sent");
    await submitPrompt("c1", "same text");
    expect(sentMessageID(1)).toBe(first);
  });

  it("keeps the id stable across repeated retries of the same text", async () => {
    resetStore("c1");
    mockSendPromptTo.mockResolvedValue("failed");
    await submitPrompt("c1", "same text");
    await submitPrompt("c1", "same text");
    await submitPrompt("c1", "same text");
    expect(sentMessageID(1)).toBe(sentMessageID(0));
    expect(sentMessageID(2)).toBe(sentMessageID(0));
  });

  it("mints a fresh id once the text is edited — a different message earns a different row", async () => {
    resetStore("c1");
    mockSendPromptTo.mockResolvedValue("failed");
    await submitPrompt("c1", "first wording");
    await submitPrompt("c1", "second wording");
    expect(sentMessageID(1)).not.toBe(sentMessageID(0));
  });

  it("mints a fresh id for the same text on a different chat", async () => {
    resetStore("c1");
    mockSendPromptTo.mockResolvedValue("failed");
    await submitPrompt("c1", "same text");
    await submitPrompt("c2", "same text");
    expect(sentMessageID(1)).not.toBe(sentMessageID(0));
  });

  it("forgets the failed id after a send succeeds", async () => {
    resetStore("c1");
    mockSendPromptTo.mockResolvedValue("failed");
    await submitPrompt("c1", "same text");
    mockSendPromptTo.mockResolvedValue("sent");
    await submitPrompt("c1", "same text");
    // A third send of identical text is a NEW message, not a retry of the one
    // that already landed.
    await submitPrompt("c1", "same text");
    expect(sentMessageID(2)).not.toBe(sentMessageID(1));
  });
});

describe("steer attachments", () => {
  // `_session/steer` takes a plain string, so a file cannot ride along as a
  // content block. Degrading to the path reference the server already uses for
  // an unsupported document keeps ONE convention for "here is a file".
  it("folds attachment paths into the text using the server's own wording", async () => {
    resetStore("c1");
    setThinking("c1", true);
    mockTakeAttachments.mockReturnValue([{ path: "src/a.ts" }, { path: "src/b.ts" }]);

    await submitPrompt("c1", "look at these");
    expect(steeredText()).toBe("look at these\n\nAttached file: src/a.ts\nAttached file: src/b.ts");
  });

  it("leaves the text alone when there are no attachments", async () => {
    resetStore("c1");
    setThinking("c1", true);
    mockTakeAttachments.mockReturnValue([]);

    await submitPrompt("c1", "plain");
    expect(steeredText()).toBe("plain");
  });

  it("sends an attachment-only steer as the path lines alone", async () => {
    resetStore("c1");
    setThinking("c1", true);
    mockHasAttachments.mockReturnValue(true);
    mockTakeAttachments.mockReturnValue([{ path: "shot.png", name: "shot.png" }]);

    await submitPrompt("c1", "");
    expect(steeredText()).toBe("Attached file: shot.png");
  });

  it("skips an attachment with no usable path rather than emitting a blank line", async () => {
    resetStore("c1");
    setThinking("c1", true);
    mockTakeAttachments.mockReturnValue([
      { path: "", name: "" },
      { path: "ok.ts", name: "ok.ts" },
    ]);

    await submitPrompt("c1", "one good file");
    expect(steeredText()).toBe("one good file\n\nAttached file: ok.ts");
  });
});

// QUEUE MODE: a busy chat holds the message for the turn's end instead of steering it.
describe("submitPrompt in Queue mode", () => {
  function busyQueueChat(): void {
    resetStore("c1");
    setChatInterruptMode("c1", "queue");
    setThinking("c1", true);
  }

  it("queues a follow-up mid-turn and never steers", async () => {
    busyQueueChat();

    expect(await submitPrompt("c1", "then add tests")).toBe("queued");
    expect(mockSteer).not.toHaveBeenCalled();
    expect(mockSendPromptTo).not.toHaveBeenCalled();
    expect(queuedArgs()).toMatchObject({ chatID: "c1", text: "then add tests" });
  });

  it("keeps attachments as attachments rather than folding them into the text", async () => {
    busyQueueChat();
    mockTakeAttachments.mockReturnValue([{ path: "src/a.ts", name: "a.ts" }]);

    await submitPrompt("c1", "look at this");
    expect(queuedArgs()).toMatchObject({
      text: "look at this",
      attachments: [{ path: "src/a.ts", name: "a.ts" }],
    });
  });

  it("queues when a prompt meets a plain 409, the turn having started underneath it", async () => {
    resetStore("c1");
    setChatInterruptMode("c1", "queue");
    mockSendPromptTo.mockResolvedValue("queued");

    expect(await submitPrompt("c1", "hello")).toBe("queued");
    expect(mockQueue).toHaveBeenCalledTimes(1);
    expect(mockSteer).not.toHaveBeenCalled();
  });

  it("sends the prompt it should have been when the turn ended first", async () => {
    busyQueueChat();
    mockQueue.mockReturnValue(steerRefused("nothing is running", "no_turn"));
    mockSendPromptTo.mockResolvedValue("sent");

    expect(await submitPrompt("c1", "hello")).toBe("sent");
    expect(mockSendPromptTo).toHaveBeenCalledTimes(1);
  });

  it("hands the text back and shows the server's words when the queue is full", async () => {
    busyQueueChat();
    mockQueue.mockReturnValue(steerRefused("this chat already holds the most follow-ups", "full"));

    expect(await submitPrompt("c1", "hello")).toBe("failed");
    expect(mockRestoreFailedSend).toHaveBeenCalledWith("c1", "hello");
    expect(mockReportSendRefused).toHaveBeenCalledWith(
      "this chat already holds the most follow-ups",
    );
    expect(mockSendPromptTo).not.toHaveBeenCalled();
  });

  // A lost response is not a refusal when the header already lists the row: the
  // server holds the text and the attachments, so handing them back duplicates them.
  it("hands nothing back when the row the server already queued lost its response", async () => {
    busyQueueChat();
    mockTakeAttachments.mockReturnValue([{ path: "a.ts", name: "a.ts" }]);
    mockQueue.mockImplementation((args: { messageID: string; text: string }) => {
      const session = get("c1");
      if (session !== undefined) {
        session.queued = [{ id: args.messageID, text: args.text }];
      }
      return steerRefused("connection lost", "network");
    });

    expect(await submitPrompt("c1", "hello")).toBe("failed");
    expect(mockRestoreFailedSend).not.toHaveBeenCalled();
    expect(mockAddAttachmentTo).not.toHaveBeenCalled();
  });
});

describe("submitPrompt guards", () => {
  it("refuses an empty chat id or empty text without touching the wire", async () => {
    resetStore("c1");
    expect(await submitPrompt("", "hello")).toBe("failed");
    expect(await submitPrompt("c1", "")).toBe("failed");
    expect(mockSendPromptTo).not.toHaveBeenCalled();
    expect(mockSteer).not.toHaveBeenCalled();
  });

  it("sends an attachment-only prompt when the box is empty", async () => {
    resetStore("c1");
    mockHasAttachments.mockReturnValue(true);
    mockTakeAttachments.mockReturnValue([{ path: "shot.png", name: "shot.png" }]);
    mockSendPromptTo.mockResolvedValue("sent");

    expect(await submitPrompt("c1", "")).toBe("sent");
    expect(mockSendPromptTo).toHaveBeenCalledWith(
      "c1",
      "",
      expect.objectContaining({
        attachments: [{ path: "shot.png", name: "shot.png" }],
      }),
    );
  });
});
