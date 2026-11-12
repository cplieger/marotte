// showNotice shows the server's own words through this device's gates, whatever this page has on
// screen (the server already picked the page), decides one notice id once, and reports the outcome.

import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { chatTarget, runTarget } from "./push-subject.js";
import { noticeFor } from "./__test-helpers__/notice.js";
import type * as Persist from "./persist.js";
import type * as NotifyActions from "./actions/notify.js";
import type * as SSEAdapter from "./sse-adapter.js";

vi.mock("./persist.js", async (importOriginal) => ({
  ...(await importOriginal<typeof Persist>()),
  patchSettings: async (): Promise<{ outcome: string }> => ({ outcome: "committed" }),
}));

vi.mock("./actions/notify.js", async (importOriginal) => ({
  ...(await importOriginal<typeof NotifyActions>()),
  registerPush: { dispatch: async (): Promise<null> => null, cancel: vi.fn() },
  unsubscribePush: { dispatch: async (): Promise<null> => null },
}));

vi.mock("./sse-adapter.js", async (importOriginal) => ({
  ...(await importOriginal<typeof SSEAdapter>()),
  presentedTag: (): string => "tag-A",
}));

const notify = await import("./notify.js");

let shown: { title: string; body: string | undefined }[] = [];

function shadowNotification(permission = "granted"): void {
  const fake = function fakeNotification(title: string, o?: NotificationOptions): void {
    shown.push({ title, body: o?.body });
  } as unknown as {
    (title: string, o?: NotificationOptions): void;
    permission: string;
    prototype: { addEventListener: (t: string, f: () => void) => void };
  };
  fake.permission = permission;
  fake.prototype = { addEventListener: vi.fn() };
  vi.stubGlobal("Notification", fake);
}

const fetchSpy = vi.fn(
  async (_url: string, _init?: RequestInit): Promise<Response> =>
    new Response(null, { status: 204 }),
);

function reports(): { body: unknown; tag: string | null }[] {
  return fetchSpy.mock.calls
    .filter(([url]) => url === "/api/events/notice")
    .map(([, init]) => ({
      body: JSON.parse(String(init?.body)) as unknown,
      tag: new Headers(init?.headers).get("SSE-Client"),
    }));
}

beforeEach(() => {
  shown = [];
  fetchSpy.mockClear();
  vi.stubGlobal("fetch", fetchSpy);
  shadowNotification();
  notify.setNotificationsEnabled(true);
  for (const kind of ["agent_finished", "run_outcome", "pr_status"]) {
    notify.setKindEnabled(kind, true);
  }
});

afterEach(() => {
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

describe("showNotice", () => {
  it("shows the server's title and body verbatim and reports it shown under the page's tag", () => {
    const n = noticeFor(chatTarget("c1"), "Permission required · git push", "permission", "Fix it");
    expect(notify.showNotice(n)).toBe(true);
    expect(shown).toEqual([{ title: "Fix it", body: "Permission required · git push" }]);
    expect(reports()).toEqual([{ body: { id: n.id, outcome: "shown" }, tag: "tag-A" }]);
  });

  it("shows while the page is visible: the server judged the screen", () => {
    vi.spyOn(document, "visibilityState", "get").mockReturnValue("visible");
    expect(notify.showNotice(noticeFor(chatTarget("c1"), "Response complete"))).toBe(true);
    expect(shown).toHaveLength(1);
  });

  it("honours the per-kind switch and reports the notice declined", () => {
    notify.setKindEnabled("run_outcome", false);
    const n = noticeFor(runTarget("wf1"), "Workflow completed", "run_outcome");
    expect(notify.showNotice(n)).toBe(false);
    expect(shown).toEqual([]);
    expect(reports()).toEqual([{ body: { id: n.id, outcome: "declined" }, tag: "tag-A" }]);
  });

  it("declines with the master switch off or without the OS permission", () => {
    notify.setNotificationsEnabled(false);
    const off = noticeFor(chatTarget("c1"), "ask");
    expect(notify.showNotice(off)).toBe(false);
    notify.setNotificationsEnabled(true);
    shadowNotification("default");
    const denied = noticeFor(chatTarget("c1"), "ask");
    expect(notify.showNotice(denied)).toBe(false);
    expect(reports().map((r) => r.body)).toEqual([
      { id: off.id, outcome: "declined" },
      { id: denied.id, outcome: "declined" },
    ]);
  });

  it("decides one notice id once", () => {
    const n = noticeFor(chatTarget("c1"), "ask");
    expect(notify.showNotice(n)).toBe(true);
    expect(notify.showNotice(n)).toBe(false);
    expect(shown).toHaveLength(1);
    expect(reports()).toHaveLength(1);
  });

  it("forgets the oldest of more than 256 decided ids", () => {
    const first = noticeFor(chatTarget("c1"), "first");
    notify.showNotice(first);
    for (let i = 0; i < 256; i++) {
      notify.showNotice(noticeFor(chatTarget("c1"), "filler"));
    }
    expect(notify.showNotice(first)).toBe(true);
  });

  it("shows a notice with no id and reports nothing", () => {
    expect(notify.showNotice({ ...noticeFor(chatTarget("c1"), "ask"), id: "" })).toBe(true);
    expect(notify.showNotice({ ...noticeFor(chatTarget("c1"), "ask"), id: "" })).toBe(true);
    expect(reports()).toEqual([]);
  });
});
