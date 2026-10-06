// A PR title link keeps its hit target on `--hit-floor` while its painted box stays a line box; a real hit test.

import { describe, it, expect, vi, beforeEach, afterEach, afterAll } from "vitest";
import type * as ModPRs from "./git-prs-tab.js";

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
  createSearchPopup: vi.fn(() => ({ open: vi.fn(), close: vi.fn(), toggle: vi.fn() })),
}));
vi.mock("@cplieger/ui-primitives/dialog", () => ({
  createDialog: vi.fn(() => ({ open: vi.fn(), close: vi.fn() })),
}));
vi.mock("./git-scroll.js", () => ({
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
const REPO_ID = "v1.63706c69656765722f6f6e65";

/** Long enough to ellipsise at every width here, so the clip does real work. */
const LONG_TITLE =
  "A pull request title long enough that it has to ellipsise against the action column at " +
  "every width this suite runs at, which takes rather more words than one line of prose";

function pr(n: number) {
  return {
    repo_id: REPO_ID,
    repo: "cplieger/one",
    number: n,
    title: `${LONG_TITLE} #${n}`,
    url: `https://github.com/cplieger/one/pull/${n}`,
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

/** In the viewport, so every rect is real. */
const host = document.createElement("div");
host.style.cssText = "position:fixed;top:0;left:0;inline-size:900px;";
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

/** Two rows: overhanging into the neighbour is the failure one row cannot show. */
async function rows(): Promise<HTMLElement[]> {
  ensureForges.mockImplementation(() => Promise.resolve({ forges: [forge], kinds: ["github"] }));
  const list = {
    entries: [
      {
        forge_id: forge.id,
        state: "ready",
        cycle_id: "1",
        credential: "valid",
        scopes: [{ scope: "owner", owner: "cplieger", rows: [pr(101), pr(102)] }],
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
    /* @vite-ignore */ `./git-prs-tab.ts?target=${String(bootSeq)}`
  )) as typeof ModPRs;
  await mod.refreshPRs();
  await vi.advanceTimersByTimeAsync(0);
  const found = [...host.querySelectorAll<HTMLElement>(".git-pr-row")];
  expect(
    found,
    "the production path has to render two rows, or nothing here is measured",
  ).toHaveLength(2);
  return found;
}

/** A custom property reads back as its raw token, so the engine resolves it on a real box. */
function hitFloorPx(): number {
  const probe = document.createElement("div");
  probe.style.blockSize = "var(--hit-floor)";
  host.appendChild(probe);
  const v = probe.getBoundingClientRect().height;
  probe.remove();
  return v;
}

function titleOf(row: HTMLElement): HTMLAnchorElement {
  const a = row.querySelector<HTMLAnchorElement>("a.git-pr-row-title");
  expect(a, "a PR carrying a url renders its title as the row's one link").not.toBeNull();
  return a!;
}

describe("a PR row's title link", () => {
  it.each(["fine", "coarse"] as const)("keeps its TARGET on the hit floor at %s", async (tier) => {
    document.documentElement.dataset["pointer"] = tier;
    const [first, second] = await rows();
    const link = titleOf(first!);
    const box = link.getBoundingClientRect();
    const floor = hitFloorPx();

    // The expander is centred, reaching half the shortfall each way.
    const reach = (floor - box.height) / 2;
    expect(reach, "the link paints under the floor, or there is nothing to test").toBeGreaterThan(
      1,
    );

    const cx = box.left + box.width / 2;
    expect(
      document.elementFromPoint(cx, box.top - reach + 1),
      "a point just inside the target's top edge activates the link",
    ).toBe(link);
    expect(
      document.elementFromPoint(cx, box.bottom + reach - 1),
      "and one just inside its bottom edge",
    ).toBe(link);
    // The control: without it an expander of any size passes, including one overhanging the next row's title.
    expect(
      document.elementFromPoint(cx, box.bottom + reach + 2),
      "and the target stops there: it may not reach past the floor",
    ).not.toBe(link);

    // An expander is admissible only while it steals nothing from the neighbour.
    const nextLink = titleOf(second!);
    const nb = nextLink.getBoundingClientRect();
    expect(
      document.elementFromPoint(nb.left + nb.width / 2, nb.top + nb.height / 2),
      `${tier}: the next row's title is still its own target`,
    ).toBe(nextLink);
    for (const [i, row] of [first, second].entries()) {
      const merge = row!.querySelector<HTMLButtonElement>(".git-pr-row-actions .btn-small");
      const mb = merge!.getBoundingClientRect();
      expect(
        document.elementFromPoint(mb.left + mb.width / 2, mb.top + mb.height / 2),
        `${tier}: row ${String(i)}'s action cluster is untouched`,
      ).toBe(merge);
    }
  });

  it.each(["fine", "coarse"] as const)(
    "grows its target without growing the row, and clips on the TEXT span, at %s",
    async (tier) => {
      document.documentElement.dataset["pointer"] = tier;
      const [first] = await rows();
      const link = titleOf(first!);
      const text = link.querySelector<HTMLElement>(".git-pr-row-text");
      expect(text, "the text needs its own span for the clip to live on").not.toBeNull();

      // A `min-height` on the anchor would satisfy the floor too, but grows the row.
      expect(
        link.getBoundingClientRect().height,
        `${tier}: the paint stays a line box; only the target takes the floor`,
      ).toBeLessThan(hitFloorPx());

      // The clip is the span's: an `overflow` on the anchor clips the expander away, invisibly to a cascade read.
      expect(getComputedStyle(text!).overflow, `${tier}: the span clips`).toBe("hidden");
      expect(getComputedStyle(link).overflow, `${tier}: the anchor does not`).toBe("visible");
      expect(
        text!.scrollWidth,
        `${tier}: the fixture's title has to overflow, or the clip is inert here`,
      ).toBeGreaterThan(text!.clientWidth + 1);
    },
  );
});
