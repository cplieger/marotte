import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import fc from "fast-check";

import { createPopover, type PopoverController, type VirtualAnchor } from "./popover.js";
import {
  type World,
  changedStyles,
  explore,
  firstFailure,
  isInvalidState,
  sentinelsOnce,
  takesAway,
  throwSentinel,
  type Injection,
  type Scenario,
} from "./__test-helpers__/reentry.js";

// The re-entry property for a popover. Its model is the popup lifecycle plus
// the placement options; at every quiescent point an open panel sits where an
// independent implementation of the documented placement rules puts it for
// the anchor's current rect, the panel's current size and the newest options.

type Anchor = "element" | "virtual" | "kicking" | "resetting";
type Step =
  | { readonly k: "show" | "hide" | "toggle" | "reposition" | "move" }
  | { readonly k: "setOptions"; readonly offset: number; readonly placement: "top" | "bottom" }
  | { readonly k: "dispose" | "settle" | "throw" };

interface Case {
  readonly anchor: Anchor;
  readonly fillOnOpen: boolean;
  readonly script: readonly Step[];
  readonly injections: readonly Injection<Step>[];
}

const stepArb: fc.Arbitrary<Step> = fc.oneof(
  fc.constantFrom<Step>(
    { k: "show" },
    { k: "hide" },
    { k: "toggle" },
    { k: "reposition" },
    { k: "move" },
    { k: "dispose" },
    { k: "settle" },
  ),
  fc
    .record({
      offset: fc.constantFrom(4, 12, 100),
      placement: fc.constantFrom<"top" | "bottom">("top", "bottom"),
    })
    .map((r): Step => ({ k: "setOptions", ...r })),
);
const injArb: fc.Arbitrary<Step> = fc.oneof(
  { weight: 6, arbitrary: stepArb.filter((s) => s.k !== "settle") },
  { weight: 1, arbitrary: fc.constant<Step>({ k: "throw" }) },
);
const caseArb: fc.Arbitrary<Case> = fc.record({
  anchor: fc.constantFrom<Anchor>("element", "virtual", "kicking", "resetting"),
  fillOnOpen: fc.boolean(),
  script: fc.array(stepArb, { minLength: 1, maxLength: 8 }),
  injections: fc.array(
    fc.record({
      at: fc.nat({ max: 400 }),
      actions: fc.array(injArb, { minLength: 1, maxLength: 2 }),
    }),
    { maxLength: 2 },
  ),
});

const VIEWPORT = { width: 1280, height: 720 };
const MARGIN = 8;

interface Rect {
  readonly top: number;
  readonly left: number;
  readonly width: number;
  readonly height: number;
}

/** The documented content-sized placement on the top or bottom side with start
 *  alignment, flip and clamp on, computed without the library. */
function expectedCoords(
  rect: Rect,
  panelW: number,
  panelH: number,
  placement: "top" | "bottom",
  offset: number,
): { top: number; left: number } {
  const bottom = rect.top + rect.height;
  const above = rect.top;
  const below = VIEWPORT.height - bottom;
  let side = placement;
  if (
    placement === "bottom" &&
    bottom + offset + panelH > VIEWPORT.height - MARGIN &&
    above > below
  ) {
    side = "top";
  } else if (placement === "top" && rect.top - offset - panelH < MARGIN && below > above) {
    side = "bottom";
  }
  const top = side === "bottom" ? bottom + offset : rect.top - panelH - offset;
  const hi = VIEWPORT.width - panelW - MARGIN;
  const left = hi < MARGIN ? MARGIN : Math.min(Math.max(rect.left, MARGIN), hi);
  return { top, left };
}

