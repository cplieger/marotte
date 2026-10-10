// The run card's head and step rows under width pressure: no slot paints over another, no slot is
// clipped away, and the head keeps the shared box-header height.
import { beforeAll, afterAll, afterEach, describe, it, expect, vi } from "vitest";
import type * as ScrollModule from "./scroll.js";

// scroll.ts is a self-initialising singleton over a real `#messages`, and the spread below
// evaluates it, so its ids exist before any import of this file is linked.
vi.hoisted(() => {
  for (const [tag, id] of [
    ["div", "messages"],
    ["div", "messages-wrap"],
    ["button", "scroll-bottom"],
  ] as const) {
    if (document.getElementById(id) === null) {
      const host = document.createElement(tag);
      host.id = id;
      document.body.appendChild(host);
    }
  }
});
vi.mock("./scroll.js", async (importOriginal) => ({
  ...(await importOriginal<typeof ScrollModule>()),
  ...(await import("./__test-helpers__/scroll-mock.js")).scrollMock,
}));

import { buildRunCard } from "./fundamentals/run-card.js";
import type { RunAsks } from "./run-asks.js";
import { mountAppCSS } from "./__test-helpers__/css-rules.js";
import type { RunNode, RunState } from "./run-store.js";

/** A run label long enough to squeeze the head at any phone width. */
const LONG_LABEL = "investigate-greenfield-forge-research";

/** CARD widths, so the head's own content box lands where a real viewport puts it: the card's
 *  border and the head's inline padding come off, leaving the widths a 320px and a 390px
 *  viewport give it. WIDE stays inside the runner's viewport. */
const NARROW_PX = 252;
const PHONE_PX = 332;
const WIDE_PX = 1200;

const NO_ASKS: RunAsks = { count: 0, asked: [], label: "" };
const ASKING: RunAsks = {
  count: 1,
  asked: [{ nodeID: "s6", sessionID: "", answer: false }],
  label: "s6",
};
const STARTED = "2026-09-20T06:00:00.000Z";

let style: HTMLStyleElement;
let host: HTMLDivElement;

beforeAll(() => {
  style = mountAppCSS();
  host = document.createElement("div");
  // Fixed, so the card's width is this element's and never the page's flow.
  host.style.cssText = "position:fixed;top:0;left:0;";
  document.body.appendChild(host);
});

afterAll(() => {
  style.remove();
  host.remove();
});

afterEach(() => {
  // Browser Mode isolates per file, not per test, and nothing clears the body.
  host.replaceChildren();
  document.documentElement.removeAttribute("data-pointer");
});

function step(nodeId: string, status: RunNode["status"], startedAt?: string): RunNode {
  const node: RunNode = { nodeId, type: "step", status };
  if (startedAt !== undefined) {
    node.startedAt = startedAt;
  }
  return node;
}

function liveRun(): RunState {
  const children: RunNode[] = [];
  for (let i = 0; i < 10; i++) {
    children.push(
      i < 6
        ? step(`s${String(i)}`, "completed", STARTED)
        : i === 6
          ? step(`s${String(i)}`, "running", STARTED)
          : step(`s${String(i)}`, "pending"),
    );
  }
  return {
    workflowId: "wf_1",
    runLabel: LONG_LABEL,
    status: "running",
    root: { nodeId: "wf_1", type: "sequence", status: "running", children },
  };
}

function longSteps(): RunState {
  return {
    workflowId: "wf_1",
    runLabel: LONG_LABEL,
    status: "running",
    root: {
      nodeId: "wf_1",
      type: "sequence",
      status: "running",
      children: [
        step("requirements", "completed", STARTED),
        step("semantic-review-multi-model", "running", STARTED),
      ],
    },
  };
}

function mount(widthPx: number, state: RunState, asks: RunAsks = NO_ASKS): HTMLElement {
  host.style.inlineSize = `${String(widthPx)}px`;
  const view = buildRunCard("wf_1", LONG_LABEL, () => {
    /* the footer link is not under test */
  });
  host.appendChild(view.root);
  view.render(state, asks);
  return view.root;
}

function el(root: HTMLElement, sel: string): HTMLElement {
  const found = root.querySelector<HTMLElement>(sel);
  if (found === null) {
    throw new Error(`missing ${sel}`);
  }
  return found;
}

/** The head's content box, which is what its two slots have to fit inside. */
function headContentRight(root: HTMLElement): number {
  const head = el(root, ".run-head");
  return head.getBoundingClientRect().right - parseFloat(getComputedStyle(head).paddingRight);
}

const WIDTHS = [
  ["a 320px phone", NARROW_PX],
  ["a 390px phone", PHONE_PX],
  ["a desktop window", WIDE_PX],
] as const;

describe("the run card's head under width pressure", () => {
  for (const [label, widthPx] of WIDTHS) {
    it(`never paints the step counter over the name on ${label}`, () => {
      const root = mount(widthPx, liveRun());
      // The name's BOX, which is what the ellipsis clips to, rather than its text.
      expect(el(root, ".run-count").getBoundingClientRect().left).toBeGreaterThanOrEqual(
        el(root, ".run-name").getBoundingClientRect().right,
      );
    });

    it(`keeps the step counter inside the head on ${label}`, () => {
      const root = mount(widthPx, liveRun());
      expect(el(root, ".run-count").getBoundingClientRect().right).toBeLessThanOrEqual(
        Math.ceil(headContentRight(root)),
      );
    });
  }

  it("leaves the name room to name the run on a phone", () => {
    const root = mount(PHONE_PX, liveRun());
    // The card's identity, and no other row of it carries the name.
    expect(el(root, ".run-name").getBoundingClientRect().width).toBeGreaterThan(150);
  });
});

describe("the head against the foot", () => {
  it("leaves the run's word and its elapsed to the foot", () => {
    const root = mount(WIDE_PX, liveRun());
    const foot = el(root, ".run-ledger").textContent ?? "";
    expect(foot).toContain("running");
    expect(foot).toMatch(/\d+s|\d+m|\d+h/);
    // Even with the width to spare, the head does not restate either of them.
    expect(el(root, ".run-head").textContent).not.toContain("running");
  });

  it("keeps the run's word in the head's accessible name", () => {
    const root = mount(PHONE_PX, liveRun(), ASKING);
    // The head's only statement of the state, and the one an ask reaches: the foot says the run's
    // own status, which stays `running` while a step's ask blocks it.
    expect(el(root, ".run-head").getAttribute("aria-label")).toContain("needs input");
  });
});

describe("a step row under the same pressure", () => {
  it("keeps every step's duration inside the row on a 320px phone", () => {
    const root = mount(NARROW_PX, longSteps());
    const rows = [...root.querySelectorAll<HTMLElement>(".run-step")];
    expect(rows).toHaveLength(2);
    for (const row of rows) {
      const head = el(row, ".run-step-head");
      const pad = parseFloat(getComputedStyle(head).paddingRight);
      const contentRight = head.getBoundingClientRect().right - pad;
      expect(el(row, ".run-step-dur").getBoundingClientRect().right).toBeLessThanOrEqual(
        Math.ceil(contentRight),
      );
      expect(head.scrollWidth - head.clientWidth).toBe(0);
    }
  });

  it("shows a step name in full where the row has room for it", () => {
    const root = mount(WIDE_PX, longSteps());
    const name = el(root, ".run-step-name");
    expect(name.scrollWidth).toBeLessThanOrEqual(Math.ceil(name.clientWidth));
  });
});
