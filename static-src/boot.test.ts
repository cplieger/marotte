// The boot: reads ISSUED TOGETHER in the first frame, each answer adopted where it lands,
// and the identity verdict gating one sidebar row rather than the app. Every collaborator
// is mocked: under test are the ORDER of reads and the branches over their answers.

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import type * as BootModule from "./boot.js";
import type * as RoutePath from "./route-path.js";
import type { Route } from "./route-path.js";
import type { RouteOrigin } from "./router.js";
import type { BootSnapshot } from "./boot-snapshot.js";
import type { IdentityVerdict } from "./identity.js";
import type { EffectiveSettings } from "./persist.js";
import { settingsPayload } from "./__test-helpers__/settings.js";

/** A promise plus the handle to settle it, so a test can hold one read open and
 *  watch what the boot does without it. */
function deferred<T>(): { promise: Promise<T>; resolve: (v: T) => void } {
  let resolve!: (v: T) => void;
  const promise = new Promise<T>((r) => {
    resolve = r;
  });
  return { promise, resolve };
}

const m = vi.hoisted(() => {
  // The router's suppression COUNT, modelled: `pushRoute`/`replaceRoute` return early above
  // zero (router.ts), so the depth a write is made AT is the question at each window edge.
  const suppression = { depth: 0 };
  // The router's location CLAIM, modelled: it decides whether a push to a DIFFERENT location
  // lands.
  const claim = { path: "" };
  /** The depth each URL-writing call was made at, in order. */
  const depths = {
    activate: [] as number[],
    replaceRoute: [] as number[],
    share: [] as number[],
  };
  /** The claim standing at each call that applies a route, in order. */
  const claims = { applyRoute: [] as string[] };
  return {
    suppression,
    claim,
    depths,
    claims,
    resetDepths: (): void => {
      suppression.depth = 0;
      claim.path = "";
      depths.activate.length = 0;
      depths.replaceRoute.length = 0;
      depths.share.length = 0;
      claims.applyRoute.length = 0;
    },
    loadList: vi.fn(),
    loadSettings: vi.fn(),
    resolveIdentity: vi.fn(),
    refreshRetention: vi.fn(),
    listTabs: vi.fn(),
    renderIdentity: vi.fn(),
    restoreAll: vi.fn(),
    restoreShell: vi.fn(),
    shellPanel: document.createElement("div"),
    adoptThemeFromSettings: vi.fn(),
    initPostAuthUI: vi.fn(),
    showLoginModal: vi.fn(),
    createSession: vi.fn(),
    // Records the depth: `activateTab`'s onShow ends in `pushRoute`.
    activateRestoredTab: vi.fn(() => {
      depths.activate.push(suppression.depth);
    }),
    markHydrated: vi.fn(),
    markBootDone: vi.fn(),
    applyShareTarget: vi.fn(() => {
      depths.share.push(suppression.depth);
      return Promise.resolve();
    }),
    toastError: vi.fn(),
    getSessions: vi.fn(),
    getActive: vi.fn(),
    getActiveId: vi.fn(),
    getActiveTabRoute: vi.fn(),
    setStatus: vi.fn(),
    // Records the claim standing when the route is applied: two lazy arms open their view after
    // the call returns, so the claim outlives the promise. Typed for the origin cases.
    applyRoute: vi.fn<(route: Route, origin?: RouteOrigin) => Promise<void>>(() => {
      claims.applyRoute.push(claim.path);
      return Promise.resolve();
    }),
    navigationOrigin: vi.fn<() => RouteOrigin>(() => "deeplink"),
    readBootSnapshot: vi.fn(),
    paintBootSnapshot: vi.fn(),
    clearBootSnapshot: vi.fn(),
    startBootSnapshot: vi.fn(),
    clearDeviceKeys: vi.fn(),
    resetFoldState: vi.fn(),
    parseRoute: vi.fn(() => ({ kind: "chat", id: "" })),
    replaceRoute: vi.fn(() => {
      depths.replaceRoute.push(suppression.depth);
    }),
    suppressPush: vi.fn((v: boolean) => {
      suppression.depth = v ? suppression.depth + 1 : Math.max(0, suppression.depth - 1);
    }),
    claimLocation: vi.fn((path: string) => {
      claim.path = path;
    }),
    releaseLocation: vi.fn(() => {
      claim.path = "";
    }),
    initGovernance: vi.fn(),
    initRuntimeHealth: vi.fn(),
    initStatusVersions: vi.fn(),
    loadVersions: vi.fn(),
    fetchCatalog: vi.fn(),
    rebuildLiveRuns: vi.fn(),
    registerEvictionExemption: vi.fn(),
    // Typed, because the case below reads a registered predicate back OFF the mock and
    // calls it: one of the two is an arrow with no identity to compare against.
    registerRunStateDemand: vi.fn<(fn: (id: string) => boolean) => () => void>(
      () => () => undefined,
    ),
    registerTurnRepair: vi.fn(),
    registerRevertReadAbort: vi.fn(),
    requestTurnRange: vi.fn(),
    abortReadsForRevert: vi.fn(),
    registerRunTurnRepair: vi.fn(),
    requestRunTurnRange: vi.fn(),
    subagentTabProjectsChat: vi.fn(),
    chatTabFoldsRun: vi.fn(),
    hasTab: vi.fn(() => false),
    showBanner: vi.fn(),
    bootMode: vi.fn(() => "full"),
    reloadCount: vi.fn(() => 1),
    clearReloadGuard: vi.fn(),
    noteBootAlive: vi.fn(),
    blur: vi.fn(),
    // Typed, because the cases below read the registered listener back OFF the mock
    // and call it: an inferred zero-arg signature makes `mock.calls` an empty tuple.
    subscribeByName: vi.fn<
      (name: string, fn: (inst: { readonly status: string }) => void) => () => void
    >(() => () => undefined),
  };
});

