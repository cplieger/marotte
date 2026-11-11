import { describe, it, expect, afterEach } from "vitest";
import fc from "fast-check";
import { createRoot, runWithOwner, setEffectErrorHandler, type Owner } from "@cplieger/reactive";

import type { Fx } from "./fx.js";
import { createDriver, ownedBinding, type Driver } from "./owner.js";

// The driver and binding model, with no DOM: generated actions run inside the driver's checked
// external calls. The oracles are the driver's contract: no external call starts in a pass whose
// state moved, the registers and the dependent equal the newest state once an entry returns,
// kicks never move the state and restart only a pass that measured before them, the terminal
// pass runs once and calls every step, and every thrown error leaves exactly once.

interface S {
  readonly a: number;
  readonly b: number;
  /** The platform register, folded in by observe. */
  readonly e: number;
}

type Field = "a" | "b" | "e";
type Action =
  | { readonly k: "commit"; readonly field: "a" | "b"; readonly value: number }
  | { readonly k: "kick" | "edge" | "explicit" | "owner" | "throw" | "childRoot" | "sameValue" };

interface Case {
  readonly program: readonly { readonly field: Field; readonly measure: boolean }[];
  readonly entries: readonly Action[];
  readonly actions: readonly { readonly at: number; readonly action: Action }[];
}

const fieldArb = fc.constantFrom<Field>("a", "b", "e");
const commitArb = fc.constantFrom<"a" | "b">("a", "b");
const actionArb: fc.Arbitrary<Action> = fc.oneof(
  fc
    .record({ field: commitArb, value: fc.integer({ min: 1, max: 9 }) })
    .map((r): Action => ({ k: "commit", ...r })),
  fc.constantFrom<Action>(
    { k: "kick" },
    { k: "edge" },
    { k: "explicit" },
    { k: "owner" },
    { k: "throw" },
    { k: "childRoot" },
    { k: "sameValue" },
  ),
);
const caseArb: fc.Arbitrary<Case> = fc.record({
  program: fc.array(fc.record({ field: fieldArb, measure: fc.boolean() }), {
    minLength: 1,
    maxLength: 4,
  }),
  entries: fc.array(actionArb, { minLength: 1, maxLength: 5 }),
  actions: fc.array(fc.record({ at: fc.nat({ max: 30 }), action: actionArb }), { maxLength: 4 }),
});

const TERMINAL = -1;

class Sentinel extends Error {}