function scenarioFor(c: Case) {
  return (w: World): Scenario<Step> => {
    let failure: string | null = null;
    const fail = (why: string): void => {
      failure ??= why;
    };
    const model = {
      open: false,
      closing: false,
      hidden: true,
      ended: false,
      offset: 4,
      placement: "bottom" as "top" | "bottom",
      opens: 0,
      closes: 0,
      filled: false,
    };
    let geometry: Rect = { top: 300, left: 400, width: 120, height: 30 };
    let anchorThrew = false;
    const anchorEl = w.probe("anchor", "button");
    const panel = w.probe("panel");
    w.quiet(() => {
      anchorEl.style.position = "fixed";
      anchorEl.style.margin = "0";
      anchorEl.style.padding = "0";
      anchorEl.style.border = "0";
      panel.style.width = "200px";
      panel.style.display = "block";
      panel.hidden = true;
      document.body.append(anchorEl, panel);
    });
    const placeElement = (): void => {
      w.quiet(() => {
        anchorEl.style.top = `${geometry.top.toString()}px`;
        anchorEl.style.left = `${geometry.left.toString()}px`;
        anchorEl.style.width = `${geometry.width.toString()}px`;
        anchorEl.style.height = `${geometry.height.toString()}px`;
      });
    };
    placeElement();
    let ctl: PopoverController | undefined;
    const rectWithGetters = (): DOMRect => {
      const g = geometry;
      const member = (name: string, v: number) => () => {
        w.hit(`rect:${name}`);
        return v;
      };
      const r = {} as Record<string, unknown>;
      for (const [name, v] of [
        ["top", g.top],
        ["left", g.left],
        ["right", g.left + g.width],
        ["bottom", g.top + g.height],
        ["width", g.width],
        ["height", g.height],
        ["x", g.left],
        ["y", g.top],
      ] as const) {
        Object.defineProperty(r, name, { enumerable: true, get: member(name, v) });
      }
      r["toJSON"] = () => ({});
      return r as unknown as DOMRect;
    };
    const virtual: VirtualAnchor = {
      getBoundingClientRect: () => {
        w.hit("anchor:measure");
        if (c.anchor === "kicking") {
          ctl?.reposition();
        } else if (c.anchor === "resetting") {
          // A same-value patch changes nothing.
          ctl?.setOptions({ offset: model.offset });
        }
        return rectWithGetters();
      },
    };
    const anchor = c.anchor === "element" ? anchorEl : virtual;
    // A released popover only takes state away; a live one writes coordinates
    // only while open, and only those of the newest options for what it measures now.
    w.admit = (write) => {
      if (write.el !== panel && write.el !== anchorEl) {
        return undefined;
      }
      if (model.ended) {
        return takesAway(write);
      }
      if (write.name !== "style") {
        return undefined;
      }
      const changed = changedStyles(write);
      const top = changed.get("top");
      const left = changed.get("left");
      if (top === undefined && left === undefined) {
        return undefined;
      }
      if (!model.open) {
        return false;
      }
      const want = w.quiet(() =>
        expectedCoords(
          geometry,
          panel.offsetWidth,
          panel.offsetHeight,
          model.placement,
          model.offset,
        ),
      );
      return (
        (top === undefined || top === `${want.top.toString()}px`) &&
        (left === undefined || left === `${want.left.toString()}px`)
      );
    };

    const onOpen = (): void => {
      if (model.ended) {
        fail("onOpen after release");
      }
      model.opens++;
      w.hit("onOpen");
      if (c.fillOnOpen && !model.filled) {
        model.filled = true;
        for (let i = 0; i < 5; i++) {
          const row = document.createElement("div");
          row.style.height = "20px";
          panel.append(row);
        }
        ctl?.reposition();
      }
    };
    const onClose = (): void => {
      if (model.ended) {
        fail("onClose after release");
      }
      model.closes++;
      w.hit("onClose");
    };

    const hideModel = (): void => {
      if (model.open) {
        model.open = false;
        model.closing = true;
      }
    };
    const showModel = (): void => {
      if (!model.ended && !model.open) {
        model.open = true;
        model.closing = false;
      }
    };

    return {
      start(): void {
        w.entry(null, () => {
          ctl = createPopover(anchor, panel, { onOpen, onClose });
        });
      },
      act(s: Step): undefined {
        const p = ctl;
        // A hit inside createPopover comes before the controller exists.
        if (p === undefined && s.k !== "throw") {
          return;
        }
        switch (s.k) {
          case "show":
            w.entry(null, () => {
              showModel();
              p?.show();
            });
            return;
          case "hide":
            w.entry(null, () => {
              if (!model.ended) {
                hideModel();
              }
              p?.hide();
            });
            return;
          case "toggle":
            w.entry(null, () => {
              if (!model.ended) {
                if (model.open) {
                  hideModel();
                } else {
                  showModel();
                }
              }
              p?.toggle();
            });
            return;
          case "reposition":
            w.entry(null, () => p?.reposition());
            return;
          case "move":
            w.entry(null, () => {
              // A layout change the popover cannot see is reported with reposition().
              geometry = { ...geometry, top: geometry.top === 300 ? 120 : 300 };
              placeElement();
              p?.reposition();
            });
            return;
          case "setOptions":
            w.entry(null, () => {
              if (!model.ended) {
                model.offset = s.offset;
                model.placement = s.placement;
              }
              p?.setOptions({ offset: s.offset, placement: s.placement });
            });
            return;
          case "dispose":
            w.entry(null, () => {
              w.markRelease();
              model.ended = true;
              model.open = false;
              p?.dispose();
            });
            w.entry(null, () => {
              p?.show();
              p?.setOptions({ offset: 50 });
              if (p?.isOpen === true) {
                fail("isOpen after dispose");
              }
            });
            return;
          case "settle":
            w.advance(400, null);
            model.closing = false;
            return;
          case "throw": {
            const at = w.trace[w.trace.length - 1] ?? "";
            if (at.startsWith("anchor:") || at.startsWith("rect:")) {
              // A throwing anchor leaves the last placement in place.
              anchorThrew = true;
            }
            throwSentinel(w);
          }
        }
      },
      check(where: string): string | null {
        if (failure !== null) {
          return failure;
        }
        const isOpenCls = panel.classList.contains("is-open");
        const leaving = panel.classList.contains("is-leaving");
        const settled = panel.hidden && !isOpenCls && !leaving;
        let state: boolean;
        if (model.open) {
          state = isOpenCls && !leaving && !panel.hidden;
        } else if (model.closing && !model.ended) {
          state = settled || (leaving && !isOpenCls && !panel.hidden);
        } else {
          state = settled;
        }
        const checks: [boolean, string][] = [
          [
            state,
            `panel hidden=${String(panel.hidden)} is-open=${String(isOpenCls)} is-leaving=${String(leaving)}`,
          ],
          [ctl?.isOpen === model.open, `isOpen ${String(ctl?.isOpen)}`],
          [
            model.ended || model.opens - model.closes === (model.open ? 1 : 0),
            "onOpen/onClose net",
          ],
        ];
        const tracking = w.ledger.count(window, ["resize"]) + w.ledger.count(document, ["scroll"]);
        checks.push([
          tracking === (model.open ? 2 : 0),
          `${tracking.toString()} tracking listeners`,
        ]);
        if (model.open && !anchorThrew) {
          const want = expectedCoords(
            geometry,
            panel.offsetWidth,
            panel.offsetHeight,
            model.placement,
            model.offset,
          );
          const top = panel.style.top;
          const left = panel.style.left;
          checks.push([
            top === `${want.top.toString()}px` &&
              left === `${want.left.toString()}px` &&
              panel.style.position === "fixed",
            `panel at top ${top} left ${left}, expected ${want.top.toString()}px ${want.left.toString()}px`,
          ]);
          // A quiescent popover is a fixed point: reposition() writes nothing.
          const before = panel.getAttribute("style");
          w.quiet(() => {
            ctl?.reposition();
          });
          checks.push([
            panel.getAttribute("style") === before,
            "reposition() on a quiescent popover changed its style",
          ]);
        }
        return firstFailure(where, checks);
      },
      finish(): string | null {
        return failure ?? sentinelsOnce(w, isInvalidState);
      },
      cleanup(): void {
        try {
          ctl?.dispose();
        } catch {
          // Observed already.
        }
      },
    };
  };
}

