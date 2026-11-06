// The typed-command table. Small on purpose, and what matters is the BOUNDARY:
// everything the table does not claim must fall through untouched, or marotte
// starts silently swallowing text KAS itself parses.
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import type * as Persist from "./persist.js";
import { settingsPayload } from "./__test-helpers__/settings.js";

const dispatch = vi.fn();
vi.mock("./actions/chat.js", () => ({
  compactChat: {
    dispatch: (args: unknown) => {
      dispatch(args);
    },
  },
}));

const toasts: string[] = [];
const toastChats: string[] = [];
vi.mock("./notice-subject.js", () => ({
  chatNotice: (chatID: string, msg: string) => {
    toastChats.push(chatID);
    toasts.push(msg);
    return () => undefined;
  },
}));

// `shared` routes loadSettings to the real shared read.
const memoryMode = vi.hoisted(() => ({ value: "off" as string | null, shared: false }));
vi.mock("./persist.js", async (importOriginal) => {
  const real = await importOriginal<typeof Persist>();
  return {
    ...real,
    loadSettings: vi.fn(() =>
      memoryMode.shared
        ? real.loadSettings()
        : Promise.resolve(memoryMode.value === null ? null : { memory_mode: memoryMode.value }),
    ),
  };
});
const applyRoute = vi.hoisted(() => vi.fn(() => Promise.resolve()));
vi.mock("./route-apply.js", () => ({ applyRoute }));
const createSession = vi.hoisted(() => vi.fn(() => Promise.resolve("c-new")));
vi.mock("./chat.js", () => ({ createSession }));
const owners = vi.hoisted(() => ({
  openModelCard: vi.fn(),
  pickModel: vi.fn(),
  openModeCard: vi.fn(),
  openContextCard: vi.fn(),
  openSetting: vi.fn(),
  setComposerValue: vi.fn(),
  models: [] as { model_id: string; model_name: string }[],
}));
vi.mock("./model-switcher.js", () => ({
  openModelCard: owners.openModelCard,
  pickModel: owners.pickModel,
}));
vi.mock("./role-picker.js", () => ({ openModeCard: owners.openModeCard }));
vi.mock("./context-card.js", () => ({ openContextCard: owners.openContextCard }));
vi.mock("./settings-highlight.js", () => ({ openSetting: owners.openSetting }));
vi.mock("./picker.js", () => ({ getCachedModels: () => owners.models }));
vi.mock("./composer-value.js", () => ({ setComposerValue: owners.setComposerValue }));

const { handleTypedCommand, terminalCommandRefusal, MAROTTE_COMMANDS } =
  await import("./typed-commands.js");
const store = await import("./store.js");
const persist = await import("./persist.js");

beforeEach(() => {
  dispatch.mockReset();
  toasts.length = 0;
  toastChats.length = 0;
});

