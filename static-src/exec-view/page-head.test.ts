// The exec page's header row under width pressure: the identity, the meta and the run's controls
// all stay on screen, and a desktop window keeps them on one row.
import { afterAll, afterEach, beforeAll, describe, expect, it } from "vitest";
import { mountAppCSS } from "../__test-helpers__/css-rules.js";
import { buildExecPage } from "./page.js";
import type { ExecNode, ExecRun } from "./model.js";

const LONG_LABEL = "forge-transient-selfseal-fix";
const STARTED = "2026-09-20T06:00:00.000Z";

let style: HTMLStyleElement;
let host: HTMLDivElement;

beforeAll(() => {
  style = mountAppCSS();
  host = document.createElement("div");
  host.style.cssText = "position:fixed;top:0;left:0;";
  document.body.appendChild(host);
});

afterAll(() => {
  style.remove();
  host.remove();
});

afterEach(() => {
  host.replaceChildren();
});

function pausedRun(): ExecRun {
  const nodes: ExecNode[] = [];
  for (let i = 0; i < 22; i++) {
    nodes.push({
      path: `s${String(i)}`,
      label: `step-${String(i)}`,
      kind: "step",
      state: i < 20 ? "ok" : "pending",
      children: [],
      start: STARTED,
      end: STARTED,
    });
  }
  return { id: "wf_1", label: LONG_LABEL, state: "waiting", nodes, live: false };
}

/** The widest row the run tab offers: a paused loop's count, its two verbs and Cancel. */
function controlsRow(): HTMLElement {
  const row = document.createElement("div");
  row.className = "run-controls";
  const count = document.createElement("input");
  count.type = "number";
  count.className = "tool-form-input run-extend-count";
  count.value = "1";
  row.append(count);
  for (const [text, cls] of [
    ["Add iterations", "btn btn-sm"],
    ["End this loop", "btn btn-sm"],
    ["Cancel", "btn btn-sm btn-danger"],
  ] as const) {
    const btn = document.createElement("button");
    btn.type = "button";
    btn.className = cls;
    btn.textContent = text;
    row.append(btn);
  }
  return row;
}

function mount(widthPx: number): HTMLElement {
  host.style.inlineSize = `${String(widthPx)}px`;
  const row = controlsRow();
  const view = buildExecPage({ emptyNote: () => "", controls: () => row });
  host.appendChild(view.root);
  view.render(pausedRun());
  view.dispose();
  return view.root;
}

function q(root: HTMLElement, sel: string): HTMLElement {
  const found = root.querySelector<HTMLElement>(sel);
  if (found === null) {
    throw new Error(`missing ${sel}`);
  }
  return found;
}

/** The page widths a 320px and a 390px phone leave once `.page-content` takes its inset. */
const PHONES = [
  ["a 320px phone", 288],
  ["a 390px phone", 358],
] as const;

describe("the exec page header on a phone", () => {
  for (const [label, widthPx] of PHONES) {
    it(`keeps every control inside the header on ${label}`, () => {
      const root = mount(widthPx);
      const head = q(root, ".ev-head").getBoundingClientRect();
      const controls = [
        ...root.querySelectorAll<HTMLElement>(".ev-h-actions input, .ev-h-actions button"),
      ];
      expect(controls).toHaveLength(4);
      for (const c of controls) {
        const r = c.getBoundingClientRect();
        expect(r.left, c.textContent || c.className).toBeGreaterThanOrEqual(Math.floor(head.left));
        expect(r.right, c.textContent || c.className).toBeLessThanOrEqual(Math.ceil(head.right));
      }
    });

    it(`leaves the run's name room to be read on ${label}`, () => {
      const root = mount(widthPx);
      expect(q(root, ".ev-name").getBoundingClientRect().width).toBeGreaterThan(150);
    });

    it(`keeps the state, progress and clock inside the header on ${label}`, () => {
      const root = mount(widthPx);
      const head = q(root, ".ev-head").getBoundingClientRect();
      expect(q(root, ".ev-h-meta").getBoundingClientRect().right).toBeLessThanOrEqual(
        Math.ceil(head.right),
      );
    });
  }
});

describe("the exec page header on a desktop window", () => {
  it("keeps the name, the meta and the controls on one row", () => {
    const root = mount(1200);
    const mid = (sel: string): number => {
      const r = q(root, sel).getBoundingClientRect();
      return r.top + r.height / 2;
    };
    expect(mid(".ev-h-meta")).toBeCloseTo(mid(".ev-name"), -0.5);
    expect(mid(".ev-h-actions")).toBeCloseTo(mid(".ev-name"), -0.5);
  });
});
