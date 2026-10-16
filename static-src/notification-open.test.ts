// Where a notification click lands, through the real seam, router, run door and tab projection, against
// the fake tab server's copy of the server's run-parent rule.

import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { join } from "@cplieger/keyenc";
import type * as ApiClient from "./api-client.js";
import type * as DeepLink from "./deep-link.js";
import type * as Dom from "./dom.js";
import type * as Router from "./router.js";
import type * as RunStore from "./run-store.js";
import type * as Store from "./store.js";
import type * as Toast from "./toast.js";
import type * as Transport from "./transport.js";

const m = vi.hoisted(() => ({ chats: new Set<string>() }));

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
// A real module graph reads named elements at import time; any element stands in but the strip.
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
  // A chat this page holds a row for, so the chat route switches to it rather than asking the server.
  get: vi.fn((id: string) => (m.chats.has(id) ? { id } : undefined)),
}));
vi.mock("./run-store.js", async (importOriginal) => ({
  ...(await importOriginal<typeof RunStore>()),
  // This page saw none of the run's frames: the ended-while-away case the server answers.
  runChatID: vi.fn(() => ""),
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
}));

import { getActiveTabRoute, openTabSubjects, tabIdFor, _resetForTest } from "./tabs.js";
import { applyRoute } from "./route-apply.js";
import { buildPath, parseRoute } from "./route-path.js";
import { parsePushTarget, pushTargetRoute } from "./push-subject.js";
import {
  registerNotificationOpener,
  _resetNotificationOpenerForTest,
} from "./notification-open.js";
import { routePushMessage } from "./handlers/push-message.js";
import { switchSession } from "./chat.js";
import { runChatID } from "./run-store.js";
import { error as toastError } from "./toast.js";
import { registerTabOpeners, _resetTabOpenersForTest } from "./tab-materialize.js";
import { ingestTabsChanged, listTabs, _resetTabsSyncForTest } from "./tabs-sync.js";
import { resetActionFramework } from "./actions/__test-helpers__/action-test-setup.js";
import { bindTabsSync, tabServer } from "./__test-helpers__/tabs-server.js";

bindTabsSync({ ingest: ingestTabsChanged, list: listTabs });

beforeEach(async () => {
  tabServer.reset();
  _resetTabsSyncForTest();
  _resetTabOpenersForTest();
  registerTabOpeners({
    chat: { show: vi.fn(), refresh: vi.fn(), close: vi.fn(), dot: () => "" },
    editor: { show: vi.fn(), refresh: vi.fn(), close: vi.fn() },
    run: { show: vi.fn(), refresh: vi.fn() },
    subagent: { show: vi.fn(), refresh: vi.fn() },
    spec: { show: vi.fn(), refresh: vi.fn() },
  });
  resetActionFramework();
  _resetForTest();
  document.body.innerHTML = '<div id="tab-list"></div>';
  m.chats = new Set(["c-launcher", "c-other"]);
  vi.mocked(switchSession).mockReset().mockResolvedValue(undefined);
  vi.mocked(runChatID).mockReset().mockReturnValue("");
  _resetNotificationOpenerForTest();
  // The composition root's registration (app.ts).
  registerNotificationOpener((route) => {
    void applyRoute(route);
  });
  // The launching chat and a bystander, so a wrong nest has somewhere to go.
  await tabServer.seed({ kind: "chat", ref: "c-launcher" }, { kind: "chat", ref: "c-other" });
  tabServer.setRunLauncher("wf_child", "c-launcher");
});

afterEach(() => {
  _resetTabsSyncForTest();
  _resetNotificationOpenerForTest();
  document.body.innerHTML = "";
});

/** The worker's post to an open page on a click (sw.ts `notificationclick`). */
function click(chatId: string, subject: string): void {
  routePushMessage({ type: "push", reason: "clicked", chatId, subject, title: "x", body: "" });
}

/** The URL the worker hands `openWindow` when no page is open. */
function coldURL(chatId: string, subject: string): string {
  return buildPath(pushTargetRoute(parsePushTarget({ chatId, subject })));
}

/** The open is a round trip plus the run door's dynamic import, so the check polls. */
async function settled(check: () => boolean, what: string): Promise<void> {
  for (let i = 0; i < 100 && !check(); i++) {
    await new Promise((r) => {
      setTimeout(r, 1);
    });
  }
  expect(check(), what).toBe(true);
}

function topLevelCount(): number {
  return openTabSubjects().filter((s) => s.parent === "").length;
}

function runParent(workflowID: string): string | undefined {
  return openTabSubjects().find((s) => s.kind === "run" && s.ref === workflowID)?.parent;
}

