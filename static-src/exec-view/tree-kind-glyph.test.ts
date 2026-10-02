// The exec tree's kind column: one glyph per `ExecKind`, six distinct marks, none
// borrowed from git or from the run page's own header, and the drawings this
// column owns kept on the icon grid `icon-crisp.ts` snaps to.
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { buildExecTree } from "./tree.js";
import { iconEl } from "../icon-el.js";
import type { ExecKind, ExecNode } from "./model.js";
import {
  ICON_EXEC_GROUP,
  ICON_EXEC_PARALLEL,
  ICON_EXEC_SEQUENCE,
  ICON_EXEC_WATCH,
  ICON_GIT_BRANCH,
  ICON_REFRESH,
  ICON_TAB_GIT,
  ICON_TAB_RUN,
} from "../icons.js";

// A Record rather than an array, so a seventh kind fails the type check here too.
const KINDS: Record<ExecKind, true> = {
  step: true,
  sequence: true,
  repeat: true,
  parallel: true,
  watch: true,
  group: true,
};

function inner(svg: string): string {
  return svg
    .replace(/^<svg\b[^>]*>/, "")
    .replace(/<\/svg>$/, "")
    .replace(/\s+/g, " ")
    .trim();
}

let host: HTMLElement;

beforeEach(() => {
  host = document.createElement("div");
  document.body.appendChild(host);
});

afterEach(() => {
  host.remove();
});

/** The drawing each kind's row renders, read off the real tree pane. */
function renderedGlyphs(): Map<ExecKind, string> {
  const tree = buildExecTree(vi.fn());
  host.appendChild(tree.root);
  const kinds = Object.keys(KINDS) as ExecKind[];
  const nodes: ExecNode[] = kinds.map((kind) => ({
    path: kind,
    label: kind,
    kind,
    state: "pending",
    children: [],
  }));
  tree.render(nodes, "");
  const out = new Map<ExecKind, string>();
  for (const kind of kinds) {
    const slot = host.querySelector<HTMLElement>(`.ev-row[data-path="${kind}"] .ev-kind`);
    expect(slot, `${kind} row has a kind slot`).not.toBeNull();
    expect(slot!.hidden, `${kind} kind slot is shown`).toBe(false);
    const svg = slot!.querySelector("svg");
    expect(svg, `${kind} row draws a glyph`).not.toBeNull();
    out.set(kind, inner(svg!.outerHTML));
  }
  return out;
}

describe("the exec tree's kind glyph", () => {
  it("gives every kind a glyph", () => {
    expect(renderedGlyphs().size).toBe(6);
  });

  it("draws six distinct marks", () => {
    const glyphs = [...renderedGlyphs().values()];
    expect(new Set(glyphs).size).toBe(glyphs.length);
  });

  it("redraws the glyph when a path's kind changes and keeps it when it does not", () => {
    const tree = buildExecTree(vi.fn());
    host.appendChild(tree.root);
    const node = (kind: ExecKind): ExecNode => ({
      path: "n",
      label: "n",
      kind,
      state: "pending",
      children: [],
    });
    const slot = (): HTMLElement =>
      host.querySelector<HTMLElement>('.ev-row[data-path="n"] .ev-kind')!;

    tree.render([node("step")], "");
    const first = slot().querySelector("svg");
    tree.render([node("step")], "");
    expect(slot().querySelector("svg"), "same kind keeps the same element").toBe(first);

    tree.render([node("parallel")], "");
    expect(slot().querySelectorAll("svg")).toHaveLength(1);
    expect(inner(slot().querySelector("svg")!.outerHTML)).toBe(
      inner(iconEl(ICON_EXEC_PARALLEL).outerHTML),
    );
  });

  it("borrows no git glyph and not the run page's header glyph", () => {
    // Through the DOM like the rendered side, which serializes `<x/>` as `<x></x>`.
    const forbidden = [ICON_TAB_GIT, ICON_GIT_BRANCH, ICON_TAB_RUN].map((svg) =>
      inner(iconEl(svg).outerHTML),
    );
    for (const [kind, glyph] of renderedGlyphs()) {
      expect(forbidden, `${kind} glyph`).not.toContain(glyph);
    }
  });
});

interface Seg {
  axis: "h" | "v" | "other";
  /** The perpendicular coordinate of an axis-aligned segment. */
  at: number;
}

