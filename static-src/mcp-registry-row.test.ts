// A registry row says that an entry is deprecated (the registry still lists them) and what installing will ask for.
// It is a compact disclosure: it starts closed and installing is reachable without opening it.
import { describe, it, expect, vi, beforeEach } from "vitest";

// The configured list is the row's second input; hoisted above module bindings for the mock factory.
const state = vi.hoisted(() => ({ configured: [] as unknown[] }));

vi.mock("./mcp-state.js", () => ({
  configuredServers: () => state.configured,
  SECRET_MASK: "***",
}));

vi.mock("./dom.js", () => ({
  byId: () => document.createElement("div"),
}));
vi.mock("./actions/mcp.js", () => ({
  searchRegistry: { cancel: () => undefined, dispatch: async () => null },
  // Present-but-undefined for real-ESM linking; nothing here reaches its branch.
  registryFailureOf: undefined,
}));
vi.mock("./actions/index.js", () => ({
  subscribeToActions: () => () => undefined,
  bindLoadingState: () => () => undefined,
  debouncedDispatch: () => Object.assign(() => undefined, { cancel: () => undefined }),
  registerCleanup: () => undefined,
}));

import { renderRegistryResult } from "./mcp-panels-search.js";
import type { RegistryEntry as Entry } from "./wire/types.gen.js";
import type { KeyPair, Server } from "./mcp-state.js";

/** Only the fields the row's match and satisfaction checks read are spelled out. */
function server(patch: Partial<Server> & { name: string }): Server {
  return {
    id: `id-${patch.name}`,
    transport: "http",
    enabled: true,
    created_at: 0,
    updated_at: 0,
    ...patch,
  };
}

const pairs = (...names: string[]): KeyPair[] => names.map((name) => ({ name, value: "***" }));

function label(row: HTMLElement): string {
  const p = row.querySelector(".mcp-requires-label");
  expect(p).not.toBeNull();
  return p!.textContent ?? "";
}

beforeEach(() => {
  state.configured = [];
});

const liveRemote: Entry = {
  name: "ex/live",
  title: "Live",
  version: "2.0.0",
  description: "still maintained",
  remotes: [{ type: "http", url: "https://live/mcp" }],
};

function disc(row: HTMLElement): HTMLDetailsElement {
  const d = row.querySelector<HTMLDetailsElement>(".mcp-result-disc");
  expect(d).not.toBeNull();
  if (d === null) {
    throw new Error("no .mcp-result-disc");
  }
  return d;
}

describe("deprecated flag on a registry row", () => {
  it("badges a deprecated entry and shows the publisher's reason", () => {
    const row = renderRegistryResult({
      ...liveRemote,
      name: "ex/dead",
      status: "deprecated",
      status_message: "unmaintained; use ex/live instead",
    });
    const badge = row.querySelector(".mcp-result-status");
    expect(badge).not.toBeNull();
    expect(badge!.textContent).toBe("deprecated");
    expect(row.classList.contains("mcp-result-deprecated")).toBe(true);
    expect(row.querySelector(".mcp-result-status-note")!.textContent).toContain("use ex/live");
  });

  it("keeps the badge on the collapsed line and the reason behind the disclosure", () => {
    // The badge must be readable without a click, so it is in the summary; the publisher's sentence is detail.
    const row = renderRegistryResult({
      ...liveRemote,
      status: "deprecated",
      status_message: "unmaintained",
    });
    expect(row.querySelector(".mcp-result-summary > .mcp-result-status")).not.toBeNull();
    expect(row.querySelector(".mcp-result-body > .mcp-result-status-note")).not.toBeNull();
  });

  it("falls back to naming the status when the publisher gave no reason", () => {
    const row = renderRegistryResult({ ...liveRemote, status: "deleted" });
    expect(row.querySelector(".mcp-result-status")!.textContent).toBe("deleted");
    expect(row.querySelector(".mcp-result-status-note")!.textContent).toContain("deleted");
  });

  it("leaves a live entry unbadged", () => {
    const row = renderRegistryResult(liveRemote);
    expect(row.querySelector(".mcp-result-status")).toBeNull();
    expect(row.querySelector(".mcp-result-status-note")).toBeNull();
    expect(row.classList.contains("mcp-result-deprecated")).toBe(false);
  });
});

