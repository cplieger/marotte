// ---------------------------------------------------------------------------
// Tests for steer-resend.ts: the one armed slot that carries an unread steer
// into the next turn.
//
// The subject is the MECHANISM, not either producer: which text ends up armed
// when the boundary and the dock's arrow both reach for the slot, what the send
// looks like when it fires, and what happens to the reader's message when the
// chat refuses it. The two producers are tested where they live —
// handlers/turn.test.ts and handlers/steer.test.ts drive the boundary doors,
// pending-steers.test.ts drives the arrow.
//
// PRECEDENCE IS THE CASE THAT MATTERS MOST. The arrow arms and then cancels, and
// its own cancel is what produces the boundary — so a generic capture that
// overwrote an armed slot would silently re-sort the reader's messages and
// un-fix the arrow, with every other assertion in this file still green.
//
// Everything the module reaches out to is mocked: each is a command at its
// boundary (a POST, a composer write, the send button's face), and the real
// modules would pull the transport and the actions framework in behind them.
// vi.hoisted because steer-resend.js is a STATIC import below, so the factories
// run during that import's resolution — before a plain top-level const exists.
// ---------------------------------------------------------------------------

import { describe, it, expect, beforeEach, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  sendPromptTo: vi.fn(
    (_chatID: string, _text: string, _opts?: { messageID?: string; resends?: readonly string[] }) =>
      Promise.resolve<"sent" | "queued" | "starting" | "gone" | "failed">("sent"),
  ),
  clearDispatch: vi.fn(() => Promise.resolve(true)),
  restoreFailedSend: vi.fn(),
  reportSendRefused: vi.fn(),
  newMessageID: vi.fn(() => "m-resend"),
}));

vi.mock("./chat-commands.js", () => ({
  sendPromptTo: mocks.sendPromptTo,
  switchModel: vi.fn(),
}));
vi.mock("./actions/chat.js", () => ({ clearSteers: { dispatch: mocks.clearDispatch } }));
vi.mock("./composer-state.js", () => ({ restoreFailedSend: mocks.restoreFailedSend }));
vi.mock("./send-state.js", () => ({ reportSendRefused: mocks.reportSendRefused }));
vi.mock("./transport.js", () => ({ newMessageID: mocks.newMessageID }));

const { sendPromptTo, clearDispatch, restoreFailedSend, reportSendRefused } = mocks;

import {
  forgetSteerPreference,
  forgetSteerResend,
  noteBoundaryDrop,
  preferSteerFirst,
  runArmedResend,
} from "./steer-resend.js";

/** The boundary hands over entries, so the cases read as the dock does: an id per row
 *  and the text beside it. Ids are positional so a case can name its lead row. */
function rows(...texts: readonly string[]): readonly { id: string; text: string }[] {
  return texts.map((text, i) => ({ id: `steer-${String(i + 1)}`, text }));
}

const CHAT = "c1";

/** The text of the one prompt that was sent. Fails loudly on a second one, which is
 *  the shape every case here cares about — one boundary means one new turn. */
function sentText(): string {
  expect(sendPromptTo).toHaveBeenCalledTimes(1);
  return String(sendPromptTo.mock.calls[0]?.[1]);
}

/** Fire and let the send's own await settle. */
async function fire(chatID = CHAT): Promise<void> {
  runArmedResend(chatID);
  await vi.waitFor(() => {
    expect(clearDispatch.mock.calls.length + sendPromptTo.mock.calls.length).toBeGreaterThan(0);
  });
}

