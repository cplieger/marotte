// dialog-engine.ts — INTERNAL: the one owner of every <dialog> the library
// drives. openDialog, closeDialog, createDialog, createModal and ask all reach
// a dialog through its engine, which holds its phase, its live regions, its
// fade, its closeDialog callbacks and its view of the opens and closes the
// browser makes on its own. An API that adds behaviour to a dialog
// does so as the engine's one controller facet, realized inside its passes.

import type { Fx } from "./fx.js";
import { keepRegions } from "./live-regions.js";
import { trackOpens } from "./modal-host.js";
import { type SUPERSEDED, createDriver } from "./owner.js";
import { watchSettle } from "./transition.js";

/** A dialog's committed state. */
interface DialogState {
  readonly phase: "closed" | "open" | "leaving";
  /** Counts leaves; the settle token. */
  readonly leave: number;
  /** The dialog was open when the leave was asked: a leave of one never shown has
   *  nothing to fade, and one the engine's own earlier close closed is shown again. */
  readonly shown: boolean;
  /** Counts closed-to-open changes; the initial-focus token. */
  readonly opens: number;
  /** The close a closeDialog callback waits for; moves at every reclaim. */
  readonly cycle: number;
  readonly facet: DialogFacet | null;
  /** The facet's content token (ask's question id; 0 otherwise). */
  readonly subject: number;
  /** A library-built dialog its facet has spent: closed for good. */
  readonly retired: boolean;
}

/** The part of a dialog's realization one API adds. Every hook runs inside the engine's pass. */
export interface DialogFacet {
  /** Before the dialog steps, every pass while attached. */
  prepare?(fx: Fx, d: DialogState): void;
  /** After the dialog steps, every pass while attached. */
  follow?(fx: Fx, d: DialogState): void;
  /** After convergence, while attached. */
  notify?(fx: Fx, d: DialogState): void;
  /** After detach, each pass until it returns. Idempotent. */
  cleanup?(fx: Fx): void;
  /** Set the drivers derived from this dialog's state (`null` once detached). Only `set` or `sync`. */
  derive?(d: DialogState | null): void;
  /** Take a refused open instead of the engine reporting it. */
  refused?(subject: number, error: unknown): void;
}

export interface DialogEngine {
  readonly state: DialogState;
  /** The phase with every browser change so far folded in. It commits nothing; a
   *  change it reads is realized at the next microtask. */
  readonly phase: DialogState["phase"];
  /** The methods below commit at once and run no app code; the caller flushes. */
  open(subject?: number): void;
  close(onClosed?: () => void): void;
  attach(facet: DialogFacet): void;
  detach(facet: DialogFacet): void;
  /** `detach`, and closed for good: for a library-built dialog its facet spends. */
  retire(facet: DialogFacet): void;
  flush(): void;
  consult<R>(fn: () => R): R | typeof SUPERSEDED;
  /** The closeDialog callbacks still held, for tests. */
  readonly outboxSize: number;
}

function invalidState(message: string): DOMException {
  return new DOMException(message, "InvalidStateError");
}

const engines = new WeakMap<HTMLDialogElement, DialogEngine>();

/** Each engine's own call under way, told of every `beforetoggle` of its dialog as the
 *  dispatch starts. */
const starts = new WeakMap<HTMLDialogElement, (e: ToggleEvent) => void>();
const startRoots = new WeakSet<ShadowRoot>();

function onStart(e: Event): void {
  if (e.target instanceof HTMLDialogElement) {
    starts.get(e.target)?.(e as ToggleEvent);
  }
}

// A capture listener on the window from load runs before those the page adds later, so
// an event is seen before code its listeners reach starts a nested one.
addEventListener("beforetoggle", onStart, true);

/** A dialog's `beforetoggle` stays inside its shadow root, so the root is listened on too. */
function listenForStarts(dialog: HTMLDialogElement): void {
  const root = dialog.getRootNode();
  if (root instanceof ShadowRoot && !startRoots.has(root)) {
    startRoots.add(root);
    root.addEventListener("beforetoggle", onStart, true);
  }
}

/** The element's engine when it has one. Creates nothing. */
export function existingEngine(dialog: HTMLDialogElement): DialogEngine | undefined {
  return engines.get(dialog);
}

/** The element's engine, created with its `open` observer on first use. */
export function dialogEngine(dialog: HTMLDialogElement): DialogEngine {
  let engine = engines.get(dialog);
  if (engine === undefined) {
    engine = createEngine(dialog);
    engines.set(dialog, engine);
  }
  return engine;
}