vi.mock("./actions/index.js", () => ({ subscribeByName: m.subscribeByName }));
// Only the action's NAME is read here (boot.ts subscribes by it rather than by a
// literal), so the definition itself needs no behaviour.
vi.mock("./actions/settings.js", () => ({ logout: { name: "settings.logout" } }));
vi.mock("./store-load.js", () => ({
  loadList: m.loadList,
  requestTurnRange: m.requestTurnRange,
  abortReadsForRevert: m.abortReadsForRevert,
}));
vi.mock("./persist.js", () => ({}));
vi.mock("./store.js", () => ({
  getActive: m.getActive,
  getActiveId: m.getActiveId,
  getSessions: m.getSessions,
  registerEvictionExemption: m.registerEvictionExemption,
  registerTurnRepair: m.registerTurnRepair,
  registerRevertReadAbort: m.registerRevertReadAbort,
  startEvictionSweep: vi.fn(),
}));
vi.mock("./settings.js", () => ({
  adoptThemeFromSettings: m.adoptThemeFromSettings,
  initPostAuthUI: m.initPostAuthUI,
  loadSettings: m.loadSettings,
  renderIdentity: m.renderIdentity,
  restoreAll: m.restoreAll,
}));
vi.mock("./session-context.js", () => ({
  restoreLastEffort: vi.fn(),
  restoreLastModel: vi.fn(),
}));
vi.mock("./identity.js", () => ({ resolveIdentity: m.resolveIdentity }));
vi.mock("./session-catalog.js", () => ({ fetchCatalog: m.fetchCatalog }));
vi.mock("./sse-adapter.js", () => ({ markHydrated: m.markHydrated }));
vi.mock("./modals.js", () => ({ showLoginModal: m.showLoginModal }));
vi.mock("./tabs.js", () => ({
  activateRestoredTab: m.activateRestoredTab,
  getActiveTabRoute: m.getActiveTabRoute,
  hasTab: m.hasTab,
}));
vi.mock("./tabs-sync.js", () => ({ listTabs: m.listTabs }));
vi.mock("./router.js", () => ({
  navigationOrigin: m.navigationOrigin,
  replaceRoute: m.replaceRoute,
  suppressPush: m.suppressPush,
  claimLocation: m.claimLocation,
  releaseLocation: m.releaseLocation,
}));
// Spread the original: Browser Mode links for real, and `buildPath` lives here too.
vi.mock("./route-path.js", async (importOriginal) => ({
  ...(await importOriginal<typeof RoutePath>()),
  parseRoute: m.parseRoute,
}));
vi.mock("./chat.js", () => ({ createSession: m.createSession }));
vi.mock("./governance.js", () => ({ initGovernance: m.initGovernance }));
vi.mock("./runtime-health.js", () => ({ initRuntimeHealth: m.initRuntimeHealth }));
vi.mock("./status.js", () => ({
  initStatusVersions: m.initStatusVersions,
  setStatus: m.setStatus,
}));
vi.mock("./versions.js", () => ({ loadVersions: m.loadVersions }));
vi.mock("./retention.js", () => ({ refreshRetention: m.refreshRetention }));
// Present so re-adding a boot call to it fails the assertion, not the LINK.
vi.mock("./run-store.js", () => ({
  rebuildLiveRuns: m.rebuildLiveRuns,
  registerRunStateDemand: m.registerRunStateDemand,
  registerRunTurnRepair: m.registerRunTurnRepair,
}));
vi.mock("./run-turn-range.js", () => ({ requestRunTurnRange: m.requestRunTurnRange }));
vi.mock("./chat-run-dots.js", () => ({ chatTabFoldsRun: m.chatTabFoldsRun }));
vi.mock("./subagent-view.js", () => ({ subagentTabProjectsChat: m.subagentTabProjectsChat }));
vi.mock("./view-swap.js", () => ({ markBootDone: m.markBootDone }));
vi.mock("./share-target.js", () => ({ applyShareTarget: m.applyShareTarget }));
vi.mock("./toast.js", () => ({ error: m.toastError }));
vi.mock("./boot-snapshot.js", () => ({
  readBootSnapshot: m.readBootSnapshot,
  paintBootSnapshot: m.paintBootSnapshot,
  clearBootSnapshot: m.clearBootSnapshot,
  startBootSnapshot: m.startBootSnapshot,
}));
vi.mock("./ls-keys.js", () => ({
  clearDeviceKeys: m.clearDeviceKeys,
  LS_UI_STATE_KEY: "marotte.ui-state",
}));
vi.mock("./fold-state.js", () => ({ resetFoldState: m.resetFoldState }));
vi.mock("./banner-stack.js", () => ({ GLOBAL_BANNER: "*", showBanner: m.showBanner }));
// The composer, reached for one call only: a reduced boot blurs it. And the shell
// panel, which a signed-out boot shuts.
vi.mock("./dom.js", () => ({ $: { promptInput: { blur: m.blur }, shellPanel: m.shellPanel } }));
vi.mock("./shell.js", () => ({ restoreShell: m.restoreShell }));
vi.mock("./reload-guard.js", () => ({
  bootMode: m.bootMode,
  reloadCount: m.reloadCount,
  clearReloadGuard: m.clearReloadGuard,
  noteBootAlive: m.noteBootAlive,
}));

/**
 * A fresh module per test: `vi.resetModules()` does not re-evaluate in Browser Mode, so a
 * busted specifier mints one (`.ts` for coverage attribution).
 */
let bootSeq = 0;
async function freshBoot(): Promise<typeof BootModule> {
  bootSeq++;
  return (await import(/* @vite-ignore */ `./boot.ts?t=${bootSeq}`)) as typeof BootModule;
}

const SIGNED_IN: IdentityVerdict = { state: "signed_in", email: "someone@example.test" };

/** A record with something in it, so a paint that DID happen is distinguishable
 *  from the empty default. */
const SNAPSHOT: BootSnapshot = {
  tabs: [{ id: "t1", kind: "chat", ref: "c1", parent: "", pinned: false, owns: true }],
  chats: [],
  window: null,
};

/** The default happy answers: settings load, one chat, the tab set adopts, and no
 *  snapshot — a first-ever boot on this screen. */
function arrangeHappy(settings: EffectiveSettings = settingsPayload()): void {
  m.loadSettings.mockResolvedValue(settings);
  m.resolveIdentity.mockResolvedValue(SIGNED_IN);
  m.loadList.mockResolvedValue(true);
  m.refreshRetention.mockResolvedValue(undefined);
  m.listTabs.mockResolvedValue(true);
  m.createSession.mockResolvedValue(undefined);
  m.getSessions.mockReturnValue([{ id: "c1" }]);
  // No chat and no restored non-chat tab, so the default "/" boot canonicalizes
  // nothing until a case says otherwise.
  m.getActive.mockReturnValue(undefined);
  m.getActiveId.mockReturnValue("");
  m.getActiveTabRoute.mockReturnValue(null);
  m.readBootSnapshot.mockResolvedValue(null);
  m.paintBootSnapshot.mockReturnValue(false);
  // `clearAllMocks` drops a mock's implementation as well as its calls, so the two
  // guard readers are re-armed here rather than at their declaration.
  m.bootMode.mockReturnValue("full");
  m.reloadCount.mockReturnValue(1);
  m.clearBootSnapshot.mockResolvedValue(undefined);
  m.navigationOrigin.mockReturnValue("deeplink");
}

