// A TOOL GROUP'S HEADER ALWAYS CARRIES A MARK, including while it runs: `paintGroupOutcome` writes
// no node into the verdict slot while running, so `14-tools.css` draws a hollow ring there. Numeric,
// because the mark is a pseudo-element. Fixtures come from the production builders, which write the
// `data-outcome` the state classes key on.

import { describe, it, expect, beforeAll, beforeEach, afterAll, afterEach } from "vitest";

// The card builder's import graph reaches the shared DOM registry, which throws on
// a missing app root, so these ids exist before the imports are evaluated.
for (const id of [
  "messages",
  "messages-wrap",
  "messages-wrap-outer",
  "chat-view",
  "scroll-bottom",
]) {
  const d = document.createElement("div");
  d.id = id;
  document.body.appendChild(d);
}

const { mountAppCSS } = await import("./__test-helpers__/css-rules.js");
const { buildToolGroupShell, groupBody, refreshGroupHeader } = await import("./tool-group.js");
const { buildToolCard } = await import("./tool-card.js");

const host = document.createElement("div");
host.style.cssText = "position:fixed;top:-9999px;left:0;inline-size:760px;";
document.body.appendChild(host);

let style: HTMLStyleElement;

beforeAll(() => {
  style = mountAppCSS();
});

// Per test, not once: the diameter case below drives both pointer tiers, and a case
// inheriting the previous one's tier would pass for a reason it did not state.
beforeEach(() => {
  document.documentElement.dataset["pointer"] = "fine";
});

afterAll(() => {
  style.remove();
  host.remove();
  document.documentElement.removeAttribute("data-pointer");
});

afterEach(() => {
  host.replaceChildren();
});

type Status = "in_progress" | "completed" | "failed";

function card(i: number, status: Status): HTMLDivElement {
  return buildToolCard({
    id: `t${String(i)}`,
    title: "Execute Bash",
    kind: "execute",
    status,
    live: status === "in_progress",
    input: { command: "go build ./..." },
  });
}

/** A real two-member group, mounted, header refreshed — which is what writes the
 *  state class. Two members because a one-member shell is BARE and its header is
 *  `display: none`, so there would be no slot on screen to measure. */
function group(...statuses: Status[]): HTMLElement {
  const g = buildToolGroupShell();
  statuses.forEach((s, i) => {
    groupBody(g).appendChild(card(i, s));
  });
  host.appendChild(g);
  refreshGroupHeader(g);
  return g;
}

function slotOf(g: Element): HTMLElement {
  return g.querySelector<HTMLElement>(".tool-group-header .tool-group-icon")!;
}

const running = (): HTMLElement => slotOf(group("completed", "in_progress"));
const settled = (): HTMLElement => slotOf(group("completed", "completed"));
const broken = (): HTMLElement => slotOf(group("completed", "failed"));

describe("the running group header's slot", () => {
  it("paints a ring, so it is never a blank gap between the chevron and the count", () => {
    const slot = running();
    expect(slot.classList.contains("is-running"), "the fixture reaches the running state").toBe(
      true,
    );
    const ring = getComputedStyle(slot, "::before");
    // The defect, in the three properties that make the mark exist at all.
    expect(ring.content, "the slot draws a mark while the group runs").not.toBe("none");
    expect(parseFloat(ring.width), "and that mark has a real box").toBeGreaterThan(0);
    expect(parseFloat(ring.borderTopWidth), "drawn as a ring").toBeGreaterThan(0);
  });

  it("keeps the ring HOLLOW, so the shape carries the difference from a settled verdict", () => {
    // Every settled mark is solid, so a solid running dot would leave TINT the only channel (WCAG
    // 1.4.1). Two channels: no silhouette, and an unfilled ring.
    const slot = running();
    const ring = getComputedStyle(slot, "::before");
    expect(slot.querySelector("svg"), "no silhouette while there is no verdict").toBeNull();
    expect(ring.backgroundColor, "the ring is unfilled").toBe("rgba(0, 0, 0, 0)");
    expect(ring.borderTopLeftRadius, "and it is a circle").toBe("50%");

    expect(settled().querySelector("svg"), "a settled group draws the silhouette").not.toBeNull();
  });

  it("does not spin: the running members below carry the motion", () => {
    // No spin: each running member has its own `.tool-spinner`, and a container claims no second work
    // indicator (as `.subagent-icon`).
    expect(getComputedStyle(running(), "::before").animationName).toBe("none");
  });

  it.each(["fine", "coarse"] as const)(
    "renders at the settled mark's own diameter on a %s pointer, so the header's mark does not resize on settle",
    (tier) => {
      // One slot for both states, so the oracle is the RENDERED silhouette (path bbox scaled by viewBox),
      // not the stylesheet's ratio.
      document.documentElement.dataset["pointer"] = tier;
      const slot = running();
      const ring = getComputedStyle(slot, "::before");
      // `box-sizing: border-box` on the pseudo, so the declared size IS the outer
      // diameter — asserted, because the reset's `*` does not reach a pseudo-element
      // and the content-box default would put the border outside it.
      expect(ring.boxSizing).toBe("border-box");
      const outer = parseFloat(ring.width);
      // Read before the clear: a detached element measures 0, which would satisfy the
      // fits-the-slot assertion below for the wrong reason.
      const slotWidth = slot.getBoundingClientRect().width;
      host.replaceChildren();

      const mark = settled().querySelector("svg")!;
      const path = mark.querySelector("path")!;
      const units = parseFloat(mark.getAttribute("viewBox")!.split(/\s+/)[2]!);
      const painted = (path.getBBox().width / units) * mark.getBoundingClientRect().width;

      expect(outer).toBeCloseTo(painted, 2);
      expect(outer, "and it fits the slot it is centred in").toBeLessThanOrEqual(slotWidth);
    },
  );

  it("takes its colour from the slot, so the accent stays declared once", () => {
    // `currentColor` through `.tool-icon.is-running`. A literal or a second token
    // would drift from the slot's own colour, which is what this compares.
    const slot = running();
    const ring = getComputedStyle(slot, "::before");
    expect(ring.borderTopColor).toBe(getComputedStyle(slot).color);
    // And the in-flight tint is its own, not the settled one inherited.
    expect(getComputedStyle(slot).color).not.toBe(getComputedStyle(settled()).color);
  });
});