describe("handleTypedCommand", () => {
  function seedConversation(): void {
    store.setSessions([
      {
        id: "c1",
        name: "test",
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
        turn_count: 1,
        has_more: false,
        thinking: false,
        working_label: "Thinking",
      },
    ]);
  }

  it("claims /compact and dispatches the native verb", () => {
    seedConversation();
    expect(handleTypedCommand("c1", "/compact")).toBe(true);
    expect(dispatch).toHaveBeenCalledWith({ chatID: "c1" });
  });

  it("tolerates surrounding whitespace and case", () => {
    seedConversation();
    expect(handleTypedCommand("c1", "  /COMPACT  ")).toBe(true);
    expect(dispatch).toHaveBeenCalledTimes(1);
  });

  it("refuses /compact mid-turn with the menu row's notice and dispatches nothing", () => {
    seedConversation();
    store.setThinking("c1", true);
    expect(handleTypedCommand("c1", "/compact")).toBe(true);
    expect(dispatch).not.toHaveBeenCalled();
    expect(toasts).toEqual(["Wait for this turn to finish, then compact the conversation"]);
  });

  it("refuses /compact on a chat with no conversation", () => {
    store.setSessions([]);
    expect(handleTypedCommand("c1", "/compact")).toBe(true);
    expect(dispatch).not.toHaveBeenCalled();
    expect(toasts).toEqual(["Send a message first, then compact the conversation"]);
  });

  // `/compact` is a command; `/compact this file` is a sentence that begins with
  // one, and guessing which was meant is how a table like this starts lying.
  it("does not claim a verb with arguments", () => {
    expect(handleTypedCommand("c1", "/compact this file")).toBe(false);
    expect(dispatch).not.toHaveBeenCalled();
  });

  it.each([
    ["ordinary prose", "please compact the context"],
    ["a bare slash", "/"],
    ["an unknown verb", "/frobnicate"],
    ["a path that looks like a command", "/usr/bin/env"],
    ["empty", ""],
  ])("falls through for %s", (_desc, text) => {
    expect(handleTypedCommand("c1", text)).toBe(false);
    expect(dispatch).not.toHaveBeenCalled();
  });

  // Slash text KAS parses itself must reach KAS. marotte deliberately does not
  // enumerate that list — it is KAS's and it moves.
  it("leaves other slash commands to KAS", () => {
    expect(handleTypedCommand("c1", "/goal")).toBe(false);
    expect(dispatch).not.toHaveBeenCalled();
  });

  it("does not claim a name the table only inherits", () => {
    expect(handleTypedCommand("c1", "/constructor")).toBe(false);
    expect(handleTypedCommand("c1", "/toString")).toBe(false);
  });

  it("refuses /tangent on a chat with no conversation", () => {
    expect(handleTypedCommand("c-none", "/tangent")).toBe(true);
    expect(toasts).toEqual(["Send a message first, then start a tangent from it."]);
    expect(toastChats).toEqual(["c-none"]);
  });

  it("refuses without a chat", () => {
    expect(handleTypedCommand("", "/compact")).toBe(false);
    expect(dispatch).not.toHaveBeenCalled();
  });
});

describe("handleTypedCommand /memories", () => {
  beforeEach(() => {
    applyRoute.mockClear();
  });

  it("says Memory is off instead of opening Docs", async () => {
    memoryMode.value = "off";
    expect(handleTypedCommand("c1", "/memories")).toBe(true);
    await vi.waitFor(() => {
      expect(toasts).toEqual([
        "Memory is off. Turn it on in Settings > General to review memories.",
      ]);
    });
    expect(toastChats).toEqual(["c1"]);
    expect(applyRoute).not.toHaveBeenCalled();
  });

  it.each([
    ["Memory is on", "learn"],
    ["the settings cannot be read", null],
  ])("opens Docs on Memories when %s", async (_desc, mode) => {
    memoryMode.value = mode;
    expect(handleTypedCommand("c1", "/memories")).toBe(true);
    await vi.waitFor(() => {
      expect(applyRoute).toHaveBeenCalledWith({ kind: "docs", tab: "memories" });
    });
    expect(toasts).toEqual([]);
  });
});

// The real shared read, the network held: a read older than settings the page already holds must not decide.
describe("handleTypedCommand /memories over the shared settings read", () => {
  let gets: ((revision: number, mode: string) => void)[] = [];

  beforeEach(() => {
    applyRoute.mockClear();
    gets = [];
    persist.__testResetTracking();
    memoryMode.shared = true;
    vi.stubGlobal(
      "fetch",
      vi.fn(
        () =>
          new Promise<Response>((resolve) => {
            gets.push((revision, mode) => {
              resolve(
                Response.json(settingsPayload({ settings_revision: revision, memory_mode: mode })),
              );
            });
          }),
      ),
    );
  });

  afterEach(() => {
    vi.unstubAllGlobals();
    memoryMode.shared = false;
  });

  it("decides from the read sent after a newer settings event, not the older one it joined", async () => {
    void persist.loadSettings();
    persist.noteSettingsEvent({ settings_epoch: 0, settings_revision: 2 });
    void persist.refreshSettings();
    expect(handleTypedCommand("c1", "/memories")).toBe(true);
    await vi.waitFor(() => {
      expect(gets).toHaveLength(1);
    });
    gets[0]?.(1, "learn");
    await vi.waitFor(() => {
      expect(gets).toHaveLength(2);
    });
    gets[1]?.(2, "off");
    await vi.waitFor(() => {
      expect(toasts).toEqual([
        "Memory is off. Turn it on in Settings > General to review memories.",
      ]);
    });
    expect(applyRoute).not.toHaveBeenCalled();
  });

  it("opens Memories as for an unread setting when the read answers below the held revision", async () => {
    expect(persist.adoptSettings(settingsPayload({ settings_revision: 2 }))).toBe(true);
    expect(handleTypedCommand("c1", "/memories")).toBe(true);
    await vi.waitFor(() => {
      expect(gets).toHaveLength(1);
    });
    gets[0]?.(1, "off");
    await vi.waitFor(() => {
      expect(applyRoute).toHaveBeenCalledWith({ kind: "docs", tab: "memories" });
    });
    expect(toasts).toEqual([]);
  });
});

