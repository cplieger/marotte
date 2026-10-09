// chat.ts's lifecycle, with every direct dependency mocked at the first hop. The store mock
// is stateful for activeId so getActiveId() returns the id createSession set.
import { describe, it, expect, vi, beforeEach } from "vitest";

// Type-only, for the `importOriginal` below.
import type * as Skeleton from "./skeleton.js";

// The creating actions answer with the SERVER's header, so the mocks do too (undefined
// would take the refused branch). A FIXED id per action, so assertions name their id.
const { setModeDispatch, forkDispatch, createDispatch, submitPromptMock, messagesEl } = vi.hoisted(
  () => {
    const serverHeader = (id: string): unknown => ({
      id,
      name: "New conversation",
      model: "auto",
      usage: {
        context_pct: 0,
        context_size: 0,
        credits: 0,
        last_turn_ms: 0,
        has_real_data: false,
      },
      created_at: 0,
      updated_at: 0,
      turn_count: 0,
    });
    // The creating reply: chat, the tab the coordinator opened, the committed version. A fork's
    // subject carries the PARENT the server nested it under.
    const serverCreated = (id: string, tabID: string, parent = ""): unknown => ({
      chat: serverHeader(id),
      subject: { id: tabID, kind: "chat", ref: id, parent, pinned: false, owns: true },
      version: 3,
    });
    return {
      setModeDispatch: vi.fn(),
      // Typed with a payload parameter, so the calls tuple carries the argument under test.
      forkDispatch: vi.fn(async (_payload?: Record<string, unknown>) =>
        serverCreated("c-forked", "tb_forked", "tb_parent"),
      ),
      createDispatch: vi.fn(async (_payload?: Record<string, unknown>) =>
        serverCreated("c-created", "tb_created"),
      ),
      submitPromptMock: vi.fn(),
      messagesEl: document.createElement("div"),
    };
  },
);

let activeId = "";

vi.mock("./store.js", () => ({
  // A real spy: seeding a created chat recomputes the DERIVED usage.context_size, a function
  // of the model the header cannot carry.
  setModel: vi.fn(),
  getActiveId: () => activeId,
  getActive: vi.fn(() => undefined),
  get: vi.fn(() => undefined),
  getSessions: vi.fn(() => []),
  // The per-row strip effect's tracked read; inert until a case aims it.
  watchSession: vi.fn(() => undefined),
  // Present for real-ESM linking (`chat-settled.js` imports it), with the real derivation as
  // in `__test-helpers__/store-mock.ts`.
  turnLive: (s: { thinking: boolean; turn_open?: boolean; provisional?: boolean }) =>
    s.thinking || s.turn_open === true || s.provisional === true,
  setActive: vi.fn((id: string) => {
    activeId = id;
  }),
  upsertHeader: vi.fn(),
  // The real predicate, transcribed: the model-picker branch keys on it, so a
  // stub returning a constant would send every fixture down one arm.
  isEmptyChat: (s: { turn_count: number; turn_order: unknown[] } | undefined) =>
    s === undefined || (s.turn_count === 0 && s.turn_order.length === 0),
  contextSizeFor: vi.fn(() => 0),
  defaultUsage: vi.fn(() => ({
    context_pct: 0,
    context_size: 0,
    credits: 0,
    last_turn_ms: 0,
    has_real_data: false,
  })),
  activeSession: { value: undefined },
  removeChat: vi.fn(),
  // The dot's seed at tab creation; the finished-turn mark is the header's
  // `last_turn_outcome`.
  tabStatusFor: vi.fn(() => ""),
  // The activation refetch gate, TRUE by default; the gate describe drives both verdicts. The
  // predicate's truth table is store.test.ts's; this suite owns the routing.
  transcriptStale: vi.fn(() => true),
}));
// The loader, stubbed: `resolveUnknownChat` owns the ROUTING of each verdict; the
// status-to-verdict mapping is store-load.test.ts's.
vi.mock("./store-load.js", () => ({
  loadList: vi.fn(),
  loadMessages: vi.fn(),
  confirmChatExists: vi.fn(),
}));
vi.mock("./banner-stack.js", () => ({ ensureBound: vi.fn() }));
vi.mock("./chat-commands.js", () => ({ sendPromptTo: vi.fn() }));
vi.mock("./tabs.js", () => ({
  // `openTab` resolves with its OUTCOME: every open is a round trip through
  // `open_tab`, and the callers that branch (History's reopen) read the string.
  openTab: vi.fn(() => Promise.resolve("opened")),
  // The adoption path: creating commands paint their tab from the reply.
  adoptSubject: vi.fn(),
  activateTab: vi.fn(),
  // A chat id is not a tab id; "" means no tab is open for that chat.
  tabIdFor: vi.fn(() => ""),
  // The registry seam: which chat refs hold an open tab. Empty means the strip
  // wires no row effects; the row-effect suite below aims it at one chat.
  openChatRefs: vi.fn(() => [] as string[]),
  getActiveTabId: vi.fn(() => ""),
  renameTab: vi.fn(),
  setTabStatus: vi.fn(),
}));
vi.mock("./toast.js", () => import("./__test-helpers__/toast-mock.js").then((m) => m.toastMock()));
// The dot asks the dock for an unanswered decision; mocked to avoid the card builders.
vi.mock("./decision-dock.js", () => ({
  hasPendingDecision: vi.fn(() => false),
  dropDecisions: vi.fn(),
}));
// `paintPlaceholder` comes through REAL: it is the empty-container enforcement, and
// the case pinning its refusal asserts against the container it guards.
vi.mock("./skeleton.js", async (importOriginal) => ({
  ...(await importOriginal<typeof Skeleton>()),
  chatSkeleton: vi.fn(() => document.createElement("div")),
}));
vi.mock("@cplieger/ui-primitives/skeleton", () => ({
  skeletonTiming: vi.fn(() => ({ commit: vi.fn(), cancel: vi.fn() })),
}));
vi.mock("./picker.js", () => ({ showModelPicker: vi.fn(), hideModelPicker: vi.fn() }));
vi.mock("./messages.js", () => ({
  mountChatView: vi.fn(),
  setLoadMore: vi.fn(),
  rebaseLoadMore: vi.fn(),
  // activateChatView's success branch calls it, so it must exist.
  loadTurnRail: vi.fn(),
  pointTurnRail: vi.fn(),
  // No view here, so callers fall back to $.messages; a tab close disposes the chat's view.
  activeTranscriptView: vi.fn(() => null),
  transcriptViewFor: vi.fn(() => null),
  disposeChatView: vi.fn(),
}));
vi.mock("./attachments.js", () => ({
  addAttachment: vi.fn(),
  unownedAttachmentPaths: vi.fn(() => []),
}));
// chat.ts only orders the save/restore pair, which the mock records.
vi.mock("./composer-state.js", () => ({
  saveComposerState: vi.fn(),
  restoreComposerState: vi.fn(),
  retargetComposer: vi.fn(),
  seedComposerState: vi.fn(),
  flushComposerDraft: vi.fn(),
  dropComposerState: vi.fn(),
}));
vi.mock("./session-context.js", () => ({ setCurrentModel: vi.fn(), getLastModel: () => "auto" }));
vi.mock("./model-switcher.js", () => ({ applyLocalModel: vi.fn() }));
vi.mock("./context-ui.js", () => ({ refreshContextUI: vi.fn() }));
vi.mock("./roles.js", () => ({ iconForMode: vi.fn(() => "") }));
vi.mock("./submit.js", () => ({ submitPrompt: submitPromptMock }));
// A real element, witnessing that chat.ts registers no listener (see "no transcript context
// menu").
vi.mock("./dom.js", () => ({
  $: { messages: messagesEl, promptInput: { focus: () => undefined } },
  // The real `paintPlaceholder` marks its host busy through this module; the fake is
  // `setBusy`'s own body, since spreading the original back in reintroduces element lookups.
  setBusy: (el: Element, busy: boolean) => {
    if (busy) {
      el.setAttribute("aria-busy", "true");
    } else {
      el.removeAttribute("aria-busy");
    }
  },
}));
vi.mock("./retention.js", () => ({ isRetentionEnabled: vi.fn(() => false) }));
// governance.ts, reached through the settings actions, imports `onSSE`; Browser
// Mode links for real, so the name has to exist.
vi.mock("./bus.js", () => ({
  onBus: vi.fn(),
  onSSE: undefined,
  BUS_ACTIVATE_CHAT: "activate-chat",
}));
// chat.ts reaches transport.ts only for the correlation id; the `op-` prefix passes the
// server's ValidIdent gate.
vi.mock("./transport.js", () => ({ newOpID: () => "op-test" }));
vi.mock("./actions/chat.js", () => ({
  deleteChat: { dispatch: vi.fn() },
  restoreChat: { dispatch: vi.fn() },
  setMode: { dispatch: setModeDispatch },
  forkChat: { dispatch: forkDispatch },
  createChat: { dispatch: createDispatch },
}));