beforeEach(() => {
  vi.clearAllMocks();
  // Neither the depth nor the recorded call sites are mock state, so
  // `clearAllMocks` does not reach them.
  m.resetDepths();
  arrangeHappy();
});

describe("the boot issues its reads together", () => {
  it("has all five in flight before any of them answers", async () => {
    // Held open, all of them, so the only way a call can be recorded below is if
    // the boot issued it WITHOUT waiting for the others.
    const settings = deferred<EffectiveSettings>();
    const identity = deferred<IdentityVerdict>();
    const chats = deferred<boolean>();
    const retention = deferred<undefined>();
    m.loadSettings.mockReturnValue(settings.promise);
    m.resolveIdentity.mockReturnValue(identity.promise);
    m.loadList.mockReturnValue(chats.promise);
    m.refreshRetention.mockReturnValue(retention.promise);

    const { startBoot } = await freshBoot();
    const booted = startBoot({ applyRoute: m.applyRoute });

    expect(m.loadSettings).toHaveBeenCalledTimes(1);
    expect(m.resolveIdentity).toHaveBeenCalledTimes(1);
    expect(m.loadList).toHaveBeenCalledTimes(1);
    expect(m.refreshRetention).toHaveBeenCalledTimes(1);
    expect(m.readBootSnapshot).toHaveBeenCalledTimes(1);
    // And nothing has been adopted, because nothing has answered.
    expect(m.renderIdentity).not.toHaveBeenCalled();
    expect(m.restoreAll).not.toHaveBeenCalled();

    settings.resolve(settingsPayload());
    identity.resolve(SIGNED_IN);
    chats.resolve(true);
    retention.resolve(undefined);
    await booted;
  });

  it("renders the identity row without waiting for the chat list", async () => {
    const chats = deferred<boolean>();
    m.loadList.mockReturnValue(chats.promise);

    const { startBoot } = await freshBoot();
    const booted = startBoot({ applyRoute: m.applyRoute });
    await vi.waitFor(() => {
      expect(m.renderIdentity).toHaveBeenCalledWith(SIGNED_IN);
    });
    // The strip is still pending: the tab set is adopted after the chat fold,
    // because a chat tab's row is named from the chat store.
    expect(m.listTabs).not.toHaveBeenCalled();

    chats.resolve(true);
    await booted;
    expect(m.listTabs).toHaveBeenCalledTimes(1);
  });

  it("restores settings without waiting for the identity verdict", async () => {
    const identity = deferred<IdentityVerdict>();
    m.resolveIdentity.mockReturnValue(identity.promise);

    const { startBoot } = await freshBoot();
    const booted = startBoot({ applyRoute: m.applyRoute });
    await vi.waitFor(() => {
      expect(m.restoreAll).toHaveBeenCalledTimes(1);
      expect(m.adoptThemeFromSettings).toHaveBeenCalledTimes(1);
    });
    // …and the workspace comes up too: the whole point of the fan-out is that a
    // 5-second whoami cannot hold the app back.
    await vi.waitFor(() => {
      expect(m.activateRestoredTab).toHaveBeenCalledTimes(1);
    });

    identity.resolve(SIGNED_IN);
    await booted;
  });
});

describe("the identity verdict gates one row", () => {
  it("comes up working when whoami is unavailable, and offers a re-read", async () => {
    m.resolveIdentity.mockResolvedValue({ state: "unavailable", reason: "timed out" });

    const { startBoot } = await freshBoot();
    await startBoot({ applyRoute: m.applyRoute });

    // NOT a sign-out: no login modal over a working app.
    expect(m.showLoginModal).not.toHaveBeenCalled();
    // The app is interactive: the tab set was adopted and a tab activated.
    expect(m.listTabs).toHaveBeenCalledTimes(1);
    expect(m.activateRestoredTab).toHaveBeenCalledTimes(1);
    // And the post-auth fan-out ran, so the app is not half-booted.
    expect(m.initPostAuthUI).toHaveBeenCalledTimes(1);
    // With a way back to a real answer.
    expect(m.toastError).toHaveBeenCalledWith(
      expect.stringContaining("timed out"),
      expect.objectContaining({ label: "Retry" }),
    );
  });

  it("paints an EMPTY workspace's strip while whoami is still pending", async () => {
    // A first run has no chats and the starter chat needs the verdict; nothing else waits.
    const identity = deferred<IdentityVerdict>();
    m.resolveIdentity.mockReturnValue(identity.promise);
    m.getSessions.mockReturnValue([]);

    const { startBoot } = await freshBoot();
    const booted = startBoot({ applyRoute: m.applyRoute });

    await vi.waitFor(() => {
      expect(m.listTabs).toHaveBeenCalledTimes(1);
      expect(m.activateRestoredTab).toHaveBeenCalledTimes(1);
    });
    expect(m.createSession).not.toHaveBeenCalled();

    identity.resolve(SIGNED_IN);
    await booted;
    // And the chat still arrives, once the verdict says it may.
    expect(m.createSession).toHaveBeenCalledTimes(1);
  });

  it("raises the login modal on a sign-out and mints no chat behind it", async () => {
    m.resolveIdentity.mockResolvedValue({ state: "signed_out" });
    m.getSessions.mockReturnValue([]);

    const { startBoot } = await freshBoot();
    await startBoot({ applyRoute: m.applyRoute });

    expect(m.showLoginModal).toHaveBeenCalledTimes(1);
    // And the next boot must not paint this workspace at a login screen.
    expect(m.clearBootSnapshot).toHaveBeenCalledTimes(1);
    // A starter chat is `onLoginSuccess`'s to create, once the identity is real.
    expect(m.createSession).not.toHaveBeenCalled();
    // No post-auth fetches on the login screen.
    expect(m.initPostAuthUI).not.toHaveBeenCalled();
    // Held SSE frames are released anyway: nothing hydrates behind a login modal.
    expect(m.markHydrated).toHaveBeenCalled();
  });

  it("says nothing about unreachable chats while the login modal is up", async () => {
    m.resolveIdentity.mockResolvedValue({ state: "signed_out" });
    m.loadList.mockResolvedValue(false);
    m.getSessions.mockReturnValue([]);

    const { startBoot } = await freshBoot();
    await startBoot({ applyRoute: m.applyRoute });

    expect(m.toastError).not.toHaveBeenCalledWith("Could not load your chats.", expect.anything());
  });

  it("complains about unreachable chats when someone IS signed in", async () => {
    m.loadList.mockResolvedValue(false);
    m.getSessions.mockReturnValue([]);

    const { startBoot } = await freshBoot();
    await startBoot({ applyRoute: m.applyRoute });

    expect(m.toastError).toHaveBeenCalledWith(
      "Could not load your chats.",
      expect.objectContaining({ label: "Reload" }),
    );
    // And the empty state still gets its fallback chat.
    expect(m.createSession).toHaveBeenCalledTimes(1);
  });
});

