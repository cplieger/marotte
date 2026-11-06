// Which turns are open: three overriding layers, and their order is the design.
import { describe, it, expect, beforeEach, onTestFinished, vi } from "vitest";

import { BUS_FOLDS_CHANGED, onBus } from "./bus.js";
import {
  isTurnOpen,
  setTurnOpen,
  openForSearch,
  clearSearchOpened,
  resetFoldState,
} from "./fold-state.js";
import { LS_TURN_FOLDS_KEY } from "./ls-keys.js";
import { MAX_CHATS } from "./per-chat-store.js";
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
    branchFrom: undefined,
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
    expect(clearSearchOpened("c1").length).toBeGreaterThan(0);
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
    expect(clearSearchOpened("c1")).toEqual([]);
  });

  it("does not persist a search reveal", () => {
    const list = turns(6);
    openForSearch("c1", list[0]!.id);
    expect(JSON.stringify(localStorage)).not.toContain(list[0]!.id);
  });
});

// Another window shares this storage: its write lands here with no call into this module, and the
// browser tells this window with a `storage` event.
describe("across windows", () => {
  const t0 = turn("t0");
  const t1 = turn("t1");

  function stored(): Record<string, Record<string, boolean>> {
    return JSON.parse(localStorage.getItem(LS_TURN_FOLDS_KEY) ?? "{}") as Record<
      string,
      Record<string, boolean>
    >;
  }

  function otherWindowWrites(doc: Record<string, Record<string, boolean>>): void {
    localStorage.setItem(LS_TURN_FOLDS_KEY, JSON.stringify(doc));
  }

  function tellThisWindow(key: string | null = LS_TURN_FOLDS_KEY, area = localStorage): void {
    window.dispatchEvent(new StorageEvent("storage", { key, storageArea: area }));
  }

  it("keeps a fold another window saved for a different chat", () => {
    setTurnOpen("c1", "t1", true);
    otherWindowWrites({ c1: { t1: true }, c2: { t1: true } });
    setTurnOpen("c1", "t0", true);
    expect(stored()["c2"]).toEqual({ t1: true });
  });

  it("keeps another window's fold of the same chat's other turn", () => {
    expect(isTurnOpen("c1", t0, 0, 6)).toBe(false);
    otherWindowWrites({ c1: { t1: false } });
    setTurnOpen("c1", "t0", true);
    expect(stored()["c1"]).toEqual({ t0: true, t1: false });
  });

  it("shows a fold another window made once storage says so", () => {
    expect(isTurnOpen("c1", t0, 0, 6)).toBe(false);
    otherWindowWrites({ c1: { t0: true } });
    tellThisWindow();
    expect(isTurnOpen("c1", t0, 0, 6)).toBe(true);
  });

  it("forgets folds another window's sign-out cleared", () => {
    setTurnOpen("c1", "t0", true);
    expect(isTurnOpen("c1", t0, 0, 6)).toBe(true);
    localStorage.removeItem(LS_TURN_FOLDS_KEY);
    tellThisWindow();
    expect(isTurnOpen("c1", t0, 0, 6)).toBe(false);
  });

  it("evicts by every window's touches, not this window's stale order", () => {
    const doc: Record<string, Record<string, boolean>> = {};
    for (let i = 0; i < MAX_CHATS; i++) {
      doc[`c${String(i)}`] = { t0: true };
    }
    otherWindowWrites(doc);
    expect(isTurnOpen("c5", t0, 0, 6)).toBe(true);
    // c0 moves to the end: the other window just touched it.
    const { c0, ...rest } = doc;
    otherWindowWrites({ ...rest, c0: c0! });
    setTurnOpen("fresh", "t0", true);
    const kept = stored();
    expect(kept["c0"]).toEqual({ t0: true });
    expect(kept["c1"]).toBeUndefined();
  });

  it("stops showing a chat its own write evicted", () => {
    for (let i = 0; i < MAX_CHATS; i++) {
      setTurnOpen(`c${String(i)}`, "t1", true);
    }
    setTurnOpen("extra", "t1", true);
    expect(stored()["c0"]).toBeUndefined();
    expect(isTurnOpen("c0", t1, 0, 6)).toBe(false);
  });

  it("tells the transcript once the cache has dropped", () => {
    expect(isTurnOpen("c1", t0, 0, 6)).toBe(false);
    const seen: boolean[] = [];
    const off = onBus(BUS_FOLDS_CHANGED, () => {
      seen.push(isTurnOpen("c1", t0, 0, 6));
    });
    onTestFinished(off);
    otherWindowWrites({ c1: { t0: true } });
    tellThisWindow();
    expect(seen).toEqual([true]);
  });

  it("ignores a storage event from session storage", () => {
    setTurnOpen("c1", "t0", true);
    expect(isTurnOpen("c1", t0, 0, 6)).toBe(true);
    otherWindowWrites({});
    tellThisWindow(null, sessionStorage);
    expect(isTurnOpen("c1", t0, 0, 6)).toBe(true);
  });

  it("keeps a fold for the page when storage refuses the write", () => {
    vi.stubGlobal("localStorage", {
      getItem: (): null => null,
      setItem: (): never => {
        throw new DOMException("full", "QuotaExceededError");
      },
      removeItem: (): void => undefined,
      clear: (): void => undefined,
      key: (): null => null,
      length: 0,
    });
    setTurnOpen("c1", "t0", true);
    expect(isTurnOpen("c1", t0, 0, 6)).toBe(true);
  });
});
