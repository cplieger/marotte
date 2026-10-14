// The Changes tab re-reads git status when the usable forge accounts change, through the real forge store: a
// retrying account's new message each minute must not re-read it, and a sign-in must.
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { forgeRow, forgesList } from "./__test-helpers__/forge-rows.js";

const H = vi.hoisted(() => ({
  apiGet: vi.fn(),
  listForges: vi.fn(),
  sse: new Map<string, (() => void)[]>(),
}));

vi.mock("./api-client.js", async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  apiGet: H.apiGet,
  apiPost: vi.fn(),
  apiGetTyped: vi.fn(() => Promise.resolve(null)),
  apiGetOrError: vi.fn(() => Promise.resolve({ ok: false, status: 0, data: null, error: "" })),
}));
vi.mock("./bus.js", async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  onSSE: (type: string, fn: () => void) => {
    H.sse.set(type, [...(H.sse.get(type) ?? []), fn]);
    return () => undefined;
  },
  onBus: vi.fn(),
}));
vi.mock("./actions/index.js", async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  registerCleanup: vi.fn(),
  bindLoadingState: vi.fn(() => vi.fn()),
}));
vi.mock("./actions/forge-list.js", () => ({ listForges: { dispatch: H.listForges } }));
vi.mock("./actions/forge.js", () => ({
  listRepoPage: { dispatch: vi.fn() },
  cloneRepo: { dispatch: vi.fn() },
  deleteLocal: { dispatch: vi.fn() },
}));
vi.mock("./actions/git-changes.js", () => {
  const stub = { dispatch: vi.fn() };
  return {
    stage: stub,
    unstage: stub,
    discard: stub,
    pull: stub,
    pullAll: stub,
    push: stub,
    stash: stub,
    stashPop: stub,
    commit: stub,
    generateCommitMessage: stub,
  };
});

import { initChangesTab } from "./git-changes-tab.js";

/** Every subscriber of the frame, as the bus delivers it. */
function broadcast(type: string): void {
  for (const fn of H.sse.get(type) ?? []) {
    fn();
  }
}

function statusReads(): number {
  return H.apiGet.mock.calls.filter((c: unknown[]) =>
    String(c[0]).startsWith("/api/git/status-all"),
  ).length;
}

describe("the Changes tab over the forge store", () => {
  beforeEach(() => {
    vi.useFakeTimers();
    document.body.innerHTML = `<div id="git-changes-mount"></div>`;
    H.apiGet.mockResolvedValue({ repos: [] });
  });

  afterEach(() => {
    vi.useRealTimers();
    document.body.innerHTML = "";
  });

  it("starts the store, and re-reads git status on a sign-in but not on a retrying row's new message", async () => {
    const retrying = forgeRow({ status: "retrying", error: { message: "slow" } });
    const answers = [
      forgesList([retrying]),
      forgesList([{ ...retrying, error: { message: "slower" } }]),
      forgesList([
        retrying,
        forgeRow({ id: "gitea:git.example", kind: "gitea", host: "git.example" }),
      ]),
    ];
    H.listForges.mockImplementation(() => Promise.resolve(answers.shift() ?? null));

    initChangesTab();
    expect(H.listForges).toHaveBeenCalledTimes(1);
    await vi.advanceTimersByTimeAsync(1000);
    const before = statusReads();

    broadcast("forges_changed");
    await vi.advanceTimersByTimeAsync(1000);
    expect(statusReads()).toBe(before);

    broadcast("forges_changed");
    await vi.advanceTimersByTimeAsync(1000);
    expect(statusReads()).toBe(before + 1);
  });
});
