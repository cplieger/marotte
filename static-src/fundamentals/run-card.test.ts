// The run card's state vocabulary: the per-step GLYPH is the only signal for what a step is
// doing, so every step state has to reach it.
import { vi, describe, it, expect } from "vitest";

import { buildRunCard } from "./run-card.js";
import type { RunAsks } from "../run-asks.js";
import { workNodes } from "../exec-view/model.js";
import { runToExec } from "../run-exec-source.js";
import type { RunNode, RunState } from "../run-store.js";

function step(nodeId: string, status: RunNode["status"]): RunNode {
  return { nodeId, type: "step", status };
}

function runOf(status: NonNullable<RunState["status"]>, ...children: RunNode[]): RunState {
  return {
    workflowId: "wf_1",
    status,
    root: { nodeId: "wf_1", type: "sequence", status: "running", children },
  };
}

function asks(count: number, nodes: string[], label = ""): RunAsks {
  return { count, asked: nodes.map((nodeID) => ({ nodeID, sessionID: "", answer: false })), label };
}

const NO_ASKS: RunAsks = asks(0, []);

function card(): ReturnType<typeof buildRunCard> {
  return buildRunCard("wf_1", "Workflow run", () => {
    /* the footer link is not under test */
  });
}

function rowStates(root: HTMLElement): string[] {
  return [...root.querySelectorAll<HTMLElement>(".run-step")].map((e) => e.dataset["status"] ?? "");
}

/** Each step's mark: `"icon"` for an SVG silhouette (the settled outcomes), else the character
 *  it paints (`""` for a CSS ring). */
function glyphs(root: HTMLElement): string[] {
  return [...root.querySelectorAll<HTMLElement>(".run-step-glyph")].map((e) =>
    e.querySelector("svg") === null ? (e.textContent ?? "") : "icon",
  );
}

function iconMarkup(root: HTMLElement): string[] {
  return [...root.querySelectorAll<HTMLElement>(".run-step-glyph svg")].map((e) => e.outerHTML);
}

/** The word the head's `aria-label` states for this state; the visible word is the FOOT's
 *  (see `ledger`). */
function statusWord(root: HTMLElement): string {
  const label = root.querySelector(".run-head")?.getAttribute("aria-label") ?? "";
  return label.slice(label.lastIndexOf(", ") + 2);
}

function ledger(root: HTMLElement): string {
  return root.querySelector(".run-ledger")?.textContent ?? "";
}

function alertText(root: HTMLElement): string {
  const a = root.querySelector<HTMLElement>(".run-alert");
  return a === null || a.classList.contains("hidden") ? "" : (a.textContent ?? "");
}

describe("the deleted pip row", () => {
  it("builds no spine region at all", () => {
    const c = card();
    c.render(runOf("running", step("a", "running"), step("b", "pending")));
    // Absent, not `.hidden`: a region kept in the DOM can come back with one CSS rule.
    expect(c.root.querySelector(".run-spine")).toBeNull();
    expect(c.root.querySelector(".run-pip")).toBeNull();
  });

  it("leaves the head, alert, body and foot in that order", () => {
    const c = card();
    expect([...c.root.children].map((e) => e.className.split(" ")[0])).toEqual([
      "run-head",
      "run-alert",
      "run-body",
      "run-foot",
    ]);
  });
});

describe("the head against the foot", () => {
  it("renders the run's word and its elapsed in the foot alone", () => {
    const c = card();
    c.render(runOf("running", step("a", "running")));
    // The head is the identity row: its name and its step counter, and nothing the
    // foot below it already says. Absent from the DOM rather than hidden, because a
    // span kept for one CSS rule is a span that comes back.
    expect(c.root.querySelector(".run-state")).toBeNull();
    expect(c.root.querySelector(".run-clock")).toBeNull();
    expect(c.root.querySelector(".run-head-meta")).toBeNull();
    expect(
      [...(c.root.querySelector(".run-head")?.children ?? [])].map((e) => e.className),
    ).toEqual(["run-toggle", "run-icon", "run-name", "run-count"]);
    expect(ledger(c.root)).toContain("running");
  });
});

describe("a background-process watch's result", () => {
  const record = JSON.stringify({
    terminalId: "t1",
    status: "exited",
    exitCode: 1,
    signal: null,
    startedAt: "2026-10-06T00:00:00.000Z",
    outputTail: "ok\nFAIL x\n",
  });

  it("reads how the watch ended and its last output line on the row", () => {
    const c = card();
    c.render(
      runOf("completed", {
        nodeId: "build",
        type: "watch",
        status: "completed",
        capturedOutput: record,
      }),
    );
    expect(c.root.querySelector(".run-step-meta")?.textContent).toBe("exited 1");
    expect(c.root.querySelector(".run-step-capture")?.textContent).toBe("FAIL x");
  });

  it("lists the summary for a watch key and the raw value for a step key", () => {
    const c = card();
    c.render({
      ...runOf(
        "completed",
        { nodeId: "build", type: "watch", status: "completed" },
        { nodeId: "report", type: "step", status: "completed" },
      ),
      capturedOutputs: { build: record, report: '{"done":true}' },
    });
    const vals = [...c.root.querySelectorAll(".run-output-val")].map((e) => e.textContent);
    expect(vals).toEqual(["exited 1", '{"done":true}']);
  });
});

