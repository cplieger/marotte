// Editing an MCP server must not rewrite its command line. The modal picks its panel from the record's shape: the npm
// form saves `npx -y <pkg>` whatever it was handed. Each case asserts the PUT body's `command` and `args`.

import { describe, it, expect, vi, beforeEach } from "vitest";
import indexHtml from "../static/index.html?raw";
import type { Server } from "./mcp-state.js";

// Node's absence would put an install banner in the npm panel; the real probe is a network call.
vi.mock("./actions/tools.js", () => ({
  getToolsStatus: { dispatch: async () => ({ npx: true }) },
}));
vi.mock("./tools.js", () => ({ installToolAndWait: async () => ({ ok: true }) }));

const saved = vi.hoisted(() => ({
  calls: [] as { id: string; body: Partial<Server> }[],
}));

// One export replaced: `submitServer`'s dispatch is the wire under test.
vi.mock(import("./actions/mcp.js"), async (importOriginal) => {
  const actual = await importOriginal();
  return {
    ...actual,
    saveServer: {
      dispatch: (args: { id: string; body: Partial<Server> }) => {
        saved.calls.push(args);
        return { outcome: Promise.resolve({ status: "success", value: {} }) };
      },
    },
  } as unknown as typeof actual;
});

function mountModal(): void {
  const start = indexHtml.indexOf('<div id="mcp-modal"');
  const end = indexHtml.indexOf("<!-- Git output popup -->", start);
  expect(start, "#mcp-modal not found").toBeGreaterThan(-1);
  expect(end, "the marker after the MCP modal not found").toBeGreaterThan(start);
  const host = document.createElement("div");
  host.innerHTML = indexHtml.slice(start, end);
  document.body.replaceChildren(...Array.from(host.childNodes));
}

function record(over: Partial<Server>): Server {
  return {
    id: "srv-1",
    name: "example",
    transport: "stdio",
    enabled: true,
    created_at: 1,
    updated_at: 2,
    ...over,
  };
}

interface Shape {
  readonly label: string;
  readonly server: Server;
  readonly mode: "npm" | "raw";
}

const SHAPES: readonly Shape[] = [
  {
    label: "npx",
    server: record({ command: "npx", args: ["-y", "@modelcontextprotocol/server-github"] }),
    mode: "npm",
  },
  {
    label: "uvx",
    server: record({ command: "uvx", args: ["mcp-server-git", "--repository", "/workspace"] }),
    mode: "raw",
  },
  {
    label: "docker",
    server: record({ command: "docker", args: ["run", "--rm", "-i", "ghcr.io/x/y"] }),
    mode: "raw",
  },
  {
    label: "a bare binary",
    server: record({ command: "/usr/local/bin/my-mcp", args: ["--stdio"] }),
    mode: "raw",
  },
];

beforeEach(() => {
  saved.calls.length = 0;
  mountModal();
});

describe("editModeFor", () => {
  for (const { label, server, mode } of SHAPES) {
    it(`routes a ${label} server to the ${mode} panel`, async () => {
      const { editModeFor } = await import("./mcp-panels.js");
      expect(editModeFor(server)).toBe(mode);
    });
  }

  it("routes a remote server to the remote panel", async () => {
    const { editModeFor } = await import("./mcp-panels.js");
    expect(editModeFor(record({ transport: "http", url: "https://example.com/mcp" }))).toBe(
      "remote",
    );
  });

  it("keeps an npx server with an argument past the package off the npm form", async () => {
    const { editModeFor } = await import("./mcp-panels.js");
    // `npx -y mcp-remote <url>` is a remote bridge; the npm form's `["-y", pkg]` would drop the URL.
    expect(
      editModeFor(record({ command: "npx", args: ["-y", "mcp-remote", "https://x/sse"] })),
    ).toBe("raw");
  });
});