import * as chatModule from "./chat.js";
import {
  activateChatView,
  refreshChatView,
  closeChatTab,
  createPlannerSession,
  openTangentChat,
  openPreviousSession,
  installStoreSubscribers,
  switchSession,
  resolveUnknownChat,
} from "./chat.js";
import {
  openTab,
  adoptSubject,
  activateTab,
  tabIdFor,
  getActiveTabId,
  renameTab,
  setTabStatus,
  openChatRefs,
} from "./tabs.js";
import { addAttachment, unownedAttachmentPaths } from "./attachments.js";
import { dropDecisions } from "./decision-dock.js";
import { get, watchSession, removeChat, setActive, upsertHeader } from "./store.js";
import { transcriptStale } from "./store.js";
import { loadList, loadMessages, confirmChatExists } from "./store-load.js";
import { loadTurnRail, pointTurnRail, setLoadMore, disposeChatView } from "./messages.js";
import { seedComposerState } from "./composer-state.js";
import { info } from "./toast.js";
import { isRetentionEnabled } from "./retention.js";
import { skeletonTiming } from "@cplieger/ui-primitives/skeleton";
// No `closeChat`: a chat-tab close's process teardown is the server's `close_tab`.
import { deleteChat } from "./actions/chat.js";

beforeEach(() => {
  vi.clearAllMocks();
  vi.mocked(get).mockReturnValue(undefined);
  vi.mocked(isRetentionEnabled).mockReturnValue(false);
  vi.mocked(loadMessages).mockResolvedValue(true);
  vi.mocked(transcriptStale).mockReturnValue(true);
  activeId = "";
});

describe("createPlannerSession", () => {
  it("dispatches chat.set_mode with modeID 'plan' for the newly created chat", async () => {
    await createPlannerSession();
    expect(setModeDispatch).toHaveBeenCalledTimes(1);
    const arg = setModeDispatch.mock.calls[0]?.[0] as { chatID: string; modeID: string };
    expect(arg.modeID).toBe("plan");
    // The id is the SERVER's, so the dispatch has to wait for the create's reply.
    // Detaching it would send set_mode to whatever chat was active before.
    expect(arg.chatID).toBe("c-created");
  });

  // The create is a round trip, so it can be refused. Nothing may be addressed to
  // a chat that does not exist.
  it("dispatches nothing when the create is refused", async () => {
    createDispatch.mockResolvedValueOnce(null);
    await createPlannerSession();
    expect(setModeDispatch).not.toHaveBeenCalled();
  });

  // One op id per gesture, as a DISPATCH ARGUMENT: minted in run() it would change per retry.
  it("passes an op id with the create", async () => {
    await createPlannerSession();
    const arg = createDispatch.mock.calls[0]?.[0] as { opID: string };
    expect(arg.opID).toMatch(/^op-/);
  });
});

// createSession is ASYNC because the id is the SERVER's; these pin that every caller awaits
// or detaches, since a bare `void` silently lands work on the previous chat.

describe("createSession is async, and what that means for its callers", () => {
  it("does not set the active chat until the server has answered", async () => {
    let resolveCreate: (h: unknown) => void = () => undefined;
    createDispatch.mockReturnValueOnce(
      new Promise((res) => {
        resolveCreate = res;
      }),
    );
    const pending = chatModule.createSession();

    // The window a bare `void` would leave a dependent caller reading.
    expect(activeId).toBe("");
    expect(vi.mocked(adoptSubject)).not.toHaveBeenCalled();

    resolveCreate({
      chat: {
        id: "c-late",
        name: "New conversation",
        model: "auto",
        usage: {
          context_pct: 0,
          context_size: 0,
          credits: 0,
          last_turn_ms: 0,
          has_real_data: false,
        },
        created_at: 0,
        updated_at: 0,
        turn_count: 0,
      },
      subject: {
        id: "tb_late",
        kind: "chat",
        ref: "c-late",
        parent: "",
        pinned: false,
        owns: true,
      },
      version: 9,
    });
    await expect(pending).resolves.toBe("c-late");
    expect(activeId).toBe("c-late");
    expect(vi.mocked(adoptSubject)).toHaveBeenCalledTimes(1);
    expect(vi.mocked(activateTab)).toHaveBeenCalledWith("tb_late");
  });

  // A refused create opens nothing and returns "". Opening a tab anyway is the
  // window this whole stage removes: an id no server can resolve.
  it("opens no tab and returns the empty id when the create is refused", async () => {
    createDispatch.mockResolvedValueOnce(null);
    await expect(chatModule.createSession()).resolves.toBe("");
    expect(vi.mocked(adoptSubject)).not.toHaveBeenCalled();
    expect(vi.mocked(activateTab)).not.toHaveBeenCalled();
    expect(activeId).toBe("");
  });

  // The tab rides the CREATE's reply (one coordinator lock), so no second `open_tab`.
  it("adopts the tab from the create's reply instead of dispatching a second open", async () => {
    await chatModule.createSession();
    expect(vi.mocked(openTab)).not.toHaveBeenCalled();
    const [subject, name] = vi.mocked(adoptSubject).mock.calls[0] ?? [];
    expect(subject).toMatchObject({ id: "tb_created", kind: "chat", ref: "c-created" });
    expect(name).toBe("New conversation");
    expect(vi.mocked(activateTab)).toHaveBeenCalledWith("tb_created");
  });

  // The initial prompt rides INSIDE the create, which is why app.ts can detach that
  // one site: the send happens in the continuation, addressed to the created chat.
  it("sends an initial prompt to the chat it created, not to the one that was active", async () => {
    activeId = "c-previous";
    await chatModule.createSession("do the thing");
    expect(submitPromptMock).toHaveBeenCalledWith("c-created", "do the thing");
  });

  // Files staged before any chat existed would be discarded by the retarget, so an
  // attachment-only send from the empty state would create a chat and send nothing.
  it("carries files staged with no chat into the new chat and sends them", async () => {
    vi.mocked(unownedAttachmentPaths).mockReturnValueOnce(["/workspace/shot.png"]);
    await chatModule.createSession("");
    expect(vi.mocked(addAttachment)).toHaveBeenCalledWith("/workspace/shot.png");
    expect(submitPromptMock).toHaveBeenCalledWith("c-created", "");
  });

  it("sends nothing for an empty box with no staged files", async () => {
    await chatModule.createSession("");
    expect(submitPromptMock).not.toHaveBeenCalled();
  });

  it("seeds the row from the SERVER's header rather than a local guess", async () => {
    await chatModule.createSession();
    expect(vi.mocked(upsertHeader).mock.calls.at(-1)?.[0]).toMatchObject({ id: "c-created" });
  });
});

