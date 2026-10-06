// Model-refusal callout (kiro-cli 2.13 refusal contract).

import { el } from "@cplieger/reactive";
import type { EntryPrompt, RefusalInfo } from "./types.js";
import { getActive } from "./store.js";
import { switchModel } from "./actions/chat.js";
import { iconEl } from "./icon-el.js";
import { ICON_REFUSAL } from "./icons.js";

const CLS = "refusal-callout";

/** The rewind flow is owned by messages.ts (confirm dialog + the revert); injected here to avoid
 *  a module cycle (messages.ts imports this module for syncRefusal). */
type RewindHandler = (target: EntryPrompt) => void;
let onRewind: RewindHandler | undefined;

export function setRefusalRewindHandler(fn: RewindHandler): void {
  onRewind = fn;
}

/** Ensure `wrap`'s refusal callout matches the turn's refusal. Appends the callout when the turn
 *  was refused, removes it when it wasn't (a rewind can truncate the turn that carried one), and
 *  leaves a mounted callout's metadata alone — it is immutable once stamped. */
export function syncRefusal(
  wrap: HTMLElement,
  refusal?: RefusalInfo,
  rewindTo?: EntryPrompt,
): void {
  const existing = wrap.querySelector<HTMLElement>(`:scope > .${CLS}`);
  if (refusal === undefined) {
    existing?.remove();
    return;
  }
  const callout = existing ?? buildCallout(refusal);
  if (existing === null) {
    wrap.appendChild(callout);
  }
  syncRewind(callout, rewindTo);
}

/** Mount, rebind or withdraw the Rewind action. Withdrawn when the turn carries no prompt of its
 *  own: KAS refuses to revert to anything but a user message, so an agent-initiated refusal has
 *  no address. Rebound on every call for the reason the footer's button is — the value moves
 *  when the projection does. */
function syncRewind(callout: HTMLElement, rewindTo?: EntryPrompt): void {
  const actions = callout.querySelector<HTMLElement>(`:scope > .refusal-actions`);
  if (actions === null) {
    return;
  }
  const existing = actions.querySelector<HTMLButtonElement>(":scope > .refusal-rewind");
  if (rewindTo === undefined) {
    existing?.remove();
    return;
  }
  const btn = existing ?? buildRewindButton();
  if (existing === null) {
    // First in the row: recovering the conversation reads before changing the model.
    actions.prepend(btn);
  }
  btn.onclick = (): void => {
    onRewind?.(rewindTo);
  };
}

function buildRewindButton(): HTMLButtonElement {
  return el(
    "button",
    {
      type: "button",
      className: "btn-small refusal-btn refusal-rewind",
      "data-tooltip": "Discard this prompt and everything after it, then try another approach",
    },
    "Rewind",
  ) as HTMLButtonElement;
}

function buildCallout(refusal: RefusalInfo): HTMLElement {
  const header = el(
    "div",
    { className: "refusal-header" },
    iconEl(ICON_REFUSAL),
    el("span", { className: "refusal-title" }, "The model declined to continue"),
  );
  if (refusal.category !== undefined && refusal.category !== "") {
    header.appendChild(el("span", { className: "refusal-chip" }, refusal.category));
  }

  // The service's own sentence, when it sent one.
  const explanation = refusal.explanation ?? "";
  const body =
    explanation === "" ? null : el("p", { className: "refusal-explanation" }, explanation);

  const actions = el("div", { className: "refusal-actions" });

  const rec = refusal.recommended_model ?? "";
  if (rec !== "") {
    const switchBtn = el(
      "button",
      {
        type: "button",
        className: "btn-small refusal-btn",
        "data-tooltip": `Switch this chat to ${rec}`,
      },
      `Switch to ${rec}`,
    ) as HTMLButtonElement;
    switchBtn.addEventListener("click", () => {
      const session = getActive();
      if (session === undefined) {
        return;
      }
      switchBtn.disabled = true;
      void switchModel.dispatch({ chatID: session.id, model: rec }).finally(() => {
        switchBtn.disabled = false;
      });
    });
    actions.appendChild(switchBtn);
  }

  const callout = el(
    "div",
    { className: CLS, role: "note", "aria-label": "Model refusal" },
    header,
  );
  if (body !== null) {
    callout.appendChild(body);
  }
  callout.appendChild(actions);
  return callout;
}
