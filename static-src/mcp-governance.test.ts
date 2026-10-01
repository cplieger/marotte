import { describe, expect, it } from "vitest";
import { applyMcpGovernance, type McpGovernanceEls } from "./mcp-ui.js";
import type { GovernanceStatePayload } from "./types.js";

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

function fixtures(): McpGovernanceEls {
  const notice = document.createElement("p");
  notice.hidden = true;
  return { add: document.createElement("button"), notice, empty: document.createElement("p") };
}

function govOff(over: Partial<GovernanceStatePayload> = {}): GovernanceStatePayload {
  return govState({ features: { ...govState().features, mcp_enabled: false }, ...over });
}

describe("applyMcpGovernance", () => {
  it("disables the add affordance + shows the notice when MCP is org-disabled", () => {
    const els = fixtures();
    applyMcpGovernance(govOff(), els);
    expect(els.add.disabled).toBe(true);
    expect(els.add.getAttribute("data-tooltip")).toContain("disabled by your organization");
    expect(els.notice.hidden).toBe(false);
    expect(els.notice.textContent).toContain("disabled by your organization");
  });

  it("includes the disabledReason in the notice when present", () => {
    const els = fixtures();
    applyMcpGovernance(govOff({ disabled_reason: "Blocked by policy X" }), els);
    expect(els.notice.textContent).toContain("Blocked by policy X");
  });

  it("leaves the affordance enabled when MCP is allowed", () => {
    const els = fixtures();
    applyMcpGovernance(govState(), els);
    expect(els.add.disabled).toBe(false);
    expect(els.notice.hidden).toBe(true);
  });

  it("stays permissive while the policy is unknown", () => {
    const els = fixtures();
    // Known=false → treat as unknown: don't disable even though mcp_enabled is false.
    applyMcpGovernance(govOff({ known: false }), els);
    expect(els.add.disabled).toBe(false);
    expect(els.notice.hidden).toBe(true);
  });

  // The empty state's call to action names the add button, so it has to follow
  // the same policy: telling a reader to click a disabled control is the one
  // instruction the section can give that cannot be followed.
  it("stops the empty state naming the add button when the gate is off", () => {
    const els = fixtures();
    applyMcpGovernance(govOff(), els);
    expect(els.empty.textContent).not.toContain("Click +");
    expect(els.empty.textContent).toBe(
      "No integrations connected, and none can be added while MCP is disabled.",
    );
  });

  it("restores the call to action when the gate is on", () => {
    const els = fixtures();
    applyMcpGovernance(govOff(), els);
    applyMcpGovernance(govState(), els);
    expect(els.empty.textContent).toBe(
      "No integrations connected yet. Click + to search the official MCP registry or paste a config.",
    );
  });
});
