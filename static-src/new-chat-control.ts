import { bindLoadingState } from "./actions/index.js";
import { createChat } from "./actions/chat.js";
import { guardDuplicateActivation } from "./platform.js";

/** Wire the New chat button: `create` runs once per activation, and the button shows its busy face
 *  from any create's dispatch to its answer, whichever door dispatched it. */
export function initNewChatControl(btn: HTMLButtonElement, create: () => void): void {
  btn.addEventListener("click", guardDuplicateActivation(create));
  bindLoadingState(createChat.name, btn, { pendingClass: "btn-loading" });
}
