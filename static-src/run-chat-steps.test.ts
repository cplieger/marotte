// The run pane's step-transcript render lifecycle.

import { vi, describe, it, expect, beforeEach } from "vitest";
import type { Entry } from "./types.js";
// TYPE-only, so it is erased and cannot defeat the `vi.mock` hoisting the value import below is
// deferred for.
import type { RunStepPaint } from "./run-chat-steps.js";

interface Call {
  fn: "build" | "update" | "finalize" | "dispose";
  /** The render key the call was made under: a step turn's own id. */
  key: string;
  /** The LANE the call paired that key with. `""` is the pane's root. */
  lane: string;
  host?: HTMLElement;
  entryCount?: number;
  live?: boolean;
  /** Always "" here — a step's entries are the RUN's, so a delegate's page link has no destination
   *  and the dispatcher withholds it on "". */
  chatID?: string;
}

const m = vi.hoisted(() => ({ calls: [] as unknown[] }));

vi.mock("./messages-blocks.js", () => ({
  buildDetachedBody: vi.fn(
    (host: HTMLElement, turn: { id: string; body?: Entry[] }, chatID, lane, live) => {
      m.calls.push({
        fn: "build",
        key: turn.id,
        lane,
        chatID,
        host,
        entryCount: (turn.body ?? []).length,
        live,
      });
      // A real build fills the box, so the rebuild case can prove the box was cleared first rather
      // than appended to.
      host.appendChild(document.createElement("p"));
    },
  ),
  updateDetachedBody: vi.fn(
    (host: HTMLElement, turn: { id: string; body?: Entry[] }, chatID, lane, live) => {
      m.calls.push({
        fn: "update",
        key: turn.id,
        lane,
        chatID,
        host,
        entryCount: (turn.body ?? []).length,
        live,
      });
    },
  ),
  finalizeDetachedBody: vi.fn((key: string, lane: string) => {
    m.calls.push({ fn: "finalize", key, lane });
  }),
  disposeDetachedBody: vi.fn((key: string, lane: string) => {
    m.calls.push({ fn: "dispose", key, lane });
  }),
}));

const { createRunStepStream } = await import("./run-chat-steps.js");

function entry(turn: string, seq: number, kind: Entry["kind"] = "text"): Entry {
  return { id: `${turn}-${String(seq)}`, turn, kind, seq, ts: 0, payload: {} };
}

/** A step turn as the pane paints it. Only the fields this module reads are filled; the
 *  dispatcher's own view of a turn is `messages-blocks.test.ts`'s subject. */
function paint(
  turnID: string,
  kinds: readonly Entry["kind"][],
  live = true,
  prompt?: string,
): RunStepPaint {
  const body = kinds.map((kind, i) => entry(turnID, i + 1, kind));
  return {
    turn: {
      id: turnID,
      n: 1,
      trigger: prompt === undefined ? undefined : { id: `p-${turnID}`, text: prompt },
      body,
      openEntries: new Map(),
      ts: 0,
      outcome: live ? "running" : "completed",
      rewindTo: undefined,
    },
    live,
  };
}

function harness(): {
  apply: (paints: Record<string, readonly RunStepPaint[]>) => void;
  dispose: () => void;
  host: (path: string) => HTMLElement;
  calls: Call[];
} {
  const hosts = new Map<string, HTMLElement>();
  const stream = createRunStepStream((path) => {
    let host = hosts.get(path);
    if (host === undefined) {
      host = document.createElement("div");
      hosts.set(path, host);
    }
    return host;
  });
  return {
    apply: (paints) => {
      stream.apply(new Map(Object.entries(paints)));
    },
    dispose: () => {
      stream.dispose();
    },
    host: (path) => {
      const host = hosts.get(path);
      if (host === undefined) {
        throw new Error(`no host for ${path}`);
      }
      return host;
    },
    calls: m.calls as Call[],
  };
}

/** The build calls only. A build is preceded by an unconditional dispose (the rebuild arm's
 *  clear), which is noise in an assertion about what was built. */
function builds(h: { calls: Call[] }): Call[] {
  return h.calls.filter((c) => c.fn === "build");
}

beforeEach(() => {
  m.calls.length = 0;
});

