// The node the run page opens on before the reader picks one: the work that wants attention NOW.
import { afterEach, describe, expect, it } from "vitest";
import { buildExecPage } from "./page.js";
import type { ExecNode, ExecRun } from "./model.js";
import type { ExecState } from "./status.js";

const STARTED = new Date(Date.now() - 60_000).toISOString();

function step(path: string, state: ExecState): ExecNode {
  return { path, label: "a", kind: "step", state, children: [], start: STARTED };
}

/** A loop whose pass 1 `a` failed and whose pass 2 `a` is `last`. */
function loop(last: ExecState): ExecRun {
  const pass = (i: number, kid: ExecNode): ExecNode => ({
    path: `loop:iter-${String(i)}`,
    label: `pass ${String(i + 1)}`,
    kind: "sequence",
    state: kid.state,
    children: [kid],
    pass: i + 1,
  });
  const repeat: ExecNode = {
    path: "loop",
    label: "loop",
    kind: "repeat",
    state: last,
    children: [pass(0, step("loop:iter-0:a", "fail")), pass(1, step("loop:iter-1:a", last))],
  };
  return { id: "wf_1", label: "run", state: "running", nodes: [repeat], live: true };
}

let host: HTMLElement | undefined;

afterEach(() => {
  host?.remove();
  host = undefined;
});

function selectedPath(run: ExecRun): string {
  const view = buildExecPage({ emptyNote: () => "" });
  host = document.createElement("div");
  host.appendChild(view.root);
  document.body.appendChild(host);
  view.render(run);
  view.dispose();
  return host.querySelector<HTMLElement>(".ev-row.ev-selected")?.dataset["path"] ?? "";
}

describe("the page's default selection", () => {
  it("opens on the latest pass's running step, not a failure an earlier pass moved past", () => {
    expect(selectedPath(loop("running"))).toBe("loop:iter-1:a");
  });

  it("opens on the latest pass once the loop recovered", () => {
    expect(selectedPath(loop("ok"))).toBe("loop:iter-1:a");
  });
});