describe("the row as a disclosure", () => {
  it("starts closed, whatever the entry declares", () => {
    // The row is the one thing that opens, on the reader's word, even with a required credential.
    const required = renderRegistryResult({
      name: "ex/gh",
      packages: [
        {
          registry_type: "npm",
          identifier: "@ex/gh",
          env_vars: [{ name: "GITHUB_TOKEN", required: true, secret: true }],
        },
      ],
    });
    expect(disc(required).open).toBe(false);
    expect(disc(renderRegistryResult(liveRemote)).open).toBe(false);
  });

  it("has exactly one disclosure and no nested one", () => {
    // One disclosure: the requirements are plain inside it, not a nested <details>.
    const row = renderRegistryResult({
      name: "ex/both",
      packages: [
        { registry_type: "npm", identifier: "@ex/both", env_vars: [{ name: "A", required: true }] },
      ],
      remotes: [{ type: "http", url: "https://both/mcp", headers: [{ name: "B" }] }],
    });
    expect(row.querySelectorAll("details")).toHaveLength(1);
    expect(row.querySelectorAll("summary")).toHaveLength(1);
  });

  it("puts the install buttons outside the summary", () => {
    // A <summary> maps to role=button, so a <button> inside is axe nested-interactive; outside, installing needs no expand.
    const row = renderRegistryResult(liveRemote);
    const btn = row.querySelector<HTMLButtonElement>(".mcp-install-btn");
    expect(btn).not.toBeNull();
    expect(btn!.closest("summary")).toBeNull();
    expect(btn!.closest("details")).toBeNull();
    expect(btn!.parentElement!.className).toBe("mcp-result-actions");
  });
});

describe("install preview on a registry row", () => {
  it("names the required env vars a package install will need", () => {
    const row = renderRegistryResult({
      name: "ex/gh",
      version: "1.0.0",
      packages: [
        {
          registry_type: "npm",
          identifier: "@ex/gh",
          env_vars: [
            {
              name: "GITHUB_TOKEN",
              description: "PAT with repo scope",
              required: true,
              secret: true,
            },
            { name: "GITHUB_HOST", description: "for GHES" },
          ],
        },
      ],
    });
    const preview = row.querySelector<HTMLElement>(".mcp-requires");
    expect(preview).not.toBeNull();
    expect(preview!.querySelector(".mcp-requires-label")!.textContent).toContain("Needs 1 of 2");
    const names = [...preview!.querySelectorAll(".mcp-requires-name")].map((n) => n.textContent);
    expect(names).toEqual(["GITHUB_TOKEN", "GITHUB_HOST"]);
    expect(preview!.textContent).toContain("PAT with repo scope");
    expect(preview!.querySelectorAll(".mcp-pair-mark-required")).toHaveLength(1);
  });

  it("names a remote's headers and says when none are required", () => {
    const row = renderRegistryResult({
      name: "ex/remote",
      remotes: [
        {
          type: "http",
          url: "https://remote/mcp",
          headers: [{ name: "X-Tenant", description: "optional tenant id" }],
        },
      ],
    });
    const preview = row.querySelector<HTMLElement>(".mcp-requires");
    expect(preview).not.toBeNull();
    expect(preview!.querySelector(".mcp-requires-label")!.textContent).toContain(
      "Optional headers (1)",
    );
    expect(preview!.querySelectorAll(".mcp-pair-mark-required")).toHaveLength(0);
  });

  it("shows no preview when the publisher declared nothing", () => {
    const row = renderRegistryResult(liveRemote);
    expect(row.querySelector(".mcp-requires")).toBeNull();
    // Nothing to configure is a valid answer.
    expect(row.querySelector(".mcp-install-btn")).not.toBeNull();
  });

  it("keeps one preview per install option", () => {
    const row = renderRegistryResult({
      name: "ex/both",
      packages: [
        { registry_type: "npm", identifier: "@ex/both", env_vars: [{ name: "A", required: true }] },
      ],
      remotes: [
        { type: "http", url: "https://both/mcp", headers: [{ name: "B", required: true }] },
      ],
    });
    expect(row.querySelectorAll(".mcp-install-option")).toHaveLength(2);
    expect(row.querySelectorAll(".mcp-requires")).toHaveLength(2);
    expect(row.querySelectorAll(".mcp-install-btn")).toHaveLength(2);
  });

  it("names the transport on the button and the identifier in its accessible name", () => {
    // Without the identifier, two remotes of one kind would be two buttons reading the same words.
    const row = renderRegistryResult({
      name: "ex/both",
      packages: [{ registry_type: "npm", identifier: "@ex/both" }],
      remotes: [{ type: "http", url: "https://both/mcp" }],
    });
    const btns = [...row.querySelectorAll<HTMLButtonElement>(".mcp-install-btn")];
    expect(btns.map((b) => b.textContent)).toEqual(["Use npm", "Use http"]);
    expect(btns.map((b) => b.getAttribute("aria-label"))).toEqual([
      "Use npm: @ex/both",
      "Use http: https://both/mcp",
    ]);
    // Each option's identifier is readable once the row is open.
    expect([...row.querySelectorAll(".mcp-install-id")].map((c) => c.textContent)).toEqual([
      "npm: @ex/both",
      "http: https://both/mcp",
    ]);
  });
});