describe("the ring belongs to the running state alone", () => {
  // Built INSIDE the loop, and measured before the host is cleared: a detached
  // element's pseudo resolves `content` to the empty string rather than to `none`,
  // so a fixture built up front and read after a clear passes for the wrong reason.
  it.each([
    ["ok", () => settled()],
    ["fail", () => broken()],
  ] as const)(
    "draws no ring behind a settled %s verdict, so no group carries two marks",
    (_name, build) => {
      expect(
        getComputedStyle(build(), "::before").content,
        "the silhouette is the whole mark",
      ).toBe("none");
    },
  );

  it("leaves the slot's own box identical in both states, which is what keeps the count still", () => {
    // Why a pseudo-element in a declared square: sizing the slot to the mark passes everything above and
    // shifts the summary on settle.
    const run = slotOf(group("completed", "in_progress")).getBoundingClientRect();
    host.replaceChildren();
    const done = slotOf(group("completed", "completed")).getBoundingClientRect();
    expect(run.width).toBe(done.width);
    expect(run.height).toBe(done.height);
    expect(run.width, "and it is a real box").toBeGreaterThan(0);
  });
});

describe("the mark sits on the header's own centre line", () => {
  // The reported droop needed a rect: a grid slot smaller than its content does not centre it on the
  // BLOCK axis (the implicit `auto` row starts at the top), so the glyph sat 1px low fine and 3px low
  // coarse.
  const centreY = (el: Element): number => {
    const r = el.getBoundingClientRect();
    return r.y + r.height / 2;
  };

  it.each(["fine", "coarse"] as const)(
    "centres the settled silhouette in its bar on a %s pointer",
    (tier) => {
      document.documentElement.dataset["pointer"] = tier;
      const slot = settled();
      const header = slot.closest<HTMLElement>(".tool-group-header")!;
      const mark = slot.querySelector("svg")!;

      // Against the HEADER, not against the slot: the slot was already centred while
      // the mark was not, so a slot-relative assertion passes with the defect in place.
      expect(centreY(mark), "the mark is on the bar's centre line").toBeCloseTo(centreY(header), 1);
      // And the mark fills the slot, which is the mechanism rather than a second
      // symptom: a slot that cannot be overflowed has nothing to mis-align.
      expect(mark.getBoundingClientRect().height).toBeCloseTo(
        slot.getBoundingClientRect().height,
        1,
      );
    },
  );

  it("puts the running ring on that same centre line, so the mark does not jump on settle", () => {
    document.documentElement.dataset["pointer"] = "coarse";
    const slot = running();
    const header = slot.closest<HTMLElement>(".tool-group-header")!;
    // The ring is a pseudo-element, so its own box is unmeasurable; the SLOT is what
    // centres it (`place-items: center` over a box the ring is smaller than), so the
    // slot's centre line is the assertion.
    expect(centreY(slot)).toBeCloseTo(centreY(header), 1);
  });
});