describe("a step's own state", () => {
  it("separates a paused step from a running one", () => {
    const c = card();
    c.render(runOf("paused", step("a", "running"), step("b", "paused")));
    // Folding `paused` onto `running` would claim progress on a step where nothing moves.
    expect(rowStates(c.root)).toEqual(["running", "waiting"]);
    // Neither carries a badge character: CSS draws the ring for both, and MOTION is
    // what separates them.
    expect(glyphs(c.root)).toEqual(["", ""]);
  });

  it("gives every settled state its own mark", () => {
    const c = card();
    c.render(
      runOf(
        "completed",
        step("a", "completed"),
        step("b", "failed"),
        step("c", "aborted"),
        step("d", "skipped"),
        step("e", "pending"),
      ),
    );
    expect(rowStates(c.root)).toEqual(["ok", "fail", "warn", "skipped", "pending"]);
    // The three settled OUTCOMES take a silhouette; `skipped` keeps its en dash
    // (nothing happened) and `pending` is a CSS ring.
    expect(glyphs(c.root)).toEqual(["icon", "icon", "icon", "\u2013", ""]);
    // The shape channel at this surface: three states, three different shapes, so
    // hue is never the only thing separating them (WCAG 1.4.1).
    const marks = iconMarkup(c.root);
    expect(marks).toHaveLength(3);
    expect(new Set(marks).size).toBe(3);
  });

  it("renders an unknown run as unknown and live, never as starting", () => {
    const c = card();
    c.render(runOf("unknown", step("a", "unknown")));
    expect(statusWord(c.root)).toBe("unknown");
    expect(rowStates(c.root)).toEqual(["unknown"]);
    expect(c.root.classList.contains("collapsed")).toBe(false);
  });

  it("names the state in the row's accessible label", () => {
    const c = card();
    c.render(runOf("paused", step("a", "paused")));
    expect(c.root.querySelector(".run-step-head")?.getAttribute("aria-label")).toBe("a, waiting");
  });
});

describe("an unanswered ask", () => {
  it("marks the step the ask names", () => {
    const c = card();
    c.render(runOf("running", step("a", "running"), step("b", "pending")), asks(1, ["a"]));
    expect(rowStates(c.root)).toEqual(["input", "pending"]);
    expect(glyphs(c.root)[0]).toBe("?");
    expect(c.root.querySelector(".run-step-head")?.getAttribute("aria-label")).toBe(
      "a, waiting for your answer",
    );
  });

  it("marks the asker in a loop's latest pass, though node_id is not instance-unique", () => {
    const c = card();
    // A repeat's iterations are separate `iter-N` containers holding the SAME step, so
    // an ask naming `a` matches both; the card shows the latest pass, the one in flight.
    const iter = (n: string, iteration: number, status: RunNode["status"]): RunNode => ({
      nodeId: n,
      type: "sequence",
      status: "completed",
      iteration,
      children: [step("a", status)],
    });
    c.render(
      {
        workflowId: "wf_1",
        status: "running",
        root: {
          nodeId: "wf_1",
          type: "repeat",
          status: "running",
          children: [iter("loop#0", 0, "completed"), iter("loop#1", 1, "running")],
        },
      },
      asks(1, ["a"]),
    );
    expect(rowStates(c.root)).toEqual(["input"]);
  });

  it("takes the head's status word over the run's own", () => {
    const c = card();
    const state = runOf("running", step("a", "running"));
    c.render(state, asks(1, ["a"]));
    // The run genuinely IS running — KAS blocks the asking step's turn and leaves the
    // run's status alone — so `data-status` must not be overwritten, and the second
    // axis is what the rail and the word read.
    expect(statusWord(c.root)).toBe("needs input");
    expect(c.root.dataset["status"]).toBe("running");
    expect(c.root.dataset["asking"]).toBe("true");

    c.render(state, asks(0, []));
    expect(statusWord(c.root)).toBe("running");
    expect(c.root.dataset["asking"]).toBeUndefined();
  });

  it("reports itself in the alert, ahead of the run's own status", () => {
    const c = card();
    c.render(runOf("running", step("a", "running")), asks(1, ["a"], "Run git push"));
    expect(alertText(c.root)).toBe("Waiting for your answer: Run git push");
    expect(c.root.querySelector<HTMLElement>(".run-alert")?.dataset["kind"]).toBe("input");
  });

  it("still says so when the wire could not name a step", () => {
    const c = card();
    // No sub-session in the step registry means no `node_id`, and the run is blocked
    // either way — only the ROW cannot be marked.
    c.render(runOf("running", step("a", "running")), asks(2, []));
    expect(rowStates(c.root)).toEqual(["running"]);
    expect(statusWord(c.root)).toBe("needs input");
    expect(alertText(c.root)).toBe("Waiting for your answer \u00b7 2 asks waiting");
  });

  it("loses to a failed launch, which is the one state with no run behind it", () => {
    const c = card();
    c.render(runOf("running", step("a", "running")), asks(1, ["a"], "Run git push"));
    c.setLaunch("failed", "recipe not found");
    expect(alertText(c.root)).toBe("recipe not found");
    // setLaunch re-renders from what it was last told, so the ask survives the pass
    // rather than being cleared by the omission.
    expect(c.root.dataset["asking"]).toBe("true");
  });

  it("says the user paused it, and that a requested pause has not landed yet", () => {
    const paused: RunState = { ...runOf("paused", step("a", "paused")), stopInitiator: "user" };
    expect(alertText(buildAndRender(paused, NO_ASKS))).toBe("Paused by user");
    const closed: RunState = {
      ...runOf("aborted", step("a", "aborted")),
      stopInitiator: "user",
      stopReason: "tab closed",
    };
    expect(alertText(buildAndRender(closed, NO_ASKS))).toBe("Stopped by user: tab closed");
    const pending: RunState = {
      ...runOf("running", step("a", "running")),
      pausePending: { initiator: "user" },
    };
    expect(alertText(buildAndRender(pending, NO_ASKS))).toBe("Pausing after the current step");
  });

  it("outranks a pause, which is the state a click cannot resolve", () => {
    const state: RunState = {
      ...runOf("paused", step("a", "paused")),
      pauseReason: "retry budget",
    };
    expect(alertText(buildAndRender(state, asks(0, [])))).toBe("Waiting: retry budget");
    expect(alertText(buildAndRender(state, asks(1, ["a"], "Approve")))).toBe(
      "Waiting for your answer: Approve",
    );
  });
});

