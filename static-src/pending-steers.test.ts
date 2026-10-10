// The steer stack is a pure projection of `session.steers`, so these cases drive the store the way
// the submit path and the SSE handlers do and read the DOM the way a person does. A row leaves on
// its own `steer` entry or its `removed` frame; nothing client-side drops one.
import { describe, it, expect, beforeAll, afterAll, beforeEach, vi } from "vitest";

// vi.hoisted because pending-steers.js is a STATIC import below: the mock factories run during that
// import's resolution, before a plain top-level const is initialized.
const mocks = vi.hoisted(() => {
  const errorToastMock = vi.fn();
  return {
    clearDispatch: vi.fn(() => Promise.resolve(true)),
    cancelDispatch: vi.fn((_args: { chatID: string; lead?: string }) => ({
      outcome: Promise.resolve<{ status: string }>({ status: "success" }),
    })),
    removeDispatch: vi.fn((_args: { chatID: string; steerID: string }) => ({
      outcome: Promise.resolve<RemoveOutcome>({ status: "success" }),
    })),
    confirmMock: vi.fn((_message: string) => Promise.resolve(true)),
    setComposerValueMock: vi.fn(),
    composerDraftMock: vi.fn((_chatID: string) => ""),
    restoreRefusedEditMock: vi.fn(),
    errorToastMock,
    chatNoticeMock: vi.fn((_chatID: string, message: string, _level?: string, _name?: string) => {
      errorToastMock(message);
      return () => undefined;
    }),
    unqueueDispatch: vi.fn((_args: { chatID: string; messageID: string }) => ({
      outcome: Promise.resolve<{ status: string }>({ status: "success" }),
    })),
    addAttachmentMock: vi.fn((_chatID: string, _path: string) => true),
    removeAttachmentMock: vi.fn(),
  };
});

type RemoveOutcome =
  { status: "success" } | { status: "error"; error: { status?: number; message: string } };

vi.mock("./actions/chat.js", () => ({
  clearSteers: { dispatch: mocks.clearDispatch },
  cancelTurn: { dispatch: mocks.cancelDispatch },
  removeSteer: { dispatch: mocks.removeDispatch },
  unqueuePrompt: { dispatch: mocks.unqueueDispatch },
}));
// Every export is present so real-ESM linking succeeds; only the restore is observed.
vi.mock("./attachments.js", () => ({
  addAttachmentTo: mocks.addAttachmentMock,
  removeAttachmentFrom: mocks.removeAttachmentMock,
  attachmentGeneration: vi.fn(() => 0),
  addAttachment: vi.fn(),
  takeAttachments: vi.fn(() => []),
  hasAttachments: vi.fn(() => false),
  stashAttachments: vi.fn(),
  flushAttachments: vi.fn(),
  restoreAttachments: vi.fn(),
  dropAttachments: vi.fn(),
  seedAttachments: vi.fn(),
  adoptRemoteAttachments: vi.fn(),
  _resetAttachmentsForTest: vi.fn(),
}));
vi.mock("./confirm.js", () => ({ confirm: mocks.confirmMock }));
vi.mock("./composer-value.js", () => ({ setComposerValue: mocks.setComposerValueMock }));
// The rollback's own rules are pinned against the real composer in
// pending-steers-edit-rollback.test.ts; here only what reaches it is asserted.
vi.mock("./composer-state.js", () => ({
  composerDraft: mocks.composerDraftMock,
  restoreRefusedEdit: mocks.restoreRefusedEditMock,
}));
// The refusal is a chat notice; the message is what these cases read.
vi.mock("./notice-subject.js", async () => {
  const store = await import("./store.js");
  return {
    chatNotice: mocks.chatNoticeMock,
    subjectName: (id: string) => store.get(id)?.name ?? "",
  };
});

const {
  clearDispatch,
  cancelDispatch,
  removeDispatch,
  confirmMock,
  setComposerValueMock,
  composerDraftMock,
  restoreRefusedEditMock,
  errorToastMock,
  unqueueDispatch,
  addAttachmentMock,
  removeAttachmentMock,
} = mocks;

import {
  setSessions,
  setActive,
  recordSteerQueued,
  recordSteerSent,
  openTurn,
  appendEntry,
} from "./store.js";
import { initPendingSteers, mountSteerStack, type SteerStackSource } from "./pending-steers.js";
import { signal } from "@cplieger/reactive";
import type { Entry, PendingSteer, QueuedPrompt, Session } from "./types.js";
import { loadCSS, mountAppCSS, ruleBody } from "./__test-helpers__/css-rules.js";