describe("the chat list is read once per cold boot", () => {
  it("does not re-read it on the connection the boot itself rides", async () => {
    const { startBoot, onTransportStatus } = await freshBoot();
    // app.ts opens the transport BEFORE the boot, so a cold load's first `connected` arrives
    // mid-read and must not fetch the list a second time.
    onTransportStatus("connected");
    await startBoot({ applyRoute: m.applyRoute });

    expect(m.loadList).toHaveBeenCalledTimes(1);
  });

  it("re-reads it on a RE-connect, which is what the hook is for", async () => {
    const { startBoot, onTransportStatus } = await freshBoot();
    onTransportStatus("connected");
    await startBoot({ applyRoute: m.applyRoute });
    onTransportStatus("disconnected");
    onTransportStatus("connected");

    expect(m.loadList).toHaveBeenCalledTimes(2);
  });

  it("recovers a read that FAILED while the stream was already up", async () => {
    // The case the latch's ANSWER half exists for: the boot's connection is skipped, the read
    // fails, and a stream that never dropped sends no later `connected`.
    m.loadList.mockResolvedValue(false);
    m.getSessions.mockReturnValue([]);

    const { startBoot, onTransportStatus } = await freshBoot();
    onTransportStatus("connected");
    await startBoot({ applyRoute: m.applyRoute });

    // The boot's own read, plus the recovery it triggered. No third: the hook
    // skipped the connect, because at that moment the read had not settled.
    expect(m.loadList).toHaveBeenCalledTimes(2);
  });

  it("does not recover a read that SUCCEEDED", async () => {
    const { startBoot, onTransportStatus } = await freshBoot();
    onTransportStatus("connected");
    await startBoot({ applyRoute: m.applyRoute });

    expect(m.loadList).toHaveBeenCalledTimes(1);
  });

  it("does not recover a failed read with no stream up, because a connect will cover it", async () => {
    // The offline boot below: nothing to fetch over yet, so the recovery must not
    // fire a request into a dead link — the first `connected` is what covers it.
    m.loadList.mockResolvedValue(false);
    m.getSessions.mockReturnValue([]);

    const { startBoot } = await freshBoot();
    await startBoot({ applyRoute: m.applyRoute });

    expect(m.loadList).toHaveBeenCalledTimes(1);
  });

  it("reads it on the FIRST connect when the boot's own read failed", async () => {
    // An offline boot: the first connect, seconds later, carries no Last-Event-ID and the
    // boot's read answered nothing, so this connect must fetch.
    m.loadList.mockResolvedValue(false);
    m.getSessions.mockReturnValue([]);

    const { startBoot, onTransportStatus } = await freshBoot();
    await startBoot({ applyRoute: m.applyRoute });
    expect(m.loadList).toHaveBeenCalledTimes(1);

    onTransportStatus("connected");

    expect(m.loadList).toHaveBeenCalledTimes(2);
  });
});

// Two windows: the activation must not write the URL, the canonicalization must.
describe("the push-suppression window", () => {
  it("covers the resumed activation, so a deep-linked launch survives to be read", async () => {
    // Unsuppressed, `activateRestoredTab`'s `pushRoute` adds a history entry and rewrites the
    // path before `applyInitialRoute` reads it.
    m.paintBootSnapshot.mockReturnValue(true);

    const { startBoot } = await freshBoot();
    await startBoot({ applyRoute: m.applyRoute });

    expect(m.depths.activate).toEqual([1]);
  });

  it("covers the tab set's own activation when there was nothing to resume", async () => {
    const { startBoot } = await freshBoot();
    await startBoot({ applyRoute: m.applyRoute });

    expect(m.depths.activate).toEqual([1]);
  });

  it("is CLOSED before the URL is canonicalized", async () => {
    // At "/" with a chat on screen, `replaceRoute` is the one write making the URL agree.
    m.getActiveId.mockReturnValue("c1");
    m.getActive.mockReturnValue({ id: "c1" });

    const { startBoot } = await freshBoot();
    await startBoot({ applyRoute: m.applyRoute });

    expect(m.replaceRoute).toHaveBeenCalledWith({ kind: "chat", id: "c1" });
    expect(m.depths.replaceRoute).toEqual([0]);
  });

  it("is CLOSED before a share is delivered", async () => {
    // A `?agent=planner` launch's push and canonicalization must land outside the window.
    const { startBoot } = await freshBoot();
    await startBoot({ applyRoute: m.applyRoute });

    expect(m.depths.share).toEqual([0]);
  });

  it("leaves the tab-set read OUTSIDE it, and the claim guards the deep link across it", async () => {
    // A window spanning an await silences every shell push; the claim protects the deep link.
    const depthAtListTabs: number[] = [];
    m.listTabs.mockImplementation(() => {
      depthAtListTabs.push(m.suppression.depth);
      return Promise.resolve(true);
    });

    const { startBoot } = await freshBoot();
    await startBoot({ applyRoute: m.applyRoute });

    expect(depthAtListTabs).toEqual([0]);
    // And the restore itself is still covered — narrowing must not open it.
    expect(m.depths.activate).toEqual([1]);
  });
});