// Attaching a BATCH: N singular calls on an empty workspace would each create a chat.

describe("attaching several paths at once", () => {
  it("creates ONE chat for the whole batch", async () => {
    await chatModule.attachPathsToActiveChat(["/w/a.ts", "/w/b.ts", "/w/c.ts"]);

    expect(createDispatch).toHaveBeenCalledTimes(1);
    expect(vi.mocked(addAttachment).mock.calls.flat()).toEqual(["/w/a.ts", "/w/b.ts", "/w/c.ts"]);
  });

  it("creates nothing when a chat is already active", async () => {
    activeId = "c-live";
    await chatModule.attachPathsToActiveChat(["/w/a.ts"]);

    expect(createDispatch).not.toHaveBeenCalled();
    expect(vi.mocked(addAttachment)).toHaveBeenCalledWith("/w/a.ts");
  });

  it("attaches nothing when the create is refused", async () => {
    createDispatch.mockResolvedValueOnce(null);
    await chatModule.attachPathsToActiveChat(["/w/a.ts"]);

    expect(vi.mocked(addAttachment)).not.toHaveBeenCalled();
  });

  it("does not ask for a chat for an empty batch", async () => {
    await chatModule.attachPathsToActiveChat([]);

    expect(createDispatch).not.toHaveBeenCalled();
  });
});

// Activating a chat with NO messages: the draft rides the single-chat GET, and a chat can be
// persisted with zero messages (set_mode, set_effort), so the draft must still load.

describe("the draft of a chat with no turns", () => {
  /** A persisted zero-turn chat. `model` is empty so the context-size branch
   *  above it (which calls setModel) stays out of the way. */
  function emptyChat(): never {
    return {
      id: "c-empty",
      model: "",
      turn_count: 0,
      turn_order: [],
      usage: { context_size: 0 },
    } as never;
  }

  /** Activate through the History row path, the one exported caller that reaches
   *  activateChatView for a chat the store already holds. */
  function activate(): void {
    openPreviousSession({ chat_id: "c-empty", session_id: "s1", title: "t", updated_at: 1 });
  }

  it("fetches the record so the stored draft can be adopted", async () => {
    vi.mocked(get).mockReturnValue(emptyChat());
    activate();
    await vi.waitFor(() => {
      expect(seedComposerState).toHaveBeenCalledWith("c-empty");
    });
    expect(loadMessages).toHaveBeenCalledWith("c-empty");
  });

  it("seeds nothing when the fetch fails", async () => {
    vi.mocked(get).mockReturnValue(emptyChat());
    vi.mocked(loadMessages).mockResolvedValue(false);
    activate();
    await vi.waitFor(() => {
      expect(loadMessages).toHaveBeenCalledWith("c-empty");
    });
    expect(seedComposerState).not.toHaveBeenCalled();
  });

  it("seeds nothing once the user has moved to another chat", async () => {
    // The composer is shared, so a seed landing after a switch would write the
    // outgoing chat's draft into the incoming chat's box.
    vi.mocked(get).mockReturnValue(emptyChat());
    vi.mocked(loadMessages).mockImplementation(async () => {
      activeId = "c-other";
      return true;
    });
    activate();
    await vi.waitFor(() => {
      expect(loadMessages).toHaveBeenCalledWith("c-empty");
    });
    expect(seedComposerState).not.toHaveBeenCalled();
  });

  // A brand-new chat is a server record before its tab opens, so the GET has an answer.
  it("fetches even a brand-new chat, because the server already has it", async () => {
    await createPlannerSession();
    const id = (vi.mocked(upsertHeader).mock.calls.at(-1)?.[0] as { id: string }).id;
    expect(id).toBe("c-created");
    vi.mocked(get).mockReturnValue(emptyChat());
    activate();
    await vi.waitFor(() => {
      expect(loadMessages).toHaveBeenCalledWith("c-empty");
    });
  });
});

// The rail is a module singleton, so activation must hand it the chat even with no turns.

describe("the timeline rail of a chat with no turns", () => {
  function emptyChat(): never {
    return {
      id: "c-empty",
      model: "",
      turn_count: 0,
      turn_order: [],
      usage: { context_size: 0 },
    } as never;
  }

  /** Drive the restore and await the activation, which runs in the open's continuation. */
  async function activate(): Promise<void> {
    openPreviousSession({ chat_id: "c-empty", session_id: "s1", title: "t", updated_at: 1 });
    await vi.waitFor(() => {
      expect(setActive).toHaveBeenCalledWith("c-empty");
    });
  }

  it("is pointed at the chat being activated", async () => {
    vi.mocked(get).mockReturnValue(emptyChat());
    await activate();
    expect(pointTurnRail).toHaveBeenCalledWith("c-empty");
  });

  it("is pointed at a brand-new chat too, which has no turns to fetch", async () => {
    // Every New chat click lands here. Pointing costs no request, and skipping
    // it is what left the previous chat's markers on screen.
    vi.mocked(get).mockReturnValue(emptyChat());
    await activate();
    expect(pointTurnRail).toHaveBeenCalledWith("c-empty");
  });

  it("is not fetched, because a chat with no resident turns has none to mark", async () => {
    vi.mocked(get).mockReturnValue(emptyChat());
    await activate();
    expect(loadTurnRail).not.toHaveBeenCalled();
  });
});

