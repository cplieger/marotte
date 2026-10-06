import { afterEach, describe, expect, it, vi } from "vitest";
import { isPending } from "@cplieger/actions";
import {
  applyMcpGovernance,
  applyMcpRowPolicy,
  bindRegistryPanel,
  mountRow,
  renderRegistryPanel,
  type McpGovernanceEls,
} from "./mcp-ui.js";
import { servers, type Server } from "./mcp-state.js";
import type { GovernanceMCPRegistry, GovernanceStatePayload } from "./types.js";

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

  it("words an administrator's decision without the raw token", () => {
    const els = fixtures();
    applyMcpGovernance(govOff({ disabled_reason: "admin_disabled" }), els);
    expect(els.notice.textContent).toBe("MCP integrations are disabled by your organization.");
    expect(els.add.getAttribute("data-tooltip")).toBe("MCP is disabled by your organization");
  });

  it.each(["api_failure", "no_endpoint"])(
    "says the settings could not be loaded for %s, not that an admin decided",
    (token) => {
      const els = fixtures();
      applyMcpGovernance(govOff({ disabled_reason: token }), els);
      expect(els.notice.textContent).toBe(
        "MCP integrations are unavailable. Couldn't load your organization's settings.",
      );
      expect(els.notice.textContent).not.toContain(token);
      expect(els.add.getAttribute("data-tooltip")).toBe("MCP is unavailable");
    },
  );

  it("never shows an unrecognised token", () => {
    const els = fixtures();
    applyMcpGovernance(govOff({ disabled_reason: "Blocked by policy X" }), els);
    expect(els.notice.textContent).toBe("MCP integrations are disabled by your organization.");
  });

  it("leaves the affordance enabled when MCP is allowed", () => {
    const els = fixtures();
    applyMcpGovernance(govState(), els);
    expect(els.add.disabled).toBe(false);
    expect(els.notice.hidden).toBe(true);
  });

  it("stays permissive while the policy is unknown", () => {
    const els = fixtures();
    // Known=false is unknown: nothing is disabled even though mcp_enabled is false.
    applyMcpGovernance(govOff({ known: false }), els);
    expect(els.add.disabled).toBe(false);
    expect(els.notice.hidden).toBe(true);
  });

  // The empty state names the add button, so it follows the same policy.
  it("stops the empty state naming the add button when the gate is off", () => {
    const els = fixtures();
    applyMcpGovernance(govOff(), els);
    expect(els.empty.textContent).not.toContain("Click +");
    expect(els.empty.textContent).toBe(
      "No integrations connected, and none can be added while MCP is disabled.",
    );
  });

  it("blocks MCP on a personal account when the administrator denies it", () => {
    const els = fixtures();
    applyMcpGovernance(
      govState({
        locks: {
          mcp: {
            source: "administrator",
            reason: "MCP servers are blocked by your organization",
            value: false,
          },
        },
      }),
      els,
    );
    expect(els.add.disabled).toBe(true);
    expect(els.add.getAttribute("data-tooltip")).toBe(
      "MCP servers are blocked by your organization",
    );
    expect(els.notice.textContent).toBe("MCP servers are blocked by your organization.");
  });

  // In registry mode KAS discards searched and pasted configs, so their affordance goes until the mode lifts.
  it("hides the add affordance in registry mode and restores it after", () => {
    const els = fixtures();
    const reg = { servers: [], filtered: [], unresolved: [] };
    applyMcpGovernance(govState({ mcp_registry: reg }), els);
    expect(els.add.classList.contains("hidden")).toBe(true);
    expect(els.empty.textContent).toBe(
      "No integrations connected yet. Add one from your organization's registry above.",
    );

    applyMcpGovernance(govState(), els);
    expect(els.add.classList.contains("hidden")).toBe(false);
    expect(els.empty.textContent).toContain("Click +");
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

describe("a configured row under the organization's policy", () => {
  const lock = {
    mcp: {
      source: "administrator",
      reason: "MCP servers are blocked by your organization",
      value: false,
    },
  };

  function server(name: string): Server {
    return {
      id: `id-${name}`,
      name,
      transport: "stdio",
      command: "run-it",
      enabled: true,
      created_at: 0,
      updated_at: 0,
    };
  }

  function control(row: HTMLElement, label: string): HTMLButtonElement {
    const btn = row.querySelector<HTMLButtonElement>(`[aria-label="${label}"]`);
    if (btn === null) {
      throw new Error(`no control ${label}`);
    }
    return btn;
  }

  afterEach(() => {
    applyMcpRowPolicy(govState());
  });

  it("is read-only while an administrator denies MCP, and editable once it lifts", () => {
    const s = server("locked-one");
    const row = mountRow(s, s.id);
    const toggle = row.querySelector<HTMLInputElement>('input[type="checkbox"]');

    applyMcpRowPolicy(govState({ locks: lock }));
    expect(toggle?.disabled).toBe(true);
    expect(control(row, "Edit locked-one").classList.contains("hidden")).toBe(true);
    expect(control(row, "Remove locked-one").classList.contains("hidden")).toBe(true);
    expect(row.querySelector(".mcp-toggle")?.getAttribute("data-tooltip")).toBe(
      "MCP servers are blocked by your organization",
    );

    applyMcpRowPolicy(govState());
    expect(toggle?.disabled).toBe(false);
    expect(control(row, "Edit locked-one").classList.contains("hidden")).toBe(false);
    expect(control(row, "Remove locked-one").classList.contains("hidden")).toBe(false);
    expect(row.querySelector(".mcp-toggle")?.hasAttribute("data-tooltip")).toBe(false);
  });

  it.each([
    ["admin_disabled", "MCP is disabled by your organization"],
    ["api_failure", "MCP is unavailable"],
  ])(
    "is read-only while the account profile turns MCP off (%s), and editable once it lifts",
    (token, reason) => {
      const s = server(`profile-${token}`);
      const row = mountRow(s, s.id);
      const toggle = row.querySelector<HTMLInputElement>('input[type="checkbox"]');

      applyMcpRowPolicy(govOff({ disabled_reason: token }));
      expect(toggle?.disabled).toBe(true);
      expect(control(row, `Remove ${s.name}`).classList.contains("hidden")).toBe(true);
      expect(row.querySelector(".mcp-toggle")?.getAttribute("data-tooltip")).toBe(reason);

      applyMcpRowPolicy(govState());
      expect(toggle?.disabled).toBe(false);
      expect(control(row, `Remove ${s.name}`).classList.contains("hidden")).toBe(false);
    },
  );

  it("keeps the rows editable while an unknown profile reads MCP off", () => {
    const s = server("unknown-profile");
    const row = mountRow(s, s.id);
    applyMcpRowPolicy(govOff({ known: false }));
    expect(row.querySelector<HTMLInputElement>('input[type="checkbox"]')?.disabled).toBe(false);
  });

  it("leaves a toggle disabled when the lock lands while its request is in flight", async () => {
    let settle: (r: Response) => void = () => undefined;
    const fetchSpy = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      if (init?.method === "PATCH") {
        return new Promise<Response>((r) => {
          settle = r;
        });
      }
      void input;
      return Promise.resolve(new Response("[]", { status: 200 }));
    });
    vi.stubGlobal("fetch", fetchSpy);
    try {
      const s = server("in-flight");
      const row = mountRow(s, s.id);
      const toggle = row.querySelector<HTMLInputElement>('input[type="checkbox"]');
      if (toggle === null) {
        throw new Error("no toggle");
      }
      toggle.checked = false;
      toggle.dispatchEvent(new Event("change"));
      await vi.waitFor(() => {
        expect(isPending("mcp.toggle_server")).toBe(true);
      });

      applyMcpRowPolicy(govState({ locks: lock }));
      settle(new Response(JSON.stringify({ ...s, enabled: false }), { status: 200 }));
      await vi.waitFor(() => {
        expect(isPending("mcp.toggle_server")).toBe(false);
      });
      expect(toggle.disabled).toBe(true);
    } finally {
      vi.unstubAllGlobals();
    }
  });

  it("says on its own row that the registry does not allow it, until the mode lifts", () => {
    const s = server("dropped");
    const row = mountRow(s, s.id);
    const meta = row.querySelector(".mcp-row-meta-text");
    const dot = row.querySelector(".mcp-dot");

    applyMcpRowPolicy(
      govState({ mcp_registry: { servers: [], filtered: ["dropped"], unresolved: [] } }),
    );
    expect(meta?.textContent).toBe("Not allowed by your organization's MCP registry");
    expect(dot?.classList.contains("disabled")).toBe(true);

    applyMcpRowPolicy(govState());
    expect(meta?.textContent).toBe("no credentials · run-it");
    expect(dot?.classList.contains("disabled")).toBe(false);
  });
});

