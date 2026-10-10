// A server the agent reports that this page does not configure gets a read-only row with a provenance chip.
import { describe, expect, it, vi, beforeEach } from "vitest";

// The status fetch is the only input to the read-only name list.
const statusResponse = { servers: [] as Record<string, unknown>[] };
// The real signature, generics included: tsconfig.test.json checks the factory against Partial<typeof module>.
vi.mock(import("./api-client.js"), async (importOriginal) => {
  const actual = await importOriginal();
  return {
    ...actual,
    apiGetTyped: async <T>(path: string, decode: Decoder<T>): Promise<T | null> =>
      path === "/api/mcp/status" ? decode(statusResponse) : null,
  };
});
// Only the two members mcp-ui.ts calls at module scope are replaced.
vi.mock(import("./actions/index.js"), async (importOriginal) => {
  const actual = await importOriginal();
  return {
    ...actual,
    // registerCleanup returns its unregister function.
    registerCleanup:
      (_fn: () => void): (() => void) =>
      () => {
        /* noop: the singleton cleanup registry is process-wide */
      },
  };
});

import type { Decoder } from "./api-client.js";
import {
  applyOriginChip,
  carriesLiveReport,
  mountForeignRow,
  mountRow,
  renderForeignMeta,
} from "./mcp-ui.js";
import {
  discoverySignalFor,
  mcpState,
  servers,
  statusSignalFor,
  unconfiguredNames,
} from "./mcp-state.js";
import type { RuntimeStatus, Server } from "./mcp-state.js";
import type { MCPOrigin } from "./wire/types.gen.js";

function status(over: Partial<RuntimeStatus> = {}): RuntimeStatus {
  return { name: "s", origin: "power", state: "connected", ...over } as RuntimeStatus;
}

function configured(name: string): Server {
  return {
    id: `id-${name}`,
    name,
    transport: "stdio",
    enabled: true,
    created_at: 0,
    updated_at: 0,
  };
}

/** Lets the controller's queueMicrotask and fetch settle. */
async function settle(): Promise<void> {
  await new Promise((r) => setTimeout(r, 0));
  await new Promise((r) => setTimeout(r, 0));
}

describe("applyOriginChip", () => {
  it("stays hidden for the user's own server", () => {
    const chip = document.createElement("span");
    applyOriginChip(chip, { origin: "user" });
    expect(chip.hidden).toBe(true);
    expect(chip.textContent).toBe("");
  });

  it("names the Power a server came from", () => {
    const chip = document.createElement("span");
    applyOriginChip(chip, { origin: "power" });
    expect(chip.hidden).toBe(false);
    expect(chip.textContent).toContain("Power");
    // The row has no edit or remove control, so the tooltip says what the reader can do.
    expect(chip.dataset["tooltip"]).toContain("cannot edit or remove");
  });

  it("says an unattributable server is not managed here", () => {
    const chip = document.createElement("span");
    applyOriginChip(chip, { origin: "unknown" });
    expect(chip.hidden).toBe(false);
    expect(chip.textContent).toContain("not managed here");
  });

  it("names the Power by its name when the wire carries one", () => {
    const chip = document.createElement("span");
    applyOriginChip(chip, { origin: "power", originPower: "aws-infra" });
    expect(chip.textContent).toBe("from the aws-infra Power");
  });

  it("points a workspace server at the file that defines it", () => {
    const chip = document.createElement("span");
    applyOriginChip(chip, { origin: "workspace", originRoot: "/workspace/app" });
    expect(chip.hidden).toBe(false);
    expect(chip.textContent).toBe("from the workspace config");
    expect(chip.dataset["tooltip"]).toContain("/workspace/app/.kiro/settings/mcp.json");
  });

  it("says a bundled server ships with Kiro", () => {
    const chip = document.createElement("span");
    applyOriginChip(chip, { origin: "bundled" });
    expect(chip.hidden).toBe(false);
    expect(chip.textContent).toBe("bundled with Kiro");
  });

  it("clears a stale chip when the origin becomes user", () => {
    const chip = document.createElement("span");
    applyOriginChip(chip, { origin: "power" });
    applyOriginChip(chip, { origin: "user" });
    expect(chip.hidden).toBe(true);
    expect(chip.textContent).toBe("");
    expect(chip.hasAttribute("data-tooltip")).toBe(false);
  });
});

