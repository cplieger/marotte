// The worker host's marotte-owned decisions (the attach reconnect, the profile's one digest, which
// tab draws a frame) driven through the REAL library host over Node MessageChannel ports and a
// scripted stream. Node: a SharedWorker's scope has no DOM, and the host's behaviour must hold with
// none.

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import {
  type DigestClient,
  type DigestResult,
  type DigestVerdict,
  type RevalidateContext,
  type Stream,
  type TabToWorker,
  type WorkerHost,
  type WorkerToTab,
  createVersionMap,
} from "@cplieger/sse";

import { createSSEHost, profileRevalidate } from "./sse-worker-host.js";
import {
  EPOCH_A,
  EPOCH_B,
  createScriptedFetch,
  json,
  type ScriptedFetch,
} from "./__test-helpers__/sse-fetch.js";
import { until } from "./__test-helpers__/until.js";

/** One attached tab: its port, what the host sent it, and a reply policy for runs. */
interface Tab {
  readonly port: MessagePort;
  readonly received: WorkerToTab[];
  answerRuns: "done" | "failed" | "silent";
}

let scripted: ScriptedFetch;
let host: WorkerHost | null;
const tabs: Tab[] = [];

beforeEach(() => {
  scripted = createScriptedFetch();
  vi.stubGlobal("fetch", scripted.fetch);
  host = null;
});

afterEach(() => {
  host?.close();
  for (const tab of tabs) {
    tab.port.close();
  }
  tabs.length = 0;
});

function attach(
  tabId: string,
  tag = "profile-tag-0123456789",
  scope?: readonly string[] | null,
): Tab {
  const channel = new MessageChannel();
  const tab: Tab = { port: channel.port1, received: [], answerRuns: "done" };
  tab.port.onmessage = (event: MessageEvent) => {
    const message = event.data as WorkerToTab;
    tab.received.push(message);
    if (message.type === "heartbeat") {
      send(tab, { type: "heartbeat_ack", seq: message.seq });
    }
    if (message.type === "revalidate_run") {
      if (tab.answerRuns === "done") {
        send(tab, { type: "revalidate_done", runId: message.runId });
      } else if (tab.answerRuns === "failed") {
        send(tab, {
          type: "revalidate_failed",
          runId: message.runId,
          cause: "pending moved; only a hello carries it",
        });
      }
    }
  };
  tab.port.start();
  tabs.push(tab);
  host?.attach(channel.port2);
  send(tab, {
    type: "attach",
    tabId,
    visible: true,
    online: true,
    hadWorker: false,
    tag,
    ...(scope === undefined ? {} : { scope }),
  });
  return tab;
}

function send(tab: Tab, message: TabToWorker): void {
  tab.port.postMessage(message);
}

let pingSeq = 0;

/** Resolves on the host's answer to a ping sent now: the host handles one port's messages in
 *  order, so every message `tab` sent before it, and the microtask work each one started, has run. */
async function roundTrip(tab: Tab): Promise<void> {
  const seq = ++pingSeq;
  send(tab, { type: "ping", seq });
  await until(
    () => tab.received.some((m) => m.type === "pong" && m.seq === seq),
    "the host's pong",
  );
}

function runsSeen(tab: Tab): number {
  return tab.received.filter((m) => m.type === "revalidate_run").length;
}

function runContexts(tab: Tab) {
  return tab.received.flatMap((m) => (m.type === "revalidate_run" ? [m.ctx] : []));
}

/** Hide every tab, then show the first: the profile's fold turns that into the stream's wake.
 *  Waits for the fold to read hidden in between, because two ports deliver in no fixed order
 *  relative to each other and a show that lands before the last hide is no wake. */
async function wake(owner: WorkerHost, first: Tab, ...others: Tab[]): Promise<void> {
  for (const tab of [first, ...others]) {
    send(tab, { type: "visibility", ev: "hidden" });
  }
  await until(() => {
    const state = owner.stream().state();
    return state.kind === "open" && !state.visible;
  }, "the host to read the profile hidden");
  send(first, { type: "visibility", ev: "visible" });
}

