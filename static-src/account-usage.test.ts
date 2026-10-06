// account-usage.ts renders account usage into the status-popup footer; api-client and the
// generated decoder are mocked so the payload is controlled.

import { vi, describe, it, expect, beforeEach } from "vitest";
import type { AccountUsage } from "./types.js";

const mockApiGetTyped = vi.fn();
vi.mock("./api-client.js", () => ({
  apiGetTyped: (...args: unknown[]) => mockApiGetTyped(...args),
  // Present-but-inert so real-ESM linking succeeds; no case calls them.
  apiGet: vi.fn(),
}));
vi.mock("./wire/decoders.gen.js", () => ({ decodeAccountUsage: vi.fn() }));

import { $ } from "./dom.js";
import { mountAppCSS } from "./__test-helpers__/css-rules.js";
const { loadAccountUsage } = await import("./account-usage.js");

/** Flush the fetch().then(render).finally() microtask chain. */
async function flush(): Promise<void> {
  await Promise.resolve();
  await Promise.resolve();
}

/**
 * The card's account row as `static/index.html` authors it: `#st-account` is the `<a>`
 * itself, with the plan and meter in `.pill-account-lines`. `data-tooltip` sits on the
 * METER, so the link inherits no tooltip.
 */
function seedDom(): void {
  document.body.innerHTML = `
    <a id="st-account" class="pill-account" hidden
       href="https://app.kiro.dev/account/usage" target="_blank" rel="noopener">
      <span class="pill-account-lines">
        <span class="pill-account-plan" id="acct-plan"></span>
        <span class="pill-account-meter" id="acct-meter"></span>
        <span id="acct-overage"></span>
      </span>
    </a>`;
}

beforeEach(() => {
  vi.clearAllMocks();
  seedDom();
});

describe("loadAccountUsage", () => {
  it("renders plan name + credit meter with a percentage", async () => {
    const usage: AccountUsage = {
      plan_name: "KIRO POWER",
      billing_cycle_reset: "2026-08-01",
      breakdowns: [
        {
          resource_type: "CREDIT",
          display_name: "Credits",
          used: 133705,
          limit: 10000,
          percentage: 1337,
          currency: "USD",
          has_limit: true,
        },
      ],
      overages_enabled: true,
    };
    mockApiGetTyped.mockResolvedValue(usage);
    loadAccountUsage(true);
    await flush();

    expect($.stAccount.hidden).toBe(false);
    expect($.acctPlan.textContent).toBe("KIRO POWER");
    expect($.acctMeter.textContent).toContain("(1337%)");
    expect($.acctMeter.textContent).toContain("cr");
    // The reset date's only home is the tooltip, republished as aria-describedby.
    expect($.acctMeter.dataset["tooltip"]).toContain("2026-08-01");
  });

  it("shows 'Usage unavailable' when the fetch fails", async () => {
    mockApiGetTyped.mockResolvedValue(null);
    loadAccountUsage(true);
    await flush();
    expect($.acctPlan.textContent).toBe("Usage unavailable");
    expect($.acctMeter.textContent).toBe("");
  });

  it("marks a cached (stale) snapshot", async () => {
    mockApiGetTyped.mockResolvedValue({
      plan_name: "KIRO POWER",
      stale: true,
      breakdowns: [],
      overages_enabled: false,
    } satisfies AccountUsage);
    loadAccountUsage(true);
    await flush();
    expect($.acctPlan.textContent).toBe("KIRO POWER (cached)");
  });

  it("renders the note for an admin-managed plan with no breakdowns", async () => {
    mockApiGetTyped.mockResolvedValue({
      note: "Your plan is managed by admin",
      breakdowns: [],
      overages_enabled: false,
    } satisfies AccountUsage);
    loadAccountUsage(true);
    await flush();
    expect($.acctPlan.textContent).toBe("Your plan is managed by admin");
    expect($.acctMeter.textContent).toBe("");
  });

  // Overage state is read-only, so the row states it and links to where it changes. Both
  // values are asserted: `overages_enabled` has no `omitempty`, so false is a statement.
  it("reports that overages are on", async () => {
    mockApiGetTyped.mockResolvedValue({
      plan_name: "KIRO POWER",
      breakdowns: [],
      overages_enabled: true,
    } satisfies AccountUsage);
    loadAccountUsage(true);
    await flush();
    expect($.acctOverage.textContent).toBe("Overages on");
  });

  it("reports that overages are off", async () => {
    mockApiGetTyped.mockResolvedValue({
      plan_name: "KIRO POWER",
      breakdowns: [],
      overages_enabled: false,
    } satisfies AccountUsage);
    loadAccountUsage(true);
    await flush();
    expect($.acctOverage.textContent).toBe("Overages off");
  });

  // Sequenced: the row is never re-hidden, so a failure must CLEAR an earlier render's state
  // (a fresh element starts empty and would pass anyway).
  it("clears the line when a later fetch fails, rather than leaving a stale state", async () => {
    mockApiGetTyped.mockResolvedValue({
      plan_name: "KIRO POWER",
      breakdowns: [],
      overages_enabled: true,
    } satisfies AccountUsage);
    loadAccountUsage(true);
    await flush();
    expect($.acctOverage.textContent).toBe("Overages on");

    mockApiGetTyped.mockResolvedValue(null);
    loadAccountUsage(true);
    await flush();
    expect($.acctOverage.textContent).toBe("");
  });

  it("throttles repeat calls within the client TTL (force bypasses)", async () => {
    mockApiGetTyped.mockResolvedValue({
      plan_name: "P",
      breakdowns: [],
      overages_enabled: false,
    } satisfies AccountUsage);
    loadAccountUsage(true);
    await flush();
    expect(mockApiGetTyped).toHaveBeenCalledTimes(1);
    loadAccountUsage(); // not forced, within TTL → skipped
    await flush();
    expect(mockApiGetTyped).toHaveBeenCalledTimes(1);
  });
});

// An unrendered row generates no box: `.pill-account`'s author `display: flex` beats the
// UA `[hidden]` rule, so `&[hidden] { display: none }` is required. A real cascade, since
// only the assembled sheet answers which `display` wins.
describe("the row's hidden state", () => {
  it("generates no box while it is hidden", async () => {
    const style = mountAppCSS();
    try {
      // Inside a card, because that is where the row lives and where its
      // `align-self: stretch` and negative margins mean anything.
      const card = document.createElement("span");
      card.className = "pill-expand-content pill-status-content";
      card.append(...Array.from(document.body.children));
      document.body.replaceChildren(card);

      const row = $.stAccount;
      expect(row.hidden, "the fixture ships hidden, like index.html").toBe(true);
      expect(getComputedStyle(row).display, "the attribute has to beat the author rule").toBe(
        "none",
      );
      expect(row.getBoundingClientRect().height, "no box at all").toBe(0);

      // And the other direction, so the case cannot pass by the row never rendering:
      // once `account-usage.ts` reveals it, it IS a flex row.
      mockApiGetTyped.mockResolvedValue({
        plan_name: "P",
        breakdowns: [],
        overages_enabled: false,
      } satisfies AccountUsage);
      loadAccountUsage(true);
      await flush();
      expect(row.hidden).toBe(false);
      expect(getComputedStyle(row).display).toBe("flex");
      expect(row.getBoundingClientRect().height).toBeGreaterThan(0);
    } finally {
      style.remove();
    }
  });
});
