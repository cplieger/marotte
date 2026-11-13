import { describe, it, expect, afterEach } from "vitest";
import fc from "fast-check";
import { createRoot, runWithOwner } from "@cplieger/reactive";

import { rovingFocus, type RovingFocusController } from "./roving-focus.js";
import {
  type World,
  explore,
  firstFailure,
  isInvalidState,
  sentinelsOnce,
  throwSentinel,
  type Injection,
  type Scenario,
} from "./__test-helpers__/reentry.js";

// The re-entry property for roving focus. A focusin on an item and refresh()
// commit the tab stop; at a quiescent point exactly one item carries
// tabindex 0, the committed stop when it is still an item, else the first.

type Step =
  | { readonly k: "focusItem"; readonly i: number }
  | { readonly k: "arrow" | "home" | "end" | "refresh" | "focusFirst" | "add" | "focusOutside" }
  | { readonly k: "dispose" | "owner" | "throw" };

const stepArb: fc.Arbitrary<Step> = fc.oneof(
  fc.nat({ max: 3 }).map((i): Step => ({ k: "focusItem", i })),
  fc.constantFrom<Step>(
    { k: "arrow" },
    { k: "home" },
    { k: "end" },
    { k: "refresh" },
    { k: "focusFirst" },
    { k: "add" },
    { k: "focusOutside" },
    { k: "dispose" },
    { k: "owner" },
  ),
);
const injArb: fc.Arbitrary<Step> = fc.oneof(
  { weight: 6, arbitrary: stepArb },
  { weight: 1, arbitrary: fc.constant<Step>({ k: "throw" }) },
);
interface Run {
  readonly script: readonly Step[];
  readonly injections: readonly Injection<Step>[];
}

const runArb: fc.Arbitrary<Run> = fc.record({
  script: fc.array(stepArb, { minLength: 1, maxLength: 8 }),
  injections: fc.array(
    fc.record({
      at: fc.nat({ max: 400 }),
      actions: fc.array(injArb, { minLength: 1, maxLength: 2 }),
    }),
    {
      maxLength: 2,
    },
  ),
});

function scenario(w: World): Scenario<Step> {
  let failure: string | null = null;
  const container = w.probe("container");
  const outside = w.probe("outside", "button");
  const items: HTMLElement[] = [];
  const addItem = (): void => {
    const item = w.probe(`item${items.length.toString()}`);
    w.quiet(() => {
      item.className = "item";
      item.removeAttribute("tabindex");
    });
    items.push(item);
    container.append(item);
  };
  w.quiet(() => {
    for (let i = 0; i < 3; i++) {
      addItem();
    }
    document.body.append(container, outside);
  });
  const m = { stop: null as HTMLElement | null, ended: false };
  w.ownListen(
    document,
    "focusin",
    (e) => {
      const t = e.target;
      if (!m.ended && t instanceof HTMLElement && items.includes(t)) {
        m.stop = t;
      }
    },
    true,
  );
  let ctl: RovingFocusController | undefined;
  let disposeParent = (): void => undefined;
  // A released roving controller writes nothing and moves no focus.
  w.admit = (write) => (m.ended && items.includes(write.el as HTMLElement) ? false : undefined);
  const live = (): HTMLElement[] => items.filter((i) => i.isConnected);
  const release = (how: "dispose" | "owner"): void => {
    if (how === "dispose" && ctl === undefined) {
      // A setup hit: the controller is not returned yet.
      return;
    }
    w.entry(null, () => {
      m.ended = true;
      if (how === "dispose") {
        ctl?.dispose();
      } else {
        disposeParent();
      }
    });
    const c = ctl;
    if (c !== undefined) {
      w.entry(null, () => {
        const before = items.map((i) => i.getAttribute("tabindex"));
        c.refresh();
        c.focusFirst();
        if (items.some((i, n) => i.getAttribute("tabindex") !== before[n])) {
          failure ??= "a refused refresh() or focusFirst() changed a tabindex";
        }
      });
    }
  };
  const key = (k: string): void => {
    (document.activeElement ?? container).dispatchEvent(
      new KeyboardEvent("keydown", { key: k, bubbles: true }),
    );
  };

  return {
    start(): void {
      w.entry(null, () => {
        const [d, parent] = createRoot((dispose, owner) => [dispose, owner] as const);
        disposeParent = d;
        ctl = runWithOwner(parent, () => rovingFocus(container, ".item"));
      });
    },
    act(s: Step): undefined {
      switch (s.k) {
        case "focusItem":
          w.entry(null, () => live()[s.i % live().length]?.focus());
          return;
        case "arrow":
          w.entry(null, () => {
            key("ArrowDown");
          });
          return;
        case "home":
          w.entry(null, () => {
            key("Home");
          });
          return;
        case "end":
          w.entry(null, () => {
            key("End");
          });
          return;
        case "refresh":
          w.entry(null, () => {
            if (!m.ended && ctl !== undefined) {
              const list = live();
              const active = document.activeElement;
              m.stop =
                active instanceof HTMLElement && list.includes(active)
                  ? active
                  : (list.find((i) => i.getAttribute("tabindex") === "0") ?? null);
            }
            ctl?.refresh();
          });
          return;
        case "focusFirst":
          w.entry(null, () => ctl?.focusFirst());
          return;
        case "add":
          w.entry(null, addItem);
          return;
        case "focusOutside":
          w.entry(null, () => {
            outside.focus();
          });
          return;
        case "dispose":
        case "owner":
          release(s.k);
          return;
        case "throw":
          throwSentinel(w);
      }
    },
    check(where: string): string | null {
      if (failure !== null) {
        return failure;
      }
      if (m.ended) {
        return firstFailure(where, [
          [w.ledger.count(container, ["keydown", "focusin"]) === 0, "listeners after release"],
        ]);
      }
      const list = live();
      // An item added since the last realization carries no tabindex until the next entry realizes it.
      const realized = list.filter((i) => i.hasAttribute("tabindex"));
      const target = m.stop !== null && realized.includes(m.stop) ? m.stop : (realized[0] ?? null);
      const zeros = realized.filter((i) => i.getAttribute("tabindex") === "0");
      return firstFailure(where, [
        [w.ledger.count(container, ["keydown", "focusin"]) === 2, "listeners"],
        [
          realized.every((i) => ["0", "-1"].includes(i.getAttribute("tabindex") ?? "")),
          "a tabindex other than 0 or -1",
        ],
        [
          zeros.length === (target === null ? 0 : 1) && (target === null || zeros[0] === target),
          "the tab stop",
        ],
      ]);
    },
    finish(): string | null {
      return failure ?? sentinelsOnce(w, isInvalidState);
    },
    cleanup(): void {
      ctl?.dispose();
    },
  };
}

afterEach(() => {
  document.body.replaceChildren();
});

describe("rovingFocus: re-entry property", () => {
  it("exactly one item holds the tab stop, the committed one, through re-entry", async () => {
    await fc.assert(
      fc.asyncProperty(runArb, async (run) => {
        expect(await explore(scenario, run)).toBeNull();
      }),
      {
        numRuns: 150,
        examples: [
          // A release at the first tabindex write writes no other item.
          [
            {
              script: [{ k: "refresh" }],
              injections: [{ at: 0, hit: "attr:tabindex@item0", actions: [{ k: "owner" }] }],
            },
          ],
          [
            {
              script: [{ k: "focusItem", i: 1 }],
              injections: [{ at: 1, hit: "attr:tabindex@item0", actions: [{ k: "dispose" }] }],
            },
          ],
        ],
      },
    );
  }, 60000);
});
