import { describe, it, expect, afterEach } from "vitest";
import { buildExecPage, type ExecPageView } from "./page.js";
import type { ExecNode, ExecRun } from "./model.js";
import { syncScrollableTables, tableObservationCount } from "../table-scroll.js";

const TABLE = "| a | b |\n|---|---|\n| 1 | 2 |\n";

function step(id: string, output?: string): ExecNode {
  return {
    path: `root/${id}`,
    label: id,
    kind: "step",
    state: "ok",
    children: [],
    ...(output === undefined ? {} : { output }),
  };
}

function finished(nodes: ExecNode[]): ExecRun {
  return { id: "wf_1", label: "release", state: "ok", nodes, live: false };
}

// Every table the results region rendered is watched by the shared observer, whose own report of a
// removed table is not delivered on every engine. These tests read the count with no frame awaited,
// so only the page's own release can lower it.
describe.each([
  ["undocked", false],
  ["docked", true],
])("step results tables, %s page", (_name, docked) => {
  const hosts: HTMLElement[] = [];
  let page: ExecPageView | undefined;

  afterEach(() => {
    page?.dispose();
    page?.root.remove();
    for (const h of hosts.splice(0)) {
      h.remove();
    }
  });

  function mount(): { page: ExecPageView; host: HTMLElement } {
    const host = document.createElement("div");
    page = buildExecPage({
      emptyNote: () => "",
      onShowNode: () => undefined,
      ...(docked ? { dock: host } : {}),
      follow: "newest",
    });
    document.body.append(page.root, host);
    hosts.push(host);
    return { page, host };
  }

  function select(path: string): void {
    document.querySelector<HTMLElement>(`.ev-row[data-path="${path}"] > .ev-row-main`)?.click();
  }

  it("lets go of the previous step's tables when another step is selected", () => {
    const { page: p } = mount();
    const before = tableObservationCount();
    p.render(
      finished([
        step("plan", `${TABLE}\n${TABLE}`),
        step("build", TABLE),
        step("ship", "no table"),
        step("idle"),
      ]),
    );

    select("root/plan");
    expect(tableObservationCount() - before, "plan's two tables").toBe(2);
    select("root/build");
    expect(tableObservationCount() - before, "build's one table").toBe(1);
    select("root/ship");
    expect(tableObservationCount() - before, "ship has none").toBe(0);
    select("root/build");
    expect(tableObservationCount() - before, "build again").toBe(1);
    select("root/idle");
    expect(tableObservationCount() - before, "idle captured nothing").toBe(0);
  });

  it("lets go of the shown tables when the page is disposed", () => {
    const { page: p } = mount();
    const before = tableObservationCount();
    p.render(finished([step("plan", TABLE)]));
    select("root/plan");
    expect(tableObservationCount() - before, "shown").toBe(1);

    p.dispose();

    expect(tableObservationCount() - before).toBe(0);
  });

  it("lets go of the detail pane's transcript tables when the page is disposed", () => {
    const { page: p } = mount();
    const before = tableObservationCount();
    p.render(finished([step("plan")]));
    // A step's transcript renders into its `bodyFor` host, which lives under the page for its life.
    const host = p.bodyFor("root/plan");
    host.append(document.createElement("table"));
    syncScrollableTables(host);
    expect(tableObservationCount() - before, "the transcript's table").toBe(1);

    p.dispose();

    expect(tableObservationCount() - before).toBe(0);
  });
});
