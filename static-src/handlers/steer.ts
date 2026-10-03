// A ROW `steer_queued` confirms the row its id names (the sending device drew it at
// submit, so the optimistic and confirmed rows share the derived id); a BATCH frame
// (`replaces`) says KAS holds several rows under one id and adds no row. A row leaves
// the dock on its `steer` entry, inside `appendEntry`.

import { onSSE } from "../bus.js";
import { recordSteerQueued } from "../store.js";
import { info, success, error } from "../toast.js";

onSSE("steer_queued", (chatID, p) => {
  recordSteerQueued(chatID, {
    id: p.steer_id,
    text: p.text,
    origin: p.origin,
    replaces: p.replaces,
    state: p.state,
  });
});

// A toast: nobody can discard a notice and it has no later state, and the step's
// output already lands in its delegated-work block.
onSSE("agent_notice", (_chatID, p) => {
  const text = p.text.trim();
  if (text === "") {
    return;
  }
  switch (p.severity) {
    case "success":
      success(text);
      return;
    case "warning":
    case "error":
      // Both take the error face — warning is closer to error than aside.
      error(text);
      return;
    default:
      info(text);
  }
});
