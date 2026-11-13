// The `notification` frame shows only on a page its `show_in` names, once per notice id, whatever
// the page's own screen or the chat's open turn, and tells the server it was shown.

import { vi, describe, it, expect, beforeEach, afterEach } from "vitest";
import { dispatch } from "../bus.js";
import { makeSession } from "../__test-helpers__/model.js";
import { pageID } from "../page-view.js";
import { setSessions } from "../store.js";
import type { NotificationPayload } from "../wire/types.gen.js";
import type * as Persist from "../persist.js";
import type * as NotifyActions from "../actions/notify.js";
import type * as SSEAdapter from "../sse-adapter.js";

vi.mock("../persist.js", async (importOriginal) => ({
  ...(await importOriginal<typeof Persist>()),
  patchSettings: async (): Promise<{ outcome: string }> => ({ outcome: "committed" }),
}));

vi.mock("../actions/notify.js", async (importOriginal) => ({
  ...(await importOriginal<typeof NotifyActions>()),
  registerPush: { dispatch: async (): Promise<null> => null, cancel: vi.fn() },
  unsubscribePush: { dispatch: async (): Promise<null> => null },
}));

vi.mock("../sse-adapter.js", async (importOriginal) => ({
  ...(await importOriginal<typeof SSEAdapter>()),
  presentedTag: (): string => "tag-A",
}));

const notify = await import("../notify.js");
await import("./notification.js");
const { initPushMessages } = await import("./push-message.js");
initPushMessages(() => undefined);

let shown: string[] = [];

function shadowNotification(): void {
  const fake = function fakeNotification(title: string): void {
    shown.push(title);
  } as unknown as {
    (title: string, o?: NotificationOptions): void;
    permission: string;
    prototype: { addEventListener: (t: string, f: () => void) => void };
  };
  fake.permission = "granted";
  fake.prototype = { addEventListener: vi.fn() };
  vi.stubGlobal("Notification", fake);
}

const fetchSpy = vi.fn(
  async (_url: string, _init?: RequestInit): Promise<Response> =>
    new Response(null, { status: 204 }),
);

function reports(): { id: string; outcome: string }[] {
  return fetchSpy.mock.calls
    .filter(([url]) => url === "/api/events/notice")
    .map(([, init]) => JSON.parse(String(init?.body)) as { id: string; outcome: string });
}

function frame(n: NotificationPayload): void {
  dispatch({ type: "notification", chat_id: n.chat_id ?? "", payload: n });
}

function notice(id: string, chatID: string, over: Partial<NotificationPayload> = {}) {
  return {
    id,
    chat_id: chatID,
    kind: "permission",
    title: `title ${id}`,
    body: "Permission required",
    ...over,
  } satisfies NotificationPayload;
}

function pageIs(state: DocumentVisibilityState): void {
  vi.spyOn(document, "visibilityState", "get").mockReturnValue(state);
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
  setSessions([]);
});

describe("the notification frame", () => {
  it("raises nothing when show_in does not name this page, though the page is hidden", () => {
    pageIs("hidden");
    frame(notice("n-other", "c1", { show_in: ["some-other-page"] }));
    expect(shown).toEqual([]);
    expect(reports()).toEqual([]);
  });

  it("raises a notice naming this page although its chat is on screen, and reports it shown", async () => {
    pageIs("visible");
    frame(notice("n-here", "c1", { show_in: [pageID()] }));
    expect(shown).toEqual(["title n-here"]);
    await vi.waitFor(() => {
      expect(reports()).toEqual([{ id: "n-here", outcome: "shown" }]);
    });
  });

  it("shows one notice id once: a replayed frame and then its push arrival raise nothing more", () => {
    pageIs("hidden");
    const n = notice("n-once", "c1", { show_in: [pageID()] });
    frame(n);
    frame(n);
    navigator.serviceWorker.dispatchEvent(
      new MessageEvent("message", {
        data: {
          type: "push",
          reason: "arrived",
          id: "n-once",
          chatId: "c1",
          subject: "",
          kind: "permission",
          title: "title n-once",
          body: "Permission required",
        },
      }),
    );
    expect(shown).toEqual(["title n-once"]);
  });

  it("raises an agent_finished notice naming this page at once while the chat's turn is open", () => {
    pageIs("hidden");
    setSessions([makeSession({ id: "c-open", turn_open: true })]);
    frame(
      notice("n-finished", "c-open", {
        kind: "agent_finished",
        body: "Response complete",
        show_in: [pageID()],
      }),
    );
    expect(shown).toEqual(["title n-finished"]);
  });
});
