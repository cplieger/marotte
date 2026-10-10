import { describe, it, expect, beforeEach, vi } from "vitest";
import { chatTarget } from "./push-subject.js";
import { noticeFor } from "./__test-helpers__/notice.js";

vi.mock("./persist.js", () => ({
  patchSettings: async (): Promise<Record<string, unknown>> => ({ ok: true }),
}));

vi.mock("./actions/notify.js", () => ({
  registerPush: { dispatch: async (): Promise<null> => null, cancel: vi.fn() },
  unsubscribePush: { dispatch: async (): Promise<null> => null },
}));

const notify = await import("./notify.js");

let opts: NotificationOptions[] = [];

/** A stand-in for window.Notification: a constructor recording its options, plus the
 *  two statics notifyOffScreen reads. */
function shadowNotification(): void {
  const fake = function fakeNotification(_title: string, o?: NotificationOptions): void {
    opts.push(o ?? {});
  } as unknown as {
    (title: string, o?: NotificationOptions): void;
    permission: string;
    prototype: { addEventListener: (t: string, f: () => void) => void };
  };
  fake.permission = "granted";
  // The real constructor returns an EventTarget and notifyOffScreen subscribes to its
  // click, so the instance has to accept a listener.
  fake.prototype = { addEventListener: vi.fn() };
  vi.stubGlobal("Notification", fake);
}

beforeEach(() => {
  opts = [];
  shadowNotification();
  // The page has to be in the background, or notifyOffScreen declines before it
  // constructs anything.
  vi.spyOn(document, "visibilityState", "get").mockReturnValue("hidden");
  notify.setNotificationsEnabled(true);
});

describe("notifyOffScreen tags a notification by its subject", () => {
  it("gives two chats two different tags", () => {
    expect(notify.notifyOffScreen(noticeFor(chatTarget("c1"), "Agent finished"))).toBe(true);
    expect(notify.notifyOffScreen(noticeFor(chatTarget("c2"), "Agent finished"))).toBe(true);
    expect(opts).toHaveLength(2);
    expect(opts[0]?.tag).toBe("marotte:c1");
    expect(opts[1]?.tag).toBe("marotte:c2");
    expect(opts[0]?.tag).not.toBe(opts[1]?.tag);
  });

  it("gives the workspace-global subject the constant tag", () => {
    expect(notify.notifyOffScreen(noticeFor(chatTarget(""), "Permission needed"))).toBe(true);
    expect(opts[0]?.tag).toBe("marotte");
  });
});