// A pause reason is KAS's prose shown verbatim, EXCEPT the two literals meaning a person owes an
// answer; this arm covers the question text never having arrived.
describe("a pause that means a step is waiting on a person", () => {
  function pausedWith(reason: string): string {
    return alertText(
      buildAndRender({ ...runOf("paused", step("a", "paused")), pauseReason: reason }, asks(0, [])),
    );
  }

  it("replaces the send_message literal with a sentence about the reader", () => {
    expect(pausedWith("Step requested user input via send_message.")).toBe(
      "A step is waiting for your answer",
    );
  });

  it("replaces the re-park literal too, whose node id sits in the middle", () => {
    // A plain Resume clears the RUN's pause reason and leaves the step node's
    // signal, so the next step execution parks again under this fallback.
    expect(pausedWith("Step 'review' is waiting for user input.")).toBe(
      "A step is waiting for your answer",
    );
    expect(pausedWith("Step 'review' is waiting for the next user message.")).toBe(
      "A step is waiting for your answer",
    );
  });

  // The branch arm, which no reason can reach: the run keeps only the wrapper KAS
  // composes for a parallel, and that same wrapper covers an interruption and a
  // permanent failure — so the sentence has to come from the node's own signal.
  it("recognises a park inside a parallel branch", () => {
    const run: RunState = {
      workflowId: "wf_1",
      status: "paused",
      pauseReason: "Parallel 'phase1' is waiting on branch 'verify'.",
      root: {
        nodeId: "wf_1",
        type: "sequence",
        status: "running",
        children: [
          {
            nodeId: "phase1",
            type: "parallel",
            status: "paused",
            children: [
              { nodeId: "verify", type: "step", status: "paused", completionSignal: "need_input" },
            ],
          },
        ],
      },
    };
    expect(alertText(buildAndRender(run, asks(0, [])))).toBe("A step is waiting for your answer");
  });

  it("quotes any other reason verbatim, because it is not about the reader", () => {
    expect(pausedWith("waiting on a watch condition")).toBe(
      "Waiting: waiting on a watch condition",
    );
    expect(pausedWith("")).toBe("Waiting");
  });

  it("keeps the transient-error code beside the sentence", () => {
    const text = alertText(
      buildAndRender(
        {
          ...runOf("paused", step("a", "paused")),
          pauseReason: "Step requested user input via send_message.",
          pauseDetail: { code: "ThrottlingException" },
        },
        asks(0, []),
      ),
    );
    expect(text).toBe(
      "A step is waiting for your answer \u00b7 after a transient error (Throttli" + "ngException)",
    );
  });

  // An exhausted continuation budget (`pauseDetail.class`, upstream 2.21.1) is not transient;
  // `pauseReason` carries upstream's sentence, so the class states its code only.
  it("does not call an exhausted continuation budget a transient error", () => {
    const text = alertText(
      buildAndRender(
        {
          ...runOf("paused", step("a", "paused")),
          pauseReason: "Step could not be continued after 3 consecutive attempts.",
          pauseDetail: { class: "continuation-exhausted", code: "MaxContinuationAttempts" },
        },
        asks(0, []),
      ),
    );
    expect(text).not.toContain("transient");
    expect(text).toBe(
      "Waiting: Step could not be continued after 3 consecutive attempts. \u00b7 " +
        "(MaxContinuationAttempts)",
    );
  });
});

function buildAndRender(state: RunState, a: RunAsks): HTMLElement {
  const c = card();
  c.render(state, a);
  return c.root;
}