describe("renderRegistryPanel", () => {
  const registry: GovernanceMCPRegistry = {
    servers: [
      { name: "docs", description: "Internal docs search", enabled: true },
      { name: "tickets", enabled: true },
    ],
    filtered: ["my-local"],
    unresolved: ["gone"],
  };
  // The panel reads only the name off a configured server.
  const configured = [{ name: "docs" } as Server];

  it("stays hidden and empty when MCP is not restricted to a registry", () => {
    const host = document.createElement("div");
    host.append(document.createElement("p"));
    renderRegistryPanel(host, undefined, configured, () => Promise.resolve());
    expect(host.classList.contains("hidden")).toBe(true);
    expect(host.childElementCount).toBe(0);
  });

  it("offers Add only for catalog servers not yet configured", () => {
    const host = document.createElement("div");
    renderRegistryPanel(host, registry, configured, () => Promise.resolve());
    expect(host.classList.contains("hidden")).toBe(false);
    const buttons = [...host.querySelectorAll<HTMLButtonElement>(".mcp-registry-row button")];
    expect(buttons.map((b) => [b.textContent, b.disabled])).toEqual([
      ["Added", true],
      ["Add", false],
    ]);
    expect(host.textContent).toContain("Internal docs search");
  });

  it("names the dropped servers with no row here and the entries its catalog lacks", () => {
    const host = document.createElement("div");
    renderRegistryPanel(host, registry, configured, () => Promise.resolve());
    expect(host.textContent).toContain(
      "Not allowed by your organization's MCP registry: my-local.",
    );
    expect(host.textContent).toContain("Not in your organization's catalog: gone.");
  });

  // The server's own row says it was dropped, so the panel does not repeat it.
  it("leaves a dropped server this page configures to its own row", () => {
    const host = document.createElement("div");
    renderRegistryPanel(host, registry, [{ name: "my-local" } as Server], () => Promise.resolve());
    expect(host.textContent).not.toContain("my-local");
  });

  it("disables Add while an administrator denies MCP", () => {
    const host = document.createElement("div");
    renderRegistryPanel(
      host,
      registry,
      configured,
      () => Promise.resolve(),
      "MCP servers are blocked by your organization",
    );
    const add = host.querySelector<HTMLButtonElement>('[aria-label="Add tickets"]');
    expect(add?.disabled).toBe(true);
    expect(add?.dataset["tooltip"]).toBe("MCP servers are blocked by your organization");
  });

  it.each([
    ["admin_disabled", "MCP is disabled by your organization"],
    ["api_failure", "MCP is unavailable"],
  ])(
    "keeps Add disabled across a configured-list change while the profile turns MCP off (%s)",
    (token, reason) => {
      const host = document.createElement("div");
      const dispose = bindRegistryPanel(host);
      try {
        applyMcpRowPolicy(govOff({ disabled_reason: token, mcp_registry: registry }));
        servers.setAll([
          {
            id: "id-docs",
            name: "docs",
            transport: "stdio",
            command: "run-it",
            enabled: true,
            created_at: 0,
            updated_at: 0,
          },
        ]);
        expect(host.querySelector('[aria-label="docs is added"]')).not.toBeNull();
        const add = host.querySelector<HTMLButtonElement>('[aria-label="Add tickets"]');
        expect(add?.disabled).toBe(true);
        expect(add?.dataset["tooltip"]).toBe(reason);
      } finally {
        dispose();
        servers.clear();
        applyMcpRowPolicy(govState());
      }
    },
  );

  it("adds the server the reader picked", () => {
    const host = document.createElement("div");
    const onAdd = vi.fn(() => Promise.resolve());
    renderRegistryPanel(host, registry, configured, onAdd);
    host.querySelector<HTMLButtonElement>('[aria-label="Add tickets"]')?.click();
    expect(onAdd).toHaveBeenCalledWith("tickets");
  });
});
