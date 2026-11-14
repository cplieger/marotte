// One rep of one measured cell on a seeded, serving instance (tests/perf/instance/serve.sh): a
// scenario of tests/perf/plan.json in one engine and width, its windows in order, written to --out
// as one JSON object that tests/perf/measure/summarize.py reads. Exits 1 when a window did not
// settle, a driver did not finish, or a Chromium page's stream scope was never seen.
// Usage: node view.mjs --instance DIR --plan PLAN --scenario NAME --engine chromium|webkit|server
//          --width desktop|phone|none --rep N --out FILE [--windows cold,rest] [--profile]
import { spawn } from "node:child_process";
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { parseArgs } from "node:util";

import playwright from "playwright";

import { decodeShares } from "./attrprobe.mjs";
import { DRIVERS, VIEWS, instanceFacts } from "./views.mjs";

const LIVE_PORT = "9847";
const TICK_MS = 10; // USER_HZ, the unit of /proc/<pid>/stat's utime and stime on Linux
const REST_MS = 15000;
const QUIET_MS = 2500;
const SLOW_PING_MS = 50;
const STREAM_WINDOWS = new Set(["live", "away"]);
const WORKER_SCRIPT = "**/chunks/sse-worker-*.js";
const VIEWPORTS = {
  desktop: { viewport: { width: 1440, height: 900 }, deviceScaleFactor: 1 },
  phone: {
    viewport: { width: 390, height: 844 },
    deviceScaleFactor: 3,
    isMobile: true,
    hasTouch: true,
  },
};
const CAPTURE = fileURLToPath(new URL("../../tests/perf/measure/sse-capture.py", import.meta.url));

const { values: args } = parseArgs({
  options: {
    instance: { type: "string" },
    plan: { type: "string" },
    scenario: { type: "string" },
    engine: { type: "string" },
    width: { type: "string" },
    rep: { type: "string", default: "1" },
    out: { type: "string" },
    windows: { type: "string" },
    profile: { type: "boolean", default: false },
  },
});
for (const k of ["instance", "plan", "scenario", "engine", "width", "out"]) {
  if (!args[k]) {
    throw new Error(`view.mjs: --${k} is required`);
  }
}
const readJSON = (f) => JSON.parse(fs.readFileSync(f, "utf8"));
const inst = readJSON(path.join(args.instance, "instance.json"));
if (String(inst.port) === LIVE_PORT || String(inst.forge_port) === LIVE_PORT) {
  throw new Error(`view.mjs: ${LIVE_PORT} is the live marotte`);
}
const facts = instanceFacts(inst.seeded);
const scenario = readJSON(args.plan).scenarios[args.scenario];
if (!scenario) {
  throw new Error(`view.mjs: plan.json has no scenario ${args.scenario}`);
}
const windows = new Set(args.windows ? args.windows.split(",") : scenario.windows);
const origin = `http://127.0.0.1:${String(inst.port)}`;
const trigDir = path.join(args.instance, "trigger");
const logDir = path.join(args.instance, "logs");
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const withTimeout = (p, ms) =>
  Promise.race([
    p.then(
      (v) => ({ ok: true, v }),
      (e) => ({ ok: false, err: String(e) }),
    ),
    sleep(ms).then(() => ({ ok: false, err: "timeout" })),
  ]);

const procTicks = (statPath) => {
  try {
    const f = fs.readFileSync(statPath, "utf8").split(") ")[1].split(" ");
    return (Number(f[11]) + Number(f[12])) * TICK_MS;
  } catch {
    return null;
  }
};
const processCpuMs = (pid) => procTicks(`/proc/${String(pid)}/stat`);
// A renderer's main thread is the thread whose id is the process id.
const mainThreadCpuMs = (pid) => procTicks(`/proc/${String(pid)}/task/${String(pid)}/stat`);

/** The engine's page process among this process's descendants (Chromium's largest renderer,
 *  WebKit's web process) with its RSS in MB. */