function run(c: Case): string | null {
  const violations: string[] = [];
  const regs: Record<Field, number> = { a: 0, b: 0, e: 0 };
  let depReg = 0;
  let platform = 0;
  let known = 0;
  let calls = 0;
  let terminalSteps = 0;
  let terminalPasses = 0;
  let released = false;
  let notified: S | null = null;
  const thrown: Sentinel[] = [];
  const seen: unknown[] = [];
  const scheduled = new Map<number, Action[]>();
  for (const { at, action } of c.actions) {
    scheduled.set(at, [...(scheduled.get(at) ?? []), action]);
  }
  // The kick count when the running pass first measured.
  let measuredAt: number | null = null;
  let kicks = 0;
  let restartOwed = false;
  let disposeParent: () => void = () => undefined;
  let parentDisposed = false;

  const dep = createDriver<{ readonly x: number }>({
    initial: { x: 0 },
    realize(fx, s) {
      fx.call(() => {
        depReg = s.x;
      });
    },
  });

  let driver!: Driver<S>;
  let binding!: { readonly ended: boolean; readonly dispose: () => void };

  const perform = (a: Action): void => {
    switch (a.k) {
      case "commit":
        entry(a);
        return;
      case "kick":
        kicks++;
        driver.kick();
        return;
      case "edge":
        platform++;
        return;
      case "explicit":
        binding.dispose();
        return;
      case "owner":
        parentDisposed = true;
        disposeParent();
        return;
      case "throw": {
        const s = new Sentinel(`s${thrown.length.toString()}`);
        thrown.push(s);
        throw s;
      }
      case "childRoot": {
        // A root made inside a drive belongs to the caller's owner (none for a
        // top-level entry, the parent during the factory call), never to the binding.
        let root: ReturnType<typeof createRoot<Owner>> | null = null;
        try {
          root = createRoot((_d, o) => o);
        } catch {
          // The caller's owner, the parent, was being disposed.
        }
        root?.onDispose(() => {
          if (!parentDisposed) {
            violations.push("a root made inside a drive was owned by the binding");
          }
        });
        return;
      }
      case "sameValue":
        driver.commit(driver.state);
        return;
    }
  };

  const react = (): void => {
    const at = calls++;
    for (const a of scheduled.get(at) ?? []) {
      perform(a);
    }
  };

  const checkFresh = (s: S): void => {
    if (driver.state !== s && !driver.released) {
      violations.push("an external call started in a pass whose state had moved");
    }
  };

  const write = (fx: Fx, s: S, field: Field, measure: boolean): void => {
    checkFresh(s);
    const body = (): void => {
      regs[field] = s[field];
      react();
    };
    if (measure) {
      fx.measure(() => {
        measuredAt ??= kicks;
        body();
      });
    } else {
      fx.call(body);
    }
  };

  const entry = (a: Action): void => {
    if (a.k !== "commit") {
      return;
    }
    if (binding.ended) {
      return;
    }
    driver.commit({ ...driver.state, [a.field]: a.value });
  };

  const previous = setEffectErrorHandler((error) => {
    seen.push(error);
  });
  try {
    const [dp, parent] = createRoot((d, o) => [d, o] as const);
    disposeParent = dp;
    // A hook error in the first pass leaves the factory; the binding is then rolled back.
    let ctl: { ended: () => boolean } | undefined;
    try {
      ctl = runWithOwner(parent, () =>
        ownedBinding((b) => {
          binding = b;
          driver = createDriver<S>({
            initial: { a: 0, b: 0, e: 0 },
            realize(fx, s) {
              measuredAt = null;
              // The generated order and measures, then any field it left out.
              const fields: Field[] = ["a", "b", "e"];
              const steps = [
                ...c.program,
                ...fields
                  .filter((f) => !c.program.some((p) => p.field === f))
                  .map((field) => ({ field, measure: false })),
              ];
              for (const step of steps) {
                write(fx, s, step.field, step.measure);
              }
              checkFresh(s);
              fx.call(() => {
                dep.flush();
              });
              // Read by the entry: a kick after this pass measured must have restarted it.
              restartOwed = measuredAt !== null && kicks !== measuredAt;
            },
            terminal(fx) {
              terminalPasses++;
              for (const field of ["a", "b", "e"] as const) {
                fx.call(() => {
                  terminalSteps++;
                  regs[field] = TERMINAL;
                  react();
                });
              }
              fx.call(() => {
                dep.flush();
              });
            },
            notify(fx, s) {
              fx.call(() => {
                notified = s;
                react();
              });
            },
            observe(s) {
              if (platform === known) {
                return s;
              }
              known = platform;
              return { ...s, e: platform };
            },
            derive(s, isReleased) {
              dep.set({ x: isReleased ? TERMINAL : s.a });
            },
          });
          return {
            controller: { ended: () => b.ended },
            start: () => {
              driver.flush();
            },
            release: () => {
              released = true;
              driver.release();
            },
          };
        }),
      );
    } catch (error) {
      seen.push(error);
    }

    for (const a of c.entries) {
      try {
        perform(a);
        if (a.k === "edge") {
          // Outside any pass, the platform's own event is what folds an edge.
          driver.sync();
          driver.flush();
        }
      } catch (error) {
        seen.push(error);
      }
      if (!released) {
        if (regs.a !== driver.state.a || regs.b !== driver.state.b || regs.e !== driver.state.e) {
          violations.push(
            `registers ${JSON.stringify(regs)} after an entry, state ${JSON.stringify(driver.state)}`,
          );
        }
        if (driver.state.e !== platform) {
          violations.push("an edge was not folded by the time its entry returned");
        }
        if (notified !== driver.state) {
          violations.push("notify did not report the converged state");
        }
        if (depReg !== driver.state.a) {
          violations.push(
            `dependent register ${depReg.toString()}, derived ${driver.state.a.toString()}`,
          );
        }
        if (restartOwed) {
          violations.push("a kick after the pass's first measure did not restart it");
        }
      } else {
        if (
          regs.a !== TERMINAL ||
          regs.b !== TERMINAL ||
          regs.e !== TERMINAL ||
          depReg !== TERMINAL
        ) {
          violations.push("released, but the terminal registers are not all written");
        }
        if (ctl !== undefined && !ctl.ended()) {
          violations.push("released, but the binding reads live");
        }
      }
    }
    if (released && terminalPasses !== 1) {
      violations.push(`${terminalPasses.toString()} terminal passes`);
    }
    if (released && terminalSteps < 3) {
      violations.push("a terminal step was skipped");
    }
    const flat: unknown[] = [];
    const visit = (e: unknown): void => {
      if (e instanceof AggregateError) {
        e.errors.forEach(visit);
      } else {
        flat.push(e);
      }
    };
    seen.forEach(visit);
    for (const e of flat) {
      if (!(e instanceof Sentinel)) {
        violations.push(`unexpected error: ${String(e)}`);
      }
    }
    for (const s of thrown) {
      const n = flat.filter((e) => e === s).length;
      if (n !== 1) {
        violations.push(`${s.message} observed ${n.toString()} times`);
      }
    }
  } finally {
    setEffectErrorHandler(previous);
  }
  return violations[0] ?? null;
}

afterEach(() => {
  document.body.replaceChildren();
});

describe("driver: model", () => {
  it("realizes the newest state through any interleaving of commits, edges, kicks, releases and throws", () => {
    fc.assert(
      fc.property(caseArb, (c) => {
        expect(run(c)).toBeNull();
      }),
      { numRuns: 500 },
    );
  });
});
