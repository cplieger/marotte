// What Send MEANS, over the REAL store with the send primitive and attachment row mocked: one prompt
// POST per press, which the server places; a refusal is recoverable in place.

import { describe, it, expect, vi, beforeEach } from "vitest";

// Declared via vi.hoisted so they exist when the hoisted vi.mock factories run.
const {
  mockSendPromptTo,
  mockTakeAttachments,
  mockHasAttachments,
  mockAddAttachmentTo,
  mockAttachmentGeneration,
  mockTypedCommand,
  mockTerminalRefusal,
  mockInvokesCatalog,
  mockClearAgentDown,
  mockReportSendRefused,
  mockRestoreFailedSend,
  mockChatNotice,
} = vi.hoisted(() => ({
  mockSendPromptTo: vi.fn(),
  mockTakeAttachments: vi.fn(() => [] as unknown[]),
  mockHasAttachments: vi.fn(() => false),
  mockAddAttachmentTo: vi.fn(),
  mockAttachmentGeneration: vi.fn(() => 0),
  mockTypedCommand: vi.fn(() => false),
  mockTerminalRefusal: vi.fn((): { notice: string; keepText: boolean } | null => null),
  mockInvokesCatalog: vi.fn(() => false),
  mockClearAgentDown: vi.fn(),
  mockReportSendRefused: vi.fn(),
  mockRestoreFailedSend: vi.fn(),
  mockChatNotice: vi.fn(),
}));

vi.mock("./chat-commands.js", () => ({ sendPromptTo: mockSendPromptTo }));
vi.mock("./notice-subject.js", () => ({ chatNotice: mockChatNotice }));
vi.mock("./typed-commands.js", () => ({
  handleTypedCommand: mockTypedCommand,
  terminalCommandRefusal: mockTerminalRefusal,
}));
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
  adoptUnownedComposer: vi.fn(),
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

import { submitPrompt, submitLabelled, forgetDeletedChat } from "./submit.js";
import { applyRun, setSessions, setActive, openTurn, appendEntry } from "./store.js";
import { runAt, turnEntry } from "./__test-helpers__/run-order.js";
import type { Session } from "./types.js";
import type { Entry } from "./wire/types.gen.js";

function makeSession(id: string): Session {
  return {
    id,
    name: "test",
    model: "model-x",
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
  mockTerminalRefusal.mockReturnValue(null);
});

