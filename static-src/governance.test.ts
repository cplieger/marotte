import { beforeEach, describe, expect, it, vi } from "vitest";
import type { GovernanceStatePayload } from "./types.js";
import type * as ModGovernance from "./governance.js";

/**
 * Cache-buster: `vi.resetModules()` does not re-evaluate a module in Browser Mode (URL-keyed module map). The `.ts`
 * extension is load-bearing: written `.js`, coverage attributes every evaluation to a file that does not exist. Only
 * the module under test is busted, so `vi.mock` still intercepts its dependencies.
 */
let bootSeq = 0;

// State is driven through the governance_state SSE here, so the snapshot fetch is a harmless null.
vi.mock("./api-client.js", () => ({
  apiGetTyped: vi.fn(() => Promise.resolve(null)),
  apiGet: vi.fn(() => Promise.resolve(null)),
}));

function govState(over: Partial<GovernanceStatePayload> = {}): GovernanceStatePayload {
  return {
    known: true,
    is_enterprise: false,
    features: {
      mcp_enabled: true,
      web_tools_enabled: true,
      usage_analytics: false,
      content_collection: true,
      prompt_logging: false,
      code_reference_tracker: false,
      autonomous_agents: true,
    },
    ...over,
  };
}

// A fresh module graph per test so `current`, `listeners` and the bus do not leak. The return type is inferred to
// avoid inline import() types (consistent-type-imports).
async function load() {
  vi.resetModules();
  bootSeq++;
  const gov = (await import(
    /* @vite-ignore */ `./governance.ts?boot=${bootSeq}`
  )) as typeof ModGovernance;
  const bus = await import("./bus.js");
  return { gov, bus };
}

beforeEach(() => {
  document.body.innerHTML =
    '<div class="page-section" id="general-governance-section" hidden></div>';
});

describe("governance state", () => {
  it("featureDisabled is permissive until the policy is known", async () => {
    const { gov } = await load();
    expect(gov.currentGovernance()).toBeNull();
    // Unknown governance never reports a feature as disabled.
    expect(gov.featureDisabled("mcp_enabled")).toBe(false);
    expect(gov.featureDisabled("code_reference_tracker")).toBe(false);
  });

  it("featureDisabled reflects a known policy", async () => {
    const { gov, bus } = await load();
    gov.initGovernance();
    bus.dispatch({
      type: "governance_state",
      chat_id: "",
      payload: govState({ features: { ...govState().features, mcp_enabled: false } }),
    });

    expect(gov.currentGovernance()?.known).toBe(true);
    expect(gov.featureDisabled("mcp_enabled")).toBe(true);
    expect(gov.featureDisabled("web_tools_enabled")).toBe(false);
    expect(gov.featureDisabled("code_reference_tracker")).toBe(true);
  });

  it("renders the read-only Organization-policy disclosure for an enterprise account", async () => {
    const { gov, bus } = await load();
    gov.initGovernance();
    bus.dispatch({
      type: "governance_state",
      chat_id: "",
      payload: govState({ is_enterprise: true }),
    });

    const section = document.getElementById("general-governance-section");
    expect(section).not.toBeNull();
    expect(section?.hidden).toBe(false);
    const text = section?.textContent ?? "";
    expect(text).toContain("Organization policy");
    expect(text).toContain("Prompt logging");
    expect(text).toContain("Usage analytics");
    expect(text).toContain("Content collection");
    const offCells = section?.querySelectorAll("dd.governance-off");
    expect(offCells?.length ?? 0).toBeGreaterThan(0);
  });

  it("keeps the disclosure hidden and empty on a personal account", async () => {
    const { gov, bus } = await load();
    gov.initGovernance();
    bus.dispatch({
      type: "governance_state",
      chat_id: "",
      payload: govState({ is_enterprise: true }),
    });
    bus.dispatch({
      type: "governance_state",
      chat_id: "",
      payload: govState({ is_enterprise: false }),
    });
    const section = document.getElementById("general-governance-section");
    expect(section?.hidden).toBe(true);
    expect(section?.childElementCount).toBe(0);
  });

  it.each(["api_failure", "no_endpoint"])(
    "says the settings could not be loaded for %s",
    async (token) => {
      const { gov, bus } = await load();
      gov.initGovernance();
      bus.dispatch({
        type: "governance_state",
        chat_id: "",
        payload: govState({ is_enterprise: true, disabled_reason: token }),
      });
      const text = document.getElementById("general-governance-section")?.textContent ?? "";
      expect(text).toContain("Couldn't load your organization's settings.");
      expect(text).not.toContain(token);
    },
  );

  it("adds no reason line for an administrator's decision and never shows the token", async () => {
    const { gov, bus } = await load();
    gov.initGovernance();
    bus.dispatch({
      type: "governance_state",
      chat_id: "",
      payload: govState({ is_enterprise: true, disabled_reason: "admin_disabled" }),
    });
    const section = document.getElementById("general-governance-section");
    expect(section?.textContent).not.toContain("admin_disabled");
    expect(section?.querySelector(".governance-reason")).toBeNull();
  });

  it("keeps the disclosure hidden while the policy is unknown", async () => {
    const { gov, bus } = await load();
    gov.initGovernance();
    bus.dispatch({ type: "governance_state", chat_id: "", payload: govState({ known: false }) });
    const section = document.getElementById("general-governance-section");
    expect(section?.hidden).toBe(true);
  });

  it("shows the administrator note only while the machine carries admin rules", async () => {
    document.body.insertAdjacentHTML(
      "beforeend",
      '<p id="security-profile-admin" class="hidden"></p>',
    );
    const { gov, bus } = await load();
    gov.initGovernance();
    const note = document.getElementById("security-profile-admin");
    bus.dispatch({
      type: "governance_state",
      chat_id: "",
      payload: govState({ admin_restricted: true }),
    });
    expect(note?.classList.contains("hidden")).toBe(false);
    bus.dispatch({ type: "governance_state", chat_id: "", payload: govState() });
    expect(note?.classList.contains("hidden")).toBe(true);
  });

  it("onGovernanceChange replays the current state and fires on updates", async () => {
    const { gov, bus } = await load();
    gov.initGovernance();
    bus.dispatch({ type: "governance_state", chat_id: "", payload: govState() });

    const seen: boolean[] = [];
    gov.onGovernanceChange((g) => seen.push(g.features.mcp_enabled));
    // Immediate replay of the current state.
    expect(seen).toEqual([true]);

    bus.dispatch({
      type: "governance_state",
      chat_id: "",
      payload: govState({ features: { ...govState().features, mcp_enabled: false } }),
    });
    expect(seen).toEqual([true, false]);
  });
});