// `/drop` is the one verb that asks the engine for nothing. That is its whole
// contract, so the tests assert BOTH halves: the composer leaves steer mode, and
// no command travels.
describe("handleTypedCommand /drop", () => {
  function seedThinking(...ids: string[]): void {
    store.setSessions(
      ids.map((id) => ({
        id,
        name: "test",
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
      })),
    );
    store.setActive(ids[0] ?? "");
    for (const id of ids) {
      store.setThinking(id, true);
    }
  }

  it("refuses /rewind mid-turn", () => {
    seedThinking("c1");
    expect(handleTypedCommand("c1", "/rewind")).toBe(true);
    expect(toasts).toEqual(["Rewind is unavailable while the agent is working."]);
    expect(toastChats).toEqual(["c1"]);
  });

  it("returns the composer to prompt mode by clearing thinking", () => {
    seedThinking("c1");
    expect(store.isBusy("c1")).toBe(true);
    expect(handleTypedCommand("c1", "/drop")).toBe(true);
    // This is what makes the next Send a PROMPT rather than a steer: submit.ts
    // branches on exactly this read.
    expect(store.isBusy("c1")).toBe(false);
  });

  // A turn the client holds open with no close (one the agent started, or one whose close never
  // arrived) is what `isBusy` reads besides `thinking`, so `/drop` has to waive it too.
  it("returns the composer to prompt mode beside an open turn that never streamed", () => {
    seedThinking("c1");
    store.setThinking("c1", false);
    store.openTurn("c1", {
      id: "t-wake-open",
      turn: "t-wake",
      kind: "turn_open",
      seq: 0,
      ts: 1,
      payload: { source: "wire_turn_start", n: 1 },
    });
    expect(store.isBusy("c1")).toBe(true);
    expect(handleTypedCommand("c1", "/drop")).toBe(true);
    expect(store.isBusy("c1")).toBe(false);
    expect(toasts.at(-1)).toContain("not told to stop");
  });

  it("dispatches no command, so it needs nothing from the engine", () => {
    seedThinking("c1");
    handleTypedCommand("c1", "/drop");
    expect(dispatch).not.toHaveBeenCalled();
  });

  it("says the agent was not told to stop", () => {
    seedThinking("c1");
    handleTypedCommand("c1", "/drop");
    expect(toasts.at(-1)).toContain("not told to stop");
  });

  it("claims the verb on an idle chat rather than sending it to the model", () => {
    seedThinking("c1");
    store.setThinking("c1", false);
    expect(handleTypedCommand("c1", "/drop")).toBe(true);
    expect(toasts.at(-1)).toContain("No turn is running");
  });

  it("touches only the named chat", () => {
    seedThinking("c1", "c2");
    handleTypedCommand("c2", "/drop");
    expect(store.isBusy("c1")).toBe(true);
    expect(store.isBusy("c2")).toBe(false);
  });

  it("does not claim /drop with arguments", () => {
    seedThinking("c1");
    expect(handleTypedCommand("c1", "/drop this turn")).toBe(false);
    expect(store.isThinking("c1")).toBe(true);
  });
});

describe("handleTypedCommand /tangent merge", () => {
  function seed(tangent: boolean): void {
    store.setSessions([
      {
        id: "c1",
        name: "test",
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
        turn_count: 1,
        has_more: false,
        thinking: false,
        working_label: "Thinking",
        tangent,
      },
    ]);
    store.setActive("c1");
  }

  it("claims the two-word verb with any spacing, and explains itself on an ordinary chat", () => {
    seed(false);
    expect(handleTypedCommand("c1", "/tangent   MERGE")).toBe(true);
    expect(toasts).toEqual([
      "This chat has no parent chat to merge into. Start a tangent with /tangent.",
    ]);
  });

  it("refuses mid-turn rather than queueing a summary behind the turn", () => {
    seed(true);
    store.setThinking("c1", true);
    expect(handleTypedCommand("c1", "/tangent merge")).toBe(true);
    expect(toasts).toEqual(["Wait for this turn to finish, then merge."]);
  });

  it("still leaves a sentence that starts with the verb alone", () => {
    seed(true);
    expect(handleTypedCommand("c1", "/tangent merge it all")).toBe(false);
  });
});

