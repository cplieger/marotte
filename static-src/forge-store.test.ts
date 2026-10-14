// The store is the one client copy of the forge rows and Sources' listings. The network is faked at the actions and
// the client; the store itself, its signals and its effects run for real.
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { effect } from "@cplieger/reactive";
import type * as ModStore from "./forge-store.js";
import type { ConfiguredForge, RepoList } from "./wire/types.gen.js";
import { forgeRow, forgesList } from "./__test-helpers__/forge-rows.js";

// Cache-buster: vi.resetModules() does not re-evaluate a module in Browser Mode, and every case needs an unstarted
// store.
let bootSeq = 0;

const H = vi.hoisted(() => ({
  listForges: vi.fn(),
  listRepoPage: vi.fn(),
  apiGetTyped: vi.fn(),
  sse: new Map<string, () => void>(),
  bus: new Map<string, () => void>(),
}));

vi.mock("./actions/forge-list.js", () => ({ listForges: { dispatch: H.listForges } }));
vi.mock("./actions/forge.js", () => ({
  listRepoPage: { dispatch: H.listRepoPage },
  // Read by forge-auth-repos.ts, whose pure folds the store uses.
  cloneRepo: { dispatch: vi.fn() },
  deleteLocal: { dispatch: vi.fn() },
}));
vi.mock("./api-client.js", () => ({ apiGetTyped: H.apiGetTyped }));
vi.mock("./bus.js", () => ({
  BUS_PAGE_RESUMED: "transport:resumed",
  BUS_RECONCILE: "transport:reconcile",
  onSSE: (type: string, fn: () => void) => {
    H.sse.set(type, fn);
    return () => undefined;
  },
  onBus: (type: string, fn: () => void) => {
    H.bus.set(type, fn);
    return () => undefined;
  },
}));

async function load(): Promise<typeof ModStore> {
  bootSeq += 1;
  return (await import(
    /* @vite-ignore */ `./forge-store.ts?boot=${String(bootSeq)}`
  )) as typeof ModStore;
}

interface Deferred<T> {
  promise: Promise<T>;
  resolve: (v: T) => void;
}

function deferred<T>(): Deferred<T> {
  let resolve: (v: T) => void = () => undefined;
  const promise = new Promise<T>((r) => {
    resolve = r;
  });
  return { promise, resolve };
}

/** Each rows read answers the next queued value; an empty queue answers `fallback`. */
function answerRows(
  ...answers: (ReturnType<typeof forgesList> | null | Deferred<unknown>)[]
): void {
  H.listForges.mockImplementation(() => {
    const next = answers.shift();
    if (next !== undefined && next !== null && "promise" in next) {
      return next.promise;
    }
    return Promise.resolve(next ?? null);
  });
}

function page(account: string, names: string[], next = ""): RepoList {
  return {
    account,
    next,
    repos: names.map((n) => ({ repo_id: `v1.${n}`, full_name: n }) as RepoList["repos"][number]),
  };
}

/** Answers every first-page read from `byPath`, a path to its answer (null is a failed read). */
function answerPages(byPath: (path: string) => Promise<RepoList | null>): void {
  H.apiGetTyped.mockImplementation((path: string) => byPath(path));
}

function repoReads(): string[] {
  return H.apiGetTyped.mock.calls.map((c: unknown[]) => String(c[0]));
}

const flush = (): Promise<void> => new Promise((r) => setTimeout(r, 0));

beforeEach(() => {
  H.listForges.mockReset();
  H.listRepoPage.mockReset();
  H.apiGetTyped.mockReset();
  H.sse.clear();
  H.bus.clear();
});

afterEach(() => {
  vi.useRealTimers();
});