interface OwnCall {
  readonly state: "open" | "closed";
  event: Event | null;
  // The call's showModal has written `open`: its focus steps may close the dialog
  // again before the call returns.
  opened: boolean;
}

interface Waiter {
  readonly fn: () => void;
  readonly cycle: number;
  due: boolean;
}

function createEngine(dialog: HTMLDialogElement): DialogEngine {
  let watch: { readonly leave: number; readonly cancel: () => void } | null = null;
  // Since the record (the last observation), the page added `open`, removed it,
  // or added it after the element had been open: the level alone cannot show these.
  // The first record is D's initial `closed`, so an element open at creation was opened by the page.
  let pageOpened = dialog.open;
  let pageClosed = false;
  let reopened = false;
  // The element has been open at some moment since the record.
  let seenOpen = dialog.open;
  // The engine's own open wrote `open` after a page close since the record, and no
  // page open followed: the close is the page's newest edge, so a leave or a teardown
  // it completed stays complete.
  let closedUnderOwn = false;
  // The edge the engine's own showModal (true) or close (false) writes next, from the
  // end of its `beforetoggle` dispatch until a record makes it (`own`). The write follows
  // at once and fx's check after the call takes its record, so it is null again when
  // `own` returns; `own` never nests, since one engine's passes do not.
  let expected: boolean | null = null;

  /** Reduce `open` records, oldest first. A record holds the presence before its
   *  write; the next record's, or the live level for the last, is the presence
   *  after it, so a write of the value the attribute has is no edge. True when a
   *  record is not the engine's own edge: the page wrote `open`. */
  const take = (records: readonly MutationRecord[]): boolean => {
    let page = false;
    for (const [i, r] of records.entries()) {
      const before = r.oldValue !== null;
      const next = records[i + 1];
      const after = next === undefined ? dialog.open : next.oldValue !== null;
      if (before !== after && expected === after) {
        expected = null;
        if (call !== null) {
          call.opened ||= after;
        }
        closedUnderOwn ||= after && pageClosed;
      } else {
        page = true;
        if (!before && after) {
          closedUnderOwn = false;
          pageOpened = true;
          reopened ||= seenOpen;
        } else if (before && !after) {
          pageClosed = true;
        }
      }
      seenOpen = true;
    }
    return page;
  };
  // A microtask checkpoint delivers the records no check took: a page change
  // outside every engine call. It is realized at once, since a dialog fires no
  // `toggle` for some changes (an `open` attribute write, a show() in its own `toggle`).
  const opens = new MutationObserver((records) => {
    take(records);
    realizePageChange();
  });
  opens.observe(dialog, { attributes: true, attributeFilter: ["open"], attributeOldValue: true });
  // From creation, so the page's own opening has a pair before the adoption pass.
  keepRegions(dialog);
  trackOpens(dialog);
  /** The engine's own showModal() or close() under way, and its event once it starts. */
  let call: OwnCall | null = null;
  /** The callbacks a teardown inside the engine's own showModal found waiting: that
   *  open's outcome decides whether the teardown cut their fade. */
  let cut: Waiter[] | null = null;
  starts.set(dialog, (e) => {
    if (call !== null && call.event === null && e.newState === call.state) {
      call.event = e;
    }
  });
  const onEnd = (e: Event): void => {
    const c = call;
    if (c?.event !== e) {
      return;
    }
    if (take(opens.takeRecords())) {
      queueMicrotask(realizePageChange);
    }
    const armed =
      c.state === "open"
        ? !e.defaultPrevented &&
          !dialog.open &&
          dialog.isConnected &&
          !dialog.matches(":popover-open")
        : dialog.open;
    expected = armed ? c.state === "open" : null;
  };
  /** Run `fn`, the engine's own showModal() (`edge` true) or close(), so that only
   *  the edge it writes is taken as the engine's. Its `beforetoggle` listeners run first
   *  and can open or close the dialog themselves: those edges are the page's. When the
   *  dialog's last listener sees the call's own event, no code runs before the write
   *  (HTML, the showModal() and close-the-dialog steps). Returns the call's result and
   *  whether its event fired (C3 reads it). */
  const own = <R>(edge: boolean, fn: () => R): [R, boolean] => {
    const mine: OwnCall = { state: edge ? "open" : "closed", event: null, opened: false };
    call = mine;
    listenForStarts(dialog);
    // Added again so it runs after every listener the page added since.
    dialog.removeEventListener("beforetoggle", onEnd);
    dialog.addEventListener("beforetoggle", onEnd);
    try {
      const result = fn();
      return [result, mine.event !== null];
    } finally {
      call = null;
      const waiting = cut;
      cut = null;
      if (waiting !== null && (dialog.open || mine.opened)) {
        // The open went through, so the teardown cut a fade: drop its callbacks.
        for (const w of waiting) {
          const i = outbox.indexOf(w);
          if (i !== -1) {
            outbox.splice(i, 1);
          }
        }
      }
    }
  };
  // The last pass ended inside a close of the dialog that C3 could not make: it
  // is not realized until that close finishes, so it notifies nothing.
  let closing = false;
  const prepared = new Set<DialogFacet>();
  let derivedFor: DialogFacet | null = null;
  const outbox: Waiter[] = [];

  const fold = (d: DialogState): DialogState => {
    // Records taken here never reach the callback: a page change among them is
    // realized at the microtask it would have been, whatever folds it meanwhile.
    if (take(opens.takeRecords())) {
      queueMicrotask(realizePageChange);
    }
    if (d.retired) {
      return d;
    }
    if (closedUnderOwn && d.phase !== "open") {
      // The engine's own open wrote after a page close: that close stands, whatever
      // the focus steps did after, and a page reopen before it drops the fade's
      // callbacks.
      return d.phase === "closed"
        ? d
        : { ...d, phase: "closed", cycle: reopened ? d.cycle + 1 : d.cycle };
    }
    if (!dialog.open) {
      // The page's close is the newest edge, whatever D intends. The cycle stays:
      // the callbacks waiting for that close are due.
      return pageClosed && d.phase !== "closed" ? { ...d, phase: "closed" } : d;
    }
    return reopened || (pageOpened && d.phase === "closed")
      ? { ...d, phase: "open", opens: d.opens + 1, cycle: d.cycle + 1 }
      : d;
  };

  const cancelWatch = (): void => {
    watch?.cancel();
    watch = null;
  };

  const settle = (leave: number): void => {
    driver.update((d) =>
      d.phase === "leaving" && d.leave === leave ? { ...d, phase: "closed" } : d,
    );
    driver.flush();
  };

  /** Cleanup for every facet that has been replaced or detached. */
  const cleanupReplaced = (fx: Fx, current: DialogFacet | null): void => {
    for (const facet of [...prepared]) {
      if (facet !== current) {
        facet.cleanup?.(fx);
        prepared.delete(facet);
      }
    }
  };

  /** Move the element to `d.phase`. False when the outcome committed a new
   *  state the next pass realizes. */
  const steps = (fx: Fx, d: DialogState): boolean => {
    closing = false;
    switch (d.phase) {
      case "open": {
        cancelWatch();
        fx.removeClass(dialog, "is-leaving");
        // Since creation the dialog may have moved to another tree, or lost its pair to
        // the app's render.
        trackOpens(dialog);
        keepRegions(dialog);
        const [shown] = own(true, () => fx.showModal(dialog));
        if (shown.open) {
          return true;
        }
        if (d.facet?.refused !== undefined) {
          d.facet.refused(d.subject, shown.error);
        } else if (shown.error !== undefined) {
          fx.report(shown.error);
        }
        driver.set({ ...d, phase: "closed" });
        return false;
      }
      case "leaving": {
        if (!dialog.open && d.shown) {
          // The engine's own close of an earlier state closed it; a failed show leaves
          // nothing to fade.
          own(true, () => fx.showModal(dialog));
        }
        if (!dialog.open) {
          driver.set({ ...d, phase: "closed" });
          return false;
        }
        if (!dialog.classList.contains("is-leaving")) {
          // The start state, flushed so the class change animates from it.
          dialog.getBoundingClientRect();
          fx.addClass(dialog, "is-leaving");
        }
        if (watch?.leave !== d.leave) {
          cancelWatch();
          const leave = d.leave;
          watch = {
            leave,
            cancel: watchSettle(dialog, () => {
              settle(leave);
            }),
          };
        }
        return true;
      }
      case "closed": {
        cancelWatch();
        fx.removeClass(dialog, "is-leaving");
        const [, fired] = own(false, () => {
          fx.close(dialog);
        });
        if (!dialog.open) {
          return true;
        }
        if (!fired) {
          // Chromium ignores a close while the dialog's own close is in progress
          // (`is_closing_` in blink's html_dialog_element.cc). That close closes it
          // when this call returns, and the pass its records start completes this one.
          closing = true;
          return true;
        }
        // Code the close reached opened it again. fx's check folded that open and
        // restarted the pass, unless the engine is retired and follows no edge:
        // then close it again.
        driver.invalidate();
        return false;
      }
    }
  };

  const driver = createDriver<DialogState>({
    initial: {
      phase: "closed",
      leave: 0,
      shown: false,
      opens: 0,
      cycle: 0,
      facet: null,
      subject: 0,
      retired: false,
    },
    observe(d) {
      const next = fold(d);
      pageOpened = false;
      pageClosed = false;
      reopened = false;
      closedUnderOwn = false;
      seenOpen = dialog.open;
      return next;
    },
    derive(d) {
      if (d.facet !== derivedFor) {
        const previous = derivedFor;
        derivedFor = d.facet;
        previous?.derive?.(null);
      }
      d.facet?.derive?.(d);
    },
    realize(fx, d) {
      const facet = d.facet;
      if (facet !== null) {
        // A replaced facet's cleanup runs before its successor's prepare: they
        // write the same element.
        cleanupReplaced(fx, facet);
        prepared.add(facet);
        facet.prepare?.(fx, d);
      }
      if (!steps(fx, d)) {
        return;
      }
      if (facet === null) {
        // After the steps: a modal's cleanup removes the dialog it has just closed.
        cleanupReplaced(fx, null);
      } else {
        facet.follow?.(fx, d);
      }
    },
    notify(fx, d) {
      if (closing) {
        return;
      }
      // First, so a hook that reopens or disposes cannot cancel a close that completed.
      if (d.phase === "closed") {
        for (const w of outbox) {
          if (w.cycle === d.cycle) {
            w.due = true;
          }
        }
      }
      d.facet?.notify?.(fx, d);
      for (let i = outbox.length - 1; i >= 0; i--) {
        const w = outbox[i];
        if (w !== undefined && !w.due && w.cycle !== d.cycle) {
          outbox.splice(i, 1);
        }
      }
      // Removed before its call, so a restart never delivers a callback twice.
      for (;;) {
        const i = outbox.findIndex((w) => w.due);
        const w = outbox[i];
        if (w === undefined) {
          break;
        }
        outbox.splice(i, 1);
        fx.call(w.fn);
      }
    },
  });

  // The element may have changed and back again while a dependent read it live
  // (the scroll lock): realize again, whatever the records fold to.
  function realizePageChange(): void {
    driver.invalidate();
    driver.sync();
    driver.flush();
  }

  const detachTo = (facet: DialogFacet, retired: boolean): void => {
    driver.update((d) => {
      if (d.facet !== facet) {
        return d;
      }
      // Teardown closes at once with no fade: callbacks waiting for one are dropped.
      // Inside the engine's own showModal there is a fade to cut only if that open
      // goes through, which its beforetoggle listeners decide after this.
      const cuts = d.phase !== "closed";
      const deferred = cuts && call?.state === "open";
      if (deferred) {
        cut = outbox.filter((w) => w.cycle === d.cycle && !w.due);
      }
      return {
        ...d,
        facet: null,
        phase: "closed",
        cycle: cuts && !deferred ? d.cycle + 1 : d.cycle,
        retired: d.retired || retired,
      };
    });
  };

  return {
    get state(): DialogState {
      return driver.state;
    },
    get phase(): DialogState["phase"] {
      return fold(driver.state).phase;
    },
    open(subject) {
      if (driver.state.retired) {
        throw invalidState("The dialog's controller is disposed");
      }
      driver.update((d) => {
        const s = subject ?? d.subject;
        if (d.phase === "closed") {
          return { ...d, phase: "open", opens: d.opens + 1, cycle: d.cycle + 1, subject: s };
        }
        if (d.phase === "leaving") {
          // A reopen drops the callbacks waiting for this fade.
          return { ...d, phase: "open", cycle: d.cycle + 1, subject: s };
        }
        return d.subject === s ? d : { ...d, subject: s };
      });
    },
    close(onClosed) {
      driver.update((d) =>
        d.phase === "open" ? { ...d, phase: "leaving", leave: d.leave + 1, shown: dialog.open } : d,
      );
      if (onClosed !== undefined) {
        const d = driver.state;
        outbox.push({ fn: onClosed, cycle: d.cycle, due: d.phase === "closed" && !dialog.open });
        // The outbox is work the state does not describe: the flush must drive.
        driver.invalidate();
      }
    },
    attach(facet) {
      const d = driver.state;
      if (d.retired || d.facet !== null) {
        throw invalidState("The dialog already has a controller");
      }
      driver.update((s) => ({ ...s, facet }));
    },
    detach(facet) {
      detachTo(facet, false);
    },
    retire(facet) {
      detachTo(facet, true);
    },
    flush: () => {
      driver.flush();
    },
    consult: (fn) => driver.consult(fn),
    get outboxSize(): number {
      return outbox.length;
    },
  };
}