describe("an edit round trip", () => {
  for (const { label, server, mode } of SHAPES) {
    it(`preserves a ${label} server's command and args exactly`, async () => {
      const { setEditing, initModal, editModeFor } = await import("./mcp-panels.js");
      setEditing({ id: server.id });
      initModal({ mode: editModeFor(server), server });

      const save = document.getElementById(`mcp-${mode}-save`);
      expect(save, `#mcp-${mode}-save must exist`).not.toBeNull();
      save?.click();

      await vi.waitFor(() => {
        expect(saved.calls).toHaveLength(1);
      });
      expect(saved.calls[0]?.id).toBe(server.id);
      expect(saved.calls[0]?.body.command).toBe(server.command);
      expect(saved.calls[0]?.body.args).toEqual(server.args);
      expect(saved.calls[0]?.body.name).toBe(server.name);
      // The row's switch owns this, so an absent field must not read as off.
      expect(saved.calls[0]?.body.enabled).toBe(true);
    });
  }

  it("hides the tab bar while editing, so the npm form stays unreachable", async () => {
    const { setEditing, initModal, editModeFor } = await import("./mcp-panels.js");
    const server = record({ command: "uvx", args: ["mcp-server-git"] });
    setEditing({ id: server.id });
    initModal({ mode: editModeFor(server), server });

    // Unhiding the bar would leave the npm panel reachable and still rewriting.
    expect(document.getElementById("mcp-modal-tabs")?.classList.contains("hidden")).toBe(true);
  });

  it("seeds the raw box from the record and omits the fields other controls own", async () => {
    const { setEditing, initModal, editModeFor } = await import("./mcp-panels.js");
    const server = record({
      command: "docker",
      args: ["run", "--rm", "-i", "ghcr.io/x/y"],
      disabled_tools: ["dangerous"],
    });
    setEditing({ id: server.id });
    initModal({ mode: editModeFor(server), server });

    const box = document.getElementById("mcp-raw-input") as HTMLTextAreaElement | null;
    const parsed = JSON.parse(box?.value ?? "{}") as Record<string, unknown>;
    expect(parsed["command"]).toBe("docker");
    expect(parsed["args"]).toEqual(server.args);
    expect(Object.keys(parsed)).not.toContain("id");
    expect(Object.keys(parsed)).not.toContain("created_at");
    expect(Object.keys(parsed)).not.toContain("enabled");
    expect(Object.keys(parsed)).not.toContain("disabled_tools");
  });

  it.each([
    {
      mode: "npm",
      server: record({
        command: "npx",
        args: ["-y", "@modelcontextprotocol/server-github"],
        wait_for_ready: true,
        timeout_ms: 120000,
      }),
    },
    {
      mode: "remote",
      server: record({
        transport: "http",
        url: "https://example.com/mcp",
        wait_for_ready: true,
        timeout_ms: 120000,
      }),
    },
  ] as const)(
    "keeps a $mode server's pasted wait_for_ready and timeout_ms across a form save",
    async ({ mode, server }) => {
      const { setEditing, initModal, editModeFor } = await import("./mcp-panels.js");
      expect(editModeFor(server)).toBe(mode);
      setEditing({ id: server.id });
      initModal({ mode, server });

      document.getElementById(`mcp-${mode}-save`)?.click();

      await vi.waitFor(() => {
        expect(saved.calls).toHaveLength(1);
      });
      // Update replaces the whole record, so a body without them clears them.
      expect(saved.calls[0]?.body.wait_for_ready).toBe(true);
      expect(saved.calls[0]?.body.timeout_ms).toBe(120000);
    },
  );

  it("keeps a remote server's oauth metadata URL and redirect across a form save", async () => {
    // No form control edits these two, and Update replaces the whole record.
    const { setEditing, initModal } = await import("./mcp-panels.js");
    const server = record({
      transport: "http",
      url: "https://example.com/mcp",
      oauth_client_metadata_url: "https://example.com/c.json",
      oauth_redirect_uri: "localhost:7778",
    });
    setEditing({ id: server.id });
    initModal({ mode: "remote", server });

    document.getElementById("mcp-remote-save")?.click();

    await vi.waitFor(() => {
      expect(saved.calls).toHaveLength(1);
    });
    expect(saved.calls[0]?.body.oauth_client_metadata_url).toBe("https://example.com/c.json");
    expect(saved.calls[0]?.body.oauth_redirect_uri).toBe("localhost:7778");
  });

  it("shows a stored wait_for_ready and timeout_ms in the raw box", async () => {
    const { setEditing, initModal, editModeFor } = await import("./mcp-panels.js");
    const server = record({
      command: "uvx",
      args: ["mcp-server-git"],
      wait_for_ready: true,
      timeout_ms: 90000,
    });
    setEditing({ id: server.id });
    initModal({ mode: editModeFor(server), server });

    const box = document.getElementById("mcp-raw-input") as HTMLTextAreaElement | null;
    const parsed = JSON.parse(box?.value ?? "{}") as Record<string, unknown>;
    expect(parsed["wait_for_ready"]).toBe(true);
    expect(parsed["timeout_ms"]).toBe(90000);
  });

  it("leaves the paste template in the box when adding", async () => {
    const { setEditing, initModal } = await import("./mcp-panels.js");
    setEditing({ id: "" });
    initModal({ mode: "raw", server: null });

    const box = document.getElementById("mcp-raw-input") as HTMLTextAreaElement | null;
    expect(box?.value).toContain("mcpServers");
    expect(document.querySelector(".mcp-raw-edit-note")).toBeNull();
  });
});

describe("the npm panel's copy", () => {
  it("names Paste JSON for a server that runs another command", async () => {
    const { setEditing, initModal } = await import("./mcp-panels.js");
    setEditing({ id: "" });
    initModal({ mode: "npm", server: null });

    const note = document.querySelector(".mcp-npm-alt");
    expect(note?.textContent).toBe(
      "Paste JSON takes a server that runs any other command: uvx, docker, or a binary of your own.",
    );
  });

  it("adds the note once however often the panel is opened", async () => {
    const { setEditing, initModal } = await import("./mcp-panels.js");
    for (let i = 0; i < 3; i++) {
      setEditing({ id: "" });
      initModal({ mode: "npm", server: null });
    }
    expect(document.querySelectorAll(".mcp-npm-alt")).toHaveLength(1);
  });
});
