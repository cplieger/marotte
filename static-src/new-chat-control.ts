import { createChat } from "./actions/chat.js";
import { guardDuplicateActivation } from "./platform.js";
import { bindLoadingSpinner } from "./spinner.js";

/** Wire the New chat button: `create` runs once per activation, and the button shows its busy face
 *  from any create's dispatch to its answer, whichever door dispatched it. */
export function initNewChatControl(btn: HTMLButtonElement, create: () => void): void {
  btn.addEventListener("click", guardDuplicateActivation(create));
  bindLoadingSpinner(createChat.name, btn);
}
