import { apiAction } from "./index.js";

/** Kill the PTY and install a fresh one: terminal.Handler is single-use, so an exited child or a
 *  wedged foreground process leaves a session that never starts again. No success toast (the new
 *  prompt is the feedback); the caller confirms first. */
// eslint-disable-next-line @typescript-eslint/no-invalid-void-type -- void as the args type for an action taking none, per notify.ts
export const restartShell = apiAction<void, { ok?: boolean }>({
  name: "shell.restart",
  request: () => ({ method: "POST", path: "/api/shell/restart" }),
  success: false,
  error: "Shell restart failed",
});
