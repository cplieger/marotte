import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import fc from "fast-check";
import { createRoot, runWithOwner } from "@cplieger/reactive";

import { closeDialog, createDialog, openDialog, type DialogController } from "./dialog.js";
import { existingEngine } from "./dialog-engine.js";
import { topmostOpenDialog } from "./modal-host.js";
import {
  World,
  isInvalidState,
  sentinelsOnce,
  type CloseKind,
  type PlatformKind,
} from "./__test-helpers__/reentry.js";

// The re-entry property for one caller dialog driven by createDialog and the
// openDialog/closeDialog helpers. The reference model below is written from the
// documented contracts alone: every entry commits its intent at once, a
// browser open or close is an edge taken the moment the platform makes it, a
// dismissal's guard answer counts only when nothing changed while it ran, and
// dispose ends the controller at once. At every quiescent point the dialog's
// DOM and the resources it holds must be what the model's state implies.

type Other = Exclude<PlatformKind, CloseKind | "showModal">;
type CbKind = "log" | "throw" | "none";

type Act =
  | { readonly k: "open" | "close" | "escape" | "backdrop" | "helperOpen" | "second" }
  | { readonly k: "helperClose"; readonly cb: CbKind }
  | { readonly k: "platformClose"; readonly kind: CloseKind }
  | { readonly k: "platformOpen" }
  | { readonly k: "platformOther"; readonly kind: Other }
  | { readonly k: "dispose" | "owner" };

type Step = Act | { readonly k: "settle" | "arm" | "focusOpener" };
type InjAct = Act | { readonly k: "focusProbe" | "throw" };

interface Case {
  readonly guard: "none" | "allow" | "refuse";
  readonly cancelWorld: boolean;
  readonly onCloseDisposes: boolean;
  readonly onCloseThrows: boolean;
  readonly closeOnBackdrop: boolean;
  readonly script: readonly Step[];
  readonly injections: readonly {
    readonly at: number;
    readonly hit?: string;
    readonly actions: readonly InjAct[];
  }[];
}

type Ev =
  | { readonly t: "open"; readonly by: "ctl" | "helper"; readonly dead?: true }
  | { readonly t: "close"; readonly shown: boolean; readonly dead?: true }
  | { readonly t: "helperClose"; readonly cb: number | null; readonly shown: boolean }
  | { readonly t: "cancel" | "backdrop"; readonly shown: boolean }
  | { readonly t: "settle" | "release" | "second" }
  /** The first realization's error released the controller somewhere among the
   *  `movable` events emitted before this one, after the last sentinel thrown. */
  | { readonly t: "rollback"; readonly movable: number }
  | { readonly t: "guard-start" | "guard-throw" }
  | { readonly t: "guard-end"; readonly answer: boolean; readonly shown: boolean }
  | {
      readonly t: "edge";
      readonly open: boolean;
      readonly opening: boolean;
      readonly closing: boolean;
    };

type Phase = "open" | "leaving" | "closed";

interface Cb {
  readonly id: number;
  readonly cycle: number;
  closedInCycle: boolean;
  /** The order its close completed in: callbacks are delivered in this order. */
  rank: number;
}

/** The reference model: the engine's state, the controller facet and the waiters. */
class Model {
  phase: Phase = "closed";
  cycle = 0;
  rev = 0;
  facet = false;
  ended = false;
  expectInvalid = 0;
  unshown = false;
  /** A page close made while an open is under way, which writes `open` before the next
   *  microtask: the engine folds it closed at its next call, else as a reopen. */
  closePending = false;
  /** Shown-to-closed changes of the attached facet: at most one onClose each. */
  closes = 0;
  private ranks = 0;
  cbs: Cb[] = [];
  private pending: { rev: number; dismissible: boolean }[] = [];

  constructor(private readonly c: Case) {}

  clone(): Model {
    const m = Object.assign(Object.create(Model.prototype) as Model, this);
    m.cbs = this.cbs.map((cb) => ({ ...cb }));
    m.pending = this.pending.map((p) => ({ ...p }));
    return m;
  }

  private set(phase: Phase, cycleUp: boolean): void {
    if (phase === this.phase && !cycleUp) {
      return;
    }
    if (phase === "closed" && this.phase !== "closed" && this.facet && !this.ended) {
      this.closes++;
    }
    this.unshown = false;
    this.phase = phase;
    if (cycleUp) {
      this.cycle++;
    }
    this.rev++;
    if (phase === "closed") {
      for (const cb of this.cbs) {
        if (cb.cycle === this.cycle && !cb.closedInCycle) {
          cb.closedInCycle = true;
          cb.rank = ++this.ranks;
        }
      }
    }
  }

  /** The callbacks the engine still holds: those of the current cycle, until it closes. */
  undelivered(): number {
    return this.phase === "closed"
      ? 0
      : this.cbs.filter((cb) => cb.cycle === this.cycle && !cb.closedInCycle).length;
  }

  private engineOpen(): void {
    if (this.phase === "closed") {
      // A cancelled beforetoggle refuses the open; the cycle still moved.
      this.set(this.c.cancelWorld ? "closed" : "open", true);
    } else if (this.phase === "leaving") {
      this.set("open", true);
    }
  }

  /** A leave asked for while the element is neither open nor being opened fades
   *  only if the page opens it before the leave is realized (an open edge). */
  private leave(shown: boolean): void {
    this.set("leaving", false);
    this.unshown = !shown;
  }

  /** At a quiescent point: an unshown leave no open edge reached had nothing to fade. */
  resolve(open: boolean): void {
    if (this.closePending && open) {
      // The open under way was the engine's own call, which the platform completed.
      this.closePending = false;
      this.set("open", true);
    }
    this.commitClose();
    if (this.phase === "leaving" && this.unshown) {
      this.set("closed", false);
    }
  }

  private dismissible(): boolean {
    return this.facet && !this.ended && this.phase === "open";
  }

  private dismiss(shown: boolean): void {
    if (!this.dismissible()) {
      return;
    }
    if (this.c.guard === "none") {
      this.leave(shown);
    }
  }

  private commitClose(): void {
    if (this.closePending) {
      this.closePending = false;
      this.set("closed", false);
    }
  }

