// The run tab's tree addresses a node by its path key, so ids that spell alike under a slash join
// stay two rows and one selection marks one of them.
import { describe, expect, it, vi } from "vitest";
import { buildExecTree } from "./tree.js";
import type { RunAsks } from "../run-asks.js";
import { runToExec } from "../run-exec-source.js";
import { makeRunState } from "../__test-helpers__/model.js";
import type { RunNode, RunState } from "../run-store.js";

const NO_ASKS: RunAsks = { count: 0, asked: [], label: "" };

function seq(nodeId: string, children: RunNode[]): RunNode {
  return { nodeId, type: "sequence", status: "running", children };
}

function step(nodeId: string): RunNode {
  return { nodeId, type: "step", status: "running" };
}

// `a/b` > `c` and `a` > `b` > `c`: a slash join spells both groups `wf_1/a/b` and both steps
// `wf_1/a/b/c`.
const STATE: RunState = {
  ...makeRunState({ status: "running" }),
  root: seq("wf_1", [seq("a/b", [step("c")]), seq("a", [seq("b", [step("c")])])]),
};

describe("the tree pane's node addresses", () => {
  it("keeps groups and steps whose ids spell alike under a slash join apart", () => {
    const tree = buildExecTree(vi.fn());
    tree.render(runToExec("wf_1", STATE, undefined, NO_ASKS).nodes, "wf_1:a/b:c");

    const rows = [...tree.root.querySelectorAll<HTMLElement>(".ev-row")];
    const paths = rows.map((r) => r.dataset["path"] ?? "");
    expect(new Set(paths).size).toBe(paths.length);
    expect(paths).toEqual(["wf_1:a/b", "wf_1:a/b:c", "wf_1:a", "wf_1:a:b", "wf_1:a:b:c"]);
    const selected = rows.filter((r) => r.getAttribute("aria-selected") === "true");
    expect(selected.map((r) => r.dataset["path"])).toEqual(["wf_1:a/b:c"]);
  });
});
