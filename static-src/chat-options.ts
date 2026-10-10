// The chat-actions menu, and the test a new row has to pass: a pill earns its prompt-row
// slot by changing per MESSAGE, which none of these do. Every row is built here;
// static/index.html carries an empty card. Actions first, the two switches last.

import { el, effect } from "@cplieger/reactive";
import { $ } from "./dom.js";
import { activeSession, isThinking, isEmptyChat } from "./store.js";
import { makeExpandable, collapseAll } from "./pill-expand.js";
import { iconEl } from "./icon-el.js";
import {
  compactChat,
  downloadKiroSession,
  MAX_CHAT_NAME_UNITS,
  renameChat,
  setInterruptMode,
  setSupervised,
} from "./actions/chat.js";
import { copyClipboard } from "./actions/messages.js";
import { downloadChatExport } from "./chat-export.js";
import { buildPath } from "./route-path.js";
import { ICON_EXPORT, ICON_LINK } from "./icons.js";
import { openFilePicker } from "./files-picker.js";
import { uploadLimitHint } from "./upload-policy.js";
import { openTangentChat } from "./chat.js";
import { mergeTangentChat } from "./tangent-merge.js";
import { submitPrompt } from "./submit.js";
import * as toast from "./toast.js";
import { chatNotice } from "./notice-subject.js";
import type { InterruptMode, Session } from "./types.js";

// Local constants rather than icons.ts entries: each is used once, by this module, and `icons.ts`
// is the shared vocabulary.
const ICON_ATTACH =
  '<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M21.44 11.05l-9.19 9.19a6 6 0 01-8.49-8.49l9.19-9.19a4 4 0 015.66 5.66l-9.2 9.19a2 2 0 01-2.83-2.83l8.49-8.48"/></svg>';
const ICON_GOAL =
  '<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="9"/><circle cx="12" cy="12" r="5"/><circle cx="12" cy="12" r="1" fill="currentColor"/></svg>';
const ICON_TANGENT =
  '<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><line x1="6" y1="3" x2="6" y2="15"/><circle cx="18" cy="6" r="3"/><circle cx="6" cy="18" r="3"/><path d="M18 9a9 9 0 01-9 9"/></svg>';
const ICON_MERGE =
  '<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><circle cx="18" cy="18" r="3"/><circle cx="6" cy="6" r="3"/><path d="M6 21V9a9 9 0 009 9"/></svg>';
const ICON_SESSION =
  '<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M21 8v13H3V8"/><path d="M1 3h22v5H1z"/><path d="M10 12h4"/></svg>';
const ICON_RENAME =
  '<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M12 20h9"/><path d="M16.5 3.5a2.12 2.12 0 013 3L7 19l-4 1 1-4z"/></svg>';
const ICON_COMPACT =
  '<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M4 5h16"/><path d="M4 19h16"/><path d="M12 9v-3l-3 3"/><path d="M12 9v-3l3 3"/><path d="M12 15v3l-3-3"/><path d="M12 15v3l3-3"/></svg>';

/**
 * KAS's own clamp on the goal loop's budget, mirrored: `parseGoalCommand` runs
 * `Math.min(Math.max(parseInt(n, 10), 1), 200)`. The default is NOT mirrored: an unset cap
 * omits the suffix, so KAS's parser decides.
 */
const GOAL_MAX_FLOOR = 1;
const GOAL_MAX_CEILING = 200;

let bound = false;

/** Wire the chat actions pill. Idempotent; called once from app.ts. */
export function initChatOptions(): void {
  if (bound) {
    return;
  }
  bound = true;

  const pill = $.chatOptionsBtn;
  const card = $.chatOptionsCard;

  card.append(
    chatStrip(),
    attachRow(),
    goalRow(),
    tangentRow(),
    mergeRow(),
    compactRow(),
    renameRow(),
    interruptRow(),
    supervisedRow(),
  );

  makeExpandable(pill, card, { haspopup: "dialog" });
}

/** How long the Link button reads "Copied" after a copy. */
const COPIED_MS = 1500;

