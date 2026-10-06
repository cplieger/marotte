// A refused Edit puts the replaced draft back, and the delete it waits on can take a lock, a clear
// and a KAS round trip.
import { describe, it, expect, beforeAll, beforeEach, vi } from "vitest";
import type * as ActionsIndex from "./actions/index.js";
import type * as ChatActions from "./actions/chat.js";
import type * as NoticeSubject from "./notice-subject.js";

type RemoveOutcome =
  { status: "success" } | { status: "error"; error: { status?: number; message: string } };

const mocks = vi.hoisted(() => {
  const errorToastMock = vi.fn();
  return {
    removeDispatch: vi.fn((_args: { chatID: string; steerID: string }) => ({
      outcome: Promise.resolve<RemoveOutcome>({ status: "success" }),
    })),
    saveDispatch: vi.fn(),
    saveFlush: vi.fn(),
    savePending: vi.fn(() => false),
    errorToastMock,
    chatNoticeMock: vi.fn((_chatID: string, message: string, _level?: string, _name?: string) => {
      errorToastMock(message);
      return () => undefined;
    }),
  };
});

// The debounce is the library's; what matters here is which chat and text reach it.
vi.mock("./actions/index.js", async (importOriginal) => ({
  ...(await importOriginal<typeof ActionsIndex>()),
  debouncedDispatch: () =>
    Object.assign(mocks.saveDispatch, {
      isPending: mocks.savePending,
      flush: mocks.saveFlush,
      cancel: vi.fn(),
    }),
  registerCleanup: vi.fn(),
}));
vi.mock("./actions/chat.js", async (importOriginal) => ({
  ...(await importOriginal<typeof ChatActions>()),
  removeSteer: { dispatch: mocks.removeDispatch },
}));
// The refusal is a chat notice; the message is what these cases read.
vi.mock("./notice-subject.js", async (importOriginal) => {
  const store = await import("./store.js");
  return {
    ...(await importOriginal<typeof NoticeSubject>()),
    chatNotice: mocks.chatNoticeMock,
    subjectName: (id: string) => store.get(id)?.name ?? "",
  };
});

const { removeDispatch, saveDispatch, saveFlush, savePending, errorToastMock } = mocks;

import { setSessions, setActive, recordSteerQueued } from "./store.js";
import { initPendingSteers } from "./pending-steers.js";
import {
  initComposerState,
  restoreComposerState,
  retargetComposer,
  dropComposerState,
} from "./composer-state.js";
import type { Session } from "./types.js";

function makeSession(id: string): Session {
  return {
    id,
    name: id,
    model: "",
    acp_session_id: "",
    current_mode_id: "",
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

function input(): HTMLTextAreaElement {
  return document.getElementById("prompt-input") as HTMLTextAreaElement;
}

/** A keystroke: the value AND the `input` event that makes it the draft. */
function type(text: string): void {
  input().value = text;
  input().dispatchEvent(new Event("input", { bubbles: true }));
}

/** Holds the delete's reply open and hands back the hand that lands it. */
function holdTheReply(): (outcome: RemoveOutcome) => void {
  let land: (outcome: RemoveOutcome) => void = () => undefined;
  removeDispatch.mockReturnValue({
    outcome: new Promise<RemoveOutcome>((resolve) => {
      land = resolve;
    }),
  });
  return land;
}

const REFUSED: RemoveOutcome = {
  status: "error",
  error: { status: 409, message: "The agent already read that message" },
};

async function refusalReported(): Promise<void> {
  await vi.waitFor(() => {
    expect(errorToastMock).toHaveBeenCalledWith("The agent already read that message");
  });
}

/** Edit the one row chat-1's dock shows, with "half a draft" typed beforehand. */
function editWithADraft(): void {
  type("half a draft");
  recordSteerQueued("chat-1", { id: "steer-1", text: "actually target main", origin: "user" });
  const edit = document.querySelector<HTMLButtonElement>(
    '#steer-stack .steer-act[aria-label="Edit this message"]',
  );
  if (edit === null) {
    throw new Error("no Edit control");
  }
  edit.click();
  expect(input().value, "the Edit fills the box at once").toBe("actually target main");
}

describe("a refused Edit's rollback", () => {
  // Both modules bind the elements once, so the page outlives every case.
  beforeAll(() => {
    document.body.innerHTML = `
      <ul id="steer-stack" class="steer-stack hidden"></ul>
      <textarea id="prompt-input"></textarea>
      <ul id="attachment-row" class="hidden"></ul>`;
    initComposerState();
    initPendingSteers();
  });

  beforeEach(() => {
    dropComposerState("chat-1");
    dropComposerState("chat-2");
    setSessions([makeSession("chat-1"), makeSession("chat-2")]);
    setActive("chat-1");
    restoreComposerState("chat-1");
    removeDispatch.mockReset();
    saveDispatch.mockClear();
    saveFlush.mockClear();
    savePending.mockReturnValue(false);
    errorToastMock.mockClear();
  });

  it("puts the replaced draft back when nothing moved", async () => {
    const land = holdTheReply();
    editWithADraft();

    land(REFUSED);
    await refusalReported();

    expect(input().value).toBe("half a draft");
    expect(saveDispatch).toHaveBeenLastCalledWith({ chatID: "chat-1", text: "half a draft" });
  });

  it("keeps what the reader typed while the delete was pending", async () => {
    const land = holdTheReply();
    editWithADraft();
    type("actually target the release branch");

    land(REFUSED);
    await refusalReported();

    expect(input().value).toBe("actually target the release branch");
    expect(saveDispatch).toHaveBeenLastCalledWith({
      chatID: "chat-1",
      text: "actually target the release branch",
    });
    retargetComposer("chat-2");
    retargetComposer("chat-1");
    expect(input().value, "the draft map kept it too").toBe("actually target the release branch");
  });

  it("leaves another chat's composer alone and converges the chat that edited", async () => {
    const land = holdTheReply();
    editWithADraft();
    setActive("chat-2");
    retargetComposer("chat-2");
    type("words for the other chat");
    savePending.mockReturnValue(true);

    land(REFUSED);
    await refusalReported();

    expect(input().value).toBe("words for the other chat");
    // The live chat's pending save goes out first, so the rollback's own save cannot take its place
    // in the one shared debounce slot.
    expect(saveFlush.mock.calls).toEqual([
      [{ chatID: "chat-2", text: "words for the other chat" }],
      [{ chatID: "chat-1", text: "half a draft" }],
    ]);
    savePending.mockReturnValue(false);
    setActive("chat-1");
    retargetComposer("chat-1");
    expect(input().value, "the chat that edited has its draft back").toBe("half a draft");
  });
});
