import { defineAction, ActionError } from "./index.js";

/** Copy text to clipboard with success/error toast. */
// eslint-disable-next-line @typescript-eslint/no-invalid-void-type -- void used as generic type argument for action with no args/result
export const copyClipboard = defineAction<string, void>({
  name: "ui.copy_clipboard",
  networkMode: "always",
  run: async (text) => {
    try {
      await navigator.clipboard.writeText(text);
    } catch (e) {
      throw new ActionError("Clipboard unavailable", { code: "clipboard", cause: e });
    }
  },
  success: "Copied",
  error: "Could not copy",
});