describe("reading the rows", () => {
  it("ensureForges reads once, then answers what it holds", async () => {
    answerRows(forgesList([forgeRow({ username: "alice" })]));
    const store = await load();

    expect((await store.ensureForges())?.forges[0]?.username).toBe("alice");
    expect((await store.ensureForges())?.forges[0]?.username).toBe("alice");
    // The start's own read is the one ensureForges waited on.
    expect(H.listForges).toHaveBeenCalledTimes(1);
  });

  it("keeps the rows it holds through a failed read, and reports the failure", async () => {
    answerRows(forgesList([forgeRow()]), null);
    const store = await load();
    await store.ensureForges();

    expect(await store.readForges()).toBeNull();
    expect(store.forgeRows()?.forges).toHaveLength(1);
    expect(store.forgeRowsFailed()).toBe(true);
  });

  it("applies only the newest-issued read when an older one answers last", async () => {
    const a = deferred<unknown>();
    const b = deferred<unknown>();
    answerRows(forgesList([]), a, b);
    const store = await load();
    await store.ensureForges();

    const readA = store.readForges();
    const readB = store.readForges();
    b.resolve(forgesList([forgeRow({ username: "bob" })]));
    await readB;
    a.resolve(forgesList([forgeRow({ username: "alice" })]));
    await readA;

    expect(store.forgeRows()?.forges[0]?.username).toBe("bob");
  });
});

describe("what reads the rows again", () => {
  it("re-reads on forges_changed and on a page resume, and on nothing else", async () => {
    answerRows();
    const store = await load();
    store.forgeRows();
    expect(H.listForges).toHaveBeenCalledTimes(1);

    H.sse.get("forges_changed")?.();
    expect(H.listForges).toHaveBeenCalledTimes(2);
    H.bus.get("transport:resumed")?.();
    expect(H.listForges).toHaveBeenCalledTimes(3);
    // A reconnect's reconcile is covered by the connect read the boot hands the store.
    expect(H.bus.has("transport:reconcile")).toBe(false);
  });

  it("reads on a stream connect only once it has started", async () => {
    answerRows();
    const store = await load();

    store.notifyStreamConnected();
    expect(H.listForges).not.toHaveBeenCalled();
    store.usablePairs();
    store.notifyStreamConnected();
    expect(H.listForges).toHaveBeenCalledTimes(2);
  });

  it("sends nothing while idle", async () => {
    vi.useFakeTimers();
    answerRows(forgesList([forgeRow()]));
    const store = await load();
    store.forgeRows();
    await vi.advanceTimersByTimeAsync(60_000);

    expect(H.listForges).toHaveBeenCalledTimes(1);
  });

  it("starts from a subscribing effect", async () => {
    answerRows(forgesList([forgeRow()]));
    const store = await load();
    const seen: (readonly string[] | null)[] = [];
    effect(() => {
      seen.push(store.usablePairs());
    });
    await flush();

    expect(H.listForges).toHaveBeenCalledTimes(1);
    expect(seen.map((p) => p?.length ?? null)).toEqual([null, 1]);
  });
});

describe("the usable pairs", () => {
  it("move on a sign-in and a usable/unusable change, not on a retrying row's new message", async () => {
    const retrying = forgeRow({ status: "retrying", error: { message: "slow" } });
    answerRows(
      forgesList([retrying]),
      forgesList([{ ...retrying, error: { message: "slower" } }]),
      forgesList([{ ...retrying, account: "acct-2" }]),
      forgesList([{ ...retrying, account: "acct-2", status: "refused" }]),
    );
    const store = await load();
    await store.ensureForges();
    const first = store.usablePairs();

    await store.readForges();
    expect(store.usablePairs()).toBe(first);
    await store.readForges();
    const second = store.usablePairs();
    expect(second).not.toEqual(first);
    await store.readForges();
    expect(store.usablePairs()).toEqual([]);
  });
});

