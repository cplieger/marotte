// Which turns are open: three overriding layers, and their order is the design.
import { describe, it, expect, beforeEach } from "vitest";

import {
  isTurnOpen,
  setTurnOpen,
  openForSearch,
  clearSearchOpened,
  resetFoldState,
} from "./fold-state.js";
import type { Turn, TurnOutcome } from "./turns.js";

function turn(id: string, outcome: TurnOutcome = "completed"): Turn {
  return {
    id,
    n: 1,
    trigger: undefined,
    body: [],
    openEntries: new Map(),
    ts: 0,
    outcome,
    rewindTo: undefined,
  };
}

function turns(n: number): Turn[] {
  return Array.from({ length: n }, (_, i) => turn(`t${String(i)}`));
}

beforeEach(() => {
  localStorage.clear();
  resetFoldState();
});

describe("the automatic rule", () => {
  // A turn auto-collapses when the next turn starts, not before.
  it("keeps exactly the last turn open", () => {
    const list = turns(6);
    const open = list.map((t, i) => isTurnOpen("c1", t, i, list.length));
    expect(open).toEqual([false, false, false, false, false, true]);
  });

  it("folds the first of two turns", () => {
    const list = turns(2);
    expect(list.map((t, i) => isTurnOpen("c1", t, i, list.length))).toEqual([false, true]);
  });

  it("keeps a single turn open", () => {
    expect(isTurnOpen("c1", turn("only"), 0, 1)).toBe(true);
  });
});

describe("outcome and position", () => {
  // A broken turn never auto-folds.
  it.each(["failed", "interrupted", "refused"] as const)(
    "keeps a settled %s turn open wherever it sits",
    (outcome) => {
      const list = [turn("bad", outcome), ...turns(10)];
      expect(isTurnOpen("c1", list[0]!, 0, list.length)).toBe(true);
    },
  );

  // The two stopped outcomes fold: a requested cancel and an unmeasured stop are not failures.
  it.each(["cancelled", "unknown"] as const)("still auto-folds a %s turn", (outcome) => {
    const list = [turn("stopped", outcome), ...turns(10)];
    expect(isTurnOpen("c1", list[0]!, 0, list.length)).toBe(false);
  });

  it("keeps a running turn open wherever it sits", () => {
    const list = [turn("live", "running"), ...turns(10)];
    expect(isTurnOpen("c1", list[0]!, 0, list.length)).toBe(true);
  });

  it("still folds a completed turn in the same position", () => {
    const list = [turn("fine"), ...turns(10)];
    expect(isTurnOpen("c1", list[0]!, 0, list.length)).toBe(false);
  });

  // An active turn cannot collapse; the rule outranks an explicit override.
  it("ignores a recorded fold while the turn is running", () => {
    const t = turn("live", "running");
    setTurnOpen("c1", t.id, false);
    expect(isTurnOpen("c1", t, 0, 10)).toBe(true);
  });

  // A reader's fold of a settled failure holds.
  it("lets the reader fold a failed turn anyway", () => {
    const t = turn("bad", "failed");
    setTurnOpen("c1", t.id, false);
    expect(isTurnOpen("c1", t, 0, 10)).toBe(false);
  });
});

describe("the reader's own choice", () => {
  it("opens an old turn and keeps it open", () => {
    const list = turns(6);
    setTurnOpen("c1", list[0]!.id, true);
    expect(isTurnOpen("c1", list[0]!, 0, list.length)).toBe(true);
  });

  // The newest turn cannot fold; a stale recorded fold must not strand the tail closed.
  it("keeps the newest turn open even with a recorded fold", () => {
    const list = turns(6);
    setTurnOpen("c1", list[5]!.id, false);
    expect(isTurnOpen("c1", list[5]!, 5, list.length)).toBe(true);
  });

  it("applies a recorded fold once the turn is no longer newest", () => {
    const list = turns(7);
    setTurnOpen("c1", list[5]!.id, false);
    expect(isTurnOpen("c1", list[5]!, 5, list.length)).toBe(false);
  });

  it("persists across a fresh read of the module's state", () => {
    const list = turns(6);
    setTurnOpen("c1", list[0]!.id, true);
    // Overrides are per chat.
    expect(isTurnOpen("c2", list[0]!, 0, list.length)).toBe(false);
    expect(isTurnOpen("c1", list[0]!, 0, list.length)).toBe(true);
  });

  // Overrides survive a fresh module read; the store is bounded by chat count with oldest-first eviction.
  it("comes back from localStorage after the module is reset", () => {
    const list = turns(6);
    setTurnOpen("c1", list[0]!.id, true);
    resetFoldState();
    expect(isTurnOpen("c1", list[0]!, 0, list.length)).toBe(true);
  });

  it("keeps a chat's overrides out of another chat's storage", () => {
    const list = turns(6);
    setTurnOpen("c1", list[0]!.id, true);
    setTurnOpen("c2", list[1]!.id, true);
    resetFoldState();
    expect(isTurnOpen("c1", list[0]!, 0, list.length)).toBe(true);
    expect(isTurnOpen("c2", list[1]!, 1, list.length)).toBe(true);
    expect(isTurnOpen("c1", list[1]!, 1, list.length)).toBe(false);
  });
});

describe("search reveal", () => {
  it("opens a turn holding a hit", () => {
    const list = turns(6);
    openForSearch("c1", list[0]!.id);
    expect(isTurnOpen("c1", list[0]!, 0, list.length)).toBe(true);
  });

  // A search must not permanently rearrange the transcript.
  it("re-folds when the search closes", () => {
    const list = turns(6);
    openForSearch("c1", list[0]!.id);
    expect(clearSearchOpened("c1")).toBe(true);
    expect(isTurnOpen("c1", list[0]!, 0, list.length)).toBe(false);
  });

  // A hand-opened turn is left alone: isTurnOpen consults the persisted override before the search set.
  it("leaves a hand-opened turn open after the search closes", () => {
    const list = turns(6);
    setTurnOpen("c1", list[0]!.id, true);
    openForSearch("c1", list[0]!.id);
    clearSearchOpened("c1");
    expect(isTurnOpen("c1", list[0]!, 0, list.length)).toBe(true);
  });

  it("reports nothing to clear when no search reveal is active", () => {
    expect(clearSearchOpened("c1")).toBe(false);
  });

  it("does not persist a search reveal", () => {
    const list = turns(6);
    openForSearch("c1", list[0]!.id);
    expect(JSON.stringify(localStorage)).not.toContain(list[0]!.id);
  });
});
