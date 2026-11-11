import { describe, it, expect, afterEach } from "vitest";

import { createRoot, effect, setEffectErrorHandler, signal } from "@cplieger/reactive";

import type { Fx } from "./fx.js";
import { SUPERSEDED, createDriver, ownedBinding, type Binding, type Driver } from "./owner.js";

afterEach(() => {
  document.body.replaceChildren();
});

/** Collect what reactive's effect error handler receives while `fn` runs. */
function handled(fn: () => void): unknown[] {
  const seen: unknown[] = [];
  const previous = setEffectErrorHandler((error) => {
    seen.push(error);
  });
  try {
    fn();
  } finally {
    setEffectErrorHandler(previous);
  }
  return seen;
}

/** A binding over one numeric driver whose passes run `steps` through `fx.call`. */
function counter(steps: ((fx: Fx, n: number) => void)[] = [], terminal?: (fx: Fx) => void) {
  const log: string[] = [];
  let driver!: Driver<number>;
  let binding!: Binding;
  const controller = ownedBinding((b) => {
    binding = b;
    driver = createDriver<number>({
      initial: 0,
      realize(fx, n) {
        log.push(`pass ${n.toString()}`);
        for (const step of steps) {
          step(fx, n);
        }
      },
      terminal(fx) {
        log.push("terminal");
        terminal?.(fx);
      },
    });
    return {
      controller: {
        set: (n: number) => {
          if (!b.ended) {
            driver.commit(n);
          }
        },
      },
      start: () => {
        driver.flush();
      },
      release: () => {
        driver.release();
      },
    };
  });
  return { controller, driver: () => driver, binding: () => binding, log };
}

describe("ownedBinding: construction", () => {
  it("throws before make runs under an owner being disposed", () => {
    let made = false;
    let thrown: unknown = null;
    const [dispose, owner] = createRoot((d, o) => [d, o] as const);
    owner.onDispose(() => {
      try {
        ownedBinding(() => {
          made = true;
          return { controller: null, start: () => undefined, release: () => undefined };
        });
      } catch (error) {
        thrown = error;
      }
    });
    dispose();
    expect(made).toBe(false);
    expect(thrown).toBeInstanceOf(Error);
  });

  it("start runs outside the binding's root and untracked", () => {
    const s = signal(0);
    let runs = 0;
    const [dispose] = createRoot((d) => {
      effect(() => {
        runs++;
        ownedBinding(() => ({
          controller: null,
          start: () => {
            void s.value;
          },
          release: () => undefined,
        }));
      });
      return [d] as const;
    });
    s.value = 1;
    expect(runs).toBe(1);
    dispose();
  });

  it("a root start() creates belongs to the caller's owner, not the binding's root", () => {
    let innerDisposed = false;
    let release = (): void => undefined;
    const [disposeOuter] = createRoot((d) => {
      release = ownedBinding((binding) => ({
        controller: binding.dispose,
        start: () => {
          createRoot((_dispose, owner) => {
            owner.onDispose(() => {
              innerDisposed = true;
            });
          });
        },
        release: () => undefined,
      }));
      return [d] as const;
    });
    release();
    expect(innerDisposed).toBe(false);
    disposeOuter();
    expect(innerDisposed).toBe(true);
  });

  it("a start() throw disposes the root, and a terminal throw goes to the handler", () => {
    const startError = new Error("start");
    const terminalError = new Error("terminal");
    let thrown: unknown = null;
    const seen = handled(() => {
      try {
        ownedBinding(() => {
          const d = createDriver<number>({
            initial: 0,
            realize: () => undefined,
            terminal(fx) {
              fx.call(() => {
                throw terminalError;
              });
            },
          });
          return {
            controller: null,
            start: () => {
              throw startError;
            },
            release: () => {
              d.release();
            },
          };
        });
      } catch (error) {
        thrown = error;
      }
    });
    expect(thrown).toBe(startError);
    expect(seen).toEqual([terminalError]);
  });
});

