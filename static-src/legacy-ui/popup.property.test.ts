import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import fc from "fast-check";
import { createRoot, runWithOwner } from "@cplieger/reactive";

import { closePopupGroup, createPopup, type PopupController } from "./popup.js";
import {
  type World,
  explore,
  firstFailure,
  isInvalidState,
  sentinelsOnce,
  takesAway,
  throwSentinel,
  type Injection,
  type Scenario,
} from "./__test-helpers__/reentry.js";

// The re-entry property for a grouped popup with a trigger, beside a peer in
// the same group. The model is written from the popup contract: show and hide
// commit at once, a show closes the open peers, light dismissal arms at once
// under the current flags, a hide fades and then hides the panel, a
// same-value option patch changes nothing, and dispose hides at once with no
// onClose. An Escape or outside click dispatched inside a popup's own show() may
// come before or after its listeners arm, so either outcome is accepted. A closed popup whose fade the model cannot place (a show undone
// inside its own realization never revealed the panel) may be fading or hidden.

type Flag = "closeOnOutside" | "closeOnEscape";
type Step =
  | { readonly k: "show" | "hide" | "toggle" | "outside" | "escape" | "peerShow" | "closeGroup" }
  | { readonly k: "setOptions"; readonly flag: Flag; readonly value: boolean }
  | { readonly k: "dispose" | "owner" | "settle" | "focusProbe" | "throw" };

interface Pop {
  open: boolean;
  /** open; closing (fading or hidden, not placeable); fading; hidden (after a settle). */
  phase: "open" | "closing" | "hidden" | "untouched";
  readonly flags: Record<Flag, boolean>;
  /** How many of this popup's show() calls are running. */
  showing: number;
  ended: boolean;
  annotated: "yes" | "maybe" | "no";
  opens: number;
  closes: number;
}

function newPop(): Pop {
  return {
    open: false,
    phase: "untouched",
    flags: { closeOnOutside: true, closeOnEscape: true },
    showing: 0,
    ended: false,
    annotated: "no",
    opens: 0,
    closes: 0,
  };
}

