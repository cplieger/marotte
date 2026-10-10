// A COLLAPSED TOOL BOX IS ONE HEIGHT, whichever kind: a collapsed `.tool-group` and a claim-only
// `.tool-call` sit side by side. Measured as OUTER heights, because the divergence was
// `.tool-call`'s `contain-intrinsic-size`, what an off-screen card renders at.
import { describe, it, expect, beforeAll, afterAll } from "vitest";

import { mountAppCSS } from "./__test-helpers__/css-rules.js";

let style: HTMLStyleElement;
let host: HTMLElement;

beforeAll(() => {
  style = mountAppCSS();
});

afterAll(() => {
  style.remove();
  host?.remove();
});

/** The two collapsed shapes as the transcript builds them, in one container so
 *  they share a width and a font. */
function mountBoxes(): { group: HTMLElement; card: HTMLElement } {
  host?.remove();
  host = document.createElement("div");
  host.className = "turn-body";
  host.style.inlineSize = "760px";
  document.body.appendChild(host);

  host.innerHTML = `
    <div class="tool-group">
      <div class="tool-group-header" role="button" aria-expanded="false">
        <span class="tool-group-icon"></span>
        <span class="tool-group-summary">Ran 2 commands</span>
      </div>
      <div class="tool-group-body" hidden></div>
    </div>
    <div class="tool-call">
      <div class="tool-summary">
        <div class="tool-header">
          <span class="tool-icon"></span>
          <span class="tool-title">Read File</span>
          <span class="tool-subject">auth.go</span>
        </div>
      </div>
    </div>`;

  return {
    group: host.querySelector<HTMLElement>(".tool-group")!,
    card: host.querySelector<HTMLElement>(".tool-call")!,
  };
}

/** A custom property reads back as its raw token (`2.25rem`), so the only honest way to get the
 *  length is to let the engine resolve it on a real box. */
function controlHeight(): number {
  const probe = document.createElement("div");
  probe.style.blockSize = "var(--btn-h)";
  host.appendChild(probe);
  const h = probe.getBoundingClientRect().height;
  probe.remove();
  return h;
}

describe("a collapsed tool box", () => {
  it("measures the same whichever kind it is", () => {
    const { group, card } = mountBoxes();
    // Rects rather than offsetHeight: a fractional line box is the thing that
    // made these disagree by less than a pixel before the floor landed.
    const g = group.getBoundingClientRect().height;
    const c = card.getBoundingClientRect().height;
    expect(c).toBeCloseTo(g, 1);
  });

  it("puts both headers on the control-height floor", () => {
    mountBoxes();
    const floor = controlHeight();
    expect(floor).toBeGreaterThan(0);

    for (const sel of [".tool-group-header", ".tool-header"] as const) {
      const el = host.querySelector<HTMLElement>(sel)!;
      expect(el.getBoundingClientRect().height, sel).toBeCloseTo(floor, 1);
    }
  });

  /** The `auto <length>` reserve a box renders at before its first layout. */
  function reservedSize(sel: string): number {
    const el = document.createElement("div");
    el.className = sel.slice(1);
    host.appendChild(el);
    const declared = getComputedStyle(el).containIntrinsicSize;
    const reserved = Number.parseFloat(declared.replace(/^auto\s+/, ""));
    expect(Number.isNaN(reserved), `${sel} containIntrinsicSize was ${declared}`).toBe(false);
    el.remove();
    return reserved;
  }

  // A placeholder disagreeing with the collapsed height resizes the card on scroll.
  // `contain-intrinsic-size` sizes the CONTENT box, so borders are excluded. Only `.tool-call` sits
  // on the header floor: the others always carry something under their header.
  it("reserves the header floor, borders excluded, on a claim-only card", () => {
    mountBoxes();
    expect(reservedSize(".tool-call")).toBeCloseTo(controlHeight(), 1);
  });

  // A RELATION, not values: forbids pulling a header-plus-more box down onto the bare-header floor.
  it("reserves more than that floor wherever the collapsed state carries content", () => {
    mountBoxes();
    const floor = controlHeight();

    // .subagent-block keeps a foot, and a tail while it runs; .plan-message
    // always has an entry under its header; .run-card has its first step row.
    for (const sel of [".subagent-block", ".plan-message", ".run-card"] as const) {
      expect(reservedSize(sel), sel).toBeGreaterThan(floor);
    }
  });
});