describe("ownedBinding: release", () => {
  it("dispose() outside any drive returns with the terminal pass done; a second dispose does nothing", () => {
    const c = counter();
    c.binding().dispose();
    expect(c.log).toEqual(["pass 0", "terminal"]);
    expect(c.binding().ended).toBe(true);
    c.binding().dispose();
    expect(c.log).toEqual(["pass 0", "terminal"]);
  });

  it("dispose() from inside an external call ends the binding at once and tears down when the call returns", () => {
    const seenInside: boolean[] = [];
    const c = counter([
      (fx, n) => {
        if (n === 1) {
          fx.call(() => {
            c.binding().dispose();
            seenInside.push(c.binding().ended);
            c.controller.set(5);
          });
          c.log.push("after the call");
        }
      },
    ]);
    c.controller.set(1);
    expect(seenInside).toEqual([true]);
    expect(c.log).toEqual(["pass 0", "pass 1", "terminal"]);
    expect(c.driver().state).toBe(1);
  });

  it("an explicit terminal throw leaves dispose(); an owner one goes to the handler", () => {
    const boom = new Error("terminal");
    const c = counter([], (fx) => {
      fx.call(() => {
        throw boom;
      });
    });
    expect(() => {
      c.binding().dispose();
    }).toThrow(boom);

    let disposeOwner = (): void => undefined;
    createRoot((d) => {
      disposeOwner = d;
      counter([], (fx) => {
        fx.call(() => {
          throw boom;
        });
      });
    });
    expect(handled(disposeOwner)).toEqual([boom]);
  });

  it("every terminal step runs even when one throws, and both errors leave together", () => {
    const one = new Error("one");
    const two = new Error("two");
    const ran: string[] = [];
    const c = counter([], (fx) => {
      fx.call(() => {
        ran.push("one");
        throw one;
      });
      fx.call(() => {
        ran.push("two");
        throw two;
      });
    });
    let thrown: unknown = null;
    try {
      c.binding().dispose();
    } catch (error) {
      thrown = error;
    }
    expect(ran).toEqual(["one", "two"]);
    expect(thrown).toBeInstanceOf(AggregateError);
    expect((thrown as AggregateError).errors).toEqual([one, two]);
  });
});

describe("driver: restart and convergence", () => {
  it("a commit inside an external call restarts the pass from the newest state", () => {
    const written: number[] = [];
    let nested = false;
    const d = createDriver<number>({
      initial: 0,
      realize(fx, n) {
        fx.call(() => {
          if (n === 1 && !nested) {
            nested = true;
            d.commit(2);
          }
        });
        written.push(n);
      },
    });
    d.flush();
    d.commit(1);
    expect(written).toEqual([0, 2]);
  });

  it("a derived dependent restarts when its source is released during the dependent's call", () => {
    const depWrites: number[] = [];
    let release = (): void => undefined;
    const dep = createDriver<number>({
      initial: 0,
      realize(fx, n) {
        fx.call(() => {
          if (n === 1) {
            release();
          }
        });
        depWrites.push(n);
      },
    });
    const source = createDriver<number>({
      initial: 0,
      realize(fx) {
        fx.call(() => {
          dep.flush();
        });
      },
      terminal(fx) {
        fx.call(() => {
          dep.flush();
        });
      },
      derive(n, released) {
        dep.set(released ? -1 : n);
      },
    });
    release = (): void => {
      source.release();
    };
    source.flush();
    source.commit(1);
    expect(depWrites).toEqual([0, -1]);
    expect(dep.state).toBe(-1);
  });

  it("a kick from notify after a pass that measured runs one more pass", () => {
    let passes = 0;
    let kicked = false;
    const d = createDriver<number>({
      initial: 0,
      realize(fx) {
        passes++;
        fx.measure(() => 0);
      },
      notify(fx) {
        fx.call(() => {
          if (!kicked) {
            kicked = true;
            d.kick();
          }
        });
      },
    });
    d.flush();
    expect(passes).toBe(2);
  });

  it("a kick from inside the first measure is absorbed by it", () => {
    let passes = 0;
    const d = createDriver<number>({
      initial: 0,
      realize(fx) {
        passes++;
        fx.call(() => {
          d.kick();
        });
        fx.measure(() => 0);
      },
    });
    d.flush();
    expect(passes).toBe(1);
  });

  it("a kick never moves the state", () => {
    const d = createDriver<number>({ initial: 3, realize: () => undefined });
    d.flush();
    const before = d.state;
    d.kick();
    expect(d.state).toBe(before);
  });

  it("stops a loop the app made after 50 restarts, with the error, the state intact and realization behind", () => {
    let n = 0;
    const d = createDriver<number>({
      initial: 0,
      realize(fx) {
        fx.call(() => {
          d.commit(++n);
        });
      },
    });
    expect(() => {
      d.flush();
    }).toThrow(/did not converge/);
    expect(d.state).toBe(n);
    expect(d.released).toBe(false);
  });

  it("a release in the pass that reaches the restart bound still runs the terminal pass", () => {
    let pass = 0;
    let terminated = false;
    const d = createDriver<number>({
      initial: 0,
      realize(fx) {
        fx.call(() => {
          pass++;
          if (pass <= 50) {
            d.commit(pass);
          } else {
            d.release();
          }
        });
      },
      terminal() {
        terminated = true;
      },
    });
    expect(() => {
      d.flush();
    }).not.toThrow();
    expect(d.released).toBe(true);
    expect(terminated).toBe(true);
  });

  it("errors from hooks are collected and thrown after convergence, from the entry", () => {
    const boom = new Error("hook");
    const written: number[] = [];
    const d = createDriver<number>({
      initial: 0,
      realize(fx, n) {
        fx.call(() => {
          throw boom;
        });
        written.push(n);
      },
    });
    expect(() => {
      d.flush();
    }).toThrow(boom);
    expect(written).toEqual([0]);
  });
});