/**
 * The top strip: four one-shot actions on the whole chat, each a glyph plus a visible
 * caption (no tooltips on phones), the full action as its accessible name.
 */
function chatStrip(): HTMLElement {
  const strip = el("div", { className: "chat-opt-strip" });
  const button = (
    icon: string,
    caption: string,
    label: string,
    onClick: (btn: HTMLButtonElement, chatID: string, name: string) => void,
  ): HTMLButtonElement => {
    const cap = el("span", { className: "chat-opt-strip-caption" }, caption);
    const btn = el(
      "button",
      { type: "button", className: "chat-opt-strip-btn", "aria-label": label },
      el("span", { className: "chat-opt-strip-icon" }, iconEl(icon)),
      cap,
    ) as HTMLButtonElement;
    btn.addEventListener("click", () => {
      const session = activeSession.peek();
      if (session === undefined) {
        return;
      }
      onClick(btn, session.id, session.name);
    });
    strip.appendChild(btn);
    return btn;
  };
  const link = button(ICON_LINK, "Link", "Copy link to this chat", (btn, chatID) => {
    const url = location.origin + buildPath({ kind: "chat", id: chatID });
    void copyClipboard.dispatch(url, {
      silent: true,
      onSuccess: () => {
        const cap = btn.querySelector(".chat-opt-strip-caption");
        if (cap !== null) {
          cap.textContent = "Copied";
          setTimeout(() => {
            cap.textContent = "Link";
          }, COPIED_MS);
        }
      },
    });
  });
  const md = button(ICON_EXPORT, "Markdown", "Export chat as Markdown", (_b, chatID, name) => {
    collapseAll();
    downloadChatExport(chatID, name, "md");
  });
  const json = button(ICON_EXPORT, "JSON", "Export chat as JSON", (_b, chatID, name) => {
    collapseAll();
    downloadChatExport(chatID, name, "json");
  });
  const session = button(ICON_SESSION, "Session", "Download Kiro session", (_b, chatID, name) => {
    collapseAll();
    void downloadKiroSession.dispatch({ chatID, name });
  });
  effect(() => {
    const none = activeSession.value === undefined;
    for (const b of [link, md, json, session]) {
      b.disabled = none;
    }
  });
  return strip;
}

/**
 * A menu row that DOES something: glyph, name, hint. Returns the button and hint too, for
 * rows whose availability depends on chat state.
 */
function actionRow(opts: {
  icon: string;
  name: string;
  hint: string;
  onClick: (row: HTMLElement) => void;
}): { row: HTMLElement; btn: HTMLButtonElement; hint: HTMLElement } {
  const hint = el("span", { className: "chat-opt-hint" }, opts.hint);
  const btn = el(
    "button",
    { type: "button", className: "chat-opt-btn" },
    el("span", { className: "chat-opt-icon" }, iconEl(opts.icon)),
    el(
      "span",
      { className: "chat-opt-text" },
      el("span", { className: "chat-opt-name" }, opts.name),
      hint,
    ),
  ) as HTMLButtonElement;
  const row = el("div", { className: "chat-opt-entry" }, btn);
  btn.addEventListener("click", () => {
    opts.onClick(row);
  });
  return { row, btn, hint };
}

/** Collapse FIRST, so the card is not left under the picker's modal. Both calls stay SYNCHRONOUS: an
 *  `await` here would move the picker's open off the user's gesture, whose activation window a file
 *  input cannot ask for again. */
function attachRow(): HTMLElement {
  return actionRow({
    icon: ICON_ATTACH,
    name: "Attach a file",
    // The cap is stated where the choice is made, not discovered as a server 413.
    hint: `Pick a workspace file or upload one (${uploadLimitHint().toLowerCase()})`,
    onClick: () => {
      collapseAll();
      openFilePicker();
    },
  }).row;
}

/** The tangent row's hints; the disabled one names what to do next. */
const TANGENT_HINT = "Branch this conversation into a sub-chat that keeps its context";
const TANGENT_HINT_EMPTY = "Send a message first. A tangent inherits the conversation";

