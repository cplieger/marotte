// The real settings controls against a model server, for any interleaving of clicks, debounce
// flushes, this page's writes committing one at a time and answering late, another device's writes
// to keys no control owns, a directory's verdict changing with no write, settings events and reads:
// at rest the server, each control and the state the page acts on hold what the user's clicks leave
// and the verdicts of the newest payload the server resolved, and no write carries a key more often
// than the user clicked it. The controls are the notification switch and a kind row (real clicks on
// the shipped markup, each only while a user could click it), the last-model memory and the
// directories panel. Only fetch, the debounce timers and the notification permission are faked.
import { describe, it, expect, beforeAll, afterAll, vi } from "vitest";
import fc from "fast-check";
import indexHtml from "../static/index.html?raw";
import { settingsPayload } from "./__test-helpers__/settings.js";
import type { EffectiveSettings } from "./persist.js";
import { __testResetTracking } from "./persist.js";
import { dispatch } from "./bus.js";
import type { ServerEvent } from "./types.js";
import { byId } from "./dom.js";
import { areNotificationsEnabled, isKindEnabled } from "./notify.js";
import { initNotificationToggles } from "./settings-notifications.js";
import { getLastModel, setLastModel } from "./session-context.js";
import { initAgentDirsUI } from "./agent-dirs-ui.js";
import { additionalDirectories, _resetForTest as resetWorkspace } from "./workspace.js";

type Action =
  | { t: "kind" }
  | { t: "master" }
  | { t: "pick"; model: "A" | "B" }
  | { t: "dir" }
  | { t: "flush" }
  | { t: "commit" }
  | { t: "deliver" }
  | { t: "other" }
  | { t: "drift" }
  | { t: "event" }
  | { t: "serveRead" };

/** The page's starting settings. */
interface Start {
  /** notifications_enabled. */
  on: boolean;
  /** notify_agent_finished, the clicked kind. */
  kind: boolean;
  /** notify_run_outcome, a kind no click moves; with it off the clicked kind can be the last one on. */
  sibling: boolean;
}

interface Held {
  body: Partial<EffectiveSettings>;
  answer: (s: EffectiveSettings | null) => void;
}

/** A payload the server resolved, with when. */
interface Resolved {
  seq: number;
  payload: EffectiveSettings;
}

interface World {
  server: EffectiveSettings;
  /** Whether /a is refused at a read, a verdict config.json does not hold. */
  refusedA: boolean;
  /** How many payloads the server has resolved. */
  resolved: number;
  patches: Held[];
  /** Committed writes' answers, resolved at commit and not delivered yet. */
  answers: { held: Held; resolved: Resolved }[];
  gets: Held[];
  events: number[];
  /** The newest-resolved payload delivered to the page. */
  received: Resolved | null;
  /** What the notification clicks leave: the switch and each kind are separate choices. */
  notify: Notify;
  last: { model?: string; dirA?: boolean };
  /** How many clicks named each settings key, and how many writes of this page carried it. */
  clicked: Map<string, number>;
  carried: Map<string, number>;
}

// on, kind, pr and sibling: notifications_enabled, notify_agent_finished, notify_pr_status and
// notify_run_outcome.
interface Notify {
  on: boolean;
  kind: boolean;
  pr: boolean;
  sibling: boolean;
}

function count(m: Map<string, number>, key: string): void {
  m.set(key, (m.get(key) ?? 0) + 1);
}

let world: World;

function held(queue: Held[], body: Partial<EffectiveSettings>): Promise<Response> {
  return new Promise((resolve) => {
    queue.push({
      body,
      answer: (s) => {
        resolve(
          s === null
            ? new Response(JSON.stringify({ error: "unavailable" }), { status: 500 })
            : new Response(JSON.stringify(s), {
                status: 200,
                headers: { "content-type": "application/json" },
              }),
        );
      },
    });
  });
}

function fakeFetch(input: RequestInfo | URL, init?: RequestInit): Promise<Response> {
  const url = typeof input === "string" ? input : input instanceof URL ? input.href : input.url;
  if (!url.endsWith("/api/settings")) {
    return Promise.resolve(new Response("{}", { status: 404 }));
  }
  const method = init?.method ?? (input instanceof Request ? input.method : "GET");
  if (method !== "PATCH") {
    return held(world.gets, {});
  }
  const body = typeof init?.body === "string" ? (JSON.parse(init.body) as unknown) : {};
  for (const key of Object.keys(body as object)) {
    count(world.carried, key);
  }
  return held(world.patches, body as Partial<EffectiveSettings>);
}