beforeEach(() => {
  vi.useFakeTimers();
});

afterEach(() => {
  vi.useRealTimers();
  document.body.replaceChildren();
});

describe("popover: re-entry property", () => {
  it("an open panel sits where the placement rules put it for the newest options, through re-entry", async () => {
    await fc.assert(
      fc.asyncProperty(caseArb, async (c) => {
        expect(await explore(scenarioFor(c), c)).toBeNull();
      }),
      {
        numRuns: 150,
        examples: [
          // A hide and a show re-entered from a setOptions commit: the listeners arm again.
          [
            {
              anchor: "virtual",
              fillOnOpen: false,
              script: [{ k: "show" }, { k: "setOptions", offset: 4, placement: "top" }],
              injections: [{ at: 14, actions: [{ k: "hide" }, { k: "show" }] }],
            },
          ],
          // A show() and a throw from the anchor's rect during show(): later placements are still judged.
          [
            {
              anchor: "virtual",
              fillOnOpen: true,
              script: [{ k: "setOptions", offset: 4, placement: "top" }, { k: "show" }],
              injections: [{ at: 0, hit: "rect:top", actions: [{ k: "show" }, { k: "throw" }] }],
            },
          ],
          // A show() from the settle's class write arms its listeners at once.
          [
            {
              anchor: "element",
              fillOnOpen: false,
              script: [{ k: "show" }, { k: "hide" }, { k: "settle" }],
              injections: [{ at: 4, hit: "attr:class@panel", actions: [{ k: "show" }] }],
            },
          ],
          // An anchor that sets the current offset while measured converges.
          [{ anchor: "resetting", fillOnOpen: false, script: [{ k: "show" }], injections: [] }],
          // A newer offset committed while the anchor is measured is the one placed.
          [
            {
              anchor: "virtual",
              fillOnOpen: false,
              script: [{ k: "show" }],
              injections: [
                {
                  at: 0,
                  hit: "anchor:measure",
                  actions: [{ k: "setOptions", offset: 100, placement: "bottom" }],
                },
              ],
            },
          ],
          // A dispose at the first reveal write lands no later reveal write.
          [
            {
              anchor: "element",
              fillOnOpen: false,
              script: [{ k: "show" }],
              injections: [{ at: 0, hit: "attr:class@panel", actions: [{ k: "dispose" }] }],
            },
          ],
        ],
      },
    );
  }, 60000);
});
