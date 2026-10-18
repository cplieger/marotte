import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { settingsPayload } from "./__test-helpers__/settings.js";

vi.mock("./api-client.js", () => ({
  apiGet: vi.fn(() => Promise.resolve({})),
  withTimeout: () => AbortSignal.timeout(30000),
  API_TIMEOUT_MS: 30000,
  // Present-but-inert so real-ESM linking succeeds: the tab projection widened this graph and these
  // names are imported somewhere in it. No case here calls them.
  apiGetTyped: vi.fn(),
}));
vi.mock("./save-indicator.js", () => ({
  showSaving: vi.fn(),
  showSaved: vi.fn(),
  showError: vi.fn(),
}));
vi.mock("./toast.js", () => ({
  info: vi.fn(),
  success: vi.fn(),
  error: vi.fn(),
  showToast: vi.fn(),
}));

import {
  patchSettings,
  initSettingsTracking,
  loadSettings,
  noteSettingsEvent,
  refreshSettings,
  settingsAdopter,
  __testResetTracking,
} from "./persist.js";

describe("patchSettings debounce coalescing", () => {
  let fetchSpy: ReturnType<typeof vi.fn>;

  beforeEach(() => {
    vi.useFakeTimers();
    vi.clearAllMocks();
    fetchSpy = vi.fn(() => Promise.resolve(new Response("{}", { status: 200 })));
    vi.stubGlobal("fetch", fetchSpy);
    // Reset the dedup tracker so tests don't bleed state between each other. Without this,
    // patchSettings({last_model: "claude"}) in test N+1 would be filtered out as a duplicate of
    // test N's value.
    __testResetTracking();
  });

  afterEach(() => {
    vi.useRealTimers();
    vi.unstubAllGlobals();
  });

  it("coalesces multiple rapid calls into a single PATCH with merged body", async () => {
    patchSettings({ notifications_enabled: true });
    patchSettings({ debug_logs: true });
    patchSettings({ last_model: "claude" });

    // Advance past debounce.
    await vi.advanceTimersByTimeAsync(350);

    expect(fetchSpy).toHaveBeenCalledTimes(1);
    const [url, init] = fetchSpy.mock.calls[0]!;
    expect(url).toBe("/api/settings");
    expect(init.method).toBe("PATCH");
    expect(JSON.parse(init.body as string)).toEqual({
      notifications_enabled: true,
      debug_logs: true,
      last_model: "claude",
    });
  });

  it("last-writer-wins for same key across rapid calls", async () => {
    patchSettings({ last_model: "gpt4" });
    patchSettings({ last_model: "claude" });
    patchSettings({ last_model: "gemini" });

    await vi.advanceTimersByTimeAsync(350);

    expect(fetchSpy).toHaveBeenCalledTimes(1);
    const [, init] = fetchSpy.mock.calls[0]!;
    expect(JSON.parse(init.body as string)).toEqual({
      last_model: "gemini",
    });
  });

  it("property: N rapid calls produce merged result matching Object.assign", async () => {
    // NOTE: This property test uses Math.random for fuzzing. Failures may not be reproducible
    // without seeding. Log `iter` on failure to narrow down.
    const iterations = 50;
    for (let iter = 0; iter < iterations; iter++) {
      fetchSpy.mockClear();
      // Each iteration is an independent batch; reset the dedup tracker so values from prior
      // iterations don't filter out patches in this one.
      __testResetTracking();
      const keys = ["notifications_enabled", "debug_logs", "last_model"] as const;
      const patches: Record<string, unknown>[] = [];
      const count = 1 + Math.floor(Math.random() * 5);
      for (let i = 0; i < count; i++) {
        const patch: Record<string, unknown> = {};
        for (const k of keys) {
          if (Math.random() > 0.5) {
            patch[k] =
              k === "last_model"
                ? ["a", "b", "c"][Math.floor(Math.random() * 3)]
                : Math.random() > 0.5;
          }
        }
        if (Object.keys(patch).length > 0) {
          patches.push(patch);
        }
      }
      if (patches.length === 0) {
        continue;
      }

      for (const patch of patches) {
        patchSettings(patch as Parameters<typeof patchSettings>[0]);
      }
      await vi.advanceTimersByTimeAsync(350);

      const expected: Record<string, unknown> = {};
      for (const patch of patches) {
        Object.assign(expected, patch);
      }

      const calls = fetchSpy.mock.calls;
      expect(calls.length).toBe(1);
      const [, init] = calls[0]!;
      expect(JSON.parse(init.body as string)).toEqual(expected);
    }
  });
});

