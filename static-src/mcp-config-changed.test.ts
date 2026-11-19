// An mcp_config_changed frame re-reads the organization provider, so a provider saved or pasted on another page
// reaches this one and the card's Edit opens on it.
import { beforeAll, describe, expect, it, vi } from "vitest";

const wire = vi.hoisted(() => ({
  provider: null as Record<string, unknown> | null,
  providerReads: 0,
}));
vi.mock(import("./api-client.js"), async (importOriginal) => {
  const actual = await importOriginal();
  return {
    ...actual,
    apiGetTyped: <T>(path: string, decode: Decoder<T>): Promise<T | null> => {
      switch (path) {
        case "/api/mcp/identity-provider":
          wire.providerReads++;
          return Promise.resolve(
            decode({ config: wire.provider, status: { known: false }, unreadable: false }),
          );
        case "/api/mcp/status":
          return Promise.resolve(
            decode({ servers: [], subject: { kind: "mcp_status", ref: "", version: "1" } }),
          );
        case "/api/mcp":
          return Promise.resolve(decode({ servers: [] }));
        case "/api/config/standing":
          return Promise.resolve(decode({ standing: [], load_errors: {} }));
        default:
          return Promise.resolve(null);
      }
    },
  };
});
vi.mock(import("./actions/index.js"), async (importOriginal) => {
  const actual = await importOriginal();
  return {
    ...actual,
    registerCleanup:
      (_fn: () => void): (() => void) =>
      () => {
        /* noop: the singleton cleanup registry is process-wide */
      },
  };
});

import type { Decoder } from "./api-client.js";
import { dispatch } from "./bus.js";
import { identityProviderConfig } from "./mcp-state.js";
import { initMCP } from "./mcp-ui.js";
import { installMirrors } from "./mirror.js";
import type { ServerEvent } from "./types.js";

beforeAll(() => {
  const section = document.createElement("div");
  section.id = "mcp-section";
  const modal = document.createElement("div");
  modal.id = "mcp-modal";
  document.body.replaceChildren(section, modal);
  installMirrors();
  initMCP();
});

describe("mcp_config_changed", () => {
  it("re-reads the organization provider a write elsewhere changed", async () => {
    wire.provider = {
      issuer: "https://login.acme.example",
      client_id: "kiro-client",
      name: "Acme",
    };
    const before = wire.providerReads;

    dispatch({ type: "mcp_config_changed", payload: {} } as ServerEvent);

    await vi.waitFor(() => {
      expect(identityProviderConfig.value.config?.issuer).toBe("https://login.acme.example");
    });
    expect(wire.providerReads).toBeGreaterThan(before);
  });
});