describe("the armed slot", () => {
  beforeEach(() => {
    // The module holds its state per chat and Browser Mode caches the module, so the
    // reset is the module's own forget rather than a re-import.
    forgetSteerResend(CHAT);
    vi.clearAllMocks();
    sendPromptTo.mockResolvedValue("sent");
  });

  // The join convention: kiro-cli's own TUI concatenates queued steer messages with
  // a blank line, and submit.ts already joins a message and its attachment lines the
  // same way. Order is the order they were typed.
  it("joins several waiting messages with one blank line, in queue order", async () => {
    noteBoundaryDrop(CHAT, rows("first", "second", "third"));
    await fire();
    expect(sentText()).toBe("first\n\nsecond\n\nthird");
  });

  it("sends one message unchanged, with no prefix or marker", async () => {
    noteBoundaryDrop(CHAT, rows("actually target main"));
    await fire();
    expect(sentText()).toBe("actually target main");
  });

  // THE ORDERING CASE. The arrow names its lead row and cancels; that cancel produces
  // the very boundary this capture runs at, so the preference is what reorders it.
  it("leads with the row the arrow named", async () => {
    preferSteerFirst(CHAT, "steer-2");
    noteBoundaryDrop(CHAT, rows("first", "second", "third"));
    await fire();
    expect(sentText()).toBe("second\n\nfirst\n\nthird");
  });

  // The whole reason the arrow arms an ID: a row confirmed between the click and the
  // boundary is in the boundary's own read, so it is carried rather than dropped.
  it("carries a row that arrived after the arrow was pressed", async () => {
    preferSteerFirst(CHAT, "steer-1");
    noteBoundaryDrop(CHAT, rows("pressed", "arrived later"));
    await fire();
    expect(sentText()).toBe("pressed\n\narrived later");
  });

  // A preference for a row this boundary is not carrying must not drop the batch: the
  // named row was read or discarded, and the others are still owed a turn.
  it("keeps arrival order when the named row is not in the batch", async () => {
    preferSteerFirst(CHAT, "steer-gone");
    noteBoundaryDrop(CHAT, rows("first", "second"));
    await fire();
    expect(sentText()).toBe("first\n\nsecond");
  });

  // The preference is spent by the boundary that used it, so an unrelated later turn
  // end cannot resurrect an order the reader asked for once.
  it("spends the arrow's preference on the boundary that used it", async () => {
    preferSteerFirst(CHAT, "steer-2");
    noteBoundaryDrop(CHAT, rows("first", "second"));
    await fire();
    forgetSteerResend(CHAT);
    vi.clearAllMocks();
    noteBoundaryDrop(CHAT, rows("first", "second"));
    await fire();
    expect(sentText()).toBe("first\n\nsecond");
  });

  // A cancel that never landed leaves no boundary to order, so the preference goes.
  it("forgets the arrow's preference when its cancel fails", async () => {
    preferSteerFirst(CHAT, "steer-2");
    forgetSteerPreference(CHAT);
    noteBoundaryDrop(CHAT, rows("first", "second"));
    await fire();
    expect(sentText()).toBe("first\n\nsecond");
  });

  // It runs on every settled turn_closed of every chat, so the empty case is the
  // common one and must cost nothing.
  it("sends nothing when nothing is armed", () => {
    runArmedResend(CHAT);
    expect(sendPromptTo).not.toHaveBeenCalled();
    expect(clearDispatch).not.toHaveBeenCalled();
  });

  it("arms nothing from an empty list, and nothing from empty strings", () => {
    noteBoundaryDrop(CHAT, []);
    noteBoundaryDrop(CHAT, rows("", ""));
    runArmedResend(CHAT);
    expect(sendPromptTo).not.toHaveBeenCalled();
  });

  it("forgets an armed slot when the chat goes", () => {
    noteBoundaryDrop(CHAT, rows("one"));
    forgetSteerResend(CHAT);
    runArmedResend(CHAT);
    expect(sendPromptTo).not.toHaveBeenCalled();
  });

  it("keeps each chat's slot to itself", async () => {
    noteBoundaryDrop(CHAT, rows("mine"));
    noteBoundaryDrop("c2", rows("theirs"));
    await fire("c2");
    expect(sentText()).toBe("theirs");
    forgetSteerResend("c2");
  });

  // A steer confirmed just before the boundary can still be sitting in KAS's buffer,
  // where it would be injected into the very turn this opens — the reader's message
  // twice. Both actions hold `chat:<id>`, which is FIFO, so the drain is ordered by
  // its scope rather than by an await.
  it("drains the buffer before it opens the new turn", async () => {
    noteBoundaryDrop(CHAT, rows("one"));
    await fire();
    expect(clearDispatch).toHaveBeenCalledWith({ chatID: CHAT });
    expect(clearDispatch.mock.invocationCallOrder[0]).toBeLessThan(
      Number(sendPromptTo.mock.invocationCallOrder[0]),
    );
  });

  // It sends a PROMPT through the low-level primitive rather than submitPrompt: that
  // one takes the composer's staged attachments, and this is not the message the
  // reader is composing.
  it("sends it as a new turn under its own message id", async () => {
    noteBoundaryDrop(CHAT, rows("one"));
    await fire();
    // The id is this case's oracle; the batch's own ids ride the same opts and have
    // their own cases below, so an exact-object assertion here would fail on them
    // rather than on a lost message id.
    expect(sendPromptTo).toHaveBeenCalledWith(
      CHAT,
      "one",
      expect.objectContaining({ messageID: "m-resend" }),
    );
  });
});

