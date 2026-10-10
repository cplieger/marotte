// The subject is request count, which the store exists to cut; payloads only need to differ between calls.
import { describe, it, expect, vi, beforeEach } from "vitest";
import type * as ModStore from "./forge-store.js";

// Cache-buster: vi.resetModules() does not re-evaluate a module in Browser Mode (URL-keyed module map), and the
// store's `started` flag and payload signal must be fresh per case.
let bootSeq = 0;

const dispatch = vi.fn();
const pollAction = vi.fn();
const onSSE = vi.fn();

vi.mock("./actions/forge-list.js", () => ({ listForges: { dispatch, cancel: vi.fn() } }));
vi.mock("./actions/index.js", () => ({ pollAction, registerCleanup: vi.fn() }));
vi.mock("./bus.js", () => ({ onSSE }));

const payload = (username: string) => ({
  forges: [
    {
      id: "github:github.com",
      kind: "github" as const,
      host: "github.com",
      username,
      connected: true,
      reconnect_required: false,
    },
  ],
  kinds: ["github", "gitlab", "codeberg", "gitea"] as const,
  oauth: { github: true },
});

async function load(): Promise<typeof ModStore> {
  bootSeq += 1;
  return (await import(
    /* @vite-ignore */ `./forge-store.ts?boot=${String(bootSeq)}`
  )) as typeof ModStore;
}

beforeEach(() => {
  dispatch.mockReset();
  pollAction.mockReset();
  onSSE.mockReset();
});

describe("forge-store read-through", () => {
  it("ensureForges fetches once, then answers from what it already has", async () => {
    dispatch.mockResolvedValue(payload("alice"));
    const store = await load();

    const first = await store.ensureForges();
    const second = await store.ensureForges();

    expect(first?.forges[0]?.username).toBe("alice");
    expect(second).toEqual(first);
    // A second reader must not add a round trip once the list is known.
    expect(dispatch).toHaveBeenCalledTimes(1);
  });

  it("refreshForges always fetches, because its callers have a reason to distrust the cache", async () => {
    dispatch.mockResolvedValueOnce(payload("alice")).mockResolvedValueOnce(payload("bob"));
    const store = await load();

    await store.ensureForges();
    const forced = await store.refreshForges();

    expect(dispatch).toHaveBeenCalledTimes(2);
    expect(forced?.forges[0]?.username).toBe("bob");
    expect((await store.ensureForges())?.forges[0]?.username).toBe("bob");
  });

  it("publishes the payload to its accessors", async () => {
    dispatch.mockResolvedValue(payload("alice"));
    const store = await load();

    expect(store.oauthByKind()).toEqual({});

    await store.ensureForges();

    expect(store.oauthByKind()).toEqual({ github: true });
  });
});

describe("forge-store failure handling", () => {
  it("reports a failure without discarding the last good payload", async () => {
    dispatch.mockResolvedValueOnce(payload("alice")).mockResolvedValueOnce(null);
    const store = await load();

    await store.ensureForges();
    expect(store.forgeLoadFailed()).toBe(false);

    await store.refreshForges();

    expect(store.forgeLoadFailed()).toBe(true);
    // Blanking would turn one bad round trip into "no forges connected" in the PRs tab.
    expect((await store.ensureForges())?.forges).toHaveLength(1);
  });

  it("a failed first load leaves the list empty and retries on the next read", async () => {
    dispatch.mockResolvedValueOnce(null).mockResolvedValueOnce(payload("alice"));
    const store = await load();

    expect(await store.ensureForges()).toBeNull();
    expect(store.forgeLoadFailed()).toBe(true);
    // Nothing was cached, so the next read reaches the endpoint again instead of serving the failure.
    expect(await store.ensureForges()).not.toBeNull();
    expect(store.forgeLoadFailed()).toBe(false);
    expect(dispatch).toHaveBeenCalledTimes(2);
  });
});

describe("forge-store lifecycle", () => {
  it("starts exactly one poll and one invalidation listener, however many init paths call it", async () => {
    dispatch.mockResolvedValue(payload("alice"));
    const store = await load();

    store.initForgeStore();
    store.initForgeStore();
    store.initForgeStore();

    // Several modules reach init, so a second timer here would be the duplication this store removed.
    expect(pollAction).toHaveBeenCalledTimes(1);
    expect(onSSE).toHaveBeenCalledTimes(1);
    expect(onSSE.mock.calls[0]?.[0]).toBe("forges_changed");
    expect(pollAction.mock.calls[0]?.[2]).toMatchObject({ interval: 15_000 });
  });
});