describe("patchSettings no-op dedup", () => {
  let fetchSpy: ReturnType<typeof vi.fn>;

  beforeEach(() => {
    vi.useFakeTimers();
    vi.clearAllMocks();
    fetchSpy = vi.fn(() => Promise.resolve(new Response("{}", { status: 200 })));
    vi.stubGlobal("fetch", fetchSpy);
    __testResetTracking();
  });

  afterEach(() => {
    vi.useRealTimers();
    vi.unstubAllGlobals();
  });

  it("skips PATCH when the value matches the seeded server state (page-reload bootstrap case)", async () => {
    initSettingsTracking(settingsPayload({ supervised_default: false, last_model: "claude" }));
    // Simulate the bootstrap fire from onSelectionChange: same supervised_default value the server
    // already has.
    patchSettings({ supervised_default: false });
    await vi.advanceTimersByTimeAsync(350);
    expect(fetchSpy).not.toHaveBeenCalled();
  });

  it("PATCHes only the changed key when bootstrap and a real change overlap", async () => {
    initSettingsTracking(settingsPayload({ supervised_default: false, last_model: "claude" }));
    // supervised_default unchanged, last_model changed.
    patchSettings({ supervised_default: false });
    patchSettings({ last_model: "gemini" });
    await vi.advanceTimersByTimeAsync(350);
    expect(fetchSpy).toHaveBeenCalledTimes(1);
    expect(JSON.parse((fetchSpy.mock.calls[0]![1] as RequestInit).body as string)).toEqual({
      last_model: "gemini",
    });
  });

  it("the second identical patch in a session is also filtered", async () => {
    patchSettings({ last_model: "claude" });
    await vi.advanceTimersByTimeAsync(350);
    expect(fetchSpy).toHaveBeenCalledTimes(1);
    fetchSpy.mockClear();
    patchSettings({ last_model: "claude" });
    await vi.advanceTimersByTimeAsync(350);
    expect(fetchSpy).not.toHaveBeenCalled();
  });

  it("PATCHes again when the value changes back after a different value", async () => {
    initSettingsTracking(settingsPayload({ last_model: "claude" }));
    patchSettings({ last_model: "gemini" });
    await vi.advanceTimersByTimeAsync(350);
    expect(fetchSpy).toHaveBeenCalledTimes(1);
    fetchSpy.mockClear();
    // Round-tripping back to "claude" IS a change (current is "gemini").
    patchSettings({ last_model: "claude" });
    await vi.advanceTimersByTimeAsync(350);
    expect(JSON.parse((fetchSpy.mock.calls[0]![1] as RequestInit).body as string)).toEqual({
      last_model: "claude",
    });
  });

  it("array-valued settings dedup correctly", async () => {
    initSettingsTracking(settingsPayload({ agent_ignore_files: ["a", "b", "c"] }));
    patchSettings({ agent_ignore_files: ["a", "b", "c"] });
    await vi.advanceTimersByTimeAsync(350);
    expect(fetchSpy).not.toHaveBeenCalled();
    // Different order is treated as different (we use deterministic JSON).
    patchSettings({ agent_ignore_files: ["c", "b", "a"] });
    await vi.advanceTimersByTimeAsync(350);
    expect(JSON.parse((fetchSpy.mock.calls[0]![1] as RequestInit).body as string)).toEqual({
      agent_ignore_files: ["c", "b", "a"],
    });
  });

  it("does not fire showSaving when every key in the patch is filtered", async () => {
    initSettingsTracking(settingsPayload({ supervised_default: true }));
    const { showSaving } = await import("./save-indicator.js");
    patchSettings({ supervised_default: true });
    await vi.advanceTimersByTimeAsync(350);
    expect(showSaving).not.toHaveBeenCalled();
  });

  it("resolvers fire even when showSaved throws (try/finally guarantee)", async () => {
    const { showSaved } = await import("./save-indicator.js");
    const consoleErr = vi.spyOn(console, "error").mockImplementation(() => undefined);
    vi.mocked(showSaved).mockImplementation(() => {
      throw new Error("indicator boom");
    });
    const p = patchSettings({ last_model: "opus" });
    await vi.advanceTimersByTimeAsync(350);
    // The promise should still resolve (resolvers fire in finally block).
    const result = await p;
    expect(result).not.toBeUndefined();
    expect(consoleErr).toHaveBeenCalled();
    consoleErr.mockRestore();
    vi.mocked(showSaved).mockReset();
  });
});