// The location claim silences only a push to a DIFFERENT location, which makes the narrow
// window safe.
describe("the location claim", () => {
  it("claims the location the DOCUMENT loaded at, before anything can push", async () => {
    const { startBoot } = await freshBoot();
    await startBoot({ applyRoute: m.applyRoute });

    // `location.pathname + location.hash`, not the parsed route: the claim has to
    // name what the reader asked for, and nothing has parsed it yet.
    expect(m.claimLocation).toHaveBeenCalledWith(location.pathname + location.hash);
  });

  it("still stands while the route is being applied", async () => {
    // A route that names something, so `applyInitialRoute` hands off rather than
    // taking the default-"/" canonicalization branch.
    m.parseRoute.mockReturnValue({ kind: "run", id: "wf_1" });

    const { startBoot } = await freshBoot();
    await startBoot({ applyRoute: m.applyRoute });

    expect(m.claims.applyRoute).toEqual([location.pathname + location.hash]);
  });

  it("outlives an opener that resolves after applyRoute RETURNS", async () => {
    // `/run/{id}` opens through a dynamic import, so the claim is released on the PROMISE.
    // `undefined`, not `void`: the linter forbids `void` as a type argument.
    const opened = deferred<undefined>();
    m.parseRoute.mockReturnValue({ kind: "run", id: "wf_1" });
    m.applyRoute.mockReturnValue(opened.promise);

    const { startBoot } = await freshBoot();
    const booted = startBoot({ applyRoute: m.applyRoute });

    await vi.waitFor(() => {
      expect(m.applyRoute).toHaveBeenCalledTimes(1);
    });
    expect(m.releaseLocation).not.toHaveBeenCalled();

    opened.resolve(undefined);
    await booted;
    expect(m.releaseLocation).toHaveBeenCalledTimes(1);
  });

  it("is released even when the restore throws, so the app can write its own URL", async () => {
    m.listTabs.mockRejectedValue(new Error("boom"));

    const { startBoot } = await freshBoot();
    await startBoot({ applyRoute: m.applyRoute });

    expect(m.releaseLocation).toHaveBeenCalledTimes(1);
  });

  it("is released when a throw lands BEFORE the workspace region exists", async () => {
    // A throw between the claim and its region's `finally` must still release it, or
    // `pushRoute` refuses every other pathname for the page's life.
    m.bootMode.mockReturnValue("reduced");
    m.blur.mockImplementation(() => {
      throw new Error("no composer");
    });

    const { startBoot } = await freshBoot();
    await expect(startBoot({ applyRoute: m.applyRoute })).rejects.toThrow("no composer");

    expect(m.releaseLocation).toHaveBeenCalledTimes(1);
    // The boot is dead on this path, so nothing claims it finished.
    expect(m.markBootDone).not.toHaveBeenCalled();
  });
});

