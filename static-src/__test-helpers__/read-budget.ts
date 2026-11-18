// The read-budget check: the whole app, with its shipped stylesheet, booted against the canned set
// (static-src/perf/canned/) and a fake shell server, driven through a tier 2 scenario's windows
// (perf/views.ts, tests/perf/plan.json) on a faked clock, counting every request by route and the
// largest answer per route in each window.
import { commands, page } from "vitest/browser";
import { expect, it, vi } from "vitest";

import indexHtml from "../../static/index.html?raw";
import planText from "../../tests/perf/plan.json?raw";
import factsText from "../perf/canned/facts.json?raw";
import { routeOf } from "../perf/route-of.ts";
import { instanceFacts, type PerfView, type SeededFacts, VIEWS } from "../perf/views.ts";
import type { CannedAnswer, CannedConnect, CannedStream } from "./canned.js";
import { FakeShellSocket, mountShellCSS } from "./shell-socket.js";
import { createScriptedFetch, type ScriptedFetch } from "./sse-fetch.js";

declare module "vitest/internal/browser" {
  interface BrowserCommands {
    readBudget(view: string, counts: ViewCounts): Promise<{ recording: boolean; budget: unknown }>;
  }
}

const WINDOWS = ["cold", "rest", "live", "away", "back"] as const;
type WindowName = (typeof WINDOWS)[number];

interface RouteCount {
  count: number;
  maxBytes: number;
}

/** One window's requests by route, and the page's stream topics at its end (null: everything). */
interface WindowCounts {
  readonly routes: { [route: string]: RouteCount };
  readonly topics: readonly string[] | null;
}

type ViewCounts = { readonly [W in WindowName]: WindowCounts };

/** The tree read every run surface replaced with its shapes; no window may issue it. */
const RUN_TREE_ROUTE = "GET /api/runs/{run}";

// view.mjs's own: its rest window, and how long a window waits with no new request to call the
// page settled.
const REST_MS = 15_000;
const QUIET_MS = 2_500;
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
 *  recorded frames. The three endpoints are the stream's own traffic, which a page's SharedWorker
 *  sends in production, so they are answered here and never counted. The digest answers nothing
 *  changed: the canned answers are the server's whole state, and it moves only by the frames the
 *  page itself receives. */
class Hub {
  private readonly topics = new Map<number, readonly string[] | null>();
  private greeted = 0;
  private readonly seen: number[] = [];
  private nextBeat = 0;
  private offset = String(connect.hello["head"] ?? "0");
  private readonly beatMs = Number(connect.hello["keepalive_ms"] ?? 15_000);
  private readonly beatEvent = String(connect.hello["keepalive_event"] ?? "heartbeat");