// The scroller is the other per-chat singleton: the "Latest" control and the unkeyed
// "Load older messages" button must follow the chat switch.

describe("the scroller on a chat switch", () => {
  /** A chat holding `turnIDs` resident turns; `turn_order` is the only field activation reads. */
  function chat(turnIDs: string[]): never {
    return {
      id: "c-1",
      model: "",
      turn_count: turnIDs.length,
      turn_order: turnIDs,
      usage: { context_size: 0 },
    } as never;
  }

  /** Drive the restore and wait for the activation, which runs in the OPEN's
   *  continuation now that opening a tab is a round trip. */
  async function activate(): Promise<void> {
    openPreviousSession({ chat_id: "c-1", session_id: "s1", title: "t", updated_at: 1 });
    await vi.waitFor(() => {
      expect(setActive).toHaveBeenCalledWith("c-1");
    });
  }

  it("re-keys the rail before the transcript repaints", async () => {
    // setActive repaints synchronously (the multiplexer swaps views); activation still owns
    // the rail, pointed before that frame.
    vi.mocked(get).mockReturnValue(chat([]));
    await activate();
    const rail = vi.mocked(pointTurnRail).mock.invocationCallOrder[0] ?? 0;
    const active = vi.mocked(setActive).mock.invocationCallOrder[0] ?? 0;
    expect(rail).toBeGreaterThan(0);
    expect(rail).toBeLessThan(active);
  });

  it("points the rail on a chat that has messages, without waiting for the fetch", async () => {
    // Re-keyed at the switch, not after the load resolves.
    vi.mocked(get).mockReturnValue(chat(["t1"]));
    await activate();
    expect(pointTurnRail).toHaveBeenCalledWith("c-1");
    expect(loadMessages).toHaveBeenCalledWith("c-1");
  });

  it("points the rail for a chat the store holds no row for", () => {
    // The re-point sits above the missing-row early return. Called by NAME: `activateChatView`
    // IS the factory's registered hook.
    vi.mocked(get).mockReturnValue(undefined);
    activateChatView("c-1");
    expect(pointTurnRail).toHaveBeenCalledWith("c-1");
  });
});

// The loading skeleton is a PLACEHOLDER and must never paint beside the content it stands
// for, as on a switch back to a loaded chat.

describe("the transcript's loading skeleton", () => {
  function chat(turnIDs: string[]): never {
    return {
      id: "c-1",
      model: "",
      turn_count: Math.max(turnIDs.length, 1),
      turn_order: turnIDs,
      usage: { context_size: 0 },
    } as never;
  }

  // The pair the dispatcher runs: the activation points the views (and is what sets
  // the store's active chat, which the arm's own gate reads), the refresh fetches.
  async function activate(): Promise<void> {
    activateChatView("c-1");
    refreshChatView("c-1");
    await vi.waitFor(() => {
      expect(loadMessages).toHaveBeenCalledWith("c-1");
    });
  }

  it("is not armed for a chat whose transcript is already in the store", async () => {
    vi.mocked(get).mockReturnValue(chat(["t1"]));
    await activate();
    expect(skeletonTiming).not.toHaveBeenCalled();
  });

  it("is armed for a chat with history the store has not fetched yet", async () => {
    // `turn_count` says the conversation exists, `turn_order` says nothing of it is
    // resident — the one state a placeholder is for.
    vi.mocked(get).mockReturnValue(chat([]));
    await activate();
    expect(skeletonTiming).toHaveBeenCalledTimes(1);
  });
});

describe("closeChatTab is the one client-local teardown", () => {
  // `closeChatTab` IS the tab's teardown, called by name. It runs identical local cleanup
  // whatever retention says; teardown and delete are the server's `close_tab`.

  it("cleans up locally with retention ENABLED, and dispatches nothing", () => {
    vi.mocked(get).mockReturnValue({ turn_count: 3 } as never);
    vi.mocked(isRetentionEnabled).mockReturnValue(true);
    closeChatTab("c-closed");
    expect(removeChat).toHaveBeenCalledWith("c-closed");
    expect(dropDecisions).toHaveBeenCalledWith("c-closed");
    // The chat's view is disposed BEFORE the store row goes, so the repaint cannot park it.
    expect(disposeChatView).toHaveBeenCalledWith("c-closed");
    const disposeOrder = vi.mocked(disposeChatView).mock.invocationCallOrder[0] ?? 0;
    const removeOrder = vi.mocked(removeChat).mock.invocationCallOrder[0] ?? 0;
    expect(disposeOrder).toBeLessThan(removeOrder);
    expect(deleteChat.dispatch).not.toHaveBeenCalled();
  });

  it("cleans up locally with retention DISABLED, and still dispatches nothing", () => {
    // Ephemeral: the server deletes the record inside its close; a second delete would race it.
    vi.mocked(get).mockReturnValue({ turn_count: 3 } as never);
    vi.mocked(isRetentionEnabled).mockReturnValue(false);
    closeChatTab("c-ephemeral");
    expect(removeChat).toHaveBeenCalledWith("c-ephemeral");
    expect(deleteChat.dispatch).not.toHaveBeenCalled();
  });

  it("removes a zero-turn chat like any other", () => {
    // The server deletes zero-turn chats like any other; no client branch.
    vi.mocked(get).mockReturnValue({ turn_count: 0 } as never);
    vi.mocked(isRetentionEnabled).mockReturnValue(false);
    closeChatTab("c-empty");
    expect(removeChat).toHaveBeenCalledWith("c-empty");
    expect(deleteChat.dispatch).not.toHaveBeenCalled();
  });

  it("drops the chat's unanswered asks on close, in every retention mode", () => {
    for (const retention of [true, false]) {
      vi.clearAllMocks();
      vi.mocked(get).mockReturnValue({ turn_count: 3 } as never);
      vi.mocked(isRetentionEnabled).mockReturnValue(retention);
      closeChatTab("c-closed");
      expect(dropDecisions).toHaveBeenCalledWith("c-closed");
    }
  });
});

// The tangent: a sub-tab whose context is the parent's REAL context via a session fork.

