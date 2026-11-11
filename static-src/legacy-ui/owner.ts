// owner.ts — INTERNAL: the driver every controller is built on, and the owned
// binding that ends a controller with the reactive owner current at its call.
//
// A driver holds one committed desired state and realizes it through checked
// external calls (fx.ts). Code outside the library runs only inside those
// calls; when it changes the state, folds a platform change into it, kicks
// after the pass measured, or releases the driver, the pass is stale and the
// loop restarts realization from the newest state before its next effect.

import { createRoot, untracked } from "@cplieger/reactive";

import { fxAt, type Fx } from "./fx.js";

/** Returned by `consult` when the state moved or the driver was released while the guard ran. */
export const SUPERSEDED: unique symbol = Symbol("superseded");

/** Thrown by a pass's checks and caught only by the drive or act that made that pass. */
const STALE: Error = Object.freeze(new Error("legacy-ui: stale pass"));

/** A restart needs app code to change the state again inside this driver's own
 *  calls, so this many in one drive is a loop the app made; React bounds the
 *  same mechanism at 50 (NESTED_UPDATE_LIMIT in react-reconciler's ReactFiberWorkLoop.js). */
const RESTART_LIMIT = 50;

/** How a driver realizes its state. */
interface DriverSpec<S> {
  readonly initial: S;
  /** Move the DOM toward `s`; read the DOM only to diff it and to see a platform
   *  call's outcome, and write only what differs. */
  realize(fx: Fx, s: S): void;
  /** Realize the released target: give back every resource and call no user hook. */
  terminal?(fx: Fx, s: S): void;
  /** After a converged pass: report the net change since the last report. */
  notify?(fx: Fx, s: S): void;
  /** Set the drivers whose state derives from this one. Runs inside every state
   *  change and the release, so it may only call `set` or `sync`. */
  derive?(s: S, released: boolean): void;
  /** Fold a change the platform made on its own into `s`, recording it as seen.
   *  Runs no app code; returns `s` itself when there is none. */
  observe?(s: S): S;
}

/** One desired state, its revision and its realization loop. */
export interface Driver<S> {
  readonly state: S;
  // deadset:ignore DS1301 -- the driver tests read it to tell a released driver from a live one after a pass
  readonly released: boolean;
  /** Store `next` and move the revision without starting a drive. */
  set(next: S): void;
  /** Drive now when idle and the current revision or kick is not yet realized. */
  flush(): void;
  /** `set`, then `flush`. */
  commit(next: S): void;
  /** One `set` of `f` applied to the state with any platform change folded in. */
  update(f: (s: S) => S): void;
  /** Fold any platform change into the state with one `set`. */
  sync(): void;
  /** An outside fact that realization measures has changed. */
  kick(): void;
  /** Make the terminal target current. Idempotent. */
  release(): void;
  /** Move the revision with no state change: realization has work the state does not describe. */
  invalidate(): void;
  /** Call a guard whose answer decides the next state; SUPERSEDED when the state moved
   *  meanwhile, a platform change included, or the driver was released. */
  consult<R>(fn: () => R): R | typeof SUPERSEDED;
  /** One stateless effect now; refused once released. */
  act(body: (fx: Fx) => void): void;
}

function rethrow(errors: readonly unknown[]): void {
  if (errors.length === 1) {
    throw errors[0];
  }
  if (errors.length > 1) {
    throw new AggregateError(errors, "legacy-ui: several callbacks threw");
  }
}

function collector(errors: unknown[]): (error: unknown) => void {
  return (error) => {
    if (error === STALE) {
      throw STALE;
    }
    errors.push(error);
  };
}

