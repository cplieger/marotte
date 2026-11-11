import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import fc from "fast-check";
import { createRoot, runWithOwner } from "@cplieger/reactive";

import { createDisclosure, type DisclosureController } from "./disclosure.js";
import {
  type World,
  changedStyles,
  explore,
  firstFailure,
  isInvalidState,
  sentinelsOnce,
  throwSentinel,
  type Injection,
  type Scenario,
} from "./__test-helpers__/reentry.js";

// The re-entry property for a disclosure. Every mutator commits the open state
// at once; at a quiescent point the trigger and region reflect it, the height
// is settled or tweening toward it, and onToggle has reported each net change
// once. A released disclosure keeps its state reflected with a settled height.

type Trigger = "native" | "custom" | "none";
type Step =
  | { readonly k: "open" | "close" | "toggle" | "click" | "enter" }
  | { readonly k: "dispose" | "owner" | "settle" | "throw" };

interface Case {
  readonly trigger: Trigger;
  readonly animate: boolean;
  readonly initial: boolean;
  readonly script: readonly Step[];
  readonly injections: readonly Injection<Step>[];
}

const stepArb = fc.constantFrom<Step>(
  { k: "open" },
  { k: "close" },
  { k: "toggle" },
  { k: "click" },
  { k: "enter" },
  { k: "dispose" },
  { k: "owner" },
  { k: "settle" },
);
const injArb: fc.Arbitrary<Step> = fc.oneof(
  { weight: 6, arbitrary: stepArb.filter((s) => s.k !== "settle") },
  { weight: 1, arbitrary: fc.constant<Step>({ k: "throw" }) },
);
const caseArb: fc.Arbitrary<Case> = fc.record({
  trigger: fc.constantFrom<Trigger>("native", "custom", "none"),
  animate: fc.boolean(),
  initial: fc.boolean(),
  script: fc.array(stepArb, { minLength: 1, maxLength: 8 }),
  injections: fc.array(
    fc.record({
      at: fc.nat({ max: 400 }),
      actions: fc.array(injArb, { minLength: 1, maxLength: 2 }),
    }),
    { maxLength: 2 },
  ),
});