describe("driver: guards, acts and edges", () => {
  it("consult answers SUPERSEDED when the state moved while the guard ran", () => {
    const d = createDriver<number>({ initial: 0, realize: () => undefined });
    d.flush();
    expect(d.consult(() => true)).toBe(true);
    expect(
      d.consult(() => {
        d.commit(1);
        return true;
      }),
    ).toBe(SUPERSEDED);
  });

  it("act is refused once released", () => {
    const d = createDriver<number>({ initial: 0, realize: () => undefined });
    d.flush();
    d.release();
    let ran = false;
    d.act(() => {
      ran = true;
    });
    expect(ran).toBe(false);
  });

  it("update folds an edge and applies the intent in one state change", () => {
    let platform = 0;
    let known = 0;
    const states: number[] = [];
    const d = createDriver<{ readonly seen: number; readonly want: number }>({
      initial: { seen: 0, want: 0 },
      realize(_fx, s) {
        states.push(s.seen * 10 + s.want);
      },
      observe(s) {
        if (platform === known) {
          return s;
        }
        known = platform;
        return { ...s, seen: platform };
      },
    });
    d.flush();
    platform = 1;
    d.update((s) => ({ ...s, want: 2 }));
    d.flush();
    expect(states).toEqual([0, 12]);
  });

  it("set during a release does nothing", () => {
    const d = createDriver<number>({ initial: 0, realize: () => undefined });
    d.flush();
    d.release();
    d.set(4);
    expect(d.state).toBe(0);
  });
});

describe("fx: a move a blur handler interrupts", () => {
  it("append finishes after the handler moved the child elsewhere", () => {
    const parent = document.createElement("div");
    const aside = document.createElement("div");
    const holder = document.createElement("div");
    const child = document.createElement("div");
    const button = document.createElement("button");
    child.append(button);
    holder.append(child);
    document.body.append(holder, parent, aside);
    button.focus();
    button.addEventListener(
      "focusout",
      () => {
        aside.append(child);
      },
      { once: true },
    );
    const d = createDriver<number>({
      initial: 0,
      realize(fx) {
        fx.append(parent, child);
      },
    });
    expect(() => {
      d.flush();
    }).not.toThrow();
    expect(child.parentNode).toBe(parent);
    document.body.replaceChildren();
  });
});