/** One real task, which a faked setTimeout cannot give: a response body is read across tasks. */
function task(): Promise<void> {
  return new Promise((resolve) => {
    const c = new MessageChannel();
    c.port1.onmessage = () => {
      resolve();
    };
    c.port2.postMessage(0);
  });
}

async function settled(): Promise<void> {
  for (let i = 0; i < 4; i++) {
    await task();
    await vi.advanceTimersByTimeAsync(0);
  }
}

function box(): HTMLInputElement {
  return byId<HTMLInputElement>("notify-finished-toggle");
}

function masterBox(): HTMLInputElement {
  return byId<HTMLInputElement>("notify-toggle");
}

function chipRemove(dir: string): HTMLButtonElement | null {
  return byId("agent-dirs-chips").querySelector<HTMLButtonElement>(
    `.chip-remove[aria-label="Remove ${dir}"]`,
  );
}

/** The server resolves a payload as a read or a write's answer does: the stored settings and each
 *  stored directory's verdict at that moment. */
function resolve(w: World): Resolved {
  const refused =
    w.refusedA && w.server.additional_directories.includes("/a")
      ? { "/a": "/a is not a directory" }
      : {};
  return { seq: ++w.resolved, payload: { ...w.server, additional_directories_refused: refused } };
}

function deliver(w: World, h: Held, r: Resolved): void {
  if (w.received === null || r.seq > w.received.seq) {
    w.received = r;
  }
  h.answer(r.payload);
}

function commitOnServer(w: World, body: Partial<EffectiveSettings>): void {
  w.server = { ...w.server, ...body, settings_revision: w.server.settings_revision + 1 };
  w.events.push(w.server.settings_revision);
}

function announce(revision: number): void {
  dispatch({
    type: "settings_updated",
    payload: { settings_epoch: 1, settings_revision: revision },
  } as ServerEvent);
}

async function apply(w: World, a: Action): Promise<void> {
  switch (a.t) {
    case "kind":
      // A user reaches a kind row only while the rows show.
      if (!byId("notify-sub-options").inert) {
        box().click();
        w.notify = { ...w.notify, kind: box().checked };
        count(w.clicked, "notify_agent_finished");
      }
      break;
    case "master":
      // The switch is disabled while a settings write is in flight.
      if (!masterBox().disabled) {
        masterBox().click();
        w.notify = { ...w.notify, on: masterBox().checked };
        count(w.clicked, "notifications_enabled");
      }
      break;
    case "pick":
      setLastModel(a.model);
      w.last.model = a.model;
      count(w.clicked, "last_model");
      break;
    case "dir": {
      if (byId<HTMLInputElement>("agent-dirs-input").disabled) {
        break;
      }
      const remove = chipRemove("/a");
      if (remove === null) {
        byId<HTMLInputElement>("agent-dirs-input").value = "/a";
        byId<HTMLButtonElement>("agent-dirs-add").click();
      } else {
        remove.click();
      }
      w.last.dirA = remove === null;
      count(w.clicked, "additional_directories");
      break;
    }
    case "flush":
      await vi.advanceTimersByTimeAsync(300);
      break;
    case "commit": {
      const head = w.patches.shift();
      if (head !== undefined) {
        commitOnServer(w, head.body);
        w.answers.push({ held: head, resolved: resolve(w) });
      }
      break;
    }
    case "deliver": {
      const next = w.answers.shift();
      if (next !== undefined) {
        deliver(w, next.held, next.resolved);
      }
      break;
    }
    case "other":
      commitOnServer(w, { debug_logs: !w.server.debug_logs });
      break;
    case "drift":
      w.refusedA = !w.refusedA;
      break;
    case "event": {
      const revision = w.events.shift();
      if (revision !== undefined) {
        announce(revision);
      }
      break;
    }
    case "serveRead": {
      const head = w.gets.shift();
      if (head !== undefined) {
        deliver(w, head, resolve(w));
      }
      break;
    }
  }
  await settled();
}

