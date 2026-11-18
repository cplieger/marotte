// The frame-batch check: the canned app at the chat-stream view, fed the recorded streamed turn at
// its arrival instants, one frame per task, with the page's animation frames run by the test at
// each 60 Hz display slot.
import type * as Reactive from "@cplieger/reactive";
import { afterAll, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";

import { bootCanned, type CannedApp } from "./__test-helpers__/canned-app.js";
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

describe("frame-batch", () => {
  it("the recorded turn paints at most once and forces at most two scroller layouts per display frame", async () => {
    expect(document.visibilityState).toBe("visible");
    const grouped = slots(frames);
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
    const recordedStamps = frames.filter((f) => (f.envelope as Envelope)["subject"] !== undefined);
    expect(recordedStamps.length, "the recorded turn carries a stamped frame").toBeGreaterThan(0);
    expect(stamps, "every stamped frame checked").toHaveLength(recordedStamps.length);
    expect(
      stamps.filter((s) => s.before === s.version || s.after !== s.version),
      "stamps observed before their frame's animation frame, or not after it",
    ).toEqual([]);
  });

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

  it("a pending set whose item handler throws leaves the set's stamp unobserved", async () => {
    const errors = vi.spyOn(console, "error").mockImplementation(() => undefined);
    const off = bus.onSSE("chat_updated", () => {
      throw new Error("item handler fault");
    });
    try {
      const item = recordedOf("chat_updated").envelope as Envelope;
      const version = "9000000";
      app.hub.send(
        {
          type: "pending_snapshot",
          chat_id: "",
          payload: { items: [item] },
          subject: { kind: "pending", ref: "", version },
        },
        undefined,
      );
      await task();
      gate.run(Date.now());
      await task();
      app.release();
      const held = versions
        .versionMap()
        .snapshot()
        .held.find((h) => h.kind === "pending");
      expect(held?.version, "the partly applied set's stamp").not.toBe(version);
    } finally {
      off();
      errors.mockRestore();
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