describe("handleTypedCommand /clear", () => {
  beforeEach(() => {
    createSession.mockClear();
  });

  it("claims /clear and opens a new chat", async () => {
    expect(handleTypedCommand("c1", "/clear")).toBe(true);
    await vi.waitFor(() => {
      expect(createSession).toHaveBeenCalledTimes(1);
    });
  });

  it("claims /clear mid-turn and leaves the running turn alone", async () => {
    store.setSessions([
      {
        id: "c1",
        name: "test",
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
        turn_count: 1,
        has_more: false,
        thinking: false,
        working_label: "Thinking",
      },
    ]);
    store.setThinking("c1", true);
    expect(handleTypedCommand("c1", "/CLEAR")).toBe(true);
    await vi.waitFor(() => {
      expect(createSession).toHaveBeenCalledTimes(1);
    });
    expect(store.isThinking("c1")).toBe(true);
    expect(dispatch).not.toHaveBeenCalled();
  });

  it("does not claim /clear with arguments", async () => {
    expect(handleTypedCommand("c1", "/clear the cache")).toBe(false);
    await Promise.resolve();
    expect(createSession).not.toHaveBeenCalled();
  });
});

// kiro-cli's inspect commands open the control that already draws their state.
describe("handleTypedCommand inspect verbs", () => {
  function seed(thinking: boolean): void {
    store.setSessions([
      {
        id: "c1",
        name: "test",
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
        turn_count: 1,
        has_more: false,
        thinking: false,
        working_label: "Thinking",
      },
    ]);
    store.setThinking("c1", thinking);
  }

  beforeEach(() => {
    for (const fn of [
      owners.openModelCard,
      owners.pickModel,
      owners.openModeCard,
      owners.openContextCard,
      owners.openSetting,
      owners.setComposerValue,
      applyRoute,
    ]) {
      fn.mockClear();
    }
    owners.models = [
      { model_id: "claude-sonnet-4.5", model_name: "Claude Sonnet 4.5" },
      { model_id: "auto", model_name: "Auto" },
    ];
    seed(false);
  });

  it("opens the model card on /model and sends nothing", async () => {
    expect(handleTypedCommand("c1", "/model")).toBe(true);
    await vi.waitFor(() => {
      expect(owners.openModelCard).toHaveBeenCalledTimes(1);
    });
    expect(owners.pickModel).not.toHaveBeenCalled();
    expect(dispatch).not.toHaveBeenCalled();
    expect(toasts).toEqual([]);
  });

  it("opens the mode card on /agent", async () => {
    expect(handleTypedCommand("c1", "/Agent")).toBe(true);
    await vi.waitFor(() => {
      expect(owners.openModeCard).toHaveBeenCalledTimes(1);
    });
  });

  it.each(["/context", "/usage", "/stats"])("opens the context card on %s", async (text) => {
    expect(handleTypedCommand("c1", text)).toBe(true);
    await vi.waitFor(() => {
      expect(owners.openContextCard).toHaveBeenCalledTimes(1);
    });
    expect(toasts).toEqual([]);
  });

  // The composer clears its box right after a consumed send, so the `/` lands after that clear.
  it("puts / in the composer on /help, after the send clears the box", async () => {
    expect(handleTypedCommand("c1", "/help")).toBe(true);
    owners.setComposerValue("");
    await vi.waitFor(() => {
      expect(owners.setComposerValue.mock.calls).toEqual([[""], ["/"]]);
    });
  });

  it("opens Settings on Permissions for /tools", async () => {
    expect(handleTypedCommand("c1", "/tools")).toBe(true);
    await vi.waitFor(() => {
      expect(applyRoute).toHaveBeenCalledWith({ kind: "settings", tab: "permissions" });
    });
  });

  it("opens MCP integrations in Settings > Tools for /mcp", async () => {
    expect(handleTypedCommand("c1", "/mcp")).toBe(true);
    await vi.waitFor(() => {
      expect(owners.openSetting).toHaveBeenCalledWith("tools", "mcp-section");
    });
  });

  it("picks a listed model on /model <id>, matching the id in any case", async () => {
    expect(handleTypedCommand("c1", "/model  Claude-Sonnet-4.5 ")).toBe(true);
    await vi.waitFor(() => {
      expect(owners.pickModel).toHaveBeenCalledWith("claude-sonnet-4.5");
    });
    expect(owners.openModelCard).not.toHaveBeenCalled();
    expect(toasts).toEqual(["Model set to claude-sonnet-4.5."]);
  });

  it("says a mid-turn /model <id> applies when the turn ends", async () => {
    seed(true);
    expect(handleTypedCommand("c1", "/model auto")).toBe(true);
    await vi.waitFor(() => {
      expect(owners.pickModel).toHaveBeenCalledWith("auto");
    });
    expect(toasts).toEqual(["The model switches to auto when this turn ends."]);
  });

  it("leaves /model <unlisted> to its refusal, which names the id and keeps the text", () => {
    expect(handleTypedCommand("c1", "/model gpt-9")).toBe(false);
    expect(owners.pickModel).not.toHaveBeenCalled();
    expect(terminalCommandRefusal("/model gpt-9")).toEqual({
      notice: "No model named gpt-9 is in the model list, so this was not sent to the agent.",
      keepText: true,
    });
  });

  it("does not match a model name, only its id", () => {
    expect(handleTypedCommand("c1", "/model Claude Sonnet 4.5")).toBe(false);
    expect(owners.pickModel).not.toHaveBeenCalled();
  });

  it("leaves /agent create foo to its refusal, which keeps the text", () => {
    expect(handleTypedCommand("c1", "/agent create foo")).toBe(false);
    expect(owners.openModeCard).not.toHaveBeenCalled();
    expect(terminalCommandRefusal("/agent create foo")).toEqual({
      notice:
        "marotte's /agent takes no arguments, so this was not sent to the agent. Switch modes and agents with the mode pill in the prompt bar.",
      keepText: true,
    });
  });
});