describe("openTangentChat", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    activeId = "";
    vi.mocked(get).mockReturnValue({ model: "parent-model" } as never);
  });

  // The fork mints the id, so the sub-tab is adopted from its reply, parent included, with no
  // second POST.
  it("adopts the sub-tab from the fork's reply, parent and all, with no second POST", async () => {
    await openTangentChat("c-parent");
    expect(vi.mocked(openTab)).not.toHaveBeenCalled();
    const [subject, name] = vi.mocked(adoptSubject).mock.calls[0] ?? [];
    expect(subject).toMatchObject({
      id: "tb_forked",
      kind: "chat",
      ref: "c-forked",
      parent: "tb_parent",
      owns: true,
    });
    expect(name).toBe("New conversation");
    expect(vi.mocked(activateTab)).toHaveBeenCalledWith("tb_forked");
  });

  // The fork names the parent and carries no chat id; the reply brings it back.
  it("dispatches chat.fork naming the parent, with an op id and no chat id", async () => {
    await openTangentChat("c-parent");
    expect(forkDispatch).toHaveBeenCalledTimes(1);
    const arg = forkDispatch.mock.calls[0]?.[0] as {
      parentChatID: string;
      opID: string;
      chatID?: string;
    };
    expect(arg.parentChatID).toBe("c-parent");
    expect(arg.opID).toMatch(/^op-/);
    expect(arg.chatID).toBeUndefined();
  });

  // No seeded prompt: the fork carries the whole context.
  it("seeds no prompt: the fork carries the context, not a quoted phrase", async () => {
    await openTangentChat("c-parent");
    expect(submitPromptMock).not.toHaveBeenCalled();
  });

  // Model, mode and effort are copied server-side; the row is seeded from the returned header.
  it("seeds the row from the server's header and leaves mode to the server", async () => {
    vi.mocked(get).mockReturnValue({ model: "parent-model", current_mode_id: "plan" } as never);
    await openTangentChat("c-parent");
    expect(vi.mocked(upsertHeader).mock.calls.at(-1)?.[0]).toMatchObject({ id: "c-forked" });
    expect(setModeDispatch).not.toHaveBeenCalled();
  });

  // A refused fork opens nothing: there is no chat to open a tab for, and opening
  // one under a guessed id is the window this whole stage removes.
  it("opens no tab when the fork is refused", async () => {
    forkDispatch.mockResolvedValueOnce(null);
    await openTangentChat("c-parent");
    expect(vi.mocked(adoptSubject)).not.toHaveBeenCalled();
    expect(vi.mocked(activateTab)).not.toHaveBeenCalled();
  });

  it("does nothing when the parent is unknown", async () => {
    vi.mocked(get).mockReturnValue(undefined);
    await openTangentChat("c-parent");
    expect(vi.mocked(adoptSubject)).not.toHaveBeenCalled();
    expect(forkDispatch).not.toHaveBeenCalled();
  });

  it("does nothing for an empty parent id", async () => {
    await openTangentChat("");
    expect(vi.mocked(adoptSubject)).not.toHaveBeenCalled();
    expect(forkDispatch).not.toHaveBeenCalled();
  });
});

// No transcript context menu: a tangent inherits the whole conversation, so a
// phrase-scoped gesture would mislead. The `+` menu is the door.
describe("no transcript context menu", () => {
  it("exports no initTranscriptContextMenu", () => {
    expect(chatModule).not.toHaveProperty("initTranscriptContextMenu");
  });

  // A transcript right-click is the native menu's: asserted on defaultPrevented.
  it("leaves a transcript right-click to the native menu", () => {
    activeId = "c-active";
    const e = new MouseEvent("contextmenu", { bubbles: true, cancelable: true });
    messagesEl.dispatchEvent(e);
    expect(e.defaultPrevented).toBe(false);
  });
});

describe("the chat tab's row effect supplies the title, and no second string", () => {
  /** The opaque id minted for chat `c1`; the row effect reuses ONE `tabIdFor` lookup. */
  const TAB_ID = "tb_c1";

  function driveEffect(over: Record<string, unknown>): void {
    const s = { id: "c1", name: "Fix the parser", ...over };
    // One open tab for c1; its row effect reads the chat through watchSession.
    vi.mocked(openChatRefs).mockReturnValue(["c1"]);
    vi.mocked(watchSession).mockReturnValue(s as never);
    vi.mocked(tabIdFor).mockReturnValue(TAB_ID);
    installStoreSubscribers();
  }

  it("reconciles the row's name from the session", () => {
    driveEffect({ current_mode_id: "plan" });
    expect(renameTab).toHaveBeenCalledWith(TAB_ID, "Fix the parser");
  });

  it("hands the mode to no strip writer", () => {
    driveEffect({ current_mode_id: "plan" });
    const written = [...vi.mocked(renameTab).mock.calls, ...vi.mocked(setTabStatus).mock.calls];
    // The loop is the assertion, so an effect that wrote nothing would leave it
    // with no subject. `requireAssertions` fails that too; this names it.
    expect(written.length).toBeGreaterThan(0);
    for (const call of written) {
      for (const arg of call) {
        // Case-insensitive: the realistic regression folds in the RAW mode id (`plan`).
        expect(String(arg)).not.toMatch(/plan/iu);
      }
    }
  });
});

describe("a superseded activation paints no failure", () => {
  // History activates a chat TWICE and store-load aborts the first fetch by chat id;
  // loadMessages reports that abort like a failure, so the superseded activation must not
  // paint its retry box.
  function loadedChat(): never {
    return {
      id: "c-loaded",
      model: "",
      turn_count: 3,
      turn_order: ["t1"],
      usage: { context_size: 0 },
      has_more: false,
    } as never;
  }

  beforeEach(() => {
    messagesEl.replaceChildren();
  });

  it("shows no retry box when a newer activation superseded the fetch", async () => {
    vi.mocked(get).mockReturnValue(loadedChat());
    // First fetch aborted (false), second one fine — what the two activations
    // produce in production.
    vi.mocked(loadMessages).mockResolvedValueOnce(false).mockResolvedValueOnce(true);

    const row = { chat_id: "c-loaded", session_id: "s1", title: "t", updated_at: 1 };
    openPreviousSession(row);
    openPreviousSession(row);

    await vi.waitFor(() => {
      expect(loadMessages).toHaveBeenCalledTimes(2);
    });
    await Promise.resolve();
    expect(messagesEl.textContent).not.toContain("Failed to load messages");
  });

  it("still shows the retry box when the load genuinely failed", async () => {
    // The guard must not swallow a real failure: one activation, one failure.
    vi.mocked(get).mockReturnValue(loadedChat());
    vi.mocked(loadMessages).mockResolvedValue(false);

    openPreviousSession({ chat_id: "c-loaded", session_id: "s1", title: "t", updated_at: 1 });

    await vi.waitFor(() => {
      expect(messagesEl.textContent).toContain("Failed to load messages");
    });
  });
});

