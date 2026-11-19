// The whole app, with its shipped stylesheet, booted at a tier 2 scenario's view (perf/views.ts,
// tests/perf/plan.json) against the canned set (static-src/perf/canned/) and a fake shell server,
// on a faked clock.
import { page } from "vitest/browser";
import { vi } from "vitest";

import indexHtml from "../../static/index.html?raw";
import planText from "../../tests/perf/plan.json?raw";
import factsText from "../perf/canned/facts.json?raw";
import { routeOf } from "../perf/route-of.ts";
import { instanceFacts, type PerfView, type SeededFacts, VIEWS } from "../perf/views.ts";
import type { CannedAnswer, CannedConnect, CannedStream } from "./canned.js";
import { FakeShellSocket, mountShellCSS } from "./shell-socket.js";
import { createScriptedFetch, type ScriptedFetch } from "./sse-fetch.js";

export type WindowName = "cold" | "rest" | "live" | "away" | "back";

// view.mjs's own: how long a window waits with no new request to call the page settled.
export const QUIET_MS = 2_500;
const STEP_MS = 100;
const SETTLE_CAP_MS = 60_000;

const answerFiles = import.meta.glob<string>("../perf/canned/answers/*.json", {
  query: "?raw",
  import: "default",
  eager: true,
});
const frameFiles = import.meta.glob<string>("../perf/canned/frames/*.json", {
  query: "?raw",
  import: "default",
  eager: true,
});

const plan = JSON.parse(planText) as {
  scenarios: { [scenario: string]: { drivers?: { [window: string]: string } } };
};
const facts = JSON.parse(factsText) as SeededFacts;

/** A request body as canned.py keys it: JSON with every `op_id` dropped and its keys sorted. */
function requestKey(body: unknown): string {
  const drop = (v: unknown): unknown => {
    if (Array.isArray(v)) {
      return v.map(drop);
    }
    if (typeof v !== "object" || v === null) {
      return v;
    }
    return Object.fromEntries(
      Object.entries(v)
        .filter(([k]) => k !== "op_id")
        .sort(([a], [b]) => (a < b ? -1 : a > b ? 1 : 0))
        .map(([k, x]) => [k, drop(x)]),
    );
  };
  return body === undefined || body === null ? "" : JSON.stringify(drop(body));
}

function parsedBody(text: string | null): unknown {
  if (text === null) {
    return null;
  }
  try {
    return JSON.parse(text) as unknown;
  } catch {
    return text;
  }
}

const byRequest = new Map<string, CannedAnswer>();
// A route's answer for a URL or body the recording never sent, held only when every recorded
// answer of the route is the same, so a request-dependent answer is never served for another
// request.
const byRoute = new Map<string, CannedAnswer | "varies">();
for (const text of Object.values(answerFiles)) {
  const a = JSON.parse(text) as CannedAnswer;
  const method = a.route.slice(0, a.route.indexOf(" "));
  byRequest.set(`${method} ${a.url} ${requestKey(a.request)}`, a);
  const route = routeOf(method, a.url, location.origin);
  const held = byRoute.get(route);
  if (held === undefined) {
    byRoute.set(route, a);
  } else if (
    held !== "varies" &&
    JSON.stringify([held.status, held.body]) !== JSON.stringify([a.status, a.body])
  ) {
    byRoute.set(route, "varies");
  }
}
const connect = JSON.parse(
  frameFiles["../perf/canned/frames/connect.json"] ?? "{}",
) as CannedConnect;
const streams = Object.entries(frameFiles)
  .filter(([f]) => f.includes("/frames/stream."))
  .map(([, text]) => JSON.parse(text) as CannedStream);

const sameTopics = (a: readonly string[] | null, b: readonly string[] | null): boolean =>
  a === null || b === null ? a === b : a.length === b.length && a.every((t, i) => t === b[i]);

/** The canned hub: a hello and the recorded connect frames on each stream the page opens, a
 *  keepalive on the hello's period, the scope, liveness and digest endpoints, and a driver's
 *  recorded frames. The three endpoints answer from the hub's own state and are counted like any
 *  read. The digest answers nothing changed: the canned answers are the server's whole state, and
 *  it moves only by the frames the page itself receives. */
