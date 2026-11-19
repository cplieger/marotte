import { describe, it, expect, vi, beforeEach } from "vitest";
import fc from "fast-check";
import indexHtml from "../static/index.html?raw";

// `tools.ts` reaches the editor openers and their whole graph; cut at that edge, since no case opens a file.
vi.mock("./editor-openers.js", () => ({
  openFile: vi.fn(),
  openConfigFile: vi.fn(),
  openFileDiff: vi.fn(),
  openFileInBackground: vi.fn(),
  openFileGitDiff: vi.fn(),
  fetchGitDiffSources: vi.fn(),
  activateFile: vi.fn(),
  refreshEditorFile: vi.fn(),
  closeEditorFile: vi.fn(),
}));
// `tools.ts` also reaches the tab store; cut there too, since no case navigates.
vi.mock("./tabs.js", async () => (await import("./__test-helpers__/tabs-mock.js")).tabsMock());
// The real lookups, so the remote form below runs on the production markup; `$` stays a stub for the modal handle.
vi.mock(import("./dom.js"), async (importOriginal) => ({
  ...(await importOriginal()),
  $: new Proxy({}, { get: () => document.createElement("div") }) as never,
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
// vi.hoisted: a vi.mock factory is hoisted above top-level consts.
const { provider, saveDispatch } = vi.hoisted(() => ({
  provider: { set: false },
  saveDispatch: vi.fn((_args: { id: string; body: Record<string, unknown> }) => ({
    outcome: Promise.resolve({ status: "success" }),
  })),
}));
vi.mock("./mcp-state.js", () => ({
  discoverySignalFor: () => ({ peek: () => ({ tools: [] }) }),
  identityProviderConfigured: () => provider.set,
  mcpState: {
    refetchServers: async () => {
      /* noop */
    },
    refetchStatus: async () => {
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
  saveServer: { dispatch: saveDispatch },
  importServers: { dispatch: async () => ({}) },
  searchRegistry: { cancel: () => undefined },
}));

import { simplifyName } from "./mcp-panels-search.js";
import { extractNpxPackage, initModal, setEditing, signInChoiceShown } from "./mcp-panels.js";
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
    // No npm package the npm form could prefill, so `editModeFor` opens the raw panel for these records.
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

describe("the remote form's Sign-in choice", () => {
  const remote = (over: Partial<Server> = {}): Server => ({
    id: "id-r",
    name: "linear",
    transport: "http",
    url: "https://mcp.linear.app/mcp",
    enabled: true,
    created_at: 0,
    updated_at: 0,
    ...over,
  });

  function modal(): void {
    const start = indexHtml.indexOf('<div id="mcp-modal"');
    const end = indexHtml.indexOf("<!-- Git output popup -->", start);
    const host = document.createElement("div");
    host.innerHTML = indexHtml.slice(start, end);
    document.body.replaceChildren(host);
  }

  function open(s: Server | null): { row: HTMLElement; select: HTMLSelectElement } {
    setEditing({ id: s?.id ?? "" });
    initModal({ mode: "remote", server: s });
    const select = document.querySelector<HTMLSelectElement>("#mcp-remote-oauth-mode");
    if (select === null) {
      throw new Error("no Sign-in choice on the remote form");
    }
    return { row: select.closest("label") as HTMLElement, select };
  }

  async function save(): Promise<Record<string, unknown>> {
    saveDispatch.mockClear();
    document.querySelector<HTMLFormElement>('form[data-mcp-mode="remote"]')?.requestSubmit();
    await Promise.resolve();
    return saveDispatch.mock.calls[0]?.[0].body ?? {};
  }

  beforeEach(() => {
    provider.set = false;
    modal();
  });

  it("is shown only with a provider, or with a stored organization choice", () => {
    // KAS's enterprise mode only fails without a provider and auto behaves as the browser, so the control does nothing.
    expect(signInChoiceShown(false, undefined)).toBe(false);
    expect(signInChoiceShown(false, "browser")).toBe(false);
    expect(signInChoiceShown(true, undefined)).toBe(true);
    expect(signInChoiceShown(false, "enterprise")).toBe(true);
    expect(signInChoiceShown(false, "auto")).toBe(true);
  });

  it("hides on a server with no provider and the default sign-in", () => {
    expect(open(remote()).row.classList.contains("hidden")).toBe(true);
  });

  it("stays on a server whose stored choice the provider's removal left behind", () => {
    const { row, select } = open(remote({ oauth_mode: "enterprise" }));
    expect(row.classList.contains("hidden")).toBe(false);
    expect(select.value).toBe("enterprise");
  });

  it("sends the chosen organization sign-in", async () => {
    provider.set = true;
    const { row, select } = open(remote());
    expect(row.classList.contains("hidden")).toBe(false);
    select.value = "auto";
    expect(await save()).toMatchObject({ oauth_mode: "auto" });
  });

  it("drops a stored choice set back to the browser default", async () => {
    provider.set = true;
    open(remote({ oauth_mode: "enterprise" })).select.value = "";
    expect(await save()).not.toHaveProperty("oauth_mode");
  });

  it("keeps a stored value it does not show", async () => {
    // Update replaces the whole record, so a hidden control carries what is stored.
    open(remote({ oauth_mode: "browser" }));
    expect(await save()).toMatchObject({ oauth_mode: "browser" });
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