  constructor(private readonly net: ScriptedFetch) {
    net.respond("/api/events/alive", () => new Response(null, { status: 204 }));
    net.respond("/api/sync", (req) => {
      const { subjects } = JSON.parse(req.body ?? "{}") as { subjects?: unknown[] };
      return Response.json({
        epoch: connect.hello["epoch"],
        head: this.offset,
        must_refetch: false,
        checked: subjects?.length ?? 0,
        changed: [],
        removed: [],
      });
    });
    net.respond("/api/events/scope", (req) => {
      const body = JSON.parse(req.body ?? "{}") as { conn?: string; topics?: string[] | null };
      const index = this.net.connections.findIndex(
        (c, i) => this.connName(i) === body.conn && !c.aborted(),
      );
      if (index >= 0) {
        this.topics.set(index, body.topics ?? null);
      }
      return Response.json({ epoch: connect.hello["epoch"], offset: this.offset });
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

function clickRow(selector: string): void {
  const row = document.querySelector(selector);
  if (row === null) {
    throw new Error(`no ${selector} to click`);
  }
  const pointer = { bubbles: true, isPrimary: true, pointerType: "mouse" };
  row.dispatchEvent(new PointerEvent("pointerdown", pointer));
  row.dispatchEvent(new PointerEvent("pointerup", pointer));
  (row as HTMLElement).click();
}

const tabRow = (id: string | null | undefined): string => {
  if (id === null || id === undefined) {
    throw new Error("the view names no tab row");
  }
  return `#tab-list [data-tab-id="${CSS.escape(id)}"]`;
};

/** Boots the app at `scenario`'s view and runs its windows, returning each window's counts. */
async function measure(scenario: string): Promise<{ counts: ViewCounts; missing: Set<string> }> {
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
  const hub = new Hub(net);
  vi.stubGlobal("fetch", net.fetch);
  // A fresh profile's empty IndexedDB, answered at once: the boot snapshot read
  // (boot-snapshot.ts) races the chat list, and a real-time read would decide that race.
  vi.stubGlobal("indexedDB", undefined);
  FakeShellSocket.reset();
  vi.stubGlobal("WebSocket", FakeShellSocket);
  history.replaceState(null, "", view.path);
  mountShellCSS();
  mount();

  // The hello's place, as the real server's recording orders a cold load: a step after the stream
  // opens, after the boot's first reads applied (the `connected` frame then re-reads the chat
  // list, MEASURED.md X3) and before the reads asked since answer (the tab set, then a run deep
  // link's `open_tab`, which names the parent chat the `connected` frame's live runs carry).
  // A whole step between an answer and the hello keeps that order out of real time.
  const step = async (ms: number): Promise<void> => {
    await vi.advanceTimersByTimeAsync(ms);
    await nextFrame();
    hub.tick(Date.now());
    await nextFrame();
    for (const release of held.splice(0)) {
      release();
    }
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

  const out: Partial<Record<WindowName, WindowCounts>> = {};
  const measureWindow = async (name: WindowName, body: () => Promise<void>): Promise<void> => {
    const from = counted.length;
    await body();
    const routes: { [route: string]: RouteCount } = {};
    for (const c of counted.slice(from)) {
      const r = (routes[c.route] ??= { count: 0, maxBytes: 0 });
      r.count++;
      r.maxBytes = Math.max(r.maxBytes, c.bytes);
    }
    out[name] = { routes, topics: hub.held() };
  };

  await measureWindow("cold", async () => {
    await import("../app.js");
    document.dispatchEvent(new Event("DOMContentLoaded"));
    if (view.shell === true) {
      await settle(false);
      toggleShell();
    }
    await settle(true);
  });
  await advance(QUIET_MS);
  await measureWindow("rest", () => advance(REST_MS));
  await measureWindow("live", async () => {
    await replay("live");
    await settle(false);
  });
  await measureWindow("away", async () => {
    if (view.shell !== true) {
      clickRow(tabRow(view.away));
      await settle(false);
    } else if (drivers["away"] === "shell-closed") {
      toggleShell();
    }
    await replay("away");
    await settle(false);
  });
  await measureWindow("back", async () => {
    if (view.shell === true) {
      toggleShell();
    } else {
      clickRow(view.opener ?? tabRow(view.tab));
    }
    await settle(true);
  });
  vi.useRealTimers();
  return { counts: out as ViewCounts, missing };
}

/** The read-budget case for one tier 2 scenario: its windows' counts equal its entry in
 *  perf/read-budgets.json, which READ_BUDGET_RECORD=1 rewrites from this run. */
export function readBudgetCase(scenario: string): void {
  it(`${scenario} reads what its budget holds in every window`, { timeout: 180_000 }, async () => {
    const { counts, missing } = await measure(scenario);
    expect(
      [...missing],
      "routes the canned set has no answer for: run tests/perf/record-canned.sh",
    ).toEqual([]);
    for (const w of WINDOWS) {
      expect(Object.keys(counts[w].routes), `${w} read the whole run tree`).not.toContain(
        RUN_TREE_ROUTE,
      );
    }
    const { recording, budget } = await commands.readBudget(scenario, counts);
    if (!recording) {
      expect(
        counts,
        `perf/read-budgets.json's ${scenario}; after a deliberate change, rerun with READ_BUDGET_RECORD=1`,
      ).toEqual(budget);
    }
  });
}