async function boot(start: Start): Promise<World> {
  __testResetTracking();
  const w: World = {
    server: settingsPayload({
      settings_epoch: 1,
      settings_revision: 1,
      notifications_enabled: start.on,
      notify_agent_finished: start.kind,
      notify_pr_status: false,
      notify_run_outcome: start.sibling,
      last_model: "A",
      additional_directories: ["/data"],
    }),
    refusedA: false,
    resolved: 0,
    patches: [],
    answers: [],
    gets: [],
    events: [],
    received: null,
    notify: { on: start.on, kind: start.kind, pr: false, sibling: start.sibling },
    last: {},
    clicked: new Map(),
    carried: new Map(),
  };
  world = w;
  announce(1);
  await settled();
  await apply(w, { t: "serveRead" });
  return w;
}

function granted(s: EffectiveSettings): string[] {
  return s.additional_directories.filter(
    (d) => !Object.hasOwn(s.additional_directories_refused, d),
  );
}

/** Let every write, event and read play out, then require the user's last click everywhere. */
async function quiesce(w: World): Promise<void> {
  for (let guard = 0; guard < 100; guard++) {
    await apply(w, { t: "flush" });
    const next: Action | null =
      w.patches.length > 0
        ? { t: "commit" }
        : w.answers.length > 0
          ? { t: "deliver" }
          : w.events.length > 0
            ? { t: "event" }
            : w.gets.length > 0
              ? { t: "serveRead" }
              : null;
    if (next === null) {
      break;
    }
    await apply(w, next);
  }
  expect(w.patches, "every write answered").toEqual([]);
  expect(w.gets, "every read answered").toEqual([]);
  for (const [key, n] of w.carried) {
    expect(n, `writes carrying ${key}, at most one per click of it`).toBeLessThanOrEqual(
      w.clicked.get(key) ?? 0,
    );
  }
  const want = w.notify;
  expect(w.server.notify_agent_finished, "the server holds the clicked kind").toBe(want.kind);
  expect(box().checked, "the kind row shows it").toBe(want.kind);
  expect(isKindEnabled("agent_finished"), "notifications follow it").toBe(want.kind);
  expect(w.server.notify_pr_status, "the server's pull request kind").toBe(want.pr);
  expect(w.server.notify_run_outcome, "the server's workflow run kind").toBe(want.sibling);
  expect(w.server.notifications_enabled, "the server's notification switch").toBe(want.on);
  expect(masterBox().checked, "the notification switch shows it").toBe(want.on);
  expect(areNotificationsEnabled(), "the page notifies by it").toBe(want.on);
  const wantModel = w.last.model ?? "A";
  expect(w.server.last_model, "the server holds the last pick").toBe(wantModel);
  expect(getLastModel(), "a new chat opens with it").toBe(wantModel);
  const wantDirs = w.last.dirA === true ? ["/data", "/a"] : ["/data"];
  expect(w.server.additional_directories, "the server holds the last directory click").toEqual(
    wantDirs,
  );
  const newest = w.received?.payload;
  expect(newest, "a payload reached the page").toBeDefined();
  if (newest !== undefined) {
    expect(additionalDirectories(), "links follow the newest verdicts").toEqual(granted(newest));
  }
}

/** A failed run leaves requests held; failing them frees the settings queue for the next run. */
async function drain(w: World): Promise<void> {
  for (let i = 0; i < 20 && w.patches.length + w.answers.length + w.gets.length > 0; i++) {
    const open = [...w.patches.splice(0), ...w.answers.splice(0).map((a) => a.held)];
    for (const h of [...open, ...w.gets.splice(0)]) {
      h.answer(null);
    }
    await settled();
    await vi.advanceTimersByTimeAsync(300);
    await settled();
  }
}

async function run(start: Start, actions: readonly Action[]): Promise<void> {
  const w = await boot(start);
  try {
    for (const a of actions) {
      await apply(w, a);
    }
    await quiesce(w);
  } finally {
    await drain(w);
  }
}

const start: fc.Arbitrary<Start> = fc.record({
  on: fc.boolean(),
  kind: fc.boolean(),
  sibling: fc.boolean(),
});

const action: fc.Arbitrary<Action> = fc.oneof(
  { weight: 4, arbitrary: fc.constant({ t: "kind" as const }) },
  { weight: 2, arbitrary: fc.constant({ t: "master" as const }) },
  {
    weight: 3,
    arbitrary: fc.constantFrom("A", "B").map((model) => ({ t: "pick" as const, model })),
  },
  { weight: 2, arbitrary: fc.constant({ t: "dir" as const }) },
  { weight: 3, arbitrary: fc.constant({ t: "flush" as const }) },
  { weight: 3, arbitrary: fc.constant({ t: "commit" as const }) },
  { weight: 3, arbitrary: fc.constant({ t: "deliver" as const }) },
  { weight: 2, arbitrary: fc.constant({ t: "other" as const }) },
  { weight: 1, arbitrary: fc.constant({ t: "drift" as const }) },
  { weight: 3, arbitrary: fc.constant({ t: "event" as const }) },
  { weight: 3, arbitrary: fc.constant({ t: "serveRead" as const }) },
);

