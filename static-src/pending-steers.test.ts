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
      outcome: Promise.resolve<{ status: string; value?: boolean }>({ status: "success" }),
    })),
    removeDispatch: vi.fn((_args: { chatID: string; steerID: string }) => ({
      outcome: Promise.resolve<RemoveOutcome>({ status: "success" }),
    })),
    confirmMock: vi.fn((_q: Confirmation) => Promise.resolve(true)),
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
    sendQueuedNowDispatch: vi.fn((_args: { chatID: string; messageID: string }) => ({
      outcome: Promise.resolve<RemoveOutcome>({ status: "success" }),
    })),
    announceMock: vi.fn((_message: string) => undefined),
    queueDispatch: vi.fn((_args: { chatID: string; text: string; messageID: string }) => ({
      outcome: Promise.resolve<QueueOutcome>({ status: "success" }),
    })),
    submitPromptMock: vi.fn((_chatID: string, _text: string, _opts?: unknown) =>
      Promise.resolve("sent"),
    ),
    restoreFailedSendMock: vi.fn((_chatID: string, _text: string) => undefined),
    cancelCompactDispatch: vi.fn((_args: { chatID: string }) => ({
      outcome: Promise.resolve<RemoveOutcome>({ status: "success" }),
    })),
  };
});

type QueueOutcome =
  | { status: "success" }
  | { status: "error"; error: { status?: number; code?: string; message: string } };

type RemoveOutcome =
  | { status: "success"; value?: boolean }
  | { status: "error"; error: { status?: number; message: string } };

