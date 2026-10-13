// The node path key must equal the server's `workflow.PathKey` byte for byte, or a step's run-log turns
// and transcript file under a key its row never asks for. Both sides read the same fixture.

import { readFileSync } from "node:fs";
import { describe, it, expect } from "vitest";

import { nodePathKey, nodePathSegment } from "./run-node-key.js";
import type { RunNode } from "./run-store.js";

function step(nodeId: string, over: Partial<RunNode> = {}): RunNode {
  return { nodeId, type: "step", status: "pending", ...over };
}

interface NodePathKeyCase {
  name: string;
  path: string[];
  key: string;
}

function nodePathKeyCases(): NodePathKeyCase[] {
  const raw = readFileSync(
    new URL("../internal/workflow/testdata/node_path_key.json", import.meta.url),
    "utf8",
  );
  return (JSON.parse(raw) as { cases: NodePathKeyCase[] }).cases;
}

describe("nodePathKey agrees with the server's workflow.PathKey", () => {
  const cases = nodePathKeyCases();

  it.each(cases)("$name", ({ path, key }) => {
    expect(nodePathKey(path)).toBe(key);
  });

  it("keeps every case's key distinct, the slash-alike pairs included", () => {
    const keys = cases.map((c) => nodePathKey(c.path));
    expect(new Set(keys).size).toBe(cases.length);
  });
});

// KAS describes one node two ways: a repeat's iteration container is `<repeatId>#<n>` in the state
// tree these fixtures reproduce and `iter-<n>` in the `nodePath` it stamps on a step FRAME.
describe("nodePathSegment spells a node the way a step frame addresses it", () => {
  const loop: RunNode = { nodeId: "loop", type: "repeat", status: "running" };

  it("names a repeat's pass by its iteration, so two passes of one id differ", () => {
    const first: RunNode = {
      nodeId: "loop#0",
      type: "sequence",
      status: "completed",
      iteration: 0,
    };
    const second: RunNode = { ...first, nodeId: "loop#1", iteration: 1 };
    expect(nodePathSegment(first, loop)).toBe("iter-0");
    expect(nodePathSegment(second, loop)).toBe("iter-1");
  });

  it("falls back to a repeat child's own id when it carries no iteration", () => {
    // Every iteration container on real runs carries an `iteration`, so this is the unobserved
    // branch: it must degrade to a row in the wrong place rather than to `iter-undefined`, which is
    // the same call the server's own runNodePath makes for a frame with no path.
    expect(nodePathSegment(step("loop#0"), loop)).toBe("loop#0");
  });

  it("leaves a parallel BRANCH spelled as its own id", () => {
    // Real data: a parallel's branches are named `plan-a`…`plan-d` on both sides and match
    // byte-for-byte today. The rule is a repeat's, not every container's.
    const fan: RunNode = { nodeId: "investigate", type: "parallel", status: "running" };
    expect(nodePathSegment(step("plan-a", { branchId: "plan-a" }), fan)).toBe("plan-a");
  });

  it("rewrites a step sitting DIRECTLY under a repeat", () => {
    // The rule keys on the PARENT's type, not on the node being a container.
    expect(nodePathSegment(step("work", { iteration: 2 }), loop)).toBe("iter-2");
  });

  it("uses the bare id at the root", () => {
    expect(nodePathSegment(step("wf"), undefined)).toBe("wf");
  });
});