// A step row is a door into `/run/<id>` with that node selected, keyed by `runToExec`'s path, which
// `nodePathSegment` spells as KAS spells a frame path (`iter-<n>`, not the state tree's `<id>#<n>`).
describe("a step row's key is the exec view's own node path", () => {
  // The shape a real completed loop run has: the iteration container carries its
  // own generated id AND its `iteration`, which is what the segment rule reads.
  function loopRun(): RunState {
    return {
      workflowId: "wf_1",
      status: "completed",
      root: {
        nodeId: "wf_1",
        type: "sequence",
        status: "completed",
        children: [
          step("plan", "completed"),
          {
            nodeId: "loop",
            type: "repeat",
            status: "completed",
            children: [
              {
                nodeId: "loop#0",
                type: "sequence",
                status: "completed",
                iteration: 0,
                children: [step("code", "completed"), step("review", "completed")],
              },
            ],
          },
        ],
      },
    };
  }

  const CODE_PATH = "wf_1:loop:iter-0:code";

  function rowPaths(root: HTMLElement): (string | undefined)[] {
    return [...root.querySelectorAll<HTMLElement>(".run-step")].map((e) => e.dataset["node"]);
  }

  it("keys every row by the path the exec view addresses that node by", () => {
    const state = loopRun();
    const c = card();
    c.render(state);
    // The run tab's model over the same state: its paths are what the page
    // navigates and `bodyFor` files a transcript under, so a row that keyed on
    // anything else would open the page on no node at all.
    const execPaths = workNodes(runToExec("wf_1", state, undefined, NO_ASKS).nodes).map(
      (n) => n.path,
    );
    expect(execPaths).toEqual(["wf_1:plan", CODE_PATH, "wf_1:loop:iter-0:review"]);
    expect(rowPaths(c.root)).toEqual(execPaths);
  });

  it("names a row after its last path segment, keeping the loop above it", () => {
    const c = card();
    c.render(loopRun());
    const row = c.root.querySelector<HTMLElement>(`.run-step[data-node="${CODE_PATH}"]`);
    expect(row?.querySelector(".run-step-name")?.textContent).toBe("code");
    expect(row?.dataset["status"]).toBe("ok");
    // A settled ok step's mark is the outcome SVG silhouette, not a ✓ glyph.
    expect(row?.querySelector(".run-step-glyph svg")).not.toBeNull();
  });

  it("shows a loop's latest pass, keyed by that pass's own path", () => {
    // Two passes share a nodeId, so the node PATH is what separates them.
    const c = card();
    c.render({
      workflowId: "wf_1",
      status: "completed",
      root: {
        nodeId: "wf_1",
        type: "repeat",
        status: "completed",
        children: [
          {
            nodeId: "loop#0",
            type: "sequence",
            status: "completed",
            iteration: 0,
            children: [step("work", "completed")],
          },
          {
            nodeId: "loop#1",
            type: "sequence",
            status: "completed",
            iteration: 1,
            children: [step("work", "completed")],
          },
        ],
      },
    });
    expect(rowPaths(c.root)).toEqual(["wf_1:iter-1:work"]);
  });

  it("keeps two steps whose ids spell alike under a slash join in two rows", () => {
    const c = card();
    c.render({
      workflowId: "wf_1",
      status: "completed",
      root: {
        nodeId: "wf_1",
        type: "sequence",
        status: "completed",
        children: [
          {
            nodeId: "a/b",
            type: "sequence",
            status: "completed",
            children: [step("c", "completed")],
          },
          {
            nodeId: "a",
            type: "sequence",
            status: "completed",
            children: [step("b/c", "completed")],
          },
        ],
      },
    });
    expect(rowPaths(c.root)).toEqual(["wf_1:a/b:c", "wf_1:a:b/c"]);
    expect([...c.root.querySelectorAll(".run-step-name")].map((e) => e.textContent)).toEqual([
      "c",
      "b/c",
    ]);
  });

  // A slash join would spell the `a/b` group and the `a` > `b` group alike, and their steps too.
  it("keeps groups whose ids spell alike under a slash join apart, with their steps", () => {
    const c = card();
    c.render(
      runOf(
        "running",
        { nodeId: "a/b", type: "sequence", status: "running", children: [step("c", "running")] },
        {
          nodeId: "a",
          type: "sequence",
          status: "running",
          children: [
            { nodeId: "b", type: "sequence", status: "running", children: [step("c", "pending")] },
          ],
        },
      ),
    );
    const keyed = [...c.root.querySelectorAll<HTMLElement>("[data-node]")].map(
      (e) => e.dataset["node"] ?? "",
    );
    expect(new Set(keyed).size).toBe(keyed.length);
    expect(keyed).toEqual(["wf_1:a/b", "wf_1:a/b:c", "wf_1:a", "wf_1:a:b", "wf_1:a:b:c"]);
    expect(rowStates(c.root)).toEqual(["running", "pending"]);
    const hrefs = [...c.root.querySelectorAll<HTMLAnchorElement>(".run-step-head")].map((a) =>
      a.getAttribute("href"),
    );
    expect(new Set(hrefs).size).toBe(2);
  });
});

