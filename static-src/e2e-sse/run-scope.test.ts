// Against the REAL binary: a run's log reaches only the streams and tabs scoped to that run, and
// every broadcast reaches every one. The run's frames come from the fixture's run-log hook, which
// publishes through the run log's own publishers; a chat published after them is the barrier, since
// one connection delivers in publish order. Node: the fixture refuses cross-site POSTs.

import { afterEach, describe, expect, inject, it, vi } from "vitest";

import {
  type Frame,
  type LifecycleEvent,
  type SharedWorkerLike,
  type Stream,
  type TabAttachment,
  type WorkerHost,
  attachToWorker,
  createOnlineManager,
  createStream,
  createVersionMap,
  createVisibilityManager,
} from "@cplieger/sse";

import { createSSEHost } from "../sse-worker-host.js";

const SKIP_REASON =
  "marotte fixture not started: set SSE_FIXTURE to a marotte binary built with -tags marotte_test";
const FIXTURE = process.env["SSE_FIXTURE"];
if (FIXTURE === undefined || FIXTURE === "") {
  console.warn(`[vitest] ${SKIP_REASON}`);
}

/** The run log's frame types: each carries its run in `payload.workflow_id`. */
const RUN_LOG_TYPES = new Set([
  "turn_opened",
  "entry_opened",
  "entry_delta",
  "entry_sealed",
  "entry_appended",
  "turn_closed",
]);

/** What the run-log hook publishes for one run, in order. */
const HOOK_RUN_LOG = [
  "turn_opened",
  "entry_delta",
  "entry_sealed",
  "entry_appended",
  "turn_closed",
];

interface Envelope {
  readonly type: string;
  readonly chat_id: string;
  readonly payload: { readonly workflow_id?: string; readonly chat?: { readonly id?: string } };
}

function until(predicate: () => boolean, what: string, timeoutMs = 10_000): Promise<void> {
  return new Promise((resolve, reject) => {
    const deadline = Date.now() + timeoutMs;
    const tick = (): void => {
      if (predicate()) {
        resolve();
        return;
      }
      if (Date.now() > deadline) {
        reject(new Error(`timed out waiting for ${what}`));
        return;
      }
      setTimeout(tick, 10);
    };
    tick();
  });
}

function runID(): string {
  return `wf_e2e${Math.random().toString(36).slice(2, 14)}`;
}

/** The run log frames of `run` among `frames`, by type, in arrival order. */
function runLog(frames: readonly Envelope[], run: string): string[] {
  return frames
    .filter((f) => RUN_LOG_TYPES.has(f.type) && f.payload.workflow_id === run)
    .map((f) => f.type);
}

function created(frames: readonly Envelope[], chat: string): boolean {
  return frames.some((f) => f.type === "chat_created" && f.chat_id === chat);
}

