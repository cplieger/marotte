// What the stood-in step stream (`__mocks__/run-chat-steps.ts`) was asked to paint.

import { vi, type MockInstance } from "vitest";
import { createRunStepStream, type RunStepStream } from "../run-chat-steps.js";

function applies(): MockInstance<RunStepStream["apply"]>[] {
  return vi
    .mocked(createRunStepStream)
    .mock.results.flatMap((r) => (r.type === "return" ? [vi.mocked(r.value.apply)] : []));
}

/** The step paths of each paint since the last `clearStepPaints`, stream by stream. */
export function stepPaints(): string[][] {
  return applies().flatMap((apply) => apply.mock.calls.map(([paints]) => [...paints.keys()]));
}

export function clearStepPaints(): void {
  for (const apply of applies()) {
    apply.mockClear();
  }
}
