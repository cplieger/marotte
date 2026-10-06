// A configured row states whether a credential is supplied: the masked export keeps each pair's name and the slice
// length, so presence is knowable. Mock shape as mcp-origin.test.ts.
import { describe, expect, it, vi } from "vitest";

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

import { credentialSummary, renderMeta } from "./mcp-ui.js";
import type { KeyPair, RuntimeStatus, Server } from "./mcp-state.js";
import { SECRET_MASK } from "./mcp-state.js";

function server(over: Partial<Server> = {}): Server {
  return {
    id: "id-s",
    name: "s",
    transport: "http",
    enabled: true,
    url: "https://ex/mcp",
    created_at: 0,
    updated_at: 0,
    ...over,
  };
}

const pairs = (...names: string[]): KeyPair[] =>
  names.map((name) => ({ name, value: SECRET_MASK }));

function status(over: Partial<RuntimeStatus> = {}): RuntimeStatus {
  return { name: "s", origin: "user", state: "connected", ...over } as RuntimeStatus;
}

describe("credentialSummary", () => {
  it("says so when the record holds nothing", () => {
    // Silence would be indistinguishable from an unread row, so the absence is stated.
    expect(credentialSummary(server())).toBe("no credentials");
  });

  it("counts a masked pair by its NAME, never its value", () => {
    // A saved secret returns as the mask, so a value test would read every stored credential as absent.
    expect(credentialSummary(server({ headers: pairs("Authorization") }))).toBe("1 header");
  });

  it("agrees with itself on singular and plural", () => {
    expect(credentialSummary(server({ env: pairs("A") }))).toBe("1 environment variable");
    expect(credentialSummary(server({ env: pairs("A", "B") }))).toBe("2 environment variables");
    expect(credentialSummary(server({ headers: pairs("A", "B") }))).toBe("2 headers");
  });

  it("skips a blank-named pair, which is the form's own empty row", () => {
    // renderKeyPairList seeds one blank row that collectKeyPairs drops, so a record can carry one.
    expect(credentialSummary(server({ headers: [{ name: "  ", value: "" }] }))).toBe(
      "no credentials",
    );
  });

  it("reports an OAuth client from any of its members", () => {
    // The oauth members are not masked, so either present means a configured client.
    expect(credentialSummary(server({ oauth_client_id: "abc" }))).toBe("OAuth client configured");
    expect(
      credentialSummary(server({ oauth_client_metadata_url: "https://example.com/c.json" })),
    ).toBe("OAuth client configured");
    expect(credentialSummary(server({ oauth_client_id: "", oauth_client_metadata_url: "" }))).toBe(
      "no credentials",
    );
  });

  it("reports every kind the record holds rather than picking one", () => {
    // A hand-edited mcp.json can carry both; scoping by transport would hide half.
    expect(
      credentialSummary(
        server({ env: pairs("A"), headers: pairs("B", "C"), oauth_client_id: "x" }),
      ),
    ).toBe("1 environment variable, 2 headers, OAuth client configured");
  });
});

describe("the row's meta line", () => {
  it("puts the credential fact AHEAD of the source", () => {
    // The line ellipsises and the source is its long segment, so the credential fact leads.
    const meta = renderMeta(server({ headers: pairs("Authorization") }), status());
    expect(meta).toBe("1 header · https://ex/mcp");
    expect(meta.indexOf("1 header")).toBeLessThan(meta.indexOf("https://ex/mcp"));
  });

  it("leads with the state a reader has to act on", () => {
    expect(renderMeta(server(), status({ state: "failed", error: "spawn ENOENT" }))).toBe(
      "Failed to start. spawn ENOENT · no credentials · https://ex/mcp",
    );
    expect(renderMeta(server(), status({ state: "needs_auth" } as Partial<RuntimeStatus>))).toBe(
      "Waiting for sign-in · no credentials · https://ex/mcp",
    );
  });

  it("names the failure without a dangling separator when KAS gave no reason", () => {
    // An empty reason would render a bare em dash, so the server substitutes a stand-in.
    expect(renderMeta(server(), status({ state: "failed", error: "" }))).toBe(
      "Failed to start · no credentials · https://ex/mcp",
    );
  });

  it("carries no state phrase for a settled row", () => {
    // The dot already says connected or idle; a phrase would push the rest out of the track.
    expect(renderMeta(server(), status())).toBe("no credentials · https://ex/mcp");
    expect(renderMeta(server(), status({ state: "idle" } as Partial<RuntimeStatus>))).toBe(
      "no credentials · https://ex/mcp",
    );
  });

  it("says only Disabled for a disabled server", () => {
    // Nothing else on the line is true of a server that is not running.
    expect(renderMeta(server({ enabled: false, headers: pairs("Authorization") }), status())).toBe(
      "Disabled",
    );
  });

  it("says a row is not in use when another config defines the running server", () => {
    // KAS runs the workspace's server under this name, so this row's facts describe nothing in use.
    expect(
      renderMeta(
        server({ headers: pairs("Authorization") }),
        status({ origin: "workspace", originRoot: "/workspace/app", shadows: true }),
      ),
    ).toBe("Not in use: the workspace config defines a server with this name");
    expect(
      renderMeta(server(), status({ origin: "power", originPower: "aws-infra", shadows: true })),
    ).toBe("Not in use: the aws-infra Power defines a server with this name");
  });

  it("reads a stdio server's source off its command", () => {
    // No `url` key: `exactOptionalPropertyTypes` refuses an explicit undefined on an optional field.
    const stdio: Server = {
      id: "id-s",
      name: "s",
      transport: "stdio",
      enabled: true,
      command: "npx",
      created_at: 0,
      updated_at: 0,
    };
    expect(renderMeta(stdio, status())).toBe("no credentials · npx");
  });
});
