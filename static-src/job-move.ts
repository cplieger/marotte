// Move to background on a running command's card: one press asks kiro-cli to make the call a background job now,
// rather than at its own delay. kiro-cli decides; a moved card repaints through the frames it sends for the move.

import { el } from "@cplieger/reactive";

import { apiAction } from "./actions/index.js";
import { CHROME_ATTR } from "./chrome-attr.js";
import { shownSettingsSignal } from "./persist.js";
import { toolCallSigKey } from "./store-signals.js";
import type { ToolCall } from "./types.js";
import type { BackgroundJobMoveRequest } from "./wire/types.gen.js";

/** Cards whose move the server answered, keyed `toolCallSigKey`: any answer ends the control for that call. */
const answered = new Set<string>();

// eslint-disable-next-line @typescript-eslint/no-invalid-void-type -- void used as generic type argument for an action with no result
const moveJob = apiAction<{ chatID: string; toolCallID: string }, void>({
  name: "jobs.move",
  request: ({ chatID, toolCallID }) => ({
    method: "POST",
    path: `/api/chats/${encodeURIComponent(chatID)}/jobs`,
    body: { tool_call_id: toolCallID } satisfies BackgroundJobMoveRequest,
  }),
  // The server's words are kiro-cli's refusal or its own failure sentence, each whole.
  error: (_args, err) => err.message,
});

/** Whether the card offers the move: Background jobs on (tracked), a chat's own running shell call that is no job
 *  yet and has not been answered. kiro-cli is the authority; a call it cannot move answers not_movable. */
function movable(tc: ToolCall, chatID: string): boolean {
  return (
    shownSettingsSignal.value?.background_jobs === true &&
    chatID !== "" &&
    tc.kind === "execute" &&
    tc.status === "in_progress" &&
    tc.job === undefined &&
    // A subagent's or a workflow step's call runs in a session with no jobs.
    (tc.agent_subtask_id ?? "") === "" &&
    // A command in marotte's terminal ran before the chat's session took the setting; only kiro-cli's own shell moves.
    (tc.terminal_id ?? "") === "" &&
    !answered.has(toolCallSigKey(chatID, tc.id))
  );
}

/** Show or remove the card's Move to background control for the call's current snapshot. Idempotent; reads the
 *  setting tracked, so a caller inside an effect repaints when it changes. */
export function syncMoveAction(card: HTMLElement, tc: ToolCall, chatID: string): void {
  const header = card.querySelector<HTMLElement>(".tool-header");
  const existing = header?.querySelector<HTMLButtonElement>(".tool-move-bg") ?? null;
  if (header === null || !movable(tc, chatID)) {
    existing?.remove();
    return;
  }
  if (existing !== null) {
    return;
  }
  const title = card.dataset["title"] ?? tc.title;
  const btn = el(
    "button",
    {
      type: "button",
      className: "btn-small tool-move-bg",
      "aria-label": `Move to background: ${title}`,
      [CHROME_ATTR]: "",
    },
    "Move to background",
  ) as HTMLButtonElement;
  const key = toolCallSigKey(chatID, tc.id);
  btn.addEventListener("click", () => {
    btn.disabled = true;
    btn.setAttribute("aria-busy", "true");
    void moveJob.dispatch({ chatID, toolCallID: tc.id }).outcome.then((o) => {
      // A transport failure or a cancel never reached kiro-cli, so the control stays for another press.
      if (o.status === "cancelled" || (o.status === "error" && (o.error.status ?? 0) === 0)) {
        btn.disabled = false;
        btn.removeAttribute("aria-busy");
        return;
      }
      answered.add(key);
      btn.remove();
    });
  });
  header.insertBefore(
    btn,
    header.querySelector(".tool-spinner") ?? header.querySelector(".tool-disclosure"),
  );
}

// deadset:ignore DS1102 -- test seam: clears the answered calls
export function _resetForTest(): void {
  answered.clear();
}
