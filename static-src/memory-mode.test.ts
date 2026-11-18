// The Memory row over the shipped markup with only the network faked: it writes kiro-cli's device choice through
// PUT /api/memory/mode, never the settings PATCH, and says why it cannot change (a lock, no Kiro sign-in, a read
// still waiting on the engine, a refused write).
import { describe, it, expect, beforeAll, beforeEach, afterEach, afterAll, vi } from "vitest";
import indexHtml from "../static/index.html?raw";
import { settingsPayload } from "./__test-helpers__/settings.js";
import type { EffectiveSettings } from "./persist.js";
import { __testResetTracking } from "./persist.js";
import { dispatch } from "./bus.js";
import type { ServerEvent } from "./types.js";
import { byId } from "./dom.js";
import { adoptSignInHealth } from "./signin-health.js";
import type { DeviceMemory, SignInState } from "./wire/types.gen.js";

const showLoginModal = vi.hoisted(() => vi.fn());
vi.mock("./modals.js", async (importOriginal) => ({
  ...(await importOriginal<typeof import("./modals.js")>()),
  showLoginModal,
}));

interface Held {
  answer: (status: number, body: unknown) => void;
  body: unknown;
}

let gets: Held[] = [];
let puts: Held[] = [];
let patches: unknown[] = [];