/** Walk a path's `d` and report every segment's end points plus each axis-aligned
 *  segment's perpendicular coordinate. Handles the commands these drawings use.
 *  End points only: a curve's or arc's extremes between them are not checked. */
function walk(d: string): { points: [number, number][]; segs: Seg[] } {
  const tokens = d.match(/[a-zA-Z]|-?(?:\d+\.?\d*|\.\d+)/g) ?? [];
  let i = 0;
  let cmd = "";
  let x = 0;
  let y = 0;
  let sx = 0;
  let sy = 0;
  const points: [number, number][] = [];
  const segs: Seg[] = [];
  const num = (): number => Number(tokens[i++]);
  const line = (nx: number, ny: number): void => {
    const axis = ny === y ? "h" : nx === x ? "v" : "other";
    segs.push({ axis, at: axis === "h" ? y : nx });
    x = nx;
    y = ny;
    points.push([x, y]);
  };
  while (i < tokens.length) {
    if (/[a-zA-Z]/.test(tokens[i]!)) {
      cmd = tokens[i++]!;
    }
    const rel = cmd === cmd.toLowerCase();
    switch (cmd.toLowerCase()) {
      case "m": {
        const nx = num() + (rel ? x : 0);
        const ny = num() + (rel ? y : 0);
        x = sx = nx;
        y = sy = ny;
        points.push([x, y]);
        cmd = rel ? "l" : "L";
        break;
      }
      case "l":
        line(num() + (rel ? x : 0), num() + (rel ? y : 0));
        break;
      case "h":
        line(num() + (rel ? x : 0), y);
        break;
      case "v":
        line(x, num() + (rel ? y : 0));
        break;
      case "c":
        for (let k = 0; k < 4; k++) {
          num();
        }
        x = num() + (rel ? x : 0);
        y = num() + (rel ? y : 0);
        segs.push({ axis: "other", at: 0 });
        points.push([x, y]);
        break;
      case "a":
        for (let k = 0; k < 5; k++) {
          num();
        }
        x = num() + (rel ? x : 0);
        y = num() + (rel ? y : 0);
        segs.push({ axis: "other", at: 0 });
        points.push([x, y]);
        break;
      case "z":
        line(sx, sy);
        break;
      default:
        throw new Error(`unhandled path command ${cmd}`);
    }
  }
  return { points, segs };
}

describe("the kind glyphs drawn on the 24 grid keep to it", () => {
  const OWNED = [
    ["parallel", ICON_EXEC_PARALLEL],
    ["sequence", ICON_EXEC_SEQUENCE],
    ["group", ICON_EXEC_GROUP],
    ["watch", ICON_EXEC_WATCH],
    ["repeat", ICON_REFRESH],
  ] as const;
  const pathsOf = (svg: string): string[] => [...svg.matchAll(/\bd="([^"]+)"/g)].map((m) => m[1]!);

  for (const [name, svg] of OWNED) {
    it(`${name} stays inside the 2..22 box`, () => {
      const ds = pathsOf(svg);
      expect(ds.length).toBeGreaterThan(0);
      for (const d of ds) {
        for (const [px, py] of walk(d).points) {
          expect(px, `${name} x in ${d}`).toBeGreaterThanOrEqual(2);
          expect(px, `${name} x in ${d}`).toBeLessThanOrEqual(22);
          expect(py, `${name} y in ${d}`).toBeGreaterThanOrEqual(2);
          expect(py, `${name} y in ${d}`).toBeLessThanOrEqual(22);
        }
      }
    });
  }

  // Group and watch are all diagonals and curves, so they have no structural stroke to place.
  for (const [name, svg] of OWNED.filter(([n]) => n !== "group" && n !== "watch")) {
    it(`${name} puts every axis-aligned stroke on the 3-unit grid`, () => {
      const aligned = pathsOf(svg).flatMap((d) =>
        walk(d)
          .segs.filter((seg) => seg.axis !== "other")
          .map((seg) => ({ d, seg })),
      );
      expect(aligned.length, `${name} has structural strokes to check`).toBeGreaterThan(0);
      for (const { d, seg } of aligned) {
        expect(seg.at % 3, `${name} ${seg.axis} stroke at ${seg.at} in ${d}`).toBe(0);
      }
    });
  }
});