function renderer(engine) {
  const kids = new Map();
  for (const d of fs.readdirSync("/proc")) {
    if (!/^\d+$/.test(d)) {
      continue;
    }
    try {
      const ppid = Number(fs.readFileSync(`/proc/${d}/stat`, "utf8").split(") ")[1].split(" ")[1]);
      kids.set(ppid, [...(kids.get(ppid) ?? []), Number(d)]);
    } catch {
      // The process exited while the walk read it.
    }
  }
  const all = [];
  const walk = (p) => {
    for (const k of kids.get(p) ?? []) {
      all.push(k);
      walk(k);
    }
  };
  walk(process.pid);
  let best = null;
  for (const pid of all) {
    let cmd, rss;
    try {
      cmd = fs.readFileSync(`/proc/${String(pid)}/cmdline`, "utf8").replaceAll("\0", " ");
      const line = fs
        .readFileSync(`/proc/${String(pid)}/smaps_rollup`, "utf8")
        .split("\n")
        .find((l) => l.startsWith("Rss:"));
      rss = Number(line?.split(/\s+/)[1] ?? 0) / 1024;
    } catch {
      continue;
    }
    const isPage =
      engine === "chromium"
        ? cmd.includes("--type=renderer") && !cmd.includes("--extension-process")
        : cmd.includes("WPEWebProcess") || cmd.includes("WebKitWebProcess");
    if (isPage && (best === null || rss > best.rssMB)) {
      best = { pid, rssMB: Math.round(rss) };
    }
  }
  return best;
}

/** JSON lines of a log whose `t` (epoch seconds, or ms for the capture) falls in [from, to] ms. */
function linesIn(file, from, to, scale = 1000) {
  let text;
  try {
    text = fs.readFileSync(file, "utf8");
  } catch {
    return [];
  }
  return text
    .split("\n")
    .filter(Boolean)
    .map((l) => JSON.parse(l))
    .filter((e) => e.t * scale >= from && e.t * scale <= to);
}

const ID_SEGMENTS = [
  [/\/(steps|turns|tools)\/[^/]+/g, "/$1/{id}"],
  [/wf_[0-9a-z]+/g, "{run}"],
  [/c-[0-9a-f]{32}/g, "{chat}"],
  [/b[0-9a-f]{31}/g, "{tab}"],
  [/sess_[0-9a-f-]+/g, "{session}"],
];
/** A request's route: method, path with its ids named, and its query keys (`view` keeps its value,
 *  since it picks the answer's shape). tests/perf/measure/servercpu.py replays one URL per route. */
function routeOf(method, url) {
  const u = new URL(url, origin);
  let p = u.pathname;
  for (const [re, name] of ID_SEGMENTS) {
    p = p.replace(re, name);
  }
  const keys = [...new Set(u.searchParams.keys())]
    .sort()
    .map((k) => (k === "view" ? `view=${u.searchParams.get(k) ?? ""}` : k));
  return `${method} ${p}${keys.length ? `?${keys.join("&")}` : ""}`;
}

