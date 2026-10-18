// The page adopts only settings at or above the version it holds, whichever of the settings_updated
// handler (handlers/system.ts), the shared read (persist.loadSettings) or the additional
// directories panel's own write delivers them, a failed write rolls back nothing newer settings
// or a later write own, and what the page acts on is the server's while its controls show a click
// not yet committed, over the shipped markup with only the network faked.
import { describe, it, expect, beforeAll, beforeEach, afterAll, vi } from "vitest";
import indexHtml from "../static/index.html?raw";
import { settingsPayload } from "./__test-helpers__/settings.js";
import type { EffectiveSettings } from "./persist.js";
import { dispatch } from "./bus.js";
import type { ServerEvent } from "./types.js";
import { applyAgentDirs, initAgentDirsUI } from "./agent-dirs-ui.js";
import { byId } from "./dom.js";
import { __testResetTracking, loadSettings, patchSettings } from "./persist.js";
import { additionalDirectories, _resetForTest as resetWorkspace } from "./workspace.js";
import { isKindEnabled } from "./notify.js";
import { isRetentionEnabled } from "./retention.js";
import { initNotificationToggles } from "./settings-notifications.js";

interface Held {
  answer: (s: EffectiveSettings) => void;
  fail: () => void;
  body: unknown;
}

let gets: Held[] = [];
let patches: Held[] = [];

function held(queue: Held[], body: unknown): Promise<Response> {
  return new Promise((resolve) => {
    queue.push({
      body,
      answer: (s) => {
        resolve(
          new Response(JSON.stringify(s), {
            status: 200,
            headers: { "content-type": "application/json" },
          }),
        );
      },
      fail: () => {
        resolve(new Response(JSON.stringify({ error: "unavailable" }), { status: 500 }));
      },
    });
  });
}

function fakeFetch(input: RequestInfo | URL, init?: RequestInit): Promise<Response> {
  const url = typeof input === "string" ? input : input instanceof URL ? input.href : input.url;
  const method = init?.method ?? (input instanceof Request ? input.method : "GET");
  if (url.endsWith("/api/settings")) {
    const body = typeof init?.body === "string" ? (JSON.parse(init.body) as unknown) : undefined;
    return held(method === "PATCH" ? patches : gets, body);
  }
  return Promise.resolve(new Response("{}", { status: 404 }));
}

function settingsEvent(revision: number, epoch = 1): void {
  dispatch({
    type: "settings_updated",
    payload: { settings_epoch: epoch, settings_revision: revision },
  } as ServerEvent);
}

function chipLabels(): string[] {
  return [...byId("agent-dirs-chips").querySelectorAll(".chip-label")].map((c) => c.textContent);
}

async function settled(): Promise<void> {
  await new Promise((resolve) => setTimeout(resolve, 0));
  await new Promise((resolve) => setTimeout(resolve, 0));
}

function dirsAt(revision: number, dirs: string[], epoch = 1): EffectiveSettings {
  return settingsPayload({
    settings_epoch: epoch,
    settings_revision: revision,
    additional_directories: dirs,
  });
}

async function debounced(): Promise<void> {
  await new Promise((resolve) => setTimeout(resolve, 350));
  await settled();
}

function debugLogs(): HTMLInputElement {
  return byId<HTMLInputElement>("flag-debug-logs");
}

function saveDebugLogs(on: boolean): void {
  debugLogs().checked = on;
  void patchSettings({ debug_logs: on }, { el: debugLogs(), key: "debug_logs", on: true });
}

function debugAt(revision: number, on: boolean): EffectiveSettings {
  return settingsPayload({ settings_epoch: 1, settings_revision: revision, debug_logs: on });
}

function add(dir: string): void {
  byId<HTMLInputElement>("agent-dirs-input").value = dir;
  byId<HTMLButtonElement>("agent-dirs-add").click();
}

beforeAll(async () => {
  document.body.innerHTML = new DOMParser().parseFromString(indexHtml, "text/html").body.innerHTML;
  // After the markup: the handler's module graph reads elements as it loads.
  await import("./handlers/system.js");
  initAgentDirsUI();
  initNotificationToggles();
});

