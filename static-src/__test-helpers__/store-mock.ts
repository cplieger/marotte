// The canonical `store.js` mock: every store export in one place. Browser Mode links
// real ESM, so a factory listing them by hand fails the WHOLE FILE the moment the graph
// imports a name it lacks — three action suites died that way at once.
// Spread it and override what a suite drives:
//   vi.mock("../store.js", async () => ({
//     ...(await import("../__test-helpers__/store-mock.js")).storeMock, get: mockGet }));
// `store-mock.test.ts` is the drift guard, in both directions.
import { vi } from "vitest";
import { computed, SignalMap, type Signal } from "@cplieger/reactive";

import type { Session } from "../types.js";
import type { ToolStatus } from "../wire/types.gen.js";

// A REAL SignalMap: consumers read the returned signal's `.value` and subscribe to it,
// so a vi.fn() crashes the first suite whose graph wires an effect at import time.
const versionSigs = new SignalMap<number>();

export const storeMock = {
  // The reactive exports are real primitives for that same reason; a signal that never
  // changes is inert instead.
  messagesVersionOf: (chatID: string): Signal<number> => versionSigs.ensure(chatID, 0),
  activeSession: computed<undefined>(() => undefined),
  bumpMessages: vi.fn(),
  watchActiveId: vi.fn(() => ""),
  // `shape` is the real module's answer for a chat with no flushed cause, and keeps a
  // spreading suite on the full-pass paint path.
  renderCauseOf: vi.fn(() => ({ cause: "shape" as const })),

  MODEL_CONTEXT_SIZES: {} as Record<string, number>,
  parseContextSize: vi.fn((): number | undefined => undefined),
  contextSizeFor: vi.fn(() => 0),
  defaultUsage: vi.fn(() => ({})),

  getSessions: vi.fn(() => []),
  getActiveId: vi.fn(() => ""),
  getActive: vi.fn((): undefined => undefined),
  get: vi.fn((): undefined => undefined),
  watchSession: vi.fn((): undefined => undefined),
  setSessions: vi.fn(),
  setActive: vi.fn(),
  // The head of the list rather than the real function's -1, which is not a position for
  // the `reinsertSession` path a spreading suite reaches this from.
  indexOfSession: vi.fn(() => 0),
  reinsertSession: vi.fn(),
  removeChat: vi.fn(),
  upsertHeader: vi.fn(),

  // The acceptance test a failed send reads before handing text back to the composer, so
  // a suite that drives the rescue path overrides it from its own turns.
  hasMessage: vi.fn(() => false),

  isThinking: vi.fn(() => false),
  isEmptyChat: vi.fn(() => false),
  setThinking: vi.fn(),
  setTurnOpen: vi.fn(),
  // LIVENESS IS THE LOG: a resident turn with no `turn_close`, else the header's own
  // `live`, else a row that states neither. Never `thinking`, which is a latch and may
  // not outrank an open turn.
  turnLive: vi.fn(
    (s: Session) =>
      [...s.turns.values()].some((t) => t.closeAt === undefined) ||
      s.turn_open === true ||
      s.provisional === true,
  ),
  // A pure function of what it is handed, so there is nothing to fake.
  derivedHasMore: vi.fn((turnCount: number, residentCount: number) => turnCount > residentCount),
  // The EMPTY value, like every other reader here: one latching `done` would make a dot
  // assertion pass for a reason production did not supply.
  outcomeLatch: vi.fn((): "done" | "failed" | "" => ""),
  // One naming a chat would route an entry frame into a window production never opened.
  chatHoldingTurn: vi.fn(() => ""),

  // The stale-delegate fold answers IDENTITY, for `settledToolCall`'s reason one section
  // down: it takes the status the caller already holds, and a mock folding `in_progress`
  // onto `aborted` would settle a card no production liveness read had settled.
  delegateStatusFor: vi.fn((status: ToolStatus) => status),
  // TRUE, because absence claims nothing: the real function answers live for a chat it
  // holds no session for, and the FALSE direction is the one that ends a delegate's spinner.
  chatTurnLive: vi.fn(() => true),

  tabStatusFor: vi.fn(() => ""),
  runStatusFor: vi.fn(() => ""),
  subagentStatusFor: vi.fn(() => ""),
  setAgentStatus: vi.fn(),
  setWorkingLabel: vi.fn(),

  // Mirrors `internal/marotte`'s derived id, so an assertion on a steer id gets the real
  // shape rather than a placeholder.
  steerIDFor: vi.fn((messageID: string) => `steer-${messageID}`),
  steerCount: vi.fn(() => 0),
  pendingSteerCarry: vi.fn(() => []),
  markSteersCompacted: vi.fn(),
  recordSteerSent: vi.fn(),
  recordSteerQueued: vi.fn(),
  forgetSteer: vi.fn(),
  forgetSteers: vi.fn(),
  dropSteers: vi.fn(),
  dropConfirmedSteers: vi.fn(() => []),
  restoreSteers: vi.fn(),

  // The five entry operations and the window's repair doors, inert: a suite that drives
  // them imports the real module.
  openTurn: vi.fn(),
  appendEntry: vi.fn(),
  openEntry: vi.fn(),
  applyDelta: vi.fn(),
  sealEntry: vi.fn(),
  markWindowStale: vi.fn(),
  registerTurnRepair: vi.fn(),
  registerRevertReadAbort: vi.fn(),

  // The pure folds answer IDENTITY, because each takes the call the caller already holds:
  // a fresh object would make a card assertion pass against a value nothing wrote.
  settledToolCall: vi.fn((call: unknown) => call),
  foldToolCallDelta: vi.fn((prev: unknown) => prev),
  applyToolProgress: vi.fn((): undefined => undefined),
  republishWindowToolCalls: vi.fn(),

  // The per-turn live facts, both readers on the empty case: one claiming a refusal would
  // paint a callout the turn never carried.
  setCodeReferences: vi.fn(),
  codeReferencesFor: vi.fn((): undefined => undefined),
  setLiveRefusal: vi.fn(),
  liveRefusalFor: vi.fn((): undefined => undefined),
  clearLiveTurnFacts: vi.fn(),

  setCurrentMode: vi.fn(),
  setSupervisedMode: vi.fn(),
  setEffort: vi.fn(),
  setModel: vi.fn(),
  setName: vi.fn(),

  // The two constants are the shipped numbers, because a suite reasoning about cadence
  // should reason about those; the registration returns a real unregister.
  EVICT_SWEEP_MS: 5 * 60 * 1000,
  EVICT_IDLE_MS: 30 * 60 * 1000,
  registerEvictionExemption: vi.fn(() => () => {}),
  startEvictionSweep: vi.fn(),
  stopEvictionSweep: vi.fn(),
  evictChatMessages: vi.fn(),

  // Defaults TRUE — the always-refetch behaviour every spreading suite was written
  // against; a suite exercising the zero-fetch activation overrides it.
  transcriptStale: vi.fn(() => true),
};