describe("a tab whose chat this device's store does not hold", () => {
  // A truncated /api/chats answer left open tabs with no store row, and the pane kept the
  // previous chat's content with no retry.
  function loadedChat(): never {
    return {
      id: "c-missing",
      model: "",
      turn_count: 3,
      turn_order: ["t1"],
      usage: { context_size: 0 },
      has_more: false,
    } as never;
  }

  beforeEach(() => {
    messagesEl.replaceChildren();
  });

  it("says so instead of leaving a blank pane", async () => {
    vi.mocked(get).mockReturnValue(undefined);
    vi.mocked(loadList).mockResolvedValue(false);

    activateChatView("c-missing");

    expect(messagesEl.textContent).toContain("This conversation is not loaded yet.");
    expect(messagesEl.querySelector("button")?.textContent).toBe("Retry");
  });

  it("re-reads the chat list once and activates the chat when it arrives", async () => {
    // Absent on the first activation, present after the re-read.
    vi.mocked(get).mockReturnValueOnce(undefined).mockReturnValue(loadedChat());
    vi.mocked(loadList).mockResolvedValue(true);

    activateChatView("c-missing");

    await vi.waitFor(() => {
      expect(loadMessages).toHaveBeenCalledWith("c-missing");
    });
    expect(loadList).toHaveBeenCalledTimes(1);
    expect(messagesEl.textContent).not.toContain("This conversation is not loaded yet.");
  });

  it("does not loop when the re-read still does not produce the chat", async () => {
    vi.mocked(get).mockReturnValue(undefined);
    vi.mocked(loadList).mockResolvedValue(true);

    activateChatView("c-missing");

    await vi.waitFor(() => {
      expect(loadList).toHaveBeenCalledTimes(1);
    });
    // A second heal would mean a second read. The affordance stays, which is the
    // honest end state for a chat the server does not report.
    await Promise.resolve();
    expect(loadList).toHaveBeenCalledTimes(1);
    expect(messagesEl.textContent).toContain("This conversation is not loaded yet.");
  });

  it("clears a previous activation's failure box", async () => {
    vi.mocked(get).mockReturnValue(undefined);
    vi.mocked(loadList).mockResolvedValue(false);

    activateChatView("c-missing");
    activateChatView("c-missing");

    expect(messagesEl.querySelectorAll(".load-error")).toHaveLength(1);
  });
});

describe("restore: opening a closed conversation from History", () => {
  // The transcript is FETCHED and the tab opens; every History row carries a chat_id.
  function closedChat(): never {
    return {
      id: "c-closed",
      name: "Yesterday's work",
      model: "",
      turn_count: 12,
      turn_order: [],
      usage: { context_size: 0 },
      has_more: true,
    } as never;
  }

  const row = {
    chat_id: "c-closed",
    session_id: "sess_closed",
    title: "Yesterday's work",
    updated_at: 1,
  };

  beforeEach(() => {
    messagesEl.replaceChildren();
  });

  it("opens the tab and fetches the transcript", async () => {
    vi.mocked(get).mockReturnValue(closedChat());
    openPreviousSession(row);

    // The store already holds the row, so no list refetch is needed.
    expect(loadList).not.toHaveBeenCalled();
    // The tab is what makes it reachable again, and the chat id is its REF: an id
    // is opaque and server-minted, so the door names the subject instead.
    const spec = vi.mocked(openTab).mock.calls.at(-1)?.[0] as { ref: string; name: string };
    expect(spec.ref).toBe("c-closed");
    // The store's name wins over the row's title: the chat record is the
    // authority on its own name, and KAS's copy can be a stale derivation.
    expect(spec.name).toBe("Yesterday's work");
    await vi.waitFor(() => {
      expect(loadMessages).toHaveBeenCalledWith("c-closed");
    });
    expect(messagesEl.textContent).not.toContain("Failed to load messages");
  });

  it("fetches the chat list first when this device dropped the store row", async () => {
    // A chat closed in this page is absent from the store while its file survives, so
    // activating before the header lands renders an empty view.
    vi.mocked(get).mockReturnValue(undefined);
    vi.mocked(loadList).mockResolvedValue(true);
    openPreviousSession(row);

    await vi.waitFor(() => {
      expect(loadList).toHaveBeenCalled();
    });
    await vi.waitFor(() => {
      const spec = vi.mocked(openTab).mock.calls.at(-1)?.[0] as { ref: string } | undefined;
      if (spec?.ref !== "c-closed") {
        throw new Error("tab not opened");
      }
    });
    // The tab opens only AFTER the list lands, or it would activate against the
    // same empty store the guard exists for.
    const listOrder = vi.mocked(loadList).mock.invocationCallOrder[0] ?? 0;
    const tabOrder = vi.mocked(openTab).mock.invocationCallOrder[0] ?? 0;
    expect(listOrder).toBeLessThan(tabOrder);
  });

  it("ignores a row with no owning chat", async () => {
    // Belt and braces: an unclaimed row is never adopted.
    await openPreviousSession({ ...row, chat_id: "" });
    expect(openTab).not.toHaveBeenCalled();
    expect(loadMessages).not.toHaveBeenCalled();
  });

  it("a 404 reopen answers 'gone': ephemeral notice, NO activation", async () => {
    // Retention off and the chat deleted after History listed it: the 404 outcome says so and
    // must not activate.
    vi.mocked(get).mockReturnValue(closedChat());
    vi.mocked(openTab).mockResolvedValue("not-found");

    await expect(openPreviousSession(row)).resolves.toBe("gone");

    // Named by the History row's title, the name the reader clicked.
    expect(vi.mocked(info)).toHaveBeenCalledWith(
      "Yesterday's work: That conversation is gone. It was ephemeral because retention is off.",
    );
    // activateChatView never ran: no rail pointing, no fetch.
    expect(pointTurnRail).not.toHaveBeenCalled();
    expect(loadMessages).not.toHaveBeenCalled();
  });

  it("a network failure answers 'failed', which is NOT the ephemeral face", async () => {
    // A failed fetch must never read as "deleted": the row stays, the framework
    // toast has already spoken, and nothing here claims the chat is gone.
    vi.mocked(get).mockReturnValue(closedChat());
    vi.mocked(openTab).mockResolvedValue("failed");

    await expect(openPreviousSession(row)).resolves.toBe("failed");

    expect(vi.mocked(info)).not.toHaveBeenCalled();
    expect(pointTurnRail).not.toHaveBeenCalled();
    expect(loadMessages).not.toHaveBeenCalled();
  });
});

// The activation refetch gate: stale routes to the fetch, with the rail FORCED after the
// load; fresh means no message fetch, re-wired furniture, and the rail left to its own gate
// (turn-rail.test.ts).