describe("setting locks", () => {
  function mountSwitch(id: string, checked: boolean): HTMLInputElement {
    document.body.insertAdjacentHTML(
      "beforeend",
      `<div class="section-option"><label class="toggle">` +
        `<input type="checkbox" id="${id}"${checked ? " checked" : ""}></label>` +
        `<div><span class="section-option-label">Label</span></div></div>`,
    );
    return document.getElementById(id) as HTMLInputElement;
  }

  it("pins a locked switch to the required value, disables it and names who set it", async () => {
    const input = mountSwitch("flag-telemetry", true);
    const { gov, bus } = await load();
    gov.initGovernance();
    bus.dispatch({
      type: "governance_state",
      chat_id: "",
      payload: govState({
        locks: {
          "telemetry.enabled": {
            source: "organization",
            reason: "Set by your organization",
            value: false,
          },
        },
      }),
    });
    expect(input.disabled).toBe(true);
    expect(input.checked).toBe(false);
    const note = document.getElementById("flag-telemetry-lock");
    expect(note?.textContent).toBe("Set by your organization");
    expect(input.getAttribute("aria-describedby")).toBe("flag-telemetry-lock");
  });

  it("frees the switch and drops the note when the lock goes away", async () => {
    const input = mountSwitch("flag-workflows", true);
    const { gov, bus } = await load();
    gov.initGovernance();
    const lock = {
      source: "administrator",
      reason: "Subagents are blocked by your organization",
      value: false,
    };
    bus.dispatch({
      type: "governance_state",
      chat_id: "",
      payload: govState({ locks: { workflows_enabled: lock } }),
    });
    bus.dispatch({ type: "governance_state", chat_id: "", payload: govState() });
    expect(input.disabled).toBe(false);
    expect(document.getElementById("flag-workflows-lock")).toBeNull();
    expect(input.hasAttribute("aria-describedby")).toBe(false);
  });

  it("re-applies the lock over a value a later load wrote", async () => {
    const input = mountSwitch("flag-content-collection", false);
    const { gov, bus } = await load();
    gov.initGovernance();
    bus.dispatch({
      type: "governance_state",
      chat_id: "",
      payload: govState({
        locks: {
          content_collection_enabled: {
            source: "organization",
            reason: "Set by your organization",
            value: true,
          },
        },
      }),
    });
    gov.writeSwitch(input, false);
    gov.paintSettingLocks();
    expect(input.checked).toBe(true);
    expect(document.querySelectorAll("#flag-content-collection-lock")).toHaveLength(1);
  });

  const lockable = [
    ["flag-content-collection", "content_collection_enabled"],
    ["flag-telemetry", "telemetry.enabled"],
    ["flag-workflows", "workflows_enabled"],
    ["flag-inline-agents", "inline_agents_enabled"],
  ] as const;

  describe.each(lockable)("%s", (inputID, key) => {
    it.each([true, false])("restores a stored %s when the opposite lock lifts", async (stored) => {
      const input = mountSwitch(inputID, stored);
      const { gov, bus } = await load();
      gov.initGovernance();
      const lock = { source: "organization", reason: "Set by your organization", value: !stored };
      bus.dispatch({
        type: "governance_state",
        chat_id: "",
        payload: govState({ locks: { [key]: lock } }),
      });
      expect(input.checked).toBe(!stored);
      bus.dispatch({ type: "governance_state", chat_id: "", payload: govState() });
      expect(input.checked).toBe(stored);
      expect(input.disabled).toBe(false);
    });
  });

  it("restores the value a load wrote while the switch was locked", async () => {
    const input = mountSwitch("flag-telemetry", false);
    const { gov, bus } = await load();
    gov.initGovernance();
    const lock = { source: "organization", reason: "Set by your organization", value: false };
    bus.dispatch({
      type: "governance_state",
      chat_id: "",
      payload: govState({ locks: { "telemetry.enabled": lock } }),
    });
    gov.writeSwitch(input, true);
    gov.paintSettingLocks();
    expect(input.checked).toBe(false);
    bus.dispatch({ type: "governance_state", chat_id: "", payload: govState() });
    expect(input.checked).toBe(true);
  });
});