describe.skipIf(FIXTURE === undefined || FIXTURE === "")("the run topic", () => {
  const base = (): string => inject("marotteURL");
  const cleanups: (() => void)[] = [];

  const unlisten = (): (() => void) => () => undefined;
  const visibility = createVisibilityManager({ visible: () => true, listen: unlisten });
  const online = createOnlineManager({ online: () => true, listen: unlisten });

  afterEach(() => {
    for (const cleanup of cleanups.splice(0).reverse()) {
      cleanup();
    }
    vi.unstubAllGlobals();
  });

  async function post(path: string, body: unknown, headers: Record<string, string> = {}) {
    const res = await fetch(`${base()}${path}`, {
      method: "POST",
      headers: { "Content-Type": "application/json", ...headers },
      body: JSON.stringify(body),
    });
    if (!res.ok) {
      throw new Error(`POST ${path} answered ${String(res.status)}: ${await res.text()}`);
    }
    return res;
  }

  async function publishRunLog(run: string): Promise<void> {
    await post("/api/test/sse/run-log", { run });
  }

  /** A chat_created on the broadcast topic; its id, for the barrier. */
  async function createChat(): Promise<string> {
    const res = await post(
      "/api/command",
      { type: "create_chat", chat_id: "", payload: { name: "run-scope" } },
      { "Sec-Fetch-Site": "same-origin" },
    );
    const body = (await res.json()) as { chat: { id: string } };
    return body.chat.id;
  }

  interface Client {
    readonly stream: Stream;
    readonly frames: Envelope[];
    readonly lifecycle: LifecycleEvent[];
  }

  /** A tab's own stream (the no-SharedWorker fallback), scoped to broadcasts at its first connect. */
  function connect(tag: string): Client {
    const frames: Envelope[] = [];
    const lifecycle: LifecycleEvent[] = [];
    const stream = createStream({
      url: `${base()}/api/events`,
      scopeUrl: `${base()}/api/events/scope`,
      initialScope: [],
      headers: { "SSE-Client": tag },
      versions: createVersionMap(),
      visibility,
      online,
      onFrame(frame: Frame) {
        frames.push(JSON.parse(frame.data) as Envelope);
      },
      onLifecycle(ev) {
        lifecycle.push(ev);
      },
      revalidate: () => Promise.resolve(),
    });
    stream.start();
    cleanups.push(() => {
      stream.stop();
    });
    return { stream, frames, lifecycle };
  }

  function helloed(c: { readonly lifecycle: readonly LifecycleEvent[] }): boolean {
    return c.lifecycle.some((e) => e.kind === "hello");
  }

  function refused(c: { readonly lifecycle: readonly LifecycleEvent[] }): LifecycleEvent[] {
    return c.lifecycle.filter((e) => e.kind === "scope_refused" || e.kind === "auth_lost");
  }

  it("a stream scoped to broadcasts receives no run's log; one scoped to run R receives R's only", async () => {
    const chatTab = connect("e2e_run_scope_chat_tab");
    const runTab = connect("e2e_run_scope_run_tab_");
    await until(() => helloed(chatTab) && helloed(runTab), "both hellos");
    const r = runID();
    const s = runID();
    await runTab.stream.setScope([`run:${r}`]);

    await publishRunLog(r);
    await publishRunLog(s);
    const chat = await createChat();
    await until(() => created(chatTab.frames, chat), "the chat tab's chat_created");
    await until(() => created(runTab.frames, chat), "the run tab's chat_created");

    expect(runLog(chatTab.frames, r)).toEqual([]);
    expect(runLog(chatTab.frames, s)).toEqual([]);
    expect(runLog(runTab.frames, r)).toEqual(HOOK_RUN_LOG);
    expect(runLog(runTab.frames, s)).toEqual([]);
    expect(refused(chatTab)).toEqual([]);
    expect(refused(runTab)).toEqual([]);
  });

  interface Tab {
    readonly attachment: TabAttachment;
    readonly frames: Envelope[];
    readonly lifecycle: LifecycleEvent[];
  }

  /** The fixture's origin under the bundle's relative routes, as a page's fetch resolves them. */
  function stubSameOriginFetch(): void {
    const real = globalThis.fetch;
    vi.stubGlobal("fetch", (input: RequestInfo | URL, init?: RequestInit) =>
      real(typeof input === "string" && input.startsWith("/") ? `${base()}${input}` : input, init),
    );
  }

  /** One tab on marotte's worker host, over a port pair standing in for the SharedWorker. */
  function attachTab(host: WorkerHost, tag: string): Tab {
    const frames: Envelope[] = [];
    const lifecycle: LifecycleEvent[] = [];
    const spawn = (): SharedWorkerLike => {
      const channel = new MessageChannel();
      host.attach(channel.port2);
      return { port: channel.port1, addEventListener: () => undefined };
    };
    const attachment = attachToWorker({
      spawn,
      fallback: () => {
        throw new Error("the tab fell back to its own stream");
      },
      onFrame(frame) {
        frames.push(JSON.parse(frame.data) as Envelope);
      },
      onLifecycle(ev) {
        lifecycle.push(ev);
      },
      visibility,
      online,
      tag,
      supported: true,
      scope: [],
    });
    cleanups.push(() => {
      attachment.detach("unload");
    });
    return { attachment, frames, lifecycle };
  }

  it("worker host: the chat tab's port draws no frame of run R while the run tab's draws R's", async () => {
    stubSameOriginFetch();
    const host = createSSEHost();
    cleanups.push(() => {
      host.close();
    });
    const chatTab = attachTab(host, "e2e_run_scope_worker_tab");
    const runTab = attachTab(host, "e2e_run_scope_worker_tab");
    await until(
      () => host.stream().state().kind === "open" && chatTab.attachment.state()?.kind === "open",
      "the host's stream open",
    );
    const r = runID();
    const s = runID();
    // A Cursor, not null: marotte's host knows scopes.
    await expect(runTab.attachment.setScope([`run:${r}`])).resolves.not.toBeNull();

    await publishRunLog(r);
    await publishRunLog(s);
    const chat = await createChat();
    await until(() => created(chatTab.frames, chat), "the chat tab's chat_created");
    await until(() => created(runTab.frames, chat), "the run tab's chat_created");

    expect(runLog(chatTab.frames, r)).toEqual([]);
    expect(runLog(chatTab.frames, s)).toEqual([]);
    expect(runLog(runTab.frames, r)).toEqual(HOOK_RUN_LOG);
    expect(runLog(runTab.frames, s)).toEqual([]);
    for (const tab of [chatTab, runTab]) {
      expect(tab.lifecycle.filter((e) => e.kind === "deliver_failed")).toEqual([]);
      expect(refused(tab)).toEqual([]);
    }
  });
});
