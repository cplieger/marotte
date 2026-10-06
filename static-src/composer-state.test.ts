// Per-chat composer state: the draft and the staged attachments. The bug is a bleed across a switch, a reload or a
// failed send, since one textarea and one pill row serve every chat.
import { describe, it, expect, beforeEach, vi } from "vitest";

const { mockDispatch, mockFlush, mockPending } = vi.hoisted(() => ({
  mockDispatch: vi.fn(),
  mockFlush: vi.fn(),
  mockPending: vi.fn(() => false),
}));

// The debounce is the library's; the fake records chat id and text and reports its own pending state.
vi.mock("./actions/index.js", () => ({
  debouncedDispatch: () =>
    Object.assign(mockDispatch, { isPending: mockPending, flush: mockFlush, cancel: vi.fn() }),
  registerCleanup: vi.fn(),
}));
// Both composer writers: a mock naming only one fails the module's import, not an assertion.
vi.mock("./actions/chat.js", () => ({
  setDraft: { name: "chat.set_draft" },
  setAttachments: { name: "chat.set_attachments" },
}));

import {
  initComposerState,
  noteComposerText,
  flushComposerDraft,
  saveComposerState,
  restoreComposerState,
  retargetComposer,
  restoreFailedSend,
  restoreRefusedEdit,
  composerDraft,
  seedComposerState,
  adoptRemoteComposerState,
  dropComposerState,
  _resetComposerStateForTest,
} from "./composer-state.js";
import {
  addAttachment,
  takeAttachments,
  addAttachmentTo,
  attachmentGeneration,
} from "./attachments.js";
import { setSessions } from "./store.js";
import type { Session } from "./types.js";

