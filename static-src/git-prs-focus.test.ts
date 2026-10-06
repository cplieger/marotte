// A notification naming a pull request lands on its row. Its own file because the claims are about real frames and
// `preserveGitScroll`'s own rAF restore, which git-prs-tab.test.ts fakes and stubs.

import { describe, it, expect, vi, beforeEach } from "vitest";
import type * as ModPRs from "./git-prs-tab.js";
import { framesBudgetMs, testTimeoutFor } from "./__test-helpers__/frame-budget.js";
import { prIdentity } from "./push-subject.js";

/**
 * Cache-buster: `vi.resetModules()` does not re-evaluate a module in Browser Mode; each case needs a fresh
 * `pendingFocus` slot.
 */
let bootSeq = 0;

const apiGetTyped = vi.fn();
const ensureForges = vi.fn();

vi.mock("./api-client.js", () => ({ apiGetTyped, apiPost: vi.fn() }));
vi.mock("./forge-store.js", () => ({ ensureForges }));
vi.mock("./bus.js", () => ({
  BUS_RECONCILE: "transport:reconcile",
  onSSE: vi.fn(),
  onBus: vi.fn(),
}));
vi.mock("./sse-adapter.js", () => ({ presentedTag: () => "" }));
vi.mock("./confirm.js", () => ({ confirm: vi.fn(async () => true) }));
vi.mock("./merge-dialog.js", () => ({ openMergeMethodDialog: vi.fn(async () => "rebase") }));
vi.mock("./actions/index.js", () => ({
  registerCleanup: vi.fn(),
  bindLoadingState: vi.fn(() => vi.fn()),
}));
vi.mock("./actions/git-prs.js", () => {
  const stub = { dispatch: vi.fn(), cancel: vi.fn() };
  return {
    mergePR: stub,
    closePR: stub,
    createPR: stub,
    armAutoMerge: stub,
    reopenPR: stub,
    rerunChecks: stub,
    readCapabilities: { dispatch: vi.fn(() => Promise.resolve(null)), cancel: vi.fn() },
    readMergeStatus: { dispatch: vi.fn(() => Promise.resolve(null)), cancel: vi.fn() },
    refreshPRs: stub,
    requestPRCycle: stub,
    sendCloseOnUnload: vi.fn(),
    watchPRView: stub,
  };
});
vi.mock("./search-popup.js", () => ({
  createSearchPopup: vi.fn(() => ({
    open: vi.fn(),
    close: vi.fn(),
    toggle: vi.fn(),
    shell: { setNote: vi.fn() },
  })),
}));
vi.mock("@cplieger/ui-primitives/dialog", () => ({
  createDialog: vi.fn(() => ({ open: vi.fn(), close: vi.fn() })),
}));

const FORGE_ID = "github:github.com";

const REPO_IDS = {
  one: "v1.63706c69656765722f6f6e65",
  two: "v1.63706c69656765722f74776f",
  three: "v1.63706c69656765722f7468726565",
} as const;

interface RepoRow {
  repo_id: string;
  full_name: string;
}

const forge = {
  id: FORGE_ID,
  kind: "github" as const,
  host: "github.com",
  connected: true,
  reconnect_required: false,
};
const repos: RepoRow[] = [
  { repo_id: REPO_IDS.one, full_name: "cplieger/one" },
  { repo_id: REPO_IDS.two, full_name: "cplieger/two" },
];

function pr(number: number, repo: RepoRow): Record<string, unknown> {
  return {
    repo_id: repo.repo_id,
    repo: repo.full_name,
    number,
    title: `change ${String(number)}`,
    state: "open",
    source_branch: "feat",
    target_branch: "main",
    url: `https://example.test/pr/${String(number)}`,
    action: {
      mergeable: "yes",
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
    },
  };
}

/** One PR per repo: #1 in cplieger/one, #2 in every other. */
function routeAPI(rows: readonly RepoRow[] = repos): void {
  ensureForges.mockImplementation(() =>
    Promise.resolve({ forges: [forge], kinds: ["github"] as const }),
  );
  const list = {
    entries: [
      {
        forge_id: FORGE_ID,
        state: "ready",
        cycle_id: "1",
        credential: "valid",
        scopes: [
          {
            scope: "owner",
            owner: "cplieger",
            rows: rows.map((r) => pr(r.repo_id === REPO_IDS.one ? 1 : 2, r)),
          },
        ],
        clones: [],
        fetched_at: 1,
      },
    ],
    subject: [],
    viewing: false,
  };
  apiGetTyped.mockImplementation((_url: string, decode: (v: unknown) => unknown) =>
    Promise.resolve(decode(list)),
  );
}

async function load(): Promise<typeof ModPRs> {
  bootSeq += 1;
  return (await import(
    /* @vite-ignore */ `./git-prs-tab.ts?focus=${String(bootSeq)}`
  )) as typeof ModPRs;
}

/** Keyed on the repository id, as the server mints the subject. */
function identityFor(repo: keyof typeof REPO_IDS, number: number): string {
  return prIdentity(FORGE_ID, REPO_IDS[repo], number);
}

async function frames(n: number): Promise<void> {
  for (let i = 0; i < n; i++) {
    await new Promise((r) => {
      requestAnimationFrame(() => {
        r(null);
      });
    });
  }
}

/** Bounded, and asserts on the way out, so a case whose subject is a poll still states one. */
async function until(check: () => boolean, what: string, max = 30): Promise<void> {
  for (let i = 0; i < max; i++) {
    if (check()) {
      break;
    }
    await frames(1);
  }
  expect(check(), what).toBe(true);
}