describe("a chat that is busy again", () => {
  beforeEach(() => {
    forgetSteerResend(CHAT);
    vi.clearAllMocks();
  });

  // A turn can start underneath the boundary, or the admission slot can be held by a
  // spawn. Neither is a refusal of the message, so it waits for the next boundary
  // rather than being converted back into the steer it just failed to be.
  it("re-arms on a refusal and sends at the next boundary", async () => {
    sendPromptTo.mockResolvedValueOnce("queued");
    noteBoundaryDrop(CHAT, rows("one"));
    await fire();
    expect(sendPromptTo).toHaveBeenCalledTimes(1);
    expect(restoreFailedSend).not.toHaveBeenCalled();

    sendPromptTo.mockResolvedValue("sent");
    runArmedResend(CHAT);
    await vi.waitFor(() => {
      expect(sendPromptTo).toHaveBeenCalledTimes(2);
    });
    expect(String(sendPromptTo.mock.calls[1]?.[1])).toBe("one");
  });

  // Bounded, so a chat churning turn boundaries faster than the round trips cannot
  // hold the message in the air forever. Past the budget it lands on the ordinary
  // failed-send surface with the text back in the box.
  it("gives up after the second refusal and hands the text back", async () => {
    sendPromptTo.mockResolvedValue("starting");
    noteBoundaryDrop(CHAT, rows("one"));
    await fire();
    runArmedResend(CHAT);
    await vi.waitFor(() => {
      expect(sendPromptTo).toHaveBeenCalledTimes(2);
    });

    expect(restoreFailedSend).toHaveBeenCalledWith(CHAT, "one");
    expect(reportSendRefused).toHaveBeenCalledTimes(1);
    // And nothing is left armed, so a later unrelated boundary sends nothing.
    runArmedResend(CHAT);
    expect(sendPromptTo).toHaveBeenCalledTimes(2);
  });

  // A hard refusal is not a race, so it spends no retry: the text goes back to the
  // composer on the first answer.
  it("hands the text back at once on a hard failure", async () => {
    sendPromptTo.mockResolvedValue("failed");
    noteBoundaryDrop(CHAT, rows("one"));
    await fire();
    await vi.waitFor(() => {
      expect(restoreFailedSend).toHaveBeenCalledWith(CHAT, "one");
    });
    expect(sendPromptTo).toHaveBeenCalledTimes(1);
  });

  // A tombstoned chat is terminal like a hard failure: no later boundary will ever
  // carry the text, so re-arming would hold it in the air for a chat that is gone.
  it("treats a gone chat as terminal and spends no retry", async () => {
    sendPromptTo.mockResolvedValue("gone");
    noteBoundaryDrop(CHAT, rows("one"));
    await fire();
    await vi.waitFor(() => {
      expect(restoreFailedSend).toHaveBeenCalledWith(CHAT, "one");
    });
    expect(reportSendRefused).toHaveBeenCalledTimes(1);
    runArmedResend(CHAT);
    expect(sendPromptTo).toHaveBeenCalledTimes(1);
  });

  // The retry budget is per STALL, not per chat for the page's life: a send that
  // lands clears it, so a later refusal gets its own retry.
  it("clears the retry budget once a send lands", async () => {
    sendPromptTo.mockResolvedValueOnce("queued");
    noteBoundaryDrop(CHAT, rows("one"));
    await fire();
    sendPromptTo.mockResolvedValue("sent");
    runArmedResend(CHAT);
    await vi.waitFor(() => {
      expect(sendPromptTo).toHaveBeenCalledTimes(2);
    });

    sendPromptTo.mockResolvedValueOnce("queued");
    noteBoundaryDrop(CHAT, rows("two"));
    runArmedResend(CHAT);
    await vi.waitFor(() => {
      expect(sendPromptTo).toHaveBeenCalledTimes(3);
    });
    expect(restoreFailedSend).not.toHaveBeenCalled();

    sendPromptTo.mockResolvedValue("sent");
    runArmedResend(CHAT);
    await vi.waitFor(() => {
      expect(sendPromptTo).toHaveBeenCalledTimes(4);
    });
    expect(String(sendPromptTo.mock.calls[3]?.[1])).toBe("two");
  });
});

