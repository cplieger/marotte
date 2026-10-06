// Agent-terminal live stream into the tool card that spawned it (a terminal lives for one call).
// terminal_created/terminal_exited render nothing; terminal_exited is subscribed only to learn that
// nothing more will come, releasing an unclaimed hold.

import { onSSE } from "./bus.js";
import { appendTerminalChunk, forgetTerminal } from "./messages-tools.js";
import { registerCleanup } from "./actions/index.js";

export function initTerminalStream(): void {
  const unsubOutput = onSSE("terminal_output", (_chatID, p) => {
    appendTerminalChunk(p.terminal_id, p.data, p.spans ?? [], p.offset);
  });
  const unsubExited = onSSE("terminal_exited", (_chatID, p) => {
    forgetTerminal(p.terminal_id);
  });
  registerCleanup(() => {
    unsubOutput();
    unsubExited();
  });
}