class Hub {
  private readonly topics = new Map<number, readonly string[] | null>();
  private greeted = 0;
  private readonly seen: number[] = [];
  private nextBeat = 0;
  private offset = String(connect.hello["head"] ?? "0");
  private readonly beatMs = Number(connect.hello["keepalive_ms"] ?? 15_000);
  private readonly beatEvent = String(connect.hello["keepalive_event"] ?? "heartbeat");

  constructor(
    private readonly net: ScriptedFetch,
    counted: Counted[],
  ) {
    // Answered synchronously, as a body read would resolve on real time and move the counts.
    const own = (prefix: string, reply: (body: string | null) => unknown): void => {
      net.respond(prefix, (req) => {
        const value = reply(req.body);
        const text = value === null ? null : JSON.stringify(value);
        counted.push({
          route: routeOf(req.method, req.url, location.origin),
          bytes: text === null ? 0 : new TextEncoder().encode(text).length,
        });
        return text === null
          ? new Response(null, { status: 204 })
          : new Response(text, { headers: { "content-type": "application/json" } });
      });
    };
    own("/api/events/alive", () => null);
    own("/api/sync", (body) => {
      const { subjects } = JSON.parse(body ?? "{}") as { subjects?: unknown[] };
      return {
        epoch: connect.hello["epoch"],
        head: this.offset,
        must_refetch: false,
        checked: subjects?.length ?? 0,
        changed: [],
        removed: [],
      };
    });
    own("/api/events/scope", (body) => {
      const req = JSON.parse(body ?? "{}") as { conn?: string; topics?: string[] | null };
      const index = this.net.connections.findIndex(
        (c, i) => this.connName(i) === req.conn && !c.aborted(),
      );
      if (index >= 0) {
        this.topics.set(index, req.topics ?? null);
      }
      return { epoch: connect.hello["epoch"], offset: this.offset };
    });
  }

  private connName(i: number): string {
    return `conn-${String(i).padStart(17, "0")}`;
  }

  /** Greets each stream first seen a step or more ago, and sends a keepalive when one is due. */
  tick(now: number): void {
    for (let i = this.seen.length; i < this.net.connections.length; i++) {
      this.seen.push(now);
    }
    for (; this.greeted < this.net.connections.length; this.greeted++) {
      const conn = this.net.connections[this.greeted];
      if (conn === undefined) {
        continue;
      }
      if (now - (this.seen[this.greeted] ?? now) < STEP_MS) {
        break;
      }
      const header = conn.headers.get("SSE-Topics");
      this.topics.set(this.greeted, header === null ? null : (JSON.parse(header) as string[]));
      conn.hello({ ...connect.hello, head: this.offset, conn: this.connName(this.greeted) });
      for (const f of connect.frames) {
        conn.frame(f.envelope, f.id);
      }
      this.nextBeat = now + this.beatMs;
    }
    if (now >= this.nextBeat) {
      for (const c of this.open()) {
        c.named(this.beatEvent, "{}");
      }
      this.nextBeat = now + this.beatMs;
    }
  }

  /** The streams greeted and still open: a frame or keepalive before the hello fails the stream. */
  private open() {
    return this.net.connections.slice(0, this.greeted).filter((c) => !c.aborted());
  }

  /** The topics the page's open stream holds (null: everything). */
  held(): readonly string[] | null {
    for (let i = this.greeted - 1; i >= 0; i--) {
      if (this.net.connections[i]?.aborted() === false) {
        return this.topics.get(i) ?? null;
      }
    }
    return null;
  }

  /** The recorded stream `driver` carried in `window` to a page holding today's topics: this
   *  scenario's own, else the one canned.py kept for every scenario that streamed the same. */
  stream(driver: string, scenario: string, window: WindowName): CannedStream {
    const held = this.held();
    const fits = streams.filter(
      (x) => x.driver === driver && x.window === window && sameTopics(x.topics, held),
    );
    const s = fits.find((x) => x.scenario === scenario) ?? fits[0];
    if (s === undefined) {
      throw new Error(
        `the canned set records no ${driver} stream for topics ${JSON.stringify(held)}: run tests/perf/record-canned.sh`,
      );
    }
    return s;
  }

