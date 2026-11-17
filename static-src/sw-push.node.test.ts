// The worker's push handler over a fake worker scope: a push landing while a window is focused goes
// to that window alone, carrying the notice id the page reports against.

import { describe, it, expect, vi, beforeAll } from "vitest";

interface FakeClient {
  readonly url: string;
  readonly focused: boolean;
  readonly posted: unknown[];
  focus: () => Promise<FakeClient>;
  postMessage: (m: unknown) => void;
}

function client(url: string, focused: boolean): FakeClient {
  const c: FakeClient = {
    url,
    focused,
    posted: [],
    focus: async () => c,
    postMessage: (m) => {
      c.posted.push(m);
    },
  };
  return c;
}

const handlers = new Map<string, (event: unknown) => void>();
let windows: FakeClient[] = [];
const showNotification = vi.fn(
  async (_title: string, _options?: unknown): Promise<void> => undefined,
);

beforeAll(async () => {
  vi.stubGlobal("self", {
    addEventListener: (type: string, fn: (event: unknown) => void) => {
      handlers.set(type, fn);
    },
    clients: {
      matchAll: async () => windows,
      openWindow: async () => null,
      claim: async () => undefined,
    },
    registration: { showNotification, pushManager: {} },
  });
  await import("./sw.js");
});

async function push(data: Record<string, unknown>): Promise<void> {
  const waits: Promise<unknown>[] = [];
  handlers.get("push")?.({
    data: { json: () => data, text: () => JSON.stringify(data) },
    waitUntil: (p: Promise<unknown>) => {
      waits.push(p);
    },
  });
  await Promise.all(waits);
}

describe("a push while a window is focused", () => {
  it("posts the arrival, with its id, to the focused window only", async () => {
    const focused = client("https://m.example/chat/c1", true);
    const other = client("https://m.example/chat/c2", false);
    windows = [other, focused];
    await push({
      id: "n-1",
      kind: "agent_finished",
      chat_id: "c1",
      title: "Fix login",
      body: "Response complete",
    });
    expect(other.posted).toEqual([]);
    expect(focused.posted).toEqual([
      {
        type: "push",
        reason: "arrived",
        id: "n-1",
        chatId: "c1",
        subject: "",
        kind: "agent_finished",
        title: "Fix login",
        body: "Response complete",
      },
    ]);
    expect(showNotification).not.toHaveBeenCalled();
  });
});

describe("a push with no window focused", () => {
  it("shows the PNG app icon and the alpha-only badge, both files logo.test.ts pins", async () => {
    windows = [client("https://m.example/chat/c2", false)];
    showNotification.mockClear();
    await push({ id: "n-2", kind: "agent_finished", chat_id: "c1", title: "Fix login", body: "" });
    expect(showNotification).toHaveBeenCalledWith(
      "Fix login",
      expect.objectContaining({ icon: "/icon-192.png", badge: "/badge-96.png" }),
    );
  });
});