describe("patchSettings unload flush", () => {
  let fetchSpy: ReturnType<typeof vi.fn>;

  beforeEach(() => {
    vi.useFakeTimers();
    vi.clearAllMocks();
    fetchSpy = vi.fn(() => Promise.resolve(new Response("{}", { status: 200 })));
    vi.stubGlobal("fetch", fetchSpy);
    __testResetTracking();
  });

  afterEach(() => {
    vi.useRealTimers();
    vi.unstubAllGlobals();
  });

  it("sends a still-debounced PATCH when the page is about to unload", async () => {
    patchSettings({ last_model: "opus" });
    // Inside the 300ms debounce: nothing has gone out yet, so navigating away now is where a
    // setting gets silently lost.
    expect(fetchSpy).not.toHaveBeenCalled();

    window.dispatchEvent(new Event("beforeunload"));
    await vi.advanceTimersByTimeAsync(0);

    expect(fetchSpy).toHaveBeenCalledTimes(1);
    expect(JSON.parse((fetchSpy.mock.calls[0]![1] as RequestInit).body as string)).toEqual({
      last_model: "opus",
    });
  });

  it("sends nothing on unload when there is no pending write", async () => {
    patchSettings({ last_model: "opus" });
    window.dispatchEvent(new Event("beforeunload"));
    await vi.advanceTimersByTimeAsync(0);
    fetchSpy.mockClear();

    // The queue drained on the first unload; a second one must not PATCH an empty body.
    window.dispatchEvent(new Event("beforeunload"));
    await vi.advanceTimersByTimeAsync(350);

    expect(fetchSpy).not.toHaveBeenCalled();
  });

  it("does not fire a cancelled debounce after the tracking reset", async () => {
    patchSettings({ last_model: "opus" });
    __testResetTracking();
    await vi.advanceTimersByTimeAsync(350);
    // A timer left running here would fire into whichever test came next.
    expect(fetchSpy).not.toHaveBeenCalled();
  });
});

describe("patchSettings failure handling", () => {
  let fetchSpy: ReturnType<typeof vi.fn>;

  beforeEach(() => {
    vi.useFakeTimers();
    vi.clearAllMocks();
    fetchSpy = vi.fn(() => Promise.resolve(new Response("{}", { status: 200 })));
    vi.stubGlobal("fetch", fetchSpy);
    __testResetTracking();
  });

  afterEach(() => {
    vi.useRealTimers();
    vi.unstubAllGlobals();
  });

  it("shows the error indicator when the server rejects the write", async () => {
    const { showError } = await import("./save-indicator.js");
    fetchSpy.mockImplementation(() => Promise.resolve(new Response("nope", { status: 500 })));
    patchSettings({ last_model: "opus" });
    // waitFor rather than a fixed advance: a real browser reads a failed response's body through a
    // stream, so the error path settles a macrotask or two after the debounce fires. The assertion
    // is unchanged; only the wait is event-driven instead of a guessed constant.
    await vi.waitFor(() => {
      expect(showError).toHaveBeenCalled();
    });
  });

  it("re-sends a value the server rejected instead of filtering it as sent", async () => {
    initSettingsTracking(settingsPayload({ last_model: "claude" }));
    fetchSpy.mockImplementation(() => Promise.resolve(new Response("nope", { status: 500 })));
    patchSettings({ last_model: "opus" });
    await vi.advanceTimersByTimeAsync(350);
    expect(fetchSpy).toHaveBeenCalledTimes(1);

    // The failed value was rolled back out of the dedup tracker, so asking for it again is a
    // change, not a repeat. Without the rollback the retry is dropped on the floor and the setting
    // never reaches the server.
    fetchSpy.mockClear();
    fetchSpy.mockImplementation(() => Promise.resolve(new Response("{}", { status: 200 })));
    patchSettings({ last_model: "opus" });
    await vi.advanceTimersByTimeAsync(350);

    expect(fetchSpy).toHaveBeenCalledTimes(1);
    expect(JSON.parse((fetchSpy.mock.calls[0]![1] as RequestInit).body as string)).toEqual({
      last_model: "opus",
    });
  });

  it("keeps the first pre-patch value when a key is written twice before flushing", async () => {
    initSettingsTracking(settingsPayload({ last_model: "claude" }));
    fetchSpy.mockImplementation(() => Promise.resolve(new Response("nope", { status: 500 })));
    // Two writes coalesce into one PATCH; the rollback has to restore the value the server still
    // holds ("claude"), not the intermediate one.
    patchSettings({ last_model: "opus" });
    patchSettings({ last_model: "sonnet" });
    await vi.advanceTimersByTimeAsync(350);

    fetchSpy.mockClear();
    fetchSpy.mockImplementation(() => Promise.resolve(new Response("{}", { status: 200 })));
    patchSettings({ last_model: "claude" });
    await vi.advanceTimersByTimeAsync(350);

    // "claude" is what the server has, so after a correct rollback this is a no-op. If the rollback
    // stored "opus" instead, this would PATCH.
    expect(fetchSpy).not.toHaveBeenCalled();
  });

  it("flips a checkbox back when the server rejects its write", async () => {
    const box = document.createElement("input");
    box.type = "checkbox";
    box.checked = true;
    fetchSpy.mockImplementation(() => Promise.resolve(new Response("nope", { status: 500 })));

    patchSettings({ notifications_enabled: true }, box);
    await vi.advanceTimersByTimeAsync(350);

    // The optimistic record is taken at dispatch time (the user just clicked, so the previous state
    // is the opposite), and the rollback restores it.
    expect(box.checked).toBe(false);
  });

  it("leaves the checkbox alone when the write succeeds", async () => {
    const box = document.createElement("input");
    box.type = "checkbox";
    box.checked = true;

    patchSettings({ notifications_enabled: true }, box);
    await vi.advanceTimersByTimeAsync(350);

    expect(box.checked).toBe(true);
  });
});