describe("carriesLiveReport", () => {
  it("puts marotte's own live report on marotte's row", () => {
    for (const state of ["connected", "needs_auth", "failed"] as const) {
      expect(carriesLiveReport(status({ origin: "user", state }), true)).toBe(true);
    }
  });

  it("keeps another config's server off marotte's row", () => {
    // That server has its own read-only row; Reconnect there acts on it.
    for (const origin of ["workspace", "power", "bundled", "unknown"] as const) {
      expect(carriesLiveReport(status({ origin, state: "connected" }), true)).toBe(false);
    }
  });

  it("puts any origin's live report on a read-only row", () => {
    for (const origin of ["workspace", "power", "bundled", "unknown"] as const) {
      expect(carriesLiveReport(status({ origin, state: "connected" }), false)).toBe(true);
    }
  });

  it("is not a live report when nothing is running", () => {
    for (const state of ["idle", "disabled"] as const) {
      expect(carriesLiveReport(status({ origin: "user", state }), true)).toBe(false);
      expect(carriesLiveReport(status({ origin: "workspace", state }), false)).toBe(false);
    }
  });
});

function action(row: HTMLElement, verb: string): HTMLButtonElement | null {
  for (const b of row.querySelectorAll<HTMLButtonElement>(".mcp-row-actions button")) {
    if ((b.getAttribute("aria-label") ?? "").startsWith(`${verb} `)) {
      return b;
    }
  }
  return null;
}

function shown(b: HTMLButtonElement | null): boolean {
  return b !== null && !b.hidden;
}

describe("a server marotte owns", () => {
  beforeEach(() => {
    servers.clear();
    statusResponse.servers = [];
    unconfiguredNames.value = [];
  });

  async function report(...entries: Record<string, unknown>[]): Promise<void> {
    statusResponse.servers = entries;
    mcpState.refetchStatus();
    await settle();
  }

  it("is editable while idle, with nothing to reconnect", async () => {
    const s = configured("idle-one");
    servers.setAll([s]);
    await report();

    const row = mountRow(s, s.id);
    expect(shown(action(row, "Edit"))).toBe(true);
    expect(shown(action(row, "Remove"))).toBe(true);
    expect(shown(action(row, "Reconnect"))).toBe(false);
  });

  it("is editable while disabled", async () => {
    const s = { ...configured("off-one"), enabled: false };
    servers.setAll([s]);
    await report({ name: "off-one", state: "disabled", origin: "user" });

    const row = mountRow(s, s.id);
    expect(shown(action(row, "Edit"))).toBe(true);
    expect(shown(action(row, "Remove"))).toBe(true);
    expect(row.querySelector(".mcp-row-meta-text")?.textContent).toBe("Disabled");
  });

  it("is editable while running, and carries the running server's status", async () => {
    const s = configured("live-one");
    servers.setAll([s]);
    await report({ name: "live-one", state: "connected", origin: "user" });

    const row = mountRow(s, s.id);
    expect(shown(action(row, "Edit"))).toBe(true);
    expect(shown(action(row, "Remove"))).toBe(true);
    expect(shown(action(row, "Reconnect"))).toBe(true);
    expect(row.querySelector(".mcp-dot")?.classList.contains("connected")).toBe(true);
  });

  it("stays editable when a workspace server shadows it, which gets its own row", async () => {
    const s = configured("mine");
    servers.setAll([s]);
    await report({
      name: "mine",
      state: "connected",
      origin: "workspace",
      origin_root: "/workspace/app",
      shadows: true,
    });

    const own = mountRow(s, s.id);
    expect(shown(action(own, "Edit"))).toBe(true);
    expect(shown(action(own, "Remove"))).toBe(true);
    // Reconnect acts on the running server, which is not this definition.
    expect(shown(action(own, "Reconnect"))).toBe(false);
    expect(own.querySelector(".mcp-row-meta-text")?.textContent).toContain("Not in use");

    expect(unconfiguredNames.value).toEqual(["mine"]);
    const theirs = mountForeignRow("mine");
    expect(shown(action(theirs, "Reconnect"))).toBe(true);
    expect(action(theirs, "Edit")).toBeNull();
    expect(action(theirs, "Remove")).toBeNull();
  });
});

describe("a server marotte does not own", () => {
  beforeEach(() => {
    servers.clear();
    statusResponse.servers = [];
    unconfiguredNames.value = [];
  });

  it("is a read-only row: no edit, no delete, no toggle", async () => {
    // A `user` stamp on a name marotte does not hold arrives as `unknown`, as a spoofed stamp would.
    statusResponse.servers = [{ name: "theirs", state: "connected", origin: "unknown" }];
    mcpState.refetchStatus();
    await settle();

    expect(unconfiguredNames.value).toEqual(["theirs"]);
    const row = mountForeignRow("theirs");
    expect(action(row, "Edit")).toBeNull();
    expect(action(row, "Remove")).toBeNull();
    expect(row.querySelector("input[type=checkbox]")).toBeNull();
    expect(shown(action(row, "Reconnect"))).toBe(true);
  });

  it("offers no Reconnect once the agent stops running it", async () => {
    statusResponse.servers = [{ name: "theirs", state: "disabled", origin: "power" }];
    mcpState.refetchStatus();
    await settle();

    expect(shown(action(mountForeignRow("theirs"), "Reconnect"))).toBe(false);
  });
});