/** Disabled until the chat holds a conversation: a brand-new chat has nothing to fork server-side
 *  (`errForkParentUnknown`), and the failure would arrive after the sub-tab opened. */
function tangentRow(): HTMLElement {
  const { row, btn, hint } = actionRow({
    icon: ICON_TANGENT,
    name: "Start a tangent",
    hint: TANGENT_HINT,
    onClick: () => {
      // Re-read at CLICK time: the card outlives every chat switch. Guards the window between a
      // signal write and the effect below.
      const session = activeSession.peek();
      if (session === undefined) {
        toast.error("Send a message first, then start a tangent from it");
        return;
      }
      if (isEmptyChat(session)) {
        chatNotice(session.id, "Send a message first, then start a tangent from it", "error");
        return;
      }
      collapseAll();
      // Detached: the fork mints the new chat's id and opens its sub-tab when the reply lands.
      void openTangentChat(session.id);
    },
  });

  // A projection of the ACTIVE chat, re-read on tab switch; guarded on the value because
  // `activeSession` re-derives on every chunk.
  effect(() => {
    const empty = isEmptyChat(activeSession.value);
    if (btn.disabled === empty) {
      return;
    }
    btn.disabled = empty;
    hint.textContent = empty ? TANGENT_HINT_EMPTY : TANGENT_HINT;
  });

  return row;
}

const MERGE_HINT = "Summarize this tangent and send its findings to the chat it came from";
const MERGE_HINT_BUSY = "Wait for this turn to finish, then merge";

/** Shown only on a tangent; disabled mid-turn, since the summary has to be the tangent's next
 *  answer (the server refuses a busy tangent too). */
function mergeRow(): HTMLElement {
  const { row, btn, hint } = actionRow({
    icon: ICON_MERGE,
    name: "Merge into parent chat",
    hint: MERGE_HINT,
    onClick: () => {
      // Re-read at CLICK time: the card outlives every chat switch.
      const session = activeSession.peek();
      if (session?.tangent !== true) {
        return;
      }
      if (isThinking(session.id)) {
        chatNotice(session.id, MERGE_HINT_BUSY, "error");
        return;
      }
      collapseAll();
      void mergeTangentChat(session.id);
    },
  });

  // Guarded on the values because `activeSession` re-derives on every streaming chunk.
  effect(() => {
    const session = activeSession.value;
    const shown = session?.tangent === true;
    const busy = session !== undefined && isThinking(session.id);
    const nextHint = busy ? MERGE_HINT_BUSY : MERGE_HINT;
    if (row.classList.contains("hidden") !== !shown) {
      row.classList.toggle("hidden", !shown);
    }
    if (hint.textContent !== nextHint) {
      btn.disabled = busy;
      hint.textContent = nextHint;
    }
  });

  return row;
}

/** The compact row's hints: two disabled variants for `CmdCompact`'s two different 409s. */
const COMPACT_HINT = "Summarize the history so far to free up context";
const COMPACT_HINT_EMPTY = "Send a message first. There is no session to compact yet";
const COMPACT_HINT_BUSY = "Wait for this turn to finish, or cancel it, then compact";

/**
 * Compact this chat's context (the action `/compact` dispatches). Disabled for the two
 * states `CmdCompact` refuses, no live session and a turn in flight. Reports no completion:
 * the server broadcasts `compaction_started` and persists a `compacted` row.
 */
function compactRow(): HTMLElement {
  const { row, btn, hint } = actionRow({
    icon: ICON_COMPACT,
    name: "Compact the context",
    hint: COMPACT_HINT,
    onClick: () => {
      // Re-read at CLICK time: the card outlives every chat switch.
      const session = activeSession.peek();
      if (session === undefined) {
        toast.error("Send a message first, then compact the conversation");
        return;
      }
      if (isEmptyChat(session)) {
        chatNotice(session.id, "Send a message first, then compact the conversation", "error");
        return;
      }
      if (isThinking(session.id)) {
        chatNotice(
          session.id,
          "Wait for this turn to finish, then compact the conversation",
          "error",
        );
        return;
      }
      collapseAll();
      void compactChat.dispatch({ chatID: session.id });
    },
  });

  // A projection of the ACTIVE chat, guarded on the value because `activeSession`
  // re-derives on every streaming chunk.
  effect(() => {
    const session = activeSession.value;
    const empty = isEmptyChat(session);
    const busy = session !== undefined && isThinking(session.id);
    const nextHint = empty ? COMPACT_HINT_EMPTY : busy ? COMPACT_HINT_BUSY : COMPACT_HINT;
    if (hint.textContent === nextHint) {
      return;
    }
    btn.disabled = empty || busy;
    hint.textContent = nextHint;
  });

  return row;
}