beforeEach(() => {
  gets = [];
  patches = [];
  __testResetTracking();
  // Per case: the config restores stubbed globals after each one.
  vi.stubGlobal("fetch", vi.fn(fakeFetch));
  applyAgentDirs(dirsAt(0, ["/data"]));
});

afterAll(() => {
  document.body.replaceChildren();
  resetWorkspace();
});

describe("settings sync ordering", () => {
  it("ignores a write's answer that arrives after a newer settings event", async () => {
    byId<HTMLInputElement>("agent-dirs-input").value = "/a";
    byId<HTMLButtonElement>("agent-dirs-add").click();
    await settled();
    expect(patches).toHaveLength(1);

    // Another device wrote revision 2 after this write committed revision 1.
    settingsEvent(2);
    await settled();
    expect(gets).toHaveLength(1);
    gets[0]?.answer(dirsAt(2, ["/data", "/b"]));
    await settled();
    expect(chipLabels()).toEqual(["/data", "/b"]);

    patches[0]?.answer(dirsAt(1, ["/data", "/a"]));
    await settled();
    expect(chipLabels()).toEqual(["/data", "/b"]);
    expect(additionalDirectories()).toEqual(["/data", "/b"]);
  });

  it("ignores a write's answer that arrives after a newer settings event and before its read answers", async () => {
    byId<HTMLInputElement>("agent-dirs-input").value = "/a";
    byId<HTMLButtonElement>("agent-dirs-add").click();
    await settled();
    expect(patches).toHaveLength(1);

    settingsEvent(2);
    await settled();
    expect(gets).toHaveLength(1);
    patches[0]?.answer(dirsAt(1, ["/data", "/a"]));
    await settled();
    expect(chipLabels()).toEqual(["/data"]);
    expect(additionalDirectories()).toEqual(["/data"]);

    gets[0]?.answer(dirsAt(2, ["/data", "/b"]));
    await settled();
    expect(chipLabels()).toEqual(["/data", "/b"]);
  });

  it("sends one more read for an event that arrives while a read is in flight", async () => {
    settingsEvent(1);
    await settled();
    expect(gets).toHaveLength(1);

    settingsEvent(2);
    settingsEvent(2);
    await settled();
    expect(gets, "no read starts while one is in flight").toHaveLength(1);

    gets[0]?.answer(dirsAt(1, ["/data", "/old"]));
    await settled();
    expect(gets, "the events during the first read share one more").toHaveLength(2);
    gets[1]?.answer(dirsAt(2, ["/data", "/new"]));
    await settled();
    expect(chipLabels()).toEqual(["/data", "/new"]);
    expect(additionalDirectories()).toEqual(["/data", "/new"]);
  });

  it("adopts this write's own answer when another device's event arrived first", async () => {
    add("/a");
    await settled();
    expect(patches).toHaveLength(1);

    // Another device committed revision 1 first; this write committed revision 2 over it.
    settingsEvent(1);
    await settled();
    expect(gets).toHaveLength(1);
    patches[0]?.answer(dirsAt(2, ["/data", "/a"]));
    await settled();
    expect(chipLabels()).toEqual(["/data", "/a"]);

    gets[0]?.answer(dirsAt(1, ["/data", "/b"]));
    await settled();
    expect(chipLabels(), "the read served before this write is older").toEqual(["/data", "/a"]);

    add("/c");
    await settled();
    expect(patches).toHaveLength(2);
    expect(patches[1]?.body).toEqual({ additional_directories: ["/data", "/a", "/c"] });
    patches[1]?.answer(dirsAt(3, ["/data", "/a", "/c"]));
    await settled();
  });

  it("never repaints older settings after an older event", async () => {
    add("/a");
    await settled();
    settingsEvent(2);
    await settled();
    gets[0]?.answer(dirsAt(2, ["/data", "/b"]));
    await settled();
    expect(chipLabels()).toEqual(["/data", "/b"]);

    settingsEvent(1);
    await settled();
    patches[0]?.answer(dirsAt(1, ["/data", "/a"]));
    await settled();
    expect(chipLabels()).toEqual(["/data", "/b"]);
    expect(additionalDirectories()).toEqual(["/data", "/b"]);
    gets[1]?.answer(dirsAt(2, ["/data", "/b"]));
    await settled();
    expect(chipLabels()).toEqual(["/data", "/b"]);
  });

  it("keeps the panel locked on an older answer until the newest settings are read", async () => {
    add("/a");
    await settled();
    settingsEvent(2);
    await settled();
    patches[0]?.answer(dirsAt(1, ["/data", "/a"]));
    await settled();
    expect(byId<HTMLInputElement>("agent-dirs-input").disabled).toBe(true);

    gets[0]?.answer(dirsAt(2, ["/data", "/a", "/b"]));
    await settled();
    expect(byId<HTMLInputElement>("agent-dirs-input").disabled).toBe(false);
    add("/c");
    await settled();
    expect(patches[1]?.body).toEqual({ additional_directories: ["/data", "/a", "/b", "/c"] });
    patches[1]?.answer(dirsAt(3, ["/data", "/a", "/b", "/c"]));
    await settled();
  });

  it("adopts a reset config.json's later epoch, and refuses the old epoch's settings after it", async () => {
    settingsEvent(5);
    await settled();
    gets[0]?.answer(dirsAt(5, ["/data", "/five"]));
    await settled();
    expect(chipLabels()).toEqual(["/data", "/five"]);

    // config.json was replaced, so its next write started a later epoch at 1.
    settingsEvent(1, 2);
    await settled();
    gets[1]?.answer(dirsAt(1, ["/data", "/reset"], 2));
    await settled();
    expect(chipLabels()).toEqual(["/data", "/reset"]);

    settingsEvent(6);
    await settled();
    gets[2]?.answer(dirsAt(6, ["/data", "/old"]));
    await settled();
    expect(chipLabels()).toEqual(["/data", "/reset"]);
  });

  it("keeps the panel locked when the newest settings cannot be read after an older answer", async () => {
    add("/a");
    await settled();
    settingsEvent(2);
    await settled();
    gets[0]?.fail();
    await settled();
    patches[0]?.answer(dirsAt(1, ["/data", "/a"]));
    await settled();
    expect(gets, "the panel asks for the newest settings once more").toHaveLength(2);
    gets[1]?.fail();
    await settled();

    expect(byId<HTMLInputElement>("agent-dirs-input").disabled).toBe(true);
    expect(byId("agent-dirs-error").textContent).toContain("the newest list could not be loaded");
    add("/c");
    for (const remove of byId("agent-dirs-chips").querySelectorAll<HTMLButtonElement>(
      ".chip-remove",
    )) {
      remove.click();
    }
    await settled();
    expect(patches, "no whole-list write is built from the older list").toHaveLength(1);

    byId("agent-dirs-error").querySelector<HTMLButtonElement>("button")?.click();
    await settled();
    gets[2]?.answer(dirsAt(2, ["/data", "/b"]));
    await settled();
    expect(chipLabels()).toEqual(["/data", "/b"]);
    expect(byId<HTMLInputElement>("agent-dirs-input").disabled).toBe(false);
    expect(byId("agent-dirs-error").classList.contains("hidden")).toBe(true);
  });

  it("leaves a switch and the next write to newer settings read before its write failed", async () => {
    settingsEvent(1);
    await settled();
    gets[0]?.answer(
      settingsPayload({ settings_epoch: 1, settings_revision: 1, debug_logs: false }),
    );
    await settled();
    saveDebugLogs(true);
    await debounced();
    expect(patches).toHaveLength(1);

    // Another device turned the switch on, so the server holds what this page is still sending.
    settingsEvent(2);
    await settled();
    gets[1]?.answer(settingsPayload({ settings_epoch: 1, settings_revision: 2, debug_logs: true }));
    await settled();
    patches[0]?.fail();
    await settled();
    expect(debugLogs().checked, "the server's value, not the one before this write").toBe(true);

    saveDebugLogs(false);
    await debounced();
    expect(patches[1]?.body).toEqual({ debug_logs: false });
    patches[1]?.answer(settingsPayload({ settings_epoch: 1, settings_revision: 3 }));
    await settled();
  });

  it("lets a failed write roll back neither the switch nor the value a later write sends", async () => {
    settingsEvent(1);
    await settled();
    gets[0]?.answer(
      settingsPayload({ settings_epoch: 1, settings_revision: 1, debug_logs: false }),
    );
    await settled();
    saveDebugLogs(true);
    await debounced();
    saveDebugLogs(false);
    saveDebugLogs(true);
    await debounced();
    expect(patches, "the second write waits behind the first").toHaveLength(1);

    patches[0]?.fail();
    await settled();
    expect(debugLogs().checked, "the later write still owns the switch").toBe(true);
    expect(patches).toHaveLength(2);
    expect(patches[1]?.body).toEqual({ debug_logs: true });
    patches[1]?.answer(
      settingsPayload({ settings_epoch: 1, settings_revision: 2, debug_logs: true }),
    );
    await settled();
    expect(debugLogs().checked).toBe(true);

    saveDebugLogs(true);
    await debounced();
    expect(patches, "true is what the server holds, so nothing is sent").toHaveLength(2);
  });

  it("sends a click that puts back what newer settings changed while an older write was out", async () => {
    settingsEvent(1);
    await settled();
    gets[0]?.answer(debugAt(1, false));
    await settled();
    saveDebugLogs(true);
    await debounced();
    expect(patches).toHaveLength(1);

    // Another device turned the switch off after this write committed revision 2.
    settingsEvent(3);
    await settled();
    gets[1]?.answer(debugAt(3, false));
    await settled();
    expect(debugLogs().checked).toBe(false);

    saveDebugLogs(true);
    await debounced();
    patches[0]?.answer(debugAt(2, true));
    await settled();
    expect(patches, "the click reaches the server").toHaveLength(2);
    expect(patches[1]?.body).toEqual({ debug_logs: true });
    patches[1]?.answer(debugAt(4, true));
    await settled();
    expect(debugLogs().checked).toBe(true);
  });

  it("puts a switch back to newer settings, not to an overtaken write, when the write after it fails", async () => {
    settingsEvent(1);
    await settled();
    gets[0]?.answer(debugAt(1, false));
    await settled();
    saveDebugLogs(true);
    await debounced();
    settingsEvent(3);
    await settled();
    gets[1]?.answer(debugAt(3, false));
    await settled();
    saveDebugLogs(true);
    await debounced();

    patches[0]?.answer(debugAt(2, true));
    await settled();
    expect(patches).toHaveLength(2);
    patches[1]?.fail();
    await settled();
    expect(debugLogs().checked, "revision 3's value").toBe(false);
  });

  it("puts a switch back to newer settings, not to the value before both writes, when both fail", async () => {
    settingsEvent(1);
    await settled();
    gets[0]?.answer(debugAt(1, true));
    await settled();
    saveDebugLogs(false);
    await debounced();
    settingsEvent(3);
    await settled();
    gets[1]?.answer(debugAt(3, false));
    await settled();
    saveDebugLogs(true);
    await debounced();

    patches[0]?.fail();
    await settled();
    expect(patches).toHaveLength(2);
    patches[1]?.fail();
    await settled();
    expect(debugLogs().checked, "revision 3's value").toBe(false);
  });

  it("puts a switch clicked twice around a plain read back to the server's value when both writes fail", async () => {
    settingsEvent(1);
    await settled();
    gets[0]?.answer(debugAt(1, false));
    await settled();
    saveDebugLogs(true);
    await debounced();
    void loadSettings();
    await settled();
    gets[1]?.answer(debugAt(1, false));
    await settled();
    saveDebugLogs(false);
    await debounced();

    patches[0]?.fail();
    await settled();
    expect(patches).toHaveLength(2);
    patches[1]?.fail();
    await settled();
    expect(debugLogs().checked, "revision 1's value").toBe(false);
  });

  it("keeps a click still waiting to be sent across another device's settings event", async () => {
    settingsEvent(1);
    await settled();
    gets[0]?.answer(debugAt(1, false));
    await settled();
    saveDebugLogs(true);

    settingsEvent(2);
    await settled();
    gets[1]?.answer(debugAt(2, false));
    await settled();
    expect(debugLogs().checked, "the click, not revision 2's value").toBe(true);

    await debounced();
    expect(patches[0]?.body).toEqual({ debug_logs: true });
    patches[0]?.answer(debugAt(3, true));
    await settled();
  });

  it("leaves a switch on the settings it holds when an answer older than an announced version settles", async () => {
    settingsEvent(1);
    await settled();
    gets[0]?.answer(debugAt(1, false));
    await settled();
    saveDebugLogs(true);
    await debounced();
    expect(patches).toHaveLength(1);

    // This write committed revision 2, then another device committed revision 3.
    settingsEvent(3);
    await settled();
    expect(gets).toHaveLength(2);
    patches[0]?.answer(debugAt(2, true));
    await settled();
    expect(debugLogs().checked, "revision 2 is older than the version the page holds").toBe(false);

    gets[1]?.answer(debugAt(3, false));
    await settled();
    expect(debugLogs().checked).toBe(false);
  });

  it("shows a click waiting behind another write over another device's newer settings", async () => {
    settingsEvent(1);
    await settled();
    gets[0]?.answer(debugAt(1, false));
    await settled();
    saveDebugLogs(true);
    await debounced();
    saveDebugLogs(false);
    await debounced();
    expect(patches, "the second write waits behind the first").toHaveLength(1);

    settingsEvent(3);
    await settled();
    gets[1]?.answer(debugAt(3, true));
    await settled();
    expect(debugLogs().checked, "the waiting write commits after revision 3").toBe(false);

    patches[0]?.answer(debugAt(2, true));
    await settled();
    expect(debugLogs().checked).toBe(false);
    expect(patches[1]?.body).toEqual({ debug_logs: false });
    patches[1]?.answer(debugAt(4, false));
    await settled();
    expect(debugLogs().checked).toBe(false);
  });

  it("notifies by the server's kind setting while a kind click waits, and after it fails", async () => {
    const box = byId<HTMLInputElement>("notify-finished-toggle");
    settingsEvent(1);
    await settled();
    gets[0]?.answer(
      settingsPayload({ settings_epoch: 1, settings_revision: 1, notify_agent_finished: true }),
    );
    await settled();
    expect(box.checked).toBe(true);
    box.checked = false;
    void patchSettings(
      { notify_agent_finished: false },
      { el: box, key: "notify_agent_finished", on: true },
    );

    // Another device's write, inside the click's debounce window.
    settingsEvent(2);
    await settled();
    gets[1]?.answer(
      settingsPayload({
        settings_epoch: 1,
        settings_revision: 2,
        notify_agent_finished: true,
        debug_logs: true,
      }),
    );
    await settled();
    expect(box.checked, "the click, not yet sent").toBe(false);
    expect(isKindEnabled("agent_finished"), "the kind the server holds").toBe(true);

    await debounced();
    patches[0]?.fail();
    await settled();
    expect(box.checked).toBe(true);
    expect(isKindEnabled("agent_finished")).toBe(true);
  });

  it("keeps the retention the server holds while a retention write waits, and after it fails", async () => {
    settingsEvent(1);
    await settled();
    gets[0]?.answer(
      settingsPayload({ settings_epoch: 1, settings_revision: 1, chat_retention_days: 7 }),
    );
    await settled();
    expect(isRetentionEnabled()).toBe(true);
    void patchSettings({ chat_retention_days: 0 });

    settingsEvent(2);
    await settled();
    gets[1]?.answer(
      settingsPayload({
        settings_epoch: 1,
        settings_revision: 2,
        chat_retention_days: 7,
        debug_logs: true,
      }),
    );
    await settled();
    expect(byId<HTMLInputElement>("chat-retention-days").value, "the value not yet sent").toBe("0");
    expect(isRetentionEnabled(), "the retention the server holds").toBe(true);

    await debounced();
    patches[0]?.fail();
    await settled();
    expect(isRetentionEnabled()).toBe(true);
  });

  it("shows a switch click that waited behind a directories write over that write's answer", async () => {
    settingsEvent(1);
    await settled();
    gets[0]?.answer(dirsAt(1, ["/data"]));
    await settled();
    add("/a");
    await settled();
    saveDebugLogs(true);
    await debounced();
    expect(patches, "the switch's write waits behind the directories write").toHaveLength(1);

    patches[0]?.answer(dirsAt(2, ["/data", "/a"]));
    await settled();
    expect(patches).toHaveLength(2);
    expect(debugLogs().checked, "the switch's write commits after revision 2").toBe(true);
    patches[1]?.answer(debugAt(3, true));
    await settled();
    expect(debugLogs().checked).toBe(true);
  });
});
