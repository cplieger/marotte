// Typed-command intercepts: the verbs marotte owns, checked before send. A slash command KAS does
// not parse reaches the MODEL as prose, which answers as though it ran (`/compact` "done" with no
// summarization), so a deterministic verb owns its handler here. slash-menu.ts lists these.

import { compactChat } from "./actions/chat.js";
import { dropTurnLocally, get, isBusy, isEmptyChat } from "./store.js";
import { chatNotice } from "./notice-subject.js";
import { loadSettings } from "./persist.js";
import { getCachedModels } from "./picker.js";
import { setComposerValue } from "./composer-value.js";
import { tangentRefusal } from "./tangent-refusal.js";
import type { Session } from "./types.js";

const IN_MODE_PILL = "Switch modes and agents with the mode pill in the prompt bar.";
const IN_MODEL_PILL =
  "Switch the model and reasoning effort with the model pill in the prompt bar.";
const IN_CONTEXT_PILL =
  "The context pill in the prompt bar shows this chat's context use and credits.";
const IN_PERMISSIONS = "Set what the agent's tools may do in Settings > Permissions.";
const IN_MCP = "Manage MCP servers under MCP integrations in Settings > Tools.";
const IN_HISTORY = "Open a past chat from History, or start a new one with /clear.";
const IN_WORKFLOWS = "Start, pause and cancel workflow runs on the Workflows tab of Kiro docs.";
const IN_SETTINGS = "Open Settings from the sidebar.";

/** Returns true when it consumed the input, so the caller must NOT also send it as a prompt. */
type TypedHandler = (chatID: string) => boolean;

/** The table: the verb, case-insensitive after the slash, matched only when the input is the verb
 *  ALONE (`/compact this file` is a sentence) unless it has `runWithArguments`. A two-word verb
 *  (`/tangent merge`) matches with any run of spaces between its words. */
interface Verb {
  readonly description: string;
  readonly run: TypedHandler;
  /** `/verb <args>`, the first word alone: true when it consumed the input. */
  readonly runWithArguments?: (chatID: string, args: string) => boolean;
  /** The notice refusing an argument form `runWithArguments` did not consume, for a kiro-cli
   *  command name whose argument forms would reach the model as prose. Absent, the argument form
   *  is a sentence and is sent. */
  readonly refuseArguments?: (args: string) => string;
}

/** `refuseArguments` for a verb that takes none; `pointer` names the control for the rest. */
function noArguments(name: string, pointer: string): () => string {
  return () =>
    `marotte's /${name} takes no arguments, so this was not sent to the agent. ${pointer}`;
}

/** `/model <id>`: a model whose id is `id`, ignoring case, switches through the pill's own path. */
function pickTypedModel(chatID: string, id: string): boolean {
  const wanted = id.toLowerCase();
  const model = getCachedModels().find((m) => m.model_id.toLowerCase() === wanted);
  if (model === undefined) {
    return false;
  }
  const modelID = model.model_id;
  chatNotice(
    chatID,
    isBusy(chatID)
      ? `The model switches to ${modelID} when this turn ends.`
      : `Model set to ${modelID}.`,
  );
  // Lazy, as for each pill opener: the pills' modules reach chat.ts, which imports this one.
  void import("./model-switcher.js").then(({ pickModel }) => {
    pickModel(modelID, chatID);
  });
  return true;
}

function openContext(): boolean {
  void import("./context-card.js").then(({ openContextCard }) => {
    openContextCard();
  });
  return true;
}

