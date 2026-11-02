// The master switch going ON writes the switch alone and leaves every kind as it is. A write that
// newer settings superseded still does this click's own part when those settings agree with it,
// and a kind write that committed applies what it committed. settings-sync.test.ts runs the
// switch and the kinds together on the real persist.

import { describe, it, expect, beforeEach, vi } from "vitest";
import { settingsPayload } from "./__test-helpers__/settings.js";
import type { EffectiveSettings, PatchAnswer } from "./persist.js";
import { LS_NOTIFY_ASK_KEY } from "./ls-keys.js";

const mocks = vi.hoisted(() => ({
  patches: [] as Record<string, unknown>[],
  patchResult: { outcome: "failed" } as PatchAnswer,
  /** Whether the newest settings have notifications on; null when they cannot be read. */
  newestOn: null as boolean | null,
  unregisters: 0,
  /** While set, every settings write waits for it before answering. */
  hold: null as Promise<void> | null,
}));

vi.mock("./persist.js", () => ({
  patchSettings: async (patch: Record<string, unknown>): Promise<PatchAnswer> => {
    mocks.patches.push(patch);
    await mocks.hold;
    return mocks.patchResult;
  },
  newestSettings: async () =>
    mocks.newestOn === null ? null : settingsPayload({ notifications_enabled: mocks.newestOn }),
  settingsAsShown: () => null,
}));

vi.mock("./actions/notify.js", () => ({
  registerPush: {
    dispatch: async (): Promise<null> => null,
    cancel: (): void => {
      mocks.unregisters += 1;
    },
  },
  unsubscribePush: { dispatch: async (): Promise<null> => null },
}));

vi.mock("./actions/index.js", () => ({
  bindLoadingState: (): (() => void) => vi.fn(),
  registerCleanup: vi.fn(),
}));

const notify = await import("./notify.js");
const { initNotificationToggles } = await import("./settings-notifications.js");

const KIND_IDS = {
  agent_finished: "notify-finished-toggle",
  pr_status: "notify-pr-status-toggle",
  run_outcome: "notify-run-outcome-toggle",
} as const;

/** The three kind toggles are authored with the CHECKED state index.html gives them, so the
 *  pr_status box starts unchecked like the real first frame. */
function mountPanel(): void {
  const host = document.createElement("div");
  host.innerHTML = `
    <input type="checkbox" id="notify-toggle">
    <p id="notify-hint" class="hidden"></p>
    <div id="notify-sub-options" class="hidden">
      <input type="checkbox" id="notify-finished-toggle" checked>
      <input type="checkbox" id="notify-pr-status-toggle">
      <input type="checkbox" id="notify-run-outcome-toggle" checked>
    </div>`;
  document.body.replaceChildren(host);
}

function committed(settings: EffectiveSettings): PatchAnswer {
  return { outcome: "committed", settings };
}

function input(id: string): HTMLInputElement {
  const el = document.getElementById(id);
  expect(el, `${id} is not mounted`).not.toBeNull();
  return el as HTMLInputElement;
}

async function turnMasterOn(): Promise<void> {
  const master = input("notify-toggle");
  master.checked = true;
  master.dispatchEvent(new Event("change"));
  for (let i = 0; i < 12; i++) {
    await Promise.resolve();
  }
}

async function flipped(id: string, on: boolean): Promise<void> {
  const box = input(id);
  box.checked = on;
  box.dispatchEvent(new Event("change"));
  for (let i = 0; i < 12; i++) {
    await Promise.resolve();
  }
}

beforeEach(() => {
  mocks.patches = [];
  mocks.patchResult = committed(settingsPayload({ notifications_enabled: true }));
  mocks.newestOn = null;
  mocks.unregisters = 0;
  mocks.hold = null;
  localStorage.removeItem(LS_NOTIFY_ASK_KEY);
  // Granted, so requestPermission neither prompts nor returns a hint — the prompt is
  // notify-permission.test.ts's subject, not this file's.
  vi.stubGlobal("Notification", { permission: "granted" });
  notify.setNotificationsEnabled(false);
  for (const kind of Object.keys(KIND_IDS)) {
    notify.setKindEnabled(kind, false);
  }
  mountPanel();
  initNotificationToggles();
});

