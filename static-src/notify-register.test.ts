// Enabling push makes this page present the new subscription's tag at once, so one browser is one
// profile to the server's notice ledger without a reload.

import { describe, it, expect, vi } from "vitest";
import type * as NotifyActions from "./actions/notify.js";
import type * as Persist from "./persist.js";
import type * as SSEAdapter from "./sse-adapter.js";

const h = vi.hoisted(() => ({
  sub: { endpoint: "https://push.example/endpoint-1" },
  adopted: [] as unknown[],
}));

vi.mock("./persist.js", async (importOriginal) => ({
  ...(await importOriginal<typeof Persist>()),
  patchSettings: async (): Promise<{ outcome: string }> => ({ outcome: "committed" }),
}));

vi.mock("./actions/notify.js", async (importOriginal) => ({
  ...(await importOriginal<typeof NotifyActions>()),
  registerPush: {
    dispatch: async (): Promise<unknown> => ({
      pushManager: { getSubscription: async (): Promise<unknown> => h.sub },
    }),
    cancel: vi.fn(),
  },
  unsubscribePush: { dispatch: async (): Promise<null> => null },
}));

vi.mock("./sse-adapter.js", async (importOriginal) => ({
  ...(await importOriginal<typeof SSEAdapter>()),
  adoptPushSubscription: async (sub: unknown): Promise<void> => {
    h.adopted.push(sub);
  },
}));

const notify = await import("./notify.js");

describe("enabling push", () => {
  it("hands the new subscription to the stream", async () => {
    const fake = function fakeNotification(): void {
      /* never constructed here */
    } as unknown as { permission: string };
    fake.permission = "granted";
    vi.stubGlobal("Notification", fake);
    notify.restoreNotifications({
      notifications_enabled: true,
      notify_agent_finished: true,
      notify_run_outcome: true,
      notify_pr_status: true,
    } as unknown as Parameters<typeof notify.restoreNotifications>[0]);
    await vi.waitFor(() => {
      expect(h.adopted).toEqual([h.sub]);
    });
    vi.unstubAllGlobals();
  });
});
