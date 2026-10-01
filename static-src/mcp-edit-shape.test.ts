// Editing an MCP server must not rewrite the command line it runs.
//
// The edit modal picks its panel from the record's SHAPE, not its transport: the
// npm form can only express `npx -y <pkg>` and saves that whatever it was
// handed, so a `uvx`, `docker` or bare-binary server routed there came back as
// `npx -y <first arg>` — validated, rendered into KAS's config, hot-reloaded,
// and the original gone. Every case below drives the real modal over the real
// markup and asserts the PUT body's `command` and `args` against the record.

import { describe, it, expect, vi, beforeEach } from "vitest";
import indexHtml from "../static/index.html?raw";
import type { Server } from "./mcp-state.js";

// Node's absence would put an install banner in the npm panel; the real probe
// is a network call.
vi.mock("./actions/tools.js", () => ({
  getToolsStatus: { dispatch: async () => ({ npx: true }) },
}));
vi.mock("./tools.js", () => ({ installToolAndWait: async () => ({ ok: true }) }));
// Replaced WHOLE, matching the sibling suites that mock this module: the
// suspended auto-approve list's profile pointer imports `openSetting`, whose real
// body reads `location.search` at module load and pulls in the tab projection, so
// the panels' graph now reaches it. No case here navigates.
vi.mock("./settings-highlight.js", () => ({
  openSetting: (): void => {
    /* noop */
  },
}));

const saved = vi.hoisted(() => ({
  calls: [] as { id: string; body: Partial<Server> }[],
}));

// One export replaced, the rest real: `submitServer`'s dispatch is the wire, and
// the body it hands over is what this file is about.
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
    // `npx -y mcp-remote <url>` is the ordinary shape of a remote bridge, and
    // the npm form emits `["-y", pkg]`, so saving it there drops the URL.
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

    // Unhiding the bar was the rejected fix: it leaves the npm panel reachable
    // and still rewriting, which turns a certainty into a trap.
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