describe("the master switch going on", () => {
  it("writes the switch alone", async () => {
    await turnMasterOn();
    expect(mocks.patches).toEqual([{ notifications_enabled: true }]);
  });

  it("leaves every kind's checkbox as it was", async () => {
    notify.setKindEnabled("run_outcome", true);
    mountPanel();
    initNotificationToggles();
    await turnMasterOn();
    expect(input(KIND_IDS.agent_finished).checked).toBe(false);
    expect(input(KIND_IDS.pr_status).checked).toBe(false);
    expect(input(KIND_IDS.run_outcome).checked).toBe(true);
  });

  it("leaves the in-memory state agreeing with the committed settings", async () => {
    mocks.patchResult = committed(
      settingsPayload({
        notifications_enabled: true,
        notify_agent_finished: false,
        notify_pr_status: true,
        notify_run_outcome: true,
      }),
    );
    await turnMasterOn();
    expect(notify.areNotificationsEnabled()).toBe(true);
    expect(notify.isKindEnabled("agent_finished")).toBe(false);
    expect(notify.isKindEnabled("pr_status")).toBe(true);
    expect(notify.isKindEnabled("run_outcome")).toBe(true);
  });
});

describe("a refused write tears the feature down", () => {
  it("turns notifications off and leaves the kinds as they were", async () => {
    mocks.patchResult = { outcome: "failed" };
    notify.setKindEnabled("agent_finished", true);
    await turnMasterOn();
    expect(notify.areNotificationsEnabled()).toBe(false);
    expect(notify.isKindEnabled("agent_finished"), "the kind no click moved").toBe(true);
    expect(notify.isKindEnabled("run_outcome")).toBe(false);
    expect(mocks.unregisters).toBe(1);
  });
});

describe("a write newer settings superseded", () => {
  function promptable(): ReturnType<typeof vi.fn> {
    const requestPermission = vi.fn(() => Promise.resolve("default"));
    vi.stubGlobal("Notification", { permission: "default", requestPermission });
    return requestPermission;
  }

  beforeEach(() => {
    mocks.patchResult = { outcome: "superseded" };
  });

  it("still asks for browser permission when the newest settings have notifications on", async () => {
    const requestPermission = promptable();
    mocks.newestOn = true;
    await turnMasterOn();
    expect(requestPermission).toHaveBeenCalledOnce();
    expect(input("notify-hint").textContent).toContain("Browser permission is pending");
  });

  it("asks nothing when the newest settings turned notifications off", async () => {
    const requestPermission = promptable();
    mocks.newestOn = false;
    await turnMasterOn();
    expect(requestPermission).not.toHaveBeenCalled();
  });

  it("spends this device's ask when the master switch went off and the newest settings agree", async () => {
    notify.setNotificationsEnabled(true);
    mocks.newestOn = false;
    await flipped("notify-toggle", false);
    expect(localStorage.getItem(LS_NOTIFY_ASK_KEY)).toBe("1");
    expect(mocks.unregisters).toBe(1);
  });

  it("keeps the ask when the newest settings turned notifications back on", async () => {
    notify.setNotificationsEnabled(true);
    mocks.newestOn = true;
    await flipped("notify-toggle", false);
    expect(localStorage.getItem(LS_NOTIFY_ASK_KEY)).toBeNull();
    expect(mocks.unregisters).toBe(0);
  });
});

describe("a kind write that committed", () => {
  async function settle(): Promise<void> {
    for (let i = 0; i < 12; i++) {
      await Promise.resolve();
    }
  }

  it("notifies by the kind the write committed, not by the box painted since", async () => {
    let release = (): void => undefined;
    mocks.hold = new Promise((resolve) => {
      release = resolve;
    });
    mocks.patchResult = committed(settingsPayload({ notify_agent_finished: false }));
    notify.setKindEnabled("agent_finished", true);
    await flipped(KIND_IDS.agent_finished, false);
    // A later click's write, still on its way, painted the box back on.
    input(KIND_IDS.agent_finished).checked = true;
    release();
    await settle();
    expect(notify.isKindEnabled("agent_finished")).toBe(false);
  });
});