/** Create a driver; nothing runs until its first `flush`. */
export function createDriver<S>(spec: DriverSpec<S>): Driver<S> {
  let state = spec.initial;
  let revision = 0;
  let realized = -1;
  let kicks = 0;
  let realizedKicks = 0;
  let released = false;
  let driving = false;

  const set = (next: S): void => {
    if (released || Object.is(state, next)) {
      return;
    }
    state = next;
    revision++;
    spec.derive?.(state, false);
  };
  const sync = (): void => {
    if (spec.observe !== undefined) {
      set(spec.observe(state));
    }
  };

  const drive = (): void => {
    driving = true;
    const errors: unknown[] = [];
    let passes = 0;
    try {
      for (;;) {
        sync();
        const r = revision;
        let measured: number | null = null;
        const stale = (): boolean => revision !== r || (measured !== null && kicks !== measured);
        const fx = fxAt({
          check: () => {
            sync();
            if (stale()) {
              throw STALE;
            }
          },
          measured: () => {
            measured ??= kicks;
          },
          collect: collector(errors),
        });
        let restarted = false;
        try {
          untracked(() => {
            if (released) {
              spec.terminal?.(fx, state);
            } else {
              spec.realize(fx, state);
              if (!stale()) {
                spec.notify?.(fx, state);
              }
            }
          });
        } catch (error) {
          if (error !== STALE) {
            errors.unshift(error);
            break;
          }
          restarted = true;
        }
        if (!restarted && !stale()) {
          realized = r;
          realizedKicks = kicks;
          break;
        }
        // A released driver cannot restart, so its terminal pass is never cut off.
        if (!released && ++passes > RESTART_LIMIT) {
          errors.push(new Error("legacy-ui: a controller's realization did not converge"));
          break;
        }
      }
    } finally {
      driving = false;
    }
    rethrow(errors);
  };

  const flush = (): void => {
    if (!driving && (revision !== realized || kicks !== realizedKicks)) {
      drive();
    }
  };

  return {
    get state(): S {
      return state;
    },
    get released(): boolean {
      return released;
    },
    set,
    sync,
    flush,
    commit(next) {
      set(next);
      flush();
    },
    update(f) {
      set(f(spec.observe === undefined ? state : spec.observe(state)));
    },
    kick() {
      if (released) {
        return;
      }
      kicks++;
      flush();
    },
    release() {
      if (released) {
        return;
      }
      released = true;
      revision++;
      spec.derive?.(state, true);
      flush();
    },
    invalidate() {
      if (!released) {
        revision++;
      }
    },
    consult(fn) {
      // Folded on both sides, so a platform change the guard made counts as a move.
      sync();
      const r = revision;
      const answer = untracked(fn);
      sync();
      return released || revision !== r ? SUPERSEDED : answer;
    },
    act(body) {
      if (released) {
        return;
      }
      const errors: unknown[] = [];
      const fx = fxAt({
        check: () => {
          if (released) {
            throw STALE;
          }
        },
        measured: () => undefined,
        collect: collector(errors),
      });
      // While the effect runs, a release or commit from code it reaches only sets,
      // as in a drive; the drive runs once the effect returns.
      const outer = driving;
      driving = true;
      try {
        untracked(() => {
          body(fx);
        });
      } catch (error) {
        if (error !== STALE) {
          errors.unshift(error);
        }
      } finally {
        driving = outer;
      }
      if (!outer) {
        try {
          flush();
        } catch (error) {
          errors.push(error);
        }
      }
      rethrow(errors);
    },
  };
}

/** What `make` hands back to `ownedBinding`. */
export interface Binding {
  /** True from the instruction that requested the release. */
  readonly ended: boolean;
  /** Release, then dispose the binding's root. Idempotent. */
  readonly dispose: () => void;
}

interface BindingParts<T> {
  readonly controller: T;
  /** Realize the initial state. Runs after the root is made, outside it and untracked. */
  readonly start: () => void;
  /** Commit the terminal target into every driver this binding feeds. */
  readonly release: () => void;
}

/** A binding that ends with the `@cplieger/reactive` owner current at the call.
 *  `make` must run no code outside the library: under an owner being disposed
 *  this throws before it runs. A teardown error from an owner's disposal goes
 *  to reactive's effect error handler; one from `dispose()` leaves `dispose()`. */
export function ownedBinding<T>(make: (binding: Binding) => BindingParts<T>): T {
  let ended = false;
  let parts: BindingParts<T> | null = null;
  let disposeRoot = (): void => undefined;
  const requestRelease = (): void => {
    if (ended) {
      return;
    }
    ended = true;
    parts?.release();
  };
  const binding: Binding = {
    get ended(): boolean {
      return ended;
    },
    dispose: () => {
      // Teardown first, in the caller's context; the root's own callback then finds it done.
      try {
        requestRelease();
      } finally {
        disposeRoot();
      }
    },
  };
  const made = createRoot((dispose, owner) => {
    disposeRoot = dispose;
    const p = make(binding);
    parts = p;
    owner.onDispose(requestRelease);
    return p;
  });
  try {
    untracked(made.start);
  } catch (error) {
    disposeRoot();
    throw error;
  }
  return made.controller;
}