// A ROW IS A DOOR, NOT A DISCLOSURE: it opens `/run/<id>` at that node, so the transcript never
// builds every step's blocks (`content-visibility` skips paint, not construction).
describe("a step row is a door into the run tab", () => {
  const opened: [string, string, string | undefined][] = [];

  function doorCard(): ReturnType<typeof buildRunCard> {
    opened.length = 0;
    const c = buildRunCard("wf_1", "Workflow run", (id, label, focusNode) => {
      opened.push([id, label, focusNode]);
    });
    c.render(runOf("running", step("build", "running")));
    return c;
  }

  function head(c: ReturnType<typeof buildRunCard>): HTMLAnchorElement {
    return c.root.querySelector<HTMLAnchorElement>(".run-step-head")!;
  }

  it("makes the row head a real anchor at that STEP's route", () => {
    // A real link (middle-click, copy-link); the node rides as a FRAGMENT because a node path contains
    // `/` and the tab's identity stays `(run, workflowId)`.
    const h = head(doorCard());
    expect(h.tagName).toBe("A");
    expect(h.getAttribute("href")).toBe("/run/wf_1#node=wf_1%3Abuild");
  });

  it("keeps the FOOT link on the run itself, with no fragment", () => {
    // "Open run" means the run, not a step, so it must not inherit a node from
    // whichever row happens to be first.
    const c = doorCard();
    expect(c.root.querySelector<HTMLAnchorElement>(".run-open")?.getAttribute("href")).toBe(
      "/run/wf_1",
    );
  });

  it("points a loop's row at its own pass, not at the shared node id", () => {
    // Two passes share a nodeId, so only the node path separates them.
    const c = card();
    c.render({
      workflowId: "wf_1",
      status: "completed",
      root: {
        nodeId: "wf_1",
        type: "repeat",
        status: "completed",
        children: [
          {
            nodeId: "loop#0",
            type: "sequence",
            status: "completed",
            iteration: 0,
            children: [step("work", "completed")],
          },
          {
            nodeId: "loop#1",
            type: "sequence",
            status: "completed",
            iteration: 1,
            children: [step("work", "completed")],
          },
        ],
      },
    });
    expect(
      [...c.root.querySelectorAll<HTMLAnchorElement>(".run-step-head")].map((a) =>
        a.getAttribute("href"),
      ),
    ).toEqual(["/run/wf_1#node=wf_1%3Aiter-1%3Awork"]);
  });

  it("hosts no step body and carries no disclosure chevron", () => {
    // Absent, not `.hidden` or `content-visibility`: a region kept in the DOM still costs
    // its construction.
    const c = doorCard();
    expect(c.root.querySelector(".run-step-body")).toBeNull();
    expect(c.root.querySelector(".run-step-toggle")).toBeNull();
    expect(c.root.querySelector(".run-step.collapsed")).toBeNull();
  });

  it("leaves no interactive element nested inside another", () => {
    // axe's `nested-interactive` (serious): an `<a>` inside a `role="button"` host
    // fires it, and `aria-hidden` + `tabindex="-1"` does not clear it. So the row's
    // anchor carries no role of its own and its children are spans.
    const h = head(doorCard());
    expect(h.getAttribute("role")).toBeNull();
    expect(h.hasAttribute("tabindex")).toBe(false);
    expect([...h.children].map((e) => e.tagName)).toEqual(["SPAN", "SPAN", "SPAN", "SPAN"]);
    expect(h.querySelector("a, button, [tabindex]")).toBeNull();
  });

  it("opens the run at that node on a plain click", () => {
    const c = doorCard();
    const ev = new MouseEvent("click", { bubbles: true, cancelable: true, button: 0 });
    head(c).dispatchEvent(ev);
    expect(opened).toEqual([["wf_1", "Workflow run", "wf_1:build"]]);
    // The app's own routing took the click, so the browser must not also navigate.
    expect(ev.defaultPrevented).toBe(true);
  });

  it("stands aside for a modified click", () => {
    const c = doorCard();
    const ev = new MouseEvent("click", {
      bubbles: true,
      cancelable: true,
      button: 0,
      ctrlKey: true,
    });
    head(c).dispatchEvent(ev);
    expect(opened).toEqual([]);
    expect(ev.defaultPrevented).toBe(false);
  });

  it("still names the state in the row's accessible label", () => {
    // The ROLE says the row opens something, so the name says what the row IS.
    expect(head(doorCard()).getAttribute("aria-label")).toBe("build, running");
  });
});