const HANDLERS: Readonly<Record<string, Verb>> = {
  agent: {
    description: "Pick this chat's mode or custom agent",
    run: () => {
      void import("./role-picker.js").then(({ openModeCard }) => {
        openModeCard();
      });
      return true;
    },
    refuseArguments: noArguments("agent", IN_MODE_PILL),
  },
  clear: {
    description: "Start a fresh chat; this one stays in its tab",
    run: () => {
      // Lazy: chat.ts imports submit.ts, which imports this module.
      void import("./chat.js").then(({ createSession }) => createSession());
      return true;
    },
  },
  compact: {
    description: "Compact the context now",
    run: (chatID) => {
      const refusal = compactRefusal(get(chatID));
      if (refusal !== null) {
        chatNotice(chatID, refusal, "error");
        return true;
      }
      void compactChat.dispatch({ chatID });
      return true;
    },
  },
  context: {
    description: "Show this chat's context use and credits",
    run: openContext,
    refuseArguments: noArguments("context", IN_CONTEXT_PILL),
  },
  drop: { description: "Stop waiting on a turn that is not answering", run: dropTurn },
  help: {
    description: "List the commands this chat takes",
    run: () => {
      // After the composer's own clear of the consumed text, which follows this call.
      queueMicrotask(() => {
        setComposerValue("/");
      });
      return true;
    },
    refuseArguments: noArguments("help", "Type / to list the commands this chat takes."),
  },
  mcp: {
    description: "Manage MCP servers",
    run: () => {
      void import("./settings-highlight.js").then(({ openSetting }) => {
        openSetting("tools", "mcp-section");
      });
      return true;
    },
    refuseArguments: noArguments("mcp", IN_MCP),
  },
  memories: {
    description: "Open the agent's memories",
    run: (chatID) => {
      void openMemories(chatID);
      return true;
    },
  },
  model: {
    description: "Pick the model and reasoning effort, or switch with /model <id>",
    run: () => {
      void import("./model-switcher.js").then(({ openModelCard }) => {
        openModelCard();
      });
      return true;
    },
    runWithArguments: pickTypedModel,
    refuseArguments: (id) =>
      `No model named ${id} is in the model list, so this was not sent to the agent.`,
  },
  rewind: { description: "Rewind the last turn and its file changes", run: rewindLatest },
  stats: {
    description: "Show this chat's context use and credits",
    run: openContext,
    refuseArguments: noArguments("stats", IN_CONTEXT_PILL),
  },
  tangent: {
    description: "Start a tangent from this conversation",
    run: (chatID) => {
      const refusal = tangentRefusal(get(chatID));
      if (refusal !== null) {
        chatNotice(chatID, refusal);
        return true;
      }
      // Lazy: chat.ts imports submit.ts, which imports this module.
      void import("./chat.js").then(({ openTangentChat }) => openTangentChat(chatID));
      return true;
    },
  },
  "tangent merge": {
    description: "Merge this tangent into the chat it came from",
    run: (chatID) => {
      if (get(chatID)?.tangent !== true) {
        chatNotice(
          chatID,
          "This chat has no parent chat to merge into. Start a tangent with /tangent.",
        );
        return true;
      }
      if (isBusy(chatID)) {
        chatNotice(chatID, "Wait for this turn to finish, then merge.");
        return true;
      }
      void import("./tangent-merge.js").then(({ mergeTangentChat }) => mergeTangentChat(chatID));
      return true;
    },
  },
  tools: {
    description: "Set what the agent's tools may do",
    run: () => {
      // Lazy: route-apply reaches the whole page graph.
      void import("./route-apply.js").then(({ applyRoute }) =>
        applyRoute({ kind: "settings", tab: "permissions" }),
      );
      return true;
    },
    refuseArguments: noArguments("tools", IN_PERMISSIONS),
  },
  usage: {
    description: "Show this chat's context use and credits",
    run: openContext,
    refuseArguments: noArguments("usage", IN_CONTEXT_PILL),
  },
};

/** One marotte verb as the `/` menu lists it. */
interface MarotteCommand {
  readonly name: string;
  readonly description: string;
}

/** Every verb the table claims, in table order. */
export const MAROTTE_COMMANDS: readonly MarotteCommand[] = Object.entries(HANDLERS).map(
  ([name, v]) => ({ name, description: v.description }),
);

/** `/memories`: Docs withdraws the tab while Memory is Off, so the verb says why instead of landing
 *  on another tab. An unread setting opens it, as Docs does. */
async function openMemories(chatID: string): Promise<void> {
  const settings = await loadSettings();
  if (settings?.memory_mode === "off") {
    chatNotice(chatID, "Memory is off. Turn it on in Settings > General to review memories.");
    return;
  }
  // Lazy: route-apply reaches the whole page graph.
  const { applyRoute } = await import("./route-apply.js");
  await applyRoute({ kind: "docs", tab: "memories" });
}

/** The two compact refusals the client sees before sending; KAS's own arrive as the server's error. */
export function compactRefusal(s: Session | undefined): string | null {
  if (isEmptyChat(s)) {
    return "Send a message first, then compact the conversation";
  }
  if (s !== undefined && isBusy(s.id)) {
    return "Wait for this turn to finish, then compact the conversation";
  }
  return null;
}

/** `/rewind` and the composer's Ctrl/Cmd+Up: the newest prompt turn, through the footer's own confirm. */
export function rewindLatest(chatID: string): boolean {
  if (isBusy(chatID)) {
    chatNotice(chatID, "Rewind is unavailable while the agent is working.");
    return true;
  }
  void import("./messages.js").then(async ({ rewindLatestTurn }) => {
    if (!(await rewindLatestTurn(chatID))) {
      chatNotice(chatID, "There is no turn to rewind.");
    }
  });
  return true;
}

/**
 * `/drop`: end the turn CLIENT-SIDE and put the composer back in prompt mode, for a turn the
 * engine stopped answering, where Send would steer or queue and Cancel waits on KAS.
 * `dropTurnLocally` writes the store only and asks the engine for nothing. A turn the server
 * refuses prompts beside loses nothing: the next prompt 409s and goes through the chat's busy
 * mode, a steer or a queued row.
 * KAS's steering buffer is NOT cleared; that is `steer_clear`, the chip row's Discard ×.
 */
function dropTurn(chatID: string): boolean {
  if (!isBusy(chatID)) {
    // Already idle. Claimed anyway rather than passed through: sending "/drop"
    // to the model is worse than doing nothing, and the toast says why nothing
    // happened.
    chatNotice(chatID, "No turn is running on this chat.");
    return true;
  }
  dropTurnLocally(chatID);
  chatNotice(chatID, "Dropped the turn locally. The agent was not told to stop.");
  return true;
}

