import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import fc from "fast-check";

import { ask, _resetForTest } from "./ask.js";
import {
  type World,
  explore,
  firstFailure,
  isInvalidState,
  sentinelsOnce,
  throwSentinel,
  type CloseKind,
  type Run,
  type Scenario,
} from "./__test-helpers__/reentry.js";

// The re-entry property for ask(). One question is live at a time across both
// shapes: a new ask() settles the previous one with its cancel value, a
// gesture settles the live one while its dialog shows it, and a browser close
// of its dialog cancels it.
// Options are read once, before anything else, so an ask() started from an
// option getter is preempted by the outer call.

type Shape = "boolean" | "input";
type Step =
  | { readonly k: "ask"; readonly shape: Shape }
  | { readonly k: "ok" | "cancel" | "escape" | "backdrop" | "submit" | "settle" | "throw" }
  | { readonly k: "platformClose"; readonly kind: Exclude<CloseKind, "form"> };

interface Ev {
  readonly t: "edge";
  readonly shape: Shape;
  readonly open: boolean;
}

const stepArb: fc.Arbitrary<Step> = fc.oneof(
  fc.constantFrom<Shape>("boolean", "input").map((shape): Step => ({ k: "ask", shape })),
  fc.constantFrom<Step>(
    { k: "ok" },
    { k: "cancel" },
    { k: "escape" },
    { k: "backdrop" },
    { k: "submit" },
    { k: "settle" },
  ),
  fc
    .constantFrom<"requestClose" | "closeWatcher" | "close">(
      "requestClose",
      "closeWatcher",
      "close",
    )
    .map((kind): Step => ({ k: "platformClose", kind })),
);
const injArb: fc.Arbitrary<Step> = fc.oneof(
  { weight: 6, arbitrary: stepArb.filter((s) => s.k !== "settle") },
  { weight: 1, arbitrary: fc.constant<Step>({ k: "throw" }) },
);
const runArb: fc.Arbitrary<Run<Step>> = fc.record({
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

interface Q {
  readonly id: number;
  readonly shape: Shape;
  readonly message: string;
  /** What the model expects it to settle to; undefined while live. */
  want: boolean | string | null | undefined;
  got: boolean | string | null | undefined;
  settled: boolean;
}

// "pending": committed to a closed dialog whose open has not landed. ask's driver
// defers a commit made inside that dialog's own drive, and a leave arriving before
// the drive resumes cancels the open, so the dialog never shows.
type Phase = "pending" | "open" | "leaving" | "closed";

function scenario(w: World<Ev>): Scenario<Step> {
  const failure: string | null = null;
  const opener = w.probe("opener", "button");
  w.quiet(() => {
    document.body.append(opener);
    opener.focus();
  });
  const questions: Q[] = [];
  let current: Q | null = null;
  const phase: Record<Shape, Phase> = { boolean: "closed", input: "closed" };
  // Counts leaves, so a settle closes only the fades that began before it.
  const leaves: Record<Shape, number> = { boolean: 0, input: 0 };
  const leave = (shape: Shape): void => {
    if (phase[shape] === "pending" && !opened(shape)) {
      phase[shape] = "closed";
      return;
    }
    phase[shape] = "leaving";
    leaves[shape]++;
  };
  // The asks on the stack, innermost last; each commits at its first hit after its
  // option getter returned (a hit an injection makes inside the getter is a read).
  const frames: { readonly q: Q; committed: boolean; reading: boolean }[] = [];
  const cancelValue = (q: Q): boolean | null => (q.shape === "input" ? null : false);
  const dialogOf = (shape: Shape): HTMLDialogElement | null =>
    document.querySelector<HTMLDialogElement>(
      shape === "input" ? "dialog.uip-ask--input" : "dialog.uip-ask:not(.uip-ask--input)",
    );
  const opened = (shape: Shape): boolean => {
    const d = dialogOf(shape);
    return d !== null && (d.open || w.opening(d));
  };
  const showing = (shape: Shape): boolean => phase[shape] === "open" || phase[shape] === "pending";

  // ask arms a question's gestures in the pass that writes its view, so a gesture
  // answers the live question only while its dialog shows it.
  const shown = (q: Q): boolean =>
    dialogOf(q.shape)?.querySelector(".uip-ask-msg")?.textContent === q.message;
  const settleCurrent = (value: boolean | string | null): void => {
    if (current !== null) {
      current.want = value;
      if (showing(current.shape)) {
        leave(current.shape);
      }
      current = null;
    }
  };
  const commit = (q: Q): void => {
    if (current !== null) {
      current.want = cancelValue(current);
    }
    current = q;
    phase[q.shape] = opened(q.shape) ? "open" : "pending";
    const other: Shape = q.shape === "input" ? "boolean" : "input";
    if (showing(other)) {
      leave(other);
    }
  };
  // Any cancel at the live question's dialog (Escape, requestClose, a close
  // watcher) is its answer; on document in the capture phase, before ask's own.
  w.ownListen(
    document,
    "cancel",
    (e) => {
      if (current !== null && e.target === dialogOf(current.shape) && shown(current)) {
        settleCurrent(cancelValue(current));
      }
    },
    true,
  );
  w.onHit = () => {
    // The edges seen so far happened before this commit point.
    pump();
    const top = frames[frames.length - 1];
    if (top !== undefined && !top.committed && !top.reading) {
      top.committed = true;
      commit(top.q);
    }
  };
  const apply = (ev: Ev): void => {
    if (!ev.open && phase[ev.shape] !== "closed") {
      phase[ev.shape] = "closed";
      if (current?.shape === ev.shape) {
        settleCurrent(cancelValue(current));
      }
    }
  };
  const pump = (): void => {
    for (const ev of w.drain()) {
      apply(ev);
    }
  };

  let asked = 0;
  const askOnce = (shape: Shape): void => {
    const id = ++asked;
    const q: Q = {
      id,
      shape,
      message: `q${id.toString()}`,
      want: undefined,
      got: undefined,
      settled: false,
    };
    questions.push(q);
    const frame = { q, committed: false, reading: false };
    frames.push(frame);
    const opts: Record<string, unknown> = {};
    Object.defineProperty(opts, "title", {
      enumerable: true,
      get: () => {
        frame.reading = true;
        try {
          w.hit(`getter:title:${id.toString()}`);
        } finally {
          frame.reading = false;
        }
        return `title ${id.toString()}`;
      },
    });
    if (shape === "input") {
      opts["input"] = { initialValue: `v${id.toString()}` };
    }
    // An option getter's throw leaves ask() before it commits anything.
    let returned = false;
    w.entry(null, () => {
      void (ask(q.message, opts as never) as Promise<boolean | string | null>).then((v) => {
        q.got = v;
        q.settled = true;
      });
      returned = true;
    });
    frames.pop();
    if (!returned) {
      questions.splice(questions.indexOf(q), 1);
    } else if (!frame.committed) {
      pump();
      commit(q);
    }
  };

  return {
    start(): void {
      // The dialogs are built by the first ask().
    },
    act(s: Step, injected: boolean): Promise<void> | undefined {
      pump();
      const live = current;
      const dlg = live === null ? null : dialogOf(live.shape);
      switch (s.k) {
        case "ask":
          askOnce(s.shape);
          break;
        case "ok":
        case "cancel":
          w.entry(null, () => {
            if (live !== null && dlg !== null) {
              if (shown(live)) {
                settleCurrent(
                  s.k === "cancel"
                    ? cancelValue(live)
                    : live.shape === "input"
                      ? `v${live.id.toString()}`
                      : true,
                );
              }
              dlg
                .querySelector<HTMLButtonElement>(s.k === "ok" ? ".uip-ask-ok" : ".uip-ask-cancel")
                ?.click();
            }
          });
          break;
        case "submit":
          w.entry(null, () => {
            if (live?.shape === "input" && dlg !== null) {
              if (shown(live)) {
                settleCurrent(`v${live.id.toString()}`);
              }
              dlg.querySelector("form")?.requestSubmit();
            }
          });
          break;
        case "escape":
          w.entry(null, () => {
            if (live !== null && dlg !== null) {
              dlg.dispatchEvent(new Event("cancel", { cancelable: true }));
            }
          });
          break;
        case "backdrop":
          w.entry(null, () => {
            if (live !== null && dlg !== null) {
              if (shown(live)) {
                settleCurrent(cancelValue(live));
              }
              dlg.dispatchEvent(new MouseEvent("mousedown", { bubbles: true }));
              dlg.dispatchEvent(new MouseEvent("mouseup", { bubbles: true }));
            }
          });
          break;
        case "platformClose":
          if (dlg !== null) {
            w.platform(dlg, s.kind);
          }
          break;
        case "settle": {
          // A fade begun inside the advance settles after it.
          const before = { ...leaves };
          w.advance(400, null);
          for (const shape of ["boolean", "input"] as const) {
            if (phase[shape] === "leaving" && leaves[shape] === before[shape]) {
              phase[shape] = "closed";
            }
          }
          break;
        }
        case "throw":
          throwSentinel(w);
      }
      pump();
      if (injected) {
        return undefined;
      }
      return (async () => {
        await w.quiesce();
        pump();
        for (let i = 0; i < 5; i++) {
          await Promise.resolve();
        }
      })();
    },
    check(where: string): string | null {
      pump();
      if (failure !== null) {
        return failure;
      }
      const checks: [boolean, string][] = [];
      for (const q of questions) {
        if (q === current) {
          checks.push([!q.settled, `q${q.id.toString()} is live but settled to ${String(q.got)}`]);
        } else {
          checks.push([
            q.settled && q.got === q.want,
            `q${q.id.toString()} settled=${String(q.settled)} to ${String(q.got)}, model ${String(q.want)}`,
          ]);
        }
      }
      for (const shape of ["boolean", "input"] as const) {
        const d = dialogOf(shape);
        if (d === null) {
          continue;
        }
        checks.push(
          [
            d.open === (phase[shape] !== "closed"),
            `${shape} dialog.open ${String(d.open)}, model ${phase[shape]}`,
          ],
          [
            d.classList.contains("is-leaving") === (phase[shape] === "leaving"),
            `${shape} is-leaving, model ${phase[shape]}`,
          ],
        );
      }
      if (current !== null) {
        const d = dialogOf(current.shape);
        checks.push([
          d?.querySelector(".uip-ask-msg")?.textContent === current.message,
          "the live question's message",
        ]);
      }
      return firstFailure(where, checks);
    },
    finish(): string | null {
      return failure ?? sentinelsOnce(w, isInvalidState);
    },
    cleanup(): void {
      w.onHit = null;
      _resetForTest();
    },
  };
}

beforeEach(() => {
  vi.useFakeTimers();
});

afterEach(() => {
  vi.useRealTimers();
  _resetForTest();
  document.body.replaceChildren();
});

describe("ask: re-entry property", () => {
  it("exactly the live question is unsettled and every other one settled as the contract says", async () => {
    await fc.assert(
      fc.asyncProperty(runArb, async (run) => {
        const failure = await explore(scenario, run, {
          onEdge: (d, open): Ev => ({
            t: "edge",
            shape: d.classList.contains("uip-ask--input") ? "input" : "boolean",
            open,
          }),
        });
        expect(failure).toBeNull();
      }),
      {
        numRuns: 150,
        // Counterexamples found earlier, kept as regressions.
        examples: [
          [
            {
              script: [
                { k: "ask", shape: "input" },
                { k: "platformClose", kind: "closeWatcher" },
              ],
              injections: [
                {
                  at: 247,
                  actions: [
                    { k: "platformClose", kind: "close" },
                    { k: "ask", shape: "boolean" },
                  ],
                },
                { at: 235, actions: [{ k: "ask", shape: "input" }] },
              ],
            },
          ],
          [
            {
              script: [
                { k: "ask", shape: "boolean" },
                { k: "ok" },
                { k: "settle" },
                { k: "platformClose", kind: "requestClose" },
              ],
              injections: [
                {
                  at: 0,
                  hit: "doc:beforetoggle:closed",
                  actions: [{ k: "ask", shape: "input" }, { k: "ok" }],
                },
              ],
            },
          ],
          [
            {
              script: [{ k: "ask", shape: "boolean" }],
              injections: [
                { at: 0, hit: "doc:beforetoggle:open", actions: [{ k: "ask", shape: "boolean" }] },
                { at: 0, hit: "doc:focusin", actions: [{ k: "platformClose", kind: "close" }] },
              ],
            },
          ],
          [
            {
              script: [
                { k: "ask", shape: "input" },
                { k: "ask", shape: "boolean" },
                { k: "ask", shape: "boolean" },
                { k: "ok" },
              ],
              injections: [
                { at: 0, hit: "getter:title:2", actions: [{ k: "ask", shape: "input" }] },
              ],
            },
          ],
          [
            {
              script: [{ k: "ask", shape: "boolean" }],
              injections: [
                { at: 0, hit: "getter:title:1", actions: [{ k: "throw" }, { k: "ok" }] },
              ],
            },
          ],
          [
            {
              script: [
                { k: "ask", shape: "boolean" },
                { k: "platformClose", kind: "requestClose" },
              ],
              injections: [
                {
                  at: 0,
                  hit: "doc:focusin",
                  actions: [
                    { k: "ask", shape: "boolean" },
                    { k: "platformClose", kind: "requestClose" },
                  ],
                },
              ],
            },
          ],
          [
            {
              script: [
                { k: "ask", shape: "boolean" },
                { k: "ask", shape: "boolean" },
                { k: "platformClose", kind: "close" },
                { k: "ask", shape: "input" },
              ],
              injections: [
                {
                  at: 1,
                  hit: "doc:beforetoggle:open",
                  actions: [{ k: "ask", shape: "input" }, { k: "ok" }],
                },
              ],
            },
          ],
        ],
      },
    );
  }, 60000);
});