describe("cold snapshot", () => {
  it("adopts an unknown snapshot's locks and registry without gating profile features", async () => {
    document.body.insertAdjacentHTML(
      "beforeend",
      `<div class="section-option"><label class="toggle">` +
        `<input type="checkbox" id="flag-workflows" checked></label>` +
        `<div><span class="section-option-label">Workflows</span></div></div>`,
    );
    const input = document.getElementById("flag-workflows") as HTMLInputElement;
    const { gov } = await load();
    const api = await import("./api-client.js");
    const cold = govState({
      known: false,
      is_enterprise: true,
      features: { ...govState().features, mcp_enabled: false },
      locks: {
        workflows_enabled: {
          source: "administrator",
          reason: "Subagents are blocked by your organization",
          value: false,
        },
      },
      mcp_registry: { servers: [], filtered: ["github"], unresolved: [] },
    });
    vi.mocked(api.apiGetTyped).mockResolvedValueOnce(cold);
    gov.initGovernance();
    await vi.waitFor(() => {
      expect(gov.currentGovernance()?.mcp_registry?.filtered).toEqual(["github"]);
    });
    expect(input.disabled).toBe(true);
    expect(input.checked).toBe(false);
    expect(gov.featureDisabled("mcp_enabled")).toBe(false);
    expect(document.getElementById("general-governance-section")?.hidden).toBe(true);
  });

  it("does not let a late snapshot overwrite a newer SSE frame", async () => {
    const { gov, bus } = await load();
    const api = await import("./api-client.js");
    let resolve: (g: GovernanceStatePayload) => void = () => undefined;
    vi.mocked(api.apiGetTyped).mockReturnValueOnce(
      new Promise((r) => {
        resolve = r;
      }),
    );
    gov.initGovernance();
    bus.dispatch({
      type: "governance_state",
      chat_id: "",
      payload: govState({ is_enterprise: true }),
    });
    resolve(govState({ known: false }));
    await Promise.resolve();
    await Promise.resolve();
    expect(gov.currentGovernance()?.known).toBe(true);
  });
});