describe("a registry row whose server is already configured", () => {
  // A reader who already owns the server asks what is still missing, not what installing will ask for.
  const remote: Entry = {
    name: "ex/cf",
    remotes: [
      {
        type: "http",
        url: "https://mcp.ex.com/mcp",
        headers: [{ name: "Authorization", required: true, secret: true }, { name: "X-Tenant" }],
      },
    ],
  };

  it("keeps the unconfigured label when nothing matches", () => {
    state.configured = [server({ name: "unrelated", url: "https://other/mcp" })];
    expect(label(renderRegistryResult(remote))).toBe("Needs 1 of 2 headers");
  });

  it("matches on the slug the install button would write", () => {
    // `simplifyName("ex/cf")` is `cf`, the row this option would land on.
    state.configured = [server({ name: "cf", headers: pairs("Authorization") })];
    expect(label(renderRegistryResult(remote))).toBe(
      "Already configured. All required headers set",
    );
  });

  it("matches a remote on its URL under any name the reader chose", () => {
    // The endpoint names one server whatever the row is called; the satisfaction claim rests on it.
    state.configured = [
      server({ name: "my-own-name", url: "https://mcp.ex.com/mcp", headers: pairs("X-Tenant") }),
    ];
    expect(label(renderRegistryResult(remote))).toBe(
      "Already configured. 1 of 1 required headers still missing",
    );
  });

  it("marks the fields the record holds and leaves the rest unmarked", () => {
    state.configured = [server({ name: "cf", headers: pairs("X-Tenant") })];
    const items = [...renderRegistryResult(remote).querySelectorAll(".mcp-requires-list > li")];
    expect(items).toHaveLength(2);
    // Required and absent: Required, no Set.
    expect(items[0]!.querySelector(".mcp-pair-mark-required")).not.toBeNull();
    expect(items[0]!.querySelector(".mcp-pair-mark-set")).toBeNull();
    // Optional and present: Set, no Required.
    expect(items[1]!.querySelector(".mcp-pair-mark-required")).toBeNull();
    expect(items[1]!.querySelector(".mcp-pair-mark-set")!.textContent).toBe("Set");
  });

  it("never marks a field on an unconfigured row", () => {
    // No record, so nothing can be set.
    expect(renderRegistryResult(remote).querySelectorAll(".mcp-pair-mark-set")).toHaveLength(0);
  });

  it("says only that the server is configured when nothing is required", () => {
    state.configured = [server({ name: "opt" })];
    const row = renderRegistryResult({
      name: "ex/opt",
      remotes: [{ type: "http", url: "https://opt/mcp", headers: [{ name: "X-Tenant" }] }],
    });
    expect(label(row)).toBe("Already configured");
  });

  it("matches a header case-insensitively and an env var exactly", () => {
    // Header names fold case (HTTP); env names do not (the shell).
    state.configured = [
      server({ name: "case", headers: pairs("authorization"), env: pairs("github_token") }),
    ];
    expect(
      label(
        renderRegistryResult({
          name: "ex/case",
          remotes: [
            {
              type: "http",
              url: "https://case/mcp",
              headers: [{ name: "Authorization", required: true }],
            },
          ],
        }),
      ),
    ).toBe("Already configured. All required headers set");
    expect(
      label(
        renderRegistryResult({
          name: "ex/case",
          packages: [
            {
              registry_type: "npm",
              identifier: "@ex/case",
              env_vars: [{ name: "GITHUB_TOKEN", required: true }],
            },
          ],
        }),
      ),
    ).toBe("Already configured. 1 of 1 required environment variables still missing");
  });

  it("never satisfies a declared header from an env var of the same name", () => {
    // A header is not interchangeable with an env var. Named to match (slug `cf`), so the configured branch is taken.
    state.configured = [server({ name: "cf", env: pairs("Authorization") })];
    expect(label(renderRegistryResult(remote))).toBe(
      "Already configured. 1 of 1 required headers still missing",
    );
  });

  it("answers both install options of one entry against the same snapshot", () => {
    state.configured = [server({ name: "both", env: pairs("A") })];
    const row = renderRegistryResult({
      name: "ex/both",
      packages: [
        { registry_type: "npm", identifier: "@ex/both", env_vars: [{ name: "A", required: true }] },
      ],
      remotes: [
        { type: "http", url: "https://both/mcp", headers: [{ name: "B", required: true }] },
      ],
    });
    expect([...row.querySelectorAll(".mcp-requires-label")].map((p) => p.textContent)).toEqual([
      "Already configured. All required environment variables set",
      "Already configured. 1 of 1 required headers still missing",
    ]);
  });
});
