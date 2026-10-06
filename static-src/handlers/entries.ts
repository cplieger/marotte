// SSE handlers for the entry log, each one call into a store operation. The store owns POSITION
// and every hole rule; no `getActiveId()` gate, since a background chat lands in its own store row.

import { onSSE } from "../bus.js";
import {
  appendEntry,
  applyDelta,
  applyToolProgress,
  get,
  markSteersCompacted,
  openEntry,
  openTurn,
  sealEntry,
  setCodeReferences,
  setLiveRefusal,
  setThinking,
} from "../store.js";
import { requestTurnRange } from "../store-load.js";
import { markGitDirty } from "../git.js";
import { isRepoMutatingKind } from "../tool-schema.js";
import { payloadOf } from "../turns.js";
import type { Entry } from "../types.js";
import type { RefusalInfo } from "../wire/types.gen.js";

onSSE("turn_opened", (chatID, p) => {
  // No `thinking` latch: a turn with no `turn_close` IS the liveness, and this frame is what
  // puts one in the store. The latch below is the render-time convenience.
  if (forRun(p)) {
    return;
  }
  openTurn(chatID, p.entry);
});

onSSE("entry_opened", (chatID, p) => {
  if (forRun(p)) {
    return;
  }
  markTurnLive(chatID);
  openEntry(chatID, p.open);
});

onSSE("entry_delta", (chatID, p) => {
  if (forRun(p)) {
    return;
  }
  markTurnLive(chatID);
  applyDelta(chatID, p.turn, p.entry_id, p.lane, p.n, p.delta);
});

onSSE("entry_sealed", (chatID, p) => {
  if (forRun(p)) {
    return;
  }
  sealEntry(chatID, p.turn, p.entry_id, p.lane, p.seq, p.ts, p.n);
  // The seal is the live carrier: a refusal-tagged chunk opens no entry, it SEALS
  // the lane's open one, so this is the frame the refusal branch publishes.
  stampRefusal(chatID, p.turn, p.refusal);
});

onSSE("entry_appended", (chatID, p) => {
  if (forRun(p)) {
    return;
  }
  appendEntry(chatID, p.entry);
  afterAppend(chatID, p.entry);
});

onSSE("tool_progress", (chatID, p) => {
  if (forRun(p)) {
    return;
  }
  // `undefined` means the window holds no such call, and a card cannot be built from a delta,
  // so the answer is the turn's range read rather than a synthesized create.
  if (applyToolProgress(chatID, p.turn, p) === undefined) {
    console.warn(`tool_progress: ${chatID} ${p.turn} holds no call ${p.tool_call_id}`);
    requestTurnRange(chatID, p.turn);
  }
});

onSSE("code_references", (chatID, p) => {
  // No `forRun` guard: the producer drops a step's or subagent's copy (`foreignSession`,
  // `internal/translate/code_references.go`), so the full list REPLACES. Live only; the footer
  // prefers `turn_close.code_references` once closed.
  setCodeReferences(chatID, p.turn, p.references);
});

/** A frame of a RUN's log, which the run store owns: it arrives with an empty chat id and the
 *  workflow id in the payload, and one `onSSE` registration fans out to every subscriber. */
function forRun(p: { workflow_id?: string }): boolean {
  return p.workflow_id !== undefined && p.workflow_id !== "";
}

/** What an appended entry means BESIDE its position, which the store has already applied. A
 *  TOTAL switch whose default is `never`, so a new kind fails the type check here. */
function afterAppend(chatID: string, entry: Entry): void {
  switch (entry.kind) {
    case "compaction":
      // Arrival order is the whole rule: every row the dock holds NOW was queued before this
      // compaction, so it will be read against a summarized context.
      markSteersCompacted(chatID);
      return;
    case "tool_result": {
      // A repo-mutating call SETTLING is the fact that the tree changed, where a 15-second
      // poll of 54 worktrees was a guess at it; the call also NAMES what it touched.
      const result = payloadOf(entry, "tool_result");
      if (result?.status !== "completed") {
        return;
      }
      // `kind` is the SETTLED value the appender copied off the call, so absent means none.
      if (isRepoMutatingKind(result.kind ?? "")) {
        markGitDirty(mutatedPaths(result));
      }
      return;
    }
    case "turn_open":
    case "turn_bind":
    case "text":
    case "thinking":
    case "tool_call":
    case "steer":
    case "steer_ack":
    case "plan":
    case "compaction_failed":
    case "safety_blocked":
    case "model_switched":
    case "mode_switched":
    case "turn_revert":
    case "reconciled":
    case "turn_close":
      // `steer`'s dock row leaves inside `appendEntry`, in the same store update that seats
      // the note; `turn_close` arrives as `turn_closed` and is the settle handler's.
      return;
    default:
      entry.kind satisfies never;
      return;
  }
}

/** Stamp a turn's live refusal from the seal frame (the store stamps once). A steer-only lane and
 *  an empty lane carry no seal frame and defer to `turn_close.refusal`. */
function stampRefusal(chatID: string, turnID: string, refusal: RefusalInfo | undefined): void {
  if (refusal !== undefined) {
    setLiveRefusal(chatID, turnID, refusal);
  }
}

/** Latch `thinking` on the transition alone: `setThinking(true)` clears the previous turn's
 *  verdicts, so every delta would re-clear them and churn the session signal. ANY entry of a
 *  chat's log latches it, delegate lanes included — a delegate's entries are the parent's. */
function markTurnLive(chatID: string): void {
  if (chatID !== "" && get(chatID)?.thinking === false) {
    setThinking(chatID, true);
  }
}

/** The workspace-relative paths a completed call says it touched. Two sources because neither
 *  is complete alone: `locations` is what a read or a command reports, `diffs[].path` what a
 *  write carries. EMPTY means `markGitDirty` rescans everything, which is the honest answer. */
function mutatedPaths(call: {
  locations?: readonly { path: string }[];
  diffs?: readonly { path: string }[];
}): string[] {
  const paths: string[] = [];
  for (const l of call.locations ?? []) {
    if (l.path !== "") {
      paths.push(l.path);
    }
  }
  for (const d of call.diffs ?? []) {
    if (d.path !== "") {
      paths.push(d.path);
    }
  }
  return paths;
}