// The stream helpers a driver calls; each resolves {done, ms, ...} when its stream ended.
let triggers = 0;
const helpers = (page, wsBytesIn) => ({
  async trigger(op, capMs = 300000) {
    const f = path.join(
      trigDir,
      `trigger-${String(Date.now())}${String(triggers++).padStart(3, "0")}-${op.op}.json`,
    );
    fs.writeFileSync(f, JSON.stringify(op));
    const from = Date.now();
    while (!fs.existsSync(`${f}.done`) && Date.now() - from < capMs) {
      await sleep(200);
    }
    return { op: op.op, done: fs.existsSync(`${f}.done`), ms: Date.now() - from };
  },
  // A user prompt through the command API; the fake engine streams the turn prompt-shape.json
  // shapes on the real session/prompt path and drops prompt-<session>-<ms>.done when it ended.
  async prompt(chatID, shape) {
    fs.writeFileSync(path.join(trigDir, "prompt-shape.json"), JSON.stringify(shape));
    const sid = facts.sessions[chatID];
    const marks = () => fs.readdirSync(trigDir).filter((n) => n.startsWith(`prompt-${sid}-`));
    const before = new Set(marks());
    const from = Date.now();
    const r = await fetch(`${origin}/api/command`, {
      method: "POST",
      headers: { "Content-Type": "application/json", "Idempotency-Key": `perf-${String(from)}` },
      body: JSON.stringify({
        type: "prompt",
        chat_id: chatID,
        payload: { text: "Continue with the next step.", message_id: `m-perf-${String(from)}` },
      }),
    });
    if (!r.ok) {
      return { prompt: chatID, done: false, status: r.status, ms: Date.now() - from };
    }
    let done = false;
    while (!done && Date.now() - from < 300000) {
      await sleep(200);
      done = marks().some((n) => !before.has(n));
    }
    return { prompt: chatID, done, ms: Date.now() - from };
  },
  async forgeChurn(spec, holdMs) {
    const f = path.join(trigDir, "forge-churn.json");
    fs.writeFileSync(f, JSON.stringify(spec));
    await sleep(holdMs);
    fs.rmSync(f, { force: true });
    return { churn: spec, done: true, ms: holdMs };
  },
  toggleShell: () => page.evaluate(() => document.getElementById("shell-btn")?.click()),
  // Typing must reach the terminal, never the chat composer, which would send it as a prompt.
  async shell(cmd, lines, onTyped) {
    const inShell = async () =>
      (
        await withTimeout(
          page.evaluate(() => !!document.activeElement?.closest("#shell-panel")),
          10000,
        )
      ).v === true;
    if (!(await inShell())) {
      const box = await page
        .$eval("#shell-panel", (e) => {
          const r = e.getBoundingClientRect();
          return { x: r.x + r.width / 2, y: r.y + r.height * 0.6 };
        })
        .catch(() => null);
      if (box) {
        await page.mouse.click(box.x, box.y);
      }
    }
    if (!(await inShell())) {
      return { cmd, done: false, error: "the terminal is not focused" };
    }
    const before = wsBytesIn();
    await page.keyboard.type(cmd);
    await page.keyboard.press("Enter");
    if (onTyped) {
      await onTyped();
    }
    const from = Date.now();
    let last = before;
    let lastAt = from;
    // emit.py writes more than 60 bytes a line, so the output is whole past lines * 60 bytes.
    for (;;) {
      await sleep(250);
      const b = wsBytesIn();
      if (b !== last) {
        [last, lastAt] = [b, Date.now()];
      }
      if (Date.now() - lastAt > 3000 && b - before > lines * 60) {
        break;
      }
      if (Date.now() - from > 300000) {
        return { cmd, done: false, ms: Date.now() - from };
      }
    }
    return { cmd, done: true, ms: Date.now() - from, wsBytesIn: last - before };
  },
});

function base() {
  return {
    schema: 1,
    scenario: args.scenario,
    scale: `x${String(facts.scale)}`,
    engine: args.engine,
    width: args.width,
    rep: args.rep,
    profiled: args.profile,
    binary: inst.binary,
    started: Date.now(),
    windows: {},
    failures: [],
    errors: [],
  };
}
const write = (doc) => fs.writeFileSync(args.out, JSON.stringify(doc, null, 1));

/** The server-only cell: the drive with no stream listener, marotte's CPU from its trigger to one
 *  second after it ended. */
async function serverCell() {
  const doc = base();
  const x = helpers(null, () => 0);
  const c0 = processCpuMs(inst.pid);
  const start = Date.now();
  const drive = await DRIVERS[scenario.drivers.drive](x, facts, null);
  await sleep(1000);
  const c1 = processCpuMs(inst.pid);
  doc.windows.drive = {
    start,
    end: Date.now(),
    drive,
    serverCpuMs: c0 !== null && c1 !== null ? c1 - c0 : null,
  };
  if (!drive.done) {
    doc.failures.push("drive: the driver did not finish");
  }
  return doc;
}

const probeScript = (ready) => `(() => {
  if (window.__perf) return;
  const at = (t) => performance.timeOrigin + t;
  const isReady = () => { try { return Boolean(${ready}); } catch { return false; } };
  const p = { longTasks: [], late: [], frames: [], readyAt: null, running: false };
  const watch = () => {
    if (p.readyAt !== null) return;
    if (document.readyState !== "loading" && isReady()) { p.readyAt = at(performance.now()); return; }
    requestAnimationFrame(watch);
  };
  p.armReady = () => { p.readyAt = null; requestAnimationFrame(watch); };
  let timer = 0, last = 0;
  const frame = (t) => { if (!p.running) return; p.frames.push(at(t)); requestAnimationFrame(frame); };
  p.start = () => {
    p.running = true;
    last = performance.now();
    timer = setInterval(() => {
      const now = performance.now();
      if (now - last - 10 > 2) p.late.push([at(last), now - last - 10]);
      last = now;
    }, 10);
    requestAnimationFrame(frame);
  };
  p.stop = () => { p.running = false; clearInterval(timer); };
  p.take = () => ({ longTasks: p.longTasks.splice(0), late: p.late.splice(0), frames: p.frames.splice(0), readyAt: p.readyAt });
  try {
    new PerformanceObserver((l) => { for (const e of l.getEntries()) p.longTasks.push([at(e.startTime), e.duration]); })
      .observe({ type: "longtask", buffered: true });
  } catch { /* an engine without the longtask entry type */ }
  Object.defineProperty(window, "__perf", { value: p });
  p.armReady();
})()`;

