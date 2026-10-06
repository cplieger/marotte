// The composer has ONE textarea and three modules writing to it, and exactly one of them owns what
// the draft IS.
import { describe, it, expect, beforeEach, vi } from "vitest";

import type * as Store from "./store.js";
import type * as ComposerState from "./composer-state.js";
import type * as PromptInput from "./prompt-input.js";
import type * as ShareTarget from "./share-target.js";
import type { Session, TurnState } from "./types.js";
import type { Entry, EntryTurnOpen } from "./wire/types.gen.js";

/** Cache-buster for the re-imports below. */
let bootSeq = 0;

const { mockDispatch, mockFlush, mockPending, mockSubmit } = vi.hoisted(() => ({
  mockDispatch: vi.fn(),
  mockFlush: vi.fn(),
  mockPending: vi.fn(() => false),
  mockSubmit: vi.fn(),
}));

// What reaches the draft action, and when a flush is forced. The 600ms itself is the library's
// business.
vi.mock("./actions/index.js", () => ({
  debouncedDispatch: () =>
    Object.assign(mockDispatch, { isPending: mockPending, flush: mockFlush, cancel: vi.fn() }),
  registerCleanup: vi.fn(),
}));
// Both composer writers, because attachments.ts dispatches through the same debounced-action layer
// as the draft: a mock naming only one of them fails the module's IMPORT, not an assertion, so the
// whole file goes red with no clue why.
vi.mock("./actions/chat.js", () => ({
  setDraft: { name: "chat.set_draft" },
  setAttachments: { name: "chat.set_attachments" },
}));
vi.mock("./platform.js", () => ({ fixIOSViewport: vi.fn(), isIOS: false }));
vi.mock("./pill-expand.js", () => ({ collapseAll: vi.fn() }));
// share-target's other job is the ?agent=planner shortcut, whose import graph is the whole chat
// lifecycle.
vi.mock("./chat.js", () => ({ createPlannerSession: vi.fn() }));

/** The one prompt the chat has already sent, so ArrowUp has somewhere to go. */
const PRIOR_PROMPT = "the prompt I sent an hour ago";

/** One sent prompt, as the store holds it: a turn whose `turn_open` CARRIES it. */
function promptTurn(id: string, n: number, text: string): [string, TurnState] {
  const turnID = `${id}-t${String(n)}`;
  const payload: EntryTurnOpen = {
    source: "prompt",
    n,
    prompt: { id: `m-${turnID}`, text },
  };
  const open: Entry = {
    id: `${turnID}-open`,
    turn: turnID,
    kind: "turn_open",
    seq: 0,
    ts: n,
    payload,
  };
  return [turnID, { entries: [open], openEntries: new Map() }];
}

function makeSession(id: string, prompts: string[]): Session {
  const turns = new Map<string, TurnState>();
  const order: string[] = [];
  for (const [i, text] of prompts.entries()) {
    const [turnID, state] = promptTurn(id, i + 1, text);
    turns.set(turnID, state);
    order.push(turnID);
  }
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
    turns,
    turn_order: order,
    turn_count: prompts.length,
    has_more: false,
    thinking: false,
    working_label: "Thinking",
  };
}

interface Mounted {
  store: typeof Store;
  composerState: typeof ComposerState;
  promptInput: typeof PromptInput;
  shareTarget: typeof ShareTarget;
  input: HTMLTextAreaElement;
}

/** Fresh module graph per case: prompt-input latches its own init and both modules wire
 *  listeners once, so a shared instance would carry the previous case's element and history
 *  position. */
async function mount(): Promise<Mounted> {
  vi.resetModules();
  bootSeq++;
  document.body.innerHTML = `
    <div id="chat-area">
      <form id="prompt-form">
        <div id="prompt-box">
          <textarea id="prompt-input"></textarea>
          <ul id="attachment-row" class="hidden"></ul>
          <button id="send-btn" type="submit"></button>
        </div>
      </form>
    </div>`;
  const store = await import("./store.js");
  const composerState = await import("./composer-state.js");
  // composer-state is NOT busted: prompt-input imports it, and a busted copy here would be a SECOND
  // instance holding a different drafts map from the one the module under test writes.
  composerState._resetComposerStateForTest();
  const promptInput = (await import(
    /* @vite-ignore */ `./prompt-input.ts?boot=${bootSeq}`
  )) as typeof PromptInput;
  const shareTarget = (await import(
    /* @vite-ignore */ `./share-target.ts?boot=${bootSeq}`
  )) as typeof ShareTarget;
  store.setSessions([makeSession("c1", [PRIOR_PROMPT]), makeSession("c2", [])]);
  store.setActive("c1");
  // The order app.ts uses: the draft layer is listening before anything writes.
  composerState.initComposerState();
  promptInput.initPromptInput(mockSubmit, () => undefined);
  composerState.restoreComposerState("c1");
  return {
    store,
    composerState,
    promptInput,
    shareTarget,
    input: document.getElementById("prompt-input") as HTMLTextAreaElement,
  };
}