function makeSession(chatID: string): Session {
  return {
    id: chatID,
    name: "test",
    model: "",
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

/** The seq the next entry of the fixture turn takes; `appendEntry` refuses any other. */
let seq = 0;

function landSteerEntry(
  chatID: string,
  steerID: string,
  text: string,
  state: "read" | "dropped" = "read",
): void {
  if (seq === 0) {
    const open: Entry = {
      id: "t1-open",
      turn: "t1",
      kind: "turn_open",
      seq: 0,
      ts: 1,
      payload: { source: "prompt", n: 1, prompt: { id: "m-0", text: "hi" } },
    };
    openTurn(chatID, open);
    seq = 1;
  }
  const entry: Entry = {
    id: steerID,
    turn: "t1",
    kind: "steer",
    seq,
    ts: seq + 1,
    payload: { text, origin: "user", state },
  };
  seq += 1;
  appendEntry(chatID, entry);
}

function rows(): HTMLElement[] {
  return Array.from(document.querySelectorAll<HTMLElement>("#steer-stack .steer-row"));
}

function firstRow(): HTMLElement {
  const el = rows()[0];
  if (el === undefined) {
    throw new Error("no row rendered");
  }
  return el;
}

function rowAt(i: number): HTMLElement {
  const el = rows()[i];
  if (el === undefined) {
    throw new Error(`no row ${String(i)}`);
  }
  return el;
}

function textOf(row: HTMLElement): string {
  return row.querySelector(".steer-text")?.textContent ?? "";
}

function labelOf(row: HTMLElement): string {
  return row.querySelector(".steer-state-label")?.textContent ?? "";
}

function stackHidden(): boolean {
  return document.getElementById("steer-stack")?.classList.contains("hidden") ?? false;
}

function actions(row: HTMLElement): string[] {
  return Array.from(row.querySelectorAll<HTMLElement>(".steer-act")).map(
    (b) => b.getAttribute("aria-label") ?? "",
  );
}

function actionButton(row: HTMLElement, labelStartsWith: string): HTMLButtonElement {
  const btn = Array.from(row.querySelectorAll<HTMLButtonElement>(".steer-act")).find((b) =>
    (b.getAttribute("aria-label") ?? "").startsWith(labelStartsWith),
  );
  if (btn === undefined) {
    throw new Error(`no action button starting with ${labelStartsWith}: ${String(actions(row))}`);
  }
  return btn;
}

function clickAction(row: HTMLElement, labelStartsWith: string): void {
  actionButton(row, labelStartsWith).click();
}

/** Per element, because `toEqual` over nodes compares markup and passes for a rebuild. */
function expectSameNodes(after: HTMLElement[], before: HTMLElement[]): void {
  expect(after).toHaveLength(before.length);
  before.forEach((el, i) => {
    expect(after[i], `row ${String(i)} is the same element`).toBe(el);
  });
}

function promptInput(): HTMLTextAreaElement {
  const el = document.getElementById("prompt-input");
  if (!(el instanceof HTMLTextAreaElement)) {
    throw new Error("no #prompt-input");
  }
  return el;
}

const PER_ROW = (text: string): string[] => [
  "Send this message now",
  "Edit this message",
  `Delete "${text}"`,
];

describe("the steer stack", () => {
  // The module captures the stack once at init, so it has to outlive every case.
  beforeAll(() => {
    document.body.innerHTML = `
      <ul id="steer-stack" class="steer-stack hidden"></ul>
      <textarea id="prompt-input"></textarea>`;
    initPendingSteers();
  });

  beforeEach(() => {
    seq = 0;
    setSessions([makeSession("chat-1")]);
    setActive("chat-1");
    expect(rows()).toHaveLength(0);
    promptInput().value = "";
    clearDispatch.mockClear();
    cancelDispatch.mockClear();
    removeDispatch.mockReset();
    removeDispatch.mockReturnValue({ outcome: Promise.resolve({ status: "success" }) });
    confirmMock.mockClear();
    setComposerValueMock.mockClear();
    composerDraftMock.mockReset();
    composerDraftMock.mockReturnValue("");
    restoreRefusedEditMock.mockClear();
    errorToastMock.mockClear();
    unqueueDispatch.mockReset();
    unqueueDispatch.mockReturnValue({ outcome: Promise.resolve({ status: "success" }) });
    addAttachmentMock.mockClear();
    removeAttachmentMock.mockClear();
  });

  it("renders into the bottom-bar stack rather than inside the composer", () => {
    recordSteerQueued("chat-1", { id: "steer-1", text: "use tabs instead", origin: "user" });
    expect(firstRow().closest("#steer-stack")).not.toBeNull();
    expect(firstRow().closest("#prompt-box")).toBeNull();
  });

  it("hides the stack entirely when there is nothing in it", () => {
    const stack = document.getElementById("steer-stack");
    expect(stack?.classList.contains("hidden")).toBe(true);
    recordSteerQueued("chat-1", { id: "steer-1", text: "one", origin: "user" });
    expect(stack?.classList.contains("hidden")).toBe(false);
  });

  it("stacks oldest first, so a new message lands at the bottom", () => {
    recordSteerQueued("chat-1", { id: "steer-1", text: "first", origin: "user" });
    recordSteerQueued("chat-1", { id: "steer-2", text: "second", origin: "user" });
    recordSteerQueued("chat-1", { id: "steer-3", text: "third", origin: "user" });
    expect(rows().map(textOf)).toEqual(["first", "second", "third"]);
  });

  it("says a message has been sent and is waiting", () => {
    recordSteerQueued("chat-1", { id: "steer-1", text: "use tabs instead", origin: "user" });

    const row = firstRow();
    expect(row.dataset["state"]).toBe("sent");
    expect(labelOf(row)).toBe("Sent");
    expect(row.querySelector(".steer-ack")).toBeNull();
    expect(row.getAttribute("aria-label")).toBe("Sent, waiting for the agent: use tabs instead");
  });

  it("says a message is still sending before the server confirms it", () => {
    recordSteerSent("chat-1", "m-1", "use tabs instead");

    const row = firstRow();
    expect(row.dataset["state"]).toBe("sending");
    expect(labelOf(row)).toBe("Sending");
    expect(row.getAttribute("aria-label")).toBe(
      "Sending, not in the agent's buffer yet: use tabs instead",
    );
  });

  // The server holds the row and no turn can read it (a bridge death, a turn end); it goes out as
  // the next prompt. Send-now has no turn to stop, so the row offers Edit and Delete.
  it("says an undelivered row sends next, and offers no send-now", () => {
    recordSteerQueued("chat-1", {
      id: "steer-1",
      text: "use tabs instead",
      origin: "user",
      state: "unsent",
    });

    const row = firstRow();
    expect(row.dataset["state"]).toBe("unsent");
    expect(labelOf(row)).toBe("Unread \u00b7 sends next");
    expect(row.getAttribute("aria-label")).toBe("Unread \u00b7 sends next: use tabs instead");
    expect(actions(row)).toEqual(["Edit this message", 'Delete "use tabs instead"']);
  });

  it("turns a sent row into an unsent one in place, keeping its element", () => {
    recordSteerQueued("chat-1", { id: "steer-1", text: "one", origin: "user" });
    const before = firstRow();

    recordSteerQueued("chat-1", { id: "steer-1", text: "one", origin: "user", state: "unsent" });

    expect(firstRow()).toBe(before);
    expect(labelOf(before)).toBe("Unread \u00b7 sends next");
  });

  it("takes a message the agent has read out of the stack entirely", () => {
    recordSteerQueued("chat-1", { id: "steer-1", text: "use tabs instead", origin: "user" });
    landSteerEntry("chat-1", "steer-1", "use tabs instead");

    expect(rows()).toHaveLength(0);
    expect(stackHidden()).toBe(true);
  });

  it("leaves a row whose own entry has not landed", () => {
    recordSteerQueued("chat-1", { id: "steer-1", text: "read one", origin: "user" });
    recordSteerQueued("chat-1", { id: "steer-2", text: "still waiting", origin: "user" });
    landSteerEntry("chat-1", "steer-1", "read one");

    expect(rows().map(textOf)).toEqual(["still waiting"]);
    expect(stackHidden()).toBe(false);
  });

  it("takes a dropped message out of the stack the same way a read one goes", () => {
    recordSteerQueued("chat-1", { id: "steer-1", text: "never read this", origin: "user" });
    landSteerEntry("chat-1", "steer-1", "never read this", "dropped");

    expect(rows()).toHaveLength(0);
    expect(stackHidden()).toBe(true);
  });

  it("leaves each row on its own entry, in either order", () => {
    recordSteerQueued("chat-1", { id: "steer-1", text: "first ask", origin: "user" });
    recordSteerQueued("chat-1", { id: "steer-2", text: "second ask", origin: "user" });
    landSteerEntry("chat-1", "steer-2", "second ask");
    expect(rows().map(textOf)).toEqual(["first ask"]);

    landSteerEntry("chat-1", "steer-1", "first ask");
    expect(rows()).toHaveLength(0);
  });

  // The server deletes by clearing KAS's buffer and resending the kept rows as one steer. The kept
  // rows must not be rebuilt by that, or they would fade out and back in.
  it("keeps the kept rows' elements when the deleted row leaves on its removed frame", () => {
    recordSteerQueued("chat-1", { id: "steer-1", text: "one", origin: "user" });
    recordSteerQueued("chat-1", { id: "steer-2", text: "two", origin: "user" });
    recordSteerQueued("chat-1", { id: "steer-3", text: "three", origin: "user" });
    const kept = [rowAt(0), rowAt(2)];

    recordSteerQueued("chat-1", { id: "steer-2", text: "two", origin: "user", state: "removed" });

    expectSameNodes(rows(), kept);
    expect(rows().map(textOf)).toEqual(["one", "three"]);
  });

  it("repaints nothing when the kept rows are resubmitted as one batch", () => {
    recordSteerQueued("chat-1", { id: "steer-1", text: "one", origin: "user" });
    recordSteerQueued("chat-1", { id: "steer-3", text: "three", origin: "user" });
    const before = rows();
    const deleteBtn = actionButton(rowAt(1), "Delete");
    deleteBtn.focus();

    recordSteerQueued("chat-1", {
      id: "steer-b1",
      text: "one\n\nthree",
      origin: "user",
      replaces: ["steer-1", "steer-3"],
    });

    expectSameNodes(rows(), before);
    expect(rows().map(textOf)).toEqual(["one", "three"]);
    expect(document.activeElement, "the controls were not rebuilt").toBe(deleteBtn);
  });

  it("takes every member of the batch out when the batch is read", () => {
    recordSteerQueued("chat-1", { id: "steer-1", text: "one", origin: "user" });
    recordSteerQueued("chat-1", { id: "steer-3", text: "three", origin: "user" });
    recordSteerQueued("chat-1", {
      id: "steer-b1",
      text: "one\n\nthree",
      origin: "user",
      replaces: ["steer-1", "steer-3"],
    });

    landSteerEntry("chat-1", "steer-b1", "one\n\nthree");

    expect(rows()).toHaveLength(0);
  });

  it("repaints when only the sending state changed", () => {
    recordSteerSent("chat-1", "m-1", "one");
    expect(labelOf(firstRow())).toBe("Sending");
    expect(actions(firstRow())).toEqual([]);

    recordSteerQueued("chat-1", { id: "steer-m-1", text: "one", origin: "user" });
    expect(labelOf(firstRow())).toBe("Sent");
    expect(actions(firstRow())).toEqual(PER_ROW("one"));
  });

  // A row is confirmed twice, by the POST reply and by the SSE frame, and the second can change
  // origin alone; both the computed key and the per-row key have to see it.
  it("adds the arrow when only the origin changed", () => {
    recordSteerQueued("chat-1", { id: "steer-m-1", text: "one", origin: "agent" });
    expect(actions(firstRow())).toEqual(["Edit this message", "Discard this message"]);

    recordSteerQueued("chat-1", { id: "steer-m-1", text: "one", origin: "user" });
    expect(actions(firstRow())).toEqual(PER_ROW("one"));
  });

  it("withdraws the arrow when only the origin changed", () => {
    recordSteerQueued("chat-1", { id: "steer-m-1", text: "one", origin: "user" });
    expect(actions(firstRow())).toContain("Send this message now");

    recordSteerQueued("chat-1", { id: "steer-m-1", text: "one", origin: "agent" });
    expect(actions(firstRow())).toEqual(["Edit this message", "Discard this message"]);
  });

  it("puts the whole message in the DOM and leaves the clamping to CSS", () => {
    const long =
      "stop rewriting the parser and instead widen the existing front-matter struct with the missing field";
    recordSteerQueued("chat-1", { id: "steer-1", text: long, origin: "user" });
    expect(textOf(firstRow())).toBe(long);
    expect(textOf(firstRow())).not.toContain("\u2026");
  });

  it("collapses whitespace so a multi-line message is one block", () => {
    recordSteerQueued("chat-1", {
      id: "steer-1",
      text: "first line\n\n   second line",
      origin: "user",
    });
    expect(textOf(firstRow())).toBe("first line second line");
    expect(firstRow().getAttribute("aria-label")).toBe(
      "Sent, waiting for the agent: first line second line",
    );
  });

  it("offers no controls on a message that is still sending", () => {
    recordSteerSent("chat-1", "m-1", "one");
    expect(actions(firstRow())).toEqual([]);
  });

  it("gives every row its own Edit and Delete, the delete named by its words", () => {
    recordSteerQueued("chat-1", { id: "steer-1", text: "one", origin: "user" });
    recordSteerQueued("chat-1", { id: "steer-2", text: "two", origin: "user" });
    expect(actions(rowAt(0))).toEqual(PER_ROW("one"));
    expect(actions(rowAt(1))).toEqual(PER_ROW("two"));
  });

  it("shortens a long message inside the delete's name", () => {
    const long = "rebase onto main and re-run the census against both bundles first";
    recordSteerQueued("chat-1", { id: "steer-1", text: long, origin: "user" });
    expect(actions(firstRow())[2]).toBe('Delete "rebase onto main and re-run the census …"');
  });

  it("gives a still-sending row no controls beside a confirmed one that has them", () => {
    recordSteerQueued("chat-1", { id: "steer-1", text: "confirmed", origin: "user" });
    recordSteerSent("chat-1", "m-2", "still sending");
    expect(actions(rowAt(0))).toEqual(PER_ROW("confirmed"));
    expect(actions(rowAt(1))).toEqual([]);
  });

  // The server cannot resend an agent row, so it refuses a one-row delete beside one; the dock
  // offers the only removal that exists then, the whole buffer.
  it("falls back to discard-all while a workflow result waits beside the rows", () => {
    recordSteerQueued("chat-1", { id: "steer-1", text: "mine", origin: "user" });
    recordSteerQueued("chat-1", { id: "notify-1", text: "workflow done", origin: "agent" });
    expect(actions(rowAt(0))).toEqual(["Send this message now", "Discard all 2 unread messages"]);
    expect(actions(rowAt(1))).toEqual(["Discard all 2 unread messages"]);

    landSteerEntry("chat-1", "notify-1", "workflow done");
    expect(actions(firstRow())).toEqual(PER_ROW("mine"));
  });

  it("stops the turn naming its row as the lead when send-now is pressed", async () => {
    recordSteerQueued("chat-1", { id: "steer-1", text: "actually target main", origin: "user" });
    clickAction(firstRow(), "Send this message now");

    await vi.waitFor(() => {
      expect(cancelDispatch).toHaveBeenCalledWith({ chatID: "chat-1", lead: "steer-1" });
    });
    expect(clearDispatch).not.toHaveBeenCalled();
    expect(removeDispatch).not.toHaveBeenCalled();
    expect(setComposerValueMock).not.toHaveBeenCalled();
  });

  it("names the pressed row when several are waiting", async () => {
    recordSteerQueued("chat-1", { id: "steer-1", text: "first", origin: "user" });
    recordSteerQueued("chat-1", { id: "steer-2", text: "second", origin: "user" });
    recordSteerQueued("chat-1", { id: "steer-3", text: "third", origin: "user" });

    clickAction(rowAt(1), "Send this message now");

    await vi.waitFor(() => {
      expect(cancelDispatch).toHaveBeenCalledWith({ chatID: "chat-1", lead: "steer-2" });
    });
  });

  it("offers no send-now control on the agent's own notice", () => {
    recordSteerQueued("chat-1", { id: "steer-1", text: "workflow says hi", origin: "agent" });
    expect(actions(firstRow())).not.toContain("Send this message now");
  });

  it("says the turn stops, rather than implying the agent will read it", () => {
    recordSteerQueued("chat-1", { id: "steer-1", text: "one", origin: "user" });
    expect(actionButton(firstRow(), "Send this message now").dataset["tooltip"]).toContain(
      "Stops the turn",
    );
  });

  it("takes the row back into the composer and deletes that row alone", async () => {
    recordSteerQueued("chat-1", { id: "steer-1", text: "keep me", origin: "user" });
    recordSteerQueued("chat-1", { id: "steer-2", text: "actually target main", origin: "user" });
    clickAction(rowAt(1), "Edit");

    await vi.waitFor(() => {
      expect(removeDispatch).toHaveBeenCalledWith({ chatID: "chat-1", steerID: "steer-2" });
    });
    expect(setComposerValueMock).toHaveBeenCalledWith("actually target main");
    expect(clearDispatch).not.toHaveBeenCalled();
    expect(confirmMock).not.toHaveBeenCalled();
  });

  // A lost reply must lose no text, so the box is filled before anything goes on the wire.
  it("fills the composer before the delete is dispatched", async () => {
    recordSteerQueued("chat-1", { id: "steer-1", text: "actually target main", origin: "user" });
    clickAction(firstRow(), "Edit");

    await vi.waitFor(() => {
      expect(removeDispatch).toHaveBeenCalled();
    });
    const filled = setComposerValueMock.mock.invocationCallOrder[0] ?? Infinity;
    const sent = removeDispatch.mock.invocationCallOrder[0] ?? -Infinity;
    expect(filled).toBeLessThan(sent);
  });

  it("hands the chat's replaced draft to the rollback when the server refuses the delete", async () => {
    removeDispatch.mockReturnValue({
      outcome: Promise.resolve({
        status: "error",
        error: { status: 409, message: "The agent already read that message" },
      }),
    });
    composerDraftMock.mockReturnValue("half a draft");
    recordSteerQueued("chat-1", { id: "steer-1", text: "actually target main", origin: "user" });
    clickAction(firstRow(), "Edit");

    await vi.waitFor(() => {
      expect(errorToastMock).toHaveBeenCalledWith("The agent already read that message");
    });
    expect(composerDraftMock).toHaveBeenCalledWith("chat-1");
    expect(restoreRefusedEditMock.mock.calls).toEqual([
      ["chat-1", "actually target main", "half a draft"],
    ]);
    expect(setComposerValueMock.mock.calls).toEqual([["actually target main"]]);
  });

  it("keeps the taken-back text when the delete's reply is lost", async () => {
    removeDispatch.mockReturnValue({
      outcome: Promise.resolve({ status: "error", error: { status: 0, message: "network" } }),
    });
    composerDraftMock.mockReturnValue("half a draft");
    recordSteerQueued("chat-1", { id: "steer-1", text: "actually target main", origin: "user" });
    clickAction(firstRow(), "Edit");

    await vi.waitFor(() => {
      expect(errorToastMock).toHaveBeenCalledWith("Could not confirm the message was taken back");
    });
    expect(restoreRefusedEditMock).not.toHaveBeenCalled();
    expect(setComposerValueMock.mock.calls).toEqual([["actually target main"]]);
  });

  it("takes an agent-adjacent row back through the whole-buffer clear", async () => {
    recordSteerQueued("chat-1", { id: "notify-1", text: "workflow done", origin: "agent" });
    clickAction(firstRow(), "Edit");

    await vi.waitFor(() => {
      expect(clearDispatch).toHaveBeenCalledWith({ chatID: "chat-1" });
    });
    expect(setComposerValueMock).toHaveBeenCalledWith("workflow done");
    expect(removeDispatch).not.toHaveBeenCalled();
  });

  it("deletes only the pressed row, without asking", async () => {
    recordSteerQueued("chat-1", { id: "steer-1", text: "one", origin: "user" });
    recordSteerQueued("chat-1", { id: "steer-2", text: "two", origin: "user" });
    clickAction(rowAt(1), 'Delete "two"');

    await vi.waitFor(() => {
      expect(removeDispatch).toHaveBeenCalledWith({ chatID: "chat-1", steerID: "steer-2" });
    });
    expect(removeDispatch).toHaveBeenCalledTimes(1);
    expect(confirmMock).not.toHaveBeenCalled();
    expect(clearDispatch).not.toHaveBeenCalled();
  });

  it("reports a refused delete in the server's words", async () => {
    removeDispatch.mockReturnValue({
      outcome: Promise.resolve({
        status: "error",
        error: { status: 409, message: "A turn is starting; try again in a moment" },
      }),
    });
    recordSteerQueued("chat-1", { id: "steer-1", text: "one", origin: "user" });
    clickAction(firstRow(), "Delete");

    await vi.waitFor(() => {
      expect(errorToastMock).toHaveBeenCalledWith("A turn is starting; try again in a moment");
    });
  });

  it("names the chat a refused delete was about as it was named when the delete began", async () => {
    removeDispatch.mockImplementation(() => {
      setSessions([{ ...makeSession("chat-1"), name: "renamed meanwhile" }]);
      return {
        outcome: Promise.resolve({ status: "error", error: { status: 404, message: "gone" } }),
      };
    });
    recordSteerQueued("chat-1", { id: "steer-1", text: "one", origin: "user" });
    clickAction(firstRow(), "Delete");

    await vi.waitFor(() => {
      expect(mocks.chatNoticeMock).toHaveBeenCalledWith("chat-1", "gone", "error", "test");
    });
  });

  it("discards a single agent row without asking", async () => {
    recordSteerQueued("chat-1", { id: "notify-1", text: "workflow done", origin: "agent" });
    clickAction(firstRow(), "Discard");
    await vi.waitFor(() => {
      expect(clearDispatch).toHaveBeenCalledWith({ chatID: "chat-1" });
    });
    expect(confirmMock).not.toHaveBeenCalled();
  });

  it("confirms before discarding when more than one would go", async () => {
    recordSteerQueued("chat-1", { id: "steer-1", text: "one", origin: "user" });
    recordSteerQueued("chat-1", { id: "notify-1", text: "workflow done", origin: "agent" });
    clickAction(firstRow(), "Discard");
    await vi.waitFor(() => {
      expect(confirmMock).toHaveBeenCalled();
    });
    expect(String(confirmMock.mock.calls[0]?.[0])).toContain("2");
  });

  it("sends nothing when the multi-message confirm is declined", async () => {
    confirmMock.mockResolvedValueOnce(false);
    recordSteerQueued("chat-1", { id: "steer-1", text: "one", origin: "user" });
    recordSteerQueued("chat-1", { id: "notify-1", text: "workflow done", origin: "agent" });
    clickAction(firstRow(), "Discard");
    await vi.waitFor(() => {
      expect(confirmMock).toHaveBeenCalled();
    });
    expect(clearDispatch).not.toHaveBeenCalled();
  });

  // FOLLOW-UPS. The rows the server holds for the end of the turn, drawn from the header's list
  // below the steers. Each is addressed by its own id, so its controls reach `unqueue_prompt` and
  // never the steer buffer.

  function withQueued(queued: QueuedPrompt[]): void {
    setSessions([{ ...makeSession("chat-1"), queued }]);
    setActive("chat-1");
  }

  it("draws a queued follow-up below the waiting steers", () => {
    withQueued([{ id: "m-q1", text: "then add tests" }]);
    recordSteerQueued("chat-1", { id: "steer-1", text: "use tabs", origin: "user" });

    expect(rows().map(textOf)).toEqual(["use tabs", "then add tests"]);
    expect(labelOf(rows()[1] as HTMLElement)).toBe("After this turn");
  });

  it("shows a labelled follow-up's label, keeping its text in the tooltip", () => {
    withQueued([{ id: "m-q1", text: "the whole findings", label: "Merging findings" }]);

    expect(textOf(firstRow())).toBe("Merging findings");
    expect(firstRow().dataset["tooltip"]).toBe("the whole findings");
  });

  it("repaints a follow-up whose label alone changed under the same id", () => {
    withQueued([{ id: "m-q1", text: "the whole findings", label: "Merging findings" }]);
    const before = firstRow();
    withQueued([{ id: "m-q1", text: "the whole findings", label: "Merged findings" }]);

    expect(firstRow()).toBe(before);
    expect(textOf(firstRow())).toBe("Merged findings");
    expect(firstRow().getAttribute("aria-label")).toBe("After this turn: Merged findings");
  });

  it("labels a carried row and a held row by what will happen to them", () => {
    withQueued([
      { id: "m-c", text: "unread one", resends: ["steer-1"] },
      { id: "m-h", text: "from before", held: true },
      { id: "m-ch", text: "both", resends: ["steer-2"], held: true },
    ]);

    expect(rows().map(labelOf)).toEqual([
      "Unread \u00b7 sends next",
      "Not sent \u00b7 the server restarted",
      "Not sent \u00b7 the server restarted",
    ]);
  });

  it("says what discarding loses: a user row was never sent, a carried row will not be sent again", () => {
    withQueued([
      { id: "m-u", text: "follow-up" },
      { id: "m-c", text: "unread one", resends: ["steer-1"] },
    ]);
    const hint = (row: HTMLElement): string =>
      row.querySelector(".steer-act-danger")?.getAttribute("data-tooltip") ?? "";

    expect(hint(rows()[0] as HTMLElement)).toContain("It has not been sent");
    expect(hint(rows()[1] as HTMLElement)).toContain("It will not be sent again");
  });

  it("updates a carried row's text in place when it absorbs a later carry", () => {
    withQueued([{ id: "m-c", text: "use tabs", resends: ["steer-1"] }]);
    const before = firstRow();
    withQueued([{ id: "m-c", text: "use tabs\n\nand rename it", resends: ["steer-1", "steer-2"] }]);

    expect(firstRow()).toBe(before);
    expect(textOf(firstRow())).toBe("use tabs and rename it");
  });

  it("discards one follow-up by its own id, leaving the steer buffer alone", async () => {
    withQueued([
      { id: "m-q1", text: "first" },
      { id: "m-q2", text: "second" },
    ]);
    clickAction(rows()[1] as HTMLElement, "Discard");

    await vi.waitFor(() => {
      expect(unqueueDispatch).toHaveBeenCalledWith({ chatID: "chat-1", messageID: "m-q2" });
    });
    expect(clearDispatch).not.toHaveBeenCalled();
    expect(confirmMock).not.toHaveBeenCalled();
  });

  it("fills the composer with the follow-up and its attachments before the removal is sent", () => {
    withQueued([
      { id: "m-q1", text: "look at this", attachments: [{ path: "src/a.ts", name: "a.ts" }] },
    ]);
    unqueueDispatch.mockImplementation(() => ({
      outcome: new Promise<{ status: string }>(() => undefined),
    }));
    clickAction(firstRow(), "Edit");

    expect(setComposerValueMock).toHaveBeenCalledWith("look at this");
    expect(addAttachmentMock).toHaveBeenCalledWith("chat-1", "src/a.ts");
    expect(unqueueDispatch).toHaveBeenCalledWith({ chatID: "chat-1", messageID: "m-q1" });
  });

  it("keeps the follow-up in the composer when the removal's reply is lost", async () => {
    withQueued([
      { id: "m-q1", text: "look at this", attachments: [{ path: "src/a.ts", name: "a.ts" }] },
    ]);
    unqueueDispatch.mockReturnValue({
      outcome: Promise.resolve({ status: "error", error: { message: "network" } }),
    });
    clickAction(firstRow(), "Edit");

    await vi.waitFor(() => {
      expect(errorToastMock).toHaveBeenCalledWith("Couldn't confirm the follow-up was taken back");
    });
    expect(setComposerValueMock).toHaveBeenCalledWith("look at this");
    expect(restoreRefusedEditMock).not.toHaveBeenCalled();
    expect(removeAttachmentMock).not.toHaveBeenCalled();
  });

  it.each([400, 404, 409, 502])(
    "puts the replaced draft back and drops the staged files on a %i refusal",
    async (status) => {
      withQueued([
        { id: "m-q1", text: "look at this", attachments: [{ path: "src/a.ts", name: "a.ts" }] },
      ]);
      composerDraftMock.mockReturnValue("half a sentence");
      unqueueDispatch.mockReturnValue({
        outcome: Promise.resolve({
          status: "error",
          error: { status, message: "already sending" },
        }),
      });
      clickAction(firstRow(), "Edit");

      await vi.waitFor(() => {
        expect(restoreRefusedEditMock).toHaveBeenCalledWith(
          "chat-1",
          "look at this",
          "half a sentence",
        );
      });
      expect(removeAttachmentMock).toHaveBeenCalledWith("chat-1", "src/a.ts");
      expect(errorToastMock).toHaveBeenCalledWith("already sending");
    },
  );

  it("leaves a file the composer already held when a refusal undoes the Edit", async () => {
    withQueued([
      { id: "m-q1", text: "look at this", attachments: [{ path: "src/a.ts", name: "a.ts" }] },
    ]);
    addAttachmentMock.mockReturnValueOnce(false);
    unqueueDispatch.mockReturnValue({
      outcome: Promise.resolve({ status: "error", error: { status: 409, message: "sending" } }),
    });
    clickAction(firstRow(), "Edit");

    await vi.waitFor(() => {
      expect(restoreRefusedEditMock).toHaveBeenCalled();
    });
    expect(removeAttachmentMock).not.toHaveBeenCalled();
  });
});

// THE ROW SURVIVES ITS OWN CONFIRMATION, measured against real layout under the shipped stylesheet,
// because the defect was invisible to every assertion about what a row SAYS.
describe("the row across its own confirmation", () => {
  let styleEl: HTMLStyleElement;
  let host: HTMLElement;

  beforeAll(() => {
    styleEl = mountAppCSS();
    host = document.createElement("div");
    host.style.inlineSize = "600px";
    document.body.appendChild(host);
    const stack = document.getElementById("steer-stack");
    if (stack === null) {
      throw new Error("no #steer-stack");
    }
    host.appendChild(stack);
  });

  beforeEach(() => {
    seq = 0;
    setSessions([makeSession("chat-1")]);
    setActive("chat-1");
  });

  afterAll(() => {
    styleEl.remove();
    // The tier cases below write this, and it changes `--hit-floor` and every control height for
    // the whole document, so it must not reach the clamp block.
    delete document.documentElement.dataset["pointer"];
    // Back to the body rather than removed with the host: the module renders into this element for
    // the rest of the file, and the clamp block below looks it up by id.
    const stack = document.getElementById("steer-stack");
    if (stack !== null) {
      document.body.appendChild(stack);
    }
    host.remove();
  });

  /** Two frames span one full layout-and-resize delivery. */
  async function settles(): Promise<void> {
    await new Promise<void>((resolve) => {
      requestAnimationFrame(() => {
        requestAnimationFrame(() => {
          resolve();
        });
      });
    });
  }

  /** The row's own ENTRY transitions, which are the two `@starting-style` declares. Filtered
   *  rather than the whole list, because a confirmation deliberately animates the row's ink — so
   *  "nothing is running" would forbid the settle as well as the re-entry, and `border-color`
   *  alone reports one per side. */
  function entryAnimations(row: HTMLElement): Animation[] {
    return row
      .getAnimations()
      .filter((a) =>
        ["opacity", "transform"].includes((a as CSSTransition).transitionProperty ?? ""),
      );
  }

  /** The entry transition `@starting-style` starts, once it is running. */
  async function entering(row: HTMLElement): Promise<Animation[]> {
    await vi.waitFor(() => {
      expect(entryAnimations(row).length, "the row fades in on its first paint").toBeGreaterThan(0);
    });
    return entryAnimations(row);
  }

  // The premise, asserted rather than assumed: without an entry transition on the row there is
  // nothing for a rebuild to replay, and the two cases below would pass over a stylesheet that had
  // lost it.
  it("enters through a starting style, so a replacement would re-animate", async () => {
    expect(ruleBody(loadCSS("26-dock.css"), ".steer-row")).toContain("@starting-style");

    recordSteerSent("chat-1", "m-1", "use tabs instead");
    const anims = await entering(firstRow());
    expect(anims.map((a) => (a as CSSTransition).transitionProperty).sort()).toEqual([
      "opacity",
      "transform",
    ]);
  });

  it("updates the same element rather than replacing it on the confirmation", () => {
    recordSteerSent("chat-1", "m-1", "use tabs instead");
    const sending = firstRow();
    expect(sending.dataset["state"]).toBe("sending");

    recordSteerQueued("chat-1", { id: "steer-m-1", text: "use tabs instead", origin: "user" });

    expect(firstRow(), "the node is updated in place, not rebuilt").toBe(sending);
    expect(sending.dataset["state"]).toBe("sent");
    expect(actions(sending)).toEqual(PER_ROW("use tabs instead"));
  });

  it("does not fade a second time when the confirmation lands", async () => {
    recordSteerSent("chat-1", "m-1", "use tabs instead");
    const row = firstRow();
    await Promise.all((await entering(row)).map((a) => a.finished));

    recordSteerQueued("chat-1", { id: "steer-m-1", text: "use tabs instead", origin: "user" });

    expect(entryAnimations(row), "no second fade over a row already on screen").toEqual([]);
    expect(getComputedStyle(row).opacity, "and it stays fully painted").toBe("1");
  });

  it("leaves an established row alone when a second message arrives", async () => {
    recordSteerQueued("chat-1", { id: "steer-1", text: "first", origin: "user" });
    const first = firstRow();
    await Promise.all((await entering(first)).map((a) => a.finished));

    recordSteerSent("chat-1", "m-2", "second");

    expect(rows()).toHaveLength(2);
    expect(rows()[0], "the established row is the same node").toBe(first);
    expect(entryAnimations(first), "and it does not re-enter").toEqual([]);
  });

  // THE ROW MUST NOT CHANGE HEIGHT WHEN IT CONFIRMS, and this is measured at both pointer tiers
  // because the size of the jump was the hit-target floor: the controls arrive with the
  // confirmation and each is floored to `--hit-floor`, so
  for (const tier of ["fine", "coarse"] as const) {
    it(`keeps its height across the confirmation on a ${tier} pointer`, async () => {
      document.documentElement.dataset["pointer"] = tier;
      recordSteerSent("chat-1", "m-1", "use tabs instead");
      const row = firstRow();
      await settles();
      const before = row.getBoundingClientRect().height;
      // The floor is what makes the two tiers different measurements rather than one measurement
      // run twice, so the premise is asserted.
      expect(
        getComputedStyle(document.documentElement).getPropertyValue("--hit-floor").trim(),
        "the tier is in force",
      ).toBe(tier === "fine" ? "1.5rem" : "2.75rem");

      recordSteerQueued("chat-1", { id: "steer-m-1", text: "use tabs instead", origin: "user" });
      await settles();

      expect(actions(row), "the controls did arrive").toHaveLength(3);
      expect(
        row.getBoundingClientRect().height,
        "and the row did not grow under the reader",
      ).toBeCloseTo(before, 2);
    });
  }
});

// The clamp is CSS-only and has no opener, so what real layout answers here is whether the row is
// clipped to four lines with no control to open it. The stack element is the module's own (captured
// at init), so these cases re-parent it into a narrow host rather than building a second one.
describe("the row's clamp", () => {
  let styleEl: HTMLStyleElement;
  let host: HTMLElement;

  beforeAll(() => {
    styleEl = mountAppCSS();
    host = document.createElement("div");
    host.style.inlineSize = "320px";
    document.body.appendChild(host);
    const stack = document.getElementById("steer-stack");
    if (stack === null) {
      throw new Error("no #steer-stack");
    }
    host.appendChild(stack);
  });

  beforeEach(() => {
    // Same one-line reset as the suite above: a fresh session has no steers, so the render empties
    // the stack. Repeated rather than hoisted because these cases are a sibling describe with their
    // own layout host.
    seq = 0;
    setSessions([makeSession("chat-1")]);
    setActive("chat-1");
  });

  afterAll(() => {
    styleEl.remove();
    host.remove();
  });

  function textEl(row: HTMLElement): HTMLElement {
    const t = row.querySelector<HTMLElement>(".steer-text");
    if (t === null) {
      throw new Error("no .steer-text");
    }
    return t;
  }

  const LONG = "rebase onto main and re-run the census against both bundles first ".repeat(6);

  it("clamps a long message to four lines with the whole text still in the DOM", () => {
    recordSteerQueued("chat-1", { id: "steer-1", text: LONG, origin: "user" });
    const row = firstRow();
    expect(textEl(row).textContent).toContain("bundles first");
    expect(textEl(row).scrollHeight).toBeGreaterThan(textEl(row).clientHeight);
    // The count is a stylesheet fact now, with no TypeScript constant to pair it against, so this
    // is where four is pinned.
    expect(
      getComputedStyle(textEl(row)).getPropertyValue("-webkit-line-clamp").trim(),
      "clipped to four lines",
    ).toBe("4");
  });

  // Fails if the opener comes back under any class name: a reader who wants the whole message
  // clicks Edit.
  it("offers no control to expand a clamped message", () => {
    recordSteerQueued("chat-1", { id: "steer-1", text: LONG, origin: "user" });
    const row = firstRow();
    expect(row.querySelector(".steer-more")).toBeNull();
    for (const btn of row.querySelectorAll("button")) {
      expect(btn.classList.contains("steer-act"), "every control is an action").toBe(true);
    }
  });

  // The two channels differ by one trim: `data-tooltip` carries the raw text and the accessible
  // name runs it through `oneLine`.
  it("keeps the whole message reachable in the tooltip and the accessible name", () => {
    recordSteerQueued("chat-1", { id: "steer-1", text: LONG, origin: "user" });
    const row = firstRow();
    expect(row.dataset["tooltip"]).toBe(LONG);
    expect(row.getAttribute("aria-label")).toBe(`Sent, waiting for the agent: ${LONG.trim()}`);
  });

  // The row is a grid whose middle track is `minmax(0, 1fr)`, and that is what keeps the actions on
  // the row: an `auto` middle track sizes to the text and pushes them off.
  it("keeps every control on the row at the clamped height", () => {
    recordSteerQueued("chat-1", { id: "steer-1", text: LONG, origin: "user" });
    const row = firstRow();
    expect(actions(row).length, "one unread message, so Edit is offered too").toBe(3);

    const textBox = textEl(row).getBoundingClientRect();
    for (const btn of row.querySelectorAll<HTMLElement>(".steer-act")) {
      const box = btn.getBoundingClientRect();
      expect(box.x, "still in the actions column, right of the message").toBeGreaterThan(textBox.x);
      expect(box.right, "and still inside the row").toBeLessThanOrEqual(
        row.getBoundingClientRect().right + 1,
      );
    }
  });
});

// The run tab mounts the same stack over a step's rows, with the chat's Edit and Delete and no
// Send now: nothing stops a step's turn for a reader.
describe("a stack over a run step's rows", () => {
  it("offers Edit and Delete and no Send now, and hands each to its source", () => {
    const steers = signal<PendingSteer[]>([
      { id: "steer-s1", text: "check the tests", origin: "user" },
    ]);
    const edit = vi.fn((_steer: PendingSteer) => Promise.resolve());
    const remove = vi.fn((_id: string) => Promise.resolve());
    const src: SteerStackSource = {
      read: () => ({ id: "wf_1|root/review", steers: steers.value, queued: [] }),
      edit,
      editByClear: () => Promise.resolve(),
      remove,
      discard: () => Promise.resolve(),
      waitingFor: "the step",
    };
    const stack = document.createElement("ul");
    stack.className = "steer-stack hidden";
    document.body.appendChild(stack);
    mountSteerStack(stack, src);
    const row = stack.querySelector<HTMLElement>(".steer-row")!;
    expect(actions(row)).toEqual(["Edit this message", 'Delete "check the tests"']);
    clickAction(row, "Edit");
    expect(edit).toHaveBeenCalledWith({ id: "steer-s1", text: "check the tests", origin: "user" });
    clickAction(row, "Delete");
    expect(remove).toHaveBeenCalledWith("steer-s1");
    expect(cancelDispatch).not.toHaveBeenCalled();
    steers.value = [];
    expect(stack.classList.contains("hidden")).toBe(true);
    stack.remove();
  });
});
