// The step stream as a run view test stands it in, through `vi.mock("./run-chat-steps.js")`: the
// real one links the whole transcript stack. Each stream's `apply` records its paints and writes
// each painted step's entry count into that step's host.

import { vi } from "vitest";
import type * as RunChatSteps from "../run-chat-steps.js";

export const createRunStepStream = vi.fn<typeof RunChatSteps.createRunStepStream>((hostFor) => ({
  apply: vi.fn<RunChatSteps.RunStepStream["apply"]>((paints) => {
    for (const [path, list] of paints) {
      const p = document.createElement("p");
      p.textContent = String(list.reduce((n, x) => n + x.turn.body.length, 0));
      hostFor(path).replaceChildren(p);
    }
  }),
  dispose: vi.fn<RunChatSteps.RunStepStream["dispose"]>(),
}));
