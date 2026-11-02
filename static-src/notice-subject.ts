// What a toast is about: a chat-scoped toast names its chat and offers Open when
// that tab is not on screen, `run:<id>` names its run, and "" names nothing.

import { error, notice, success, type ToastJump, type ToastRetry } from "./toast.js";
import type { NoticeLevel } from "./wire/types.gen.js";
import { get } from "./store.js";
import { activateTab, getActiveTabId, openTab, tabIdFor } from "./tabs.js";
import { truncate } from "./strings.js";
import { runLabelOf } from "./run-store.js";
import { apiGetOrError } from "./api-client.js";

/** A chat is named from its first prompt (an 80-char cut server-side), which is a
 *  paragraph opener rather than a title, so the prefix keeps its leading words. */
const MAX_NAME_CHARS = 40;

/** One word, because the button sits on the message's own line in a 384px card. */
const OPEN_LABEL = "Open";

const RUN_PREFIX = "run:";

/** A run this client has fetched nothing for, the same words its tab reads as. */
const FALLBACK_RUN_NAME = "Workflow run";

interface NoticeSubject {
  /** The display name to lead the message with; "" names nothing. */
  readonly name: string;
  /** The jump to the subject's tab, when it has one that is not on screen. */
  readonly open: ToastRetry | undefined;
}

/** The display name `subjectID` has now, for a caller capturing it where its notice
 *  or action begins: a later rename or a removed row then cannot change what it says. */
export function subjectName(subjectID: string): string {
  if (subjectID.startsWith(RUN_PREFIX)) {
    return runLabelOf(subjectID.slice(RUN_PREFIX.length));
  }
  return subjectID === "" ? "" : (get(subjectID)?.name ?? "");
}

/** Resolve the subject of a toast keyed by `chatID`. The test for "on screen" is the
 *  TAB, not the store's active chat, which nothing clears when the reader moves to
 *  Settings or an editor. A captured `chatName` (`subjectName`) outranks the store. */
export function noticeSubject(chatID: string, chatName = ""): NoticeSubject {
  if (chatID === "") {
    return { name: "", open: undefined };
  }
  if (chatID.startsWith(RUN_PREFIX)) {
    return {
      name: truncate(
        chatName || runLabelOf(chatID.slice(RUN_PREFIX.length)) || FALLBACK_RUN_NAME,
        MAX_NAME_CHARS,
      ),
      open: undefined,
    };
  }
  const name = truncate(chatName !== "" ? chatName : (get(chatID)?.name ?? ""), MAX_NAME_CHARS);
  const tabID = tabIdFor("chat", chatID);
  if (tabID === "") {
    // A retained chat with no tab is opened; one the store does not hold is gone.
    if (get(chatID) === undefined) {
      return { name, open: undefined };
    }
    return {
      name,
      open: {
        label: OPEN_LABEL,
        onClick: () => {
          void openTab({ kind: "chat", ref: chatID, name: get(chatID)?.name ?? "" });
        },
      },
    };
  }
  if (tabID === getActiveTabId()) {
    return { name, open: undefined };
  }
  return {
    name,
    open: {
      label: OPEN_LABEL,
      onClick: () => {
        activateTab(tabID);
      },
    },
  };
}

/** The jump a run's end toast offers: onto `parentChatID`, the chat that launched the run, or onto
 *  the run's own tab when that id is "" or the store no longer holds the chat. Undefined when the
 *  target is the tab on screen. The click reads the store again. */
export function runEndJump(
  workflowID: string,
  parentChatID: string,
  runName: string,
): ToastJump | undefined {
  const chat = parentChatID === "" ? undefined : get(parentChatID);
  const tabID = chat === undefined ? tabIdFor("run", workflowID) : tabIdFor("chat", parentChatID);
  if (tabID !== "" && tabID === getActiveTabId()) {
    return undefined;
  }
  const target = chat === undefined ? runName : chat.name;
  return {
    label: OPEN_LABEL,
    name: target === "" ? OPEN_LABEL : `${OPEN_LABEL} ${truncate(target, MAX_NAME_CHARS)}`,
    onClick: () => openRunEnd(workflowID, parentChatID, runName),
  };
}

async function openRunEnd(
  workflowID: string,
  parentChatID: string,
  runName: string,
): Promise<void> {
  const chat = parentChatID === "" ? undefined : get(parentChatID);
  if (chat !== undefined) {
    const tabID = tabIdFor("chat", parentChatID);
    if (tabID !== "") {
      activateTab(tabID);
      return;
    }
    // A 404 falls back to the run tab; any other refusal has raised its own toast and ends here.
    const opened = await openTab({
      kind: "chat",
      ref: parentChatID,
      name: chat.name,
      quietNotFound: true,
    });
    if (opened !== "not-found") {
      return;
    }
  }
  // A launched run can be deleted with its chat, and no frame tells this client it was.
  if (parentChatID !== "" && (await runIsGone(workflowID))) {
    return;
  }
  // Lazy: run-view.ts is the run page, which this module's many importers must not pull in.
  const { openRunView } = await import("./run-view.js");
  await openRunView(workflowID, runName);
}

/** True only when the server answers 404 for the run: any other failure is left for the open to report. */
async function runIsGone(workflowID: string): Promise<boolean> {
  const r = await apiGetOrError<unknown>(`/api/runs/${encodeURIComponent(workflowID)}`);
  return r.status === 404;
}

export function named(subject: NoticeSubject, message: string): string {
  return subject.name !== "" ? `${subject.name}: ${message}` : message;
}

export function chatNotice(
  chatID: string,
  message: string,
  level: NoticeLevel | "success" = "info",
  chatName = "",
): () => void {
  const subject = noticeSubject(chatID, chatName);
  return notice(named(subject, message), level, subject.open);
}

/** The toast of an action about `subjectID`. A retry outranks Open, because the retry
 *  is offered nowhere else and the chat is one click away in the tab strip. */
export function actionNotice(
  subjectID: string,
  message: string,
  level: "success" | "error",
  retry?: ToastRetry,
  name = "",
): void {
  const subject = noticeSubject(subjectID, name);
  const text = named(subject, message);
  if (subject.open === undefined) {
    if (level === "success") {
      success(text);
    } else {
      error(text, retry);
    }
    return;
  }
  if (level === "error" && retry !== undefined) {
    error(text, retry);
    return;
  }
  notice(text, level, subject.open);
}
