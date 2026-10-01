// A configured server's row said nothing about whether the reader had supplied a
// credential: a row with a token and a row with none rendered the same two
// segments (the source, and the source alone). The masked export preserves each
// pair's NAME and the slice LENGTH, so presence was knowable all along and the
// row simply never said it.
//
// Mock shape copied from mcp-origin.test.ts, which is the proven way to import
// mcp-ui.ts: it pulls the whole actions graph in transitively and only the two
// members it calls at module scope need replacing.
import { describe, expect, it, vi } from "vitest";

vi.mock(import("./actions/index.js"), async (importOriginal) => {
  const actual = await importOriginal();
  return {
    ...actual,
    // registerCleanup returns its UNREGISTER function, so a bare `() => {}` is
    // the wrong shape as well as the wrong arity.
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
    // The whole defect: silence here is indistinguishable from an unread row, so
    // the absence has to be stated rather than left to the empty string.
    expect(credentialSummary(server())).toBe("no credentials");
  });

  it("counts a masked pair by its NAME, never its value", () => {
    // A saved secret comes back as the mask, so a value test would report every
    // stored credential as absent — which is the reading this row exists to fix.
    expect(credentialSummary(server({ headers: pairs("Authorization") }))).toBe("1 header");
  });

  it("agrees with itself on singular and plural", () => {
    expect(credentialSummary(server({ env: pairs("A") }))).toBe("1 environment variable");
    expect(credentialSummary(server({ env: pairs("A", "B") }))).toBe("2 environment variables");
    expect(credentialSummary(server({ headers: pairs("A", "B") }))).toBe("2 headers");
  });

  it("skips a blank-named pair, which is the form's own empty row", () => {
    // renderKeyPairList seeds one blank row and collectKeyPairs drops it, so a
    // record can legitimately round-trip through a form holding one.
    expect(credentialSummary(server({ headers: [{ name: "  ", value: "" }] }))).toBe(
      "no credentials",
    );
  });

  it("reports an OAuth client from either half of the pair", () => {
    // The id is not masked at all and a set secret becomes the sentinel, so
    // either one present is evidence the reader configured a client.
    expect(credentialSummary(server({ oauth_client_id: "abc" }))).toBe("OAuth client configured");
    expect(credentialSummary(server({ oauth_client_secret: SECRET_MASK }))).toBe(
      "OAuth client configured",
    );
    expect(credentialSummary(server({ oauth_client_id: "", oauth_client_secret: "" }))).toBe(
      "no credentials",
    );
  });

  it("reports every kind the record holds rather than picking one", () => {
    // A hand-edited mcp.json can carry both, and scoping this by transport would
    // hide half of what is on disk.
    expect(
      credentialSummary(
        server({ env: pairs("A"), headers: pairs("B", "C"), oauth_client_id: "x" }),
      ),
    ).toBe("1 environment variable, 2 headers, OAuth client configured");
  });
});

describe("the row's meta line", () => {
  it("puts the credential fact AHEAD of the source", () => {
    // The line ellipsises and a URL or command is its long segment, so a
    // source-first order clips the credential fact off the tail.
    const meta = renderMeta(server({ headers: pairs("Authorization") }), status());
    expect(meta).toBe("1 header · https://ex/mcp");
    expect(meta.indexOf("1 header")).toBeLessThan(meta.indexOf("https://ex/mcp"));
  });

  it("leads with the state a reader has to act on", () => {
    expect(renderMeta(server(), status({ state: "failed", error: "spawn ENOENT" }))).toBe(
      "Failed to start — spawn ENOENT · no credentials · https://ex/mcp",
    );
    expect(renderMeta(server(), status({ state: "needs_auth" } as Partial<RuntimeStatus>))).toBe(
      "Waiting for sign-in · no credentials · https://ex/mcp",
    );
  });

  it("names the failure without a dangling separator when KAS gave no reason", () => {
    // The reason the server substitutes a stand-in reason: an empty one used to
    // render as a bare em dash with nothing after it.
    expect(renderMeta(server(), status({ state: "failed", error: "" }))).toBe(
      "Failed to start · no credentials · https://ex/mcp",
    );
  });

  it("carries no state phrase for a settled row", () => {
    // The dot already says connected or idle, and a phrase on every row pushes
    // the credential summary and the source out of the ellipsised track.
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

  it("reads a stdio server's source off its command", () => {
    // Built without a `url` key rather than with an undefined one:
    // `exactOptionalPropertyTypes` refuses an explicit undefined on an optional
    // field, and a record carrying both is not a shape the store can hold.
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
