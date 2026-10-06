// What makes the store read the tree, and what does not. Its own file because it mocks the actions layer to count
// reads, where git-status-store.test.ts uses no mocks.
import { describe, it, expect, vi } from "vitest";

// Plain counters, not `vi.fn` history: the root config resets mocks between tests, and the claim is that starting the
// store registered nothing.
const { observed } = vi.hoisted(() => ({
  observed: { reads: 0, pollers: 0, sseSubs: 0 },
}));

vi.mock("./actions/index.js", () => ({
  apiAction: () => ({
    dispatch: () => Promise.resolve({ repos: [] }),
  }),
  defineAction: () => ({
    dispatch: () => {
      observed.reads++;
      return Promise.resolve({ repos: [] });
    },
  }),
  pollAction: () => {
    observed.pollers++;
  },
}));
// The bus, so a `turn_closed` subscription would be visible.
vi.mock("./bus.js", () => ({
  onSSE: () => {
    observed.sseSubs++;
  },
}));

const store = await import("./git-status-store.js");

describe("the git-status store's triggers", () => {
  // One test, because `started` is module state: the store starts once per module instance.
  it("reads nothing until a surface subscribes, then reads once and schedules nothing", async () => {
    // Importing is not a reason to scan.
    await Promise.resolve();
    expect(observed.reads).toBe(0);

    const off = store.onGitStatusChange(() => undefined);
    await Promise.resolve();
    expect(observed.reads).toBe(1);

    // No timer: an idle page costs nothing.
    expect(observed.pollers).toBe(0);
    // No `turn_closed` subscription: the fact arrives per completed tool call through markGitDirty.
    expect(observed.sseSubs).toBe(0);

    // Three surfaces subscribe and the read is the store's, so it happens once.
    const off2 = store.onGitStatusChange(() => undefined);
    const off3 = store.onGitStatusChange(() => undefined);
    await Promise.resolve();
    expect(observed.reads).toBe(1);

    off();
    off2();
    off3();
  });

  it("reads when a fact arrives, which is what replaced the timer", async () => {
    const before = observed.reads;
    await store.refreshGitStatus();
    expect(observed.reads).toBe(before + 1);
    await store.refreshGitStatus();
    expect(observed.reads).toBe(before + 2);
  });
});

// The catch-all for writers this client cannot see. Runs after the block above on purpose: the handler is guarded on
// the store having started, which the first test does.
describe("the tab coming back", () => {
  it("re-reads the tree, because that is when a stale badge would be read", () => {
    const before = observed.reads;
    document.dispatchEvent(new Event("visibilitychange"));
    expect(observed.reads).toBe(before + 1);
  });

  // Going away must cost nothing, or this is a scan per tab switch.
  it("reads nothing on the way out", () => {
    const spy = vi.spyOn(document, "hidden", "get").mockReturnValue(true);
    const before = observed.reads;
    document.dispatchEvent(new Event("visibilitychange"));
    expect(observed.reads).toBe(before);
    spy.mockRestore();
  });
});