/** The page's stream scope, read from its own requests: the SSE-Topics header of each connect and
 *  each scope POST's topics, mirrored onto the measuring stream client (sse-capture.py). */
function scopeMirror(doc) {
  const sseFile = `${args.out}.sse.jsonl`;
  const scopeFile = `${args.out}.scope.json`;
  const stopFile = `${args.out}.sse.stop`;
  const errFile = `${sseFile}.err`;
  for (const f of [sseFile, scopeFile, stopFile, errFile]) {
    fs.rmSync(f, { force: true });
  }
  let child = null;
  let exitCode = null;
  const note = (topics) => {
    doc.scopes.push({ t: Date.now(), topics });
    fs.writeFileSync(`${scopeFile}.tmp`, JSON.stringify(topics));
    fs.renameSync(`${scopeFile}.tmp`, scopeFile);
    if (child) {
      return;
    }
    const err = fs.openSync(errFile, "w");
    child = spawn(
      "python3",
      [
        CAPTURE,
        ...["--port", String(inst.port), "--out", sseFile],
        ...["--scope-file", scopeFile, "--stop-file", stopFile],
      ],
      { stdio: ["ignore", "ignore", err] },
    );
    fs.closeSync(err);
    child.once("exit", (code, signal) => (exitCode = code ?? signal));
  };
  return {
    sseFile,
    request(url, method, headers, body) {
      const p = new URL(url, origin).pathname;
      if (p === "/api/events" && method === "GET") {
        const h = Object.entries(headers).find(([k]) => k.toLowerCase() === "sse-topics");
        note(h ? JSON.parse(h[1]) : null);
      } else if (p === "/api/events/scope" && method === "POST" && body) {
        note(JSON.parse(body).topics ?? null);
      }
    },
    async stop() {
      if (!child) {
        return;
      }
      const early = exitCode;
      fs.writeFileSync(stopFile, "");
      const exited = exitCode === null ? new Promise((r) => child.once("exit", r)) : null;
      if (exited && !(await withTimeout(exited, 10000)).ok) {
        child.kill();
        doc.failures.push("the stream client did not stop within 10 s");
      }
      fs.rmSync(stopFile, { force: true });
      if (early !== null || (exitCode !== null && exitCode !== 0)) {
        const why = fs.readFileSync(errFile, "utf8").trim().split("\n").at(-1) ?? "";
        doc.failures.push(
          `the stream client ${early === null ? "exited" : "ended before the window closed"} ` +
            `(${String(exitCode)}): ${why}`,
        );
      }
      let posted = [];
      try {
        posted = readJSON(`${sseFile}.scopes.json`);
      } catch {
        doc.failures.push("the stream client wrote no record of its scope changes");
      }
      for (const p of posted.filter((q) => q.status < 200 || q.status > 299)) {
        doc.failures.push(`the stream client's scope change was refused (${String(p.status)})`);
      }
    },
  };
}

/** Chromium's view of the SharedWorker that holds the stream: Playwright reports no request of a
 *  shared worker, so the browser's CDP session attaches to it and reads its network. The worker's
 *  script is held until the attachment can see its first request. */
