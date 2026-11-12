// A server's tools, prompts and resources ride its status row and are read reactively by server name; tools are a
// discovery result, not persisted config.

import { describe, it, expect } from "vitest";
import { effect } from "@cplieger/reactive";
import { mcpState, discoverySignalFor } from "./mcp-state.js";
import type { MCPPromptInfo, MCPResourceInfo } from "./wire/types.gen.js";

const prompts: MCPPromptInfo[] = [{ name: "Simple Prompt", description: "no args" }];
const resources: MCPResourceInfo[] = [{ name: "doc", uri: "demo://doc" }];
const tools = ["create_issue", "search_repos"];

let version = 0;

/** Deliver `name`'s status row advertising these, at the next `mcp_status` version. */
function advertise(name: string, t: string[], p: MCPPromptInfo[], r: MCPResourceInfo[]): void {
  version++;
  mcpState.applyStatusFrame(
    name,
    { name, state: "connected", origin: "user", tools: t, prompts: p, resources: r },
    { kind: "mcp_status", ref: "", version: String(version) },
  );
}

describe("discovery signals", () => {
  it("defaults to empty for an unknown server", () => {
    const d = discoverySignalFor("disc-unknown").value;
    expect(d.tools).toEqual([]);
    expect(d.prompts).toEqual([]);
    expect(d.resources).toEqual([]);
  });

  it("stores the tools, prompts and resources a status row carries", () => {
    advertise("disc-a", tools, prompts, resources);
    const d = discoverySignalFor("disc-a").value;
    expect(d.tools).toEqual(tools);
    expect(d.prompts).toHaveLength(1);
    expect(d.prompts[0]?.name).toBe("Simple Prompt");
    expect(d.resources[0]?.uri).toBe("demo://doc");
  });

  it("stores tools even when the server advertises no prompts or resources", () => {
    // Most MCP servers expose tools only; the empty-value fast path must not swallow them.
    advertise("disc-tools-only", tools, [], []);
    expect(discoverySignalFor("disc-tools-only").value.tools).toEqual(tools);
  });

  it("resets to empty when the server advertises nothing", () => {
    advertise("disc-b", tools, prompts, resources);
    advertise("disc-b", [], [], []);
    const d = discoverySignalFor("disc-b").value;
    expect(d.tools).toEqual([]);
    expect(d.prompts).toEqual([]);
    expect(d.resources).toEqual([]);
  });

  it("fires the per-server signal so a subscribed row re-renders", () => {
    let runs = 0;
    const dispose = effect(() => {
      // Reading .value subscribes this effect.
      void discoverySignalFor("disc-c").value.prompts.length;
      runs++;
    });
    const before = runs;
    advertise("disc-c", tools, prompts, resources);
    expect(runs).toBeGreaterThan(before);
    dispose();
  });
});
