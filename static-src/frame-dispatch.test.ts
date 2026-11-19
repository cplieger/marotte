// The frame-batch check: the canned app at the chat-stream view, fed the recorded streamed turn at
// its arrival instants, one frame per task, with the page's animation frames run by the test at
// each 60 Hz display slot.
import type * as Reactive from "@cplieger/reactive";
import { afterAll, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";

import { bootCanned, type CannedApp } from "./__test-helpers__/canned-app.js";
import { framesBudgetMs, testTimeoutFor } from "./__test-helpers__/frame-budget.js";
import { installLayoutSpy, type LayoutSpy } from "./__test-helpers__/layout-spy.js";
import type { CannedStream } from "./__test-helpers__/canned.js";
import type * as Actions from "./actions/index.js";
import type * as Bus from "./bus.js";
import type * as Frames from "./frame-dispatch.js";
import type * as Scroll from "./scroll.js";
import type * as Store from "./store.js";
import type * as Versions from "./subject-versions.js";
import type * as Transport from "./transport.js";

const SLOT_MS = 1000 / 60;
/** The display slots the recorded turn's frames arrive in, at most; the replay case asserts it. */
const REPLAY_SLOTS = 187;
/** The replay waits one real rendering update per display slot: under a throttled rAF the default
 *  timeout cuts it off mid-turn, and every later case then runs inside that open turn. */
const REPLAY_TIMEOUT_MS = testTimeoutFor(framesBudgetMs(REPLAY_SLOTS));

type Envelope = { readonly type: string; readonly chat_id: string } & Record<string, unknown>;
type Recorded = CannedStream["frames"][number];

/** A message task, which a faked `setTimeout` cannot give. */
const task = (): Promise<void> =>
  new Promise((resolve) => {
    const hop = new MessageChannel();
    hop.port1.onmessage = (): void => {
      hop.port1.close();
      resolve();
    };
    hop.port2.postMessage(null);
  });

/** The page's animation frames, run only when the test runs one. A callback a frame queues runs
 *  in the next, as the HTML event loop orders them. */
interface FrameGate {
  run(now: number): void;
  restore(): void;
}

function gateFrames(): FrameGate {
  const real = { request: window.requestAnimationFrame, cancel: window.cancelAnimationFrame };
  let queue = new Map<number, FrameRequestCallback>();
  let next = 1;
  window.requestAnimationFrame = (cb) => {
    const handle = next++;
    queue.set(handle, cb);
    return handle;
  };
  window.cancelAnimationFrame = (handle) => {
    queue.delete(handle);
  };
  return {
    run(now) {
      const due = queue;
      queue = new Map();
      const thrown: unknown[] = [];
      for (const cb of due.values()) {
        try {
          cb(now);
        } catch (e) {
          thrown.push(e);
        }
      }
      if (thrown.length > 0) {
        throw new AggregateError(thrown, "an animation frame callback threw");
      }
    },
    restore() {
      window.requestAnimationFrame = real.request;
      window.cancelAnimationFrame = real.cancel;
    },
  };
}

const realFrame = window.requestAnimationFrame.bind(window);

/** One browser rendering update and the task after it, so the layout a slot starts on is clean. */
const rendered = (): Promise<void> =>
  new Promise((resolve) => {
    realFrame(() => {
      void task().then(resolve);
    });
  });

// Imported once the app has booted: the scroll owner reads the document at import, and the
// API layer binds `fetch` at import, which the boot stubs.
let reactive: typeof Reactive;
let actions: typeof Actions;
let bus: typeof Bus;
let frameDispatch: typeof Frames;
let scroll: typeof Scroll;
let store: typeof Store;
let versions: typeof Versions;
let transport: typeof Transport;
let app: CannedApp;
let frames: CannedStream["frames"];
let chatID: string;
let turnID: string;
let gate: FrameGate;
let spy: LayoutSpy;
/** Runs of an effect reading the painted chat's transcript version: the transcript's passes. */
let passes = 0;
let stopPasses: () => void;
/** The spy log's index ranges the frame batch's hold covered. */
const holds: [number, number][] = [];

beforeAll(async () => {
  app = await bootCanned("chat-stream");
  [reactive, actions, bus, frameDispatch, scroll, store, versions, transport] = await Promise.all([
    import("@cplieger/reactive"),
    import("./actions/index.js"),
    import("./bus.js"),
    import("./frame-dispatch.js"),
    import("./scroll.js"),
    import("./store.js"),
    import("./subject-versions.js"),
    import("./transport.js"),
  ]);
  frames = app.hub.stream("prompt-shown", "chat-stream", "live").frames;
  const first = frames[0]?.envelope as Envelope | undefined;
  chatID = first?.chat_id ?? "";
  turnID = String((first?.["payload"] as { entry?: { turn?: string } } | undefined)?.entry?.turn);
  stopPasses = reactive.effect(() => {
    reactive.touch(store.messagesVersionOf(chatID));
    passes++;
  });
  gate = gateFrames();
  spy = installLayoutSpy();
  frameDispatch.setFrameHold((fn) => {
    const from = spy.logged();
    scroll.heldMutation(fn);
    holds.push([from, spy.logged()]);
  });
}, 120_000);

beforeEach(() => {
  app.restub();
});

afterAll(() => {
  frameDispatch.setFrameHold(scroll.heldMutation);
  spy.dispose();
  gate.restore();
  stopPasses();
  vi.useRealTimers();
});

/** The frames grouped by the 60 Hz slot of their arrival instant, in arrival order. */
function slots(recorded: readonly Recorded[]): Recorded[][] {
  const out: Recorded[][] = [];
  let key = Number.NaN;
  for (const f of recorded) {
    const k = Math.floor(f.at / SLOT_MS);
    if (k !== key) {
      out.push([]);
      key = k;
    }
    out.at(-1)?.push(f);
  }
  return out;
}

interface SlotCount {
  readonly at: number;
  readonly types: readonly string[];
  readonly passes: number;
  /** Reads of the scroll owner's scroller that forced a layout, made by the slot's frame deliveries
   *  and the frame batch's hold. Observer and scroll-event callbacks run after the browser's own
   *  layout, which the spy cannot see, so it would count their reads as forced. */
  readonly scrollerLayouts: number;
  /** The slot's frame stamps, each with the version the map held before its animation frame ran
   *  and after. */
  readonly stamps: readonly StampSeen[];
}

interface StampSeen {
  readonly version: string;
  readonly before: string | undefined;
  readonly after: string | undefined;
}

interface Stamp {
  readonly kind: string;
  readonly ref: string;
  readonly version: string;
}

function heldVersion(stamp: Stamp): string | undefined {
  return versions
    .versionMap()
    .snapshot()
    .held.find((h) => h.kind === stamp.kind && h.ref === stamp.ref)?.version;
}

/** Deliver one slot's frames, each in its own task, then run the page's animation frame. */
async function deliverSlot(slot: readonly Recorded[], from: number): Promise<SlotCount> {
  const first = slot[0];
  if (first === undefined) {
    throw new Error("an empty slot");
  }
  const wait = from + first.at - Date.now();
  if (wait > 0) {
    await vi.advanceTimersByTimeAsync(wait);
  }
  app.hub.tick(Date.now());
  await rendered();
  const passesBefore = passes;
  const ranges: [number, number][] = [];
  holds.length = 0;
  spy.begin();
  for (const f of slot) {
    const from = spy.logged();
    app.hub.send(f.envelope, f.id);
    ranges.push([from, spy.logged()]);
    await task();
  }
  const stamped = slot.flatMap((f) => {
    const subject = (f.envelope as Envelope)["subject"] as Stamp | undefined;
    return subject === undefined ? [] : [subject];
  });
  const before = stamped.map(heldVersion);
  gate.run(Date.now());
  ranges.push(...holds);
  await task();
  const stamps = stamped.map((s, i) => ({
    version: s.version,
    before: before[i],
    after: heldVersion(s),
  }));
  const { log } = spy.end();
  const scroller = scroll.getScrollEl();
  app.release();
  const counted = ranges.flatMap(([from, to]) => log.slice(from, to));
  return {
    at: first.at,
    types: slot.map((f) => (f.envelope as Envelope).type),
    passes: passes - passesBefore,
    scrollerLayouts: counted.filter((a) => a.forced && a.target === scroller).length,
    stamps,
  };
}

/** `envelope` as the frame of another turn, so a case streams a turn of its own. */
function retarget(envelope: unknown, turn: string, seq?: number): Envelope {
  const out = JSON.parse(JSON.stringify(envelope).replaceAll(turnID, turn)) as Envelope;
  const entry = (out["payload"] as { entry?: { seq?: number } } | undefined)?.entry;
  if (seq !== undefined && entry !== undefined) {
    entry.seq = seq;
  }
  return out;
}

function recordedOf(type: string): Recorded {
  const f = frames.find((x) => (x.envelope as Envelope).type === type);
  if (f === undefined) {
    throw new Error(`the recorded turn carries no ${type}`);
  }
  return f;
}

/** Open a turn of its own and apply it, then deliver its close and leave it queued: no animation
 *  frame runs after it. */
async function queueClosedTurn(turn: string): Promise<void> {
  app.hub.send(retarget(recordedOf("turn_opened").envelope, turn), undefined);
  await task();
  gate.run(Date.now());
  await task();
  expect(store.isBusy(chatID), `${turn} opened`).toBe(true);
  app.hub.send(retarget(recordedOf("turn_closed").envelope, turn, 1), undefined);
  await task();
}

const PENDING: Stamp = { kind: "pending", ref: "", version: "" };

function deliverPending(items: readonly Envelope[], version: string): void {
  app.hub.send(
    {
      type: "pending_snapshot",
      chat_id: "",
      payload: { items },
      subject: { ...PENDING, version },
    },
    undefined,
  );
}

/** Greet the stream the page opened last, and run its animation frames until `applied` holds:
 *  the stream drains the connect hook's frames only once the hello's revalidation settled. */
async function greet(applied: () => boolean): Promise<void> {
  app.hub.tick(Date.now());
  await vi.advanceTimersByTimeAsync(100);
  app.hub.tick(Date.now());
  for (let i = 0; i < 50 && !applied(); i++) {
    await task();
    gate.run(Date.now());
    app.release();
  }
  await task();
}

describe("frame-batch", () => {
  it(
    "the recorded turn paints at most once and forces at most two scroller layouts per display frame",
    async () => {
      expect(document.visibilityState).toBe("visible");
      const grouped = slots(frames);
      expect(grouped.length, "the recording's slots within the replay timeout").toBeLessThanOrEqual(
        REPLAY_SLOTS,
      );
      const from = Date.now();
      const counts: SlotCount[] = [];
      for (const slot of grouped) {
        counts.push(await deliverSlot(slot, from));
      }
      const turn = store.get(chatID)?.turns.get(turnID);
      expect(turn?.entries.at(-1)?.kind, "every recorded frame applied").toBe("turn_close");
      const over = counts.filter((c) => c.passes > 1 || c.scrollerLayouts > 2);
      expect(over, "display frames over one pass or two scroller layouts").toEqual([]);
      const stamps = counts.flatMap((c) => c.stamps);
      const recordedStamps = frames.filter(
        (f) => (f.envelope as Envelope)["subject"] !== undefined,
      );
      expect(recordedStamps.length, "the recorded turn carries a stamped frame").toBeGreaterThan(0);
      expect(stamps, "every stamped frame checked").toHaveLength(recordedStamps.length);
      expect(
        stamps.filter((s) => s.before === s.version || s.after !== s.version),
        "stamps observed before their frame's animation frame, or not after it",
      ).toEqual([]);
    },
    REPLAY_TIMEOUT_MS,
  );

  it("a handler that throws leaves the frame's stamp unobserved and re-reads its subject", async () => {
    const errors = vi.spyOn(console, "error").mockImplementation(() => undefined);
    const off = bus.onSSE("chat_updated", () => {
      throw new Error("handler fault");
    });
    try {
      const recorded = recordedOf("chat_updated").envelope as Envelope;
      const subject = recorded["subject"] as { kind: string; ref: string; version: string };
      const version = String(Number(subject.version) + 1_000);
      const reads = app.counted.length;
      app.hub.send({ ...recorded, subject: { ...subject, version } }, undefined);
      await task();
      gate.run(Date.now());
      await task();
      app.release();
      await task();
      const held = versions
        .versionMap()
        .snapshot()
        .held.find((h) => h.kind === subject.kind && h.ref === subject.ref);
      expect(held?.version, "the refused frame's stamp").not.toBe(version);
      expect(app.counted.slice(reads).map((c) => c.route)).toContain("GET /api/chats");
      expect(errors).toHaveBeenCalled();
    } finally {
      off();
      errors.mockRestore();
    }
  });

  it("a pending set whose item handler throws leaves the set's stamp unobserved and asks a fresh hello for it", async () => {
    const errors = vi.spyOn(console, "error").mockImplementation(() => undefined);
    let thrown = 0;
    const off = bus.onSSE("chat_updated", () => {
      thrown++;
      throw new Error("item handler fault");
    });
    let asks = 0;
    const offAsks = bus.onSSE("run_asks", () => {
      asks++;
    });
    const sets: (string | undefined)[] = [];
    const offSets = bus.onSSE("pending_snapshot", (_chat, _set, subject) => {
      sets.push(subject?.version);
    });
    try {
      const item = recordedOf("chat_updated").envelope as Envelope;
      const version = "9000000";
      const opened = app.net.connections.length;
      deliverPending([item], version);
      await task();
      gate.run(Date.now());
      await task();
      app.release();
      expect(thrown, "the item reached its handler").toBe(1);
      expect(errors).toHaveBeenCalledWith(
        '[bus] SSE handler error for "chat_updated":',
        expect.any(Error),
      );
      expect(heldVersion(PENDING), "the partly applied set's stamp").not.toBe(version);
      await task();
      expect(app.net.connections.length, "one fresh hello asked").toBe(opened + 1);
      await greet(() => asks > 0);
      expect(asks, "the fresh hello's set applied").toBe(1);
      expect(sets, "the refused set, then the fresh hello's").toHaveLength(2);
      expect(heldVersion(PENDING), "the fresh hello's stamp").toBe(sets[1]);
    } finally {
      off();
      offAsks();
      offSets();
      errors.mockRestore();
    }
  });

  it("a pending set refused again on the fresh hello it asked for keeps what applied", async () => {
    const errors = vi.spyOn(console, "error").mockImplementation(() => undefined);
    const warns = vi.spyOn(console, "warn").mockImplementation(() => undefined);
    let thrown = 0;
    const off = bus.onSSE("pending_snapshot", () => {
      thrown++;
      throw new Error("set handler fault");
    });
    try {
      const opened = app.net.connections.length;
      deliverPending([], "9000001");
      await task();
      gate.run(Date.now());
      await task();
      app.release();
      await task();
      expect(app.net.connections.length, "one fresh hello asked").toBe(opened + 1);
      await greet(() => thrown > 1);
      expect(thrown, "the refused set and the fresh hello's").toBe(2);
      expect(warns).toHaveBeenCalledWith(
        "sse: pending refused while its fresh hello is asked or already ran, keeping what applied",
      );
      expect(app.net.connections.length, "no second hello").toBe(opened + 1);
    } finally {
      off();
      errors.mockRestore();
      warns.mockRestore();
    }
    deliverPending([], "9000002");
    await task();
    gate.run(Date.now());
    await task();
    app.release();
    expect(heldVersion(PENDING), "a set that applied").toBe("9000002");
  });

  it("a chat's status refused across live frames asks one fresh hello per chat until it applies", async () => {
    const errors = vi.spyOn(console, "error").mockImplementation(() => undefined);
    const warns = vi.spyOn(console, "warn").mockImplementation(() => undefined);
    const other = "chat-status-other";
    const faulty = new Set([chatID, other]);
    const off = bus.onSSE("chat_status", (chat) => {
      if (faulty.has(chat)) {
        throw new Error("status handler fault");
      }
    });
    let snapshots = 0;
    const offSnapshots = bus.onSSE("status_snapshot", () => {
      snapshots++;
    });
    let version = 9_100_000;
    const deliverStatus = async (chat: string): Promise<void> => {
      app.hub.send(
        {
          type: "chat_status",
          chat_id: chat,
          payload: { status: "in_progress", description: "" },
          subject: { kind: "status", ref: "", version: String(version++) },
        },
        undefined,
      );
      await task();
      gate.run(Date.now());
      await task();
      app.release();
      await task();
    };
    try {
      const opened = app.net.connections.length;
      await deliverStatus(chatID);
      expect(app.net.connections.length, "the first refusal asks a fresh hello").toBe(opened + 1);
      await greet(() => snapshots > 0);
      expect(snapshots, "the fresh hello's status set applied").toBe(1);
      for (let i = 0; i < 3; i++) {
        await deliverStatus(chatID);
      }
      expect(app.net.connections.length, "later refusals of that chat stay").toBe(opened + 1);
      expect(warns).toHaveBeenCalledWith(
        "sse: status refused while its fresh hello is asked or already ran, keeping what applied",
      );
      await deliverStatus(other);
      expect(app.net.connections.length, "another chat's refusal asks its own").toBe(opened + 2);
      await greet(() => snapshots > 1);
      faulty.delete(chatID);
      await deliverStatus(chatID);
      expect(app.net.connections.length, "a status that applied asks nothing").toBe(opened + 2);
      faulty.add(chatID);
      await deliverStatus(chatID);
      expect(app.net.connections.length, "the applied status re-armed the chat").toBe(opened + 3);
      await greet(() => snapshots > 2);
    } finally {
      off();
      offSnapshots();
      errors.mockRestore();
      warns.mockRestore();
    }
  });

  it("transport.send posts after a turn_closed that arrived before it", async () => {
    await queueClosedTurn("t-frame-batch-send");
    let busyAtPost: boolean | undefined;
    app.net.respond("/api/command", () => {
      busyAtPost = store.isBusy(chatID);
      return new Response("{}", { headers: { "content-type": "application/json" } });
    });
    await transport.send({ type: "cancel" as const, chat_id: chatID });
    expect(busyAtPost).toBe(false);
  });

  it("a command whose fetch rejects reports its failure after a turn_closed that arrived during it", async () => {
    app.net.respond("/api/command", async () => {
      await queueClosedTurn("t-frame-batch-send-rejected");
      throw new TypeError("Failed to fetch");
    });
    let busyAtNotice: boolean | undefined;
    const off = bus.onBus(bus.BUS_COMMAND_FAILED, () => {
      busyAtNotice = store.isBusy(chatID);
    });
    try {
      const result = await transport.send({ type: "cancel" as const, chat_id: chatID });
      expect(result.ok, "the rejected fetch is a failure").toBe(false);
      expect(busyAtNotice, "busy when the failure notice ran").toBe(false);
    } finally {
      off();
    }
  });

  const wrappers = [
    {
      wrapper: "defineAction",
      make: (seen: (busy: boolean) => void) =>
        actions.defineAction<undefined, undefined>({
          name: "frame-batch.define",
          run: () => {
            seen(store.isBusy(chatID));
            return Promise.resolve(undefined);
          },
        }),
    },
    {
      wrapper: "apiAction",
      make: (seen: (busy: boolean) => void) =>
        actions.apiAction<undefined, undefined>({
          name: "frame-batch.api",
          request: () => {
            seen(store.isBusy(chatID));
            return { method: "POST", path: "/api/frame-batch-probe" };
          },
        }),
    },
    {
      wrapper: "transportAction",
      make: (seen: (busy: boolean) => void) =>
        actions.transportAction<undefined>({
          name: "frame-batch.transport",
          command: () => {
            seen(store.isBusy(chatID));
            return { type: "cancel" as const, chat_id: chatID };
          },
        }),
    },
  ];

  it.each(wrappers)(
    "an action built by $wrapper reads the store after a turn_closed that arrived before it",
    async ({ wrapper, make }) => {
      await queueClosedTurn(`t-frame-batch-${wrapper}`);
      app.net.respond("/api/frame-batch-probe", () => new Response(null, { status: 204 }));
      app.net.respond("/api/command", () => new Response("{}", { status: 200 }));
      let busyInBody: boolean | undefined;
      await make((busy) => {
        busyInBody = busy;
      }).dispatch(undefined);
      expect(busyInBody).toBe(false);
    },
  );
});