vi.mock("@cplieger/ui/announce", () => ({ announce: mocks.announceMock }));
vi.mock("./actions/chat.js", () => ({
  clearSteers: { dispatch: mocks.clearDispatch },
  cancelTurn: { dispatch: mocks.cancelDispatch },
  removeSteer: { dispatch: mocks.removeDispatch },
  unqueuePrompt: { dispatch: mocks.unqueueDispatch },
  sendQueuedNow: { dispatch: mocks.sendQueuedNowDispatch },
  cancelCompact: { dispatch: mocks.cancelCompactDispatch },
  queuePrompt: { dispatch: mocks.queueDispatch },
}));
vi.mock("./submit.js", () => ({
  submitPrompt: mocks.submitPromptMock,
  submitLabelled: vi.fn(),
  forgetDeletedChat: vi.fn(),
  inFlightMessageIDs: vi.fn(() => []),
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
  restoreFailedSend: mocks.restoreFailedSendMock,
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
  sendQueuedNowDispatch,
  queueDispatch,
  submitPromptMock,
  restoreFailedSendMock,
  cancelCompactDispatch,
} = mocks;

import { setSessions, setActive, recordSteerQueued, openTurn, appendEntry, get } from "./store.js";
import { initPendingSteers, mountSteerStack, type SteerStackSource } from "./pending-steers.js";
import { signal } from "@cplieger/ui";
import type { Entry, PendingSteer, QueuedPrompt, RunEntryView, RunView, Session } from "./types.js";
import {
  compactionEntry,
  resumeEntry,
  rowEntry,
  runAt,
  turnEntry,
} from "./__test-helpers__/run-order.js";
import type { Confirmation } from "./confirm.js";
import { loadCSS, mountAppCSS, ruleBody } from "./__test-helpers__/css-rules.js";
import { confirmText } from "./__test-helpers__/confirm-text.js";
import { QUOTE_ATTRIBUTION, appendQuote, quoteSelection } from "./message-quote.js";
import { pendingSteerRow, upsertSteerRow } from "./steer-rows.js";

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

/** A row still sending, as the run step's dock holds one (`run-step-steers.ts`): the stack draws a
 *  chat's and a step's rows through one render. */
function recordSending(chatID: string, messageID: string, text: string): void {
  const s = get(chatID);
  if (s !== undefined) {
    const steers = upsertSteerRow(s.steers ?? [], pendingSteerRow(`steer-${messageID}`, text));
    setSessions([{ ...s, steers: [...steers] }]);
  }
}

/** The version of the next run order a case states, so the store takes each as newer. */
let runVersion = 0;

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
  "Queue this message instead",
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
    sendQueuedNowDispatch.mockReset();
    sendQueuedNowDispatch.mockReturnValue({ outcome: Promise.resolve({ status: "success" }) });
    queueDispatch.mockReset();
    queueDispatch.mockReturnValue({ outcome: Promise.resolve({ status: "success" }) });
    submitPromptMock.mockClear();
    restoreFailedSendMock.mockClear();
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

  // A row another program posted through the API is not the user's message, so it says who sent it.
  it("says who sent a row an API caller posted", () => {
    recordSteerQueued("chat-1", {
      id: "steer-1",
      text: "Agent Mail: 1 new message",
      origin: "user",
      caller: { label: "Agent Mail" },
    });
    recordSteerQueued("chat-1", {
      id: "steer-2",
      text: "nightly report",
      origin: "user",
      caller: {},
      state: "unsent",
    });

    expect(labelOf(rowAt(0))).toBe("Sent by Agent Mail");
    expect(rowAt(0).getAttribute("aria-label")).toBe(
      "Sent by Agent Mail, waiting for the agent: Agent Mail: 1 new message",
    );
    expect(labelOf(rowAt(1))).toBe("Sent by an API caller \u00b7 unread \u00b7 sends next");
  });

  it("says a message is still sending before the server confirms it", () => {
    recordSending("chat-1", "m-1", "use tabs instead");

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
    recordSending("chat-1", "m-1", "one");
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

  // The turn ended while KAS held the row: KAS's continuation reads it or its boundary drops it,
  // and a delete would empty the buffer under that read, so the row is drawn like a sending one,
  // with no controls.
  it("offers no controls on a message the agent is reading at a turn's end", () => {
    recordSteerQueued("chat-1", { id: "steer-1", text: "one", origin: "user" });
    recordSteerQueued("chat-1", {
      id: "steer-1",
      text: "one",
      origin: "user",
      state: "delivering",
    });

    const row = firstRow();
    expect(row.dataset["state"]).toBe("delivering");
    expect(labelOf(row)).toBe("Delivering");
    expect(row.getAttribute("aria-label")).toBe(
      "Delivering, the agent is reading or dropping it: one",
    );
    expect(actions(row)).toEqual([]);
  });

  // Discard all and Edit beside a workflow result are one clear of the whole buffer, which the
  // server refuses while the agent reads a row: offering either would only produce a refusal.
  it("offers no whole-buffer clear beside a message the agent is reading", () => {
    recordSteerQueued("chat-1", {
      id: "steer-1",
      text: "mine",
      origin: "user",
      state: "delivering",
    });
    recordSteerQueued("chat-1", { id: "steer-2", text: "later", origin: "user" });
    recordSteerQueued("chat-1", { id: "notify-1", text: "workflow done", origin: "agent" });
    expect(actions(rowAt(0))).toEqual([]);
    expect(actions(rowAt(1))).toEqual(["Send this message now"]);
    expect(actions(rowAt(2))).toEqual([]);

    recordSteerQueued("chat-1", { id: "steer-1", text: "mine", origin: "user" });
    expect(actions(rowAt(2))).toEqual(["Discard all 3 unread messages"]);
  });

  // While the agent reads one row, any delete of a row in its buffer would clear that read, so the
  // server refuses it. Only a row the server holds itself (unsent) can still be edited or deleted.
  it("offers no Edit or Delete beside a message the agent is reading, except on an unsent one", () => {
    recordSteerQueued("chat-1", {
      id: "steer-1",
      text: "mine",
      origin: "user",
      state: "delivering",
    });
    recordSteerQueued("chat-1", { id: "steer-2", text: "later", origin: "user" });
    recordSteerQueued("chat-1", { id: "steer-3", text: "held", origin: "user", state: "unsent" });
    expect(actions(rowAt(0))).toEqual([]);
    expect(actions(rowAt(1))).toEqual(["Send this message now"]);
    expect(actions(rowAt(2))).toEqual(["Edit this message", 'Delete "held"']);

    recordSteerQueued("chat-1", { id: "steer-1", text: "mine", origin: "user" });
    expect(actions(rowAt(1))).toEqual(PER_ROW("later"));
  });

  it("offers no controls on a message that is still sending", () => {
    recordSending("chat-1", "m-1", "one");
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
    expect(actions(firstRow()).at(-1)).toBe('Delete "rebase onto main and re-run the census …"');
  });

  it("cuts the delete's name before an emoji the cut would split", () => {
    const text = "a".repeat(38) + "\u{1F600} and the rest";
    recordSteerQueued("chat-1", { id: "steer-1", text, origin: "user" });
    const name = actions(firstRow()).find((a) => a.startsWith("Delete"));
    expect(name).toBe(`Delete "${"a".repeat(38)}\u2026"`);
    expect(name).not.toMatch(/[\uD800-\uDBFF](?![\uDC00-\uDFFF])/);
  });

  it("gives a still-sending row no controls beside a confirmed one that has them", () => {
    recordSteerQueued("chat-1", { id: "steer-1", text: "confirmed", origin: "user" });
    recordSending("chat-1", "m-2", "still sending");
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

  // The server stopped nothing (the resend already carries the row), so "Stopping the turn" is false.
  it("takes back the stop announcement when the server skipped the stop", async () => {
    cancelDispatch.mockReturnValueOnce({
      outcome: Promise.resolve({ status: "success", value: true }),
    });
    recordSteerQueued("chat-1", { id: "steer-1", text: "actually target main", origin: "user" });
    clickAction(firstRow(), "Send this message now");

    await vi.waitFor(() => {
      expect(mocks.announceMock).toHaveBeenLastCalledWith(
        "The turn keeps running: the message already left the queue",
      );
    });
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

    // A plain delete, which leaves no transcript entry: the edited text is the next steer.
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

  it("queues instead: deletes the steer, then queues the same words under a new id", async () => {
    recordSteerQueued("chat-1", { id: "steer-1", text: "keep me", origin: "user" });
    recordSteerQueued("chat-1", { id: "steer-2", text: "run the census after", origin: "user" });
    clickAction(rowAt(1), "Queue this message instead");

    await vi.waitFor(() => {
      expect(queueDispatch).toHaveBeenCalledTimes(1);
    });
    expect(removeDispatch.mock.calls).toEqual([[{ chatID: "chat-1", steerID: "steer-2" }]]);
    const [queued] = queueDispatch.mock.calls[0] ?? [];
    expect(queued?.chatID).toBe("chat-1");
    expect(queued?.text).toBe("run the census after");
    expect(queued?.messageID).toMatch(/^m-/);
    const removed = removeDispatch.mock.invocationCallOrder[0] ?? Infinity;
    const queuedAt = queueDispatch.mock.invocationCallOrder[0] ?? -Infinity;
    expect(removed).toBeLessThan(queuedAt);
    expect(restoreFailedSendMock).not.toHaveBeenCalled();
    expect(submitPromptMock).not.toHaveBeenCalled();
    expect(errorToastMock).not.toHaveBeenCalled();
  });

  it("queues nothing when the server refuses the delete, and says why", async () => {
    removeDispatch.mockReturnValue({
      outcome: Promise.resolve({
        status: "error",
        error: { status: 409, message: "The agent already read that message" },
      }),
    });
    recordSteerQueued("chat-1", { id: "steer-1", text: "run it after", origin: "user" });
    clickAction(firstRow(), "Queue this message instead");

    await vi.waitFor(() => {
      expect(errorToastMock).toHaveBeenCalledWith("The agent already read that message");
    });
    expect(queueDispatch).not.toHaveBeenCalled();
    expect(restoreFailedSendMock).not.toHaveBeenCalled();
  });

  it("puts the words in the message box when the delete's reply is lost", async () => {
    removeDispatch.mockReturnValue({
      outcome: Promise.resolve({ status: "error", error: { status: 0, message: "network" } }),
    });
    recordSteerQueued("chat-1", { id: "steer-1", text: "run it after", origin: "user" });
    clickAction(firstRow(), "Queue this message instead");

    await vi.waitFor(() => {
      expect(restoreFailedSendMock).toHaveBeenCalledWith("chat-1", "run it after");
    });
    expect(queueDispatch).not.toHaveBeenCalled();
    expect(errorToastMock).toHaveBeenCalledWith("Could not confirm the message was moved");
  });

  it("sends the words as a prompt when the turn ended between the delete and the queue", async () => {
    queueDispatch.mockReturnValue({
      outcome: Promise.resolve({
        status: "error",
        error: { status: 409, code: "no_turn", message: "No turn is running" },
      }),
    });
    recordSteerQueued("chat-1", { id: "steer-1", text: "run it after", origin: "user" });
    clickAction(firstRow(), "Queue this message instead");

    await vi.waitFor(() => {
      expect(submitPromptMock).toHaveBeenCalledWith("chat-1", "run it after", { attachments: [] });
    });
    expect(restoreFailedSendMock).not.toHaveBeenCalled();
    expect(errorToastMock).not.toHaveBeenCalled();
  });

  it("puts the words in the message box when the queue refuses them after the delete", async () => {
    queueDispatch.mockReturnValue({
      outcome: Promise.resolve({
        status: "error",
        error: { status: 409, code: "full", message: "The follow-up queue is full" },
      }),
    });
    recordSteerQueued("chat-1", { id: "steer-1", text: "run it after", origin: "user" });
    clickAction(firstRow(), "Queue this message instead");

    await vi.waitFor(() => {
      expect(errorToastMock).toHaveBeenCalledWith("The follow-up queue is full");
    });
    expect(restoreFailedSendMock.mock.calls).toEqual([["chat-1", "run it after"]]);
    expect(submitPromptMock).not.toHaveBeenCalled();
  });

  it("puts the words in the message box when the queue's reply is lost", async () => {
    queueDispatch.mockReturnValue({
      outcome: Promise.resolve({ status: "error", error: { status: 0, message: "network" } }),
    });
    recordSteerQueued("chat-1", { id: "steer-1", text: "run it after", origin: "user" });
    clickAction(firstRow(), "Queue this message instead");

    await vi.waitFor(() => {
      expect(restoreFailedSendMock).toHaveBeenCalledWith("chat-1", "run it after");
    });
  });

  it("leaves the box alone when the queue's reply is lost but its row landed", async () => {
    queueDispatch.mockImplementation(({ chatID, text, messageID }) => {
      const s = get(chatID);
      if (s !== undefined) {
        setSessions([
          {
            ...s,
            run: runAt(1, [
              rowEntry(messageID, { after: "next" }, { row: { id: messageID, text } }),
            ]),
          },
        ]);
      }
      return {
        outcome: Promise.resolve({ status: "error", error: { status: 0, message: "network" } }),
      };
    });
    recordSteerQueued("chat-1", { id: "steer-1", text: "run it after", origin: "user" });
    clickAction(firstRow(), "Queue this message instead");

    await vi.waitFor(() => {
      expect(queueDispatch).toHaveBeenCalled();
    });
    await Promise.resolve();
    await Promise.resolve();
    expect(restoreFailedSendMock).not.toHaveBeenCalled();
    expect(errorToastMock).not.toHaveBeenCalled();
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
    expect(confirmText(confirmMock.mock.calls[0]?.[0])).toContain("2");
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

  // THE RUN ORDER. The dock draws the server's list (`ChatHeader.run`) in order, each entry with the
  // server's wait; it infers neither the order nor a wait. A running turn is the transcript's, so it is
  // not drawn.

  const HEAD = turnEntry("t1", { origin: "prompt", kind: "row" });

  /** The chat's run order: `entries` behind a running turn when `turnRuns`, as the server lists them. */
  function withRun(entries: RunEntryView[], over: Partial<RunView> = {}, turnRuns = true): void {
    const head = turnRuns ? [HEAD] : [];
    setSessions([
      {
        ...makeSession("chat-1"),
        run: runAt(++runVersion, [...head, ...entries], {
          ...(turnRuns ? { cancel: "t1", send: "steer" as const } : {}),
          ...over,
        }),
      },
    ]);
    setActive("chat-1");
  }

  /** Rows behind a running turn as the server waits them: one turn end per row ahead, the head's
   *  included, each with the arrow that stops the turn. */
  function behindTurn(prompts: readonly QueuedPrompt[]): RunEntryView[] {
    return prompts.map((row, i) =>
      rowEntry(
        row.id,
        i === 0 ? { after: "turns", turns: 1, this: true } : { after: "turns", turns: i + 1 },
        {
          row,
          send_now: "stop",
        },
      ),
    );
  }

  const three: QueuedPrompt[] = [
    { id: "m-q1", text: "first" },
    { id: "m-q2", text: "second" },
    { id: "m-q3", text: "third" },
  ];

  it("draws each follow-up with the wait the server gives it, in its order", () => {
    withRun(behindTurn(three));

    expect(rows().map(textOf)).toEqual(["first", "second", "third"]);
    expect(rows().map(labelOf)).toEqual(["After this turn", "After 2 turns", "After 3 turns"]);
    expect(rowAt(2).getAttribute("aria-label")).toBe("After 3 turns: third");
  });

  it("draws no row for a running turn, kiro-cli's run or a retry: their messages are the transcript's", () => {
    withRun([
      rowEntry("retry-1", { after: "turns", turns: 1, this: true }, { kind: "retry" }),
      ...behindTurn([{ id: "m-q1", text: "first" }]),
    ]);

    expect(rows().map(textOf)).toEqual(["first"]);
  });

  // A follow-up another program queued through the API is not the user's, so it says who sent it.
  it("says who sent a follow-up an API caller queued", () => {
    withRun(behindTurn([{ id: "m-q1", text: "from grelot", caller: { label: "grelot" } }]));

    expect(labelOf(firstRow())).toBe("Sent by grelot \u00b7 after this turn");
  });

  it("draws a quoted follow-up's passage as a sent prompt's band, and names it without the marks", () => {
    withRun(
      behindTurn([
        {
          id: "m-q",
          text: `${appendQuote("", quoteSelection("quoted", "From Kiro's reply"))}reply`,
        },
      ]),
    );
    const row = firstRow();
    expect(row.querySelector(".steer-text > blockquote.turn-req-quote")?.textContent).toBe(
      "From Kiro's reply: quoted",
    );
    expect(textOf(row)).toBe("From Kiro's reply: quotedreply");
    expect(row.getAttribute("aria-label")).toBe("After this turn: From Kiro's reply: quoted reply");
    expect(row.dataset["tooltip"]).toBe("From Kiro's reply: quoted\n\nreply");
  });

  it("draws every quote in a follow-up as a band, and names each without the marks", () => {
    const quote = (words: string): string => quoteSelection(words, "From Kiro's reply");
    withRun(
      behindTurn([
        {
          id: "m-q2",
          text: `${appendQuote(`${appendQuote("", quote("one"))}reply`, quote("two"))}more`,
        },
      ]),
    );
    const row = firstRow();
    const bands = [...row.querySelectorAll(".steer-text > blockquote.turn-req-quote")];
    expect(bands.map((b) => b.textContent)).toEqual([
      "From Kiro's reply: one",
      "From Kiro's reply: two",
    ]);
    expect(textOf(row)).toBe("From Kiro's reply: onereplyFrom Kiro's reply: twomore");
    expect(row.getAttribute("aria-label")).toBe(
      "After this turn: From Kiro's reply: one reply From Kiro's reply: two more",
    );
    expect(row.dataset["tooltip"]).toBe(
      "From Kiro's reply: one\n\nreply\n\nFrom Kiro's reply: two\n\nmore",
    );
  });

  // CASE A: a /compact on an idle chat runs at once; the dock shows it running with no control.
  it("draws a running compaction as the running row, with no control", () => {
    withRun([compactionEntry("compact-1", { after: "running" })], { send: "queue" }, false);

    expect(rows()).toHaveLength(1);
    expect(firstRow().dataset["state"]).toBe("compacting");
    expect(textOf(firstRow())).toBe("Compacting the context\u2026");
    expect(labelOf(firstRow())).toBe("Running");
    expect(firstRow().querySelector(".steer-state-icon .steer-spinner")).not.toBeNull();
    expect(actions(firstRow())).toEqual([]);
  });

  // CASE B: a message sent while a compaction runs waits under it and goes at its end.
  it("draws a message sent during a running compaction under it, waiting for its end", () => {
    withRun(
      [
        compactionEntry("compact-1", { after: "running" }),
        rowEntry("m-q1", { after: "compaction" }, { row: { id: "m-q1", text: "first" } }),
        rowEntry(
          "m-q2",
          { after: "turns", turns: 1 },
          { row: { id: "m-q2", text: "second" }, send_now: "after_compaction" },
        ),
      ],
      { send: "queue", next_wait: { after: "turns", turns: 2 } },
      false,
    );

    expect(rows().map(textOf)).toEqual(["Compacting the context\u2026", "first", "second"]);
    expect(rows().map(labelOf)).toEqual(["Running", "After the compaction", "After the next turn"]);
    expect(actions(rowAt(1))).toEqual(["Edit this follow-up", "Discard this follow-up"]);
    expect(actionButton(rowAt(2), "Send this follow-up first").dataset["tooltip"]).toBe(
      "Send this follow-up first once the compaction ends",
    );
  });

  // CASE C: a /compact during a turn runs at its end, ahead of the rows queued behind it.
  it("draws a compaction waiting for this turn above the rows that wait for it, with its Cancel", async () => {
    withRun([
      compactionEntry("compact-1", { after: "turns", turns: 1, this: true }),
      rowEntry(
        "m-q1",
        { after: "compaction" },
        { row: { id: "m-q1", text: "first" }, send_now: "stop_then_compaction" },
      ),
    ]);

    expect(rows().map(textOf)).toEqual(["Compact the context", "first"]);
    expect(rows().map(labelOf)).toEqual(["After this turn", "After the compaction"]);
    expect(actions(firstRow())).toEqual(["Cancel the queued compaction"]);

    clickAction(firstRow(), "Cancel the queued compaction");
    await vi.waitFor(() => {
      expect(cancelCompactDispatch).toHaveBeenCalledExactlyOnceWith({
        chatID: "chat-1",
        id: "compact-1",
      });
    });
  });

  it("turns the waiting compaction into the running one in place, with no control", () => {
    withRun([compactionEntry("compact-1", { after: "turns", turns: 1, this: true })]);
    const waiting = firstRow();
    expect(waiting.dataset["state"]).toBe("queued");

    withRun([compactionEntry("compact-1", { after: "running" })], { send: "queue" }, false);

    expectSameNodes(rows(), [waiting]);
    expect(waiting.dataset["state"]).toBe("compacting");
    expect(waiting.getAttribute("aria-label")).toBe("Running: Compacting the context\u2026");
    expect(actions(waiting)).toEqual([]);
  });

  // CASE E: the rows a stopped turn left wait for the reader's next turn, not the compaction's end.
  it("draws rows a stopped turn paused as waiting for the next turn behind a running compaction", () => {
    withRun(
      [
        compactionEntry("compact-1", { after: "running" }),
        rowEntry(
          "m-q1",
          { after: "turns", turns: 1 },
          { state: "paused", row: { id: "m-q1", text: "first" }, send_now: "after_compaction" },
        ),
      ],
      { send: "queue" },
      false,
    );

    expect(rows().map(labelOf)).toEqual(["Running", "After the next turn"]);
  });

  it("draws a taken row whose turn has not opened as starting", () => {
    withRun(
      [
        rowEntry(
          "m-q1",
          { after: "starting" },
          { state: "starting", row: { id: "m-q1", text: "first" } },
        ),
      ],
      { send: "queue" },
      false,
    );

    expect(rows().map(labelOf)).toEqual(["Starting"]);
    expect(actions(firstRow())).toEqual(["Edit this follow-up", "Discard this follow-up"]);
  });

  it("gives a held row its words and no arrow", () => {
    withRun([
      rowEntry(
        "m-h",
        { after: "held" },
        { state: "held", row: { id: "m-h", text: "from before" } },
      ),
      ...behindTurn([{ id: "m-q1", text: "first" }]),
    ]);

    expect(rows().map(labelOf)).toEqual([
      "Not sent \u00b7 the server restarted",
      "After this turn",
    ]);
    expect(firstRow().dataset["held"]).toBe("");
    expect(actions(firstRow())).toEqual(["Edit this follow-up", "Discard this follow-up"]);
  });

  // CASE I: a steer the agent had not read when the turn stopped waits under the running compaction.
  it("draws an unread steer under the running compaction it waits for", () => {
    withRun(
      [
        compactionEntry("compact-1", { after: "running" }),
        rowEntry("m-u", { after: "compaction" }, { kind: "unread", origin: "unread" }),
      ],
      { send: "queue", unread_wait: { after: "compaction" } },
      false,
    );
    recordSteerQueued("chat-1", { id: "steer-1", text: "a", origin: "user", state: "unsent" });

    expect(rows().map(textOf)).toEqual(["Compacting the context\u2026", "a"]);
    expect(rows().map(labelOf)).toEqual(["Running", "Unread \u00b7 after the compaction"]);
    expect(actions(rowAt(1))).not.toContain("Send this message now");
  });

  // THE COMPACTION FAILSAFE. At 95% context use marotte stops the turn, compacts, then sends its own resume ahead of
  // the rows the stop paused.
  const RESUME =
    "The context window was nearly full, so marotte stopped your task mid-turn. Continue the task.";

  function failsafeRun(): RunEntryView[] {
    return [
      turnEntry("t1", { origin: "prompt", kind: "row", state: "stopping" }),
      compactionEntry("compact-1", { after: "next" }, { origin: "failsafe" }),
      resumeEntry("m-resume", RESUME),
      rowEntry(
        "m-q1",
        { after: "turns", turns: 1 },
        { state: "paused", row: { id: "m-q1", text: "first" }, send_now: "after_compaction" },
      ),
      rowEntry(
        "m-q2",
        { after: "turns", turns: 2 },
        { state: "paused", row: { id: "m-q2", text: "second" }, send_now: "after_compaction" },
      ),
    ];
  }

  it("draws the failsafe's compaction, then marotte's resume, then the rows the stop paused", () => {
    withRun(failsafeRun(), { send: "queue" }, false);

    expect(rows().map(textOf)).toEqual([
      "Compact the context before it runs out",
      "Continue the stopped task",
      "first",
      "second",
    ]);
    expect(rows().map(labelOf)).toEqual([
      "Sends next",
      "Sent by marotte \u00b7 after the compaction",
      "After the next turn",
      "After 2 turns",
    ]);
    const resume = rowAt(1);
    expect(resume.getAttribute("aria-label")).toBe(
      "Sent by marotte \u00b7 after the compaction: Continue the stopped task",
    );
    expect(resume.dataset["tooltip"]).toBe(RESUME);
  });

  // The resume has no words of the reader's to edit and is never the reader's to send early: removing it is the one
  // control, and it removes only the resume.
  it("lets the reader remove the resume and nothing else", async () => {
    withRun(failsafeRun(), { send: "queue" }, false);
    const resume = rowAt(1);
    expect(actions(resume)).toEqual(["Do not continue the stopped task"]);
    expect(actionButton(resume, "Do not continue").dataset["tooltip"]).toBe(
      "marotte will not ask the agent to continue after the compaction",
    );

    clickAction(resume, "Do not continue");
    await vi.waitFor(() => {
      expect(unqueueDispatch).toHaveBeenCalledExactlyOnceWith({
        chatID: "chat-1",
        messageID: "m-resume",
      });
    });
    expect(cancelCompactDispatch).not.toHaveBeenCalled();
  });

  it("says cancelling the failsafe's compaction also leaves the stopped task", () => {
    withRun(failsafeRun(), { send: "queue" }, false);

    expect(actions(firstRow())).toEqual(["Cancel the queued compaction"]);
    expect(actionButton(firstRow(), "Cancel the queued compaction").dataset["tooltip"]).toBe(
      "The context stays as it is, and the stopped task is not continued",
    );
  });

  it("draws the failsafe's running compaction with its reason", () => {
    withRun(
      [
        compactionEntry("compact-1", { after: "running" }, { origin: "failsafe" }),
        resumeEntry("m-resume", RESUME),
      ],
      { send: "queue" },
      false,
    );

    expect(rows().map(textOf)).toEqual([
      "Compacting the context before it runs out\u2026",
      "Continue the stopped task",
    ]);
  });

  it("draws a resume taken with no turn open yet as starting, and none once its turn opens", () => {
    withRun(
      [resumeEntry("m-resume", RESUME, { after: "starting" }, { state: "starting" })],
      { send: "queue" },
      false,
    );
    expect(rows().map(labelOf)).toEqual(["Sent by marotte \u00b7 starting"]);
    expect(actions(firstRow())).toEqual([]);

    withRun(
      [resumeEntry("m-resume", RESUME, { after: "running" }, { state: "running", turn: "t2" })],
      { send: "steer", cancel: "m-resume" },
      false,
    );
    expect(rows()).toEqual([]);
  });

  it("keeps a steer the agent has yet to read above every entry, under the running turn", () => {
    withRun([compactionEntry("compact-1", { after: "turns", turns: 1, this: true })]);
    recordSteerQueued("chat-1", { id: "steer-1", text: "in this turn", origin: "user" });

    expect(rows().map(textOf)).toEqual(["in this turn", "Compact the context"]);
    expect(rows().map(labelOf)).toEqual(["Sent", "After this turn"]);
  });

  // A steer waiting for a prompt rides the entry that carries it; behind a compaction that sends none, it
  // goes with the next message, never "after the compaction".
  it("does not say a steer waiting for a prompt goes after a compaction whose end sends none", () => {
    withRun(
      [compactionEntry("compact-1", { after: "running" })],
      { send: "queue", unread_wait: { after: "compaction" } },
      false,
    );
    recordSteerQueued("chat-1", {
      id: "steer-1",
      text: "a",
      origin: "user",
      state: "unsent",
      awaitsPrompt: true,
    });

    expect(rows().map(labelOf)).toEqual(["Running", "With your next message"]);
  });

  it("draws a steer waiting for a prompt under the row that carries it, with that row's wait", () => {
    withRun([
      rowEntry(
        "m-q1",
        { after: "turns", turns: 1, this: true },
        { row: { id: "m-q1", text: "first" }, carries_unread: true, send_now: "stop" },
      ),
    ]);
    recordSteerQueued("chat-1", {
      id: "steer-1",
      text: "a",
      origin: "user",
      state: "unsent",
      awaitsPrompt: true,
    });

    expect(rows().map(textOf)).toEqual(["first", "a"]);
    expect(rows().map(labelOf)).toEqual(["After this turn", "Unread \u00b7 after this turn"]);
  });

  // A screen reader hears what a sighted reader's tooltip says the arrow does.
  it("names the send-now arrow by its tooltip, and renames it when the wait changes", () => {
    withRun(
      [
        compactionEntry("compact-1", { after: "running" }),
        ...behindTurn(three).map((e) => ({ ...e, send_now: "after_compaction" as const })),
      ],
      { send: "queue" },
      false,
    );
    const arrow = (): HTMLElement => rowAt(1).querySelector<HTMLElement>(".steer-actions button")!;
    expect(arrow().getAttribute("aria-label")).toBe(arrow().dataset["tooltip"]);

    withRun(behindTurn(three));

    expect(textOf(rowAt(1))).toBe("second");
    expect(arrow().getAttribute("aria-label")).toBe(arrow().dataset["tooltip"]);
    expect(arrow().getAttribute("aria-label")).toBe("Stop the turn and send this follow-up next");
  });

  it("draws a queued follow-up below the waiting steers", () => {
    withRun(behindTurn([{ id: "m-q1", text: "then add tests" }]));
    recordSteerQueued("chat-1", { id: "steer-1", text: "use tabs", origin: "user" });

    expect(rows().map(textOf)).toEqual(["use tabs", "then add tests"]);
    expect(labelOf(rows()[1] as HTMLElement)).toBe("After this turn");
  });

  it("shows a labelled follow-up's label, keeping its text in the tooltip", () => {
    withRun(behindTurn([{ id: "m-q1", text: "the whole findings", label: "Merging findings" }]));

    expect(textOf(firstRow())).toBe("Merging findings");
    expect(firstRow().dataset["tooltip"]).toBe("the whole findings");
  });

  it("repaints a follow-up whose label alone changed under the same id", () => {
    withRun(behindTurn([{ id: "m-q1", text: "the whole findings", label: "Merging findings" }]));
    const before = firstRow();
    withRun(behindTurn([{ id: "m-q1", text: "the whole findings", label: "Merged findings" }]));

    expect(firstRow()).toBe(before);
    expect(textOf(firstRow())).toBe("Merged findings");
    expect(firstRow().getAttribute("aria-label")).toBe("After this turn: Merged findings");
  });

  it("says what discarding loses: a user row was never sent, a carried row will not be sent again", () => {
    withRun(
      behindTurn([
        { id: "m-u", text: "follow-up" },
        { id: "m-c", text: "unread one", resends: ["steer-1"] },
      ]),
    );
    const hint = (row: HTMLElement): string =>
      row.querySelector(".steer-act-danger")?.getAttribute("data-tooltip") ?? "";

    expect(hint(rows()[0] as HTMLElement)).toContain("It has not been sent");
    expect(hint(rows()[1] as HTMLElement)).toContain("It will not be sent again");
  });

  it("updates a carried row's text in place when it absorbs a later carry", () => {
    withRun(behindTurn([{ id: "m-c", text: "use tabs", resends: ["steer-1"] }]));
    const before = firstRow();
    withRun(
      behindTurn([
        { id: "m-c", text: "use tabs\n\nand rename it", resends: ["steer-1", "steer-2"] },
      ]),
    );

    expect(firstRow()).toBe(before);
    expect(textOf(firstRow())).toBe("use tabs and rename it");
  });

  it("discards one follow-up by its own id, leaving the steer buffer alone", async () => {
    withRun(behindTurn(three.slice(0, 2)));
    clickAction(rows()[1] as HTMLElement, "Discard");

    await vi.waitFor(() => {
      expect(unqueueDispatch).toHaveBeenCalledWith({ chatID: "chat-1", messageID: "m-q2" });
    });
    expect(clearDispatch).not.toHaveBeenCalled();
    expect(confirmMock).not.toHaveBeenCalled();
  });

  // SEND NOW. The server offers the arrow (`send_now`) and moves the row; a stop names the turn the arrow
  // was drawn against, so a press that crosses that turn's end stops nothing else.
  it("offers a follow-up the server's send-now arrow, first", () => {
    withRun(behindTurn(three));
    recordSteerQueued("chat-1", { id: "steer-1", text: "use tabs", origin: "user" });

    expect(actions(rowAt(1))).toEqual([
      "Stop the turn and send this follow-up next",
      "Edit this follow-up",
      "Discard this follow-up",
    ]);
    const svg = (row: HTMLElement, label: string): string =>
      actionButton(row, label).querySelector("svg")?.outerHTML ?? "";
    expect(svg(rowAt(1), "Stop the turn")).not.toBe("");
    expect(svg(rowAt(1), "Stop the turn")).toBe(svg(rowAt(0), "Send this message now"));
  });

  it("stops the turn the arrow was drawn against and sends the pressed follow-up first", async () => {
    withRun(behindTurn(three));
    clickAction(rowAt(2), "Stop the turn");

    await vi.waitFor(() => {
      expect(sendQueuedNowDispatch).toHaveBeenCalledWith({
        chatID: "chat-1",
        messageID: "m-q3",
        stop: "t1",
      });
    });
    expect(cancelDispatch).not.toHaveBeenCalled();
    expect(unqueueDispatch).not.toHaveBeenCalled();
    expect(setComposerValueMock).not.toHaveBeenCalled();
  });

  it("takes back the stop announcement when the server stopped nothing", async () => {
    sendQueuedNowDispatch.mockReturnValueOnce({
      outcome: Promise.resolve<RemoveOutcome>({ status: "success", value: true }),
    });
    withRun(behindTurn(three));
    clickAction(rowAt(2), "Stop the turn");

    await vi.waitFor(() => {
      expect(mocks.announceMock).toHaveBeenLastCalledWith(
        "The turn keeps running: the message already left the queue",
      );
    });
  });

  it("moves a follow-up up with no stop where nothing runs to stop", async () => {
    withRun(
      [
        rowEntry("m-q1", { after: "next" }, { row: { id: "m-q1", text: "first" } }),
        rowEntry(
          "m-q2",
          { after: "turns", turns: 1 },
          { row: { id: "m-q2", text: "second" }, send_now: "now" },
        ),
      ],
      { send: "queue" },
      false,
    );

    expect(actions(firstRow())).toEqual(["Edit this follow-up", "Discard this follow-up"]);
    clickAction(rowAt(1), "Send this follow-up next");

    await vi.waitFor(() => {
      expect(sendQueuedNowDispatch).toHaveBeenCalledWith({
        chatID: "chat-1",
        messageID: "m-q2",
        stop: "",
      });
    });
    expect(cancelDispatch).not.toHaveBeenCalled();
  });

  // An empty stop names no entry, so a press that crosses a take never stops the turn it started.
  it("names no turn to stop for a follow-up sent first once the compaction ends", async () => {
    withRun(
      [
        compactionEntry("compact-1", { after: "running" }),
        ...behindTurn(three).map((e) => ({ ...e, send_now: "after_compaction" as const })),
      ],
      { send: "queue", cancel: "" },
      false,
    );
    clickAction(rowAt(2), "Send this follow-up first once the compaction ends");

    await vi.waitFor(() => {
      expect(sendQueuedNowDispatch).toHaveBeenCalledWith({
        chatID: "chat-1",
        messageID: "m-q2",
        stop: "",
      });
    });
    expect(mocks.announceMock).not.toHaveBeenCalledWith(
      "The turn keeps running: the message already left the queue",
    );
  });

  it("reports a refused send-now in the server's words", async () => {
    withRun(behindTurn([{ id: "m-q1", text: "first" }]));
    sendQueuedNowDispatch.mockReturnValue({
      outcome: Promise.resolve({
        status: "error",
        error: { status: 409, message: "this follow-up was queued before the server restarted" },
      }),
    });
    clickAction(firstRow(), "Stop the turn");

    await vi.waitFor(() => {
      expect(errorToastMock).toHaveBeenCalledWith(
        "this follow-up was queued before the server restarted",
      );
    });
  });

  it("stops the running turn a steer's send-now names, leading with that steer", async () => {
    withRun([]);
    recordSteerQueued("chat-1", { id: "steer-1", text: "use tabs", origin: "user" });
    clickAction(firstRow(), "Send this message now");

    await vi.waitFor(() => {
      expect(cancelDispatch).toHaveBeenCalledWith({
        chatID: "chat-1",
        lead: "steer-1",
        target: "t1",
      });
    });
  });

  it("offers a steer no send-now while nothing runs that it could stop", () => {
    withRun([compactionEntry("compact-1", { after: "running" })], { send: "queue" }, false);
    recordSteerQueued("chat-1", { id: "steer-1", text: "use tabs", origin: "user" });

    expect(actions(firstRow())).not.toContain("Send this message now");
  });

  it("fills the composer with the follow-up and its attachments before the removal is sent", () => {
    withRun(
      behindTurn([
        { id: "m-q1", text: "look at this", attachments: [{ path: "src/a.ts", name: "a.ts" }] },
      ]),
    );
    unqueueDispatch.mockImplementation(() => ({
      outcome: new Promise<{ status: string }>(() => undefined),
    }));
    clickAction(firstRow(), "Edit");

    expect(setComposerValueMock).toHaveBeenCalledWith("look at this");
    expect(addAttachmentMock).toHaveBeenCalledWith("chat-1", "src/a.ts");
    expect(unqueueDispatch).toHaveBeenCalledWith({ chatID: "chat-1", messageID: "m-q1" });
  });

  it("keeps the follow-up in the composer when the removal's reply is lost", async () => {
    withRun(
      behindTurn([
        { id: "m-q1", text: "look at this", attachments: [{ path: "src/a.ts", name: "a.ts" }] },
      ]),
    );
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
      withRun(
        behindTurn([
          { id: "m-q1", text: "look at this", attachments: [{ path: "src/a.ts", name: "a.ts" }] },
        ]),
      );
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
    withRun(
      behindTurn([
        { id: "m-q1", text: "look at this", attachments: [{ path: "src/a.ts", name: "a.ts" }] },
      ]),
    );
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

    recordSending("chat-1", "m-1", "use tabs instead");
    const anims = await entering(firstRow());
    expect(anims.map((a) => (a as CSSTransition).transitionProperty).sort()).toEqual([
      "opacity",
      "transform",
    ]);
  });

  it("updates the same element rather than replacing it on the confirmation", () => {
    recordSending("chat-1", "m-1", "use tabs instead");
    const sending = firstRow();
    expect(sending.dataset["state"]).toBe("sending");

    recordSteerQueued("chat-1", { id: "steer-m-1", text: "use tabs instead", origin: "user" });

    expect(firstRow(), "the node is updated in place, not rebuilt").toBe(sending);
    expect(sending.dataset["state"]).toBe("sent");
    expect(actions(sending)).toEqual(PER_ROW("use tabs instead"));
  });

  it("does not fade a second time when the confirmation lands", async () => {
    recordSending("chat-1", "m-1", "use tabs instead");
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

    recordSending("chat-1", "m-2", "second");

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
      recordSending("chat-1", "m-1", "use tabs instead");
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

      expect(actions(row), "the controls did arrive").toHaveLength(4);
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
    expect(actions(row).length, "one unread message, so every per-row control is offered").toBe(4);

    const textBox = textEl(row).getBoundingClientRect();
    for (const btn of row.querySelectorAll<HTMLElement>(".steer-act")) {
      const box = btn.getBoundingClientRect();
      expect(box.x, "still in the actions column, right of the message").toBeGreaterThan(textBox.x);
      expect(box.right, "and still inside the row").toBeLessThanOrEqual(
        row.getBoundingClientRect().right + 1,
      );
    }
  });

  // The oracle is the turn header's band (`turn-header.test.ts`): one line, its source then the passage.
  it("draws a quoted passage as a sent prompt's one-line band above the rest, within the four lines", () => {
    const text = `${appendQuote("", quoteSelection(`${"word ".repeat(60)}end`, "From Kiro's reply"))}${LONG}`;
    recordSteerQueued("chat-1", { id: "steer-1", text, origin: "user" });
    const row = firstRow();
    const band = textEl(row).querySelector<HTMLElement>(":scope > blockquote.turn-req-quote");
    expect(band?.querySelector(".turn-req-quote-from")?.textContent).toBe("From Kiro's reply:");
    expect(textEl(row).textContent).not.toContain(">");
    expect(textEl(row).textContent).not.toContain(QUOTE_ATTRIBUTION);
    const bandLine = parseFloat(getComputedStyle(band!).lineHeight);
    expect(band!.getBoundingClientRect().height).toBeCloseTo(bandLine, 0);
    const textLine = parseFloat(getComputedStyle(textEl(row)).lineHeight);
    expect(textEl(row).clientHeight).toBeCloseTo(bandLine + 3 * textLine, 0);
    expect(textEl(row).scrollHeight).toBeGreaterThan(textEl(row).clientHeight);
    // Firefox draws no ellipsis on a band that clips itself inside the clamp, which Chromium cannot show: the band
    // clips nothing, and its line clips and ellipsizes.
    const line = band!.querySelector<HTMLElement>(":scope > .turn-req-quote-line")!;
    expect(getComputedStyle(band!).overflow).toBe("visible");
    expect(getComputedStyle(line).textOverflow).toBe("ellipsis");
    expect(line.scrollWidth).toBeGreaterThan(line.clientWidth);
    const read = `From Kiro's reply: ${"word ".repeat(60)}end\n\n${LONG.trim()}`;
    expect(row.dataset["tooltip"]).toBe(read);
    expect(row.getAttribute("aria-label")).toBe(
      `Sent, waiting for the agent: ${read.replace(/\s+/g, " ")}`,
    );
    expect(actions(row)).toContain('Delete "From Kiro\'s reply: word word word word …"');
  });

  // Each band is one of the four lines, wherever it sits.
  it.each([
    {
      name: "a quote, a line, a quote, then a long rest",
      quotes: 2,
      between: "remind me",
      bands: 2,
    },
    { name: "six quotes back to back, then a long rest", quotes: 6, between: "", bands: 4 },
  ])("holds $name to four lines, each band one line", ({ quotes, between, bands: shownBands }) => {
    const quote = quoteSelection(`${"word ".repeat(60)}end`, "From Kiro's reply");
    let text = "";
    for (let i = 0; i < quotes; i++) {
      text = appendQuote(i === 0 ? "" : `${text}${between}`, quote);
    }
    text += LONG;
    recordSteerQueued("chat-1", { id: "steer-1", text, origin: "user" });
    const row = firstRow();
    const bands = [
      ...textEl(row).querySelectorAll<HTMLElement>(":scope > blockquote.turn-req-quote"),
    ];
    expect(bands).toHaveLength(quotes);
    expect(textEl(row).textContent).not.toContain(">");
    const bandLine = parseFloat(getComputedStyle(bands[0]!).lineHeight);
    for (const band of bands.slice(0, shownBands)) {
      expect(band.getBoundingClientRect().height).toBeCloseTo(bandLine, 0);
    }
    const textLine = parseFloat(getComputedStyle(textEl(row)).lineHeight);
    expect(textEl(row).clientHeight).toBeCloseTo(
      shownBands * bandLine + (4 - shownBands) * textLine,
      0,
    );
    expect(textEl(row).scrollHeight).toBeGreaterThan(textEl(row).clientHeight);
  });

  // The aside ink resolves to the band's own secondary ink in both themes, so it is set apart here
  // to see which rule paints the band.
  const quoted = `${appendQuote("", quoteSelection("quoted", "From Kiro's reply"))}reply`;
  const subordinate: [string, () => void][] = [
    ["sending", () => recordSending("chat-1", "m-1", quoted)],
    [
      "delivering",
      () =>
        recordSteerQueued("chat-1", {
          id: "steer-1",
          text: quoted,
          origin: "user",
          state: "delivering",
        }),
    ],
  ];
  for (const [state, record] of subordinate) {
    it(`dims a ${state} row's quote band to the ink of its text`, () => {
      const root = document.documentElement;
      root.style.setProperty("--c-text-aside", "rgb(1, 2, 3)");
      try {
        record();
        const row = firstRow();
        expect(row.dataset["state"]).toBe(state);
        const band = textEl(row).querySelector<HTMLElement>(":scope > blockquote.turn-req-quote")!;
        expect(getComputedStyle(textEl(row)).color).toBe("rgb(1, 2, 3)");
        expect(getComputedStyle(band).color).toBe("rgb(1, 2, 3)");
      } finally {
        root.style.removeProperty("--c-text-aside");
      }
    });
  }
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

  it("draws a quoted message to the step as the chat's stack does", () => {
    const text = `${appendQuote("", quoteSelection("quoted", "From Kiro's reply"))}reply`;
    const src: SteerStackSource = {
      read: () => ({
        id: "wf_1|root/review",
        steers: [{ id: "steer-s1", text, origin: "user" }],
        queued: [],
      }),
      edit: () => Promise.resolve(),
      editByClear: () => Promise.resolve(),
      remove: () => Promise.resolve(),
      discard: () => Promise.resolve(),
      waitingFor: "the step",
    };
    const stack = document.createElement("ul");
    stack.className = "steer-stack hidden";
    document.body.appendChild(stack);
    mountSteerStack(stack, src);
    const row = stack.querySelector<HTMLElement>(".steer-row")!;
    expect(row.querySelector(".steer-text > blockquote.turn-req-quote")?.textContent).toBe(
      "From Kiro's reply: quoted",
    );
    expect(textOf(row)).toBe("From Kiro's reply: quotedreply");
    stack.remove();
  });
});
