// notifyOffScreen shows the server's own words and stays silent only while that exact chat or
// run is on screen (web-terminal-ui's `shouldNotify` rule), never merely because the page is.

import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { chatTarget, runTarget, type PushTarget } from "./push-subject.js";
import { noticeFor } from "./__test-helpers__/notice.js";
import type * as Persist from "./persist.js";
import type * as NotifyActions from "./actions/notify.js";

vi.mock("./persist.js", async (importOriginal) => ({
  ...(await importOriginal<typeof Persist>()),
  patchSettings: async (): Promise<Record<string, unknown>> => ({ ok: true }),
}));

vi.mock("./actions/notify.js", async (importOriginal) => ({
  ...(await importOriginal<typeof NotifyActions>()),
  registerPush: { dispatch: async (): Promise<null> => null, cancel: vi.fn() },
  unsubscribePush: { dispatch: async (): Promise<null> => null },
}));

const notify = await import("./notify.js");

let shown: { title: string; body: string | undefined }[] = [];

function shadowNotification(): void {
  const fake = function fakeNotification(title: string, o?: NotificationOptions): void {
    shown.push({ title, body: o?.body });
  } as unknown as {
    (title: string, o?: NotificationOptions): void;
    permission: string;
    prototype: { addEventListener: (t: string, f: () => void) => void };
  };
  fake.permission = "granted";
  fake.prototype = { addEventListener: vi.fn() };
  vi.stubGlobal("Notification", fake);
}

let onScreen: PushTarget | null = null;

beforeEach(() => {
  shown = [];
  onScreen = null;
  shadowNotification();
  notify.setNotificationsEnabled(true);
  for (const kind of ["agent_finished", "run_outcome", "pr_status"]) {
    notify.setKindEnabled(kind, true);
  }
  notify.setOnScreen((t) => JSON.stringify(t) === JSON.stringify(onScreen));
});

afterEach(() => {
  notify.setOnScreen(() => false);
});

describe("notifyOffScreen", () => {
  it("shows the server's title and body verbatim", () => {
    notify.notifyOffScreen(
      noticeFor(chatTarget("c1"), "Permission required · git push", "permission", "Fix login"),
    );
    expect(shown).toEqual([{ title: "Fix login", body: "Permission required · git push" }]);
  });

  it("notifies a background chat while another chat is on screen", () => {
    onScreen = chatTarget("c2");
    expect(notify.notifyOffScreen(noticeFor(chatTarget("c1"), "Response complete"))).toBe(true);
    expect(shown).toHaveLength(1);
  });

  it("says nothing about the chat on screen", () => {
    onScreen = chatTarget("c1");
    expect(notify.notifyOffScreen(noticeFor(chatTarget("c1"), "Response complete"))).toBe(false);
    expect(shown).toEqual([]);
  });

  it("says nothing about the run on screen, and still notifies its parent chat's other news", () => {
    onScreen = runTarget("wf1");
    expect(notify.notifyOffScreen(noticeFor(runTarget("wf1"), "Input required"))).toBe(false);
    expect(notify.notifyOffScreen(noticeFor(chatTarget("c1"), "Response complete"))).toBe(true);
    expect(shown).toHaveLength(1);
  });

  it("honours the per-kind switch", () => {
    notify.setKindEnabled("run_outcome", false);
    expect(
      notify.notifyOffScreen(noticeFor(runTarget("wf1"), "Workflow completed", "run_outcome")),
    ).toBe(false);
    expect(shown).toEqual([]);
  });
});
