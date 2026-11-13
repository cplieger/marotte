// engine-read.ts owns the re-read on the engine's return; each order a ready event can land in is a row.

import { describe, it, expect, afterEach } from "vitest";
import { engineRead, type EngineRead } from "./engine-read.js";
import { BUS_ENGINE_READY, emitBus } from "./bus.js";

/** A panel's read: each send records a pending answer the test settles. */
function panel(shown = true): {
  read: EngineRead;
  sends: () => number;
  answer: (answered: boolean | null) => void;
  setShown: (v: boolean) => void;
} {
  let on = shown;
  const pending: ((answered: boolean | null) => void)[] = [];
  let sends = 0;
  const send = (): void => {
    sends++;
    pending.push(read.begin());
  };
  const read = engineRead(send, () => on);
  disposers.push(read);
  send();
  return {
    read,
    sends: () => sends,
    answer: (answered) => {
      const settle = pending.shift();
      if (settle === undefined) {
        throw new Error("no read is out");
      }
      settle(answered);
    },
    setShown: (v) => {
      on = v;
    },
  };
}

/** A finished case's reads are reset, so a later case's ready event asks none of them. */
const disposers: EngineRead[] = [];
afterEach(() => {
  for (const d of disposers.splice(0)) {
    d.reset();
  }
});

const ready = (): void => {
  emitBus(BUS_ENGINE_READY);
};

describe("engineRead", () => {
  it.each([
    { order: "ready before the read answers nothing new", steps: ["answer", "ready"], sends: 1 },
    { order: "ready after a refusal re-asks once", steps: ["refuse", "ready"], sends: 2 },
    {
      order: "ready twice after a refusal re-asks once",
      steps: ["refuse", "ready", "ready"],
      sends: 2,
    },
    {
      order: "ready during a refused read re-asks once it settles",
      steps: ["ready", "refuse"],
      sends: 2,
    },
    {
      order: "ready during an answering read re-asks once it settles",
      steps: ["ready", "answer"],
      sends: 2,
    },
    {
      order: "ready twice during a read re-asks once",
      steps: ["ready", "ready", "refuse"],
      sends: 2,
    },
    { order: "ready during an abandoned read re-asks once", steps: ["ready", "abandon"], sends: 2 },
    {
      order: "a re-ask refused again waits for the next ready",
      steps: ["refuse", "ready", "refuse", "ready"],
      sends: 3,
    },
  ])("$order", ({ steps, sends }) => {
    const p = panel();
    for (const step of steps) {
      switch (step) {
        case "ready":
          ready();
          break;
        case "answer":
          p.answer(true);
          break;
        case "refuse":
          p.answer(false);
          break;
        case "abandon":
          p.answer(null);
          break;
      }
    }
    expect(p.sends()).toBe(sends);
  });

  it("sends no second read while one is out", () => {
    const p = panel();
    ready();
    ready();
    expect(p.sends(), "while the first read is out").toBe(1);
    p.answer(false);
    expect(p.sends(), "after it settles").toBe(2);
  });

  it("asks nothing for a panel off screen, which reads on its next open", () => {
    const p = panel(false);
    p.answer(false);
    ready();
    expect(p.sends(), "refused, then ready").toBe(1);
    const q = panel(false);
    ready();
    q.answer(false);
    expect(q.sends(), "ready during the read").toBe(1);
  });

  it("ignores a superseded read's settle", () => {
    const p = panel();
    const first = p.read.begin();
    const second = p.read.begin();
    ready();
    first(false);
    expect(p.sends(), "the superseded read settled").toBe(1);
    second(true);
    expect(p.sends(), "the newest read settled").toBe(2);
  });

  it("asks nothing for a closed panel's refused or out read once it is reset", () => {
    const p = panel();
    p.answer(false);
    p.read.reset();
    ready();
    expect(p.sends(), "refused, reset, then ready").toBe(1);
    const q = panel();
    q.read.reset();
    q.answer(false);
    ready();
    expect(q.sends(), "out, reset, then a late settle").toBe(1);
  });
});
