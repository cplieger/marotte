// The run tab's tree: a container is structure, so only a work node's mark spins. Measured on the
// real stylesheet, because the rule is a cascade fact a source read cannot settle.
import { describe, it, expect, beforeAll, afterAll } from "vitest";
import { mountAppCSS } from "../__test-helpers__/css-rules.js";
import { buildExecTree } from "./tree.js";
import type { ExecNode } from "./model.js";

let styleEl: HTMLStyleElement;

beforeAll(() => {
  styleEl = mountAppCSS();
});

afterAll(() => {
  styleEl.remove();
});

const code: ExecNode = {
  path: "l:p:code",
  label: "code",
  kind: "step",
  state: "running",
  children: [],
};
const pass: ExecNode = {
  path: "l:p",
  label: "pass 1",
  kind: "sequence",
  state: "running",
  pass: 1,
  children: [code],
};
const loop: ExecNode = {
  path: "l",
  label: "loop",
  kind: "repeat",
  state: "running",
  children: [pass],
};

function slotOf(tree: HTMLElement, path: string): HTMLElement {
  const glyph = tree.querySelector<HTMLElement>(
    `.ev-row[data-path="${path}"] > .ev-row-main > .ev-state`,
  );
  if (glyph === null) {
    throw new Error(`no row at ${path}`);
  }
  return glyph;
}

/** The turn of a slot's running ring, or `undefined` when nothing in it spins. */
function spin(tree: HTMLElement, path: string): string | undefined {
  const anim = slotOf(tree, path)
    .getAnimations({ subtree: true })
    .find((a): a is CSSAnimation => a instanceof CSSAnimation);
  return anim?.animationName;
}

describe("the tree spins for running work, never for a container", () => {
  it("draws no in-flight ring on a running container, and a spinning one on its step", () => {
    const view = buildExecTree(() => {
      /* selection is not under test */
    });
    document.body.appendChild(view.root);
    view.attach(document.body);
    view.render([loop], "");
    try {
      for (const container of ["l", "l:p"]) {
        expect(getComputedStyle(slotOf(view.root, container), "::before").content).toBe("none");
        expect(spin(view.root, container), `${container} spins nothing`).toBeUndefined();
      }
      expect(slotOf(view.root, "l:p:code").querySelector(":scope > .spinner")).not.toBeNull();
      expect(spin(view.root, "l:p:code")).toBe("vk-spin-rotate");
    } finally {
      view.root.remove();
    }
  });

  it("keeps a container's settled outcome, which is painted into the slot itself", () => {
    const view = buildExecTree(() => {
      /* selection is not under test */
    });
    document.body.appendChild(view.root);
    view.attach(document.body);
    view.render(
      [{ ...loop, state: "fail", children: [{ ...pass, state: "fail", children: [] }] }],
      "",
    );
    try {
      const slot = view.root.querySelector('.ev-row[data-path="l"] > .ev-row-main > .ev-state');
      expect(slot?.querySelector("svg")).not.toBeNull();
    } finally {
      view.root.remove();
    }
  });
});