const kind: Action = { t: "kind" };
const master: Action = { t: "master" };
const flush: Action = { t: "flush" };
const commit: Action = { t: "commit" };
const siblingOn: Start = { on: true, kind: true, sibling: true };
const lastKindOn: Start = { on: true, kind: true, sibling: false };

beforeAll(async () => {
  document.body.innerHTML = new DOMParser().parseFromString(indexHtml, "text/html").body.innerHTML;
  // After the markup: the handler's module graph reads elements as it loads.
  await import("./handlers/system.js");
  initNotificationToggles();
  initAgentDirsUI();
});

afterAll(() => {
  document.body.replaceChildren();
  resetWorkspace();
});

describe("settings controls against a model server", () => {
  // Per case: the config restores stubbed globals and real timers after each one.
  function fake(): void {
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
    vi.stubGlobal("fetch", vi.fn(fakeFetch));
    // Blocked, so turning the switch on neither prompts nor registers push.
    vi.stubGlobal("Notification", { permission: "denied" });
  }

  it("ends on the user's last click on every control, for any interleaving", async () => {
    fake();
    try {
      await fc.assert(fc.asyncProperty(start, fc.array(action, { maxLength: 10 }), run), {
        numRuns: 150,
        seed: 20261019,
      });
    } finally {
      vi.useRealTimers();
    }
  });

  it.each<[string, Start, Action[]]>([
    ["saves the second of two kind clicks made inside one debounce", siblingOn, [kind, kind]],
    [
      "sends a kind click made while the opposite click's write is in flight",
      siblingOn,
      [kind, flush, kind],
    ],
    [
      "sends a kind click back to the server's value while the click before it is in flight",
      { on: true, kind: false, sibling: true },
      [kind, flush, kind, commit],
    ],
    [
      "saves a model picked back after another device's settings were read",
      siblingOn,
      [
        { t: "pick", model: "B" },
        { t: "other" },
        { t: "event" },
        { t: "serveRead" },
        { t: "pick", model: "A" },
      ],
    ],
    ["leaves the switch on when the last kind on is clicked off", lastKindOn, [kind]],
    [
      "leaves the switch on when the last kind on is clicked off and on inside one debounce",
      lastKindOn,
      [kind, kind],
    ],
    [
      "leaves the switch on when the last kind on is clicked on again while its off write is in flight",
      lastKindOn,
      [kind, flush, kind],
    ],
    [
      "leaves the switch on when the last kind on is clicked off and on around a model pick",
      lastKindOn,
      [kind, { t: "pick", model: "B" }, kind],
    ],
    [
      "leaves the switch on when another device's write is read between two kind clicks",
      lastKindOn,
      [kind, flush, { t: "other" }, { t: "event" }, { t: "serveRead" }, kind],
    ],
    [
      "keeps notifications off when a kind is clicked off after the switch was turned off",
      siblingOn,
      [master, kind],
    ],
    [
      "keeps notifications off when a kind is clicked off while the switch's off write is in flight",
      siblingOn,
      [master, flush, kind],
    ],
    [
      "keeps notifications off when the last kind is clicked off and on after the switch was turned off",
      lastKindOn,
      [master, kind, kind],
    ],
    [
      "keeps notifications off when a kind is clicked on after the switch was turned off",
      { on: true, kind: false, sibling: true },
      [master, kind],
    ],
    [
      "turns the switch on alone, leaving every kind off as stored",
      { on: false, kind: false, sibling: false },
      [master],
    ],
    [
      "turns the switch on alone, leaving the kinds as stored",
      { on: false, kind: true, sibling: false },
      [master, flush, commit],
    ],
    [
      "keeps the verdict a read gave over a late write's answer at the same version",
      siblingOn,
      [{ t: "dir" }, commit, { t: "drift" }, { t: "event" }, { t: "serveRead" }, { t: "deliver" }],
    ],
  ])("%s", async (_name, initial, actions) => {
    fake();
    try {
      await run(initial, actions);
    } finally {
      vi.useRealTimers();
    }
  });
});
