// Typed-command intercepts: the verbs marotte owns, checked before send. A slash command KAS does
// not parse reaches the MODEL as prose, which answers as though it ran (`/compact` "done" with no
// summarization), so a deterministic verb owns its handler here. slash-menu.ts lists these.

import { compactChat } from "./actions/chat.js";
import { dropTurnLocally, get, isBusy, isEmptyChat } from "./store.js";
import { chatNotice } from "./notice-subject.js";
import { loadSettings } from "./persist.js";
import type { Session } from "./types.js";

/** Returns true when it consumed the input, so the caller must NOT also send it as a prompt. */
type TypedHandler = (chatID: string) => boolean;

/** The table: the verb, case-insensitive after the slash, matched only when the input is the verb
 *  ALONE (`/compact this file` is a sentence). A two-word verb (`/tangent merge`) matches with any
 *  run of spaces between its words. */
interface Verb {
  readonly description: string;
  readonly run: TypedHandler;
}

const HANDLERS: Readonly<Record<string, Verb>> = {
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
  drop: { description: "Stop waiting on a turn that is not answering", run: dropTurn },
  memories: {
    description: "Open the agent's memories",
    run: (chatID) => {
      void openMemories(chatID);
      return true;
    },
  },
  rewind: { description: "Rewind the last turn and its file changes", run: rewindLatest },
  tangent: {
    description: "Start a tangent from this conversation",
    run: (chatID) => {
      if (isEmptyChat(get(chatID))) {
        chatNotice(chatID, "Send a message first, then start a tangent from it.");
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
  if (verb === null || !Object.hasOwn(HANDLERS, verb)) {
    return false;
  }
  return HANDLERS[verb]?.run(chatID) ?? false;
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

const IN_MODE_PILL = "Switch modes and agents with the mode pill in the prompt bar.";
const IN_MODEL_PILL =
  "Switch the model and reasoning effort with the model pill in the prompt bar.";
const IN_CONTEXT_PILL =
  "The context pill in the prompt bar shows this chat's context use and credits.";
const IN_HISTORY = "Open a past chat from History, or start a new one with /clear.";
const IN_PERMISSIONS = "Set what the agent's tools may do in Settings > Permissions.";
const IN_WORKFLOWS = "Start, pause and cancel workflow runs on the Workflows tab of Kiro docs.";
const IN_SETTINGS = "Open Settings from the sidebar.";

/** kiro-cli's own terminal commands that marotte does not serve, each with the control that does
 *  the job here ("" for none). KAS refuses `_kiro.dev/commands/execute`, so without this answer
 *  the name reaches the model as prose and it answers as though the command ran. */
const TERMINAL_ONLY = new Map<string, string>([
  ["agent", IN_MODE_PILL],
  ["autonomous", IN_MODE_PILL],
  ["changelog", ""],
  ["chat", IN_HISTORY],
  ["code", "Turn on a language server in Settings > Tools for code intelligence."],
  ["config", IN_SETTINGS],
  ["context", IN_CONTEXT_PILL],
  ["copy", "Copy a reply with the copy buttons at the foot of its turn."],
  ["disconnect", ""],
  ["editor", ""],
  ["effort", IN_MODEL_PILL],
  ["exit", "To leave this chat, close its tab."],
  ["feedback", ""],
  ["fullscreen", ""],
  ["guide", ""],
  ["help", "Type / to list the commands this chat takes."],
  ["hooks", "Turn hooks on and off on the Hooks tab of Kiro docs."],
  ["knowledge", "Add knowledge bases in Settings > Custom instructions."],
  ["kvim", ""],
  ["lite", ""],
  ["mcp", "Manage MCP servers under MCP integrations in Settings > Tools."],
  ["model", IN_MODEL_PILL],
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
  ["stats", IN_CONTEXT_PILL],
  ["switch", ""],
  ["theme", "Switch the theme from the sidebar."],
  ["title", ""],
  ["tools", IN_PERMISSIONS],
  ["transcript", ""],
  ["tui", ""],
  ["upgrade-agent", ""],
  ["usage", IN_CONTEXT_PILL],
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
export interface TerminalRefusal {
  readonly notice: string;
  readonly keepText: boolean;
}

/** The refusal for text whose first word names a kiro-cli terminal command, or null. The caller
 *  checks KAS's catalog first: a saved prompt or steering doc of the same name is sent, not
 *  refused. */
export function terminalCommandRefusal(text: string): TerminalRefusal | null {
  const m = /^\s*\/(\S+)(\s+\S)?/.exec(text);
  const name = (m?.[1] ?? "").toLowerCase();
  const pointer = TERMINAL_ONLY.get(name);
  if (m === null || pointer === undefined) {
    return null;
  }
  const refusal = `marotte has no /${name}, so it was not sent to the agent.`;
  return {
    notice: pointer === "" ? refusal : `${refusal} ${pointer}`,
    keepText: m[2] !== undefined,
  };
}