/** The `resends` the send primitive was handed on the Nth call. `undefined` and `[]`
 *  are different answers here — the first means the carry dropped the ids, the second
 *  means it carried an empty batch — so the helper preserves both. */
function sentResends(call = 0): readonly string[] | undefined {
  const opts = sendPromptTo.mock.calls[call]?.[2] as { resends?: readonly string[] } | undefined;
  return opts?.resends;
}

// THE IDS ARE THE OTHER HALF OF THE CARRY, and the server has been waiting for them:
// `EntrySteer.Resends` and `turn_open.resent_steer_ids` are documented wire fields with
// a complete consumer chain (the ledger, the merge's union rule, the note's `resent?`
// flag) and no producer, because the capture stored the joined TEXT and discarded the
// pairs it was handed. Carrying them is what lets one intent re-sent across a boundary
// draw ONE note with its own provenance instead of two unrelated ones.
describe("the armed slot carries the ids, not just the text", () => {
  beforeEach(() => {
    forgetSteerResend(CHAT);
    vi.clearAllMocks();
    sendPromptTo.mockResolvedValue("sent");
  });

  it("names every dropped steer, in the order the send's own text is in", async () => {
    // Same ordering the text takes: the arrow's row leads, so the ids and the words
    // cannot disagree about which message came first.
    preferSteerFirst(CHAT, "steer-2");
    noteBoundaryDrop(CHAT, rows("first", "second", "third"));
    await fire();

    expect(sentText()).toBe("second\n\nfirst\n\nthird");
    expect(sentResends()).toEqual(["steer-2", "steer-1", "steer-3"]);
  });

  it("re-arms the ids with the text when a busy chat refuses the send", async () => {
    // The retry re-offers the SAME batch at the next boundary, so a refusal must not
    // strip the provenance off it — that would make the second attempt produce a note
    // the first would not have.
    sendPromptTo.mockResolvedValueOnce("queued");
    noteBoundaryDrop(CHAT, rows("one", "two"));
    await fire();

    sendPromptTo.mockResolvedValue("sent");
    runArmedResend(CHAT);
    await vi.waitFor(() => {
      expect(sendPromptTo).toHaveBeenCalledTimes(2);
    });
    expect(sentResends(1)).toEqual(["steer-1", "steer-2"]);
  });
});