function held(queue: Held[], body: unknown): Promise<Response> {
  return new Promise((resolve) => {
    queue.push({
      body,
      answer: (status, b) => {
        resolve(
          new Response(JSON.stringify(b), {
            status,
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
  const body = typeof init?.body === "string" ? (JSON.parse(init.body) as unknown) : undefined;
  if (url.endsWith("/api/settings")) {
    if (method === "PATCH") {
      patches.push(body);
      return Promise.resolve(new Response("{}", { status: 500 }));
    }
    return held(gets, body);
  }
  if (url.endsWith("/api/memory/mode")) {
    return held(puts, body);
  }
  return Promise.resolve(new Response("{}", { status: 404 }));
}

async function settled(): Promise<void> {
  await new Promise((resolve) => setTimeout(resolve, 0));
  await new Promise((resolve) => setTimeout(resolve, 0));
}

/** Past the shared skeleton delay (legacy-ui/skeleton.ts, 150 ms). */
async function pastSkeletonDelay(): Promise<void> {
  await new Promise((resolve) => setTimeout(resolve, 200));
}

let revision = 0;

/** The settings a read answers: memory as given, or absent while the engine has reported no view. */
function settingsWith(memory: DeviceMemory | null): EffectiveSettings {
  const s = settingsPayload({ settings_epoch: 1, settings_revision: revision });
  if (memory === null) {
    delete s.memory;
  } else {
    s.memory = memory;
  }
  return s;
}

/** Deliver a settings event and answer the read it sends. */
async function announced(memory: DeviceMemory | null): Promise<void> {
  revision++;
  dispatch({
    type: "settings_updated",
    payload: { settings_epoch: 1, settings_revision: revision },
  } as ServerEvent);
  await settled();
  gets.at(-1)?.answer(200, settingsWith(memory));
  await settled();
}

let healthSeq = 0;

function signIn(state: SignInState): void {
  adoptSignInHealth({ state, version: { boot_ms: 1, seq: ++healthSeq } });
}

const select = (): HTMLSelectElement => byId<HTMLSelectElement>("memory-mode");
const line = (): HTMLElement => byId("memory-mode-state");
const reason = (): string => byId("memory-mode-reason").textContent;
const signInButton = (): HTMLButtonElement => byId<HTMLButtonElement>("memory-mode-sign-in");
const lineShown = (): boolean => !line().classList.contains("hidden");

beforeAll(async () => {
  document.body.innerHTML = new DOMParser().parseFromString(indexHtml, "text/html").body.innerHTML;
  await import("./handlers/system.js");
  const { initGeneralPanelControls } = await import("./settings.js");
  initGeneralPanelControls();
});

beforeEach(() => {
  gets = [];
  puts = [];
  patches = [];
  __testResetTracking();
  revision = 0;
  signIn("ok");
  vi.stubGlobal("fetch", vi.fn(fakeFetch));
});

afterEach(async () => {
  for (const h of [...gets.splice(0), ...puts.splice(0)]) {
    h.answer(500, { error: "test over" });
  }
  await settled();
  // Leave the row read, so the next case starts from a known server answer.
  await announced({ mode: "off" });
});

afterAll(() => {
  document.body.replaceChildren();
});

describe("the Memory row", () => {
  it("writes a pick through PUT /api/memory/mode and paints the answered choice", async () => {
    await announced({ mode: "off" });

    select().value = "learn";
    select().dispatchEvent(new Event("change"));
    await settled();

    expect(puts.map((p) => p.body)).toEqual([{ mode: "learn" }]);
    expect(patches, "no settings PATCH carries the device choice").toEqual([]);
    puts[0]?.answer(200, { lock: null, mode: "learn" });
    await settled();
    // The answer's own read still carries the old row: the answer stays shown until a later read.
    gets.at(-1)?.answer(200, settingsWith({ mode: "off" }));
    await settled();
    expect(select().value).toBe("learn");
    expect(select().disabled).toBe(false);
    expect(lineShown()).toBe(false);
  });

  it("disables the dropdown under a lock and names the layer", async () => {
    await announced({ mode: "read_only", lock: { layer: "organization", name: "Your organization" } });

    expect(select().value).toBe("read_only");
    expect(select().disabled).toBe(true);
    expect(reason()).toBe("Locked by Your organization.");
    expect(signInButton().classList.contains("hidden")).toBe(true);
  });

  it("asks for a Kiro sign-in beside the reason while signed out", async () => {
    await announced({ mode: "off" });
    signIn("none");
    await settled();

    expect(select().disabled).toBe(true);
    expect(reason()).toBe("Sign in to Kiro to change Memory.");
    expect(signInButton().classList.contains("hidden")).toBe(false);
    signInButton().click();
    expect(showLoginModal).toHaveBeenCalledTimes(1);

    signIn("ok");
    await settled();
    expect(select().disabled).toBe(false);
    expect(lineShown()).toBe(false);
  });

  it("keeps the painted choice while kiro-cli has not reported, and says Reading only past the skeleton delay", async () => {
    await announced({ mode: "read_only" });
    await announced(null);

    expect(select().value, "the choice the page last painted").toBe("read_only");
    expect(select().disabled, "a fast read never flashes a disabled row").toBe(false);
    expect(lineShown()).toBe(false);

    await pastSkeletonDelay();
    expect(select().disabled).toBe(true);
    expect(reason()).toBe("Reading…");

    await announced({ mode: "read_write" });
    expect(select().value).toBe("read_write");
    expect(select().disabled).toBe(false);
    expect(lineShown()).toBe(false);
  });

  it("says Reading past the delay when it has never painted a choice", async () => {
    await announced(null);
    expect(lineShown()).toBe(false);

    await pastSkeletonDelay();
    expect(select().disabled).toBe(true);
    expect(reason()).toBe("Reading…");
  });

  it("shows a refused write's message under the row and keeps the server's choice", async () => {
    await announced({ mode: "off" });

    select().value = "read_only";
    select().dispatchEvent(new Event("change"));
    await settled();
    puts[0]?.answer(409, {
      error: "kiro-cli cannot change memory now. Sign in to Kiro, then try again.",
      code: "unavailable",
    });
    await vi.waitFor(() => {
      expect(reason()).toBe(
        "Couldn't change Memory: kiro-cli cannot change memory now. Sign in to Kiro, then try again.",
      );
    });
    expect(select().value).toBe("off");
    expect(lineShown()).toBe(true);
  });
});