describe("run step stream", () => {
  // ONE build per step TURN, under the turn's own id and the pane's ROOT lane. The four detached
  // calls are keyed by `(turn, lane)`, so a mismatch orphans the render: the build registers under
  // one key and the dispose clears another.
  it("builds once per turn, keyed by the turn id and the root lane", () => {
    const h = harness();
    h.apply({ "a/coder": [paint("t-coder", ["text"])], "a/verify": [paint("t-verify", ["text"])] });
    expect(builds(h).map((c) => [c.key, c.lane])).toEqual([
      ["t-coder", ""],
      ["t-verify", ""],
    ]);
  });

  // The dispatcher is handed a BOX of this stream's own, inside the host the consumer supplied —
  // which is what lets one path hold several turns without them rebuilding over each other.
  it("builds into a box of its own inside the path's host", () => {
    const h = harness();
    h.apply({ "a/coder": [paint("t1", ["text"])] });
    const box = builds(h)[0]?.host;
    expect(box).not.toBe(h.host("a/coder"));
    expect(box?.parentElement).toBe(h.host("a/coder"));
    expect(box?.className).toBe("ev-d-turn");
  });

  // A path can hold SEVERAL turns — a resume after a restart re-opens the same node path as a new
  // turn — so each renders in its own box under its own key, in the order the log holds them. Keyed
  // by path instead, the second turn would rebuild over the first.
  it("renders several turns of one path in their own boxes, in file order", () => {
    const h = harness();
    h.apply({ "a/coder": [paint("t-first", ["text"], false), paint("t-second", ["text"])] });
    expect(builds(h).map((c) => c.key)).toEqual(["t-first", "t-second"]);
    expect(builds(h)[0]?.host).not.toBe(builds(h)[1]?.host);
    expect(builds(h)[0]?.host?.parentElement).toBe(h.host("a/coder"));
    expect(h.host("a/coder").childElementCount).toBe(2);
  });

  // A message that resumed a paused step opened its next turn: the user's words head that turn, the
  // way a prompt heads a chat turn, and leave with it.
  it("heads a turn the user's message opened with their words", () => {
    const h = harness();
    h.apply({
      "a/ask": [paint("t-asked", ["text"], false), paint("t-resumed", ["text"], true, "teal")],
    });
    const host = h.host("a/ask");
    const [first, heading, resumed] = [...host.children];
    expect(first).toBe(builds(h)[0]?.host);
    expect(heading?.classList.contains("turn-header")).toBe(true);
    expect(heading?.getAttribute("data-trigger")).toBe("user");
    expect(heading?.textContent).toBe("teal");
    expect(resumed).toBe(builds(h)[1]?.host);

    h.apply({ "a/ask": [paint("t-asked", ["text"], false)] });
    expect(host.childElementCount).toBe(1);
  });

  // The dispatcher's incremental update appends past a watermark, so it is correct only while the
  // prefix it mounted is unchanged. A rebuild here would throw away the reader's place on every
  // streamed chunk.
  it("updates rather than rebuilds when the entries merely grew", () => {
    const h = harness();
    h.apply({ "a/coder": [paint("t1", ["text"])] });
    m.calls.length = 0;
    h.apply({ "a/coder": [paint("t1", ["text", "text"])] });
    expect(h.calls.map((c) => c.fn)).toEqual(["update"]);
    expect(h.calls[0]?.entryCount).toBe(2);
  });

  // ...and the inverse.
  it("disposes, clears the box and rebuilds when the shape moved", () => {
    const h = harness();
    h.apply({ "a/coder": [paint("t1", ["text", "text"])] });
    // The BOX is what the rebuild clears, and the box is what the assertion has to read: the host
    // holds one box either way, so counting ITS children says nothing about the clear (measured —
    // that shape stayed green with the clear deleted).
    const box = builds(h)[0]?.host;
    expect(box?.childElementCount).toBe(1);
    m.calls.length = 0;
    h.apply({ "a/coder": [paint("t1", ["tool_call", "text"])] });
    expect(h.calls.map((c) => c.fn)).toEqual(["dispose", "build"]);
    // The rebuild started from an empty box: exactly one child, the new build's, in the box the
    // first build filled.
    expect(box?.childElementCount).toBe(1);
    expect(h.host("a/coder").childElementCount).toBe(1);
  });

  // The FIRST apply cannot update, whatever the shape says: nothing is mounted for the watermark to
  // extend, and an empty prefix trivially extends anything.
  it("builds on the first apply even though an empty shape extends everything", () => {
    const h = harness();
    h.apply({ "a/coder": [paint("t1", [])] });
    expect(h.calls.map((c) => c.fn)).toEqual(["dispose", "build"]);
  });

  // Every later repaint of a finished run lands here too, so the latch is what keeps one seal from
  // becoming dozens — each of which re-flushes the markdown streams and re-collapses the reasoning
  // traces.
  it("finalizes exactly once across repeated settled applies", () => {
    const h = harness();
    h.apply({ "a/coder": [paint("t1", ["text"], false)] });
    h.apply({ "a/coder": [paint("t1", ["text", "text"], false)] });
    h.apply({ "a/coder": [paint("t1", ["text", "text"], false)] });
    expect(h.calls.filter((c) => c.fn === "finalize")).toHaveLength(1);
  });

  it("does not finalize a turn that is still open", () => {
    const h = harness();
    h.apply({ "a/coder": [paint("t1", ["text"], true)] });
    expect(h.calls.some((c) => c.fn === "finalize")).toBe(false);
  });

  it("re-seals after a rebuild", () => {
    const h = harness();
    h.apply({ "a/coder": [paint("t1", ["text", "text"], false)] });
    m.calls.length = 0;
    h.apply({ "a/coder": [paint("t1", ["tool_call", "text"], false)] });
    expect(h.calls.map((c) => c.fn)).toEqual(["dispose", "build", "finalize"]);
  });

  it("passes the turn's own liveness through to the dispatcher", () => {
    const h = harness();
    h.apply({ "a/coder": [paint("t1", ["text"], false)] });
    expect(builds(h)[0]?.live).toBe(false);
  });

  // Liveness is per TURN and never the run's status: a `parallel` node holds several open turns in
  // one log, so a settled step inside a running run is settled — and one apply carries both.
  it("carries liveness per turn, so one apply can hold both", () => {
    const h = harness();
    h.apply({
      "a/live": [paint("t-live", ["text"], true)],
      "a/done": [paint("t-done", ["text"], false)],
    });
    expect(builds(h).find((c) => c.key === "t-live")?.live).toBe(true);
    expect(builds(h).find((c) => c.key === "t-done")?.live).toBe(false);
    // And only the settled one is sealed.
    expect(h.calls.filter((c) => c.fn === "finalize").map((c) => c.key)).toEqual(["t-done"]);
  });

  // The dispatcher uses the chat id for real: it keys tool-call signals by it and builds a
  // delegate's page link from it. A step's entries are the RUN's, so that link has no destination
  // and an empty id is what makes it correctly absent.
  it("hands the dispatcher no chat id", () => {
    const h = harness();
    h.apply({ "a/coder": [paint("t1", ["text"])] });
    expect(builds(h)[0]?.chatID).toBe("");
  });

  // A turn the log no longer holds — a truncate, or a page whose run re-read shorter — takes its
  // render with it, or its disposers stay registered under a key nothing ever disposes and its box
  // sits in the pane.
  it("releases a turn the next apply no longer names", () => {
    const h = harness();
    h.apply({ "a/coder": [paint("t-old", ["text"]), paint("t-new", ["text"])] });
    const host = h.host("a/coder");
    m.calls.length = 0;
    h.apply({ "a/coder": [paint("t-new", ["text"])] });
    expect(h.calls.filter((c) => c.fn === "dispose").map((c) => c.key)).toEqual(["t-old"]);
    expect(host.childElementCount).toBe(1);
  });

  // The tab's retarget: every render this stream registered has to be released, or the previous
  // run's disposers stay alive under the next run's page.
  it("disposes every turn it holds", () => {
    const h = harness();
    h.apply({ "a/coder": [paint("t-coder", ["text"])], "a/verify": [paint("t-verify", ["text"])] });
    m.calls.length = 0;
    h.dispose();
    expect(h.calls.map((c) => [c.fn, c.key, c.lane])).toEqual([
      ["dispose", "t-coder", ""],
      ["dispose", "t-verify", ""],
    ]);
  });

  it("disposes nothing twice", () => {
    const h = harness();
    h.apply({ "a/coder": [paint("t1", ["text"])] });
    h.dispose();
    m.calls.length = 0;
    h.dispose();
    expect(h.calls).toEqual([]);
  });
});