/** Type, the way a keystroke does: the value and the event that announces it. */
function type(input: HTMLTextAreaElement, text: string): void {
  input.value = text;
  input.dispatchEvent(new Event("input", { bubbles: true }));
}

function pressKey(input: HTMLTextAreaElement, key: string): KeyboardEvent {
  const e = new KeyboardEvent("keydown", { key, bubbles: true, cancelable: true });
  input.dispatchEvent(e);
  return e;
}

beforeEach(() => {
  mockDispatch.mockClear();
  mockFlush.mockClear();
  mockSubmit.mockClear();
  mockPending.mockReturnValue(false);
  history.replaceState(null, "", "/");
});

describe("history cycling versus the per-chat draft", () => {
  it("shows the previous prompt without telling the draft layer about it", async () => {
    const { input } = await mount();
    type(input, "the draft I am still writing");
    expect(mockDispatch).toHaveBeenLastCalledWith({
      chatID: "c1",
      text: "the draft I am still writing",
    });

    expect(pressKey(input, "ArrowUp").defaultPrevented).toBe(true);
    expect(input.value).toBe(PRIOR_PROMPT);
    // The display is not an edit: nothing new reached the action.
    expect(mockDispatch).toHaveBeenCalledTimes(1);
  });

  it("flushes the typed draft on blur, not the history item on screen", async () => {
    const { input } = await mount();
    type(input, "the draft I am still writing");
    pressKey(input, "ArrowUp");
    expect(input.value).toBe(PRIOR_PROMPT);

    mockPending.mockReturnValue(true);
    input.dispatchEvent(new FocusEvent("blur"));
    expect(mockFlush).toHaveBeenCalledWith({
      chatID: "c1",
      text: "the draft I am still writing",
    });
  });

  it("brings the typed draft back after a chat switch made while history showed", async () => {
    const { input, composerState } = await mount();
    type(input, "the draft I am still writing");
    pressKey(input, "ArrowUp");
    mockPending.mockReturnValue(true);

    composerState.saveComposerState();
    composerState.restoreComposerState("c2");
    expect(input.value).toBe("");
    composerState.saveComposerState();
    composerState.restoreComposerState("c1");

    // The local half: the map entry survived rather than being replaced by the prompt the box was
    // showing.
    expect(input.value).toBe("the draft I am still writing");
    expect(mockFlush).toHaveBeenCalledWith({
      chatID: "c1",
      text: "the draft I am still writing",
    });
  });

  it("adopts a history item the user actually edits", async () => {
    // The other direction: once a keystroke lands on the displayed prompt it IS the draft, because
    // the edit announces itself like any other.
    const { input, composerState } = await mount();
    type(input, "scratch");
    pressKey(input, "ArrowUp");
    type(input, `${PRIOR_PROMPT}, but shorter`);

    composerState.saveComposerState();
    composerState.restoreComposerState("c1");
    expect(input.value).toBe(`${PRIOR_PROMPT}, but shorter`);
  });

  it("records the submit's clear, so a sent message is not left on the record", async () => {
    const { input } = await mount();
    type(input, "send me");
    input.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true }));
    expect(mockSubmit).toHaveBeenCalledWith("send me");
    expect(input.value).toBe("");
    expect(mockDispatch).toHaveBeenLastCalledWith({ chatID: "c1", text: "" });
  });
});

describe("a prompt arriving from the share sheet", () => {
  it("is recorded for the active chat exactly once", async () => {
    const { input, shareTarget } = await mount();
    history.replaceState(null, "", "/?prompt=fix%20the%20flaky%20test");
    shareTarget.applyShareTarget();

    expect(input.value).toBe("fix the flaky test");
    // The silent assignment left this at zero, so nothing scheduled a save and a reload before the
    // first keystroke lost the shared text.
    expect(mockDispatch).toHaveBeenCalledTimes(1);
    expect(mockDispatch).toHaveBeenCalledWith({ chatID: "c1", text: "fix the flaky test" });
  });

  it("is persisted by the flush a reload runs", async () => {
    const { shareTarget } = await mount();
    history.replaceState(null, "", "/?prompt=fix%20the%20flaky%20test");
    shareTarget.applyShareTarget();

    mockPending.mockReturnValue(true);
    window.dispatchEvent(new Event("pagehide"));
    expect(mockFlush).toHaveBeenCalledWith({ chatID: "c1", text: "fix the flaky test" });
  });

  it("survives a switch away and back like any other draft", async () => {
    const { input, composerState, shareTarget } = await mount();
    history.replaceState(null, "", "/?prompt=fix%20the%20flaky%20test");
    shareTarget.applyShareTarget();

    composerState.saveComposerState();
    composerState.restoreComposerState("c2");
    composerState.saveComposerState();
    composerState.restoreComposerState("c1");
    expect(input.value).toBe("fix the flaky test");
  });
});