function makeSession(id: string, draft?: string): Session {
  const s: Session = {
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
  if (draft !== undefined) {
    s.draft = draft;
  }
  return s;
}

function input(): HTMLTextAreaElement {
  return document.getElementById("prompt-input") as HTMLTextAreaElement;
}

/** The value AND the `input` event a keystroke produces; a bare `.value` write is deliberately not this. */
function type(text: string): void {
  input().value = text;
  input().dispatchEvent(new Event("input"));
}

beforeEach(() => {
  document.body.innerHTML = `
    <div id="prompt-box">
      <textarea id="prompt-input"></textarea>
      <ul id="attachment-row" class="hidden"></ul>
    </div>`;
  _resetComposerStateForTest();
  // Dropped so the attachment stash cannot carry between cases through the module singleton.
  for (const id of ["c1", "c2", "c3"]) {
    dropComposerState(id);
  }
  mockDispatch.mockClear();
  mockFlush.mockClear();
  mockPending.mockReturnValue(false);
  setSessions([]);
  initComposerState();
});

describe("draft text across a chat switch", () => {
  it("keeps each chat's typed text and shows the right one", () => {
    restoreComposerState("c1");
    input().value = "half a question about auth";
    noteComposerText(input().value);

    saveComposerState();
    restoreComposerState("c2");
    expect(input().value).toBe("");

    input().value = "unrelated thought";
    noteComposerText(input().value);
    saveComposerState();
    restoreComposerState("c1");
    expect(input().value).toBe("half a question about auth");

    saveComposerState();
    restoreComposerState("c2");
    expect(input().value).toBe("unrelated thought");
  });

  // The reported bug: three call sites move the store's active chat before its activation repaints, so a keystroke in
  // that window landed in the previous chat's draft and vanished from the box.
  describe("a chat that becomes active before its tab does", () => {
    it("files a keystroke in the window under the NEW chat", () => {
      restoreComposerState("c1");
      type("half a question about auth");

      // createSession: setActive(new) → retargetComposer(new) → await the tab.
      retargetComposer("c2");
      type("the new chat's first line");

      saveComposerState();
      restoreComposerState("c2");
      expect(input().value).toBe("the new chat's first line");

      saveComposerState();
      restoreComposerState("c1");
      expect(input().value).toBe("half a question about auth");
    });

    it("persists the window's text under the new chat, not the old one", () => {
      restoreComposerState("c1");
      type("half a question about auth");
      mockDispatch.mockClear();

      retargetComposer("c2");
      type("the new chat's first line");

      expect(mockDispatch).toHaveBeenLastCalledWith({
        chatID: "c2",
        text: "the new chat's first line",
      });
    });
  });

  it("empties the box for a chat with no draft rather than leaving it alone", () => {
    restoreComposerState("c1");
    input().value = "text belonging to c1";
    noteComposerText(input().value);
    saveComposerState();
    restoreComposerState("c3");
    expect(input().value).toBe("");
  });

  it("persists the outgoing chat's text under the outgoing chat's id", () => {
    restoreComposerState("c1");
    noteComposerText("for c1");
    expect(mockDispatch).toHaveBeenLastCalledWith({ chatID: "c1", text: "for c1" });

    input().value = "for c1";
    mockPending.mockReturnValue(true);
    saveComposerState();
    // The flush carries c1: the outgoing id is unrecoverable once the store moves on.
    expect(mockFlush).toHaveBeenCalledWith({ chatID: "c1", text: "for c1" });
  });

  it("records nothing when no chat owns the composer", () => {
    noteComposerText("orphan keystroke");
    expect(mockDispatch).not.toHaveBeenCalled();
  });

  it("does not re-dispatch text the server already holds", () => {
    restoreComposerState("c1");
    noteComposerText("same");
    noteComposerText("same");
    expect(mockDispatch).toHaveBeenCalledTimes(1);
  });
});

describe("the listeners this module owns on the composer element", () => {
  it("records typing through the input event", () => {
    restoreComposerState("c1");
    input().value = "typed";
    input().dispatchEvent(new Event("input"));
    expect(mockDispatch).toHaveBeenLastCalledWith({ chatID: "c1", text: "typed" });
  });

  it("records a programmatic write, which prompt-input announces the same way", () => {
    // The submit clear and failed-send restore write .value and dispatch input, or autosave keeps a sent message.
    restoreComposerState("c1");
    input().value = "about to be sent";
    input().dispatchEvent(new Event("input"));
    input().value = "";
    input().dispatchEvent(new Event("input"));
    expect(mockDispatch).toHaveBeenLastCalledWith({ chatID: "c1", text: "" });
  });

  it("flushes on blur", () => {
    restoreComposerState("c1");
    type("half typed");
    mockPending.mockReturnValue(true);
    input().dispatchEvent(new FocusEvent("blur"));
    expect(mockFlush).toHaveBeenCalledWith({ chatID: "c1", text: "half typed" });
  });
});

describe("flushing a pending save", () => {
  it("flushes the recorded draft when a save is pending (blur, close, unload)", () => {
    restoreComposerState("c1");
    type("typed but not yet saved");
    mockPending.mockReturnValue(true);
    flushComposerDraft();
    expect(mockFlush).toHaveBeenCalledWith({ chatID: "c1", text: "typed but not yet saved" });
  });

  it("sends nothing when the debounce already fired", () => {
    restoreComposerState("c1");
    mockPending.mockReturnValue(false);
    flushComposerDraft();
    expect(mockFlush).not.toHaveBeenCalled();
  });

  it("flushes on pagehide, which is the unload event iOS actually delivers", () => {
    restoreComposerState("c1");
    type("unsent");
    mockPending.mockReturnValue(true);
    window.dispatchEvent(new Event("pagehide"));
    expect(mockFlush).toHaveBeenCalledWith({ chatID: "c1", text: "unsent" });
  });

  it("flushes the recorded draft, not whatever the box happens to be showing", () => {
    // The textarea is a display surface: ArrowUp shows a submitted prompt without an `input` event, so the map is the draft.
    restoreComposerState("c1");
    type("the real draft");
    input().value = "a prompt from the history"; // silent, like setInputValue
    mockPending.mockReturnValue(true);
    flushComposerDraft();
    expect(mockFlush).toHaveBeenCalledWith({ chatID: "c1", text: "the real draft" });
  });

  it("keeps the mapped draft when a save runs while history is on screen", () => {
    restoreComposerState("c1");
    type("the real draft");
    input().value = "a prompt from the history";
    saveComposerState();
    restoreComposerState("c1");
    expect(input().value).toBe("the real draft");
  });
});

describe("adopting the server's draft (the reload case)", () => {
  it("puts the stored draft back in the box on first load of a chat", () => {
    setSessions([makeSession("c1", "survived the reload")]);
    restoreComposerState("c1");
    expect(input().value).toBe("");
    seedComposerState("c1");
    expect(input().value).toBe("survived the reload");
  });

  it("loses to a draft this device is already holding", () => {
    setSessions([makeSession("c1", "stale server copy")]);
    restoreComposerState("c1");
    input().value = "what the user is typing now";
    noteComposerText(input().value);
    seedComposerState("c1");
    expect(input().value).toBe("what the user is typing now");
    saveComposerState();
    restoreComposerState("c1");
    expect(input().value).toBe("what the user is typing now");
  });

  it("loses to text already in the box, even with no recorded draft", () => {
    setSessions([makeSession("c1", "server copy")]);
    restoreComposerState("c1");
    input().value = "restored after a failed send";
    seedComposerState("c1");
    expect(input().value).toBe("restored after a failed send");
  });

  it("does not touch the box when the seed arrives for another chat", () => {
    setSessions([makeSession("c1", "c1 draft"), makeSession("c2", "c2 draft")]);
    restoreComposerState("c2");
    input().value = "c2 live text";
    seedComposerState("c1");
    expect(input().value).toBe("c2 live text");
  });
});

describe("restoring a send that the server refused", () => {
  it("puts the text back when the failing chat is the one on screen", () => {
    restoreComposerState("c1");
    restoreFailedSend("c1", "the refused message");
    expect(input().value).toBe("the refused message");
  });

  it("parks it under the failing chat when the reader has moved on", () => {
    restoreComposerState("c1");
    saveComposerState();
    restoreComposerState("c2");

    restoreFailedSend("c1", "the refused message");
    expect(input().value).toBe("");

    saveComposerState();
    restoreComposerState("c1");
    expect(input().value).toBe("the refused message");
  });

  it("persists it under the failing chat, so a reload keeps it", () => {
    restoreComposerState("c2");
    mockDispatch.mockClear();
    restoreFailedSend("c1", "the refused message");
    expect(mockDispatch).toHaveBeenLastCalledWith({
      chatID: "c1",
      text: "the refused message",
    });
  });

  it("loses to a draft the user has started since the send", () => {
    restoreComposerState("c1");
    type("already typing the next thing");
    restoreFailedSend("c1", "the refused message");
    expect(input().value).toBe("already typing the next thing");
  });

  it("leaves a non-empty box alone even with no recorded draft", () => {
    restoreComposerState("c1");
    input().value = "a prompt recalled from history";
    restoreFailedSend("c1", "the refused message");
    expect(input().value).toBe("a prompt recalled from history");
  });

  it("ignores an empty chat id and empty text", () => {
    restoreComposerState("c1");
    restoreFailedSend("", "the refused message");
    restoreFailedSend("c1", "");
    expect(input().value).toBe("");
  });
});

// Edit-side races run through the real dock in pending-steers-edit-rollback.test.ts; these are the map-only states.
describe("undoing a refused Edit", () => {
  it("leaves a history preview in the box while restoring the draft behind it", () => {
    restoreComposerState("c1");
    type("the taken-back steer");
    input().value = "a prompt recalled from history";
    mockDispatch.mockClear();

    restoreRefusedEdit("c1", "the taken-back steer", "half a draft");

    expect(input().value).toBe("a prompt recalled from history");
    expect(mockDispatch).toHaveBeenLastCalledWith({ chatID: "c1", text: "half a draft" });
  });

  it("does not bring back the draft of a chat that was closed", () => {
    restoreComposerState("c1");
    type("the taken-back steer");
    dropComposerState("c1");
    restoreComposerState("c2");
    mockDispatch.mockClear();

    restoreRefusedEdit("c1", "the taken-back steer", "half a draft");

    expect(mockDispatch).not.toHaveBeenCalled();
    expect(mockFlush).not.toHaveBeenCalled();
    expect(composerDraft("c1")).toBe("");
  });
});

describe("attachments across a chat switch", () => {
  it("parks the outgoing chat's attachments and brings them back", () => {
    restoreComposerState("c1");
    addAttachment("src/a.ts");
    addAttachment("src/b.ts");

    saveComposerState();
    restoreComposerState("c2");
    expect(takeAttachments()).toEqual([]);

    saveComposerState();
    restoreComposerState("c1");
    expect(takeAttachments().map((a) => a.path)).toEqual(["src/a.ts", "src/b.ts"]);
  });

  it("restores a failed send's attachments to the chat that failed, not the visible one", () => {
    restoreComposerState("c1");
    addAttachment("src/a.ts");
    takeAttachments();

    saveComposerState();
    restoreComposerState("c2");
    addAttachmentTo("c1", "src/a.ts");
    expect(takeAttachments()).toEqual([]);

    saveComposerState();
    restoreComposerState("c1");
    expect(takeAttachments().map((a) => a.path)).toEqual(["src/a.ts"]);
  });

  it("forgets a closed chat's parked attachments", () => {
    restoreComposerState("c1");
    addAttachment("src/a.ts");
    saveComposerState();
    restoreComposerState("c2");

    dropComposerState("c1");
    saveComposerState();
    restoreComposerState("c1");
    expect(takeAttachments()).toEqual([]);
  });

  // Dropping the live chat's state clears the box: on the close that empties the strip nothing activates after, and Send
  // would post the stale text to whatever chat the store pointed at.
  it("clears the composer when the live chat's state is dropped", () => {
    restoreComposerState("c1");
    type("half a thought");

    dropComposerState("c1");

    expect(input().value).toBe("");
  });

  it("leaves the composer alone when a background chat's state is dropped", () => {
    restoreComposerState("c1");
    type("still typing");

    dropComposerState("c2");

    expect(input().value).toBe("still typing");
  });

  // The close must win the race: a request already in flight must not write forgotten attachments back.
  it("does not resurrect a closed chat's attachments when the send fails afterwards", () => {
    restoreComposerState("c1");
    addAttachment("src/a.ts");
    const gen = attachmentGeneration("c1"); // read where submit.ts reads it
    takeAttachments(); // Send fires: the row empties

    dropComposerState("c1"); // the × while the request is in flight
    addAttachmentTo("c1", "src/a.ts", gen); // the failure lands after the close

    restoreComposerState("c1"); // reopened from History
    expect(takeAttachments()).toEqual([]);
  });

  it("still restores a failed send's attachments when the chat was NOT closed", () => {
    // A failure must keep putting the pills back, or a throttled turn costs the files too.
    restoreComposerState("c1");
    addAttachment("src/a.ts");
    const gen = attachmentGeneration("c1");
    takeAttachments();

    saveComposerState();
    restoreComposerState("c2"); // the user is looking elsewhere when it fails
    addAttachmentTo("c1", "src/a.ts", gen);

    saveComposerState();
    restoreComposerState("c1");
    expect(takeAttachments().map((a) => a.path)).toEqual(["src/a.ts"]);
  });

  it("keeps a genuinely new attachment on a reopened chat", () => {
    // Invalidation is per generation: attaching after a close belongs to the chat like any other.
    restoreComposerState("c1");
    addAttachment("src/old.ts");
    const stale = attachmentGeneration("c1");
    takeAttachments();
    dropComposerState("c1");

    restoreComposerState("c1");
    addAttachment("src/new.ts");
    addAttachmentTo("c1", "src/old.ts", stale); // refused
    expect(takeAttachments().map((a) => a.path)).toEqual(["src/new.ts"]);
  });
});

// A `draft_changed` frame converges a device that is not typing in that chat.
describe("adopting a remote composer change", () => {
  it("updates a chat this device is not looking at", () => {
    restoreComposerState("c1");
    adoptRemoteComposerState("c2", "typed on the desktop", []);

    saveComposerState();
    restoreComposerState("c2");
    expect(input().value).toBe("typed on the desktop");
  });

  // The live chat's map entry is authoritative: adopting would overwrite the box under the caret with a stale value.
  it("ignores a frame for the chat on screen", () => {
    restoreComposerState("c1");
    type("what I am typing right now");
    adoptRemoteComposerState("c1", "what the desktop had", []);
    expect(input().value).toBe("what I am typing right now");

    saveComposerState();
    restoreComposerState("c2");
    saveComposerState();
    restoreComposerState("c1");
    expect(input().value).toBe("what I am typing right now");
  });

  it("drops a frame with no chat id", () => {
    restoreComposerState("c1");
    type("mine");
    adoptRemoteComposerState("", "nobody's", []);
    expect(input().value).toBe("mine");
  });

  // Unlike the seed, a frame beats the local copy: it came from a write the server accepted.
  it("replaces a parked draft rather than deferring to it", () => {
    restoreComposerState("c1");
    type("stale, flushed an hour ago");
    saveComposerState();
    restoreComposerState("c2");

    adoptRemoteComposerState("c1", "fresh, from the desktop", []);

    saveComposerState();
    restoreComposerState("c1");
    expect(input().value).toBe("fresh, from the desktop");
  });

  // Both halves ride one frame because a receiver cannot know which command fired.
  it("carries the attachments with the text", () => {
    restoreComposerState("c1");
    adoptRemoteComposerState("c2", "look at these", ["docs/spec.pdf"]);

    saveComposerState();
    restoreComposerState("c2");
    expect(input().value).toBe("look at these");
    expect(takeAttachments().map((a) => a.path)).toEqual(["docs/spec.pdf"]);
  });

  // An adoption is not a local edit: publishing it back would make every device re-apply what it already had.
  it("persists nothing", () => {
    restoreComposerState("c1");
    mockDispatch.mockClear();
    adoptRemoteComposerState("c2", "from elsewhere", ["docs/spec.pdf"]);
    expect(mockDispatch).not.toHaveBeenCalled();
    expect(mockFlush).not.toHaveBeenCalled();
  });
});

// The seed adopts the whole composer: the sentence without the files it describes is worse than neither.
describe("seeding the staged attachments from the chat record", () => {
  it("restores the row a reload emptied", () => {
    setSessions([{ ...makeSession("c1", "half a question"), attachments: ["docs/spec.pdf"] }]);
    restoreComposerState("c1");
    seedComposerState("c1");

    expect(input().value).toBe("half a question");
    expect(takeAttachments().map((a) => a.path)).toEqual(["docs/spec.pdf"]);
  });

  it("loses to a row the user has already staged into", () => {
    setSessions([{ ...makeSession("c1", ""), attachments: ["docs/theirs.pdf"] }]);
    restoreComposerState("c1");
    addAttachment("src/mine.ts");
    seedComposerState("c1");

    expect(takeAttachments().map((a) => a.path)).toEqual(["src/mine.ts"]);
  });
});
