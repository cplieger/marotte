import { afterEach, beforeEach, describe, expect, it } from "vitest";

import {
  afterScopeAnswer,
  rescopeOwed,
  runContentScoped,
  scopeReady,
  scopedStep,
  setRunContentScope,
  setScopePresenter,
  type ScopeOutcome,
} from "./run-content-scope.js";

/** A stream standing in for the adapter: each ask waits until the case answers it. */
function scriptedStream(): {
  asks: string[];
  answer: (i: number, outcome: ScopeOutcome) => void;
} {
  const asks: string[] = [];
  const answers: ((outcome: ScopeOutcome) => void)[] = [];
  setScopePresenter((run) => {
    asks.push(run);
    return new Promise((resolve) => {
      answers.push(resolve);
    });
  });
  return {
    asks,
    answer: (i, outcome) => {
      answers[i]?.(outcome);
    },
  };
}

/** What a readiness promise has settled to by now: "pending" when it has not. */
async function settledAs(p: Promise<boolean>): Promise<boolean | "pending"> {
  let out: boolean | "pending" = "pending";
  void p.then((v) => {
    out = v;
  });
  for (let i = 0; i < 10; i++) {
    await Promise.resolve();
  }
  return out;
}

const R = "wf_r";
const S = "wf_s";

beforeEach(() => {
  setScopePresenter(() => Promise.resolve("covered"));
  setRunContentScope("", "");
});

afterEach(() => {
  setScopePresenter(() => Promise.resolve("covered"));
  setRunContentScope("", "");
});

describe("the page's content scope", () => {
  it("keeps the shown run and its step, and no step without a run", () => {
    expect(setRunContentScope(R, `${R}:a`)).toBe("");
    expect(runContentScoped(R)).toBe(true);
    expect(runContentScoped(S)).toBe(false);
    expect(runContentScoped("")).toBe(false);
    expect(scopedStep(R)).toBe(`${R}:a`);
    expect(scopedStep(S)).toBe("");
    expect(setRunContentScope("", `${R}:a`)).toBe(R);
    expect(scopedStep(R)).toBe("");
  });

  it("asks the stream for a run as it enters and for broadcasts as it leaves, never for a step change", () => {
    const stream = scriptedStream();
    setRunContentScope(R, `${R}:a`);
    setRunContentScope(R, `${R}:b`);
    setRunContentScope("", "");
    expect(stream.asks).toEqual([R, ""]);
  });
});

describe("scopeReady", () => {
  it("waits for the stream to deliver the run, then answers true", async () => {
    const stream = scriptedStream();
    setRunContentScope(R, "");
    const ready = scopeReady(R);
    expect(await settledAs(ready)).toBe("pending");
    stream.answer(0, "covered");
    expect(await settledAs(ready)).toBe(true);
    // A read starting later, the run still covered, waits one microtask.
    expect(await settledAs(scopeReady(R))).toBe(true);
  });

  it("answers false for a run the page does not show", async () => {
    scriptedStream();
    setRunContentScope(R, "");
    expect(await settledAs(scopeReady(S))).toBe(false);
    expect(await settledAs(scopeReady(""))).toBe(false);
  });

  it("answers false when the run leaves before the stream delivers it, whatever the late answer", async () => {
    const stream = scriptedStream();
    setRunContentScope(R, "");
    const ready = scopeReady(R);
    setRunContentScope("", "");
    expect(await settledAs(ready)).toBe(false);
    stream.answer(0, "covered");
    expect(await settledAs(scopeReady(R))).toBe(false);
  });

  it("a run that left and came back waits on its newest ask, not the first one's answer", async () => {
    const stream = scriptedStream();
    setRunContentScope(R, "");
    const first = scopeReady(R);
    setRunContentScope("", "");
    setRunContentScope(R, "");
    const second = scopeReady(R);
    expect(stream.asks).toEqual([R, "", R]);

    stream.answer(0, "covered");
    expect(await settledAs(first)).toBe(false);
    expect(await settledAs(second)).toBe("pending");
    stream.answer(2, "covered");
    expect(await settledAs(second)).toBe(true);
  });
});

describe("afterScopeAnswer", () => {
  it("holds a read until the stream answers the run's ask, delivered or lost", async () => {
    const stream = scriptedStream();
    const reads: string[] = [];
    setRunContentScope(R, "");
    afterScopeAnswer(R, () => reads.push("delivered"));
    await settledAs(scopeReady(R));
    expect(reads).toEqual([]);
    stream.answer(0, "covered");
    await settledAs(scopeReady(R));
    expect(reads).toEqual(["delivered"]);

    setRunContentScope(S, "");
    afterScopeAnswer(S, () => reads.push("lost"));
    stream.answer(1, "owed");
    await settledAs(Promise.resolve(true));
    expect(reads).toEqual(["delivered", "lost"]);
  });

  it("reads at once for a run not scoped or already answered, and drops a read whose run left", async () => {
    const stream = scriptedStream();
    const reads: string[] = [];
    afterScopeAnswer(S, () => reads.push("unscoped"));
    expect(reads).toEqual(["unscoped"]);

    setRunContentScope(R, "");
    afterScopeAnswer(R, () => reads.push("left"));
    setRunContentScope("", "");
    stream.answer(0, "covered");
    await settledAs(Promise.resolve(true));
    expect(reads).toEqual(["unscoped"]);

    setRunContentScope(R, "");
    stream.answer(2, "covered");
    await settledAs(scopeReady(R));
    afterScopeAnswer(R, () => reads.push("answered"));
    expect(reads).toEqual(["unscoped", "answered"]);
  });
});

describe("an ask lost with the link", () => {
  it("keeps the run waiting until the next open asks again, then answers true", async () => {
    const stream = scriptedStream();
    setRunContentScope(R, "");
    const ready = scopeReady(R);
    stream.answer(0, "owed");
    expect(await settledAs(ready)).toBe("pending");

    rescopeOwed();
    expect(stream.asks).toEqual([R, R]);
    stream.answer(1, "covered");
    expect(await settledAs(ready)).toBe(true);
  });

  it("asks once per open: a second open while the re-ask waits sends nothing more", async () => {
    const stream = scriptedStream();
    setRunContentScope(R, "");
    stream.answer(0, "owed");
    await settledAs(scopeReady(R));
    rescopeOwed();
    rescopeOwed();
    expect(stream.asks).toEqual([R, R]);
  });

  it("is never asked again once its run has left the scope", async () => {
    const stream = scriptedStream();
    setRunContentScope(R, "");
    stream.answer(0, "owed");
    await settledAs(scopeReady(R));
    setRunContentScope(S, "");
    stream.answer(1, "covered");
    await settledAs(scopeReady(S));
    rescopeOwed();
    expect(stream.asks).toEqual([R, S]);
  });

  it("an open with nothing owed asks nothing", async () => {
    const stream = scriptedStream();
    setRunContentScope(R, "");
    stream.answer(0, "covered");
    await settledAs(scopeReady(R));
    rescopeOwed();
    expect(stream.asks).toEqual([R]);
  });
});