describe("patchSettings saving indicator", () => {
  let fetchSpy: ReturnType<typeof vi.fn>;

  beforeEach(() => {
    vi.useFakeTimers();
    vi.clearAllMocks();
    fetchSpy = vi.fn(() => Promise.resolve(new Response("{}", { status: 200 })));
    vi.stubGlobal("fetch", fetchSpy);
    __testResetTracking();
  });

  afterEach(() => {
    vi.useRealTimers();
    vi.unstubAllGlobals();
  });

  it("announces each call's own keys, so a later change still raises its slot", async () => {
    const { showSaving } = await import("./save-indicator.js");
    patchSettings({ last_model: "opus" });
    patchSettings({ debug_logs: true });
    await vi.advanceTimersByTimeAsync(350);
    // Per call rather than per batch: the two keys have separate slots, and the second one is
    // flipped inside the first's debounce window.
    expect(showSaving).toHaveBeenCalledTimes(2);
    expect(showSaving).toHaveBeenNthCalledWith(1, ["last_model"]);
    expect(showSaving).toHaveBeenNthCalledWith(2, ["debug_logs"]);
  });

  it("announces only the keys that survived the no-op filter", async () => {
    initSettingsTracking(settingsPayload({ debug_logs: true }));
    const { showSaving } = await import("./save-indicator.js");
    patchSettings({ debug_logs: true, last_model: "opus" });
    await vi.advanceTimersByTimeAsync(350);
    expect(showSaving).toHaveBeenCalledExactlyOnceWith(["last_model"]);
  });

  it("reports the outcome against the keys the write carried", async () => {
    const { showSaved } = await import("./save-indicator.js");
    patchSettings({ last_model: "opus", debug_logs: true });
    await vi.advanceTimersByTimeAsync(350);
    await vi.waitFor(() => {
      expect(showSaved).toHaveBeenCalled();
    });
    expect(showSaved).toHaveBeenCalledWith(["last_model", "debug_logs"]);
  });
});