describe("activateChatView routes on the staleness verdict", () => {
  function loadedChat(id: string): never {
    return {
      id,
      name: "seeded",
      model: "",
      turn_order: ["t1"],
      turn_count: 1,
      has_more: false,
      usage: { context_size: 1 },
      draft: "",
    } as never;
  }

  function emptyChat(id: string): never {
    return {
      id,
      name: "seeded",
      model: "",
      turn_order: [],
      turn_count: 0,
      has_more: false,
      usage: { context_size: 1 },
      draft: "",
    } as never;
  }

  it("a fresh window activates with ZERO message fetches", () => {
    vi.mocked(get).mockReturnValue(loadedChat("c-fresh"));
    vi.mocked(transcriptStale).mockReturnValue(false);

    activateChatView("c-fresh");

    expect(loadMessages).not.toHaveBeenCalled();
    // The load-more hook is activation's, not the fetch's.
    expect(setLoadMore).toHaveBeenCalled();
    // The rail is handed the chat WITHOUT force: its own record decides.
    expect(loadTurnRail).toHaveBeenCalledWith("c-fresh");
  });

  // "Load older messages" has one producer, fed by `session.has_more` (store-load.ts).
  // Asserted on the ARGUMENTS: `./scroll.js` is mocked; `scroll.test.ts` owns the rendering.
  it("offers no pagination for a chat with nothing older", () => {
    vi.mocked(get).mockReturnValue(loadedChat("c-whole"));
    vi.mocked(transcriptStale).mockReturnValue(false);

    activateChatView("c-whole");

    expect(setLoadMore).toHaveBeenCalledWith(null, false);
  });

  it("offers pagination for a chat that genuinely has older messages", () => {
    // The other direction, so never offering pagination cannot pass.
    vi.mocked(get).mockReturnValue({
      id: "c-paged",
      name: "seeded",
      model: "",
      turn_order: ["t1"],
      turn_count: 40,
      has_more: true,
      usage: { context_size: 1 },
      draft: "",
    } as never);
    vi.mocked(transcriptStale).mockReturnValue(false);

    activateChatView("c-paged");

    expect(setLoadMore).toHaveBeenCalledWith(expect.any(Function), true);
  });

  it("a stale window refetches messages, then forces the rail behind the load", async () => {
    vi.mocked(get).mockReturnValue(loadedChat("c-stale"));
    vi.mocked(transcriptStale).mockReturnValue(true);

    activateChatView("c-stale");
    refreshChatView("c-stale");

    expect(loadMessages).toHaveBeenCalledWith("c-stale");
    // The rail fetch is sequenced behind the messages fetch resolving.
    expect(loadTurnRail).not.toHaveBeenCalled();
    await vi.mocked(loadMessages).mock.results[0]?.value;
    expect(loadTurnRail).toHaveBeenCalledWith("c-stale", { force: true });
  });

  it("a fresh EMPTY chat skips the draft fetch too", () => {
    // The empty branch's GET exists only to adopt the server-held draft, and a
    // window this device already fetched yielded it; zero fetches means zero.
    vi.mocked(get).mockReturnValue(emptyChat("c-empty-fresh"));
    vi.mocked(transcriptStale).mockReturnValue(false);

    activateChatView("c-empty-fresh");

    expect(loadMessages).not.toHaveBeenCalled();
    expect(loadTurnRail).not.toHaveBeenCalled();
  });

  it("a stale EMPTY chat fetches its record for the draft", () => {
    vi.mocked(get).mockReturnValue(emptyChat("c-empty-stale"));
    vi.mocked(transcriptStale).mockReturnValue(true);

    refreshChatView("c-empty-stale");

    expect(loadMessages).toHaveBeenCalledWith("c-empty-stale");
  });

  it("refreshChatView on a fresh session calls neither setLoadMore nor loadMessages", () => {
    // The furniture is the ACTIVATION's half and the fetch is gated, so a refresh of a
    // view nothing has undermined is a no-op end to end.
    vi.mocked(get).mockReturnValue(loadedChat("c-fresh-refresh"));
    vi.mocked(transcriptStale).mockReturnValue(false);

    refreshChatView("c-fresh-refresh");

    expect(loadMessages).not.toHaveBeenCalled();
    expect(setLoadMore).not.toHaveBeenCalled();
  });

  it("a gap makes the next activation refetch", () => {
    // A gap bumps the sync epoch `transcriptStale` reads, so the second pass answers otherwise.
    vi.mocked(get).mockReturnValue(loadedChat("c-gap"));
    vi.mocked(transcriptStale).mockReturnValue(false);

    activateChatView("c-gap");
    refreshChatView("c-gap");
    expect(loadMessages).not.toHaveBeenCalled();

    vi.mocked(transcriptStale).mockReturnValue(true);
    activateChatView("c-gap");
    refreshChatView("c-gap");

    expect(loadMessages).toHaveBeenCalledTimes(1);
  });

  it("one activation of a stale chat issues exactly ONE loadTurnRail", async () => {
    // The forced rail is the refresh's alone; turn-rail.ts cannot dedupe a second fetch.
    vi.mocked(get).mockReturnValue(loadedChat("c-one-rail"));
    vi.mocked(transcriptStale).mockReturnValue(true);

    activateChatView("c-one-rail");
    refreshChatView("c-one-rail");
    await vi.mocked(loadMessages).mock.results[0]?.value;

    expect(loadTurnRail).toHaveBeenCalledTimes(1);
    expect(loadTurnRail).toHaveBeenCalledWith("c-one-rail", { force: true });
  });

  it("refreshChatView for a NON-active chat fetches and paints no skeleton", () => {
    // The subagent delegation's arm gate: `showSubagent` does not setActive, so a
    // delegated refresh's host is another chat's view or the multiplexer fallback.
    messagesEl.replaceChildren();
    vi.mocked(get).mockReturnValue({
      id: "c-bg",
      name: "seeded",
      model: "",
      turn_order: [],
      turn_count: 4,
      has_more: false,
      usage: { context_size: 1 },
      draft: "",
    } as never);
    vi.mocked(transcriptStale).mockReturnValue(true);

    activateChatView("c-other");
    refreshChatView("c-bg");

    expect(loadMessages).toHaveBeenCalledWith("c-bg");
    expect(skeletonTiming).not.toHaveBeenCalled();
  });
});

// The painter is the ENFORCEMENT and the arm the optimisation, pinned separately through
// `skeletonTiming`'s show callback.

describe("the placeholder's own refusal, against the real container", () => {
  function pendingChat(id: string): never {
    return {
      id,
      name: "seeded",
      model: "",
      turn_order: [],
      turn_count: 6,
      has_more: false,
      usage: { context_size: 1 },
      draft: "",
    } as never;
  }

  /** Drive the show callback the way the 150ms timer would. */
  function paintNow(): void {
    vi.mocked(skeletonTiming).mockImplementationOnce((show) => {
      show();
      return { commit: vi.fn(), cancel: vi.fn() };
    });
  }

  it("a stale window WITH content in the container paints no skeleton", () => {
    messagesEl.replaceChildren();
    const turn = document.createElement("div");
    turn.setAttribute("data-reconcile-key", "turn-1");
    messagesEl.appendChild(turn);
    vi.mocked(get).mockReturnValue(pendingChat("c-has-turns"));
    vi.mocked(transcriptStale).mockReturnValue(true);
    paintNow();

    activateChatView("c-has-turns");
    refreshChatView("c-has-turns");

    expect(messagesEl.children).toHaveLength(1);
    expect(messagesEl.firstElementChild).toBe(turn);
  });

  it("replaces a previous load's failure box rather than shimmering under it", async () => {
    // The one surface that cannot use `mount: "replace"`, so the box must be GONE before the
    // placeholder mounts, with no activation behind the refresh.
    messagesEl.replaceChildren();
    vi.mocked(get).mockReturnValue(pendingChat("c-failed"));
    vi.mocked(transcriptStale).mockReturnValue(true);
    vi.mocked(loadMessages).mockResolvedValue(false);

    activateChatView("c-failed");
    refreshChatView("c-failed");
    await vi.waitFor(() => {
      expect(messagesEl.querySelector(".load-error")).not.toBeNull();
    });

    vi.mocked(loadMessages).mockResolvedValue(true);
    paintNow();
    refreshChatView("c-failed");

    expect(messagesEl.querySelector(".load-error")).toBeNull();
    expect(messagesEl.children).toHaveLength(1);
  });
});

