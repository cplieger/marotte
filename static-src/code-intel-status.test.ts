// Settings > General > Code intelligence: the status row over the Tools inventory, on the shipped markup.
import { beforeEach, describe, expect, it, vi } from "vitest";

import indexHtml from "../static/index.html?raw";
import type * as Tools from "./tools.js";
import type { Inventory, ToolInfo } from "./types.js";

const mocks = vi.hoisted(() => ({
  listeners: [] as ((inv: Inventory | null) => void)[],
  refresh: vi.fn(),
  show: vi.fn(() => Promise.resolve()),
}));

vi.mock("./tools.js", async (importOriginal) => ({
  ...(await importOriginal<typeof Tools>()),
  onToolInventory: (fn: (inv: Inventory | null) => void) => {
    mocks.listeners.push(fn);
    return () => undefined;
  },
  refreshToolInventory: mocks.refresh,
  showLanguageServers: mocks.show,
}));

import { initCodeIntelStatus, loadCodeIntelStatus } from "./code-intel-status.js";

const page = new DOMParser().parseFromString(indexHtml, "text/html");

function mountRow(): void {
  const row = page.getElementById("code-intel-action")?.closest(".section-option");
  if (row === null || row === undefined) {
    throw new Error("index.html has no Code intelligence row");
  }
  document.body.replaceChildren(document.importNode(row, true));
}

function server(name: string, overrides: Partial<ToolInfo> = {}): ToolInfo {
  return { name, source: "aqua:o/r", installed: true, installing: false, lsp: true, ...overrides };
}

function deliver(inv: Inventory | null): void {
  for (const fn of mocks.listeners) {
    fn(inv);
  }
}

const status = (): string => document.getElementById("code-intel-status")?.textContent ?? "";
const action = (): HTMLButtonElement =>
  document.getElementById("code-intel-action") as HTMLButtonElement;

beforeEach(() => {
  vi.clearAllMocks();
  mocks.listeners.length = 0;
  mountRow();
  initCodeIntelStatus();
});

describe("Code intelligence status row", () => {
  it("reads off with Install while no language server is installed and switched on", () => {
    deliver({
      tools: [
        server("gopls", { installed: false, disabled: true }),
        server("pyright", { installed: false, last_error: "download failed" }),
        { name: "shellcheck", source: "aqua:o/r", installed: true, installing: false },
      ],
      system: [],
    });
    expect(status()).toBe("Off. Install a language server to turn it on.");
    expect(action().textContent).toBe("Install");
    expect(action().getAttribute("aria-label")).toBe("Install a language server");
  });

  it("names each language with an installed, switched-on server, with Manage", () => {
    deliver({
      tools: [
        server("typescript-language-server"),
        server("gopls"),
        server("rust-analyzer", { installed: false, disabled: true }),
      ],
      system: [],
    });
    expect(status()).toBe("On for Go and TypeScript/JavaScript.");
    expect(action().textContent).toBe("Manage");
    expect(action().getAttribute("aria-label")).toBe("Manage language servers");
  });

  it("drops a server switched off while its uninstall is still queued", () => {
    deliver({
      tools: [server("gopls"), server("pyright", { installed: true, disabled: true })],
      system: [],
    });
    expect(status()).toBe("On for Go.");
  });

  it("names a language server it has no language for by its tool name", () => {
    deliver({ tools: [server("solargraph")], system: [] });
    expect(status()).toBe("On for solargraph.");
  });

  it("says the read failed rather than reading off", () => {
    deliver(null);
    expect(status()).toBe("Could not read the installed tools. Settings > Tools says why.");
    expect(action().textContent).toBe("Manage");
  });

  it("opens Settings > Tools narrowed to language servers", () => {
    action().click();
    expect(mocks.show).toHaveBeenCalledTimes(1);
  });

  it("reads the inventory when the General panel loads", () => {
    loadCodeIntelStatus();
    expect(mocks.refresh).toHaveBeenCalledTimes(1);
  });
});