  apply(ev: Ev): void {
    if (ev.t !== "edge") {
      this.commitClose();
    }
    switch (ev.t) {
      case "open":
        if (ev.dead === true || (ev.by === "ctl" && this.ended)) {
          this.expectInvalid++;
        } else {
          this.engineOpen();
        }
        return;
      case "close":
        if (ev.dead !== true && !this.ended && this.phase === "open") {
          this.leave(ev.shown);
        }
        return;
      case "helperClose":
        if (ev.cb !== null) {
          const closed = this.phase === "closed";
          this.cbs.push({
            id: ev.cb,
            cycle: this.cycle,
            closedInCycle: closed,
            rank: closed ? ++this.ranks : 0,
          });
          this.rev++;
        }
        if (this.phase === "open") {
          this.leave(ev.shown);
        }
        return;
      case "cancel":
        this.dismiss(ev.shown);
        return;
      case "backdrop":
        if (this.c.closeOnBackdrop) {
          this.dismiss(ev.shown);
        }
        return;
      case "guard-start":
        this.pending.push({ rev: this.rev, dismissible: this.dismissible() });
        return;
      case "guard-throw":
        this.pending.pop();
        return;
      case "guard-end": {
        const p = this.pending.pop();
        if (p?.dismissible === true && p.rev === this.rev && ev.answer) {
          this.leave(ev.shown);
        }
        return;
      }
      case "settle":
        if (this.phase === "leaving") {
          this.set("closed", false);
        }
        return;
      case "edge":
        if (ev.open && ev.closing) {
          // The engine's close in flight removes `open` after its listeners: the
          // page's open there never stands.
          return;
        }
        if (!ev.open && ev.opening && this.phase !== "closed") {
          this.closePending = true;
        } else if (!ev.open && this.phase !== "closed") {
          this.set("closed", false);
        } else if (ev.open && this.closePending) {
          // Closed and opened again before the engine saw the close: the close is never
          // applied, and the reopen drops the cycle's callbacks.
          this.closePending = false;
          this.set("open", true);
        } else if (ev.open && this.phase === "closed") {
          this.set("open", true);
        } else if (ev.open && this.unshown) {
          this.unshown = false;
        }
        return;
      case "rollback":
        // The error left at the drive's edge, after the first realization converged.
        this.resolve(false);
        this.apply({ t: "release" });
        return;
      case "release":
        if (this.ended) {
          return;
        }
        this.ended = true;
        this.facet = false;
        this.rev++;
        if (this.phase !== "closed") {
          this.set("closed", true);
        }
        return;
      case "second":
        if (this.facet) {
          this.expectInvalid++;
        } else {
          this.facet = true;
          this.ended = false;
          this.rev++;
        }
        return;
    }
  }
}

interface Instance {
  ctl: DialogController | undefined;
  ended: boolean;
  disposeParent: () => void;
  shownSinceClose: boolean;
  closes: number;
}

const CLOSERS: readonly CloseKind[] = ["form", "requestClose", "closeWatcher", "close"];
const OTHERS: readonly Other[] = [
  "show",
  "sameOpen",
  "addOpen",
  "removeOpen",
  "reopenModal",
  "reopenShow",
];

const actArb: fc.Arbitrary<Act> = fc.oneof(
  fc.constantFrom<Act>(
    { k: "open" },
    { k: "close" },
    { k: "escape" },
    { k: "backdrop" },
    { k: "helperOpen" },
    { k: "second" },
    { k: "platformOpen" },
    { k: "dispose" },
    { k: "owner" },
  ),
  fc.constantFrom<CbKind>("log", "throw", "none").map((cb): Act => ({ k: "helperClose", cb })),
  fc.constantFrom(...CLOSERS).map((kind): Act => ({ k: "platformClose", kind })),
  fc.constantFrom(...OTHERS).map((kind): Act => ({ k: "platformOther", kind })),
);

const stepArb: fc.Arbitrary<Step> = fc.oneof(
  { weight: 4, arbitrary: actArb },
  {
    weight: 2,
    arbitrary: fc.constantFrom<Step>({ k: "settle" }, { k: "arm" }, { k: "focusOpener" }),
  },
);

const injArb: fc.Arbitrary<InjAct> = fc.oneof(
  { weight: 5, arbitrary: actArb },
  { weight: 1, arbitrary: fc.constantFrom<InjAct>({ k: "focusProbe" }, { k: "throw" }) },
);

const caseArb: fc.Arbitrary<Case> = fc.record({
  guard: fc.constantFrom("none", "allow", "refuse"),
  cancelWorld: fc.oneof(
    { weight: 4, arbitrary: fc.constant(false) },
    { weight: 1, arbitrary: fc.constant(true) },
  ),
  onCloseDisposes: fc.boolean(),
  onCloseThrows: fc.oneof(
    { weight: 4, arbitrary: fc.constant(false) },
    { weight: 1, arbitrary: fc.constant(true) },
  ),
  closeOnBackdrop: fc.boolean(),
  script: fc.array(stepArb, { minLength: 1, maxLength: 8 }),
  injections: fc.array(
    fc.record({
      at: fc.nat({ max: 400 }),
      actions: fc.array(injArb, { minLength: 1, maxLength: 2 }),
    }),
    { maxLength: 2 },
  ),
});

/** Whether the current reactive owner, if any, still accepts a new root. */
function canOwn(): boolean {
  try {
    createRoot((dispose) => {
      dispose();
    });
    return true;
  } catch {
    return false;
  }
}

function isOwnerClosing(error: unknown): boolean {
  return error instanceof Error && error.message.includes("under an owner that is being disposed");
}

function hasClass(el: Element, token: string): boolean {
  return el.classList.contains(token);
}

interface Outcome {
  readonly trace: number;
  /** The hit kinds in trace order, so a generated injection's hit is known after the dry run. */
  readonly kinds: readonly string[];
  readonly failure: string | null;
}

/** A generated injection that calls the native showModal() from a beforetoggle of the dialog.
 *  The engine mishandles it (the `it.fails` case below); no marotte caller does it, which
 *  `dialog-native-open.node.test.ts` keeps true. */
function opensNativelyInToggle(c: Case, kinds: readonly string[]): boolean {
  return c.injections.some(
    (inj) =>
      inj.hit === undefined &&
      /^beforetoggle:\w+@dialog$/.test(
        kinds[kinds.length === 0 ? 0 : inj.at % kinds.length] ?? "",
      ) &&
      inj.actions.some(
        (a) => a.k === "platformOpen" || (a.k === "platformOther" && a.kind === "reopenModal"),
      ),
  );
}