// The newest top-level run card renders expanded and folds once superseded (`setSuperseded`,
// since only the dispatcher knows its position). Each fixture supplies a `disclosure`; without
// one the card keeps the `?? true` floor.
describe("the newest run card is expanded, and being superseded folds it", () => {
  const clean = (): RunState => runOf("completed", step("build", "completed"));

  /** A card wired to a disclosure. `reader` is what the registry holds (undefined =
   *  the reader has not decided), `defaultOpen` the newest-element verdict, and
   *  `wrote` records every value the card asked the registry to store. */
  function wired(opts: { reader?: boolean; defaultOpen?: boolean } = {}): {
    c: ReturnType<typeof buildRunCard>;
    wrote: boolean[];
  } {
    const wrote: boolean[] = [];
    const c = buildRunCard(
      "wf_1",
      "Workflow run",
      () => {
        /* the footer link is not under test */
      },
      {
        wasOpen: () => opts.reader,
        defaultOpen: opts.defaultOpen ?? true,
        onOpenChange: (open) => wrote.push(open),
      },
    );
    return { c, wrote };
  }

  const collapsed = (c: ReturnType<typeof buildRunCard>): boolean =>
    c.root.classList.contains("collapsed");
  const aria = (c: ReturnType<typeof buildRunCard>): string | null =>
    c.root.querySelector(".run-head")?.getAttribute("aria-expanded") ?? null;

  it("is born from the policy default, and the reader's own state outranks it", () => {
    // The verdict decides when the reader has not, and having decided turns the auto
    // path off for the card's whole life.
    expect(collapsed(wired({ defaultOpen: false }).c)).toBe(true);
    expect(collapsed(wired({ defaultOpen: true }).c)).toBe(false);

    const { c } = wired({ reader: true, defaultOpen: false });
    expect(collapsed(c)).toBe(false);
    c.render(clean());
    c.setSuperseded(true);
    expect(collapsed(c)).toBe(false);
  });

  it("folds a settled clean run the next block follows, and says so in aria", () => {
    const { c } = wired();
    c.render(clean());
    expect(collapsed(c)).toBe(false);
    expect(aria(c)).toBe("true");

    c.setSuperseded(true);
    expect(collapsed(c)).toBe(true);
    expect(aria(c)).toBe("false");
  });

  it("does not fold a run that is still live", () => {
    const { c } = wired();
    c.render(runOf("running", step("build", "running")));
    c.setSuperseded(true);
    expect(collapsed(c)).toBe(false);
    // The state settling is what releases the refusal for a verdict already given.
    c.render(clean());
    expect(collapsed(c)).toBe(true);
  });

  it("does not fold a card whose state has not been fetched", () => {
    // Not knowing is not the same as finished: the card is built before its first
    // `inspect` lands, and a fold there would hide a run that may be working.
    const { c } = wired();
    c.setSuperseded(true);
    expect(collapsed(c)).toBe(false);
  });

  it("leaves a run card open while it holds an unanswered ask", () => {
    // The head reads "needs input" whatever the run reports, so a fold would hide the waiting steps.
    // Over a SETTLED CLEAN run: the only shape the other refusals let through.
    const { c } = wired();
    c.render(clean(), asks(1, ["build"], "which branch?"));
    c.setSuperseded(true);
    expect(collapsed(c)).toBe(false);

    c.render(clean(), NO_ASKS);
    expect(collapsed(c)).toBe(true);
  });

  it("folds a run the reader stopped, because nobody is waiting on one", () => {
    // No wider than the four refusals: `stateOf` maps these to `warn`, and a "completed only" reading
    // would keep a run the reader stopped expanded forever.
    for (const status of ["cancelled", "aborted"] as const) {
      const { c } = wired();
      c.render(runOf(status, step("build", "completed")));
      expect(collapsed(c)).toBe(false);
      c.setSuperseded(true);
      expect(collapsed(c)).toBe(true);
    }
  });

  it("does not fold a clean run that holds a failed STEP", () => {
    // The run's own status says `completed`, so only the per-step count sees the
    // failure — and a failure is not noise, at either granularity.
    const { c } = wired();
    c.render(runOf("completed", step("build", "completed"), step("test", "failed")));
    c.setSuperseded(true);
    expect(collapsed(c)).toBe(false);
  });

  /** A completed loop: pass 1's `a` failed (an allSettled parallel), pass 2 ran clean. */
  function recoveredLoop(status: NonNullable<RunState["status"]>, last: RunNode): RunState {
    const pass = (i: number, kid: RunNode): RunNode => ({
      nodeId: `loop#${String(i)}`,
      type: "sequence",
      status: "completed",
      iteration: i,
      children: [kid],
    });
    return runOf(status, {
      nodeId: "loop",
      type: "repeat",
      status: "completed",
      children: [
        pass(0, { ...step("a", "failed"), failureReason: "the first try broke" }),
        pass(1, last),
      ],
    });
  }

  it("folds a clean run whose loop recovered from a failure in an earlier pass", () => {
    // The card shows the latest pass only, so a refusal read off an earlier one would hold the card
    // open with no visible row saying why.
    const { c } = wired();
    c.render(recoveredLoop("completed", step("a", "completed")));
    expect(rowStates(c.root)).toEqual(["ok"]);
    c.setSuperseded(true);
    expect(collapsed(c)).toBe(true);
  });

  it("names the failure in the pass on screen when the run fails", () => {
    const { c } = wired();
    c.render(
      recoveredLoop("failed", { ...step("a", "failed"), failureReason: "the second try broke" }),
    );
    expect(alertText(c.root)).toBe("a failed: the second try broke");
  });

  // The card and the run tab name one owner (`failureOwner`), so the two never disagree on why a
  // run failed.
  it("names the later pass's failure on the card and the run tab alike", () => {
    const state = recoveredLoop("failed", {
      ...step("a", "failed"),
      failureReason: "the second try broke",
    });
    const { c } = wired();
    c.render(state);
    expect(alertText(c.root)).toBe("a failed: the second try broke");
    expect(runToExec("wf_1", state, undefined, NO_ASKS).alert?.text).toBe(
      "a failed: the second try broke",
    );
  });

  it("names no failure the loop recovered from, on the card or the run tab", () => {
    const state = recoveredLoop("failed", step("a", "completed"));
    const { c } = wired();
    c.render(state);
    expect(alertText(c.root)).toBe("The run failed");
    expect(runToExec("wf_1", state, undefined, NO_ASKS).alert?.text).toBe("The run failed");
  });

  it("does not fold a failed run, and re-opens one that fails after folding", () => {
    const { c } = wired();
    c.render(runOf("failed", step("build", "failed")));
    c.setSuperseded(true);
    expect(collapsed(c)).toBe(false);

    const later = wired();
    later.c.render(clean());
    later.c.setSuperseded(true);
    expect(collapsed(later.c)).toBe(true);
    later.c.render(runOf("failed", step("build", "failed")));
    expect(collapsed(later.c)).toBe(false);
  });

  it("re-opens a folded card when the LAUNCH turns out to have failed", () => {
    // A failed launch created no run, so `inspect` never reports it and the tool call
    // is the only witness — which can arrive after the card has already folded.
    const { c } = wired();
    c.render(clean());
    c.setSuperseded(true);
    expect(collapsed(c)).toBe(true);
    c.setLaunch("failed", "no such recipe");
    expect(collapsed(c)).toBe(false);
  });

  it("writes the reader's choice and never its own", () => {
    // The registry is the reader's latch, so an auto fold and the failure re-open must
    // leave no entry — otherwise every fold would come back as "the reader closed it".
    const { c, wrote } = wired();
    c.render(clean());
    c.setSuperseded(true);
    c.render(runOf("failed", step("build", "failed")));
    expect(wrote).toEqual([]);

    c.root.querySelector<HTMLElement>(".run-head")?.click();
    expect(wrote).toEqual([false]);
  });
});