  send(envelope: unknown, id: string | undefined): void {
    if (id !== undefined) {
      // An SSE id is `<epoch>:<offset>`; the hello, scope and digest answers name the offset.
      this.offset = id.slice(id.lastIndexOf(":") + 1);
    }
    for (const c of this.open()) {
      c.frame(envelope, id);
    }
  }
}

interface Counted {
  readonly route: string;
  readonly bytes: number;
}

function answer(
  req: { url: string; method: string; body: string | null },
  counted: Counted[],
  missing: Set<string>,
): Response {
  const url = new URL(req.url, location.origin);
  const rel = url.pathname + url.search;
  const route = routeOf(req.method, rel, location.origin);
  const own = byRequest.get(`${req.method} ${rel} ${requestKey(parsedBody(req.body))}`);
  const shared = byRoute.get(route);
  const a = own ?? (shared === "varies" ? undefined : shared);
  if (a === undefined) {
    missing.add(route);
    counted.push({ route, bytes: 0 });
    return new Response(null, { status: 404 });
  }
  const text = a.body === null ? null : JSON.stringify(a.body);
  counted.push({ route, bytes: text === null ? 0 : new TextEncoder().encode(text).length });
  return new Response(text, {
    status: a.status ?? 200,
    headers: text === null ? {} : { "content-type": "application/json" },
  });
}

function mount(): void {
  const doc = new DOMParser().parseFromString(indexHtml, "text/html");
  const app = doc.getElementById("app");
  if (app === null) {
    throw new Error("index.html has no #app");
  }
  const modals = [...doc.querySelectorAll("body > [data-modal]")];
  document.body.replaceChildren(...[app, ...modals].map((n) => document.importNode(n, true)));
}

const nextFrame = (): Promise<void> =>
  new Promise((resolve) => {
    requestAnimationFrame(() => {
      resolve();
    });
  });

// views.ts's `ready` is page source Playwright evaluates in the page; this frame is the page.
// eslint-disable-next-line @typescript-eslint/no-implied-eval
const evaluate = (source: string): boolean => Boolean(new Function(`return (${source});`)());

export function clickRow(selector: string): void {
  const row = document.querySelector(selector);
  if (row === null) {
    throw new Error(`no ${selector} to click`);
  }
  const pointer = { bubbles: true, isPrimary: true, pointerType: "mouse" };
  row.dispatchEvent(new PointerEvent("pointerdown", pointer));
  row.dispatchEvent(new PointerEvent("pointerup", pointer));
  (row as HTMLElement).click();
}

export const tabRow = (id: string | null | undefined): string => {
  if (id === null || id === undefined) {
    throw new Error("the view names no tab row");
  }
  return `#tab-list [data-tab-id="${CSS.escape(id)}"]`;
};

/** The booted app: its view, the hub its stream reads, every counted request, and the clock. */
export interface CannedApp {
  readonly scenario: string;
  readonly view: PerfView;
  readonly drivers: { readonly [window: string]: string };
  readonly hub: Hub;
  /** The scripted network: a later `respond` answers its prefix in place of the canned set. */
  readonly net: ScriptedFetch;
  readonly counted: readonly Counted[];
  /** Routes the canned set had no answer for. */
  readonly missing: ReadonlySet<string>;
  /** Advance the faked clock `ms`, let the page's frames run, greet new streams, answer reads. */
  step(ms: number): Promise<void>;
  /** `step` in 100 ms steps across `ms`. */
  advance(ms: number): Promise<void>;
  /** Step until no request for 2.5 s (and, with `needReady`, the view's `ready` holds). */
  settle(needReady: boolean): Promise<void>;
  /** Answer every read asked so far. */
  release(): void;
  /** Replay `driver`'s recorded frames for `window` at their instants. */
  replay(window: WindowName): Promise<void>;
  toggleShell(): void;
  /** Install the network, storage and socket stubs again, for a case after the one that booted:
   *  the config's `unstubGlobals` removes them before each case. */
  restub(): void;
}

/** Boot the app at `scenario`'s view and settle it (the cold window). The caller restores real
 *  timers when it is done. */