describe("a run notification's click", () => {
  it("opens a parented run as its chat's sub-tab and selects it, with a page open", async () => {
    click("", "run:wf_child");
    await settled(() => tabIdFor("run", "wf_child") !== "", "the run's tab opened");

    expect(runParent("wf_child")).toBe(tabIdFor("chat", "c-launcher"));
    expect(getActiveTabRoute()).toEqual({ kind: "run", id: "wf_child" });
    expect(topLevelCount()).toBe(2);
  });

  it("opens a parented run as its chat's sub-tab from the URL the worker opens with no page", async () => {
    const url = coldURL("", "run:wf_child");
    expect(url).toBe("/run/wf_child");
    // A fresh page applies the URL it booted on.
    await applyRoute(parseRoute(url, ""));

    expect(runParent("wf_child")).toBe(tabIdFor("chat", "c-launcher"));
    expect(getActiveTabRoute()).toEqual({ kind: "run", id: "wf_child" });
    expect(topLevelCount()).toBe(2);
  });

  it("opens a step question's run in the same place", async () => {
    click("", `runask:${join("wf_child", "0", "sess_step")}`);
    await settled(() => tabIdFor("run", "wf_child") !== "", "the run's tab opened");

    expect(runParent("wf_child")).toBe(tabIdFor("chat", "c-launcher"));
    expect(getActiveTabRoute()).toEqual({ kind: "run", id: "wf_child" });
  });

  it("reopens the launching chat's closed tab and nests the run under it, with a page open", async () => {
    m.chats.add("c-closed");
    tabServer.setRunLauncher("wf_orphan", "c-closed");

    click("", "run:wf_orphan");
    await settled(() => tabIdFor("run", "wf_orphan") !== "", "the run's tab opened");

    expect(tabIdFor("chat", "c-closed")).not.toBe("");
    expect(runParent("wf_orphan")).toBe(tabIdFor("chat", "c-closed"));
    expect(getActiveTabRoute()).toEqual({ kind: "run", id: "wf_orphan" });
    expect(topLevelCount()).toBe(3);
  });

  it("reopens the launching chat's closed tab from the URL the worker opens with no page", async () => {
    m.chats.add("c-closed");
    tabServer.setRunLauncher("wf_orphan", "c-closed");

    await applyRoute(parseRoute(coldURL("", "run:wf_orphan"), ""));
    await settled(() => tabIdFor("chat", "c-closed") !== "", "the chat's tab frame applied");

    expect(runParent("wf_orphan")).toBe(tabIdFor("chat", "c-closed"));
    expect(getActiveTabRoute()).toEqual({ kind: "run", id: "wf_orphan" });
    expect(topLevelCount()).toBe(3);
  });

  it("still nests the run when another device closed the parent this page names", async () => {
    // This page saw the run's frames, so it names its launcher's tab as the parent.
    vi.mocked(runChatID).mockReturnValue("c-launcher");
    const named = tabIdFor("chat", "c-launcher");
    tabServer.closeElsewhere(named);

    click("", "run:wf_child");
    await settled(() => tabIdFor("run", "wf_child") !== "", "the run's tab opened");
    tabServer.flushFrames();
    await settled(() => tabIdFor("chat", "c-launcher") !== named, "the closed tab's frame applied");

    expect(tabServer.sentOfType("open_tab").at(-1)?.payload["parent"]).toBe(named);
    const reopened = tabIdFor("chat", "c-launcher");
    expect(reopened, "the launching chat's tab came back").not.toBe("");
    expect(runParent("wf_child")).toBe(reopened);
    expect(getActiveTabRoute()).toEqual({ kind: "run", id: "wf_child" });
    expect(topLevelCount()).toBe(2);
  });

  it("retries a launcher the server could not read and still nests the run", async () => {
    tabServer.failNext("open_tab", 503, "could not tell which chat launched that workflow run");

    click("", "run:wf_child");
    await vi.waitFor(() => {
      expect(tabIdFor("run", "wf_child")).not.toBe("");
    });

    expect(runParent("wf_child")).toBe(tabIdFor("chat", "c-launcher"));
    expect(getActiveTabRoute()).toEqual({ kind: "run", id: "wf_child" });
  });

  it("opens no tab while the server cannot read the run's launcher", async () => {
    for (let i = 0; i < 3; i++) {
      tabServer.failNext("open_tab", 503, "could not tell which chat launched that workflow run");
    }
    const before = openTabSubjects();

    click("", "run:wf_child");
    await vi.waitFor(
      () => {
        expect(toastError).toHaveBeenCalled();
      },
      { timeout: 5000 },
    );

    expect(openTabSubjects()).toEqual(before);
    expect(getActiveTabRoute()).not.toEqual({ kind: "run", id: "wf_child" });
  });

  it("opens a parentless run as its own tab", async () => {
    click("", "run:wf_nightly");
    await settled(() => tabIdFor("run", "wf_nightly") !== "", "the run's tab opened");

    expect(runParent("wf_nightly")).toBe("");
    expect(getActiveTabRoute()).toEqual({ kind: "run", id: "wf_nightly" });
    expect(topLevelCount()).toBe(3);
  });
});

describe("a chat notification's click", () => {
  it("opens the chat and no run", async () => {
    click("c-launcher", "");
    await settled(() => vi.mocked(switchSession).mock.calls.length > 0, "the chat was switched to");

    expect(vi.mocked(switchSession).mock.calls.map((c) => c[0])).toEqual(["c-launcher"]);
    expect(openTabSubjects().some((s) => s.kind === "run")).toBe(false);
  });
});