describe("loadSettings", () => {
  let fetchSpy: ReturnType<typeof vi.fn>;

  beforeEach(() => {
    vi.clearAllMocks();
    fetchSpy = vi.fn(() =>
      Promise.resolve(
        // The COMPLETE payload: loadSettings decodes through the generated decoder, and every field
        // of EffectiveSettings is required, so a sparse body is a response the server cannot
        // produce and the decoder refuses.
        new Response(
          JSON.stringify(settingsPayload({ last_model: "sonnet", chat_retention_days: 30 })),
          {
            status: 200,
            headers: { "content-type": "application/json" },
          },
        ),
      ),
    );
    vi.stubGlobal("fetch", fetchSpy);
    __testResetTracking();
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("returns the settings the server sent", async () => {
    await expect(loadSettings()).resolves.toEqual(
      settingsPayload({ last_model: "sonnet", chat_retention_days: 30 }),
    );
  });
});

describe("settings reads in flight", () => {
  let answers: ((s: ReturnType<typeof settingsPayload>) => void)[];

  beforeEach(() => {
    answers = [];
    vi.stubGlobal(
      "fetch",
      vi.fn(
        () =>
          new Promise<Response>((resolve) => {
            answers.push((s) => {
              resolve(
                new Response(JSON.stringify(s), {
                  status: 200,
                  headers: { "content-type": "application/json" },
                }),
              );
            });
          }),
      ),
    );
    __testResetTracking();
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  async function settle(): Promise<void> {
    await new Promise((resolve) => setTimeout(resolve, 0));
  }

  const at = (revision: number): ReturnType<typeof settingsPayload> =>
    settingsPayload({ settings_revision: revision });

  it("shares a read in flight with a plain load", async () => {
    const first = loadSettings();
    const second = loadSettings();
    await settle();
    expect(answers).toHaveLength(1);
    answers[0]?.(at(3));
    expect((await first)?.settings_revision).toBe(3);
    expect((await second)?.settings_revision).toBe(3);
  });

  it("answers a refresh from a read sent after the newest event, one for every refresh made meanwhile", async () => {
    const first = loadSettings();
    noteSettingsEvent(3);
    const refreshes = [refreshSettings(), refreshSettings()];
    await settle();
    expect(answers, "no read starts while one is in flight").toHaveLength(1);

    answers[0]?.(at(3));
    await vi.waitFor(() => {
      expect(answers, "one more read once the first settled").toHaveLength(2);
    });
    await settle();
    expect(answers, "the refreshes share that one read").toHaveLength(2);
    answers[1]?.(at(4));
    expect((await first)?.settings_revision).toBe(3);
    for (const r of refreshes) {
      expect((await r)?.settings_revision).toBe(4);
    }
  });

  it("shares one read among the refreshes after one event", async () => {
    noteSettingsEvent(1);
    const refreshes = [refreshSettings(), refreshSettings()];
    await settle();
    expect(answers).toHaveLength(1);
    answers[0]?.(at(1));
    expect((await refreshes[0])?.settings_revision).toBe(1);
    await settle();
    expect(answers, "no second read for the same event").toHaveLength(1);
    expect((await refreshes[1])?.settings_revision).toBe(1);
  });

  it("answers a load a newer event made stale from the read sent after that event", async () => {
    const load = loadSettings();
    noteSettingsEvent(2);
    const refresh = refreshSettings();
    await settle();
    answers[0]?.(at(1));
    await vi.waitFor(() => {
      expect(answers).toHaveLength(2);
    });
    answers[1]?.(at(2));
    expect((await load)?.settings_revision).toBe(2);
    expect((await refresh)?.settings_revision).toBe(2);
  });

  it("answers null for a read below the held revision when no event came since it was sent", async () => {
    expect(settingsAdopter()(at(5))).toBe(true);
    const load = loadSettings();
    await settle();
    answers[0]?.(at(4));
    await expect(load).resolves.toBeNull();
    await settle();
    expect(answers, "no read follows a refusal no event explains").toHaveLength(1);
  });

  it("follows config.json down when a read sent before the reset answers the old count", async () => {
    noteSettingsEvent(5);
    const before = refreshSettings();
    await settle();
    answers[0]?.(at(5));
    expect((await before)?.settings_revision).toBe(5);

    const load = loadSettings();
    // config.json was replaced, so the next write counted from 1.
    noteSettingsEvent(1);
    const refresh = refreshSettings();
    await settle();
    answers[1]?.(at(5));
    await vi.waitFor(() => {
      expect(answers).toHaveLength(3);
    });
    answers[2]?.(at(1));
    expect((await load)?.settings_revision).toBe(1);
    expect((await refresh)?.settings_revision).toBe(1);
    expect(settingsAdopter()(at(1))).toBe(true);
  });
});

describe("settingsAdopter", () => {
  beforeEach(() => {
    __testResetTracking();
  });

  const at = (revision: number): ReturnType<typeof settingsPayload> =>
    settingsPayload({ settings_revision: revision });

  it("accepts a revision at or above the one held, and refuses one below it", () => {
    expect(settingsAdopter()(at(2))).toBe(true);
    expect(settingsAdopter()(at(2))).toBe(true);
    expect(settingsAdopter()(at(1))).toBe(false);
    expect(settingsAdopter()(at(3))).toBe(true);
    expect(settingsAdopter()(at(2))).toBe(false);
  });

  it("takes an event's revision as the held one, above or below it", () => {
    expect(settingsAdopter()(at(5))).toBe(true);
    noteSettingsEvent(7);
    expect(settingsAdopter()(at(6))).toBe(false);
    expect(settingsAdopter()(at(7))).toBe(true);
    noteSettingsEvent(1);
    expect(settingsAdopter()(at(1))).toBe(true);
    expect(settingsAdopter()(at(0))).toBe(false);
  });

  it("refuses an answer to a request sent before an event, above or below that event's count", () => {
    const below = settingsAdopter();
    const above = settingsAdopter();
    const equal = settingsAdopter();
    noteSettingsEvent(2);
    expect(below(at(1))).toBe(false);
    expect(above(at(3))).toBe(false);
    expect(equal(at(2))).toBe(true);
    expect(settingsAdopter()(at(3)), "a request sent after the event").toBe(true);
  });
});

// The indicator belongs to the NEWEST write of each key.

describe("patchSettings generation guard", () => {
  let fetchSpy: ReturnType<typeof vi.fn>;
  let pending: ((r: Response) => void)[];

  beforeEach(() => {
    vi.useFakeTimers();
    vi.clearAllMocks();
    pending = [];
    fetchSpy = vi.fn(
      () =>
        new Promise<Response>((resolve) => {
          pending.push(resolve);
        }),
    );
    vi.stubGlobal("fetch", fetchSpy);
    __testResetTracking();
  });

  afterEach(async () => {
    // Drain: an unresolved response holds the action's scope chain, and the chain is module state
    // that would stall the next test in this file. A queued dispatch reaches the network as its
    // predecessor answers, so this walks the list as it grows.
    for (const resolve of pending) {
      resolve(new Response("{}", { status: 200 }));
      await vi.advanceTimersByTimeAsync(0);
    }
    vi.useRealTimers();
    vi.unstubAllGlobals();
  });

  /** Send two writes: the first is on the wire, the second is queued behind it and has already
   *  claimed the newer generation. */
  async function firstOnTheWireSecondQueued(): Promise<void> {
    patchSettings({ last_model: "first" });
    await vi.advanceTimersByTimeAsync(350);
    patchSettings({ last_model: "second" });
    await vi.advanceTimersByTimeAsync(350);
    expect(fetchSpy).toHaveBeenCalledTimes(1);
  }

  it("does not report Saved for a response a newer write has overtaken", async () => {
    const { showSaved } = await import("./save-indicator.js");
    await firstOnTheWireSecondQueued();

    // The queued write reaches the network only as its predecessor answers, and a real browser
    // settles a response body over a stream rather than in a microtask, so both halves wait for the
    // observable event instead of a fixed tick count.
    pending[0]?.(new Response("{}", { status: 200 }));
    await vi.waitFor(() => {
      expect(pending).toHaveLength(2);
    });
    expect(showSaved).not.toHaveBeenCalled();

    // The newest write owns the indicator, and it still gets to say so.
    pending[1]?.(new Response("{}", { status: 200 }));
    await vi.waitFor(() => {
      expect(showSaved).toHaveBeenCalledTimes(1);
    });
  });

  it("does not report a failure a newer write has overtaken", async () => {
    const { showError } = await import("./save-indicator.js");
    await firstOnTheWireSecondQueued();

    // Same two-phase wait as the Saved case above.
    pending[0]?.(new Response("nope", { status: 500 }));
    await vi.waitFor(() => {
      expect(pending).toHaveLength(2);
    });
    expect(showError).not.toHaveBeenCalled();

    pending[1]?.(new Response("nope", { status: 500 }));
    await vi.waitFor(() => {
      expect(showError).toHaveBeenCalledTimes(1);
    });
  });

  it("still reports the keys the newer write did not carry", async () => {
    const { showSaved } = await import("./save-indicator.js");
    // The first write carries two settings; the second re-writes only one of them, so the other has
    // nothing left to answer it and must report now.
    patchSettings({ last_model: "first", debug_logs: true });
    await vi.advanceTimersByTimeAsync(350);
    patchSettings({ last_model: "second" });
    await vi.advanceTimersByTimeAsync(350);

    pending[0]?.(new Response("{}", { status: 200 }));
    await vi.waitFor(() => {
      expect(showSaved).toHaveBeenCalled();
    });
    expect(showSaved).toHaveBeenCalledExactlyOnceWith(["debug_logs"]);
  });
});