// The three live direct callers of activateChatView bypass `refreshRow`, so each fetches
// itself.

describe("every direct caller of activateChatView still fetches", () => {
  function staleChat(id: string): never {
    return {
      id,
      name: "seeded",
      model: "",
      turn_order: ["t1"],
      turn_count: 1,
      has_more: false,
      usage: { context_size: 1 },
      draft: "",
    } as never;
  }

  it("Retry on the failure box refetches", () => {
    messagesEl.replaceChildren();
    vi.mocked(get).mockReturnValue(undefined);
    vi.mocked(loadList).mockResolvedValue(false);

    activateChatView("c-retry");
    vi.mocked(get).mockReturnValue(staleChat("c-retry"));
    vi.mocked(loadMessages).mockClear();
    messagesEl.querySelector<HTMLButtonElement>(".load-error button")?.click();

    expect(loadMessages).toHaveBeenCalledTimes(1);
    expect(loadMessages).toHaveBeenCalledWith("c-retry");
  });

  it("healMissingChat's tail refetches once the re-read produces the chat", async () => {
    messagesEl.replaceChildren();
    vi.mocked(get).mockReturnValue(undefined);
    vi.mocked(loadList).mockImplementation(async () => {
      vi.mocked(get).mockReturnValue(staleChat("c-healed"));
      return true;
    });

    activateChatView("c-healed");

    await vi.waitFor(() => {
      expect(loadMessages).toHaveBeenCalledWith("c-healed");
    });
    expect(loadMessages).toHaveBeenCalledTimes(1);
  });

  it("openPreviousSession's belt refetches an already-open-and-active chat", async () => {
    vi.mocked(get).mockReturnValue(staleChat("c-resume"));

    await openPreviousSession({
      chat_id: "c-resume",
      session_id: "s1",
      title: "t",
      updated_at: 1,
    });

    expect(loadMessages).toHaveBeenCalledTimes(1);
    expect(loadMessages).toHaveBeenCalledWith("c-resume");
  });
});

// A deep link to a chat with no open tab must open it through `openTab`, like the singleton
// routes. The no-record arm lives in `app.ts`'s router, verified in the live browser.

describe("switchSession", () => {
  it("activates the tab a chat already has, and dispatches no open", async () => {
    vi.mocked(tabIdFor).mockReturnValue("tb_open");
    vi.mocked(getActiveTabId).mockReturnValue("tb_other");

    await switchSession("c-has-tab");

    expect(activateTab).toHaveBeenCalledWith("tb_open");
    expect(openTab).not.toHaveBeenCalled();
  });

  it("OPENS the tab for a chat that has none, through the server door", async () => {
    // Through `open_tab` and the membership coordinator; a local-only row is never minted.
    vi.mocked(tabIdFor).mockReturnValue("");
    vi.mocked(get).mockReturnValue({ id: "c-no-tab", name: "The old chat" } as never);

    await switchSession("c-no-tab");

    expect(activateTab).not.toHaveBeenCalled();
    expect(openTab).toHaveBeenCalledTimes(1);
    expect(vi.mocked(openTab).mock.calls[0]?.[0]).toMatchObject({
      kind: "chat",
      ref: "c-no-tab",
      name: "The old chat",
    });
  });

  it("does neither for the chat that is already active in the active tab", async () => {
    vi.mocked(tabIdFor).mockReturnValue("tb_active");
    vi.mocked(getActiveTabId).mockReturnValue("tb_active");
    // The store mock's `activeId` is what `getActiveId()` answers; `setActive` is
    // its only writer, so pointing it is how this case names the active chat.
    setActive("c-active");

    await switchSession("c-active");

    expect(activateTab).not.toHaveBeenCalled();
    expect(openTab).not.toHaveBeenCalled();
  });
});

// A deep link to a chat the STORE has never heard of: a landed list goes stale, so
// `resolveUnknownChat` ASKS. Pinned: the routing of each verdict and the ordering rule. The
// URL rewrite is `applyRoute`'s, verified in the live browser.

describe("resolveUnknownChat", () => {
  it("OPENS the chat when the server says it exists", async () => {
    // `confirmChatExists` has adopted the header, so the deep link lands on a real transcript.
    vi.mocked(confirmChatExists).mockResolvedValue("exists");
    vi.mocked(tabIdFor).mockReturnValue("");

    expect(await resolveUnknownChat("c-elsewhere")).toBe("opened");
    expect(openTab).toHaveBeenCalledTimes(1);
    expect(vi.mocked(openTab).mock.calls[0]?.[0]).toMatchObject({
      kind: "chat",
      ref: "c-elsewhere",
    });
  });

  it("reports `gone` when the SERVER says the chat is gone, and opens nothing", async () => {
    // The one verdict that licenses the terminal claim: the server itself answered.
    vi.mocked(confirmChatExists).mockResolvedValue("gone");

    expect(await resolveUnknownChat("c-deleted")).toBe("gone");
    expect(openTab).not.toHaveBeenCalled();
    expect(activateTab).not.toHaveBeenCalled();
  });

  it("REFUSES the terminal claim when nobody answered", async () => {
    // A 5xx, dead network or undecodable body: nothing terminal is said.
    vi.mocked(confirmChatExists).mockResolvedValue("unresolved");

    expect(await resolveUnknownChat("c-real")).toBe("unresolved");
    expect(openTab).not.toHaveBeenCalled();
    expect(activateTab).not.toHaveBeenCalled();
  });

  it("prefers a row that appeared WHILE the request was in flight over a `gone`", async () => {
    // A `chat_created` frame landing during the trip is newer than the verdict, hence the
    // post-await store read.
    vi.mocked(confirmChatExists).mockImplementation(async () => {
      vi.mocked(get).mockReturnValue({ id: "c-raced", name: "Landed mid-flight" } as never);
      return "gone";
    });
    vi.mocked(tabIdFor).mockReturnValue("");

    expect(await resolveUnknownChat("c-raced")).toBe("opened");
    expect(vi.mocked(openTab).mock.calls[0]?.[0]).toMatchObject({
      kind: "chat",
      ref: "c-raced",
      name: "Landed mid-flight",
    });
  });
});