// A resume paints before the network answers: ahead of the chat fold, with no second
// activation.
describe("the local snapshot", () => {
  it("drops the hint when the chat list answers first", async () => {
    // A warm server against a cold IndexedDB open: `paintBootSnapshot` REPLACES the store, so
    // painting after the answer would substitute the hint for it.
    const snapshot = deferred<BootSnapshot | null>();
    m.readBootSnapshot.mockReturnValue(snapshot.promise);

    const { startBoot } = await freshBoot();
    const booted = startBoot({ applyRoute: m.applyRoute });

    // The chat list landed, and the boot moved on rather than waiting on the hint.
    await vi.waitFor(() => {
      expect(m.listTabs).toHaveBeenCalledTimes(1);
    });

    snapshot.resolve(SNAPSHOT);
    await booted;

    // The record arrived, and was never painted.
    expect(m.paintBootSnapshot).toHaveBeenCalledTimes(1);
    expect(m.paintBootSnapshot).toHaveBeenCalledWith(null);
  });

  it("paints the hint when the chat list FAILS to answer", async () => {
    // An unreachable server: `loadList` resolves FALSE early, and counting that as an answer
    // would discard the hint on the one boot with nothing else to show.
    const snapshot = deferred<BootSnapshot | null>();
    m.loadList.mockResolvedValue(false);
    m.readBootSnapshot.mockReturnValue(snapshot.promise);
    m.paintBootSnapshot.mockReturnValue(true);

    const { startBoot } = await freshBoot();
    // A macrotask out, armed AFTER the import (which spans macrotasks), so the failed fetch
    // settles first.
    setTimeout(() => {
      snapshot.resolve(SNAPSHOT);
    }, 0);
    await startBoot({ applyRoute: m.applyRoute });

    expect(m.paintBootSnapshot).toHaveBeenCalledWith(SNAPSHOT);
  });

  it("finishes the workspace when the hint's paint throws", async () => {
    // A throw in the best-effort hint must not skip the authoritative restore (hydration, tab
    // set, route).
    m.paintBootSnapshot.mockImplementation(() => {
      throw new Error("a row would not build");
    });
    const skeleton = document.createElement("div");
    skeleton.id = "tab-strip-skeleton";
    document.body.appendChild(skeleton);

    const { startBoot } = await freshBoot();
    await startBoot({ applyRoute: m.applyRoute });

    expect(m.markHydrated).toHaveBeenCalled();
    expect(m.listTabs).toHaveBeenCalledTimes(1);
    expect(m.activateRestoredTab).toHaveBeenCalledTimes(1);
    expect(document.getElementById("tab-strip-skeleton")).toBeNull();
  });

  it("falls back to the tab set's activation when the resumed one throws", async () => {
    // A resume that failed at its activation did not restore, so the tab set's activation
    // runs.
    m.paintBootSnapshot.mockReturnValue(true);
    m.activateRestoredTab.mockImplementationOnce(() => {
      throw new Error("onShow rejected");
    });

    const { startBoot } = await freshBoot();
    await startBoot({ applyRoute: m.applyRoute });

    expect(m.activateRestoredTab).toHaveBeenCalledTimes(2);
    expect(m.listTabs).toHaveBeenCalledTimes(1);
  });

  it("stops capturing and drops the record when the user logs out", async () => {
    // After a logout the page keeps running; a live capture would write the signed-out
    // workspace for the next boot to paint.
    const { startBoot } = await freshBoot();
    await startBoot({ applyRoute: m.applyRoute });
    expect(m.clearBootSnapshot).not.toHaveBeenCalled();

    const listener = m.subscribeByName.mock.calls.find(([name]) => name === "settings.logout")?.[1];
    expect(listener).toBeDefined();
    listener?.({ status: "success" });

    expect(m.clearBootSnapshot).toHaveBeenCalledTimes(1);
  });

  it("forgets every per-device record on a logout, not only the snapshot", async () => {
    // FOUR RECORDS, THREE OWNERS: the snapshot and three localStorage blobs. Clearing the fold
    // KEY alone is not enough: `persist` rewrites it from the in-memory map.
    const { startBoot } = await freshBoot();
    await startBoot({ applyRoute: m.applyRoute });

    const listener = m.subscribeByName.mock.calls.find(([name]) => name === "settings.logout")?.[1];
    listener?.({ status: "success" });

    expect(m.clearDeviceKeys).toHaveBeenCalledTimes(1);
    expect(m.resetFoldState).toHaveBeenCalledTimes(1);
  });

  it("forgets every per-device record on a signed_out boot too", async () => {
    m.resolveIdentity.mockResolvedValue({ state: "signed_out" });

    const { startBoot } = await freshBoot();
    await startBoot({ applyRoute: m.applyRoute });

    expect(m.clearBootSnapshot).toHaveBeenCalledTimes(1);
    expect(m.clearDeviceKeys).toHaveBeenCalledTimes(1);
    expect(m.resetFoldState).toHaveBeenCalledTimes(1);
  });

  it("restarts the capture on a login in the same page, after a logout stopped it", async () => {
    // The capture's lifetime is the SESSION, not the page: it sits outside `initPostAuth`'s
    // latch, so a login after a logout restarts it.
    const { startBoot, initPostAuth } = await freshBoot();
    await startBoot({ applyRoute: m.applyRoute });
    expect(m.startBootSnapshot).toHaveBeenCalledTimes(1);

    const listener = m.subscribeByName.mock.calls.find(([name]) => name === "settings.logout")?.[1];
    listener?.({ status: "success" });

    // The login door, which is the same door the boot came through.
    initPostAuth();
    expect(m.startBootSnapshot).toHaveBeenCalledTimes(2);
    // And nothing behind the latch runs twice.
    expect(m.initPostAuthUI).toHaveBeenCalledTimes(1);
  });

  it("keeps capturing when a logout FAILS, because the user is still signed in", async () => {
    const { startBoot } = await freshBoot();
    await startBoot({ applyRoute: m.applyRoute });

    const listener = m.subscribeByName.mock.calls.find(([name]) => name === "settings.logout")?.[1];
    listener?.({ status: "error" });

    expect(m.clearBootSnapshot).not.toHaveBeenCalled();
  });

  it("paints the strip and activates it before the chat list answers", async () => {
    const chats = deferred<boolean>();
    m.loadList.mockReturnValue(chats.promise);
    m.paintBootSnapshot.mockReturnValue(true);
    const skeleton = document.createElement("div");
    skeleton.id = "tab-strip-skeleton";
    document.body.appendChild(skeleton);

    const { startBoot } = await freshBoot();
    const booted = startBoot({ applyRoute: m.applyRoute });

    await vi.waitFor(() => {
      expect(m.activateRestoredTab).toHaveBeenCalledTimes(1);
    });
    // The placeholder is gone because real rows replaced it, not because an answer
    // landed — none has.
    expect(document.getElementById("tab-strip-skeleton")).toBeNull();
    expect(m.listTabs).not.toHaveBeenCalled();

    chats.resolve(true);
    await booted;
  });

  it("activates ONCE when it painted, so the transcript is fetched once", async () => {
    m.paintBootSnapshot.mockReturnValue(true);

    const { startBoot } = await freshBoot();
    await startBoot({ applyRoute: m.applyRoute });

    // The tab set still lands and still reconciles; what it must not do is re-run
    // the activation, whose onShow is a second /api/chats/{id}.
    expect(m.listTabs).toHaveBeenCalledTimes(1);
    expect(m.activateRestoredTab).toHaveBeenCalledTimes(1);
  });

  it("activates after the tab set when there was nothing to resume", async () => {
    const { startBoot } = await freshBoot();
    await startBoot({ applyRoute: m.applyRoute });

    expect(m.paintBootSnapshot).toHaveBeenCalledWith(null);
    expect(m.activateRestoredTab).toHaveBeenCalledTimes(1);
  });

  it("mints no starter chat over rows a snapshot painted", async () => {
    // The chat list could not be read, but the screen is not blank: the snapshot's
    // rows are in the store, so there is nothing for a fallback chat to fix.
    m.loadList.mockResolvedValue(false);
    m.paintBootSnapshot.mockReturnValue(true);
    m.getSessions.mockReturnValue([{ id: "c1" }]);

    const { startBoot } = await freshBoot();
    await startBoot({ applyRoute: m.applyRoute });

    expect(m.toastError).toHaveBeenCalledWith(
      "Could not load your chats.",
      expect.objectContaining({ label: "Reload" }),
    );
    expect(m.createSession).not.toHaveBeenCalled();
  });
});

describe("the tab strip's pending state", () => {
  it("drops the authored skeleton once the tab set has been answered", async () => {
    const skeleton = document.createElement("div");
    skeleton.id = "tab-strip-skeleton";
    document.body.appendChild(skeleton);

    const { startBoot } = await freshBoot();
    await startBoot({ applyRoute: m.applyRoute });

    expect(document.getElementById("tab-strip-skeleton")).toBeNull();
  });

  it("drops it even when the tab set cannot be read", async () => {
    const skeleton = document.createElement("div");
    skeleton.id = "tab-strip-skeleton";
    document.body.appendChild(skeleton);
    m.listTabs.mockResolvedValue(false);

    const { startBoot } = await freshBoot();
    await startBoot({ applyRoute: m.applyRoute });

    expect(document.getElementById("tab-strip-skeleton")).toBeNull();
    // A RE-READ rather than a reload: the GET is the only thing that failed, and a
    // reload restarts the whole boot.
    expect(m.toastError).toHaveBeenCalledWith(
      "Could not restore your tabs.",
      expect.objectContaining({ label: "Retry" }),
    );
  });
});

