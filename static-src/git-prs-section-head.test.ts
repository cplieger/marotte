// A section header keeps its whole PR count when the repo name is longer than a phone column. A geometry property,
// so the section is built by `refreshPRs` and measured under the app's stylesheet.

import { describe, it, expect, vi, beforeEach, afterEach, afterAll } from "vitest";
import type * as ModPRs from "./git-prs-tab.js";

let bootSeq = 0;

const { apiGetTyped, ensureForges } = vi.hoisted(() => ({
  apiGetTyped: vi.fn(),
  ensureForges: vi.fn(),
}));

vi.mock("./api-client.js", async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  apiGetTyped,
  apiPost: vi.fn(),
}));
vi.mock("./forge-store.js", async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  ensureForges,
}));
vi.mock("./bus.js", async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  onSSE: vi.fn(),
  onBus: vi.fn(),
}));
vi.mock("./sse-adapter.js", async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  presentedTag: () => "",
}));
vi.mock("./confirm.js", async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  confirm: vi.fn(async () => true),
}));
vi.mock("./merge-dialog.js", async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  openMergeMethodDialog: vi.fn(async () => "rebase"),
}));
vi.mock("./actions/index.js", async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  registerCleanup: vi.fn(),
  bindLoadingState: vi.fn(() => vi.fn()),
}));
vi.mock("./actions/git-prs.js", async (importOriginal) => {
  const stub = { dispatch: vi.fn(), cancel: vi.fn() };
  return {
    ...(await importOriginal<Record<string, unknown>>()),
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
    watchPRView: stub,
  };
});
vi.mock("./search-popup.js", async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  createSearchPopup: vi.fn(() => ({ open: vi.fn(), close: vi.fn(), toggle: vi.fn() })),
}));
vi.mock("@cplieger/ui-primitives/dialog", async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  createDialog: vi.fn(() => ({ open: vi.fn(), close: vi.fn() })),
}));
vi.mock("./git-scroll.js", async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  preserveGitScroll: (fn: () => void) => {
    fn();
  },
}));

const { mountAppCSS } = await import("./__test-helpers__/css-rules.js");

const forge = {
  id: "github:github.com",
  kind: "github" as const,
  host: "github.com",
  connected: true,
  reconnect_required: false,
};
/** An opaque id: nothing here reads its shape. */
const REPO_ID = "v1.sandbox";

function pr(n: number) {
  return {
    repo_id: REPO_ID,
    repo: "cplieger-bot/forgeapi-sandbox",
    number: n,
    title: `A pull request #${String(n)}`,
    url: `https://github.com/cplieger-bot/forgeapi-sandbox/pull/${String(n)}`,
    state: "open",
    author: "someone",
    source_branch: "feat/x",
    target_branch: "main",
    updated_at: Math.floor(Date.now() / 1000) - 3600,
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

/** The git view's card width on a 390px phone, in the viewport so every rect is real. */
const host = document.createElement("div");
host.style.cssText = "position:fixed;top:0;left:0;inline-size:366px;";
document.body.appendChild(host);

const style = mountAppCSS();

afterAll(() => {
  style.remove();
  host.remove();
  document.documentElement.removeAttribute("data-pointer");
});

beforeEach(async () => {
  vi.useFakeTimers();
  apiGetTyped.mockReset();
  ensureForges.mockReset();
  host.innerHTML = `<div id="git-prs-mount" class="git-multirepo-mount" aria-live="polite"></div>`;
  const { _resetForTest } = await import("./git-prs-state.js");
  _resetForTest();
});

afterEach(() => {
  vi.useRealTimers();
});

async function section(): Promise<HTMLElement> {
  ensureForges.mockImplementation(() => Promise.resolve({ forges: [forge], kinds: ["github"] }));
  const list = {
    entries: [
      {
        forge_id: forge.id,
        state: "ready",
        cycle_id: "1",
        credential: "valid",
        scopes: [{ scope: "owner", owner: "cplieger-bot", rows: [pr(15)] }],
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
  bootSeq += 1;
  const mod = (await import(
    /* @vite-ignore */ `./git-prs-tab.ts?head=${String(bootSeq)}`
  )) as typeof ModPRs;
  await mod.refreshPRs();
  await vi.advanceTimersByTimeAsync(0);
  const found = host.querySelector<HTMLElement>(".git-repo-section");
  expect(found, "the production path has to render the section").not.toBeNull();
  return found!;
}

describe("a repository section's header on the PRs tab", () => {
  it.each(["fine", "coarse"] as const)(
    "keeps the whole count beside a long repository name on a phone's column at %s",
    async (tier) => {
      document.documentElement.dataset["pointer"] = tier;
      const s = await section();
      const toggle = s.querySelector<HTMLElement>(".git-repo-section-header-toggle")!;
      const name = toggle.querySelector<HTMLElement>(".git-repo-section-name")!;
      const meta = toggle.querySelector<HTMLElement>(".git-repo-section-meta")!;

      expect(meta.textContent).toBe("1 open");
      expect(meta.scrollWidth, `${tier}: the count is not cut to an ellipsis`).toBeLessThanOrEqual(
        meta.clientWidth,
      );
      expect(
        name.getBoundingClientRect().right,
        `${tier}: the name ends before the count starts`,
      ).toBeLessThanOrEqual(meta.getBoundingClientRect().left);
      expect(
        meta.getBoundingClientRect().right,
        `${tier}: the count stays inside its toggle`,
      ).toBeLessThanOrEqual(toggle.getBoundingClientRect().right);
    },
  );
});