// The shape of a real run's `GET /api/runs/{id}` two minutes in: a repeat whose first pass holds a
// running step, a parallel KAS has not expanded yet (`children: []`) and a pending step. The
// parallel's two review steps exist only in the node PLAN.
describe("the card shows the run's structure, and only steps and watches as work", () => {
  const STARTED = new Date(Date.now() - 120_000).toISOString();

  function liveLoop(): RunState {
    return {
      workflowId: "wf_1",
      status: "running",
      root: {
        nodeId: "wf_1",
        type: "sequence",
        status: "running",
        startedAt: STARTED,
        children: [
          {
            nodeId: "build-loop",
            type: "repeat",
            status: "running",
            startedAt: STARTED,
            children: [
              {
                nodeId: "build-loop#0",
                type: "sequence",
                status: "running",
                iteration: 0,
                startedAt: STARTED,
                children: [
                  {
                    nodeId: "code",
                    type: "step",
                    status: "running",
                    agentName: "wf-coder",
                    modelId: "claude-opus-5.5",
                    iteration: 0,
                    startedAt: STARTED,
                  },
                  { nodeId: "reviews", type: "parallel", status: "pending", children: [] },
                  {
                    nodeId: "aggregate",
                    type: "step",
                    status: "pending",
                    agentName: "aggregator",
                  },
                ],
              },
            ],
          },
        ],
      },
    };
  }

  /** The same run's first read: the repeat has started and its first pass does not exist yet. */
  function loopStarting(): RunState {
    const s = liveLoop();
    const loop = s.root?.children?.[0];
    if (loop !== undefined) {
      delete loop.children;
    }
    return s;
  }

  const PLAN = [
    {
      nodeId: "build-loop",
      type: "repeat",
      maxIterations: 3,
      steps: [
        { nodeId: "code", type: "step", agentName: "wf-coder", modelId: "claude-opus-5.5" },
        {
          nodeId: "reviews",
          type: "parallel",
          branches: [
            { nodeId: "review-a", type: "step", agentName: "reviewer-a" },
            { nodeId: "review-b", type: "step", agentName: "reviewer-b" },
          ],
        },
        { nodeId: "aggregate", type: "step", agentName: "aggregator" },
      ],
    },
  ];

  /** Every row and group in DOCUMENT order, as `<kind>:<name>`. */
  function outline(root: HTMLElement): string[] {
    return [...root.querySelectorAll<HTMLElement>(".run-step, .run-group")].map((e) =>
      e.classList.contains("run-group")
        ? `group:${e.querySelector(".run-group-name")?.textContent ?? ""}`
        : `step:${e.querySelector(".run-step-name")?.textContent ?? ""}`,
    );
  }

  function spinning(root: HTMLElement): string[] {
    return [...root.querySelectorAll<HTMLElement>('[data-status="running"]')]
      .filter((e) => e !== root)
      .map((e) => e.querySelector(".run-step-name")?.textContent ?? e.className);
  }

  it("leaves only the running step spinning once the loop's first pass opens", () => {
    const c = card();
    c.render(loopStarting(), NO_ASKS, { plan: PLAN });
    c.render(liveLoop(), NO_ASKS, { plan: PLAN });
    expect(spinning(c.root)).toEqual(["code"]);
    expect(outline(c.root)).not.toContain("step:build-loop");
  });

  it("retires the last pass's rows and groups when the next pass opens", () => {
    const c = card();
    c.render(liveLoop(), NO_ASKS, { plan: PLAN });
    const next = liveLoop();
    const loop = next.root?.children?.[0];
    const first = loop?.children?.[0];
    if (loop?.children === undefined || first?.children === undefined) {
      throw new Error("fixture lost its first pass");
    }
    const settle = (n: RunNode): RunNode => ({
      ...n,
      status: "completed",
      ...(n.children === undefined ? {} : { children: n.children.map(settle) }),
    });
    const pass2 = structuredClone(first);
    pass2.nodeId = "build-loop#1";
    pass2.iteration = 1;
    loop.children = [settle(first), pass2];

    c.render(next, NO_ASKS, { plan: PLAN });

    const keyed = [...c.root.querySelectorAll<HTMLElement>("[data-node]")].map(
      (e) => e.dataset["node"] ?? "",
    );
    expect(keyed.filter((p) => p.includes("iter-0"))).toEqual([]);
    expect(keyed).toEqual([
      "wf_1:build-loop",
      "wf_1:build-loop:iter-1:code",
      "wf_1:build-loop:iter-1:reviews",
      "wf_1:build-loop:iter-1:reviews:review-a",
      "wf_1:build-loop:iter-1:reviews:review-b",
      "wf_1:build-loop:iter-1:aggregate",
    ]);
    expect(spinning(c.root)).toEqual(["code"]);
    expect(
      c.root.querySelector('.run-group[data-kind="repeat"] .run-group-meta')?.textContent,
    ).toBe("pass 2 of 3");
  });

  // A re-insert of an attached node drops its focus and restarts its animation, so a row that
  // survives a render must never move while a departed sibling still sits ahead of it.
  it("keeps a surviving row seated, and focused, when an earlier sibling leaves", () => {
    const c = card();
    document.body.append(c.root);
    try {
      c.render(runOf("running", step("a", "completed"), step("b", "running")));
      const b = c.root.querySelector<HTMLElement>('.run-step[data-node="wf_1:b"]');
      const head = b?.querySelector<HTMLElement>(".run-step-head");
      head?.focus();
      expect(document.activeElement).toBe(head);

      c.render(runOf("running", step("b", "running")));

      expect(c.root.querySelector('.run-step[data-node="wf_1:b"]')).toBe(b);
      expect(document.activeElement).toBe(head);
    } finally {
      c.root.remove();
    }
  });

  it("keeps a nested running row's animation running when a sibling ahead of it leaves", async () => {
    const c = card();
    document.body.append(c.root);
    // A real CSS animation on the glyph, standing in for the stylesheet's `vk-spin` ring.
    const style = document.createElement("style");
    style.textContent =
      "@keyframes card-spin { to { rotate: 360deg } } .run-step-glyph { animation: card-spin 600ms linear infinite }";
    document.head.appendChild(style);
    try {
      const first = liveLoop();
      const pass = first.root?.children?.[0]?.children?.[0];
      if (pass?.children === undefined) {
        throw new Error("fixture lost its first pass");
      }
      pass.children = [{ nodeId: "plan", type: "step", status: "completed" }, ...pass.children];
      c.render(first, NO_ASKS, { plan: PLAN });
      const glyphOf = (): HTMLElement | null =>
        c.root.querySelector<HTMLElement>(
          '.run-step[data-node="wf_1:build-loop:iter-0:code"] .run-step-glyph',
        );
      const anim = glyphOf()?.getAnimations()[0];
      expect(anim, "the row needs an animation to probe").toBeDefined();
      anim?.pause();
      if (anim !== undefined) {
        anim.currentTime = 250;
      }

      expect(
        c.root.querySelector('.run-step[data-node="wf_1:build-loop:iter-0:plan"]'),
        "the departing sibling must render ahead of the probe",
      ).not.toBeNull();
      c.render(liveLoop(), NO_ASKS, { plan: PLAN });
      await new Promise((r) => requestAnimationFrame(r));

      const after = glyphOf()?.getAnimations()[0];
      expect(Number(after?.currentTime), "a render must not restart the ring").toBe(250);
      expect(after?.playState).toBe("paused");
    } finally {
      style.remove();
      c.root.remove();
    }
  });

  it("swaps the shape when a replanned run puts a container where a step was, and back", () => {
    const c = card();
    c.render(runOf("running", step("x", "running")));
    c.render(
      runOf("running", {
        nodeId: "x",
        type: "parallel",
        status: "running",
        children: [step("y", "running")],
      }),
    );
    expect(outline(c.root)).toEqual(["group:x", "step:y"]);
    c.render(runOf("running", step("x", "running")));
    expect(outline(c.root)).toEqual(["step:x"]);
  });

  it("advances a running row's clock on tick, inside a group too", () => {
    vi.useFakeTimers({ toFake: ["Date"] });
    try {
      vi.setSystemTime(new Date("2026-09-20T06:01:00.000Z"));
      const c = card();
      c.render(
        runOf("running", {
          nodeId: "g",
          type: "parallel",
          status: "running",
          children: [{ ...step("y", "running"), startedAt: "2026-09-20T06:00:00.000Z" }],
        }),
      );
      const dur = (): string => c.root.querySelector(".run-step-dur")?.textContent ?? "";
      const before = dur();
      vi.setSystemTime(new Date("2026-09-20T06:03:00.000Z"));
      c.tick();
      expect(dur()).not.toBe(before);
      expect(dur()).toMatch(/^3m/);
    } finally {
      vi.useRealTimers();
    }
  });

  it("renders a repeat as a group carrying its pass, AROUND its children in document order", () => {
    const c = card();
    c.render(liveLoop(), NO_ASKS, { plan: PLAN });
    expect(outline(c.root)).toEqual([
      "group:build-loop",
      "step:code",
      "group:reviews",
      "step:review-a",
      "step:review-b",
      "step:aggregate",
    ]);
    const loop = c.root.querySelector<HTMLElement>('.run-group[data-kind="repeat"]');
    expect(loop?.querySelector(".run-group-meta")?.textContent).toBe("pass 1 of 3");
    // The pass belongs to the loop, so a step inside it does not restate it.
    const code = c.root.querySelector<HTMLElement>(".run-step");
    expect(code?.querySelector(".run-step-meta")?.textContent).toBe(
      "wf-coder \u00b7 claude-opus-5.5",
    );
    // A container is structure: it carries no state mark and no clock.
    expect(c.root.querySelector(".run-group .run-group-head .run-step-glyph")).toBeNull();
    expect(c.root.querySelector(".run-group-head .run-step-dur")).toBeNull();
  });

  it("counts the steps a reader sees, in the head and the foot alike", () => {
    const c = card();
    c.render(liveLoop(), NO_ASKS, { plan: PLAN });
    expect(c.root.querySelector(".run-count")?.textContent).toBe("step 1 of 4");
    expect(ledger(c.root)).toMatch(/^4 steps \u00b7 running \u00b7 2m/);
  });

  it("keeps the pass-1 counter while the run's first read holds no pass yet", () => {
    const c = card();
    c.render(loopStarting(), NO_ASKS, { plan: PLAN });
    expect(spinning(c.root)).toEqual([]);
    expect(outline(c.root)[0]).toBe("group:build-loop");
    expect(c.root.querySelector(".run-count")?.textContent).toBe("0 of 4");
  });
});
