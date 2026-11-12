// Every run tab shows its OWN run, through the real tab strip, tab factory, run door and run view,
// against the fake tab server: open runs from one chat and from two, switch between their tabs and
// back, and each tab draws its run's name, status and steps.

import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import type * as ApiClient from "./api-client.js";
import type * as DeepLink from "./deep-link.js";
import type * as Dom from "./dom.js";
import type * as Router from "./router.js";
import type * as Store from "./store.js";
import type * as Toast from "./toast.js";
import type * as Transport from "./transport.js";
import type * as RunChatSteps from "./run-chat-steps.js";

const m = vi.hoisted(() => ({
  chats: new Set<string>(),
  replies: new Map<string, unknown>(),
}));

vi.mock("./router.js", async (importOriginal) => ({
  ...(await importOriginal<typeof Router>()),
  pushRoute: vi.fn(),
  replaceRoute: vi.fn(),
}));
vi.mock("./deep-link.js", async (importOriginal) => ({
  ...(await importOriginal<typeof DeepLink>()),
  admitLocation: vi.fn(() => "opens"),
}));
vi.mock("./chat.js", { spy: true });
vi.mock("./agent-finished-cue.js", { spy: true });
vi.mock("./dom.js", async (importOriginal) => ({
  ...(await importOriginal<typeof Dom>()),
  $: new Proxy(
    {},
    {
      get: (_t, prop: string) =>
        (prop === "tabList" ? document.getElementById("tab-list") : null) ??
        document.createElement("div"),
    },
  ),
  byId: () => document.createElement("span"),
}));
vi.mock("./store.js", async (importOriginal) => ({
  ...(await importOriginal<typeof Store>()),
  ...(await import("./__test-helpers__/store-mock.js")).storeMock,
  get: vi.fn((id: string) => (m.chats.has(id) ? { id } : undefined)),
}));
vi.mock("./toast.js", async (importOriginal) => ({
  ...(await importOriginal<typeof Toast>()),
  ...(await import("./__test-helpers__/toast-mock.js")).toastMock(),
}));
vi.mock("./transport.js", async (importOriginal) => ({
  ...(await importOriginal<typeof Transport>()),
  ...(await import("./__test-helpers__/tabs-server.js")).tabTransportMock(),
}));
vi.mock("./api-client.js", async (importOriginal) => ({
  ...(await importOriginal<typeof ApiClient>()),
  apiGetTyped: (await import("./__test-helpers__/tabs-server.js")).tabListRead(),
  apiGetTypedOrError: vi.fn(() => Promise.resolve({ ok: false, status: 0, data: null, error: "" })),
  apiGetOrError: vi.fn((path: string) => {
    const data = m.replies.get(path.split("?")[0] ?? path);
    return Promise.resolve(
      data === undefined
        ? { ok: false, status: 404, data: null, error: "not found" }
        : { ok: true, status: 200, data: structuredClone(data), error: "" },
    );
  }),
}));
vi.mock("./run-chat-steps.js", async (importOriginal) => ({
  ...(await importOriginal<typeof RunChatSteps>()),
  createRunStepStream: vi.fn(() => ({ apply: vi.fn(), dispose: vi.fn() })),
}));

import { activateTab, closeTab, tabIdFor, _resetForTest } from "./tabs.js";
import { rerenderDocks } from "./decision-dock.js";
import { applyRoute } from "./route-apply.js";
import { registerTabOpeners, _resetTabOpenersForTest } from "./tab-materialize.js";
import { ingestTabsChanged, listTabs, _resetTabsSyncForTest } from "./tabs-sync.js";
import { resetActionFramework } from "./actions/__test-helpers__/action-test-setup.js";
import { bindTabsSync, tabServer } from "./__test-helpers__/tabs-server.js";
import { openRunView, refreshRun, showRun } from "./run-view.js";
import { forgetRun, noteRunChat } from "./run-store.js";

import { installRunDotSubscriber } from "./run-dots.js";
import { openDockPanel } from "./__test-helpers__/exec-dock.js";

bindTabsSync({ ingest: ingestTabsChanged, list: listTabs });
let dotsInstalled = false;

const CHAT_A = "c-a";
const CHAT_B = "c-b";
const SEATS = "wf_seats";
const AGENT = "wf_agent";
const OTHER = "wf_other";

function step(nodeId: string, status: string): Record<string, unknown> {
  return { nodeId, type: "step", status, agentName: nodeId, sessionId: `sess_${nodeId}` };
}

function run(
  id: string,
  name: string,
  status: string,
  children: unknown[],
): Record<string, unknown> {
  return {
    workflowId: id,
    display_name: name,
    state: {
      workflowId: id,
      runLabel: name,
      status,
      parentSessionId: `sess_${id}`,
      root: { nodeId: id, type: "sequence", status, children },
    },
  };
}

function seedRuns(): void {
  m.replies.set(
    `/api/runs/${SEATS}`,
    run(SEATS, "review-seats-r2", "running", [
      {
        nodeId: "seats",
        type: "parallel",
        status: "running",
        children: [
          step("advisor", "completed"),
          step("code-a", "completed"),
          step("adversary-a", "running"),
          step("adversary-b", "running"),
        ],
      },
    ]),
  );
  m.replies.set(
    `/api/runs/${AGENT}`,
    run(AGENT, "doc-drift-knell", "completed", [
      { ...step("run", "completed"), agentName: "doc-drift" },
    ]),
  );
  m.replies.set(
    `/api/runs/${OTHER}`,
    run(OTHER, "subflux-build", "running", [
      step("implement", "completed"),
      step("verify", "running"),
    ]),
  );
}