/**
 * Consume a typed command if the table claims it. Everything else falls through unchanged,
 * including slash text KAS parses; that list is KAS's.
 */
export function handleTypedCommand(chatID: string, text: string): boolean {
  if (chatID === "") {
    return false;
  }
  const verb = typedVerb(text);
  if (verb === null) {
    return false;
  }
  if (Object.hasOwn(HANDLERS, verb)) {
    return HANDLERS[verb]?.run(chatID) ?? false;
  }
  const call = verbCall(text);
  if (call === null || !Object.hasOwn(HANDLERS, call.name)) {
    return false;
  }
  return HANDLERS[call.name]?.runWithArguments?.(chatID, call.args) ?? false;
}

/** `/name args`: the first word lowercased and the trimmed rest as typed, or null when either is
 *  missing. */
function verbCall(text: string): { name: string; args: string } | null {
  const m = /^\s*\/(\S+)\s+(\S[\s\S]*?)\s*$/.exec(text);
  if (m === null) {
    return null;
  }
  return { name: (m[1] ?? "").toLowerCase(), args: m[2] ?? "" };
}

/** The verb `text` names alone, lowercased with its inner spaces collapsed, or null. */
function typedVerb(text: string): string | null {
  const trimmed = text.trim();
  if (!trimmed.startsWith("/")) {
    return null;
  }
  const verb = trimmed.slice(1).toLowerCase().replace(/\s+/g, " ");
  return verb === "" ? null : verb;
}

/** kiro-cli's own terminal commands that marotte does not serve, each with the control that does
 *  the job here ("" for none). KAS refuses `_kiro.dev/commands/execute`, so without this answer
 *  the name reaches the model as prose and it answers as though the command ran. */
const TERMINAL_ONLY = new Map<string, string>([
  ["autonomous", IN_MODE_PILL],
  ["changelog", ""],
  ["chat", IN_HISTORY],
  ["code", "Turn on a language server in Settings > Tools for code intelligence."],
  ["config", IN_SETTINGS],
  ["copy", "Copy a reply with the copy buttons at the foot of its turn."],
  ["disconnect", ""],
  ["editor", ""],
  ["effort", IN_MODEL_PILL],
  ["exit", "To leave this chat, close its tab."],
  ["feedback", ""],
  ["fullscreen", ""],
  ["guide", ""],
  ["hooks", "Turn hooks on and off on the Hooks tab of Kiro docs."],
  ["knowledge", "Add knowledge bases in Settings > Custom instructions."],
  ["kvim", ""],
  ["lite", ""],
  ["paste", "Paste or drop files into the message box, or attach them from Chat options."],
  ["permissions", IN_PERMISSIONS],
  ["plan", IN_MODE_PILL],
  ["powers", "Powers are on the Powers tab of Kiro docs."],
  ["prompts", "Saved prompts are on the Prompts tab of Kiro docs. Type / to run one."],
  ["quit", "To leave this chat, close its tab."],
  ["reasoning", IN_MODEL_PILL],
  ["reply", ""],
  ["repo", ""],
  ["sandbox", ""],
  ["session-id", ""],
  ["sessions", IN_HISTORY],
  ["settings", IN_SETTINGS],
  ["spawn", ""],
  ["spec", IN_MODE_PILL],
  ["switch", ""],
  ["theme", "Switch the theme from the sidebar."],
  ["title", ""],
  ["transcript", ""],
  ["tui", ""],
  ["upgrade-agent", ""],
  ["verbosity", ""],
  ["voice", ""],
  ["workflow", IN_WORKFLOWS],
  ["workflow-cancel", IN_WORKFLOWS],
  ["workflow-resume", IN_WORKFLOWS],
  ["workflow-run", IN_WORKFLOWS],
  ["workflow-status", IN_WORKFLOWS],
  ["workflows", IN_WORKFLOWS],
]);

/** A refused kiro-cli terminal command: its notice, and whether arguments followed the name, in
 *  which case the composer keeps the text so the typing is not lost. */
interface TerminalRefusal {
  readonly notice: string;
  readonly keepText: boolean;
}

/** The refusal for text whose first word names a kiro-cli terminal command, or for an argument form
 *  a verb refuses (`refuseArguments`), or null. The caller runs `handleTypedCommand` first and
 *  checks KAS's catalog: a saved prompt or steering doc of the same name is sent, not refused. */
export function terminalCommandRefusal(text: string): TerminalRefusal | null {
  const m = /^\s*\/(\S+)(\s+\S)?/.exec(text);
  if (m === null) {
    return null;
  }
  const name = (m[1] ?? "").toLowerCase();
  const pointer = TERMINAL_ONLY.get(name);
  if (pointer !== undefined) {
    const refusal = `marotte has no /${name}, so it was not sent to the agent.`;
    return {
      notice: pointer === "" ? refusal : `${refusal} ${pointer}`,
      keepText: m[2] !== undefined,
    };
  }
  const call = verbCall(text);
  if (call === null || !Object.hasOwn(HANDLERS, call.name)) {
    return null;
  }
  const refuse = HANDLERS[call.name]?.refuseArguments;
  return refuse === undefined ? null : { notice: refuse(call.args), keepText: true };
}
