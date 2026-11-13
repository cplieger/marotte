// scroll-lock.ts — INTERNAL: the body scroll lock nested modals share. iOS
// Safari ignores `overflow: hidden` on the root for touch-scroll, so the body
// is pinned with `position: fixed` at the negated scroll offset instead.
//
// The lock is held while any registered holder is live, and a holder's
// liveness is read from its dialog at every check, so a close or open the
// browser makes during a lock write stops the writes for the old target.

import type { StyleProp } from "./fx.js";
import { createDriver, type Driver } from "./owner.js";

type LockProp = Extract<StyleProp, "position" | "top" | "left" | "right" | "width" | "overflow">;
const PROPS: readonly LockProp[] = ["position", "top", "left", "right", "width", "overflow"];

interface LockState {
  readonly held: ReadonlySet<symbol>;
}

/** Each holder's live state, read at every check. */
const holders = new Map<symbol, () => boolean>();
/** The page's own body styles and scroll offset, kept across a restart. */
let saved: { readonly y: number; readonly styles: Readonly<Record<LockProp, string>> } | null =
  null;

function liveHolders(): ReadonlySet<symbol> {
  const held = new Set<symbol>();
  for (const [holder, live] of holders) {
    if (live()) {
      held.add(holder);
    }
  }
  return held;
}

function sameSet(a: ReadonlySet<symbol>, b: ReadonlySet<symbol>): boolean {
  return a.size === b.size && [...a].every((h) => b.has(h));
}

function lockValue(prop: LockProp, y: number): string {
  switch (prop) {
    case "position":
      return "fixed";
    case "top":
      return `-${y.toString()}px`;
    case "left":
    case "right":
      return "0";
    case "width":
      return "100%";
    case "overflow":
      return "hidden";
  }
}

function makeLock(): Driver<LockState> {
  return createDriver<LockState>({
    initial: { held: new Set() },
    observe(s) {
      const held = liveHolders();
      return sameSet(held, s.held) ? s : { held };
    },
    realize(fx, s) {
      const body = document.body;
      if (s.held.size > 0) {
        saved ??= {
          y: window.scrollY,
          styles: Object.fromEntries(PROPS.map((p) => [p, body.style[p]])) as Record<
            LockProp,
            string
          >,
        };
        for (const prop of PROPS) {
          fx.setStyle(body, prop, lockValue(prop, saved.y));
        }
        return;
      }
      if (saved === null) {
        return;
      }
      for (const prop of PROPS) {
        fx.setStyle(body, prop, saved.styles[prop]);
      }
      const y = saved.y;
      saved = null;
      window.scrollTo(0, y);
    },
  });
}

let lock = makeLock();

/** Register `holder` with its live state, or withdraw it with `null`. Sets
 *  the lock's state without driving it. */
export function holdScrollLock(holder: symbol, live: (() => boolean) | null): void {
  if (live === null) {
    holders.delete(holder);
  } else {
    holders.set(holder, live);
  }
  lock.sync();
}

/** Realize the lock for its current holders. */
export function flushScrollLock(): void {
  lock.sync();
  lock.flush();
}

/** Test-only: restore the page's body styles and forget every holder. */
export function _resetScrollLockForTest(): void {
  if (saved !== null) {
    for (const prop of PROPS) {
      document.body.style[prop] = saved.styles[prop];
    }
  }
  saved = null;
  holders.clear();
  lock = makeLock();
}