describe("reconnectForAttach", () => {
  it("a tab attaching to an OPEN stream reconnects it in place, cursor kept", async () => {
    host = createSSEHost();
    const a = attach("a");
    await until(() => scripted.connections.length === 1, "the first connection");
    const first = scripted.connections[0];
    expect(first?.headers.get("SSE-Client")).toBe("profile-tag-0123456789");
    first?.hello();
    await until(() => host?.stream().state().kind === "open", "the host's stream to open");
    // The attach that STARTED the stream opened exactly one connection.
    await roundTrip(a);
    expect(scripted.connections).toHaveLength(1);
    expect(scripted.connections.at(-1)?.aborted()).toBe(false);

    attach("b");
    await until(() => scripted.connections.length === 2, "the second connection");
    expect(first?.aborted()).toBe(true);
    // Resumed, not fresh: the newcomer earns the hook's sets, the profile loses no frame.
    expect(scripted.connections[1]?.headers.get("Last-Event-ID")).not.toBeNull();
  });

  it("a tab attaching while the stream is connecting adds no second connect", async () => {
    host = createSSEHost();
    attach("a");
    await until(() => scripted.connections.length === 1, "the first connection");
    const b = attach("b");
    await roundTrip(b);
    expect(scripted.connections).toHaveLength(1);
    expect(scripted.connections.at(-1)?.aborted()).toBe(false);
  });
});

describe("deliver", () => {
  const RUN = "wf_5y00000000000001";
  const OTHER = "wf_5y00000000000002";
  const CONN = "c0nn3ct10n1d0123456789";

  /** The application frames a tab received, as each frame's data. */
  function delivered(tab: Tab): string[] {
    return tab.received.flatMap((m) => (m.type === "frame" ? [m.frame.data] : []));
  }

  it("a run's log frames reach only the tab scoped to that run, every broadcast reaches both", async () => {
    scripted.respond("/api/events/scope", () => json({ epoch: EPOCH_A, offset: "0" }));
    host = createSSEHost();
    const chat = attach("chat", undefined, []);
    await until(() => scripted.connections.length === 1, "the first connection");
    const run = attach("run", undefined, [`run:${RUN}`]);
    await roundTrip(run);
    const conn = scripted.connections[0];
    // The first connect presents the one tab's scope; the run tab's topic is added on the
    // connection the hello names.
    expect(conn?.headers.get("SSE-Topics")).toBe("[]");
    conn?.hello({ conn: CONN });
    await until(
      () => scripted.requests.some((r) => r.url === "/api/events/scope"),
      "the scope request",
    );
    const scopeRequest = scripted.requests.find((r) => r.url === "/api/events/scope");
    expect(scopeRequest?.method).toBe("POST");
    expect(JSON.parse(scopeRequest?.body ?? "{}")).toEqual({
      conn: CONN,
      seq: 1,
      topics: [`run:${RUN}`],
    });

    const chatFrame = { type: "chat_updated", chat_id: "c1", payload: { id: "c1" } };
    const chatEntry = { type: "entry_delta", chat_id: "c1", payload: { turn: "t2", seq: 0 } };
    const runTree = { type: "run_progress", payload: { workflow_id: RUN } };
    const runEntry = { type: "entry_delta", payload: { workflow_id: RUN, turn: "t1", seq: 0 } };
    const otherEntry = { type: "entry_sealed", payload: { workflow_id: OTHER, turn: "t9" } };
    let offset = 0;
    for (const envelope of [chatFrame, runEntry, otherEntry, runTree, chatEntry]) {
      conn?.frame(envelope, `${EPOCH_A}:${String(++offset)}`);
    }
    // Not JSON: no run can be read off it, so it reaches every tab.
    conn?.named("message", "{not json");
    const each = (envelopes: object[]) => [...envelopes.map((e) => JSON.stringify(e)), "{not json"];
    await until(
      () => delivered(chat).length >= 4 && delivered(run).length >= 5,
      "the frames on both tabs",
    );
    await roundTrip(chat);
    await roundTrip(run);

    expect(delivered(chat)).toEqual(each([chatFrame, runTree, chatEntry]));
    expect(delivered(run)).toEqual(each([chatFrame, runEntry, runTree, chatEntry]));
    expect(
      [...chat.received, ...run.received].some(
        (m) => m.type === "lifecycle" && m.event.kind === "deliver_failed",
      ),
    ).toBe(false);
  });
});