describe("sign-in follows the live report onto either row", () => {
  beforeEach(() => {
    servers.clear();
    statusResponse.servers = [];
    unconfiguredNames.value = [];
  });

  const signIn = (row: HTMLElement) => ({
    pill: row.querySelector<HTMLAnchorElement>(".mcp-oauth-pill"),
    relay: row.querySelector("details"),
  });

  it("puts a shadowing server's sign-in on its own row, not marotte's", async () => {
    const s = configured("mine");
    servers.setAll([s]);
    statusResponse.servers = [
      {
        name: "mine",
        state: "needs_auth",
        origin: "workspace",
        origin_root: "/workspace/app",
        shadows: true,
        oauth_url: "https://auth.example.com/authorize",
      },
    ];
    mcpState.refetchStatus();
    await settle();

    const own = signIn(mountRow(s, s.id));
    expect(own.pill).toBeNull();
    expect(own.relay).toBeNull();

    const theirs = signIn(mountForeignRow("mine"));
    expect(theirs.pill?.getAttribute("href")).toBe("https://auth.example.com/authorize");
    expect(theirs.relay).not.toBeNull();
  });

  it("offers sign-in on an unowned server waiting for it, and drops the relay once spent", async () => {
    statusResponse.servers = [
      {
        name: "theirs",
        state: "needs_auth",
        origin: "power",
        oauth_url: "https://auth.example.com/authorize",
      },
    ];
    mcpState.refetchStatus();
    await settle();

    const row = mountForeignRow("theirs");
    expect(signIn(row).pill).not.toBeNull();
    expect(signIn(row).relay).not.toBeNull();

    statusResponse.servers = [
      {
        name: "theirs",
        state: "needs_auth",
        origin: "power",
        oauth_url: "https://auth.example.com/authorize",
        relayed: true,
      },
    ];
    mcpState.refetchStatus();
    await settle();
    expect(signIn(row).pill).not.toBeNull();
    expect(signIn(row).relay).toBeNull();

    statusResponse.servers = [{ name: "theirs", state: "connected", origin: "power" }];
    mcpState.refetchStatus();
    await settle();
    expect(signIn(row).pill).toBeNull();
  });
});

describe("renderForeignMeta", () => {
  // "disabled" (the agent is not using it) must not read like "idle" (no chat is running).
  const cases: { state: RuntimeStatus["state"]; want: string }[] = [
    { state: "connected", want: "available to the agent" },
    { state: "needs_auth", want: "Waiting for sign-in" },
    { state: "disabled", want: "not using it" },
    { state: "idle", want: "Not connected" },
  ];
  for (const { state, want } of cases) {
    it(`${state} reads as "${want}"`, () => {
      expect(renderForeignMeta(status({ state } as Partial<RuntimeStatus>))).toContain(want);
    });
  }

  it("surfaces the failure reason", () => {
    expect(renderForeignMeta(status({ state: "failed", error: "spawn ENOENT" }))).toContain(
      "spawn ENOENT",
    );
  });
});

describe("unconfiguredNames after a status fetch", () => {
  beforeEach(() => {
    servers.clear();
    statusResponse.servers = [];
    unconfiguredNames.value = [];
  });

  it("lists a server the config list does not hold, and only that one", async () => {
    servers.setAll([configured("mine")]);
    statusResponse.servers = [
      { name: "mine", state: "connected", origin: "user" },
      { name: "from-a-power", state: "connected", origin: "power" },
      { name: "mystery", state: "disabled", origin: "unknown" },
    ];

    mcpState.refetchStatus();
    await settle();

    // Sorted, so the assertion is on content.
    expect(unconfiguredNames.value).toEqual(["from-a-power", "mystery"]);
  });

  it("does not double-render a server that IS configured, whatever the origin says", async () => {
    // The two fetches are independent, so status can name a configured server; no second row for it.
    servers.setAll([configured("mine")]);
    statusResponse.servers = [{ name: "mine", state: "connected", origin: "power" }];

    mcpState.refetchStatus();
    await settle();

    expect(unconfiguredNames.value).toEqual([]);
  });

  it("gives a workspace server its own row when it shadows a configured name", async () => {
    // The configured row says "Not in use"; the running workspace server needs its own row.
    servers.setAll([configured("mine")]);
    statusResponse.servers = [
      { name: "mine", state: "connected", origin: "workspace", shadows: true },
    ];

    mcpState.refetchStatus();
    await settle();

    expect(unconfiguredNames.value).toEqual(["mine"]);
  });

  it("empties the list once the foreign server stops being reported", async () => {
    statusResponse.servers = [{ name: "gone-later", state: "connected", origin: "power" }];
    mcpState.refetchStatus();
    await settle();
    expect(unconfiguredNames.value).toEqual(["gone-later"]);

    statusResponse.servers = [];
    mcpState.refetchStatus();
    await settle();
    expect(unconfiguredNames.value).toEqual([]);
  });
});