describe("submitPrompt on an idle chat", () => {
  it("sends a prompt and never steers", async () => {
    resetStore("c1");
    mockSendPromptTo.mockResolvedValue("sent");

    expect(await submitPrompt("c1", "hello")).toBe("sent");
    expect(mockSendPromptTo).toHaveBeenCalledTimes(1);
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

  // A send for a chat that is not on screen names its files: the live row is another chat's.
  it("sends a named attachment list instead of the live row's, and restores it to the named chat", async () => {
    resetStore("c1");
    mockHasAttachments.mockReturnValue(true);
    mockTakeAttachments.mockReturnValue([{ path: "other.ts" }]);
    mockAttachmentGeneration.mockReturnValue(4);
    mockSendPromptTo.mockResolvedValue("failed");
    const file = { path: "shot.png", name: "shot.png" };

    expect(await submitPrompt("c1", "", { attachments: [file] })).toBe("failed");
    expect(mockSendPromptTo.mock.calls[0]?.[2]).toMatchObject({ attachments: [file] });
    expect(mockTakeAttachments).not.toHaveBeenCalled();
    expect(mockAddAttachmentTo).toHaveBeenCalledWith("c1", "shot.png", 4);
  });

  it("refuses an empty send whose named attachment list is empty, whatever the live row holds", async () => {
    resetStore("c1");
    mockHasAttachments.mockReturnValue(true);

    expect(await submitPrompt("c1", "", { attachments: [] })).toBe("failed");
    expect(mockSendPromptTo).not.toHaveBeenCalled();
  });

  // An ACCEPTED prompt that then lost its turn must not return to the composer: `CmdPrompt` persists
  // the user row before the ACP call and never rolls it back.
  describe("a prompt the server already accepted", () => {
    /** Fail the send, but echo the user row first, the way the server does, and the close
     *  its failure writes. */
    function failAfterEcho(): void {
      mockSendPromptTo.mockImplementation(
        async (chatID: string, text: string, opts: { messageID: string }) => {
          echoTurnOpen(chatID, opts.messageID, text);
          appendEntry(chatID, {
            id: `${opts.messageID}-close`,
            turn: `turn-${opts.messageID}`,
            kind: "turn_close",
            seq: 1,
            ts: 2,
            payload: { outcome: "failed" },
          });
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
    // A command is not a prompt, so it must not consume the attachment row.
    expect(mockTakeAttachments).not.toHaveBeenCalled();
  });

  it("answers a kiro-cli terminal command with its notice and sends nothing", async () => {
    resetStore("c1");
    mockInvokesCatalog.mockReturnValue(false);
    mockTerminalRefusal.mockReturnValue({ notice: "marotte has no /theme.", keepText: false });

    expect(await submitPrompt("c1", "/theme")).toBe("sent");
    expect(mockTerminalRefusal).toHaveBeenCalledWith("/theme");
    expect(mockChatNotice).toHaveBeenCalledWith("c1", "marotte has no /theme.");
    expect(mockSendPromptTo).not.toHaveBeenCalled();
    expect(mockTakeAttachments).not.toHaveBeenCalled();
  });

  it("hands a terminal command with arguments back to the composer", async () => {
    resetStore("c1");
    mockInvokesCatalog.mockReturnValue(false);
    mockTerminalRefusal.mockReturnValue({ notice: "marotte has no /theme.", keepText: true });

    expect(await submitPrompt("c1", "/theme dark")).toBe("held");
    expect(mockChatNotice).toHaveBeenCalledWith("c1", "marotte has no /theme.");
    expect(mockRestoreFailedSend).toHaveBeenCalledWith("c1", "/theme dark");
    expect(mockSendPromptTo).not.toHaveBeenCalled();
    expect(mockTakeAttachments).not.toHaveBeenCalled();
  });

  it("clears a bare terminal command from the composer", async () => {
    resetStore("c1");
    mockInvokesCatalog.mockReturnValue(false);
    mockTerminalRefusal.mockReturnValue({ notice: "marotte has no /theme.", keepText: false });

    await submitPrompt("c1", "/theme");
    expect(mockRestoreFailedSend).not.toHaveBeenCalled();
  });

  it("sends a catalog entry that shares a terminal command's name", async () => {
    resetStore("c1");
    mockSendPromptTo.mockResolvedValue("sent");
    mockInvokesCatalog.mockReturnValue(true);
    mockTerminalRefusal.mockReturnValue({ notice: "marotte has no /feedback.", keepText: false });

    expect(await submitPrompt("c1", "/feedback")).toBe("sent");
    expect(mockSendPromptTo).toHaveBeenCalledTimes(1);
    expect(mockChatNotice).not.toHaveBeenCalled();
  });
});

// The server's run order places every send (started, steered or queued): the composer posts once
// and never steers or queues on its own.
describe("one POST per press, whatever runs on the chat", () => {
  it("sends one prompt and no steer or queue command while a turn runs", async () => {
    resetStore("c1");
    applyRun("c1", runAt(1, [turnEntry("t1")], { send: "steer", cancel: "t1" }));
    mockSendPromptTo.mockResolvedValue("steered");

    expect(await submitPrompt("c1", "use tabs")).toBe("steered");
    expect(mockSendPromptTo).toHaveBeenCalledTimes(1);
  });

  it("counts an answer that queued it as placed and hands nothing back", async () => {
    resetStore("c1");
    mockSendPromptTo.mockResolvedValue("queued");

    expect(await submitPrompt("c1", "then add tests")).toBe("queued");
    expect(mockRestoreFailedSend).not.toHaveBeenCalled();
    expect(mockReportSendRefused).not.toHaveBeenCalled();
  });

  it("keeps attachments as attachments, never folded into the text", async () => {
    resetStore("c1");
    mockTakeAttachments.mockReturnValue([{ path: "a.ts", name: "a.ts" }]);
    mockSendPromptTo.mockResolvedValue("queued");

    await submitPrompt("c1", "look at this");
    expect(mockSendPromptTo.mock.calls[0]?.[1]).toBe("look at this");
    expect(mockSendPromptTo.mock.calls[0]?.[2]).toMatchObject({
      attachments: [{ path: "a.ts", name: "a.ts" }],
    });
  });
});

describe("prompt-only text", () => {
  it("marks a saved-prompt command prompt_only", async () => {
    resetStore("c1");
    mockInvokesCatalog.mockReturnValue(true);
    mockSendPromptTo.mockResolvedValue("sent");

    await submitPrompt("c1", "@review");
    expect(mockSendPromptTo.mock.calls[0]?.[2]).toMatchObject({ promptOnly: true });
  });

  it("never marks plain text prompt_only", async () => {
    resetStore("c1");
    mockSendPromptTo.mockResolvedValue("sent");

    await submitPrompt("c1", "hello");
    expect(mockSendPromptTo.mock.calls[0]?.[2]).not.toHaveProperty("promptOnly");
  });

  it("keeps the text and says why when the server refuses it 409 prompt_only", async () => {
    resetStore("c1");
    mockInvokesCatalog.mockReturnValue(true);
    mockSendPromptTo.mockResolvedValue("prompt_only");

    expect(await submitPrompt("c1", "@review")).toBe("held");
    expect(mockChatNotice).toHaveBeenCalledWith(
      "c1",
      "A steer cannot run that command. Send it when the turn ends, or turn on Queue messages in Chat options.",
    );
    expect(mockRestoreFailedSend).toHaveBeenCalledWith("c1", "@review");
  });

  it("says a steer cannot carry a context reference when the server refuses it 409 prompt_only", async () => {
    resetStore("c1");
    mockSendPromptTo.mockResolvedValue("prompt_only");

    expect(await submitPrompt("c1", "see #[[file:a.ts]]")).toBe("held");
    expect(mockSendPromptTo.mock.calls[0]?.[2]).toMatchObject({ promptOnly: true });
    expect(mockChatNotice).toHaveBeenCalledWith(
      "c1",
      "A steer cannot carry context references. Send it when the turn ends, or turn on Queue messages in Chat options.",
    );
  });
});

describe("a full queue", () => {
  it("hands the text back and says the queue is full", async () => {
    resetStore("c1");
    mockSendPromptTo.mockResolvedValue("full");

    expect(await submitPrompt("c1", "one more")).toBe("failed");
    expect(mockReportSendRefused).toHaveBeenCalledWith(
      "The queue is full. Send again once a queued message has gone",
    );
    expect(mockRestoreFailedSend).toHaveBeenCalledWith("c1", "one more");
  });
});

describe("submitPrompt on a 409-starting refusal", () => {
  /** Refuse with "starting" and append NOTHING, which is what the server does now: no
   *  `turn_open` carries this id, so `hasMessage` answers false and the text comes back. */
  function starting(): void {
    mockSendPromptTo.mockResolvedValue("starting");
  }

  it("reports failure and sends nothing else", async () => {
    resetStore("c1");
    starting();

    expect(await submitPrompt("c1", "hello")).toBe("failed");
    expect(mockSendPromptTo).toHaveBeenCalledTimes(1);
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
    // not — the same record serves both, so a later attempt can never mint a second row.
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

  it("retries as one more prompt POST", async () => {
    resetStore("c1");
    starting();
    await submitPrompt("c1", "hello");

    mockSendPromptTo.mockResolvedValue("sent");
    expect(await submitPrompt("c1", "hello")).toBe("sent");
    expect(mockSendPromptTo).toHaveBeenCalledTimes(2);
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

  it("keeps a chat's failed id when another chat's typed send succeeds", async () => {
    resetStore("c1");
    mockSendPromptTo.mockResolvedValue("failed");
    await submitPrompt("c1", "same text");
    mockSendPromptTo.mockResolvedValue("sent");
    await submitPrompt("c2", "other text");

    await submitPrompt("c1", "same text");
    expect(sentMessageID(2)).toBe(sentMessageID(0));
  });

  it("keeps the failed id when a labelled send on the same chat succeeds in between", async () => {
    resetStore("c1");
    mockSendPromptTo.mockResolvedValue("failed");
    await submitPrompt("c1", "same text");
    mockSendPromptTo.mockResolvedValue("sent");
    await submitLabelled("c1", "the whole instruction", "Run task 2");

    await submitPrompt("c1", "same text");
    expect(sentMessageID(2)).toBe(sentMessageID(0));
  });

  it("mints a fresh id once the chat was deleted", async () => {
    resetStore("c-deleted-a");
    mockSendPromptTo.mockResolvedValue("failed");
    await submitPrompt("c-deleted-a", "same text");
    forgetDeletedChat("c-deleted-a");

    await submitPrompt("c-deleted-a", "same text");
    expect(sentMessageID(1)).not.toBe(sentMessageID(0));
  });

  it("keeps a chat's failed id when another chat is deleted", async () => {
    resetStore("c1");
    mockSendPromptTo.mockResolvedValue("failed");
    await submitPrompt("c1", "same text");
    forgetDeletedChat("c-deleted-b");

    await submitPrompt("c1", "same text");
    expect(sentMessageID(1)).toBe(sentMessageID(0));
  });

  it("keeps nothing from a send that fails after its chat was deleted", async () => {
    resetStore("c-deleted-c");
    let settle: (result: string) => void = () => undefined;
    mockSendPromptTo.mockImplementationOnce(
      () =>
        new Promise<string>((resolve) => {
          settle = resolve;
        }),
    );
    const inFlight = submitPrompt("c-deleted-c", "same text");
    forgetDeletedChat("c-deleted-c");
    settle("failed");
    await inFlight;

    expect(mockRestoreFailedSend).not.toHaveBeenCalled();
    mockSendPromptTo.mockResolvedValue("failed");
    await submitPrompt("c-deleted-c", "same text");
    expect(sentMessageID(1)).not.toBe(sentMessageID(0));
  });

  it("leaves nothing of a deletion once the sends it crossed have settled", async () => {
    resetStore("c-deleted-d");
    forgetDeletedChat("c-deleted-d");
    mockSendPromptTo.mockResolvedValue("failed");

    await submitPrompt("c-deleted-d", "same text");
    await submitPrompt("c-deleted-d", "same text");

    expect(mockRestoreFailedSend).toHaveBeenCalledWith("c-deleted-d", "same text");
    expect(sentMessageID(1)).toBe(sentMessageID(0));
  });

  it("keeps the failed id of a send that fails after its chat's tab closed", async () => {
    resetStore("c-closed");
    let settle: (result: string) => void = () => undefined;
    mockSendPromptTo.mockImplementationOnce(
      () =>
        new Promise<string>((resolve) => {
          settle = resolve;
        }),
    );
    const inFlight = submitPrompt("c-closed", "same text");
    setSessions([]);
    settle("failed");
    await inFlight;

    resetStore("c-closed");
    mockSendPromptTo.mockResolvedValue("failed");
    await submitPrompt("c-closed", "same text");
    expect(sentMessageID(1)).toBe(sentMessageID(0));
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

describe("submitPrompt guards", () => {
  it("refuses an empty chat id or empty text without touching the wire", async () => {
    resetStore("c1");
    expect(await submitPrompt("", "hello")).toBe("failed");
    expect(await submitPrompt("c1", "")).toBe("failed");
    expect(mockSendPromptTo).not.toHaveBeenCalled();
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

describe("submitLabelled", () => {
  it("sends the label beside the text and takes nothing from the composer", async () => {
    resetStore("c1");
    mockSendPromptTo.mockResolvedValue("sent");

    expect(await submitLabelled("c1", "the whole instruction", "Run task 2")).toBe("sent");
    const opts = mockSendPromptTo.mock.calls[0]?.[2] as { displayText?: string } | undefined;
    expect(mockSendPromptTo.mock.calls[0]?.[1]).toBe("the whole instruction");
    expect(opts?.displayText).toBe("Run task 2");
    expect(mockTakeAttachments).not.toHaveBeenCalled();
    expect(mockTypedCommand).not.toHaveBeenCalled();
  });

  it("posts once with the label on a busy chat, the server queueing it", async () => {
    resetStore("c1");
    applyRun("c1", runAt(1, [turnEntry("t1")], { send: "steer", cancel: "t1" }));
    mockSendPromptTo.mockResolvedValue("queued");

    expect(await submitLabelled("c1", "the findings", "Merging findings")).toBe("queued");
    expect(mockSendPromptTo).toHaveBeenCalledTimes(1);
    const opts = mockSendPromptTo.mock.calls[0]?.[2] as { displayText?: string } | undefined;
    expect(opts?.displayText).toBe("Merging findings");
  });

  it("never hands its words to the composer when the send fails", async () => {
    resetStore("c1");
    mockSendPromptTo.mockResolvedValue("failed");

    expect(await submitLabelled("c1", "the whole instruction", "Run task 2")).toBe("failed");
    expect(mockRestoreFailedSend).not.toHaveBeenCalled();
  });
});