async function runCase(c: Case, traceLength: number | null): Promise<Outcome> {
  const w = new World<Ev>({
    onEdge: (_d, open, opening, closing) => ({ t: "edge", open, opening, closing }),
  });
  let m = new Model(c);
  // The events emitted when the last sentinel was thrown.
  let thrownAt = 0;
  const sentinel = (): never => {
    thrownAt = w.events.length;
    throw w.sentinel();
  };
  let cbSeq = 0;
  let expectOwnerClosing = 0;
  const cbCalls = new Map<number, number>();
  const cbOrder: number[] = [];
  // A released controller's cleanup is a terminal effect: until the next quiescent
  // point it may take its class away from a dialog a newer controller then marks again.
  let releasedSinceQuiet = false;
  let failure: string | null = null;
  const fail = (why: string): void => {
    failure ??= why;
  };

  let inst: Instance = {
    ctl: undefined,
    ended: false,
    disposeParent: () => undefined,
    shownSinceClose: false,
    closes: 0,
  };

  const opener = w.probe("opener", "button");
  const elsewhere = w.probe("elsewhere", "button");
  const dialog = w.probe("dialog", "dialog") as HTMLDialogElement;
  // The element is shown, or a showModal under way will show it: read by the harness, not the engine.
  const showing = (): boolean => dialog.open || w.opening(dialog);
  const form = document.createElement("form");
  form.method = "dialog";
  form.append(w.probe("inner", "button"));
  dialog.append(form);
  w.quiet(() => {
    document.body.append(opener, elsewhere, dialog);
  });
  w.openPopover();
  if (c.cancelWorld) {
    w.ownListen(
      document,
      "beforetoggle",
      (e) => {
        if (e.target === dialog && (e as ToggleEvent).newState === "open") {
          e.preventDefault();
        }
      },
      true,
    );
  }
  w.ownListen(
    document,
    "cancel",
    (e) => {
      if (e.target === dialog) {
        w.emit({ t: "cancel", shown: showing() });
      }
    },
    true,
  );
  // An open is seen at every hit and quiescent point: no open goes without one.
  w.onHit = () => {
    if (dialog.open) {
      inst.shownSinceClose = true;
    }
  };
  w.quiet(() => {
    opener.focus();
  });

  const makeInstance = (): void => {
    const [disposeParent, parent] = createRoot((d, o) => [d, o] as const);
    const me: Instance = {
      ctl: undefined,
      ended: false,
      disposeParent,
      shownSinceClose: dialog.open,
      closes: 0,
    };
    const canDismiss =
      c.guard === "none"
        ? undefined
        : (): boolean => {
            w.emit({ t: "guard-start" });
            try {
              w.hit("canDismiss");
            } catch (error) {
              w.emit({ t: "guard-throw" });
              throw error;
            }
            const answer = c.guard === "allow";
            w.emit({ t: "guard-end", answer, shown: showing() });
            return answer;
          };
    const onClose = (): void => {
      me.closes++;
      if (me.ended) {
        fail("onClose after its controller's release was requested");
      }
      if (dialog.open) {
        fail("onClose while the dialog is open");
      }
      if (!me.shownSinceClose) {
        fail("onClose with no open since the last one");
      }
      me.shownSinceClose = false;
      w.hit("onClose");
      if (c.onCloseDisposes) {
        w.newTarget();
        release(me, "dispose");
      }
      if (c.onCloseThrows) {
        throw sentinel();
      }
    };
    // Current from its first setup hit, so a setup-time owner release reaches it.
    const previous = inst;
    inst = me;
    try {
      me.ctl = runWithOwner(parent, () =>
        createDialog(dialog, {
          closeOnBackdrop: c.closeOnBackdrop,
          onClose,
          ...(canDismiss === undefined ? {} : { canDismiss }),
        }),
      );
    } catch (error) {
      if (isInvalidState(error)) {
        inst = previous;
      } else {
        // A callback's error in the first realization rolls the controller back.
        w.rolledBack();
        me.ended = true;
        w.emit({ t: "rollback", movable: Math.max(0, w.events.length - thrownAt) });
      }
      throw error;
    }
  };

  const release = (target: Instance, how: "dispose" | "owner"): void => {
    if (target !== inst) {
      return;
    }
    releasedSinceQuiet = true;
    w.entry({ t: "release" }, () => {
      target.ended = true;
      if (how === "dispose") {
        target.ctl?.dispose();
      } else {
        target.disposeParent();
      }
    });
    // Everything the controller offers is refused once it has ended, and changes nothing.
    const view = (): string =>
      `open=${String(dialog.open)} class="${dialog.className}" timers=${vi.getTimerCount().toString()}`;
    const refused = (what: string, fn: () => void): void => {
      const before = view();
      fn();
      const after = view();
      if (after !== before) {
        fail(`a refused ${what} after release changed the dialog: ${before} -> ${after}`);
      }
    };
    const ctl = target.ctl;
    if (ctl !== undefined) {
      // Tagged: by now a newer controller may be the current one.
      refused("open()", () => {
        w.entry({ t: "open", by: "ctl", dead: true }, () => {
          ctl.open();
        });
      });
      refused("close()", () => {
        w.entry({ t: "close", shown: showing(), dead: true }, () => {
          ctl.close();
        });
      });
    }
    // With no newer controller, the events reach only the ended one.
    const alone = inst === target;
    const dispatch = alone ? refused : (_what: string, fn: () => void): void => fn();
    dispatch("Escape", () => {
      w.entry(null, () => dialog.dispatchEvent(new Event("cancel", { cancelable: true })));
    });
    dispatch("backdrop press", () => {
      w.entry({ t: "backdrop", shown: showing() }, () => {
        backdropPress();
      });
    });
  };

  const backdropPress = (): void => {
    dialog.dispatchEvent(new MouseEvent("mousedown", { bubbles: true }));
    dialog.dispatchEvent(new MouseEvent("mouseup", { bubbles: true }));
  };

  const helperCb = (kind: CbKind): (() => void) | undefined => {
    if (kind === "none") {
      return undefined;
    }
    const id = ++cbSeq;
    return Object.assign(
      () => {
        cbCalls.set(id, (cbCalls.get(id) ?? 0) + 1);
        cbOrder.push(id);
        w.hit("closeDialogCallback");
        if (kind === "throw") {
          throw sentinel();
        }
      },
      { id },
    );
  };

  const act = (a: InjAct): void => {
    const ctl = inst.ctl;
    switch (a.k) {
      case "open":
        if (ctl !== undefined) {
          w.entry({ t: "open", by: "ctl" }, () => ctl.open());
        }
        return;
      case "close":
        if (ctl !== undefined) {
          w.entry({ t: "close", shown: showing() }, () => ctl.close());
        }
        return;
      case "escape":
        w.entry(null, () => dialog.dispatchEvent(new Event("cancel", { cancelable: true })));
        return;
      case "backdrop":
        w.entry({ t: "backdrop", shown: showing() }, backdropPress);
        return;
      case "helperOpen":
        w.entry({ t: "open", by: "helper" }, () => {
          openDialog(dialog);
        });
        return;
      case "helperClose": {
        const cb = helperCb(a.cb);
        const id = cb === undefined ? null : (cb as unknown as { id: number }).id;
        w.entry({ t: "helperClose", cb: id, shown: showing() }, () => {
          closeDialog(dialog, cb);
        });
        return;
      }
      case "second":
        if (!canOwn()) {
          // Code reached during an owner's disposal runs under that owner, where a binding acquires nothing.
          w.entry(null, () => {
            expectOwnerClosing++;
            createDialog(dialog);
          });
        } else {
          w.entry({ t: "second" }, makeInstance);
        }
        return;
      case "platformClose":
        w.platform(dialog, a.kind);
        return;
      case "platformOpen":
        w.platform(dialog, "showModal");
        return;
      case "platformOther":
        w.platform(dialog, a.kind);
        return;
      case "dispose":
        if (ctl !== undefined) {
          release(inst, "dispose");
        }
        return;
      case "owner":
        release(inst, "owner");
        return;
      case "focusProbe":
        elsewhere.focus();
        return;
      case "throw":
        throw sentinel();
    }
  };

  w.admit = (wr) => {
    if (!releasedSinceQuiet || wr.el !== dialog || wr.name !== "class") {
      return undefined;
    }
    const before = new Set((wr.old ?? "").split(/\s+/).filter((t) => t !== ""));
    const after = new Set((wr.value ?? "").split(/\s+/).filter((t) => t !== ""));
    const removed = [...before].filter((t) => !after.has(t));
    const added = [...after].filter((t) => !before.has(t));
    return removed.length === 1 && removed[0] === "uip-dialog" && added.length === 0
      ? true
      : undefined;
  };
  let lastShown = false;
  let closesAtLast = 0;
  let modelClosesAtLast = 0;
  let instAtLast: Instance | null = null;

  const quiescent = (where: string): void => {
    m.resolve(dialog.open);
    if (dialog.open) {
      inst.shownSinceClose = true;
    }
    const p = m.phase;
    const shown = p !== "closed";
    const checks: [boolean, string][] = [
      [dialog.open === shown, `dialog.open ${String(dialog.open)}, model ${p}`],
      [hasClass(dialog, "is-leaving") === (p === "leaving"), `is-leaving with model ${p}`],
      [hasClass(dialog, "uip-dialog") === m.facet, `uip-dialog with facet ${String(m.facet)}`],
      [
        dialog.querySelectorAll(":scope > .uip-visually-hidden").length ===
          (existingEngine(dialog) === undefined ? 0 : 2),
        "live regions from the library's first call on the dialog",
      ],
      [
        vi.getTimerCount() === (p === "leaving" ? 1 : 0),
        `${vi.getTimerCount().toString()} timers with model ${p}`,
      ],
      [w.ledger.count(dialog, ["cancel"]) === (m.facet ? 1 : 0), "cancel listener"],
      [
        w.ledger.count(dialog, ["mousedown", "mouseup"]) === (m.facet && c.closeOnBackdrop ? 2 : 0),
        "backdrop listeners",
      ],
      [
        w.ledger.count(document, ["click", "keydown", "focusin", "cancel"]) === 0,
        "document listeners",
      ],
      [(topmostOpenDialog() === dialog) === shown, "open rank"],
      [
        (existingEngine(dialog)?.outboxSize ?? 0) === m.undelivered(),
        `outbox holds ${String(existingEngine(dialog)?.outboxSize)}, model ${m.undelivered().toString()}`,
      ],
    ];
    for (const [ok, why] of checks) {
      if (!ok) {
        fail(`${where}: ${why}`);
        return;
      }
    }
    for (const cb of m.cbs) {
      const n = cbCalls.get(cb.id) ?? 0;
      if (n > 1 || (!cb.closedInCycle && n > 0)) {
        fail(`${where}: closeDialog callback ${cb.id.toString()} called ${n.toString()} times`);
      }
      if (cb.cycle === m.cycle && p === "closed" && n !== 1) {
        fail(
          `${where}: closeDialog callback ${cb.id.toString()} of a completed close called ${n.toString()} times`,
        );
      }
    }
    const rankOf = (id: number): number => m.cbs.find((cb) => cb.id === id)?.rank ?? 0;
    if (cbOrder.some((id, i) => i > 0 && rankOf(id) < rankOf(cbOrder[i - 1] ?? 0))) {
      fail(`${where}: closeDialog callbacks delivered out of order: ${cbOrder.join(",")}`);
    }
    if (instAtLast === inst && !inst.ended && lastShown && !shown && inst.closes === closesAtLast) {
      fail(`${where}: closed from shown with no onClose`);
    }
    if (instAtLast === inst && inst.closes - closesAtLast > m.closes - modelClosesAtLast) {
      fail(
        `${where}: ${(inst.closes - closesAtLast).toString()} onClose calls for ${(m.closes - modelClosesAtLast).toString()} closes`,
      );
    }
    lastShown = shown;
    closesAtLast = inst.closes;
    modelClosesAtLast = m.closes;
    instAtLast = inst;
    releasedSinceQuiet = false;
    const effects = w.checkEffects(where);
    if (effects !== null) {
      fail(effects);
    }
  };

  try {
    if (traceLength !== null) {
      for (const inj of c.injections) {
        w.place(
          inj,
          traceLength,
          inj.actions.map((a) => () => {
            act(a);
          }),
        );
      }
    }
    // A rollback's release happens at a moment the case cannot see, after the
    // first realization converged: of its legal places, take one the dialog agrees with.
    const applyAll = (base: Model, events: readonly Ev[]): Model => {
      const at = events.findIndex((e) => e.t === "rollback");
      const rb = events[at];
      if (rb?.t !== "rollback") {
        for (const e of events) {
          base.apply(e);
        }
        return base;
      }
      const from = at - rb.movable;
      let last = base;
      for (let place = at; place >= from; place--) {
        const cand = base.clone();
        const order = [...events.slice(0, place), rb, ...events.slice(place, at)];
        for (const e of order) {
          cand.apply(e);
        }
        last = applyAll(cand, events.slice(at + 1));
        last.resolve(dialog.open);
        if (
          dialog.open === (last.phase !== "closed") &&
          hasClass(dialog, "uip-dialog") === last.facet
        ) {
          return last;
        }
      }
      return last;
    };
    const pump = (): void => {
      m = applyAll(m, w.events.splice(0));
    };
    w.entry({ t: "second" }, makeInstance);
    await w.quiesce();
    pump();
    quiescent("create");
    for (const [i, step] of c.script.entries()) {
      if (step.k === "settle") {
        w.advance(400, { t: "settle" });
      } else if (step.k === "arm") {
        w.advance(0, null);
      } else if (step.k === "focusOpener") {
        w.entry(null, () => {
          opener.focus();
        });
      } else {
        act(step as Act);
      }
      await w.quiesce();
      pump();
      quiescent(`after step ${i.toString()} (${step.k})`);
      if (failure !== null) {
        break;
      }
    }
    const invalid = w.observed.filter(
      (o) => o.channel === "entry" && isInvalidState(o.error),
    ).length;
    if (failure === null && invalid !== m.expectInvalid) {
      fail(
        `${invalid.toString()} InvalidStateError thrown, model expects ${m.expectInvalid.toString()}`,
      );
    }
    const closingErrors = w.observed.filter(
      (o) => o.channel === "entry" && isOwnerClosing(o.error),
    ).length;
    if (failure === null && closingErrors !== expectOwnerClosing) {
      fail(
        `${closingErrors.toString()} owner-closing errors, model expects ${expectOwnerClosing.toString()}`,
      );
    }
    if (failure === null) {
      failure = sentinelsOnce(w, (e) => isInvalidState(e) || isOwnerClosing(e));
    }
    const missed = w.unreached()[0];
    if (traceLength !== null && failure === null && missed !== undefined) {
      failure = `the pinned hit ${missed} was never reached`;
    }
    return { trace: w.trace.length, kinds: [...w.trace], failure };
  } finally {
    w.quiet(() => {
      try {
        inst.ctl?.dispose();
      } catch {
        // The case's own errors were already observed.
      }
      if (dialog.open) {
        dialog.close();
      }
    });
    await w.settleCleanup();
    w.dispose();
    vi.clearAllTimers();
    document.body.replaceChildren();
  }
}