/**
 * Set a goal by sending the text KAS's parser claims. With `_meta.kiro.settings.goal`
 * declared (`internal/kascap/table.go`), `session/prompt` runs `parseGoalCommand` and
 * `launchGoal` before the model. The recipe cannot be loaded by source: `launchGoal` applies
 * the cap by mutating its repeat node. The run is parented on this chat. No clear verb:
 * `/goal clear` would be a goal named "clear"; stopping is cancelling the run.
 */
function goalRow(): HTMLElement {
  return actionRow({
    icon: ICON_GOAL,
    name: "Set a goal",
    hint: "The agent iterates toward it until it reports success",
    onClick: openGoalForm,
  }).row;
}

function openGoalForm(row: HTMLElement): void {
  const existing = row.querySelector(".chat-opt-form");
  if (existing !== null) {
    existing.remove();
    return;
  }
  row.appendChild(goalForm());
}

/**
 * The inline form: the objective and an optional loop cap, inline like the Workflows tab's
 * recipe inputs.
 */
function goalForm(): HTMLElement {
  const description = el("input", {
    type: "text",
    className: "chat-opt-input",
    placeholder: "Make the test suite pass",
    "aria-label": "Goal",
  }) as HTMLInputElement;
  // A text field with a numeric keypad, not type="number", which would put KAS's bounds in the
  // browser's hands; owning them makes the clamp and drop real for pasted values too.
  const cap = el("input", {
    type: "text",
    className: "chat-opt-input",
    inputMode: "numeric",
    "aria-label": "Max iterations",
  }) as HTMLInputElement;

  const form = el(
    "form",
    { className: "chat-opt-form" },
    el("label", { className: "chat-opt-input-label" }, "Goal", description),
    el("label", { className: "chat-opt-input-label" }, "Max iterations, optional", cap),
    el("button", { type: "submit", className: "btn-small" }, "Set goal"),
  );

  form.addEventListener("submit", (e: Event) => {
    e.preventDefault();
    // Resolved at SUBMIT time, never captured when the card was built: the card
    // is built once at init and outlives every chat switch.
    const id = activeSession.peek()?.id ?? "";
    if (id === "") {
      toast.error("Open a chat first, then set a goal in it");
      return;
    }
    const objective = description.value.trim();
    if (objective === "") {
      // A bare `/goal` returns null from parseGoalCommand and would reach the model as prose.
      chatNotice(id, "Describe the goal before setting it", "error");
      return;
    }
    if (isThinking(id)) {
      // Mid-turn Send means STEER, and `_session/steer` never reaches parseGoalCommand.
      chatNotice(id, "Wait for this turn to finish, then set the goal", "error");
      return;
    }
    form.remove();
    collapseAll();
    void submitPrompt(id, goalCommand(objective, cap.value));
  });
  form.addEventListener("keydown", (e: KeyboardEvent) => {
    // Escape closes the form, not the card; the card's own Escape fires once the form is gone.
    if (e.key === "Escape") {
      e.stopPropagation();
      form.remove();
    }
  });
  return form;
}

/**
 * Compose exactly what `parseGoalCommand` accepts: `/\s+--max\s+(\d+)$/` against the body,
 * so the suffix is LAST and digits only; a non-integer cap is DROPPED, or it would become
 * part of the goal. An absent cap omits it.
 */