describe("profileRevalidate", () => {
  it("fans the run to every attached tab and settles when each reports", async () => {
    host = createSSEHost();
    const a = attach("a");
    a.answerRuns = "silent";
    await until(() => scripted.connections.length === 1, "the first connection");
    scripted.connections[0]?.hello();
    await until(() => runsSeen(a) === 1, "the first revalidate run");
    // Held open by the silent tab: the host's stream is still on its first connection.
    await roundTrip(a);
    expect(scripted.connections).toHaveLength(1);
    expect(scripted.connections.at(-1)?.aborted()).toBe(false);
    const run = a.received.find((m) => m.type === "revalidate_run");
    if (run?.type !== "revalidate_run") {
      throw new Error("no revalidate_run reached the tab");
    }
    send(a, { type: "revalidate_done", runId: run.runId });
    await roundTrip(a);
    expect(scripted.connections).toHaveLength(1);
    expect(scripted.connections.at(-1)?.aborted()).toBe(false);
  });

  it("a tab's rejected run ends the connection, and the hello that follows runs again", async () => {
    host = createSSEHost();
    const a = attach("a");
    a.answerRuns = "failed";
    await until(() => scripted.connections.length === 1, "the first connection");
    scripted.connections[0]?.hello();
    await until(() => runsSeen(a) === 1, "the first revalidate run");
    // The tab body throws by accident only (sse-adapter.ts settles every loader), so this is the
    // safety net: the library ends the connection, then reconnects after one backoff (up to 500 ms
    // of full jitter at the base).
    await until(() => scripted.connections.length === 2, "the reconnect after one backoff", {
      timeout: 2000,
    });
    expect(scripted.connections[0]?.aborted()).toBe(true);
    const failed = a.received.find(
      (m) => m.type === "lifecycle" && m.event.kind === "revalidate_failed",
    );
    expect(failed).toBeDefined();

    // The next hello re-runs the reconciliation (the unverified latch), and a tab that now
    // converges leaves the connection standing.
    a.answerRuns = "done";
    scripted.connections[1]?.hello({ resumed: true });
    await until(() => runsSeen(a) === 2, "the second revalidate run");
    await roundTrip(a);
    expect(scripted.connections).toHaveLength(2);
    expect(scripted.connections.at(-1)?.aborted()).toBe(false);
  });

  it("routes the host's one digest to every tab: an observed stamp is in the snapshot and the verdict rides both runs", async () => {
    host = createSSEHost();
    const a = attach("a");
    const b = attach("b");
    await until(() => scripted.connections.length === 1, "the first connection");
    scripted.connections[0]?.hello();
    // The fresh hello's run: nothing held yet, so no digest and an empty verdict.
    await until(
      () => runsSeen(a) === 1 && runsSeen(b) === 1,
      "the first revalidate run on both tabs",
    );
    expect(scripted.requests.filter((r) => r.url === "/api/sync")).toHaveLength(0);
    expect(runContexts(a)[0]).toMatchObject({ full: false, changed: [], removed: [] });

    send(a, {
      type: "observed",
      subject: { kind: "chat", ref: "c1" },
      version: "4",
      epoch: EPOCH_A,
    });
    scripted.respond("/api/sync", (req) =>
      json({
        epoch: EPOCH_A,
        head: "9",
        must_refetch: false,
        checked: (JSON.parse(req.body ?? "{}") as { subjects: unknown[] }).subjects.length,
        changed: [{ kind: "chat", ref: "c1", version: "5" }],
        removed: [],
      }),
    );
    await wake(host, a, b);
    await until(
      () => runsSeen(a) === 2 && runsSeen(b) === 2,
      "the second revalidate run on both tabs",
    );

    const syncs = scripted.requests.filter((r) => r.url === "/api/sync");
    expect(syncs).toHaveLength(1);
    expect(JSON.parse(syncs[0]?.body ?? "{}")).toEqual({
      epoch: EPOCH_A,
      subjects: [{ kind: "chat", ref: "c1", version: "4" }],
    });
    const verdict = { changed: [{ kind: "chat", ref: "c1", version: "5" }], removed: [] };
    expect(runContexts(a)[1]).toMatchObject({ cause: "visible", full: false, ...verdict });
    expect(runContexts(b)[1]).toMatchObject({ cause: "visible", full: false, ...verdict });
  });

  describe("the body, over a fake tab set and digest", () => {
    const signal = new AbortController().signal;
    const base: RevalidateContext = {
      cause: "visible",
      epoch: EPOCH_A,
      generation: 1,
      full: false,
      signal,
    };

    function harness(answer: DigestResult | Error) {
      const runs: { ctx: RevalidateContext; verdict: DigestVerdict | undefined }[] = [];
      const checks: unknown[] = [];
      // What the stream was asked, in order, and how many runs had settled by then.
      const streamCalls: { call: "resetCursor" | "reconnect"; runsSettled: number }[] = [];
      const versions = createVersionMap();
      versions.bind(EPOCH_A);
      const tabs = {
        run: (ctx: RevalidateContext, verdict?: DigestVerdict) => {
          runs.push({ ctx, verdict });
          return Promise.resolve();
        },
        size: () => 1,
      };
      const digest: DigestClient = {
        check: (snapshot) => {
          checks.push(snapshot);
          return answer instanceof Error ? Promise.reject(answer) : Promise.resolve(answer);
        },
      };
      // The two members the body may call; the rest of the surface is the library's.
      const stream = {
        resetCursor: () => {
          streamCalls.push({ call: "resetCursor", runsSettled: runs.length });
        },
        reconnect: () => {
          streamCalls.push({ call: "reconnect", runsSettled: runs.length });
        },
      } as unknown as Stream;
      const revalidate = (ctx: RevalidateContext) =>
        profileRevalidate(ctx, tabs, versions, digest, stream);
      return { runs, checks, streamCalls, versions, revalidate };
    }

    it("a full run reaches the tabs with no verdict and no digest", async () => {
      const h = harness(new Error("must not be asked"));
      h.versions.observe({ kind: "chats", ref: "" }, "2");
      await h.revalidate({ ...base, full: true });
      expect(h.checks).toEqual([]);
      expect(h.runs).toEqual([{ ctx: { ...base, full: true }, verdict: undefined }]);
    });

    it("an empty map fans an empty verdict without asking", async () => {
      const h = harness(new Error("must not be asked"));
      await h.revalidate(base);
      expect(h.checks).toEqual([]);
      expect(h.runs).toEqual([{ ctx: base, verdict: { changed: [], removed: [] } }]);
    });

    it("one digest over the held map; its changed and removed sets ride the run", async () => {
      const changed = [{ kind: "chat", ref: "c1", version: "5" }];
      const removed = [{ kind: "chat", ref: "c2", reason: "gone" as const }];
      const h = harness({ kind: "ok", epoch: EPOCH_A, head: "9", changed, removed });
      h.versions.observe({ kind: "chat", ref: "c1" }, "4");
      h.versions.observe({ kind: "chat", ref: "c2" }, "1");
      await h.revalidate(base);
      expect(h.checks).toEqual([
        {
          epoch: EPOCH_A,
          held: [
            { kind: "chat", ref: "c1", version: "4" },
            { kind: "chat", ref: "c2", version: "1" },
          ],
        },
      ]);
      expect(h.runs).toEqual([{ ctx: base, verdict: { changed, removed } }]);
      expect(h.streamCalls).toEqual([]);
    });

    it("pending moved: the profile's stream connects again with no cursor, once, after the run settled", async () => {
      const changed = [
        { kind: "chat", ref: "c1", version: "5" },
        { kind: "pending", ref: "", version: "2" },
      ];
      const h = harness({ kind: "ok", epoch: EPOCH_A, head: "9", changed, removed: [] });
      h.versions.observe({ kind: "pending", ref: "" }, "1");
      await h.revalidate(base);
      expect(h.streamCalls).toEqual([
        { call: "resetCursor", runsSettled: 1 },
        { call: "reconnect", runsSettled: 1 },
      ]);
    });

    it("a hello's own run never reconnects, though its snapshot reads pending as moved", async () => {
      const changed = [{ kind: "pending", ref: "", version: "2" }];
      const h = harness({ kind: "ok", epoch: EPOCH_A, head: "9", changed, removed: [] });
      h.versions.observe({ kind: "pending", ref: "" }, "1");
      await h.revalidate({ ...base, cause: "hello" });
      expect(h.runs).toHaveLength(1);
      expect(h.streamCalls).toEqual([]);
    });

    it("must_refetch binds the map to the new epoch and fans a FULL run carrying it", async () => {
      const h = harness({ kind: "must_refetch", epoch: EPOCH_B, head: "0" });
      h.versions.observe({ kind: "chats", ref: "" }, "2");
      await h.revalidate(base);
      expect(h.versions.epoch()).toBe(EPOCH_B);
      expect(h.versions.snapshot().held).toEqual([]);
      expect(h.runs).toEqual([
        { ctx: { ...base, epoch: EPOCH_B, full: true }, verdict: undefined },
      ]);
    });

    it("a failed digest rejects before any tab runs, so the library ends the connection", async () => {
      const h = harness(new Error("digest down"));
      h.versions.observe({ kind: "chats", ref: "" }, "2");
      await expect(h.revalidate(base)).rejects.toThrow("digest down");
      expect(h.runs).toEqual([]);
    });
  });
});
