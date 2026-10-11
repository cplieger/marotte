import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";

vi.mock("../toast.js", () =>
  import("../__test-helpers__/toast-mock.js").then((m) => m.toastMock()),
);

vi.mock("../api-client.js", () => ({
  API_TIMEOUT_MS: 30_000,
  withTimeout: (signal: AbortSignal | undefined) => signal ?? new AbortController().signal,
  apiGet: vi.fn(),
  apiPost: vi.fn(),
  // Inert: present only so real-ESM linking succeeds. Kept in every sibling factory, since a
  // missing name is a link-time `SyntaxError` for the whole file once the graph reaches it.
  apiGetTyped: vi.fn(),
  apiGetOrError: vi.fn(),
}));

vi.mock("../push-util.js", () => ({
  urlBase64ToUint8Array: (s: string) => new Uint8Array(s.length),
}));

import { unsubscribePush, registerPush } from "./notify.js";
import { apiGet } from "../api-client.js";
import { resetActionFramework } from "./__test-helpers__/action-test-setup.js";
import { getActionLog as recentLog } from "@cplieger/actions";
import * as toast from "../toast.js";

const mockFetch = vi.fn();

beforeEach(() => {
  resetActionFramework();
  vi.stubGlobal("fetch", mockFetch);
});

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("unsubscribePush", () => {
  it("has correct action name", () => {
    expect(unsubscribePush.name).toBe("notify.unsubscribe_push");
  });

  it("POSTs to /api/push/unsubscribe", async () => {
    mockFetch.mockResolvedValue(new Response(JSON.stringify({}), { status: 200 }));
    await unsubscribePush.dispatch({ endpoint: "https://push.example.com/sub1" });
    const [url, opts] = mockFetch.mock.calls[0]!;
    expect(url).toBe("/api/push/unsubscribe");
    expect(opts.method).toBe("POST");
    expect(JSON.parse(opts.body as string)).toEqual({ endpoint: "https://push.example.com/sub1" });
  });

  it("suppresses error toast on failure", async () => {
    mockFetch.mockResolvedValue(new Response(JSON.stringify({ error: "gone" }), { status: 410 }));
    await unsubscribePush.dispatch({ endpoint: "x" });
    expect(toast.error).not.toHaveBeenCalled();
  });
});

describe("registerPush", () => {
  it("has correct action name", () => {
    expect(registerPush.name).toBe("notify.register_push");
  });

  it("throws when serviceWorker not supported", async () => {
    // Production asks `"serviceWorker" in navigator`, and the property is an accessor on
    // Navigator.prototype, so only removing it there (restored in finally) makes it absent.
    const proto = Object.getPrototypeOf(navigator) as object;
    const orig = Object.getOwnPropertyDescriptor(proto, "serviceWorker");
    Reflect.deleteProperty(proto, "serviceWorker");
    try {
      const result = await registerPush.dispatch(undefined);
      expect(result).toBeNull();
      const log = recentLog();
      expect(log[0]?.status).toBe("error");
      expect(log[0]?.error?.code).toBe("unsupported");
    } finally {
      if (orig) {
        Object.defineProperty(proto, "serviceWorker", orig);
      }
    }
  });

  it("fails when VAPID key fetch returns null", async () => {
    const mockReg = { pushManager: { subscribe: vi.fn() } };
    Object.defineProperty(navigator, "serviceWorker", {
      value: { register: () => Promise.resolve(mockReg) },
      configurable: true,
    });
    vi.mocked(apiGet).mockResolvedValue(null);

    const result = await registerPush.dispatch(undefined);
    expect(result).toBeNull();
    const log = recentLog();
    expect(log[0]?.status).toBe("error");
    expect(log[0]?.error?.code).toBe("network");
  });

  it("rolls back toggle on failure", async () => {
    const toggle = document.createElement("input");
    toggle.type = "checkbox";
    toggle.id = "notify-toggle";
    toggle.checked = true;
    document.body.appendChild(toggle);

    // The rollback dispatches a custom event; wire the UI's listener to test the public contract.
    const onFailed = (): void => {
      toggle.checked = false;
    };
    document.addEventListener("notify:registration-failed", onFailed);

    Object.defineProperty(navigator, "serviceWorker", {
      value: undefined,
      configurable: true,
    });

    try {
      await registerPush.dispatch(undefined);
      expect(toggle.checked).toBe(false);
    } finally {
      document.removeEventListener("notify:registration-failed", onFailed);
      document.body.removeChild(toggle);
    }
  });
});