describe("the listings", () => {
  const A = forgeRow();
  const B = forgeRow({ account: "acct-2" });

  async function heldRows(...rows: ConfiguredForge[][]): Promise<typeof ModStore> {
    answerRows(...rows.map((r) => forgesList(r)));
    const store = await load();
    await store.ensureForges();
    return store;
  }

  it("starts one first-page read per (row, account), and none while it is in flight", async () => {
    const held = deferred<RepoList | null>();
    answerPages(() => held.promise);
    const store = await heldRows([A]);

    expect(store.ensureListing(A.id)).toBeUndefined();
    expect(store.ensureListing(A.id)).toBeUndefined();
    expect(repoReads()).toEqual(["/api/forges/github%3Agithub.com/repos"]);
    held.resolve(page("acct-1", ["one"]));
    await flush();

    expect(store.ensureListing(A.id)?.first.map((r) => r.full_name)).toEqual(["one"]);
    expect(repoReads()).toHaveLength(1);
  });

  it("reads again for a new account on the same row and never answers the old one's", async () => {
    answerPages(() => Promise.resolve(page("acct-1", ["one"])));
    const store = await heldRows([A], [B]);
    store.ensureListing(A.id);
    await flush();
    expect(store.ensureListing(A.id)).toBeDefined();

    answerPages(() => Promise.resolve(page("acct-2", ["two"])));
    await store.readForges();
    expect(store.ensureListing(A.id)).toBeUndefined();
    await flush();

    expect(store.ensureListing(A.id)?.first.map((r) => r.full_name)).toEqual(["two"]);
    expect(repoReads()).toHaveLength(2);
  });

  it("drops a first page read through a replaced account", async () => {
    const first = deferred<RepoList | null>();
    answerPages(() => first.promise);
    const store = await heldRows([A], [B]);
    store.ensureListing(A.id);
    await store.readForges();
    // The new sign-in gets its own read while the old one is still out.
    const second = deferred<RepoList | null>();
    answerPages(() => second.promise);
    expect(store.ensureListing(A.id)).toBeUndefined();
    expect(repoReads()).toHaveLength(2);
    first.resolve(page("acct-1", ["old"]));
    await flush();
    expect(store.ensureListing(A.id)).toBeUndefined();

    second.resolve(page("acct-2", ["two"]));
    await flush();
    expect(store.ensureListing(A.id)?.first.map((r) => r.full_name)).toEqual(["two"]);
  });

  it("drops a first page answered through another sign-in than the one it was asked for", async () => {
    answerPages(() => Promise.resolve(page("acct-0", ["stale"])));
    const store = await heldRows([A]);
    store.ensureListing(A.id);
    await flush();

    expect(store.ensureListing(A.id)).toBeUndefined();
  });

  it("sends no Load-more for the listing of a replaced account", async () => {
    answerPages(() => Promise.resolve(page("acct-1", ["one"], "cursor")));
    const store = await heldRows([A], [B]);
    store.ensureListing(A.id);
    await flush();
    await store.readForges();

    expect(await store.readMoreListing(A.id)).toBeUndefined();
    expect(H.listRepoPage).not.toHaveBeenCalled();
  });

  it("drops a Load-more page read through another account than the listing's", async () => {
    answerPages(() => Promise.resolve(page("acct-1", ["one"], "cursor")));
    const store = await heldRows([A]);
    store.ensureListing(A.id);
    await flush();
    H.listRepoPage.mockImplementation(() => ({
      outcome: Promise.resolve({ status: "success", value: page("acct-0", ["stale"]) }),
    }));

    expect(await store.readMoreListing(A.id)).toBeUndefined();
    expect(store.ensureListing(A.id)?.more).toEqual([]);
  });

  it("drops a Load-more page that answers after the row was signed in again", async () => {
    answerPages(() => Promise.resolve(page("acct-1", ["one"], "cursor")));
    const store = await heldRows([A], [B]);
    store.ensureListing(A.id);
    await flush();
    const more = deferred<unknown>();
    H.listRepoPage.mockImplementation(() => ({ outcome: more.promise }));
    const reading = store.readMoreListing(A.id);
    await store.readForges();
    more.resolve({ status: "success", value: page("acct-1", ["stale"]) });
    await reading;

    answerPages(() => Promise.resolve(page("acct-2", ["two"])));
    store.ensureListing(A.id);
    await flush();
    expect(store.ensureListing(A.id)?.more).toEqual([]);
  });

  it("appends a Load-more page read through the row's account", async () => {
    answerPages(() => Promise.resolve(page("acct-1", ["one"], "cursor")));
    const store = await heldRows([A]);
    store.ensureListing(A.id);
    await flush();
    H.listRepoPage.mockImplementation(() => ({
      outcome: Promise.resolve({ status: "success", value: page("acct-1", ["two"]) }),
    }));

    expect(await store.readMoreListing(A.id)).toBeUndefined();
    expect(store.ensureListing(A.id)?.more.map((r) => r.full_name)).toEqual(["two"]);
  });

  it("holds a failed first read, and reads it again only when the row applies connected", async () => {
    const retrying = { ...A, status: "retrying" as const, error: { message: "slow" } };
    answerPages(() => Promise.resolve(null));
    const store = await heldRows([A], [A], [retrying], [A]);
    store.ensureListing(A.id);
    await flush();
    expect(store.ensureListing(A.id)?.unread).toBe(true);
    store.ensureListing(A.id);
    await store.readForges();
    expect(repoReads()).toHaveLength(1);

    await store.readForges();
    expect(repoReads()).toHaveLength(1);
    await store.readForges();
    expect(repoReads()).toHaveLength(2);
  });

  it("lets the automatic re-read join a refresh in flight rather than answer over it from the cache", async () => {
    const retrying = { ...A, status: "retrying" as const, error: { message: "slow" } };
    const fresh = deferred<RepoList | null>();
    answerPages((path) => (path.endsWith("?refresh=1") ? fresh.promise : Promise.resolve(null)));
    const store = await heldRows([retrying], [A]);
    store.ensureListing(A.id);
    await flush();
    expect(store.ensureListing(A.id)?.unread).toBe(true);

    const refreshed = store.refreshListing(A.id);
    await store.readForges();
    expect(repoReads()).toEqual([
      "/api/forges/github%3Agithub.com/repos",
      "/api/forges/github%3Agithub.com/repos?refresh=1",
    ]);
    fresh.resolve(page("acct-1", ["fresh"]));

    expect(await refreshed).toBe(true);
    expect(store.ensureListing(A.id)?.first.map((r) => r.full_name)).toEqual(["fresh"]);
  });

  it.each([
    ["cached", "fresh"],
    ["fresh", "cached"],
  ] as const)(
    "applies a refresh over a cached read already out, answers landing %s then %s",
    async (...order) => {
      const cached = deferred<RepoList | null>();
      const fresh = deferred<RepoList | null>();
      answerPages((path) => (path.endsWith("?refresh=1") ? fresh.promise : cached.promise));
      const store = await heldRows([A]);
      store.ensureListing(A.id);
      const refreshed = store.refreshListing(A.id);
      expect(repoReads()).toHaveLength(2);

      const answer = {
        cached: () => {
          cached.resolve(page("acct-1", ["cached"]));
        },
        fresh: () => {
          fresh.resolve(page("acct-1", ["fresh"]));
        },
      };
      for (const which of order) {
        answer[which]();
        await flush();
      }

      expect(await refreshed).toBe(true);
      expect(store.ensureListing(A.id)?.first.map((r) => r.full_name)).toEqual(["fresh"]);
      expect(repoReads()).toHaveLength(2);
    },
  );

  it("drops a row's listing once the row is no longer usable", async () => {
    answerPages(() => Promise.resolve(page("acct-1", ["one"])));
    const store = await heldRows([A], [{ ...A, status: "refused", error: { message: "no" } }], [A]);
    store.ensureListing(A.id);
    await flush();
    await store.readForges();
    expect(store.ensureListing(A.id)).toBeUndefined();

    await store.readForges();
    expect(store.ensureListing(A.id)).toBeUndefined();
    expect(repoReads()).toHaveLength(2);
  });
});
