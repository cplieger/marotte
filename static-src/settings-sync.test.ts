// Settings payloads reach the page in the order the server wrote them: the settings_updated
// handler (handlers/system.ts), the shared read (persist.loadSettings) and the additional
// directories panel's own write, over the shipped markup with only the network faked.
import { describe, it, expect, beforeAll, beforeEach, afterAll, vi } from "vitest";
import indexHtml from "../static/index.html?raw";
import { settingsPayload } from "./__test-helpers__/settings.js";
import type { EffectiveSettings } from "./persist.js";
import { dispatch } from "./bus.js";
import type { ServerEvent } from "./types.js";
import { applyAgentDirs, initAgentDirsUI } from "./agent-dirs-ui.js";
import { byId } from "./dom.js";
import { __testResetTracking } from "./persist.js";
import { additionalDirectories, _resetForTest as resetWorkspace } from "./workspace.js";

interface Held {
  answer: (s: EffectiveSettings) => void;
}

let gets: Held[] = [];
let patches: Held[] = [];

function held(queue: Held[]): Promise<Response> {
  return new Promise((resolve) => {
    queue.push({
      answer: (s) => {
        resolve(
          new Response(JSON.stringify(s), {
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
  const method = init?.method ?? (input instanceof Request ? input.method : "GET");
  if (url.endsWith("/api/settings")) {
    return held(method === "PATCH" ? patches : gets);
  }
  return Promise.resolve(new Response("{}", { status: 404 }));
}

function settingsEvent(revision: number): void {
  dispatch({ type: "settings_updated", payload: { settings_revision: revision } } as ServerEvent);
}

function chipLabels(): string[] {
  return [...byId("agent-dirs-chips").querySelectorAll(".chip-label")].map((c) => c.textContent);
}

async function settled(): Promise<void> {
  await new Promise((resolve) => setTimeout(resolve, 0));
  await new Promise((resolve) => setTimeout(resolve, 0));
}

function dirsAt(revision: number, dirs: string[]): EffectiveSettings {
  return settingsPayload({ settings_revision: revision, additional_directories: dirs });
}

beforeAll(async () => {
  document.body.innerHTML = new DOMParser().parseFromString(indexHtml, "text/html").body.innerHTML;
  // After the markup: the handler's module graph reads elements as it loads.
  await import("./handlers/system.js");
  initAgentDirsUI();
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

  it("adopts settings again after config.json's revision went back", async () => {
    settingsEvent(5);
    await settled();
    gets[0]?.answer(dirsAt(5, ["/data", "/five"]));
    await settled();
    expect(chipLabels()).toEqual(["/data", "/five"]);

    // config.json was replaced, so the next write counts from 1.
    settingsEvent(1);
    await settled();
    gets[1]?.answer(dirsAt(1, ["/data", "/reset"]));
    await settled();
    expect(chipLabels()).toEqual(["/data", "/reset"]);
  });
});