beforeEach(() => {
  vi.useFakeTimers();
});

afterEach(() => {
  vi.useRealTimers();
  document.body.replaceChildren();
});

describe("dialog: re-entry property", () => {
  it("the dialog and what it holds follow the committed state through re-entry at every reaction point", async () => {
    await fc.assert(
      fc.asyncProperty(caseArb, async (c) => {
        const dry = await runCase(c, null);
        fc.pre(!opensNativelyInToggle(c, dry.kinds));
        const out = await runCase(c, dry.trace);
        expect(out.failure).toBeNull();
      }),
      {
        numRuns: 150,
        // Counterexamples found earlier, kept as regressions.
        examples: [
          // An open() and a closeDialog() from the closing beforetoggle of the settle's
          // own close: the leave fades the dialog again.
          [
            {
              guard: "none",
              cancelWorld: false,
              onCloseDisposes: false,
              onCloseThrows: false,
              closeOnBackdrop: false,
              script: [{ k: "open" }, { k: "close" }, { k: "settle" }],
              injections: [
                {
                  at: 0,
                  hit: "beforetoggle:closed@dialog",
                  actions: [{ k: "open" }, { k: "helperClose", cb: "log" }],
                },
              ],
            },
          ],
          // dispose() and a raw show() or showModal() from the beforetoggle of the
          // engine's own showModal(): the page's open stands.
          [
            {
              guard: "none",
              cancelWorld: false,
              onCloseDisposes: false,
              onCloseThrows: false,
              closeOnBackdrop: false,
              script: [{ k: "open" }],
              injections: [
                {
                  at: 0,
                  hit: "beforetoggle:open@dialog",
                  actions: [{ k: "dispose" }, { k: "platformOther", kind: "show" }],
                },
              ],
            },
          ],
          [
            {
              guard: "none",
              cancelWorld: false,
              onCloseDisposes: false,
              onCloseThrows: false,
              closeOnBackdrop: false,
              script: [{ k: "open" }],
              injections: [
                {
                  at: 0,
                  hit: "beforetoggle:open@dialog",
                  actions: [{ k: "dispose" }, { k: "platformOther", kind: "reopenModal" }],
                },
              ],
            },
          ],
          // A raw show() from the class removal of a rolled-back controller's teardown:
          // the dialog, with no controller, follows it open.
          [
            {
              guard: "none",
              cancelWorld: false,
              onCloseDisposes: false,
              onCloseThrows: true,
              closeOnBackdrop: false,
              script: [{ k: "dispose" }],
              injections: [
                { at: 0, hit: "attr:class@dialog", actions: [{ k: "helperClose", cb: "throw" }] },
                {
                  at: 1,
                  hit: "attr:class@dialog",
                  actions: [{ k: "platformOther", kind: "show" }],
                },
              ],
            },
          ],
          // showModal() or show() from the dialog's own close toggle: adopted at the observer's microtask.
          [
            {
              guard: "none",
              cancelWorld: false,
              onCloseDisposes: false,
              onCloseThrows: false,
              closeOnBackdrop: false,
              script: [{ k: "platformOpen" }, { k: "platformClose", kind: "close" }],
              injections: [
                { at: 0, hit: "toggle:closed@dialog", actions: [{ k: "platformOpen" }] },
              ],
            },
          ],
          [
            {
              guard: "none",
              cancelWorld: false,
              onCloseDisposes: false,
              onCloseThrows: false,
              closeOnBackdrop: false,
              script: [{ k: "platformOpen" }, { k: "platformClose", kind: "form" }],
              injections: [
                {
                  at: 0,
                  hit: "toggle:closed@dialog",
                  actions: [{ k: "platformOther", kind: "show" }],
                },
              ],
            },
          ],
          // A callback error rolls the first realization back after a fade it started: its teardown is terminal.
          [
            {
              guard: "none",
              cancelWorld: false,
              onCloseDisposes: false,
              onCloseThrows: true,
              closeOnBackdrop: false,
              script: [{ k: "platformOther", kind: "reopenModal" }],
              injections: [
                {
                  at: 0,
                  hit: "attr:class@dialog",
                  actions: [{ k: "helperClose", cb: "throw" }, { k: "platformOpen" }],
                },
                {
                  at: 0,
                  hit: "beforetoggle:open@dialog",
                  actions: [{ k: "helperOpen" }, { k: "helperClose", cb: "log" }],
                },
              ],
            },
          ],
          // A raw show() and a form close in the opening beforetoggle of the engine's own
          // showModal: that showModal opens the dialog again, so it ends open.
          [
            {
              guard: "none",
              cancelWorld: false,
              onCloseDisposes: false,
              onCloseThrows: false,
              closeOnBackdrop: false,
              script: [{ k: "open" }],
              injections: [
                {
                  at: 0,
                  hit: "beforetoggle:open@dialog",
                  actions: [{ k: "platformOther", kind: "reopenShow" }],
                },
                {
                  at: 0,
                  hit: "beforetoggle:closed@popover",
                  actions: [{ k: "platformClose", kind: "form" }],
                },
              ],
            },
          ],
          // A raw close and show() from canDismiss discards its answer.
          [
            {
              guard: "allow",
              cancelWorld: false,
              onCloseDisposes: false,
              onCloseThrows: false,
              closeOnBackdrop: false,
              script: [{ k: "platformOther", kind: "reopenShow" }],
              injections: [
                {
                  at: 0,
                  hit: "beforetoggle:closed@popover",
                  actions: [{ k: "platformClose", kind: "requestClose" }],
                },
                { at: 0, hit: "canDismiss", actions: [{ k: "platformOther", kind: "reopenShow" }] },
              ],
            },
          ],
          // A sentinel thrown while the platform reports an error is never reported again.
          [
            {
              guard: "none",
              cancelWorld: false,
              onCloseDisposes: false,
              onCloseThrows: false,
              closeOnBackdrop: false,
              script: [
                { k: "platformOther", kind: "reopenModal" },
                { k: "helperClose", cb: "throw" },
                { k: "platformClose", kind: "form" },
                { k: "open" },
                { k: "platformClose", kind: "form" },
              ],
              injections: [
                { at: 0, hit: "window-error", actions: [{ k: "focusProbe" }] },
                { at: 0, hit: "focus@elsewhere", actions: [{ k: "throw" }, { k: "focusProbe" }] },
              ],
            },
          ],
          // A second controller made inside a disposed one's close: the old cleanup's class removal is terminal.
          [
            {
              guard: "none",
              cancelWorld: false,
              onCloseDisposes: false,
              onCloseThrows: false,
              closeOnBackdrop: false,
              script: [
                { k: "platformOther", kind: "reopenModal" },
                { k: "dispose" },
                { k: "platformOther", kind: "reopenModal" },
              ],
              injections: [
                { at: 0, hit: "beforetoggle:closed@dialog", actions: [{ k: "second" }] },
              ],
            },
          ],

          // An open() from the beforetoggle of a raw showModal() made in a closeDialog
          // callback, then a form close: the page's close is the last edge, so it ends closed.
          [
            {
              guard: "none",
              cancelWorld: false,
              onCloseDisposes: false,
              onCloseThrows: false,
              closeOnBackdrop: false,
              script: [{ k: "helperClose", cb: "log" }, { k: "open" }],
              injections: [
                {
                  at: 0,
                  hit: "closeDialogCallback",
                  actions: [{ k: "platformOpen" }, { k: "platformClose", kind: "form" }],
                },
                { at: 0, hit: "beforetoggle:open@dialog", actions: [{ k: "open" }] },
              ],
            },
          ],
          // An attribute-only close and open during a fade, neither with a toggle event:
          // the removal closes the dialog with its callback and onClose, and an addition
          // inside a raw close's focus restoration reopens it.
          [
            {
              guard: "none",
              cancelWorld: false,
              onCloseDisposes: false,
              onCloseThrows: false,
              closeOnBackdrop: false,
              script: [
                { k: "open" },
                { k: "helperClose", cb: "log" },
                { k: "platformOther", kind: "removeOpen" },
              ],
              injections: [],
            },
          ],
          [
            {
              guard: "none",
              cancelWorld: false,
              onCloseDisposes: false,
              onCloseThrows: false,
              closeOnBackdrop: false,
              script: [{ k: "open" }, { k: "close" }, { k: "platformClose", kind: "close" }],
              injections: [
                {
                  at: 0,
                  hit: "focus@opener",
                  actions: [{ k: "platformOther", kind: "addOpen" }],
                },
              ],
            },
          ],
          // A same-value open write during a closeDialog fade, as a step and at the
          // leave's class write: the fade still closes the dialog and calls back.
          [
            {
              guard: "none",
              cancelWorld: false,
              onCloseDisposes: false,
              onCloseThrows: false,
              closeOnBackdrop: false,
              script: [
                { k: "helperOpen" },
                { k: "helperClose", cb: "log" },
                { k: "platformOther", kind: "sameOpen" },
                { k: "settle" },
              ],
              injections: [],
            },
          ],
          [
            {
              guard: "none",
              cancelWorld: false,
              onCloseDisposes: false,
              onCloseThrows: false,
              closeOnBackdrop: false,
              script: [{ k: "open" }, { k: "close" }, { k: "settle" }],
              injections: [
                {
                  at: 1,
                  hit: "attr:class@dialog",
                  actions: [{ k: "platformOther", kind: "sameOpen" }],
                },
              ],
            },
          ],
          // A raw close and showModal() from the focus restoration of the settle's own close: it ends open.
          [
            {
              guard: "none",
              cancelWorld: false,
              onCloseDisposes: false,
              onCloseThrows: false,
              closeOnBackdrop: false,
              script: [{ k: "open" }, { k: "close" }, { k: "settle" }],
              injections: [
                {
                  at: 0,
                  hit: "focus@opener",
                  actions: [{ k: "platformOther", kind: "reopenModal" }],
                },
              ],
            },
          ],
          // A form close and an open() in the focusing steps of the engine's own showModal: it ends open.
          [
            {
              guard: "none",
              cancelWorld: false,
              onCloseDisposes: false,
              onCloseThrows: false,
              closeOnBackdrop: false,
              script: [{ k: "open" }],
              injections: [
                {
                  at: 0,
                  hit: "focus@inner",
                  actions: [{ k: "platformClose", kind: "form" }, { k: "open" }],
                },
              ],
            },
          ],
          // An open a beforetoggle listener cancels holds no rank.
          [
            {
              guard: "none",
              cancelWorld: true,
              onCloseDisposes: false,
              onCloseThrows: false,
              closeOnBackdrop: false,
              script: [{ k: "open" }],
              injections: [],
            },
          ],
          // G1: an open() from the focus restoration of a raw close keeps the dialog
          // open and wired, though onClose would dispose it.
          [
            {
              guard: "none",
              cancelWorld: false,
              onCloseDisposes: true,
              onCloseThrows: false,
              closeOnBackdrop: false,
              script: [{ k: "open" }, { k: "platformClose", kind: "close" }],
              injections: [{ at: 0, hit: "focus@opener", actions: [{ k: "open" }] }],
            },
          ],
          // G2: a dispose() there throws nothing and calls no throwing onClose.
          [
            {
              guard: "none",
              cancelWorld: false,
              onCloseDisposes: false,
              onCloseThrows: true,
              closeOnBackdrop: false,
              script: [{ k: "open" }, { k: "platformClose", kind: "close" }, { k: "second" }],
              injections: [{ at: 0, hit: "focus@opener", actions: [{ k: "dispose" }] }],
            },
          ],
          // An owner a pending close's callback disposes while a new controller starts.
          [
            {
              guard: "none",
              cancelWorld: false,
              onCloseDisposes: false,
              onCloseThrows: false,
              closeOnBackdrop: false,
              script: [
                { k: "dispose" },
                { k: "helperOpen" },
                { k: "helperClose", cb: "log" },
                { k: "platformClose", kind: "close" },
              ],
              injections: [
                { at: 0, hit: "focus@opener", actions: [{ k: "second" }] },
                { at: 0, hit: "closeDialogCallback", actions: [{ k: "owner" }] },
              ],
            },
          ],
          // A dispose at the leave's is-leaving write arms no settle watcher after it.
          [
            {
              guard: "none",
              cancelWorld: false,
              onCloseDisposes: false,
              onCloseThrows: false,
              closeOnBackdrop: false,
              script: [{ k: "open" }, { k: "close" }],
              injections: [{ at: 1, hit: "attr:class@dialog", actions: [{ k: "dispose" }] }],
            },
          ],
          [
            {
              guard: "none",
              cancelWorld: false,
              onCloseDisposes: false,
              onCloseThrows: false,
              closeOnBackdrop: false,
              script: [{ k: "helperClose", cb: "log" }],
              injections: [
                {
                  at: 1,
                  hit: "attr:class@dialog",
                  actions: [{ k: "second" }, { k: "platformClose", kind: "form" }],
                },
                { at: 0, hit: "attr:class@dialog", actions: [{ k: "owner" }, { k: "open" }] },
              ],
            },
          ],
          [
            {
              guard: "none",
              cancelWorld: false,
              onCloseDisposes: false,
              onCloseThrows: false,
              closeOnBackdrop: false,
              script: [{ k: "owner" }, { k: "settle" }],
              injections: [{ at: 1, hit: "attr:class@dialog", actions: [{ k: "second" }] }],
            },
          ],
          [
            {
              guard: "none",
              cancelWorld: false,
              onCloseDisposes: false,
              onCloseThrows: false,
              closeOnBackdrop: false,
              script: [{ k: "open" }],
              injections: [
                {
                  at: 0,
                  hit: "attr:class@dialog",
                  actions: [{ k: "helperOpen" }, { k: "helperClose", cb: "throw" }],
                },
              ],
            },
          ],
          [
            {
              guard: "none",
              cancelWorld: false,
              onCloseDisposes: false,
              onCloseThrows: false,
              closeOnBackdrop: false,
              script: [{ k: "settle" }],
              injections: [
                {
                  at: 0,
                  hit: "attr:class@dialog",
                  actions: [{ k: "helperOpen" }, { k: "helperClose", cb: "log" }],
                },
              ],
            },
          ],
          [
            {
              guard: "none",
              cancelWorld: false,
              onCloseDisposes: false,
              onCloseThrows: false,
              closeOnBackdrop: false,
              script: [
                { k: "owner" },
                { k: "platformOpen" },
                { k: "helperClose", cb: "throw" },
                { k: "helperClose", cb: "log" },
              ],
              injections: [],
            },
          ],
          // A closeDialog() and a dispose() from the beforetoggle of an open the page
          // then refuses: no fade was cut, so the callback runs.
          [
            {
              guard: "none",
              cancelWorld: true,
              onCloseDisposes: false,
              onCloseThrows: false,
              closeOnBackdrop: false,
              script: [{ k: "open" }, { k: "platformOther", kind: "show" }],
              injections: [
                { at: 0, actions: [{ k: "platformOpen" }] },
                { at: 2, actions: [{ k: "helperClose", cb: "log" }, { k: "dispose" }] },
              ],
            },
          ],
          // A dismissal and a browser close inside the engine's own showModal, which then
          // writes open again: the close completed the leave, so the dialog ends closed.
          [
            {
              guard: "allow",
              cancelWorld: false,
              onCloseDisposes: true,
              onCloseThrows: false,
              closeOnBackdrop: false,
              script: [{ k: "open" }, { k: "platformClose", kind: "closeWatcher" }],
              injections: [
                {
                  at: 49,
                  actions: [{ k: "platformOpen" }, { k: "platformClose", kind: "closeWatcher" }],
                },
              ],
            },
          ],
          // A teardown inside the engine's own showModal whose open goes through cuts a
          // fade, even when the open's focus steps close the dialog again.
          [
            {
              guard: "none",
              cancelWorld: false,
              onCloseDisposes: false,
              onCloseThrows: false,
              closeOnBackdrop: false,
              script: [{ k: "open" }],
              injections: [
                {
                  at: 0,
                  hit: "beforetoggle:open@dialog",
                  actions: [{ k: "helperClose", cb: "log" }, { k: "dispose" }],
                },
              ],
            },
          ],
          [
            {
              guard: "none",
              cancelWorld: false,
              onCloseDisposes: false,
              onCloseThrows: false,
              closeOnBackdrop: false,
              script: [{ k: "open" }],
              injections: [
                {
                  at: 0,
                  hit: "beforetoggle:open@dialog",
                  actions: [{ k: "helperClose", cb: "log" }, { k: "dispose" }],
                },
                { at: 0, hit: "focus@inner", actions: [{ k: "platformClose", kind: "close" }] },
              ],
            },
          ],
          [
            {
              guard: "none",
              cancelWorld: false,
              onCloseDisposes: false,
              onCloseThrows: false,
              closeOnBackdrop: false,
              script: [{ k: "open" }],
              injections: [
                {
                  at: 0,
                  hit: "beforetoggle:open@dialog",
                  actions: [{ k: "helperClose", cb: "log" }, { k: "dispose" }],
                },
                { at: 0, hit: "focus@inner", actions: [{ k: "platformClose", kind: "form" }] },
              ],
            },
          ],
          // A page reopen after the engine's own superseded open write stands, and a
          // reopen before it drops the fade's callbacks.
          [
            {
              guard: "none",
              cancelWorld: false,
              onCloseDisposes: false,
              onCloseThrows: false,
              closeOnBackdrop: false,
              script: [{ k: "open" }],
              injections: [
                {
                  at: 0,
                  hit: "beforetoggle:open@dialog",
                  actions: [
                    { k: "close" },
                    { k: "platformOpen" },
                    { k: "platformClose", kind: "close" },
                  ],
                },
                {
                  at: 1,
                  hit: "focus@inner",
                  actions: [{ k: "platformOther", kind: "reopenModal" }],
                },
              ],
            },
          ],
          [
            {
              guard: "none",
              cancelWorld: false,
              onCloseDisposes: false,
              onCloseThrows: false,
              closeOnBackdrop: false,
              script: [{ k: "open" }],
              injections: [
                {
                  at: 0,
                  hit: "beforetoggle:open@dialog",
                  actions: [
                    { k: "helperClose", cb: "log" },
                    { k: "platformOpen" },
                    { k: "platformClose", kind: "close" },
                  ],
                },
                {
                  at: 0,
                  hit: "focus@inner",
                  actions: [{ k: "platformOther", kind: "reopenModal" }],
                },
              ],
            },
          ],
          // The same reopens, then a page close in the engine's own showModal's focus
          // steps: the reopen before the own write still dropped the fade's callbacks.
          [
            {
              guard: "none",
              cancelWorld: false,
              onCloseDisposes: false,
              onCloseThrows: false,
              closeOnBackdrop: false,
              script: [{ k: "open" }],
              injections: [
                {
                  at: 0,
                  hit: "beforetoggle:open@dialog",
                  actions: [
                    { k: "helperClose", cb: "log" },
                    { k: "platformOpen" },
                    { k: "platformClose", kind: "close" },
                    { k: "platformOpen" },
                    { k: "platformClose", kind: "close" },
                  ],
                },
                { at: 2, hit: "focus@inner", actions: [{ k: "platformClose", kind: "close" }] },
              ],
            },
          ],
          [
            {
              guard: "none",
              cancelWorld: false,
              onCloseDisposes: false,
              onCloseThrows: false,
              closeOnBackdrop: false,
              script: [{ k: "open" }],
              injections: [
                {
                  at: 0,
                  hit: "beforetoggle:open@dialog",
                  actions: [
                    { k: "helperClose", cb: "log" },
                    { k: "platformOpen" },
                    { k: "platformClose", kind: "close" },
                  ],
                },
                {
                  at: 0,
                  hit: "focus@inner",
                  actions: [{ k: "platformOther", kind: "reopenModal" }],
                },
                { at: 2, hit: "focus@inner", actions: [{ k: "platformClose", kind: "close" }] },
              ],
            },
          ],
          // A dispose() in the focus steps of a page showModal(), then a page close and
          // show() in the dispose's own beforetoggle: the dispose's close removes `open`
          // after its listeners, so the dialog ends closed.
          [
            {
              guard: "none",
              cancelWorld: false,
              onCloseDisposes: false,
              onCloseThrows: false,
              closeOnBackdrop: false,
              script: [
                { k: "helperClose", cb: "log" },
                { k: "platformOpen" },
                { k: "helperClose", cb: "log" },
              ],
              injections: [
                { at: 0, hit: "focus@inner", actions: [{ k: "dispose" }] },
                {
                  at: 0,
                  hit: "beforetoggle:closed@dialog",
                  actions: [
                    { k: "platformOther", kind: "removeOpen" },
                    { k: "platformOther", kind: "show" },
                  ],
                },
              ],
            },
          ],
        ],
      },
    );
  }, 60000);

  // Seed 1573708530: a native showModal() and then closeDialog() from the beforetoggle of the
  // engine's own showModal(). The dialog ends closed, but the browser's showModal writes `open`
  // after the close and the engine closes it again. A fixed engine passes it, which `it.fails`
  // reports: then drop `opensNativelyInToggle`.
  it.fails(
    "leaves a native showModal() then closeDialog() in its own opening beforetoggle closed with no extra open write",
    async () => {
      const c: Case = {
        guard: "none",
        cancelWorld: false,
        onCloseDisposes: false,
        onCloseThrows: false,
        closeOnBackdrop: false,
        script: [{ k: "open" }, { k: "platformOther", kind: "show" }],
        injections: [
          { at: 92, actions: [{ k: "platformOpen" }, { k: "helperClose", cb: "log" }] },
          { at: 17, actions: [{ k: "platformOther", kind: "removeOpen" }] },
        ],
      };
      const dry = await runCase(c, null);
      const out = await runCase(c, dry.trace);
      expect(out.failure).toBeNull();
    },
  );
});
