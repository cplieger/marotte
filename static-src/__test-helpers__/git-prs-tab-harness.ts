// The PR tab suites' shared double. Each suite's `vi.mock` factories spread `mocks`
// over the real modules, so every suite drives the one state object here.

import { vi } from "vitest";
import type * as ModPRs from "../git-prs-tab.js";
import type * as ModActions from "../actions/git-prs.js";
import type { InventoryEntry } from "../wire/types.gen.js";

export const H = {
  apiGet: vi.fn(),
  apiGetTyped: vi.fn(),
  apiPost: vi.fn(),
  ensureForges: vi.fn(),
  /** Every handler the tab registered on the bus, by event name. */
  sse: new Map<string, (chatID: string, payload: unknown) => void>(),
  bus: new Map<string, (payload: unknown) => void>(),
};

/** The two callbacks the tab hands to its filter popup. */
interface FilterSeam {
  note?: boolean;
  query: (q: string, ctx: unknown) => unknown;
  render: (result: unknown, q: string) => void;
}

let filterSeam: FilterSeam | null = null;
/** Every note the tab wrote; the last one is what the box shows. */
export const notes: string[] = [];

/** Type into the filter box, exactly as the popup does: `query` records the
 *  text, `render` repaints. The popup trims a filter before this seam, so a test
 *  passes what a reader's keystrokes produce AFTER that rule. */
export function applyFilter(q: string): void {
  if (filterSeam === null) {
    throw new Error("filter seam not captured: the module never built its popup");
  }
  const result = filterSeam.query(q, {});
  filterSeam.render(result, q);
}

/** What each suite's `vi.mock` factory lays over the real module. */
export const mocks = {
  apiClient: () => ({ apiGet: H.apiGet, apiGetTyped: H.apiGetTyped, apiPost: H.apiPost }),
  // The tab reads the forge list through the shared store, for each connection's
  // kind and host.
  forgeStore: () => ({ ensureForges: H.ensureForges }),
  bus: () => ({
    BUS_RECONCILE: "transport:reconcile",
    onSSE: (type: string, fn: (chatID: string, payload: unknown) => void) => {
      H.sse.set(type, fn);
      return () => undefined;
    },
    onBus: (type: string, fn: (payload: unknown) => void) => {
      H.bus.set(type, fn);
      return () => undefined;
    },
  }),
  sseAdapter: () => ({ presentedTag: () => "profile-tag" }),
  confirm: () => ({ confirm: vi.fn(async () => true) }),
  mergeDialog: () => ({ openMergeMethodDialog: vi.fn(async () => "rebase") }),
  actionsIndex: () => ({
    registerCleanup: vi.fn(),
    bindLoadingState: vi.fn(() => vi.fn()),
  }),
  // Each PR mutation has its own stub so a case can read what one of them was asked.
  gitPRs: () => {
    const stub = (): { dispatch: ReturnType<typeof vi.fn>; cancel: ReturnType<typeof vi.fn> } => ({
      dispatch: vi.fn(),
      cancel: vi.fn(),
    });
    return {
      mergePR: stub(),
      closePR: stub(),
      createPR: stub(),
      armAutoMerge: stub(),
      reopenPR: stub(),
      rerunChecks: stub(),
      readCapabilities: stub(),
      readMergeStatus: stub(),
      refreshPRs: stub(),
      requestPRCycle: stub(),
      watchPRView: stub(),
      sendCloseOnUnload: vi.fn(),
    };
  },
  searchPopup: () => ({
    createSearchPopup: vi.fn((spec: unknown) => {
      // The filter is module state written by the popup's `query` callback and
      // published by its `render` callback. Capturing the spec lets a test drive
      // that exact seam instead of reaching into the module.
      filterSeam = spec as FilterSeam;
      return {
        open: vi.fn(),
        close: vi.fn(),
        toggle: vi.fn(),
        shell: {
          setNote: (text: string) => {
            notes.push(text);
          },
        },
      };
    }),
  }),
  dialog: () => ({ createDialog: vi.fn(() => ({ open: vi.fn(), close: vi.fn() })) }),
  // The scroll preserver is a pass-through here; its own behaviour is not the
  // subject and it reads layout these suites do not stage.
  gitScroll: () => ({
    preserveGitScroll: (fn: () => void) => {
      fn();
    },
  }),
};

export const githubForge = {
  id: "github:github.com",
  kind: "github" as const,
  host: "github.com",
  connected: true,
  reconnect_required: false,
};

