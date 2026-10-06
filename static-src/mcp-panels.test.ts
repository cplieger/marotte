import { describe, it, expect, vi } from "vitest";
import fc from "fast-check";

// `tools.ts` reaches the editor openers and their whole graph; cut at that edge, since no case opens a file.
vi.mock("./editor-openers.js", () => ({
  openFile: vi.fn(),
  openFileDiff: vi.fn(),
  openFileInBackground: vi.fn(),
  openFileGitDiff: vi.fn(),
  fetchGitDiffSources: vi.fn(),
  activateFile: vi.fn(),
  refreshFile: vi.fn(),
  closeEditorFile: vi.fn(),
}));
// `tools.ts` also reaches the tab store; cut there too, since no case navigates.
vi.mock("./tabs.js", async () => (await import("./__test-helpers__/tabs-mock.js")).tabsMock());
vi.mock("./dom.js", () => ({
  // Present-but-undefined for real-ESM linking: another module in the graph imports the name.
  byId: undefined,
  $: new Proxy({}, { get: () => document.createElement("div") }),
  el: () => document.createElement("div"),
}));
vi.mock("./api-client.js", () => ({
  apiGet: async () => null,
  // Present-but-inert for real-ESM linking; no case calls them.
  apiGetTyped: vi.fn(),
}));
vi.mock("./modals.js", () => ({
  // Present-but-undefined for real-ESM linking.
  RollingOutput: undefined,
  openModal: undefined,
  closeModal: () => {
    /* noop */
  },
}));
vi.mock("./mcp-state.js", () => ({
  // Present-but-undefined for real-ESM linking.
  discoverySignalFor: undefined,
  mcpState: {
    refetchServers: async () => {
      /* noop */
    },
  },
  configured: [],
  // A function, not undefined: a registry row calls it, and nothing here configures a server.
  configuredServers: () => [],
  SECRET_MASK: "***",
}));
vi.mock("./mcp-pairs.js", () => ({
  renderKeyPairList: () => {
    /* noop */
  },
  appendKeyPair: () => {
    /* noop */
  },
  collectKeyPairs: () => [],
}));
vi.mock(import("./icons.js"), async (importOriginal) => {
  const actual = await importOriginal();
  return { ...actual };
});
vi.mock("./actions/mcp.js", () => ({
  // Present-but-undefined for real-ESM linking.
  MCP_API: undefined,
  validationFieldsOf: undefined,
  registryFailureOf: undefined,
  saveServer: { dispatch: async () => ({}) },
  importServers: { dispatch: async () => ({}) },
  searchRegistry: { cancel: () => undefined },
}));

import { simplifyName } from "./mcp-panels-search.js";
import { extractNpxPackage } from "./mcp-panels.js";
import type { Server } from "./mcp-state.js";

describe("simplifyName", () => {
  const cases: { input: string; expected: string }[] = [
    { input: "@modelcontextprotocol/server-github", expected: "server-github" },
    { input: "my-server", expected: "my-server" },
    { input: "simple", expected: "simple" },
    { input: "@scope/pkg-name", expected: "pkg-name" },
    { input: "has spaces and !chars", expected: "has-spaces-and--chars" },
    { input: "---leading-trailing---", expected: "leading-trailing" },
    { input: "", expected: "server" },
    { input: "!!!@@@", expected: "server" },
    { input: "a".repeat(100), expected: "a".repeat(48) },
    { input: "@scope/" + "x".repeat(60), expected: "x".repeat(48) },
  ];

  for (const { input, expected } of cases) {
    it(`simplifyName(${JSON.stringify(input)}) => ${JSON.stringify(expected)}`, () => {
      expect(simplifyName(input)).toBe(expected);
    });
  }
});

describe("extractNpxPackage", () => {
  const stub = (args: string[], command = "npx"): Server => ({
    id: "x",
    name: "x",
    transport: "stdio",
    enabled: true,
    command,
    args,
    created_at: 0,
    updated_at: 0,
  });

  const cases: { label: string; args: string[]; expected: string }[] = [
    { label: "typical npx args", args: ["-y", "@scope/pkg"], expected: "@scope/pkg" },
    { label: "only package", args: ["my-pkg"], expected: "my-pkg" },
    { label: "--yes flag", args: ["--yes", "pkg"], expected: "pkg" },
    { label: "empty args", args: [], expected: "" },
    { label: "only flags", args: ["-y", "--yes"], expected: "" },
    { label: "whitespace-only arg then package", args: ["  ", "-y", "pkg"], expected: "pkg" },
    // The three refusals prewarm's extractor makes, so both halves answer "what does this run" alike.
    { label: "an unknown flag past the package", args: ["-y", "--force", "pkg"], expected: "" },
    { label: "a path where a package spec belongs", args: ["-y", "./local/dir"], expected: "" },
    { label: "an uppercase package name", args: ["-y", "MyPkg"], expected: "" },
  ];

  for (const { label, args, expected } of cases) {
    it(label, () => {
      expect(extractNpxPackage(stub(args))).toBe(expected);
    });
  }

  for (const command of ["uvx", "docker", "/usr/local/bin/my-mcp", ""]) {
    it(`refuses a ${command === "" ? "commandless" : command} server`, () => {
      expect(extractNpxPackage(stub(["-y", "pkg"], command))).toBe("");
    });
  }

  it("refuses a remote server", () => {
    expect(extractNpxPackage({ ...stub(["-y", "pkg"]), transport: "http" })).toBe("");
  });
});

// Property-based.

describe("simplifyName property", () => {
  const VALID_PATTERN = /^[A-Za-z0-9_-]+$/;

  it("always produces a non-empty string matching /^[A-Za-z0-9_-]+$/ with length ≤ 48", () => {
    fc.assert(
      fc.property(fc.string(), (input) => {
        const result = simplifyName(input);
        expect(result.length).toBeGreaterThan(0);
        expect(result.length).toBeLessThanOrEqual(48);
        expect(result).toMatch(VALID_PATTERN);
      }),
      { numRuns: 1000 },
    );
  });

  it("preserves alphanumeric content from the last path segment", () => {
    fc.assert(
      fc.property(
        fc.string({ minLength: 1 }).filter((s) => {
          // Alphanumeric content that does not simplify to "server" on its own.
          if (!/[A-Za-z0-9]/.test(s)) {
            return false;
          }
          const afterSlash = s.slice(s.lastIndexOf("/") + 1);
          const cleaned = afterSlash
            .replace(/[^A-Za-z0-9_-]/g, "-")
            .replace(/^-+|-+$/g, "")
            .slice(0, 48);
          return cleaned !== "" && cleaned !== "server";
        }),
        (input) => {
          const result = simplifyName(input);
          expect(result).not.toBe("server");
        },
      ),
      { numRuns: 500 },
    );
  });
});