// The tab set's boot read: the boot connection runs no reconcile (sse-adapter.ts), so a
// boot read that never landed is covered by nothing else. Asserted by CALL COUNT.
describe("the tab set is re-read when the boot's own read failed", () => {
  /** The retry the tab-set notice offered. */
  function offeredTabRetry(): (() => void) | undefined {
    const call = m.toastError.mock.calls.find((c) => c[0] === "Could not restore your tabs.");
    return (call?.[1] as { onClick?: () => void } | undefined)?.onClick;
  }

  it("recovers a read that FAILED while the stream was already up", async () => {
    // The connection arrived before the read settled, and no later `connected` comes.
    m.listTabs.mockResolvedValue(false);

    const { startBoot, onTransportStatus } = await freshBoot();
    onTransportStatus("connected");
    await startBoot({ applyRoute: m.applyRoute });

    expect(m.listTabs).toHaveBeenCalledTimes(2);
  });

  it("does not recover a read that SUCCEEDED", async () => {
    const { startBoot, onTransportStatus } = await freshBoot();
    onTransportStatus("connected");
    await startBoot({ applyRoute: m.applyRoute });

    expect(m.listTabs).toHaveBeenCalledTimes(1);
  });

  it("re-reads on the connection that comes up LATER", async () => {
    // An offline boot: the read fails with no stream to retry over, and the link
    // comes up seconds afterwards.
    m.listTabs.mockResolvedValue(false);

    const { startBoot, onTransportStatus } = await freshBoot();
    await startBoot({ applyRoute: m.applyRoute });
    expect(m.listTabs).toHaveBeenCalledTimes(1);

    onTransportStatus("connected");

    expect(m.listTabs).toHaveBeenCalledTimes(2);
  });

  it("stops re-reading on every connection once a read has answered", async () => {
    // Deliberately NARROWER than the chat list's every-connection rule: the tab set
    // has a gap mechanism the chat list lacks, so a healthy reconnect costs no GET.
    m.listTabs.mockResolvedValue(false);

    const { startBoot, onTransportStatus } = await freshBoot();
    await startBoot({ applyRoute: m.applyRoute });
    m.listTabs.mockResolvedValue(true);
    offeredTabRetry()?.();
    await vi.waitFor(() => {
      expect(m.listTabs).toHaveBeenCalledTimes(2);
    });

    onTransportStatus("connected");
    onTransportStatus("disconnected");
    onTransportStatus("connected");

    expect(m.listTabs).toHaveBeenCalledTimes(2);
  });

  it("re-READS on the notice's retry rather than reloading the page", async () => {
    m.listTabs.mockResolvedValue(false);

    const { startBoot } = await freshBoot();
    await startBoot({ applyRoute: m.applyRoute });
    expect(m.listTabs).toHaveBeenCalledTimes(1);

    offeredTabRetry()?.();

    await vi.waitFor(() => {
      expect(m.listTabs).toHaveBeenCalledTimes(2);
    });
  });

  it("says so again when the retry fails too, because the button dismissed the notice", async () => {
    m.listTabs.mockResolvedValue(false);

    const { startBoot } = await freshBoot();
    await startBoot({ applyRoute: m.applyRoute });
    const said = (): number =>
      m.toastError.mock.calls.filter((c) => c[0] === "Could not restore your tabs.").length;
    expect(said()).toBe(1);

    offeredTabRetry()?.();

    await vi.waitFor(() => {
      expect(said()).toBe(2);
    });
  });
});

// The ORIGIN the boot's location is applied under: a restored load names the last tab
// rather than one that still exists, and applying it as a deep link re-opens a tab closed
// elsewhere. `deep-link.ts` decides; the boot states which kind of load this was.
describe("the boot states where its location came from", () => {
  it("applies a restored document's location as a RESTORE", async () => {
    m.navigationOrigin.mockReturnValue("restore");
    m.parseRoute.mockReturnValue({ kind: "chat", id: "c-gone" });

    const { startBoot } = await freshBoot();
    await startBoot({ applyRoute: m.applyRoute });

    expect(m.applyRoute).toHaveBeenCalledExactlyOnceWith({ kind: "chat", id: "c-gone" }, "restore");
  });

  it("applies a deliberate navigation's location as a DEEP LINK", async () => {
    // The control. Without it the case above passes for a boot that simply stopped
    // applying its route.
    m.navigationOrigin.mockReturnValue("deeplink");
    m.parseRoute.mockReturnValue({ kind: "chat", id: "c-gone" });

    const { startBoot } = await freshBoot();
    await startBoot({ applyRoute: m.applyRoute });

    expect(m.applyRoute).toHaveBeenCalledExactlyOnceWith(
      { kind: "chat", id: "c-gone" },
      "deeplink",
    );
  });
});

// The rapid-reload bound (`reload-guard.ts` owns count and threshold): what a reduced boot
// WITHHOLDS, and that a full boot withholds nothing.
describe("a boot inside a reload loop", () => {
  it("paints no transcript, blurs the composer and says why", async () => {
    m.bootMode.mockReturnValue("reduced");
    m.reloadCount.mockReturnValue(4);
    // A snapshot IS there to paint, so a case that skipped it is distinguishable from
    // one that had nothing.
    m.readBootSnapshot.mockResolvedValue(SNAPSHOT);
    m.paintBootSnapshot.mockReturnValue(true);

    const { startBoot } = await freshBoot();
    await startBoot({ applyRoute: m.applyRoute });

    expect(m.paintBootSnapshot).not.toHaveBeenCalled();
    // The authoritative restore is unchanged, which is what keeps reduced mode a
    // lighter boot rather than a broken one.
    expect(m.listTabs).toHaveBeenCalledTimes(1);
    expect(m.activateRestoredTab).toHaveBeenCalledTimes(1);
    expect(m.blur).toHaveBeenCalledTimes(1);
    // Names what the reader saw and what it cost them, and offers the one way out.
    expect(m.showBanner).toHaveBeenCalledWith(
      "*",
      "reload-loop",
      "This page reloaded 4 times in a row, so it started with less loaded.",
      "warning",
      false,
      expect.objectContaining({ label: "Start in full mode" }),
    );
  });

  it("skips the fetch-only fan-outs and keeps the two that report state", async () => {
    m.bootMode.mockReturnValue("reduced");

    const { startBoot } = await freshBoot();
    await startBoot({ applyRoute: m.applyRoute });

    expect(m.fetchCatalog).not.toHaveBeenCalled();
    expect(m.loadVersions).not.toHaveBeenCalled();
    expect(m.initStatusVersions).not.toHaveBeenCalled();
    // Capability, and a degraded runtime: what a reader in this state needs most.
    expect(m.initGovernance).toHaveBeenCalledTimes(1);
    expect(m.initRuntimeHealth).toHaveBeenCalledTimes(1);
    // The live-runs inventory is not fetched: the `connected` handshake states it.
    expect(m.rebuildLiveRuns).not.toHaveBeenCalled();
  });

  it("withholds nothing on an ordinary boot, and arms the stability clear", async () => {
    m.readBootSnapshot.mockResolvedValue(SNAPSHOT);

    const { startBoot } = await freshBoot();
    await startBoot({ applyRoute: m.applyRoute });

    expect(m.paintBootSnapshot).toHaveBeenCalledWith(SNAPSHOT);
    expect(m.blur).not.toHaveBeenCalled();
    expect(m.showBanner).not.toHaveBeenCalled();
    expect(m.fetchCatalog).toHaveBeenCalledTimes(1);
    expect(m.loadVersions).toHaveBeenCalledTimes(1);
    // No boot fetch in any mode: `adoptConnectRuns` reads the handshake, and the gap door
    // is the one place a per-run refetch is wanted.
    expect(m.rebuildLiveRuns).not.toHaveBeenCalled();
    // A page that stays up costs the next boot nothing, and only this call arms it.
    expect(m.noteBootAlive).toHaveBeenCalledTimes(1);
  });
});

