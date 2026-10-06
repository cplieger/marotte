// The canonical `store.js` mock: Browser Mode links real ESM, so a hand-listed factory fails the
// WHOLE FILE once the graph imports a name it lacks. Spread it and override what a suite drives:
//   vi.mock("../store.js", async () => ({
//     ...(await import("../__test-helpers__/store-mock.js")).storeMock, get: mockGet }));
// `store-mock.test.ts` is the drift guard.
import { vi } from "vitest";
import { computed, SignalMap, type Signal } from "@cplieger/reactive";

import type { Session } from "../types.js";
import type { ToolStatus } from "../wire/types.gen.js";

// A REAL SignalMap: consumers subscribe to the returned signal, so a vi.fn() crashes the first
// suite whose graph wires an effect at import time.
const versionSigs = new SignalMap<number>();

export const storeMock = {
  // Real primitives for the same reason; a signal that never changes is inert.
  messagesVersionOf: (chatID: string): Signal<number> => versionSigs.ensure(chatID, 0),
  activeSession: computed<undefined>(() => undefined),
  bumpMessages: vi.fn(),
  watchActiveId: vi.fn(() => ""),
  // The real answer for a chat with no flushed cause; keeps suites on the full-pass paint path.
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
  // Not the real -1, which is no position for the `reinsertSession` path suites reach.
  indexOfSession: vi.fn(() => 0),
  reinsertSession: vi.fn(),
  removeChat: vi.fn(),
  upsertHeader: vi.fn(),

  // A suite driving the failed-send rescue path overrides it from its own turns.
  hasMessage: vi.fn(() => false),
  hasQueued: vi.fn(() => false),

  isThinking: vi.fn(() => false),
  isEmptyChat: vi.fn(() => false),
  setThinking: vi.fn(),
  setTurnOpen: vi.fn(),
  // LIVENESS IS THE LOG: a resident turn with no `turn_close`, else the header's `live`, else a
  // row stating neither. Never `thinking`, a latch that may not outrank an open turn.
  turnLive: vi.fn(
    (s: Session) =>
      [...s.turns.values()].some((t) => t.closeAt === undefined) ||
      s.turn_open === true ||
      s.provisional === true,
  ),
  derivedHasMore: vi.fn((turnCount: number, residentCount: number) => turnCount > residentCount),
  // EMPTY, like every reader here: a latching `done` would pass a dot assertion production did not.
  outcomeLatch: vi.fn((): "done" | "failed" | "" => ""),
  // A named chat would route an entry frame into a window production never opened.
  chatHoldingTurn: vi.fn(() => ""),

  // IDENTITY: folding `in_progress` onto `aborted` would settle a card no liveness read settled.
  delegateStatusFor: vi.fn((status: ToolStatus) => status),
  // TRUE: the real function answers live for an unknown chat; FALSE ends a delegate's spinner.
  chatTurnLive: vi.fn(() => true),

  tabStatusFor: vi.fn(() => ""),
  runStatusFor: vi.fn(() => ""),
  subagentStatusFor: vi.fn(() => ""),
  setAgentStatus: vi.fn(),
  setWorkingLabel: vi.fn(),

  // Mirrors `internal/marotte`'s derived steer id.
  steerIDFor: vi.fn((messageID: string) => `steer-${messageID}`),
  steerCount: vi.fn(() => 0),
  markSteersCompacted: vi.fn(),
  recordSteerSent: vi.fn(),
  recordSteerQueued: vi.fn(),
  forgetSteer: vi.fn(),
  forgetSteers: vi.fn(),
  dropConfirmedSteers: vi.fn(() => []),
  restoreSteers: vi.fn(),

  // Inert: a suite that drives these imports the real module.
  openTurn: vi.fn(),
  appendEntry: vi.fn(),
  openEntry: vi.fn(),
  applyDelta: vi.fn(),
  sealEntry: vi.fn(),
  markWindowStale: vi.fn(),
  registerTurnRepair: vi.fn(),
  registerRevertReadAbort: vi.fn(),

  // IDENTITY: a fresh object would pass a card assertion against a value nothing wrote.
  settledToolCall: vi.fn((call: unknown) => call),
  foldToolCallDelta: vi.fn((prev: unknown) => prev),
  applyToolProgress: vi.fn((): undefined => undefined),
  republishWindowToolCalls: vi.fn(),

  // The empty case: one claiming a refusal would paint a callout the turn never carried.
  setCodeReferences: vi.fn(),
  codeReferencesFor: vi.fn((): undefined => undefined),
  setLiveRefusal: vi.fn(),
  liveRefusalFor: vi.fn((): undefined => undefined),
  clearLiveTurnFacts: vi.fn(),

  setCurrentMode: vi.fn(),
  setSupervisedMode: vi.fn(),
  setChatInterruptMode: vi.fn(),
  setEffort: vi.fn(),
  setThinkingChoice: vi.fn(),
  setModel: vi.fn(),
  setName: vi.fn(),

  // The shipped numbers, so a suite reasoning about cadence reasons about those.
  EVICT_SWEEP_MS: 5 * 60 * 1000,
  EVICT_IDLE_MS: 30 * 60 * 1000,
  registerEvictionExemption: vi.fn(() => () => {}),
  startEvictionSweep: vi.fn(),
  stopEvictionSweep: vi.fn(),
  evictChatMessages: vi.fn(),

  // TRUE, the always-refetch behaviour suites were written against; override for zero-fetch.
  transcriptStale: vi.fn(() => true),
};