export async function bootCanned(scenario: string): Promise<CannedApp> {
  const spec = VIEWS[scenario];
  const drivers = plan.scenarios[scenario]?.drivers;
  if (spec === undefined || drivers === undefined) {
    throw new Error(`views.ts or plan.json names no ${scenario}`);
  }
  const view: PerfView = spec(instanceFacts(facts));
  await page.viewport(1440, 900);
  vi.useFakeTimers({
    toFake: ["setTimeout", "clearTimeout", "setInterval", "clearInterval", "Date"],
    now: connect.at,
  });
  const net = createScriptedFetch();
  const counted: Counted[] = [];
  const missing = new Set<string>();
  // Each answer is held to the end of the step it was asked in, after that step's frames are
  // parsed, so whether a frame's read joins one already out never depends on real time.
  const held: (() => void)[] = [];
  net.respond("/", (req) => {
    const res = answer(req, counted, missing);
    return new Promise<Response>((resolve) => {
      held.push(() => {
        resolve(res);
      });
    });
  });
  const hub = new Hub(net, counted);
  const restub = (): void => {
    vi.stubGlobal("fetch", net.fetch);
    // A fresh profile's empty IndexedDB, answered at once: the boot snapshot read
    // (boot-snapshot.ts) races the chat list, and a real-time read would decide that race.
    vi.stubGlobal("indexedDB", undefined);
    vi.stubGlobal("WebSocket", FakeShellSocket);
  };
  FakeShellSocket.reset();
  restub();
  history.replaceState(null, "", view.path);
  mountShellCSS();
  mount();

  const release = (): void => {
    for (const r of held.splice(0)) {
      r();
    }
  };
  // The hello's place, as the real server's recording orders a cold load: a step after the stream
  // opens, after the boot's first reads applied (so the `connected` frame re-reads the chat
  // list, as the live server does) and before the reads asked since answer (the tab set, then a run deep
  // link's `open_tab`, which names the parent chat the `connected` frame's live runs carry).
  // A whole step between an answer and the hello keeps that order out of real time.
  const step = async (ms: number): Promise<void> => {
    await vi.advanceTimersByTimeAsync(ms);
    await nextFrame();
    hub.tick(Date.now());
    await nextFrame();
    release();
    await nextFrame();
  };
  const advance = async (ms: number): Promise<void> => {
    for (let spent = 0; spent < ms; spent += STEP_MS) {
      await step(Math.min(STEP_MS, ms - spent));
    }
  };
  const settle = async (needReady: boolean): Promise<void> => {
    let seen = counted.length;
    let quiet = 0;
    for (let spent = 0; spent < SETTLE_CAP_MS; spent += STEP_MS) {
      await step(STEP_MS);
      quiet = counted.length === seen ? quiet + STEP_MS : 0;
      seen = counted.length;
      if (quiet >= QUIET_MS && (!needReady || evaluate(view.ready))) {
        return;
      }
    }
    const last = counted.slice(-6).map((c) => c.route);
    throw new Error(
      `${scenario}: the page did not settle in ${String(SETTLE_CAP_MS)} ms (ready ${String(evaluate(view.ready))}, at ${location.pathname}, last reads ${last.join(", ")}, unanswered ${[...missing].join(", ") || "none"})`,
    );
  };
  const replay = async (window: WindowName): Promise<void> => {
    const driver = drivers[window];
    if (driver === undefined) {
      return;
    }
    const from = Date.now();
    for (const f of hub.stream(driver, scenario, window).frames) {
      const wait = from + f.at - Date.now();
      if (wait > 0) {
        await advance(wait);
      }
      hub.send(f.envelope, f.id);
    }
  };
  const toggleShell = (): void => {
    clickRow("#shell-btn");
  };

  await import("../app.js");
  document.dispatchEvent(new Event("DOMContentLoaded"));
  if (view.shell === true) {
    await settle(false);
    toggleShell();
  }
  await settle(true);
  return {
    scenario,
    view,
    drivers,
    hub,
    net,
    counted,
    missing,
    step,
    advance,
    settle,
    release,
    replay,
    toggleShell,
    restub,
  };
}