async function watchWorker(browser, context, onRequest) {
  const cdp = await browser.newBrowserCDPSession();
  let attached;
  const ready = new Promise((r) => (attached = r));
  let id = 0;
  const send = (sessionId, method) =>
    cdp.send("Target.sendMessageToTarget", {
      sessionId,
      message: JSON.stringify({ id: ++id, method, params: {} }),
    });
  cdp.on("Target.targetCreated", (e) => {
    if (e.targetInfo.type === "shared_worker") {
      void cdp.send("Target.attachToTarget", { targetId: e.targetInfo.targetId, flatten: false });
    }
  });
  cdp.on("Target.attachedToTarget", (e) => {
    if (e.targetInfo.type === "shared_worker") {
      void send(e.sessionId, "Network.enable").then(attached);
    }
  });
  cdp.on("Target.receivedMessageFromTarget", (e) => {
    const m = JSON.parse(e.message);
    if (m.method !== "Network.requestWillBeSent") {
      return;
    }
    const r = m.params.request;
    onRequest(r.url, r.method, r.headers, r.postData);
  });
  await cdp.send("Target.setDiscoverTargets", { discover: true });
  await context.route(WORKER_SCRIPT, async (route) => {
    await withTimeout(ready, 10000);
    await route.continue();
  });
}