const flagArb = fc.constantFrom<Flag>("closeOnOutside", "closeOnEscape");
const stepArb: fc.Arbitrary<Step> = fc.oneof(
  fc.constantFrom<Step>(
    { k: "show" },
    { k: "hide" },
    { k: "toggle" },
    { k: "outside" },
    { k: "escape" },
    { k: "peerShow" },
    { k: "closeGroup" },
    { k: "dispose" },
    { k: "owner" },
    { k: "settle" },
  ),
  fc.record({ flag: flagArb, value: fc.boolean() }).map((r): Step => ({ k: "setOptions", ...r })),
);
const injArb: fc.Arbitrary<Step> = fc.oneof(
  { weight: 6, arbitrary: stepArb.filter((s) => s.k !== "settle") },
  { weight: 1, arbitrary: fc.constantFrom<Step>({ k: "focusProbe" }, { k: "throw" }) },
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
  const me = newPop();
  const peer = newPop();
  let failure: string | null = null;
  const fail = (why: string): void => {
    failure ??= why;
  };
  const trigger = w.probe("trigger", "button");
  const panel = w.probe("panel");
  const peerPanel = w.probe("peer");
  const outside = w.probe("outside", "button");
  w.quiet(() => {
    panel.hidden = true;
    peerPanel.hidden = true;
    panel.append(w.probe("inside", "button"));
    document.body.append(trigger, panel, peerPanel, outside);
  });
  let ctl: PopupController | undefined;
  let peerCtl: PopupController | undefined;
  // A released popup's terminal pass only takes its panel and trigger state away.
  w.admit = (write) =>
    me.ended && (write.el === panel || write.el === trigger) ? takesAway(write) : undefined;
  let disposeParent = (): void => undefined;
  let lateGetter = false;

  const hook = (pop: Pop, name: "onOpen" | "onClose", label: string) => (): void => {
    if (pop.ended) {
      fail(`${label} ${name} after its release`);
    }
    if (name === "onOpen") {
      if (pop.opens !== pop.closes) {
        fail(`${label} onOpen twice with no onClose between`);
      }
      pop.opens++;
    } else {
      if (pop.opens !== pop.closes + 1) {
        fail(`${label} onClose with no onOpen before it`);
      }
      pop.closes++;
    }
    w.hit(`${label}:${name}`);
  };

  // Option getters are hits too; a read after creation breaks R0.
  const options = (pop: Pop, label: string, withTrigger: boolean): object => {
    const o: Record<string, unknown> = {};
    const values: Record<string, unknown> = {
      group: "g",
      closeOnOutside: true,
      closeOnEscape: true,
      onOpen: hook(pop, "onOpen", label),
      onClose: hook(pop, "onClose", label),
      ...(withTrigger ? { trigger } : {}),
    };
    for (const [key, value] of Object.entries(values)) {
      Object.defineProperty(o, key, {
        enumerable: true,
        get: () => {
          if (created) {
            lateGetter = true;
          }
          w.hit(`getter:${label}:${key}`);
          return value;
        },
      });
    }
    return o;
  };
  let created = false;

  const hideModel = (pop: Pop): void => {
    if (pop.open) {
      pop.open = false;
      pop.phase = "closing";
    }
  };
  const showModel = (pop: Pop, other: Pop): void => {
    if (pop.ended || pop.open) {
      return;
    }
    hideModel(other);
    pop.open = true;
    pop.phase = "open";
    pop.annotated = pop.annotated === "yes" ? "yes" : "maybe";
  };

  const showCall = (pop: Pop, call: () => void): void => {
    pop.showing++;
    try {
      call();
    } finally {
      pop.showing--;
    }
  };
  /** Dispatch a dismissal event: an armed popup it applies to closes; one still
   *  inside its show() takes whichever outcome the controller reports. */
  const dismiss = (flag: Flag, event: Event): void => {
    const unsure: [Pop, PopupController | undefined][] = [];
    for (const [pop, controller] of [
      [me, ctl],
      [peer, peerCtl],
    ] as const) {
      if (pop.open && pop.flags[flag]) {
        if (pop.showing > 0) {
          unsure.push([pop, controller]);
        } else {
          hideModel(pop);
        }
      }
    }
    outside.dispatchEvent(event);
    for (const [pop, controller] of unsure) {
      if (controller?.isOpen === false) {
        hideModel(pop);
      }
    }
  };

  const release = (how: "dispose" | "owner"): void => {
    w.entry(null, () => {
      w.markRelease();
      if (!me.ended) {
        me.ended = true;
        me.open = false;
      }
      if (how === "dispose") {
        ctl?.dispose();
      } else {
        disposeParent();
      }
    });
    const c = ctl;
    if (c !== undefined) {
      w.entry(null, () => {
        c.show();
        c.toggle();
        c.setOptions({ closeOnEscape: false });
        if (c.isOpen) {
          fail("isOpen true after release");
        }
      });
    }
  };

  return {
    start(): void {
      w.entry(null, () => {
        try {
          peerCtl = createPopup(peerPanel, options(peer, "peer", false));
        } catch (error) {
          peer.ended = true;
          throw error;
        }
        const [d, parent] = createRoot((dispose, owner) => [dispose, owner] as const);
        disposeParent = d;
        try {
          ctl = runWithOwner(parent, () => createPopup(panel, options(me, "me", true)));
        } catch (error) {
          // A getter's throw leaves the factory with nothing made.
          me.ended = true;
          throw error;
        }
        created = true;
      });
    },
    act(s: Step): undefined {
      const c = ctl;
      // An option getter's hit comes before the controller exists.
      if (c === undefined && s.k !== "throw" && s.k !== "focusProbe") {
        return;
      }
      switch (s.k) {
        case "show":
          w.entry(null, () => {
            showModel(me, peer);
            showCall(me, () => c?.show());
          });
          return;
        case "hide":
          w.entry(null, () => {
            if (!me.ended) {
              hideModel(me);
            }
            c?.hide();
          });
          return;
        case "toggle":
          w.entry(null, () => {
            if (!me.ended && me.open) {
              hideModel(me);
              c?.toggle();
              return;
            }
            showModel(me, peer);
            showCall(me, () => c?.toggle());
          });
          return;
        case "outside":
          w.entry(null, () => {
            dismiss("closeOnOutside", new MouseEvent("click", { bubbles: true }));
          });
          return;
        case "escape":
          w.entry(null, () => {
            // stopPropagation cannot stop another listener on the same document.
            dismiss(
              "closeOnEscape",
              new KeyboardEvent("keydown", { key: "Escape", bubbles: true }),
            );
          });
          return;
        case "peerShow":
          w.entry(null, () => {
            showModel(peer, me.ended ? newPop() : me);
            showCall(peer, () => peerCtl?.show());
          });
          return;
        case "closeGroup":
          w.entry(null, () => {
            if (!me.ended) {
              hideModel(me);
            }
            hideModel(peer);
            closePopupGroup("g");
          });
          return;
        case "setOptions":
          w.entry(null, () => {
            if (!me.ended && me.flags[s.flag] !== s.value) {
              me.flags[s.flag] = s.value;
            }
            c?.setOptions({ [s.flag]: s.value });
          });
          return;
        case "dispose":
        case "owner":
          release(s.k);
          return;
        case "settle": {
          w.advance(400, null);
          for (const pop of [me, peer]) {
            if (pop.phase === "closing") {
              pop.phase = "hidden";
            }
          }
          return;
        }
        case "focusProbe":
          outside.focus();
          return;
        case "throw":
          throwSentinel(w);
      }
    },
    check(where: string): string | null {
      if (failure !== null) {
        return failure;
      }
      const panelOk = (p: HTMLElement, pop: Pop, label: string): (readonly [boolean, string])[] => {
        const open = p.classList.contains("is-open");
        const leaving = p.classList.contains("is-leaving");
        const view = `${label} hidden=${String(p.hidden)} is-open=${String(open)} is-leaving=${String(leaving)}`;
        if (pop.open) {
          return [[open && !leaving && !p.hidden, `${view}, model open`]];
        }
        const settled = p.hidden && !open && !leaving;
        if (pop.phase === "closing" && !pop.ended) {
          return [[settled || (leaving && !open && !p.hidden), `${view}, model closing`]];
        }
        return [[settled, `${view}, model ${pop.ended ? "ended" : pop.phase}`]];
      };
      const timers = (pop: Pop, p: HTMLElement): number =>
        !pop.open && p.classList.contains("is-leaving") ? 1 : 0;
      const docListeners = (pop: Pop): number =>
        pop.open ? (pop.flags.closeOnOutside ? 1 : 0) + (pop.flags.closeOnEscape ? 1 : 0) : 0;
      const expanded = trigger.getAttribute("aria-expanded");
      const haspopup = trigger.getAttribute("aria-haspopup");
      let aria: boolean;
      if (me.ended || ctl === undefined) {
        aria = expanded === null && haspopup === null;
      } else if (me.open) {
        aria = expanded === "true" && haspopup === "true";
      } else if (me.annotated === "yes") {
        aria = expanded === "false" && haspopup === "true";
      } else if (me.annotated === "maybe") {
        aria =
          (expanded === "false" && haspopup === "true") || (expanded === null && haspopup === null);
      } else {
        aria = expanded === null && haspopup === null;
      }
      const result = firstFailure(where, [
        ...panelOk(panel, me, "panel"),
        ...panelOk(peerPanel, peer, "peer"),
        [
          ctl === undefined || ctl.isOpen === (me.open && !me.ended),
          `isOpen ${String(ctl?.isOpen)}, model ${String(me.open)}`,
        ],
        [
          peerCtl === undefined || peerCtl.isOpen === peer.open,
          `peer isOpen ${String(peerCtl?.isOpen)}`,
        ],
        [
          vi.getTimerCount() === timers(me, panel) + timers(peer, peerPanel),
          `${vi.getTimerCount().toString()} timers`,
        ],
        [
          w.ledger.count(document, ["click", "keydown"]) === docListeners(me) + docListeners(peer),
          `${w.ledger.count(document, ["click", "keydown"]).toString()} document listeners`,
        ],
        [aria, `trigger aria-expanded=${String(expanded)} aria-haspopup=${String(haspopup)}`],
        [
          me.ended || me.opens - me.closes === (me.open ? 1 : 0),
          "onOpen/onClose net differs from the state",
        ],
        [
          peer.opens - peer.closes === (peer.open ? 1 : 0),
          "peer onOpen/onClose net differs from the state",
        ],
        [!lateGetter, "an option getter ran after creation"],
      ]);
      if (me.open && !me.ended) {
        me.annotated = "yes";
      }
      return result;
    },
    finish(): string | null {
      return failure ?? sentinelsOnce(w, isInvalidState);
    },
    cleanup(): void {
      try {
        ctl?.dispose();
        peerCtl?.dispose();
      } catch {
        // Observed already.
      }
    },
  };
}