describe("setStatusFromEvent", () => {
  it("keeps the origin an SSE frame cannot carry", async () => {
    statusResponse.servers = [{ name: "from-a-power", state: "connected", origin: "power" }];
    mcpState.refetchStatus();
    await settle();

    // mcp_failed carries no origin; falling back to "user" would present another config's server as the user's own.
    mcpState.setStatusFromEvent("from-a-power", {
      name: "from-a-power",
      state: "failed",
      error: "boom",
    });

    const after = statusSignalFor("from-a-power").peek();
    expect(after.origin).toBe<MCPOrigin>("power");
    expect(after.state).toBe("failed");
  });

  it("keeps the workspace root and the shadow flag an SSE frame cannot carry", async () => {
    statusResponse.servers = [
      {
        name: "shadowed",
        state: "connected",
        origin: "workspace",
        origin_root: "/workspace/app",
        shadows: true,
      },
    ];
    mcpState.refetchStatus();
    await settle();

    mcpState.setStatusFromEvent("shadowed", { name: "shadowed", state: "failed", error: "boom" });

    const after = statusSignalFor("shadowed").peek();
    expect(after.originRoot).toBe("/workspace/app");
    expect(after.shadows).toBe(true);
  });
});

describe("resource templates after a status fetch", () => {
  it("stores the templates a connected server advertises", async () => {
    statusResponse.servers = [
      {
        name: "tpl",
        state: "connected",
        origin: "user",
        resource_templates: [
          { name: "issue", uri_template: "gh://issues/{number}", mime_type: "text/plain" },
        ],
      },
    ];
    mcpState.refetchStatus();
    await settle();

    expect(discoverySignalFor("tpl").peek().resource_templates).toEqual([
      { name: "issue", uri_template: "gh://issues/{number}", mime_type: "text/plain" },
    ]);
  });
});

describe("a configured row's prompts & resources disclosure", () => {
  it("counts and lists resource templates beside resources, one field per variable", async () => {
    const s = configured("tpl-row");
    servers.setAll([s]);
    statusResponse.servers = [
      {
        name: "tpl-row",
        state: "connected",
        origin: "user",
        resources: [{ name: "projects", uri: "resource://projects" }],
        resource_templates: [
          {
            name: "inbox",
            uri_template: "resource://inbox/{agent}",
            description: "One agent's inbox",
          },
          { name: "broken", uri_template: "resource://bad/{agent" },
        ],
      },
    ];
    mcpState.refetchStatus();
    await settle();

    const row = mountRow(s, s.id);
    const box = row.querySelector<HTMLElement>(".mcp-discovery");
    expect(box?.hidden).toBe(false);
    expect(box?.querySelector(".mcp-discovery-summary")?.textContent).toBe(
      "Prompts & resources (3)",
    );
    const groups = [...(box?.querySelectorAll(".mcp-disc-group") ?? [])].map((g) => g.textContent);
    expect(groups).toEqual(["Resources", "Resource templates"]);

    const names = [...(box?.querySelectorAll(".mcp-disc-item-name") ?? [])].map(
      (n) => n.textContent,
    );
    expect(names).toEqual(["projects", "inbox", "broken"]);

    const inbox = box?.querySelectorAll(".mcp-disc-prompt-wrap")[0];
    const form = inbox?.querySelector<HTMLFormElement>("form.mcp-disc-arg-form");
    expect(form?.hidden).toBe(true);
    inbox?.querySelector<HTMLButtonElement>(".mcp-disc-item .mcp-disc-insert")?.click();
    expect(form?.hidden).toBe(false);
    const fields = [...(form?.querySelectorAll(".mcp-disc-arg-name") ?? [])].map(
      (f) => f.textContent,
    );
    expect(fields).toEqual(["agent"]);

    // A template parseTemplate refuses is listed without an action rather than dropped.
    const broken = [...(box?.querySelectorAll(".mcp-disc-item") ?? [])].at(-1);
    expect(broken?.querySelector(".mcp-disc-item-name")?.textContent).toBe("broken");
    expect(broken?.querySelector(".mcp-disc-insert")).toBeNull();
  });
});