describe("terminalCommandRefusal", () => {
  it("names marotte's control for a kiro-cli command it has one for", () => {
    expect(terminalCommandRefusal("/effort")).toEqual({
      notice:
        "marotte has no /effort, so it was not sent to the agent. Switch the model and reasoning effort with the model pill in the prompt bar.",
      keepText: false,
    });
    expect(terminalCommandRefusal("  /HOOKS ")?.notice).toContain("Hooks tab of Kiro docs");
  });

  it("says only that it was not sent when marotte has no counterpart", () => {
    expect(terminalCommandRefusal("/feedback")?.notice).toBe(
      "marotte has no /feedback, so it was not sent to the agent.",
    );
  });

  it.each([
    ["/effort high", "effort"],
    ["/chat resume", "chat"],
    ["/Knowledge  add docs", "knowledge"],
  ])("refuses %s by its first word and keeps the text", (text, name) => {
    const refusal = terminalCommandRefusal(text);
    expect(refusal?.notice).toMatch(new RegExp(`^marotte has no /${name}, so it was not sent`));
    expect(refusal?.keepText).toBe(true);
  });

  it.each([
    ["/agent swap x", "agent"],
    ["/context add file", "context"],
    ["/usage --billing", "usage"],
    ["/stats reset", "stats"],
    ["/help me", "help"],
    ["/tools trust-all", "tools"],
    ["/MCP list", "mcp"],
  ])("refuses %s, an argument form of a verb marotte serves bare", (text, name) => {
    const refusal = terminalCommandRefusal(text);
    expect(refusal?.notice).toMatch(
      new RegExp(`^marotte's /${name} takes no arguments, so this was not sent`),
    );
    expect(refusal?.keepText).toBe(true);
  });

  it("leaves the argument form of a verb that takes sentences to the agent", () => {
    expect(terminalCommandRefusal("/compact this file")).toBeNull();
  });

  it.each([
    ["a name kiro-cli does not define", "/frobnicate"],
    ["a command KAS parses", "/goal"],
    ["prose", "help me with this"],
    ["prose that names a command later", "please run /model"],
    ["a name the table only inherits", "/constructor"],
    ["a path", "/model/weights.bin"],
    ["a bare slash", "/"],
  ])("answers nothing for %s", (_desc, text) => {
    expect(terminalCommandRefusal(text)).toBeNull();
  });

  it("never shadows a verb marotte serves", () => {
    for (const { name } of MAROTTE_COMMANDS) {
      expect(terminalCommandRefusal(`/${name}`), name).toBeNull();
    }
  });
});