describe("the eviction exemption", () => {
  it("keeps the delegate page's alone, so no surface pins a chat's window for a run", async () => {
    const { startBoot } = await freshBoot();
    await startBoot({ applyRoute: m.applyRoute });

    // The SET of eviction exemptions: only the delegate page reads a chat's window.
    expect(m.registerEvictionExemption.mock.calls.flat()).toEqual([m.subagentTabProjectsChat]);
  });

  it("registers on a REDUCED boot too, being a registration rather than a read", async () => {
    m.bootMode.mockReturnValue("reduced");

    const { startBoot } = await freshBoot();
    await startBoot({ applyRoute: m.applyRoute });

    expect(m.registerEvictionExemption).toHaveBeenCalledWith(m.subagentTabProjectsChat);
    expect(m.registerEvictionExemption).toHaveBeenCalledTimes(1);
  });
});

describe("the turn-repair registration", () => {
  it("hands the loader's range read to the store, which detects a hole and never fetches", async () => {
    const { startBoot } = await freshBoot();
    await startBoot({ applyRoute: m.applyRoute });

    // By IDENTITY: the store detects holes and `store-load.ts` reads, so the repair is
    // injected; unregistered, nothing repairs a hole.
    expect(m.registerTurnRepair.mock.calls.flat()).toEqual([m.requestTurnRange]);
  });

  it("hands the revert's repair ABORT to the loader, the other half of that seam", async () => {
    const { startBoot } = await freshBoot();
    await startBoot({ applyRoute: m.applyRoute });

    // Unregistered, a read out during a revert would re-seat the dropped turn.
    expect(m.registerRevertReadAbort.mock.calls.flat()).toEqual([m.abortReadsForRevert]);
  });

  it("hands the run log's range read to the run store, the chat twin's seam", async () => {
    const { startBoot } = await freshBoot();
    await startBoot({ applyRoute: m.applyRoute });

    // The run store's repair, injected likewise; unregistered, a run-log hole is asked of
    // nobody, which no gate can see.
    expect(m.registerRunTurnRepair.mock.calls.flat()).toEqual([m.requestRunTurnRange]);
  });
});

describe("the run-state demands", () => {
  it("registers the run tab AND the chat-row fold, so forgetRun enumerates nobody", async () => {
    const { startBoot } = await freshBoot();
    await startBoot({ applyRoute: m.applyRoute });

    const demands = m.registerRunStateDemand.mock.calls.flat();
    expect(demands).toHaveLength(2);
    // The fold's predicate travels by IDENTITY: `chat-run-dots.ts` owns the reader
    // whose demand it states, the way each eviction exemption above does.
    expect(demands).toContain(m.chatTabFoldsRun);
    // The other is the run TAB's, which asks the tab SET rather than the store.
    const tabDemand = demands.find((fn) => fn !== m.chatTabFoldsRun);
    m.hasTab.mockReturnValue(true);
    expect(tabDemand?.("wf_1")).toBe(true);
    expect(m.hasTab).toHaveBeenCalledWith("run", "wf_1");
  });
});

describe("the shell panel is restored whatever the settings read answers", () => {
  const root = document.documentElement;

  beforeEach(() => {
    root.setAttribute("data-shell-open", "");
    root.style.setProperty("--shell-h", "300px");
  });

  afterEach(() => {
    root.removeAttribute("data-shell-open");
    root.style.removeProperty("--shell-h");
  });

  it.each([
    ["the read answers null", (): void => void m.loadSettings.mockResolvedValue(null)],
    ["the read answers settings", (): void => undefined],
    ["the read rejects", (): void => void m.loadSettings.mockRejectedValue(new Error("down"))],
  ])("restores it and ends the pre-paint state when %s", async (_name, arrange) => {
    arrange();
    const { startBoot } = await freshBoot();
    await startBoot({ applyRoute: m.applyRoute });
    expect(m.restoreShell).toHaveBeenCalledTimes(1);
    expect(root.hasAttribute("data-shell-open")).toBe(false);
    expect(root.style.getPropertyValue("--shell-h")).toBe("");
  });

  it("restores without waiting for the settings read", async () => {
    const settings = deferred<EffectiveSettings>();
    m.loadSettings.mockReturnValue(settings.promise);
    const { startBoot } = await freshBoot();
    const booted = startBoot({ applyRoute: m.applyRoute });
    // A task boundary, so everything already answered (identity included) has landed.
    await new Promise((r) => setTimeout(r, 0));
    expect(m.restoreShell).toHaveBeenCalledTimes(1);
    expect(root.hasAttribute("data-shell-open")).toBe(false);
    settings.resolve(settingsPayload());
    await booted;
    expect(m.restoreShell).toHaveBeenCalledTimes(1);
  });

  it("shuts it at once instead of restoring it on a signed-out boot", async () => {
    m.resolveIdentity.mockResolvedValue({ state: "signed_out" });
    const setStyle = vi.spyOn(m.shellPanel.style, "setProperty");
    const { startBoot } = await freshBoot();
    await startBoot({ applyRoute: m.applyRoute });
    expect(m.restoreShell).not.toHaveBeenCalled();
    expect(root.hasAttribute("data-shell-open")).toBe(false);
    // The panel is shut with its transition off (shell-prepaint-css.test.ts pins
    // what that does to the paint), and left with no inline override behind it.
    expect(setStyle).toHaveBeenCalledWith("transition", "none");
    expect(m.shellPanel.style.getPropertyValue("transition")).toBe("");
  });
});
