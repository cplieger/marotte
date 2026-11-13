import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import fc from "fast-check";
import { createRoot, runWithOwner } from "@cplieger/reactive";

import { existingEngine } from "./dialog-engine.js";
import { closeDialog, openDialog } from "./dialog.js";
import { createModal, _resetForTest, type ModalController } from "./modal.js";
import { topmostOpenDialog } from "./modal-host.js";
import {
  type World,
  changedStyles,
  constructiveAfterRelease,
  explore,
  firstFailure,
  isInvalidState,
  sentinelsOnce,
  throwSentinel,
  type CloseKind,
  type PlatformKind,
  type Scenario,
  type Injection,
} from "./__test-helpers__/reentry.js";

// The re-entry property for two modals over one scroll lock, optionally on a
// customized <body> whose style reactions are hits. Each modal's engine phase
// is modelled as in the dialog property; the body is locked exactly while a
// live modal is open or fading, and a disposed modal's dialog is spent.

type Who = "a" | "b";
type Other = Exclude<PlatformKind, CloseKind | "showModal">;
type Step =
  | {
      readonly k:
        | "open"
        | "close"
        | "escape"
        | "backdrop"
        | "helperOpen"
        | "helperClose"
        | "platformOpen"
        | "dispose";
      readonly who: Who;
    }
  | { readonly k: "platformClose"; readonly who: Who; readonly kind: CloseKind }
  | { readonly k: "platformOther"; readonly who: Who; readonly kind: Other }
  | { readonly k: "owner" | "settle" | "focusOpener" | "throw" };

interface Case {
  readonly probeBody: boolean;
  readonly allowGuard: boolean;
  readonly script: readonly Step[];
  readonly injections: readonly Injection<Step>[];
}

type Ev =
  | { readonly t: "edge"; readonly who: Who; readonly open: boolean; readonly opening: boolean }
  | { readonly t: "cancel"; readonly who: Who; readonly shown: boolean }
  | { readonly t: "guard-start" | "guard-throw"; readonly who: Who }
  | { readonly t: "guard-end"; readonly who: Who; readonly shown: boolean };

const whoArb = fc.constantFrom<Who>("a", "b");
const stepArb: fc.Arbitrary<Step> = fc.oneof(
  fc
    .record({
      k: fc.constantFrom(
        "open",
        "close",
        "escape",
        "backdrop",
        "helperOpen",
        "helperClose",
        "platformOpen",
        "dispose",
      ),
      who: whoArb,
    })
    .map((r) => r as Step),
  fc
    .record({
      who: whoArb,
      kind: fc.constantFrom<CloseKind>("form", "requestClose", "closeWatcher", "close"),
    })
    .map((r): Step => ({ k: "platformClose", ...r })),
  fc
    .record({
      who: whoArb,
      kind: fc.constantFrom<Other>(
        "show",
        "sameOpen",
        "addOpen",
        "removeOpen",
        "reopenModal",
        "reopenShow",
      ),
    })
    .map((r): Step => ({ k: "platformOther", ...r })),
  fc.constantFrom<Step>({ k: "owner" }, { k: "settle" }, { k: "focusOpener" }),
);
const injArb: fc.Arbitrary<Step> = fc.oneof(
  { weight: 6, arbitrary: stepArb.filter((s) => s.k !== "settle") },
  { weight: 1, arbitrary: fc.constant<Step>({ k: "throw" }) },
);
const caseArb: fc.Arbitrary<Case> = fc.record({
  probeBody: fc.boolean(),
  allowGuard: fc.boolean(),
  script: fc.array(stepArb, { minLength: 1, maxLength: 8 }),
  injections: fc.array(
    fc.record({
      at: fc.nat({ max: 500 }),
      actions: fc.array(injArb, { minLength: 1, maxLength: 2 }),
    }),
    { maxLength: 2 },
  ),
});

type Phase = "open" | "leaving" | "closed";

interface M {
  phase: Phase;
  ended: boolean;
  rev: number;
  closes: number;
  shownSinceClose: boolean;
  pending: { rev: number; ok: boolean }[];
  /** A leave asked for while the dialog was neither open nor being opened: it fades only if an open edge reaches it. */
  unshown: boolean;
  /** A page close made while an open is under way, which writes `open` before the next
   *  microtask: the engine folds it closed at its next call, else as a reopen. */
  closePending: boolean;
}