export const giteaForge = {
  id: "gitea:gitea.example",
  kind: "gitea" as const,
  host: "gitea.example",
  connected: true,
  reconnect_required: false,
};

export const gitlabForge = {
  id: "gitlab:gitlab.com",
  kind: "gitlab" as const,
  host: "gitlab.com",
  username: "cplieger",
  connected: true,
  reconnect_required: false,
};

export type Forge = typeof githubForge | typeof giteaForge | typeof gitlabForge;

export const repos = [
  { repo_id: "v1.63706c69656765722f6f6e65", full_name: "cplieger/one" },
  { repo_id: "v1.63706c69656765722f74776f", full_name: "cplieger/two" },
  { repo_id: "v1.63706c69656765722f7468726565", full_name: "cplieger/three" },
  // Outside the account's own repositories: a contribution elsewhere.
  { repo_id: "v1.6f746865722f6c6962", full_name: "other/lib" },
];

/** The server's action object: nothing blocks, no CI verdict. */
function action(over: Record<string, unknown> = {}): Record<string, unknown> {
  return {
    mergeable: "unknown",
    checks: "unknown",
    checks_passing: 0,
    checks_failing: 0,
    checks_pending: 0,
    checks_neutral: 0,
    checks_unknown: 0,
    checks_total: 0,
    auto_merge_armed: "no",
    queue_state: "none",
    queue_position: -1,
    merge_blocked: "none",
    ...over,
  };
}

/** One open pull request in `repos[repo]`. */
export function pr(
  number: number,
  repo = 0,
  over: Record<string, unknown> = {},
  act: Record<string, unknown> = {},
): Record<string, unknown> {
  return {
    repo_id: repos[repo]?.repo_id,
    repo: repos[repo]?.full_name,
    number,
    title: "a change",
    state: "open",
    source_branch: "feat",
    target_branch: "main",
    url: `https://example.test/pr/${String(number)}`,
    head_sha: "abc1234",
    ...over,
    action: action(act),
  };
}

/** A ready entry of `forge` listing `rows` in its owner scope (the authored scope
 *  on GitLab, whose owner scope takes a group only). */
export function entry(
  forge: Forge,
  cycle: string,
  rows: Record<string, unknown>[],
  over: Record<string, unknown> = {},
): Record<string, unknown> {
  const scopes =
    forge.kind === "gitlab"
      ? [{ scope: "authored", rows }]
      : [{ scope: "owner", owner: "cplieger", rows }];
  return {
    forge_id: forge.id,
    state: "ready",
    cycle_id: cycle,
    credential: "valid",
    scopes,
    clones: [],
    fetched_at: Date.now(),
    ...over,
  };
}

/** The inventory read's answer over `entries`, each with its stamp. */
export function inventory(...entries: Record<string, unknown>[]): Record<string, unknown> {
  return {
    entries,
    subject: entries.map((e) => ({
      kind: "forge_inventory",
      ref: e["forge_id"],
      version: e["cycle_id"],
      epoch: "ep",
    })),
    viewing: false,
  };
}

/** What the next inventory read answers. A function answers a deferred read. */
export const inventoryAnswer: { next: () => Promise<unknown> } = {
  next: () => Promise.resolve(inventory()),
};

/** Serve the forge list and the inventory. */
export function routeAPI(opts: { forgesNull?: boolean; forges?: readonly Forge[] } = {}): void {
  const forges = opts.forges ?? [githubForge];
  H.ensureForges.mockImplementation(() =>
    Promise.resolve(opts.forgesNull === true ? null : { forges, kinds: forges.map((f) => f.kind) }),
  );
  H.apiGetTyped.mockImplementation((url: string, decode: (v: unknown) => unknown) => {
    if (url !== "/api/forges/inventory") {
      return Promise.resolve(null);
    }
    return inventoryAnswer.next().then((v) => (v === null ? null : decode(v)));
  });
}

/** Answer the inventory read with these entries from now on. */
export function serve(...entries: Record<string, unknown>[]): void {
  inventoryAnswer.next = () => Promise.resolve(inventory(...entries));
}

/** The default single-forge answer: one entry of `forge` over these rows. */
export function serveRows(
  rows: Record<string, unknown>[],
  forge: Forge = githubForge,
  cycle = "1",
): void {
  serve(entry(forge, cycle, rows));
}

