// The real settings controls against a model server, for any interleaving of clicks, debounce
// flushes, this page's writes committing one at a time, another device's writes, settings events and
// reads: at rest the server, each control and the state the page acts on all hold the user's last
// click on that control. The controls are a notification kind row (a real click on the shipped
// markup) and the last-model memory (session-context.setLastModel); another device writes only keys
// neither control owns, so the last click is the oracle. Only fetch is faked.
import { describe, it, expect, beforeAll, afterAll, vi } from "vitest";
import fc from "fast-check";
import indexHtml from "../static/index.html?raw";
import { settingsPayload } from "./__test-helpers__/settings.js";
import type { EffectiveSettings } from "./persist.js";
import { __testResetTracking } from "./persist.js";
import { dispatch } from "./bus.js";
import type { ServerEvent } from "./types.js";
import { byId } from "./dom.js";
import { isKindEnabled } from "./notify.js";
import { initNotificationToggles } from "./settings-notifications.js";
import { getLastModel, setLastModel } from "./session-context.js";
import { _resetForTest as resetWorkspace } from "./workspace.js";

type Action =
  | { t: "kind" }
  | { t: "pick"; model: "A" | "B" }
  | { t: "flush" }
  | { t: "commit" }
  | { t: "other" }
  | { t: "event" }
  | { t: "serveRead" };

interface Held {
  body: Partial<EffectiveSettings>;
  answer: (s: EffectiveSettings | null) => void;
}

interface World {
  server: EffectiveSettings;
  patches: Held[];
  gets: Held[];
  events: number[];
  last: { kind?: boolean; model?: string };
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
      box().click();
      w.last.kind = box().checked;
      break;
    case "pick":
      setLastModel(a.model);
      w.last.model = a.model;
      break;
    case "flush":
      await vi.advanceTimersByTimeAsync(300);
      break;
    case "commit": {
      const head = w.patches.shift();
      if (head !== undefined) {
        commitOnServer(w, head.body);
        head.answer(w.server);
      }
      break;
    }
    case "other":
      commitOnServer(w, { debug_logs: !w.server.debug_logs });
      break;
    case "event": {
      const revision = w.events.shift();
      if (revision !== undefined) {
        announce(revision);
      }
      break;
    }
    case "serveRead":
      w.gets.shift()?.answer(w.server);
      break;
  }
  await settled();
}

async function boot(kind: boolean): Promise<World> {
  __testResetTracking();
  const w: World = {
    server: settingsPayload({
      settings_epoch: 1,
      settings_revision: 1,
      notify_agent_finished: kind,
      last_model: "A",
    }),
    patches: [],
    gets: [],
    events: [],
    last: {},
  };
  world = w;
  announce(1);
  await settled();
  w.gets.shift()?.answer(w.server);
  await settled();
  return w;
}

/** Let every write, event and read play out, then require the user's last click everywhere. */
async function quiesce(w: World, kind: boolean): Promise<void> {
  for (let guard = 0; guard < 100; guard++) {
    await apply(w, { t: "flush" });
    const next: Action | null =
      w.patches.length > 0
        ? { t: "commit" }
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
  const wantKind = w.last.kind ?? kind;
  expect(w.server.notify_agent_finished, "the server holds the last kind click").toBe(wantKind);
  expect(box().checked, "the kind row shows it").toBe(wantKind);
  expect(isKindEnabled("agent_finished"), "notifications follow it").toBe(wantKind);
  const wantModel = w.last.model ?? "A";
  expect(w.server.last_model, "the server holds the last pick").toBe(wantModel);
  expect(getLastModel(), "a new chat opens with it").toBe(wantModel);
}

/** A failed run leaves requests held; failing them frees the settings queue for the next run. */
async function drain(w: World): Promise<void> {
  for (let i = 0; i < 20 && (w.patches.length > 0 || w.gets.length > 0); i++) {
    for (const h of [...w.patches.splice(0), ...w.gets.splice(0)]) {
      h.answer(null);
    }
    await settled();
    await vi.advanceTimersByTimeAsync(300);
    await settled();
  }
}

async function run(kind: boolean, actions: readonly Action[]): Promise<void> {
  const w = await boot(kind);
  try {
    for (const a of actions) {
      await apply(w, a);
    }
    await quiesce(w, kind);
  } finally {
    await drain(w);
  }
}

const action: fc.Arbitrary<Action> = fc.oneof(
  { weight: 4, arbitrary: fc.constant({ t: "kind" as const }) },
  {
    weight: 4,
    arbitrary: fc.constantFrom("A", "B").map((model) => ({ t: "pick" as const, model })),
  },
  { weight: 3, arbitrary: fc.constant({ t: "flush" as const }) },
  { weight: 3, arbitrary: fc.constant({ t: "commit" as const }) },
  { weight: 2, arbitrary: fc.constant({ t: "other" as const }) },
  { weight: 3, arbitrary: fc.constant({ t: "event" as const }) },
  { weight: 3, arbitrary: fc.constant({ t: "serveRead" as const }) },
);

const kind: Action = { t: "kind" };
const flush: Action = { t: "flush" };
const commit: Action = { t: "commit" };

beforeAll(async () => {
  document.body.innerHTML = new DOMParser().parseFromString(indexHtml, "text/html").body.innerHTML;
  // After the markup: the handler's module graph reads elements as it loads.
  await import("./handlers/system.js");
  initNotificationToggles();
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
  }

  it("ends on the user's last click on every control, for any interleaving", async () => {
    fake();
    try {
      await fc.assert(fc.asyncProperty(fc.boolean(), fc.array(action, { maxLength: 10 }), run), {
        numRuns: 150,
        seed: 20261019,
      });
    } finally {
      vi.useRealTimers();
    }
  });

  it.each<[string, boolean, Action[]]>([
    ["saves the second of two kind clicks made inside one debounce", true, [kind, kind]],
    [
      "sends a kind click made while the opposite click's write is in flight",
      true,
      [kind, flush, kind],
    ],
    [
      "sends a kind click back to the server's value while the click before it is in flight",
      false,
      [kind, flush, kind, commit],
    ],
    [
      "saves a model picked back after another device's settings were read",
      true,
      [
        { t: "pick", model: "B" },
        { t: "other" },
        { t: "event" },
        { t: "serveRead" },
        { t: "pick", model: "A" },
      ],
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