beforeEach(async () => {
  tabServer.reset();
  _resetTabsSyncForTest();
  _resetTabOpenersForTest();
  registerTabOpeners({
    chat: {
      prefetch: vi.fn(),
      select: vi.fn(),
      loadFailed: vi.fn(),
      show: vi.fn(),
      refresh: vi.fn(() => Promise.resolve()),
      close: vi.fn(),
      dot: () => "",
    },
    editor: { show: vi.fn(), refresh: vi.fn(), close: vi.fn() },
    // The composition root's run half (app.ts).
    run: {
      show: (workflowID) => {
        showRun(workflowID);
      },
      refresh: refreshRun,
    },
    subagent: { show: vi.fn(), refresh: vi.fn() },
    spec: { show: vi.fn(), refresh: vi.fn() },
  });
  resetActionFramework();
  _resetForTest();
  document.body.innerHTML =
    '<div id="tab-list"></div><div id="run-view"><div class="page-content"><div id="run-body"></div></div></div><div id="run-exec-dock"></div><div id="run-dock"></div><ul id="run-steer-stack"></ul><form id="run-composer"><textarea id="run-composer-input"></textarea><button id="run-send-btn"></button></form>';
  m.chats = new Set([CHAT_A, CHAT_B]);
  seedRuns();
  await tabServer.seed({ kind: "chat", ref: CHAT_A }, { kind: "chat", ref: CHAT_B });
  if (!dotsInstalled) {
    dotsInstalled = true;
    installRunDotSubscriber();
  }
  tabServer.setRunLauncher(SEATS, CHAT_A);
  tabServer.setRunLauncher(AGENT, CHAT_A);
  tabServer.setRunLauncher(OTHER, CHAT_B);
  noteRunChat(SEATS, CHAT_A);
  noteRunChat(AGENT, CHAT_A);
  noteRunChat(OTHER, CHAT_B);
});

afterEach(() => {
  _resetTabsSyncForTest();
  document.body.innerHTML = "";
});

function shown(): { head: string; title: string; rows: string[]; composer: string } {
  const dock = document.getElementById("run-exec-dock");
  if (dock?.querySelector(".ev-box-toggle") != null) {
    openDockPanel(dock);
  }
  return {
    composer: document.querySelector<HTMLTextAreaElement>("#run-composer-input")?.placeholder ?? "",
    head: document.querySelector("#run-exec-dock .ev-dock-head")?.textContent ?? "",
    title: document.querySelector("#run-body .ev-d-title")?.textContent ?? "",
    rows: [...document.querySelectorAll<HTMLElement>("#run-exec-dock .ev-tree .ev-label")].map(
      (r) => r.textContent,
    ),
  };
}

/** Each run's name, its status as the dock words it, and its nodes in the order the server's
 *  reply lists them (`seedRuns`). */
const SHOWS = {
  [AGENT]: { name: "doc-drift-knell", status: "succeeded", steps: ["run"] },
  [SEATS]: {
    name: "review-seats-r2",
    status: "running",
    steps: ["seats", "advisor", "code-a", "adversary-a", "adversary-b"],
  },
  [OTHER]: { name: "subflux-build", status: "running", steps: ["implement", "verify"] },
};

async function expectShows(workflowID: keyof typeof SHOWS): Promise<void> {
  const want = SHOWS[workflowID];
  await vi.waitFor(
    () => {
      const s = shown();
      expect(s.head, JSON.stringify(s)).toContain(want.name);
      expect(s.head, JSON.stringify(s)).toContain(want.status);
      expect(s.rows, JSON.stringify(s)).toEqual(want.steps);
    },
    { timeout: 1500 },
  );
}

type Door = (workflowID: string) => Promise<void>;

/** The run bar, a run card, a History row: `openRunView` with the launching chat. */
const viaDoor: Door = (id) => openRunView(id, id);
/** A `/run/<id>` link in a reply, a notification, a reload. */
const viaRoute: Door = (id) => applyRoute({ kind: "run", id });
/** A click on the run's tab in the strip. */
const viaStrip: Door = (id) => {
  activateTab(tabIdFor("run", id));
  return Promise.resolve();
};

describe("each run tab shows its own run", () => {
  it.each([
    ["the door", viaDoor],
    ["a /run link", viaRoute],
  ])("from one chat, through %s", async (_label, open) => {
    await open(AGENT);
    await expectShows(AGENT);
    await open(SEATS);
    await expectShows(SEATS);
  });

  it("after the shown run's tab closed and its state was released", async () => {
    await viaDoor(AGENT);
    await expectShows(AGENT);
    await closeTab(tabIdFor("run", AGENT));
    // The transcript's run card releasing its claim (messages-blocks.ts `releaseRunCard`).
    forgetRun(AGENT);
    // Any decision-dock change in any chat.
    rerenderDocks();
    await viaDoor(SEATS);
    await expectShows(SEATS);
    await viaDoor(AGENT);
    await expectShows(AGENT);
  });

  it("after a run whose read the server refused", async () => {
    // No reply for it: the read answers 404, so its state never arrives.
    await viaRoute("wf_gone");
    await vi.waitFor(() => {
      expect(document.querySelector("#run-body .list-empty")?.textContent).toBe(
        "Loading run\u2026",
      );
    });
    await viaDoor(SEATS);
    await expectShows(SEATS);
  });

  it("from two chats, and back through the strip", async () => {
    await viaDoor(AGENT);
    await expectShows(AGENT);
    await viaRoute(OTHER);
    await expectShows(OTHER);
    await viaDoor(SEATS);
    await expectShows(SEATS);
    await viaStrip(AGENT);
    await expectShows(AGENT);
    await viaStrip(OTHER);
    await expectShows(OTHER);
    await viaStrip(SEATS);
    await expectShows(SEATS);
  });
});