/** Deliver one forge_inventory frame, as the stream does. */
export function frame(e: Record<string, unknown>): void {
  const fn = H.sse.get("forge_inventory");
  if (fn === undefined) {
    throw new Error("the tab registered no forge_inventory handler");
  }
  fn("", { entry: e as unknown as InventoryEntry });
}

// Cache-buster for `load`: `vi.resetModules()` does not re-evaluate a module in
// Browser Mode (the module map is URL-keyed), and these suites need fresh module
// state (`refreshGen` and the abort controller are module-level). Only the module
// under test is busted, so its dependencies stay interceptable by `vi.mock`.
let bootSeq = 0;

export async function load(): Promise<typeof ModPRs> {
  bootSeq += 1;
  return (await import(
    /* @vite-ignore */ `../git-prs-tab.ts?boot=${String(bootSeq)}`
  )) as typeof ModPRs;
}

export function mount(): HTMLElement {
  const el = document.getElementById("git-prs-mount");
  if (el === null) {
    throw new Error("mount missing");
  }
  return el;
}

/** Every URL the tab read or asked for, through either GET helper. */
export function requestedURLs(): string[] {
  return [...H.apiGet.mock.calls, ...H.apiGetTyped.mock.calls].map((c: unknown[]) => String(c[0]));
}

export async function actions(): Promise<typeof ModActions> {
  return import("../actions/git-prs.js");
}

/** A dispatch handle settling to `outcome`, the shape the action framework returns. */
export function handle(outcome: Promise<Record<string, unknown>>): unknown {
  const value = outcome.then((o) => (o["status"] === "success" ? o["value"] : null));
  return Object.assign(value, { outcome, abort: vi.fn() });
}

/** A handle that succeeds with `value`. */
export function succeeds(value: unknown): unknown {
  return handle(Promise.resolve({ status: "success", value }));
}

/** A handle the server refuses with `code` and `message`. */
export function refuses(code: string, message: string): unknown {
  return handle(Promise.resolve({ status: "error", error: { message, code, status: 501 } }));
}

/** The capability route's answer: `rerun_checks` reads `support`. */
export function capabilities(support: string, detail = ""): unknown {
  return {
    connection: { rerun_checks: { support, source: "version", detail } },
    grant: {},
  };
}

/** Each suite's `beforeEach`: fake timers, empty doubles and a fresh mount. */
export async function resetPRsTab(): Promise<void> {
  vi.useFakeTimers();
  H.apiGet.mockReset();
  H.apiGetTyped.mockReset();
  H.apiPost.mockReset();
  H.apiPost.mockResolvedValue(null);
  H.ensureForges.mockReset();
  H.sse.clear();
  H.bus.clear();
  inventoryAnswer.next = () => Promise.resolve(inventory());
  filterSeam = null;
  notes.length = 0;
  document.body.innerHTML = `<div id="git-prs-mount" class="git-multirepo-mount" aria-live="polite"></div>`;
  const a = await actions();
  for (const name of [
    "mergePR",
    "closePR",
    "createPR",
    "armAutoMerge",
    "reopenPR",
    "rerunChecks",
    "refreshPRs",
    "requestPRCycle",
    "watchPRView",
    "readCapabilities",
    "readMergeStatus",
  ] as const) {
    vi.mocked(a[name].dispatch).mockReset();
  }
  // Every row action answers, so a press that a case does not drive settles.
  const merged = {
    outcome: { state: "merged", queue_state: "none", queue_position: -1 },
    cycle_id: "2",
  };
  for (const dispatch of [a.mergePR.dispatch, a.armAutoMerge.dispatch]) {
    vi.mocked(dispatch).mockImplementation(() => succeeds(merged) as never);
  }
  for (const dispatch of [a.rerunChecks.dispatch, a.reopenPR.dispatch]) {
    vi.mocked(dispatch).mockImplementation(() => succeeds(undefined) as never);
  }
  vi.mocked(a.closePR.dispatch).mockImplementation(
    () => succeeds({ pr: {}, cycle_id: "2" }) as never,
  );
  vi.mocked(a.readCapabilities.dispatch).mockImplementation(
    () => succeeds(capabilities("yes")) as never,
  );
  vi.mocked(a.readMergeStatus.dispatch).mockImplementation(
    () => succeeds({ merged: "no", queue_state: "none" }) as never,
  );
  const { _resetForTest } = await import("../git-prs-state.js");
  _resetForTest();
}

export function restorePRsTab(): void {
  vi.useRealTimers();
  vi.restoreAllMocks();
}