function goalCommand(objective: string, cap: string): string {
  const command = `/goal ${objective}`;
  const raw = cap.trim();
  if (raw === "") {
    return command;
  }
  const n = Number(raw);
  if (!Number.isInteger(n)) {
    return command;
  }
  const bounded = Math.min(Math.max(n, GOAL_MAX_FLOOR), GOAL_MAX_CEILING);
  return `${command} --max ${bounded}`;
}

/** The same command as the tab row's in-place field; this door exists because on a phone the strip
 *  is a closed drawer. */
function renameRow(): HTMLElement {
  return actionRow({
    icon: ICON_RENAME,
    name: "Rename chat",
    hint: "Give this chat a name of your own",
    onClick: (row) => {
      const existing = row.querySelector(".chat-opt-form");
      if (existing !== null) {
        existing.remove();
        return;
      }
      row.appendChild(renameForm());
    },
  }).row;
}

function renameForm(): HTMLElement {
  const current = activeSession.peek()?.name ?? "";
  const input = el("input", {
    type: "text",
    className: "chat-opt-input",
    maxLength: MAX_CHAT_NAME_UNITS,
    value: current,
    "aria-label": "Chat name",
  }) as HTMLInputElement;
  const form = el(
    "form",
    { className: "chat-opt-form" },
    el("label", { className: "chat-opt-input-label" }, "Name", input),
    el("button", { type: "submit", className: "btn-small" }, "Rename"),
  );
  form.addEventListener("submit", (e: Event) => {
    e.preventDefault();
    const session = activeSession.peek();
    if (session === undefined) {
      toast.error("Open a chat first, then rename it");
      return;
    }
    const name = input.value.trim();
    if (name === "") {
      chatNotice(session.id, "Type a name first", "error");
      return;
    }
    form.remove();
    collapseAll();
    if (name !== session.name) {
      void renameChat.dispatch({ chatID: session.id, name });
    }
  });
  form.addEventListener("keydown", (e: KeyboardEvent) => {
    if (e.key === "Escape") {
      e.stopPropagation();
      form.remove();
    }
  });
  queueMicrotask(() => {
    input.select();
  });
  return form;
}

/**
 * A row that SWITCHES a per-chat setting: a label wrapping a checkbox, which mirrors the ACTIVE
 * chat's recorded value (the menu is a projection, so switching tabs re-reads rather than
 * remembers). With no chat there is nothing to record on, so the visual resets.
 */
function switchRow(opts: {
  id: string;
  name: string;
  hint: string;
  read: (s: Session) => boolean;
  write: (chatID: string, on: boolean) => void;
}): HTMLElement {
  const box = el("input", { type: "checkbox", id: opts.id }) as HTMLInputElement;
  box.addEventListener("change", () => {
    const chatID = activeSession.peek()?.id ?? "";
    if (chatID === "") {
      box.checked = false;
      return;
    }
    opts.write(chatID, box.checked);
  });
  effect(() => {
    const s = activeSession.value;
    box.checked = s !== undefined && opts.read(s);
  });
  return el(
    "label",
    { className: "chat-opt-row", for: opts.id },
    box,
    el(
      "span",
      { className: "chat-opt-text" },
      el("span", { className: "chat-opt-name" }, opts.name),
      el("span", { className: "chat-opt-hint" }, opts.hint),
    ),
  );
}

/** What Send means while a turn runs on this chat: on is queue, off (the default) is steer. */
function interruptRow(): HTMLElement {
  return switchRow({
    id: "chat-opt-queue",
    name: "Queue messages",
    hint: "Send waits for the current turn to end. Off, the agent reads it mid-turn",
    read: (s) => s.interrupt_mode === "queue",
    write: (chatID, on) => {
      const mode: InterruptMode = on ? "queue" : "steer";
      void setInterruptMode.dispatch({ chatID, mode });
    },
  });
}

/** Supervised mode. The default for new chats lives in Settings → Permissions. */
function supervisedRow(): HTMLElement {
  return switchRow({
    id: "chat-opt-supervised",
    name: "Supervised mode",
    hint: "Review this chat's file changes at the end of each turn",
    read: (s) => s.supervised_mode === true,
    write: (chatID, enabled) => {
      void setSupervised.dispatch({ chatID, enabled });
    },
  });
}