async function browserCell() {
  const doc = { ...base(), scopes: [] };
  const view = VIEWS[args.scenario](facts);
  const engine = args.engine;
  const browser = await playwright[engine].launch({ headless: true });
  const context = await browser.newContext({
    ...VIEWPORTS[args.width],
    serviceWorkers: "block",
    bypassCSP: true,
  });
  await context.addInitScript(probeScript(view.ready));
  const scope = scopeMirror(doc);
  if (engine === "chromium") {
    await watchWorker(browser, context, scope.request);
  }
  const page = await context.newPage();
  const cdp = engine === "chromium" ? await context.newCDPSession(page) : null;
  if (cdp) {
    await cdp.send("Performance.enable", { timeDomain: "threadTicks" });
  }
  const metrics = async () => {
    if (!cdp) {
      return null;
    }
    const r = await withTimeout(cdp.send("Performance.getMetrics"), 120000);
    return r.ok ? Object.fromEntries(r.v.metrics.map((m) => [m.name, m.value])) : null;
  };
  const heapMB = async () => {
    if (!cdp) {
      return null;
    }
    await withTimeout(cdp.send("HeapProfiler.collectGarbage"), 60000);
    const r = await withTimeout(cdp.send("Runtime.getHeapUsage"), 60000);
    return r.ok ? Math.round((r.v.usedSize / 1048576) * 10) / 10 : null;
  };

  // The page's requests (Chromium's worker requests arrive through watchWorker only).
  const reqs = [];
  const live = new Map();
  context.on("request", (r) => {
    scope.request(r.url(), r.method(), r.headers(), r.postData());
    const p = new URL(r.url()).pathname;
    if (!p.startsWith("/api/") || p.startsWith("/api/events")) {
      return;
    }
    const rec = { route: routeOf(r.method(), r.url()), url: r.url(), start: Date.now(), end: null };
    live.set(r, rec);
    reqs.push(rec);
  });
  const finish = async (r, failed) => {
    const rec = live.get(r);
    if (!rec) {
      return;
    }
    live.delete(r);
    try {
      const res = failed ? null : await r.response();
      rec.status = failed ? "failed" : (res?.status() ?? null);
      rec.bytes = res ? (await res.body()).length : 0;
    } catch {
      rec.bytes = 0;
    }
    rec.end = Date.now();
  };
  context.on("requestfinished", (r) => void finish(r, false));
  context.on("requestfailed", (r) => void finish(r, true));
  let wsIn = 0;
  let shellSocket = false;
  page.on("websocket", (ws) => {
    if (ws.url().includes("/api/shell/")) {
      shellSocket = true;
    }
    ws.on("framereceived", (f) => {
      wsIn += typeof f.payload === "string" ? Buffer.byteLength(f.payload) : f.payload.length;
    });
  });
  page.on("pageerror", (e) => doc.errors.push(String(e).slice(0, 300)));
  page.on("console", (m) => {
    if (m.type() === "error") {
      doc.errors.push(`console: ${m.text().slice(0, 240)}`);
    }
  });
  const x = helpers(page, () => wsIn);
  const evaluate = async (fn, arg) => (await withTimeout(page.evaluate(fn, arg), 30000)).v;

  // Settled: the page answered every ping within SLOW_PING_MS for QUIET_MS, no read is in flight,
  // and, when asked, the view painted.
  async function settle(from, cap, needReady) {
    let quietSince = from;
    for (;;) {
      await sleep(250);
      const t = Date.now();
      const ping = await withTimeout(page.evaluate("1"), 60000);
      const now = Date.now();
      if (!ping.ok || now - t > SLOW_PING_MS) {
        quietSince = now;
      }
      let ok = now - quietSince >= QUIET_MS && live.size === 0;
      if (ok && needReady) {
        ok = (await evaluate(() => window.__perf.readyAt !== null)) === true;
      }
      if (ok) {
        return { settled: true, ms: now - from };
      }
      if (now - from >= cap) {
        const at = await evaluate(() => ({
          path: window.location.pathname,
          ready: window.__perf.readyAt !== null,
        }));
        return { settled: false, ms: now - from, inflight: live.size, ...at };
      }
    }
  }
  async function clickTab(id) {
    return (
      (await evaluate((tabId) => {
        const row = document.querySelector(`#tab-list [data-tab-id="${CSS.escape(tabId)}"]`);
        if (!row) {
          return false;
        }
        const pointer = { bubbles: true, isPrimary: true, pointerType: "mouse" };
        row.dispatchEvent(new PointerEvent("pointerdown", pointer));
        row.dispatchEvent(new PointerEvent("pointerup", pointer));
        row.click();
        return true;
      }, id)) === true
    );
  }
  async function openShell() {
    await x.toggleShell();
    for (let i = 0; i < 100 && !shellSocket; i++) {
      await sleep(100);
    }
    await sleep(1500);
  }

  async function measure(name, body) {
    const profiling = args.profile && cdp && STREAM_WINDOWS.has(name);
    let prof = null;
    if (profiling) {
      prof = await context.newCDPSession(page);
      await prof.send("Profiler.enable");
      await prof.send("Profiler.setSamplingInterval", { interval: 200 });
      await prof.send("Profiler.start");
    }
    const frames = STREAM_WINDOWS.has(name);
    if (frames) {
      await evaluate(() => window.__perf.start());
    }
    const page0 = renderer(engine)?.pid ?? null;
    const m0 = await metrics();
    const main0 = page0 ? mainThreadCpuMs(page0) : null;
    const server0 = processCpuMs(inst.pid);
    const start = Date.now();
    const extra = await body();
    const end = Date.now();
    const server1 = processCpuMs(inst.pid);
    const main1 = page0 ? mainThreadCpuMs(page0) : null;
    const m1 = await metrics();
    if (frames) {
      await evaluate(() => window.__perf.stop());
    }
    const w = { start, end, ms: end - start, ...extra };
    if (prof) {
      const p = await withTimeout(prof.send("Profiler.stop"), 300000);
      if (p.ok) {
        w.decode = await decodeShares(p.v.profile);
      }
      w.layouts = m1 && m0 ? m1.LayoutCount - m0.LayoutCount : null;
      w.styleRecalcs = m1 && m0 ? m1.RecalcStyleCount - m0.RecalcStyleCount : null;
    }
    const procMain = main0 !== null && main1 !== null ? main1 - main0 : null;
    w.procMainCpuMs = procMain;
    w.cpuMs = m0 && m1 ? Math.round((m1.TaskDuration - m0.TaskDuration) * 1000) : procMain;
    w.serverCpuMs = server0 !== null && server1 !== null ? server1 - server0 : null;
    const snap = await evaluate(
      (sel) => ({
        ...window.__perf.take(),
        dom: document.getElementsByTagName("*").length,
        viewDom: sel ? (document.querySelector(sel)?.getElementsByTagName("*").length ?? 0) : null,
      }),
      view.container,
    );
    if (!snap) {
      doc.failures.push(`${name}: the page did not answer after the window`);
      return w;
    }
    const inWin = (t) => t >= start && t <= end;
    const tasks = snap.longTasks.filter(([t]) => inWin(t)).map(([, d]) => d);
    const gaps = snap.late.filter(([t]) => inWin(t)).map(([, d]) => d + 10);
    const shown = snap.frames.filter(inWin);
    const deltas = shown
      .slice(1)
      .map((t, i) => t - shown[i])
      .sort((a, b) => a - b);
    w.dom = snap.dom;
    w.viewDom = snap.viewDom;
    w.longTaskMs = cdp ? Math.round(Math.max(0, ...tasks)) : null;
    if (frames) {
      w.frameP95Ms = deltas.length
        ? Math.round(deltas[Math.min(deltas.length - 1, Math.floor(0.95 * deltas.length))] * 10) /
          10
        : null;
      w.heartbeatGapMs = Math.round(Math.max(0, ...gaps));
    }
    if (extra.readyFrom !== undefined) {
      w.paintedMs = snap.readyAt === null ? null : Math.round(snap.readyAt - extra.readyFrom);
    }
    w.heapMB = await heapMB();
    w.rssMB = renderer(engine)?.rssMB ?? null;
    const routes = {};
    for (const r of reqs.filter((q) => inWin(q.start))) {
      const e = (routes[r.route] ??= { count: 0, maxBytes: 0, url: r.url });
      e.count++;
      e.maxBytes = Math.max(e.maxBytes, r.bytes ?? 0);
    }
    w.routes = routes;
    w.kiroCli = linesIn(path.join(logDir, "fake-kiro.jsonl"), start, end).filter(
      (e) => "argv" in e,
    ).length;
    w.gitFetch = linesIn(path.join(logDir, "git.jsonl"), start, end).length;
    w.sseFrames = cdp
      ? linesIn(scope.sseFile, start, end, 1).filter((f) => !f.event.startsWith("sse:")).length
      : null;
    if (w.settle && !w.settle.settled) {
      const st = w.settle;
      doc.failures.push(
        `${name}: did not settle (at ${String(st.path)}, ready ${String(st.ready)}, ` +
          `${String(st.inflight)} reads in flight)`,
      );
    }
    if (w.drive && !w.drive.done) {
      doc.failures.push(`${name}: the driver did not finish`);
    }
    if (windows.has(name)) {
      doc.windows[name] = w;
    }
    write(doc);
    return w;
  }
  const drive = (window) => DRIVERS[scenario.drivers[window]](x, facts, view);

  // The cold window always runs: every later window starts from the loaded page. The renderer is
  // put on the origin first, with a read that answers 404, so the view's navigation keeps the
  // process the CDP counters and the /proc reads follow.
  await withTimeout(page.goto(`${origin}/api/perf-origin`), 30000);
  await measure("cold", async () => {
    const readyFrom = Date.now();
    const nav = await withTimeout(page.goto(`${origin}${view.path}`, { timeout: 150000 }), 150000);
    if (!nav.ok) {
      doc.failures.push(`cold: the page did not load (${nav.err})`);
    }
    if (view.shell) {
      await openShell();
    }
    return { readyFrom, settle: await settle(readyFrom, 150000, true) };
  });
  if (cdp && doc.scopes.length === 0) {
    doc.failures.push("cold: the page opened no stream");
  }
  if (windows.has("rest")) {
    await sleep(QUIET_MS);
    await measure("rest", async () => {
      await sleep(REST_MS);
      return {};
    });
  }
  if (windows.has("live")) {
    await measure("live", async () => {
      const d = await drive("live");
      return { drive: d, settle: await settle(Date.now(), 120000, false) };
    });
  }
  if (windows.has("away") || windows.has("back")) {
    await measure("away", async () => {
      if (!view.shell) {
        const clicked = await clickTab(view.away);
        if (!clicked) {
          doc.failures.push("away: no tab row to click");
        }
        await settle(Date.now(), 60000, false);
      }
      const d = await drive("away");
      return { drive: d, settle: await settle(Date.now(), 120000, false) };
    });
    await measure("back", async () => {
      await evaluate(() => window.__perf.armReady());
      const readyFrom = Date.now();
      if (view.shell) {
        await x.toggleShell();
      } else if (!(await clickTab(view.tab))) {
        doc.failures.push("back: no tab row to click");
      }
      return { readyFrom, settle: await settle(readyFrom, 150000, true) };
    });
  }
  await scope.stop();
  await withTimeout(browser.close(), 20000);
  return doc;
}

const doc = args.engine === "server" ? await serverCell() : await browserCell();
doc.ended = Date.now();
doc.errors = doc.errors.slice(0, 40);
write(doc);
for (const f of doc.failures) {
  console.error(`view.mjs: ${args.scenario} ${f}`);
}
process.exit(doc.failures.length ? 1 : 0);