/** The modal dialog at the top of the top layer, by hit test: its `::backdrop`
 *  covers the viewport and paints over every dialog below it. */
function topModal(): HTMLDialogElement | null {
  const hit = document.elementFromPoint(1, 1)?.closest("dialog") ?? null;
  return hit?.matches(":modal") === true ? hit : null;
}

/** The top modal is where chrome hosts; with none open, any open dialog may be.
 *  While a dialog the page stranded in the top layer (`World.stranded`) is still
 *  there, the platform's order is unknowable, and any open modal dialog may be. */
function rankHolds(w: World<Ev>, ...dialogs: (HTMLDialogElement | undefined)[]): boolean {
  for (const d of w.stranded) {
    if (!d.matches(":modal")) {
      w.stranded.delete(d);
    }
  }
  const top = topModal();
  const got = topmostOpenDialog();
  if (w.stranded.size > 0 && top !== null) {
    return got?.open === true && got.matches(":modal");
  }
  if (top !== null) {
    return got === top;
  }
  return dialogs.some((d) => d?.open === true) ? got?.open === true : got === null;
}

function scenarioFor(c: Case) {
  return (w: World<Ev>): Scenario<Step> => {
    let failure: string | null = null;
    const fail = (why: string): void => {
      failure ??= why;
    };
    const models: Record<Who, M> = {
      a: {
        phase: "closed",
        ended: false,
        rev: 0,
        closes: 0,
        shownSinceClose: false,
        pending: [],
        unshown: false,
        closePending: false,
      },
      b: {
        phase: "closed",
        ended: false,
        rev: 0,
        closes: 0,
        shownSinceClose: false,
        pending: [],
        unshown: false,
        closePending: false,
      },
    };
    // The fades in the order they began: each settle timer ends the oldest one.
    const fades: Who[] = [];
    const endFade = (who: Who): void => {
      const i = fades.indexOf(who);
      if (i !== -1) {
        fades.splice(i, 1);
      }
    };
    let expectInvalid = 0;
    const spent: { n: number }[] = [];
    const opener = w.probe("opener", "button");
    const contents: Record<Who, HTMLElement> = { a: w.probe("content-a"), b: w.probe("content-b") };
    w.quiet(() => {
      for (const who of ["a", "b"] as const) {
        const form = document.createElement("form");
        form.method = "dialog";
        form.append(w.probe(`inner-${who}`, "button"));
        contents[who].append(form);
      }
      document.body.append(opener);
      opener.focus();
    });
    w.openPopover();
    const ctls: Partial<Record<Who, ModalController>> = {};
    const els: Partial<Record<Who, HTMLDialogElement>> = {};
    let disposeParent = (): void => undefined;
    // The dialog is shown, or a showModal under way will show it: read by the harness, not the engine.
    const showing = (who: Who): boolean => {
      const el = els[who];
      return el !== undefined && (el.open || w.opening(el));
    };

    const setPhase = (who: Who, phase: Phase, shown = true): void => {
      const m = models[who];
      if (m.phase === phase) {
        return;
      }
      m.unshown = phase === "leaving" && !shown;
      if (m.phase === "leaving") {
        endFade(who);
      }
      if (phase === "leaving") {
        fades.push(who);
      }
      m.phase = phase;
      m.rev++;
    };
    const engineOpen = (who: Who): void => {
      const m = models[who];
      if (m.phase === "leaving") {
        endFade(who);
        m.phase = "open";
        m.unshown = false;
        m.rev++;
      } else if (m.phase === "closed") {
        setPhase(who, "open");
      }
    };
    /** An engine call sees the page's close: the open under way has not written yet. */
    const commitClose = (who: Who): void => {
      if (models[who].closePending) {
        models[who].closePending = false;
        setPhase(who, "closed");
      }
    };
    const apply = (ev: Ev): void => {
      const m = models[ev.who];
      if (ev.t !== "edge") {
        commitClose(ev.who);
      }
      switch (ev.t) {
        case "edge":
          if (m.ended) {
            return;
          }
          if (!ev.open && ev.opening && m.phase !== "closed") {
            m.closePending = true;
          } else if (!ev.open && m.phase !== "closed") {
            setPhase(ev.who, "closed");
          } else if (ev.open && m.closePending) {
            // Closed and opened again before the engine saw the close: a reopen.
            m.closePending = false;
            setPhase(ev.who, "open");
            m.rev++;
          } else if (ev.open && m.phase === "closed") {
            setPhase(ev.who, "open");
          } else if (ev.open && m.unshown) {
            m.unshown = false;
          }
          return;
        case "cancel":
          if (!m.ended && m.phase === "open" && !c.allowGuard) {
            setPhase(ev.who, "leaving", ev.shown);
          }
          return;
        case "guard-start":
          m.pending.push({ rev: m.rev, ok: !m.ended && m.phase === "open" });
          return;
        case "guard-throw":
          m.pending.pop();
          return;
        case "guard-end": {
          const p = m.pending.pop();
          if (p?.ok === true && p.rev === m.rev) {
            setPhase(ev.who, "leaving", ev.shown);
          }
          return;
        }
      }
    };
    // An open is seen at every hit and quiescent point.
    const seeOpen = (): void => {
      for (const who of ["a", "b"] as const) {
        if (els[who]?.open === true) {
          models[who].shownSinceClose = true;
        }
      }
    };
    w.onHit = seeOpen;
    // DESIGN §7.1: a lock write is admissible while the newest committed phases hold
    // the lock, even when a later step of the same drive resolves them to closed.
    const LOCK: Readonly<Record<string, (v: string) => boolean>> = {
      position: (v) => v === "fixed",
      left: (v) => v === "0px",
      right: (v) => v === "0px",
      width: (v) => v === "100%",
      overflow: (v) => v === "hidden",
      top: (v) => v.endsWith("px"),
    };
    w.admit = (write) => {
      if (write.el !== document.body || write.name !== "style") {
        return undefined;
      }
      const held = (["a", "b"] as const).some(
        (x) => !models[x].ended && models[x].phase !== "closed",
      );
      const toLock = [...changedStyles(write)].every(([prop, v]) => LOCK[prop]?.(v) === true);
      return held && toLock ? true : undefined;
    };
    w.onTimer = () => {
      pump();
      commitClose("a");
      commitClose("b");
      const who = fades[0];
      if (who !== undefined) {
        setPhase(who, "closed");
      }
    };
    const pump = (): void => {
      seeOpen();
      for (const ev of w.drain()) {
        apply(ev);
      }
    };

    const make = (who: Who): void => {
      const m = models[who];
      const onClose = (): void => {
        m.closes++;
        if (m.ended) {
          fail(`${who} onClose after release`);
        }
        if (els[who]?.open === true) {
          fail(`${who} onClose while open`);
        }
        if (!m.shownSinceClose) {
          fail(`${who} onClose with no open since the last`);
        }
        m.shownSinceClose = false;
        w.hit(`onClose:${who}`);
      };
      const canDismiss = c.allowGuard
        ? (): boolean => {
            w.emit({ t: "guard-start", who });
            try {
              w.hit(`canDismiss:${who}`);
            } catch (error) {
              w.emit({ t: "guard-throw", who });
              throw error;
            }
            w.emit({ t: "guard-end", who, shown: showing(who) });
            return true;
          }
        : undefined;
      const opts = {
        onClose,
        initialFocus: contents[who],
        ...(canDismiss === undefined ? {} : { canDismiss }),
      };
      const ctl = createModal(contents[who], opts);
      ctls[who] = ctl;
      els[who] = ctl.el;
      ctl.el.setAttribute("data-modal", who);

      // On document in the capture phase, so the model sees the cancel before the modal does.
      w.ownListen(
        document,
        "cancel",
        (e) => {
          if (e.target === ctl.el) {
            w.emit({ t: "cancel", who, shown: true });
          }
        },
        true,
      );
    };

    const release = (who: Who, how: "dispose" | "owner"): void => {
      w.entry(null, () => {
        w.markRelease();
        pump();
        for (const x of how === "owner" ? (["a"] as const) : [who]) {
          if (!models[x].ended) {
            models[x].ended = true;
            if (models[x].phase !== "closed") {
              setPhase(x, "closed");
            }
          }
        }
        if (how === "dispose") {
          ctls[who]?.dispose();
        } else {
          disposeParent();
        }
      });
      for (const x of how === "owner" ? (["a"] as const) : [who]) {
        const ctl = ctls[x];
        if (ctl !== undefined) {
          w.entry(null, () => {
            expectInvalid++;
            ctl.open();
          });
          w.entry(null, () => {
            expectInvalid++;
            openDialog(ctl.el);
          });
          w.entry(null, () => {
            ctl.close();
            // Delivered once the change in progress, if any, has finished.
            const counter = { n: 0 };
            spent.push(counter);
            closeDialog(ctl.el, () => {
              counter.n++;
            });
            if (ctl.isOpen) {
              fail("isOpen after release");
            }
          });
        }
      }
    };

    return {
      start(): void {
        w.entry(null, () => {
          const [d, parent] = createRoot((dispose, owner) => [dispose, owner] as const);
          disposeParent = d;
          runWithOwner(parent, () => {
            make("a");
          });
          make("b");
        });
        pump();
      },
      act(s: Step): Promise<void> | undefined {
        pump();
        // Only the engine the step calls folds a pending close: the other modal's
        // engine reads it at most, and its own open under way still writes `open`.
        if (s.k === "owner") {
          commitClose("a");
        } else if (
          "who" in s &&
          s.k !== "platformClose" &&
          s.k !== "platformOther" &&
          s.k !== "platformOpen"
        ) {
          commitClose(s.who);
        }
        if (s.k === "owner") {
          release("a", "owner");
          return undefined;
        }
        if (s.k === "settle") {
          w.advance(400, null);
          return undefined;
        }
        if (s.k === "focusOpener") {
          w.entry(null, () => {
            opener.focus();
          });
          return undefined;
        }
        if (s.k === "throw") {
          return throwSentinel(w);
        }
        if (!("who" in s)) {
          return undefined;
        }
        const who = s.who;
        const ctl = ctls[who];
        const el = els[who];
        const m = models[who];
        if (ctl === undefined || el === undefined) {
          return undefined;
        }
        switch (s.k) {
          case "open":
            w.entry(null, () => {
              if (m.ended) {
                expectInvalid++;
              } else {
                engineOpen(who);
              }
              ctl.open();
            });
            break;
          case "helperOpen":
            w.entry(null, () => {
              if (m.ended) {
                expectInvalid++;
              } else {
                engineOpen(who);
              }
              openDialog(el);
            });
            break;
          case "close":
          case "helperClose":
            w.entry(null, () => {
              if (!m.ended && m.phase === "open") {
                setPhase(who, "leaving", showing(who));
              }
              if (s.k === "close") {
                ctl.close();
              } else {
                closeDialog(el);
              }
            });
            break;
          case "escape":
            w.entry(null, () => el.dispatchEvent(new Event("cancel", { cancelable: true })));
            break;
          case "backdrop":
            w.entry(null, () => {
              el.dispatchEvent(new MouseEvent("mousedown", { bubbles: true }));
              pump();
              if (!c.allowGuard && !m.ended && m.phase === "open") {
                setPhase(who, "leaving", showing(who));
              }
              el.dispatchEvent(new MouseEvent("mouseup", { bubbles: true }));
            });
            break;
          case "platformClose":
          case "platformOther":
            w.platform(el, s.kind);
            break;
          case "platformOpen":
            w.platform(el, "showModal");
            break;
          case "dispose":
            release(who, "dispose");
            break;
        }
        pump();
        return undefined;
      },
      check(where: string): string | null {
        pump();
        if (failure !== null) {
          return failure;
        }
        for (const who of ["a", "b"] as const) {
          const m = models[who];
          if (m.closePending) {
            // The open under way was the engine's own call, which the platform completed.
            m.closePending = false;
            setPhase(who, els[who]?.open === true ? "open" : "closed");
            m.rev++;
          }
          if (m.phase === "leaving" && m.unshown) {
            setPhase(who, "closed");
          }
        }
        const checks: [boolean, string][] = [];
        let locked = false;
        let leaving = 0;
        for (const who of ["a", "b"] as const) {
          const m = models[who];
          const el = els[who]!;
          const shown = m.phase !== "closed";
          locked ||= shown && !m.ended;
          leaving += m.phase === "leaving" ? 1 : 0;
          checks.push(
            [
              el.isConnected === !m.ended,
              `${who} dialog mounted ${String(el.isConnected)}, ended ${String(m.ended)}`,
            ],
            [
              contents[who].classList.contains("uip-modal-dialog") === !m.ended,
              `${who} content class`,
            ],
            [el.open === shown, `${who} dialog.open ${String(el.open)}, model ${m.phase}`],
            [
              el.classList.contains("is-leaving") === (m.phase === "leaving"),
              `${who} is-leaving, model ${m.phase}`,
            ],
            [ctls[who]?.isOpen === (!m.ended && m.phase === "open"), `${who} isOpen`],
            // The case gives callbacks only to spent modals, which deliver them at once.
            [(existingEngine(el)?.outboxSize ?? 0) === 0, `${who} outbox holds a callback`],
            [
              el.querySelectorAll(":scope > .uip-visually-hidden").length === 2,
              `${who} live regions`,
            ],
          );
        }
        const s = document.body.style;
        const lockedNow = s.position === "fixed" && s.overflow === "hidden" && s.width === "100%";
        const clear = s.position === "" && s.overflow === "" && s.width === "" && s.top === "";
        checks.push(
          [
            spent.every((c) => c.n === 1),
            "a closeDialog callback on a spent modal did not run exactly once",
          ],
          [
            locked ? lockedNow : clear,
            `body position "${s.position}" overflow "${s.overflow}", model locked ${String(locked)}`,
          ],
          [
            vi.getTimerCount() === leaving,
            `${vi.getTimerCount().toString()} timers for ${leaving.toString()} fades`,
          ],
          [rankHolds(w, els.a, els.b), "open rank"],
        );
        return firstFailure(where, checks);
      },
      finish(): string | null {
        const invalid = w.observed.filter(
          (o) => o.channel === "entry" && isInvalidState(o.error),
        ).length;
        return (
          failure ??
          (invalid === expectInvalid
            ? null
            : `${invalid.toString()} InvalidStateError thrown, model expects ${expectInvalid.toString()}`) ??
          constructiveAfterRelease(w, "content-a", ["uip-modal-dialog"]) ??
          sentinelsOnce(w, isInvalidState)
        );
      },
      cleanup(): void {
        for (const ctl of [ctls.a, ctls.b]) {
          try {
            ctl?.dispose();
          } catch {
            // Observed already.
          }
        }
        _resetForTest();
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
  _resetForTest();
});

describe("createModal: re-entry property", () => {
  it("two modals, their mounts and the scroll lock follow the committed state through re-entry", async () => {
    await fc.assert(
      fc.asyncProperty(caseArb, async (c) => {
        const failure = await explore(scenarioFor(c), c, {
          probeBody: c.probeBody,
          onEdge: (d, open, opening): Ev => ({
            t: "edge",
            who: d.getAttribute("data-modal") === "a" ? "a" : "b",
            open,
            opening,
          }),
        });
        expect(failure).toBeNull();
      }),
      {
        numRuns: 150,
        examples: [
          // A page show() and form close of b inside its own showModal's beforetoggle, and
          // a's open from the close's focus steps: b's showModal still opens it after.
          [
            {
              probeBody: false,
              allowGuard: false,
              script: [
                { k: "open", who: "b" },
                { k: "platformClose", who: "b", kind: "form" },
                { k: "platformOther", who: "a", kind: "addOpen" },
                { k: "owner" },
              ],
              injections: [
                { at: 388, actions: [{ k: "open", who: "a" }] },
                {
                  at: 220,
                  actions: [
                    { k: "platformOther", who: "b", kind: "show" },
                    { k: "platformClose", who: "b", kind: "form" },
                  ],
                },
              ],
            },
          ],
          // An Escape on b from the beforetoggle of the page's show() inside a close and
          // show() of b in one task: it meets b closed, and the show() opens it.
          [
            {
              probeBody: false,
              allowGuard: false,
              script: [
                { k: "open", who: "a" },
                { k: "platformOther", who: "b", kind: "show" },
                { k: "platformOther", who: "b", kind: "reopenShow" },
              ],
              injections: [{ at: 12, actions: [{ k: "escape", who: "b" }] }],
            },
          ],
          // An open() and closeDialog() of b, closed by a form, from the lock's release
          // write: the lock is taken again for the leave, then released when it resolves unshown.
          [
            {
              probeBody: true,
              allowGuard: false,
              script: [
                { k: "owner" },
                { k: "platformOther", who: "b", kind: "show" },
                { k: "platformOther", who: "b", kind: "reopenModal" },
              ],
              injections: [
                {
                  at: 1,
                  hit: "focus@content-b",
                  actions: [{ k: "platformClose", who: "b", kind: "form" }],
                },
                {
                  at: 6,
                  hit: "attr:style@body",
                  actions: [
                    { k: "open", who: "b" },
                    { k: "helperClose", who: "b" },
                  ],
                },
              ],
            },
          ],
          // A raw show() of a spent modal's removed dialog, with a closeDialog from inside it:
          // its retired engine closes it again.
          [
            {
              probeBody: false,
              allowGuard: false,
              script: [{ k: "owner" }, { k: "platformOther", who: "a", kind: "show" }],
              injections: [
                {
                  at: 0,
                  hit: "beforetoggle:closed@popover",
                  actions: [{ k: "helperClose", who: "a" }],
                },
              ],
            },
          ],
          // showModal() or show() of a from its own close toggle: adopted at the observer's microtask.
          [
            {
              probeBody: false,
              allowGuard: false,
              script: [
                { k: "platformOpen", who: "a" },
                { k: "platformClose", who: "a", kind: "close" },
              ],
              injections: [
                { at: 0, hit: "doc:toggle:closed", actions: [{ k: "platformOpen", who: "a" }] },
              ],
            },
          ],
          [
            {
              probeBody: true,
              allowGuard: false,
              script: [
                { k: "platformOpen", who: "a" },
                { k: "platformClose", who: "a", kind: "close" },
              ],
              injections: [
                {
                  at: 0,
                  hit: "doc:toggle:closed",
                  actions: [{ k: "platformOther", who: "a", kind: "show" }],
                },
              ],
            },
          ],
          // A raw close and show() of b from the focus restoration of its settle's close: b ends open.
          [
            {
              probeBody: false,
              allowGuard: false,
              script: [
                { k: "platformOther", who: "b", kind: "reopenModal" },
                { k: "escape", who: "b" },
                { k: "settle" },
                { k: "platformClose", who: "a", kind: "form" },
              ],
              injections: [
                {
                  at: 0,
                  hit: "focus@opener",
                  actions: [{ k: "platformOther", who: "b", kind: "reopenShow" }],
                },
              ],
            },
          ],
          // a opened from b's opening beforetoggle: b, opened last by the platform, is on top.
          [
            {
              probeBody: false,
              allowGuard: false,
              script: [{ k: "open", who: "b" }, { k: "owner" }],
              injections: [
                { at: 0, hit: "doc:beforetoggle:open", actions: [{ k: "open", who: "a" }] },
                { at: 0, hit: "attr:class@content-a", actions: [{ k: "throw" }] },
              ],
            },
          ],

          // A raw close, b's open and a raw showModal() of a in the focusing steps of a's
          // own showModal, with and without an open() of a after them: a ends on top.
          [
            {
              probeBody: false,
              allowGuard: false,
              script: [{ k: "open", who: "a" }],
              injections: [
                {
                  at: 0,
                  hit: "focus@content-a",
                  actions: [
                    { k: "platformClose", who: "a", kind: "close" },
                    { k: "open", who: "b" },
                    { k: "platformOpen", who: "a" },
                  ],
                },
              ],
            },
          ],
          [
            {
              probeBody: false,
              allowGuard: false,
              script: [{ k: "open", who: "a" }],
              injections: [
                {
                  at: 0,
                  hit: "focus@content-a",
                  actions: [
                    { k: "platformClose", who: "a", kind: "close" },
                    { k: "open", who: "b" },
                    { k: "platformOpen", who: "a" },
                    { k: "open", who: "a" },
                  ],
                },
              ],
            },
          ],
          // An attribute-only close and open of a during its fade, neither with a toggle
          // event: the removal closes and unlocks it, and an addition inside a raw close's
          // focus restoration reopens it.
          [
            {
              probeBody: true,
              allowGuard: false,
              script: [
                { k: "open", who: "a" },
                { k: "close", who: "a" },
                { k: "platformOther", who: "a", kind: "removeOpen" },
              ],
              injections: [],
            },
          ],
          [
            {
              probeBody: true,
              allowGuard: false,
              script: [
                { k: "open", who: "a" },
                { k: "close", who: "a" },
                { k: "platformClose", who: "a", kind: "close" },
              ],
              injections: [
                {
                  at: 0,
                  hit: "focus@opener",
                  actions: [{ k: "platformOther", who: "a", kind: "addOpen" }],
                },
              ],
            },
          ],
          // a stranded in the top layer by a raw removal of `open`, then b and a opened:
          // a's showModal() leaves it below b, and the rank cannot see that.
          [
            {
              probeBody: false,
              allowGuard: false,
              script: [
                { k: "platformOther", who: "a", kind: "reopenModal" },
                { k: "platformOther", who: "a", kind: "removeOpen" },
                { k: "open", who: "b" },
                { k: "open", who: "a" },
              ],
              injections: [],
            },
          ],
          // A same-value open write during a's fade: the fade still closes it.
          [
            {
              probeBody: false,
              allowGuard: false,
              script: [
                { k: "open", who: "a" },
                { k: "close", who: "a" },
                { k: "platformOther", who: "a", kind: "sameOpen" },
                { k: "settle" },
              ],
              injections: [],
            },
          ],
          // A form close and an open() in the focusing steps of the engine's own showModal: it ends open.
          [
            {
              probeBody: false,
              allowGuard: false,
              script: [{ k: "open", who: "a" }],
              injections: [
                {
                  at: 0,
                  hit: "focus@content-a",
                  actions: [
                    { k: "platformClose", who: "a", kind: "form" },
                    { k: "open", who: "a" },
                  ],
                },
              ],
            },
          ],
          // A dispose at the lock's first body write lands no later lock write.
          [
            {
              probeBody: true,
              allowGuard: false,
              script: [{ k: "open", who: "a" }],
              injections: [
                { at: 0, hit: "attr:style@body", actions: [{ k: "dispose", who: "a" }] },
              ],
            },
          ],
          [
            {
              probeBody: false,
              allowGuard: false,
              script: [{ k: "platformOpen", who: "b" }, { k: "owner" }],
              injections: [
                {
                  at: 0,
                  hit: "beforetoggle:closed@popover",
                  actions: [{ k: "owner" }, { k: "platformClose", who: "b", kind: "form" }],
                },
              ],
            },
          ],
          [
            {
              probeBody: false,
              allowGuard: false,
              script: [{ k: "open", who: "b" }, { k: "helperClose", who: "b" }, { k: "settle" }],
              injections: [
                {
                  at: 0,
                  hit: "doc:beforetoggle:closed",
                  actions: [
                    { k: "open", who: "a" },
                    { k: "platformClose", who: "a", kind: "requestClose" },
                  ],
                },
              ],
            },
          ],
          [
            {
              probeBody: true,
              allowGuard: false,
              script: [
                { k: "open", who: "b" },
                { k: "platformClose", who: "b", kind: "form" },
                { k: "dispose", who: "a" },
                { k: "open", who: "a" },
              ],
              injections: [
                {
                  at: 0,
                  hit: "doc:beforetoggle:closed",
                  actions: [
                    { k: "dispose", who: "b" },
                    { k: "platformClose", who: "a", kind: "form" },
                  ],
                },
              ],
            },
          ],
          // A teardown inside the engine's own showModal drops only the callbacks
          // waiting at that moment: one added to the spent modal afterwards runs.
          [
            {
              probeBody: false,
              allowGuard: false,
              script: [{ k: "open", who: "b" }],
              injections: [{ at: 213, actions: [{ k: "dispose", who: "b" }, { k: "throw" }] }],
            },
          ],
          [
            {
              probeBody: false,
              allowGuard: false,
              script: [
                { k: "open", who: "a" },
                { k: "platformClose", who: "a", kind: "form" },
              ],
              injections: [
                { at: 350, actions: [{ k: "owner" }] },
                { at: 348, actions: [{ k: "open", who: "a" }] },
              ],
            },
          ],
        ],
      },
    );
  }, 60000);
});