beforeEach(() => {
  vi.useFakeTimers();
});

afterEach(() => {
  vi.useRealTimers();
  document.body.replaceChildren();
});

describe("popup: re-entry property", () => {
  it("the panel, trigger ARIA, listeners and timers follow the committed state through re-entry", async () => {
    await fc.assert(
      fc.asyncProperty(runArb, async (run) => {
        expect(await explore(scenario, run)).toBeNull();
      }),
      {
        numRuns: 150,
        // Counterexamples found earlier, kept as regressions.
        examples: [
          // A restart at the leave's is-open removal, and at its is-leaving write, still hides the panel at the settle.
          [
            {
              script: [{ k: "show" }, { k: "hide" }, { k: "settle" }],
              injections: [
                {
                  at: 2,
                  hit: "attr:class@panel",
                  actions: [{ k: "setOptions", flag: "closeOnOutside", value: false }],
                },
              ],
            },
          ],
          [
            {
              script: [{ k: "show" }, { k: "hide" }, { k: "settle" }],
              injections: [
                {
                  at: 3,
                  hit: "attr:class@panel",
                  actions: [{ k: "setOptions", flag: "closeOnOutside", value: false }],
                },
              ],
            },
          ],
          // A dispose at the reveal's hidden write still hides the panel.
          [
            {
              script: [{ k: "show" }],
              injections: [{ at: 0, hit: "attr:hidden@panel", actions: [{ k: "dispose" }] }],
            },
          ],
          // A dispose at the first reveal write lands no later reveal write.
          [
            {
              script: [{ k: "show" }],
              injections: [{ at: 0, hit: "attr:class@panel", actions: [{ k: "dispose" }] }],
            },
          ],
          [
            {
              script: [{ k: "show" }, { k: "setOptions", flag: "closeOnOutside", value: false }],
              injections: [{ at: 337, actions: [{ k: "toggle" }, { k: "focusProbe" }] }],
            },
          ],
          [
            {
              script: [{ k: "show" }, { k: "closeGroup" }, { k: "settle" }],
              injections: [{ at: 320, actions: [{ k: "show" }] }],
            },
          ],
        ],
      },
    );
  }, 60000);
});