function scenarioFor(c: Case) {
  return (w: World): Scenario<Step> => {
    let failure: string | null = null;
    const fail = (why: string): void => {
      failure ??= why;
    };
    const m = {
      open: c.initial,
      ended: false,
      reported: c.initial,
      toggles: 0,
      everTweened: false,
    };
    const trigger =
      c.trigger === "none"
        ? null
        : c.trigger === "native"
          ? w.probe("trigger", "button")
          : w.probe("trigger");
    const region = w.probe("region");
    w.quiet(() => {
      const content = document.createElement("div");
      content.style.height = "60px";
      region.style.display = "block";
      region.style.overflow = "hidden";
      region.append(content);
      if (trigger !== null) {
        document.body.append(trigger);
      }
      document.body.append(region);
    });
    let ctl: DisclosureController | undefined;
    let disposeParent = (): void => undefined;
    let clicking = false;
    // A live disclosure's height steps through its tween; a released one writes only its final height.
    w.admit = (write) => {
      if (m.ended || write.el !== region || write.name !== "style") {
        return undefined;
      }
      const changed = changedStyles(write);
      return changed.size === 1 && changed.has("height") ? true : undefined;
    };

    const onToggle = (open: boolean): void => {
      if (m.ended) {
        fail("onToggle after release");
      }
      if (open === m.reported) {
        fail(`onToggle(${String(open)}) repeats the last report`);
      }
      m.reported = open;
      m.toggles++;
      w.hit("onToggle");
    };
    const set = (next: boolean): void => {
      if (!m.ended) {
        m.open = next;
      }
    };
    const release = (how: "dispose" | "owner"): void => {
      w.entry(null, () => {
        m.ended = true;
        if (how === "dispose") {
          ctl?.dispose();
        } else {
          disposeParent();
        }
      });
      const d = ctl;
      if (d !== undefined) {
        w.entry(null, () => {
          d.open();
          d.close();
          d.toggle();
        });
        w.entry(null, () => trigger?.click());
      }
    };

    return {
      start(): void {
        w.entry(null, () => {
          const [d, parent] = createRoot((dispose, owner) => [dispose, owner] as const);
          disposeParent = d;
          try {
            ctl = runWithOwner(parent, () =>
              createDisclosure(trigger, region, { animate: c.animate, open: c.initial, onToggle }),
            );
          } catch (error) {
            // An error in the first realization rolls the controller back.
            m.ended = true;
            throw error;
          }
        });
      },
      act(s: Step): undefined {
        const d = ctl;
        // A setup hit offers the owner's release and the trigger's events, not the methods.
        if (
          d === undefined &&
          s.k !== "throw" &&
          s.k !== "owner" &&
          s.k !== "click" &&
          s.k !== "enter"
        ) {
          // dispose() and the methods need the controller the factory has not returned yet.
          return;
        }
        switch (s.k) {
          case "open":
          case "close": {
            const k = s.k;
            w.entry(null, () => {
              set(k === "open");
              d?.[k]();
            });
            return;
          }
          case "toggle":
            w.entry(null, () => {
              set(!m.open);
              d?.toggle();
            });
            return;
          case "click":
            w.entry(null, () => {
              // click() on an element whose own click is being dispatched does nothing.
              if (trigger !== null && !clicking) {
                clicking = true;
                try {
                  set(!m.open);
                  trigger.click();
                } finally {
                  clicking = false;
                }
              }
            });
            return;
          case "enter":
            w.entry(null, () => {
              if (trigger !== null) {
                // A native button activates on its own click; Enter on it is the page's.
                if (c.trigger === "custom") {
                  set(!m.open);
                }
                trigger.dispatchEvent(
                  new KeyboardEvent("keydown", { key: "Enter", bubbles: true }),
                );
              }
            });
            return;
          case "dispose":
          case "owner":
            release(s.k);
            return;
          case "settle":
            w.advance(400, null);
            return;
          case "throw":
            throwSentinel(w);
        }
      },
      check(where: string): string | null {
        if (failure !== null) {
          return failure;
        }
        const h = region.style.height;
        const timers = vi.getTimerCount();
        const heightOk = !m.open
          ? h === "0px" && timers === 0
          : (h === "" && timers === 0) ||
            (!m.ended && c.animate && h !== "" && h !== "0px" && timers === 1);
        const listeners = trigger === null ? 0 : w.ledger.count(trigger, ["click", "keydown"]);
        const wantListeners = m.ended || trigger === null ? 0 : c.trigger === "custom" ? 2 : 1;
        return firstFailure(where, [
          [
            trigger === null || trigger.getAttribute("aria-expanded") === String(m.open),
            "aria-expanded",
          ],
          // A release during setup ends it before the wiring is written.
          [
            m.ended || trigger === null || trigger.getAttribute("aria-controls") === region.id,
            "aria-controls",
          ],
          [region.getAttribute("aria-hidden") === String(!m.open), "aria-hidden"],
          [region.inert === !m.open, "inert"],
          [m.ended || region.classList.contains("uip-disclosure-region"), "region class"],
          [
            heightOk,
            `height "${h}" with ${timers.toString()} timers, model open=${String(m.open)}`,
          ],
          [listeners === wantListeners, `${listeners.toString()} trigger listeners`],
          [m.ended || m.reported === m.open, "the last onToggle differs from the state"],
          [m.ended || ctl?.isOpen === m.open, "isOpen"],
        ]);
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

describe("disclosure: re-entry property", () => {
  it("the trigger, region and height follow the committed state through re-entry", async () => {
    await fc.assert(
      fc.asyncProperty(caseArb, async (c) => {
        expect(await explore(scenarioFor(c), c)).toBeNull();
      }),
      {
        numRuns: 150,
        examples: [
          // A dispose at the trigger's aria-expanded write of an animated open starts no tween.
          [
            {
              trigger: "custom",
              animate: true,
              initial: false,
              script: [{ k: "open" }],
              injections: [
                { at: 1, hit: "attr:aria-expanded@trigger", actions: [{ k: "dispose" }] },
              ],
            },
          ],
        ],
      },
    );
  }, 60000);
});