/** A smooth scroll moves on the compositor's clock; with the frame-rate limit lifted thirty frames can pass before it moves. */
async function untilMs(check: () => boolean, what: string, ms: number): Promise<void> {
  const deadline = performance.now() + ms;
  while (!check() && performance.now() < deadline) {
    await frames(1);
  }
  expect(check(), what).toBe(true);
}

function mount(): HTMLElement {
  const el = document.getElementById("git-prs-mount");
  if (el === null) {
    throw new Error("mount missing");
  }
  return el;
}

function view(): HTMLElement {
  const el = document.getElementById("git-view");
  if (el === null) {
    throw new Error("view missing");
  }
  return el;
}

function toggleFor(repo: string): HTMLElement | null {
  return mount().querySelector<HTMLElement>(
    `[data-repo="cplieger/${repo}"] .git-repo-section-header-toggle`,
  );
}

function rowFor(identity: string): HTMLElement | null {
  return mount().querySelector<HTMLElement>(`[data-pr="${CSS.escape(identity)}"]`);
}

beforeEach(async () => {
  apiGetTyped.mockReset();
  ensureForges.mockReset();
  const { _resetForTest } = await import("./git-prs-state.js");
  _resetForTest();
  // Tall content above the mount, so a scroll into view has somewhere to go and `preserveGitScroll` a scrollTop to keep.
  document.body.innerHTML = `
    <div id="git-view" style="position:fixed;top:0;left:0;inline-size:600px;block-size:200px;overflow-y:auto">
      <div style="block-size:600px"></div>
      <div id="git-prs-mount" class="git-multirepo-mount" aria-live="polite"></div>
      <div style="block-size:600px"></div>
    </div>`;
});

describe("requestPRFocus", { timeout: testTimeoutFor(framesBudgetMs(30)) }, () => {
  it("finds the row its identity names", async () => {
    routeAPI();
    const { refreshPRs, requestPRFocus } = await load();
    await refreshPRs();

    const identity = identityFor("two", 2);
    // The premise: the attribute carries `prIdentity`'s spelling, the tab's one DOM row identity.
    expect(rowFor(identity)).not.toBeNull();

    requestPRFocus(identity);
    await until(
      () => rowFor(identity)?.classList.contains("deep-link-flash") === true,
      "the named row was marked",
    );
    // One row, and that one.
    expect(mount().querySelectorAll(".deep-link-flash")).toHaveLength(1);
  });

  it("keys the row on the repository id, whatever the listing's display path", async () => {
    // The server names the repository by its canonical id, so a differently spelled path still holds the notified row.
    routeAPI([{ repo_id: REPO_IDS.two, full_name: "CPlieger/Two" }]);
    const { refreshPRs, requestPRFocus } = await load();
    await refreshPRs();

    const identity = identityFor("two", 2);
    requestPRFocus(identity);
    await until(
      () => rowFor(identity)?.classList.contains("deep-link-flash") === true,
      "the identity keyed on the repository id found its row",
    );
    expect(rowFor(prIdentity(FORGE_ID, "CPlieger/Two", 2))).toBeNull();
  });

  it("force-opens a reader-collapsed section and leaves their collapse standing", async () => {
    routeAPI();
    const { refreshPRs, requestPRFocus } = await load();
    await refreshPRs();

    toggleFor("two")?.click();
    expect(toggleFor("two")?.getAttribute("aria-expanded")).toBe("false");

    const identity = identityFor("two", 2);
    requestPRFocus(identity);
    // The force-open reaches the disclosure through the paint, which is why `requestPRFocus` repaints.
    await until(
      () => toggleFor("two")?.getAttribute("aria-expanded") === "true",
      "the section holding the request opened",
    );
    await until(
      () => rowFor(identity)?.classList.contains("deep-link-flash") === true,
      "the row inside it was marked",
    );

    // The request wrote no `readerToggled` entry, so the next paint restores the reader's arrangement.
    await refreshPRs();
    expect(toggleFor("two")?.getAttribute("aria-expanded")).toBe("false");
  });

  it("scrolls a resident, painted tab with no refresh of its own", async () => {
    // A second notification with the tab already painted: nothing else would paint.
    routeAPI();
    const { refreshPRs, requestPRFocus } = await load();
    await refreshPRs();
    ensureForges.mockClear();

    const identity = identityFor("one", 1);
    requestPRFocus(identity);
    await until(
      () => rowFor(identity)?.classList.contains("deep-link-flash") === true,
      "the resident row was marked",
    );
    expect(ensureForges).not.toHaveBeenCalled();
  });

  it("heals once on an unknown identity and then gives up silently", async () => {
    routeAPI();
    const { refreshPRs, requestPRFocus } = await load();
    await refreshPRs();
    ensureForges.mockClear();

    requestPRFocus(identityFor("three", 9));
    await until(() => ensureForges.mock.calls.length === 1, "one healing refresh went out");
    await frames(10);

    // One refresh per request, and the reader is left on the PRs tab with no selection, never a wrong row.
    expect(ensureForges).toHaveBeenCalledTimes(1);
    expect(mount().querySelectorAll(".deep-link-flash")).toHaveLength(0);
    expect(mount().querySelector(".git-multirepo-error")).toBeNull();
  });

  it("survives preserveGitScroll's own scroll restore", async () => {
    // `preserveGitScroll` restores scrollTop in its own rAF. The focus attempt registers after that call returns, so the
    // restore runs first; registered before, the restore would undo the scroll a frame later.
    routeAPI();
    const { refreshPRs, requestPRFocus } = await load();
    await refreshPRs();
    view().scrollTop = 0;

    requestPRFocus(identityFor("two", 2));
    await untilMs(() => view().scrollTop > 0, "the scroll into view survived the restore", 2_000);
  });
});
