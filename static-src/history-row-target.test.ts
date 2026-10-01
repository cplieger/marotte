// THE HISTORY ROW'S TARGET IS THE WHOLE BODY, minus the one destructive control in
// it. On the shared `.entry` row the body IS the control: it stretches to the row's
// full height and runs from the row's content edge to the actions slot. The row's
// `padding-inline` is the CARD's inset and is outside the control by design
// (11-page-lists.css), so the edge these cases measure is the content edge.
//
// Real layout in real Chromium, because every assertion here is a box or a hit
// test. The rows are built through the REAL builder, so the fixture cannot drift
// from what history.ts produces.
import { describe, it, expect, beforeAll, afterAll, afterEach } from "vitest";

import { mountAppCSS } from "./__test-helpers__/css-rules.js";
import { entryRow } from "./entry-row.js";

let style: HTMLStyleElement;
const host = document.createElement("div");

const noop = (): void => undefined;

/** A history row as `history.ts` `buildRow` assembles one: the same spec shape,
 *  with the delete button in the actions slot. */
function mountRow(opts: { delete: boolean; sub?: boolean }): {
  row: HTMLElement;
  open: HTMLElement;
  del: HTMLButtonElement | null;
} {
  const container = document.createElement("div");
  container.id = "history-table";
  container.className = "list-container";

  let del: HTMLButtonElement | null = null;
  if (opts.delete) {
    del = document.createElement("button");
    del.type = "button";
    del.className = "icon-btn entry-delete";
    del.setAttribute("data-history-delete", "s:sess_1");
    del.setAttribute("aria-label", "Delete Rebuild the timeline rail");
  }

  const row = entryRow({
    key: "s:sess_1",
    title: "Rebuild the timeline rail",
    time: { ms: Date.now() - 90_000 },
    sub:
      opts.sub === false
        ? undefined
        : { kind: "line", text: "a one line summary of the conversation" },
    actions: del === null ? undefined : [del],
    open: { name: "Rebuild the timeline rail", onOpen: noop },
  });
  container.appendChild(row);
  host.replaceChildren(container);
  const open = row.querySelector<HTMLElement>("button.entry-open");
  if (open === null) {
    throw new Error("the builder produced no open control");
  }
  return { row, open, del };
}

/** The row's content edge: the card's inset is the row's own and sits outside the
 *  control. */
function contentLeft(row: HTMLElement): number {
  return row.getBoundingClientRect().left + parseFloat(getComputedStyle(row).paddingLeft);
}

/** Whether a point activates the open control: a hit on any of its descendants
 *  does, because the click reaches the button by bubbling. */
function opens(open: HTMLElement, x: number, y: number): boolean {
  const hit = document.elementFromPoint(x, y);
  return hit === open || (hit !== null && open.contains(hit));
}

beforeAll(() => {
  style = mountAppCSS();
  host.style.cssText = "position:fixed;inset-block-start:0;inset-inline-start:0;inline-size:900px;";
  document.body.appendChild(host);
});

afterAll(() => {
  style.remove();
  host.remove();
  document.documentElement.removeAttribute("data-pointer");
});

afterEach(() => {
  host.replaceChildren();
});

describe.each(["fine", "coarse"] as const)("on a %s pointer", (tier) => {
  beforeAll(() => {
    document.documentElement.dataset["pointer"] = tier;
  });

  it("gives the open control the row's full height and its content edge", () => {
    const { row, open } = mountRow({ delete: true });
    const r = row.getBoundingClientRect();
    const o = open.getBoundingClientRect();
    expect(o.left, "the control starts at the row's content edge").toBeCloseTo(contentLeft(row), 0);
    expect(o.top, "and at its top").toBeCloseTo(r.top, 0);
    expect(o.height, "and it is the row's whole height").toBeCloseTo(r.height, 0);
  });

  it("covers all but the inset and the delete button's own column", () => {
    const { row, open, del } = mountRow({ delete: true });
    const r = row.getBoundingClientRect();
    const o = open.getBoundingClientRect();
    const d = del!.getBoundingClientRect();
    const share = (o.width * o.height) / (r.width * r.height);
    // The number the complaint was about. A regression to a text-shaped control
    // lands near 0.28 on this tier's own measurement, so the floor separates the
    // two shapes rather than merely being satisfied by the current one.
    expect(share, `the control covers ${(share * 100).toFixed(1)}% of the row`).toBeGreaterThan(
      0.9,
    );
    // And it stops short of the destructive control rather than reaching under it.
    expect(o.right, "the open target does not overlap the delete button").toBeLessThanOrEqual(
      d.left + 0.5,
    );
  });

  it("opens from every part of the row a reader would aim at", () => {
    const { row, open } = mountRow({ delete: true });
    const r = row.getBoundingClientRect();
    const cy = r.top + r.height / 2;
    // The four places the old band excluded: the row's own centre (which resolved
    // to a summary span), the content's leading edge, the row's last line, and the
    // time slot at the trailing end of the title line.
    expect(opens(open, r.left + r.width / 2, cy), "the row's centre").toBe(true);
    expect(opens(open, contentLeft(row) + 2, cy), "the content's leading edge").toBe(true);
    expect(opens(open, r.left + r.width / 2, r.bottom - 2), "the last line").toBe(true);
    expect(opens(open, r.left + r.width / 2, r.top + 2), "the first line").toBe(true);
  });

  it("leaves the delete button its own target, unreachable from the open one", () => {
    const { open, del } = mountRow({ delete: true });
    const d = del!.getBoundingClientRect();
    const cx = d.left + d.width / 2;
    const cy = d.top + d.height / 2;
    const hit = document.elementFromPoint(cx, cy);
    expect(hit === del || (hit !== null && del!.contains(hit)), "the delete button answers").toBe(
      true,
    );
    expect(opens(open, cx, cy), "and the open control does not").toBe(false);
  });

  it("fills the height of a ONE-LINE row, which is the tier's height like any other", () => {
    // The row's height is the `--row-h` token, never its content, so a row with no
    // subtitle is exactly as tall as one with, and the control spans all of it
    // rather than centring at its text's height.
    const { row, open } = mountRow({ delete: true, sub: false });
    const r = row.getBoundingClientRect();
    const o = open.getBoundingClientRect();
    expect(o.height, `control ${o.height} against row ${r.height}`).toBeCloseTo(r.height, 0);
    expect(opens(open, r.left + r.width / 2, r.top + 1), "the row's top edge opens").toBe(true);
    expect(opens(open, r.left + r.width / 2, r.bottom - 1), "and its bottom edge").toBe(true);
  });

  it("reaches the content's trailing edge when the row has no delete button", () => {
    // A run still moving carries none (`buildDeleteButton` withholds it), and the
    // control grows into the space rather than leaving a dead strip.
    const { row, open, del } = mountRow({ delete: false });
    expect(del).toBeNull();
    const r = row.getBoundingClientRect();
    const o = open.getBoundingClientRect();
    const contentRight = r.right - parseFloat(getComputedStyle(row).paddingRight);
    expect(o.right).toBeCloseTo(contentRight, 0);
    expect(opens(open, contentRight - 2, r.top + r.height / 2), "the trailing edge opens").toBe(
      true,
    );
  });
});
