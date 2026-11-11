// Re-entry harness for the property tests: an instrumented world in which every
// caller-supplied node is a probe whose reactions are traced hits, and each hit
// can run injected actions (a dispose, a raw platform call, a re-entrant call).
// A case runs twice: a dry run to learn its trace, then the real run with the
// injections placed on indices of that trace. Fake timers make both runs equal
// up to the first injection.

import { setEffectErrorHandler } from "@cplieger/reactive";
import { vi } from "vitest";

const OBSERVED = [
  "class",
  "hidden",
  "inert",
  "style",
  "id",
  "role",
  "tabindex",
  "open",
  "aria-expanded",
  "aria-hidden",
  "aria-controls",
  "aria-haspopup",
] as const;

/** A thrown test error the harness counts: each must be observed exactly once. */
class Sentinel extends Error {
  constructor(readonly id: number) {
    super(`sentinel ${id.toString()}`);
  }
}

type Channel = "entry" | "window" | "timer" | "handler" | "platform";

interface Observed {
  readonly error: unknown;
  readonly channel: Channel;
}

/** One write a probe saw, with the entry sequence number current at the time. */
interface Effect {
  readonly probe: string;
  readonly kind: string;
  readonly value: string | null;
  readonly seq: number;
}

/** An attribute write, or a focus (`name` "focus"), a probe saw inside an effect window. */
interface Write {
  readonly el: Element;
  readonly name: string;
  readonly old: string | null;
  readonly value: string | null;
}

/** What happened between an injected action's return and the end of the drive it
 *  interrupted: the entry that was running returns, or a timer callback does. A
 *  newer injection or a browser change ends it early, with its writes unjudged. */
interface EffectWindow {
  /** The hit the injection ran at. */
  readonly at: string;
  /** The dialogs whose showModal or close was running at that hit: each one's next `open` write completes it. */
  readonly inFlight: Set<Element>;
  readonly refused: string[];
  readonly writes: Write[];
  readonly timers: Map<unknown, { fired: boolean }>;
  cancelled: boolean;
  readonly listeners: LedgerEntry[];
}

let current: World | null = null;

function labelOf(el: Element): string {
  return el.getAttribute("data-probe") ?? el.localName;
}

function probeHit(el: Element, kind: string): void {
  current?.hit(`${kind}@${labelOf(el)}`, el);
}

function probeAttr(el: Element, name: string, value: string | null): void {
  current?.effect(labelOf(el), `attr:${name}`, value);
  probeHit(el, `attr:${name}`);
}

class ProbeElement extends HTMLElement {
  static readonly observedAttributes = OBSERVED;
  attributeChangedCallback(name: string, _old: string | null, value: string | null): void {
    probeAttr(this, name, value);
  }
  connectedCallback(): void {
    probeHit(this, "connected");
  }
  disconnectedCallback(): void {
    probeHit(this, "disconnected");
  }
}

class ProbeDialog extends HTMLDialogElement {
  static readonly observedAttributes = OBSERVED;
  attributeChangedCallback(name: string, _old: string | null, value: string | null): void {
    probeAttr(this, name, value);
  }
  connectedCallback(): void {
    probeHit(this, "connected");
  }
  disconnectedCallback(): void {
    probeHit(this, "disconnected");
  }
}

class ProbeButton extends HTMLButtonElement {
  static readonly observedAttributes = OBSERVED;
  attributeChangedCallback(name: string, _old: string | null, value: string | null): void {
    probeAttr(this, name, value);
  }
}

class ProbeBody extends HTMLBodyElement {
  static readonly observedAttributes = OBSERVED;
  attributeChangedCallback(name: string, _old: string | null, value: string | null): void {
    probeAttr(this, name, value);
  }
}

function defineOnce(): void {
  if (customElements.get("uip-test-probe") !== undefined) {
    return;
  }
  customElements.define("uip-test-probe", ProbeElement);
  customElements.define("uip-test-probe-dialog", ProbeDialog, { extends: "dialog" });
  customElements.define("uip-test-probe-button", ProbeButton, { extends: "button" });
  customElements.define("uip-test-probe-body", ProbeBody, { extends: "body" });
}

function isProbe(target: EventTarget | null): boolean {
  return (
    target instanceof ProbeElement ||
    target instanceof ProbeDialog ||
    target instanceof ProbeButton ||
    target instanceof ProbeBody
  );
}

interface LedgerEntry {
  readonly target: EventTarget;
  readonly type: string;
  readonly listener: unknown;
  readonly capture: boolean;
}

const nativeAdd = EventTarget.prototype.addEventListener;
const nativeRemove = EventTarget.prototype.removeEventListener;
const windowAdd = Object.getOwnPropertyDescriptor(window, "addEventListener");
const windowRemove = Object.getOwnPropertyDescriptor(window, "removeEventListener");

function captureOf(
  options: boolean | AddEventListenerOptions | EventListenerOptions | undefined,
): boolean {
  return typeof options === "boolean" ? options : options?.capture === true;
}

/** Net listeners the library holds, per target and type. */
class Ledger {
  readonly entries: LedgerEntry[] = [];
  own = 0;
  onAdd: ((entry: LedgerEntry) => void) | null = null;

  install(): void {
    // eslint-disable-next-line @typescript-eslint/no-this-alias -- the patched methods need the ledger and their own receiver
    const ledger = this;
    // The test runner's window carries its own listener methods, so it is patched beside the prototype.
    const add = function (
      this: EventTarget,
      type: string,
      listener: EventListenerOrEventListenerObject | null,
      options?: boolean | AddEventListenerOptions,
    ): void {
      nativeAdd.call(this, type, listener, options);
      if (ledger.own > 0 || listener === null) {
        return;
      }
      const signal = typeof options === "object" ? options.signal : undefined;
      if (signal?.aborted === true) {
        return;
      }
      const entry = { target: this, type, listener, capture: captureOf(options) };
      if (ledger.find(entry) !== -1) {
        return;
      }
      ledger.entries.push(entry);
      ledger.onAdd?.(entry);
      if (signal !== undefined) {
        nativeAdd.call(signal, "abort", () => {
          ledger.drop(entry);
        });
      }
    };
    const remove = function (
      this: EventTarget,
      type: string,
      listener: EventListenerOrEventListenerObject | null,
      options?: boolean | EventListenerOptions,
    ): void {
      nativeRemove.call(this, type, listener, options);
      ledger.drop({ target: this, type, listener, capture: captureOf(options) });
    };
    EventTarget.prototype.addEventListener = add;
    EventTarget.prototype.removeEventListener = remove;
    Object.defineProperty(window, "addEventListener", {
      value: add,
      configurable: true,
      writable: true,
    });
    Object.defineProperty(window, "removeEventListener", {
      value: remove,
      configurable: true,
      writable: true,
    });
  }

  uninstall(): void {
    EventTarget.prototype.addEventListener = nativeAdd;
    EventTarget.prototype.removeEventListener = nativeRemove;
    for (const [name, original] of [
      ["addEventListener", windowAdd],
      ["removeEventListener", windowRemove],
    ] as const) {
      if (original === undefined) {
        Reflect.deleteProperty(window, name);
      } else {
        Object.defineProperty(window, name, original);
      }
    }
  }

  holds(e: LedgerEntry): boolean {
    return this.find(e) !== -1;
  }

  private find(e: LedgerEntry): number {
    return this.entries.findIndex(
      (x) =>
        x.target === e.target &&
        x.type === e.type &&
        x.listener === e.listener &&
        x.capture === e.capture,
    );
  }

  private drop(e: LedgerEntry): void {
    const i = this.find(e);
    if (i !== -1) {
      this.entries.splice(i, 1);
    }
  }

  count(target: EventTarget, types: readonly string[]): number {
    return this.entries.filter((e) => e.target === target && types.includes(e.type)).length;
  }
}

interface RawFrame {
  readonly dialog: HTMLDialogElement;
  seen: boolean;
}

/** A browser close of an open dialog. */
export type CloseKind = "form" | "requestClose" | "closeWatcher" | "close";

/** `show` is a raw non-modal `show()`; `sameOpen` writes `open` to the value it has;
 *  `addOpen` adds the `open` attribute to a closed dialog and `removeOpen` removes it
 *  from an open one, neither firing a toggle event; `reopenModal` and `reopenShow`
 *  are a raw `close()` followed by `showModal()` or `show()` in one call. */
export type PlatformKind =
  | CloseKind
  | "showModal"
  | "show"
  | "sameOpen"
  | "addOpen"
  | "removeOpen"
  | "reopenModal"
  | "reopenShow";

interface WorldOptions<E> {
  /** A platform change made by a raw call, observed at the next hit or when the call
   *  returns; `opening`: a showModal or show whose `beforetoggle` ran is still to write `open`;
   *  `closing`: a close the library made is in its `beforetoggle` and will remove `open`. */
  readonly onEdge?: (
    dialog: HTMLDialogElement,
    open: boolean,
    opening: boolean,
    closing: boolean,
  ) => E;
  /** Swap a customized `<body>` in for the case. */
  readonly probeBody?: boolean;
}

/** One instrumented run. */
export class World<E = unknown> {
  readonly trace: string[] = [];
  readonly events: E[] = [];
  readonly observed: Observed[] = [];
  readonly effects: Effect[] = [];
  /** Sentinels thrown, each to be observed exactly once. */
  readonly thrown: number[] = [];
  /** Sentinels thrown while the platform reports an error: HTML's "report an
   *  exception" reports nothing for a global already in error reporting mode,
   *  so each is observed at most once. */
  readonly lossy: number[] = [];
  readonly ledger = new Ledger();
  /** Dialogs a raw platform call changed since the last quiescent point. */
  readonly rawTouched = new Set<HTMLDialogElement>();
  /** Dialogs whose `open` attribute a raw call removed while they were modal. HTML
   *  ("the dialog element": removing `open` by hand) leaves such a dialog blocking the
   *  document, and Chromium leaves it in the top layer where it was, so a later
   *  showModal() of it does not move it to the top: the top-layer order no longer
   *  follows the order `open` was added, and no API exposes it. */
  readonly stranded = new Set<HTMLDialogElement>();
  /** Bumped by every entry the script or an injection makes. */
  seq = 0;
  /** Called at every hit, before its injections run. */
  onHit: ((kind: string) => void) | null = null;
  /** Called as a timer callback starts. */
  onTimer: (() => void) | null = null;
  /** Set by the case: rules on a write inside an effect window when it returns a
   *  boolean; `undefined` leaves it to the default rule (the quiescent value). */
  admit: ((write: Write) => boolean | undefined) | null = null;
  private window: EffectWindow | null = null;
  private readonly writes = new MutationObserver(() => undefined);
  /** Each dialog's toggles between their `beforetoggle` and the `open` write that
   *  ends them, with their new states, innermost last: a toggle can nest in another's `beforetoggle`. */
  private readonly toggling = new Map<Element, string[]>();
  /** Why an ended window's effects were not admissible, for the next check. */
  private readonly inadmissible: string[] = [];
  private depth = 0;
  private readonly toggleArmed = new WeakSet<Element>();
  private reporting = 0;
  private readonly injections = new Map<number, (() => void)[]>();
  /** Pinned injections by hit kind and occurrence. */
  private readonly byKind = new Map<string, Map<number, (() => void)[]>>();
  private readonly kindSeen = new Map<string, number>();
  private suspended = 0;
  private raw: RawFrame | null = null;
  private readonly cleanups: (() => void)[] = [];
  private readonly onEdge:
    | ((dialog: HTMLDialogElement, open: boolean, opening: boolean, closing: boolean) => E)
    | undefined;
  // Dialogs whose close by the library is in its beforetoggle and still to remove `open`:
  // the close removes it after its listeners whenever it is set again by then.
  private readonly ownClosing = new Set<Element>();
  private originalBody: HTMLElement | null = null;

  constructor(options: WorldOptions<E> = {}) {
    defineOnce();
    this.onEdge = options.onEdge;
    // eslint-disable-next-line @typescript-eslint/no-this-alias -- custom element reactions reach the running world through this module slot
    current = this;
    this.ownListen(window, "error", (e) => {
      const ev = e as ErrorEvent;
      ev.preventDefault();
      this.observed.push({ error: ev.error, channel: "window" });
      this.reporting++;
      try {
        this.hit("window-error");
      } finally {
        this.reporting--;
      }
    });
    this.ownListen(
      document,
      "beforetoggle",
      (e) => {
        const ev = e as ToggleEvent;
        const target = ev.target as Element;
        // At the target, after every capture listener: a cancelled toggle is not under way.
        nativeAdd.call(
          target,
          "beforetoggle",
          (te: Event) => {
            if (te.defaultPrevented) {
              this.endToggle(target);
            }
            // A close that finds `open` gone after its listeners writes nothing.
            if ((te as ToggleEvent).newState === "closed" && !target.hasAttribute("open")) {
              this.ownClosing.delete(target);
            }
          },
          { once: true },
        );
        this.armToggleHit(target);
        if (isProbe(ev.target)) {
          probeHit(ev.target as Element, `beforetoggle:${ev.newState}`);
        } else {
          this.hit(`doc:beforetoggle:${ev.newState}`, ev.target as Element);
        }
      },
      true,
    );
    this.ownListen(
      document,
      "focusin",
      (e) => {
        if (isProbe(e.target)) {
          probeHit(e.target as Element, "focusin");
        } else {
          this.hit("doc:focusin");
        }
      },
      true,
    );
    this.writes.observe(document.documentElement, {
      subtree: true,
      attributes: true,
      attributeOldValue: true,
    });
    this.wrapTimers();
    this.ledger.onAdd = (entry) => {
      this.window?.listeners.push(entry);
    };
    const previous = setEffectErrorHandler((error) => {
      this.observed.push({ error, channel: "handler" });
    });
    this.cleanups.push(() => {
      setEffectErrorHandler(previous);
    });
    if (options.probeBody === true) {
      const body = document.createElement("body", { is: "uip-test-probe-body" });
      body.setAttribute("data-probe", "body");
      this.originalBody = document.body;
      document.documentElement.replaceChild(body, document.body);
    }
    this.ledger.install();
  }

  /** A listener of the harness's own, invisible to the ledger. */
  ownListen(target: EventTarget, type: string, fn: (e: Event) => void, capture = false): void {
    this.ledger.own++;
    try {
      target.addEventListener(type, fn, capture);
    } finally {
      this.ledger.own--;
    }
    this.cleanups.push(() => {
      nativeRemove.call(target, type, fn, capture);
    });
  }

  /** A probe element of `tag` labelled `label` in hit kinds. */
  probe(label: string, tag: "probe" | "dialog" | "button" = "probe"): HTMLElement {
    return this.quiet(() => {
      let el: HTMLElement;
      if (tag === "dialog") {
        el = document.createElement("dialog", { is: "uip-test-probe-dialog" });
      } else if (tag === "button") {
        el = document.createElement("button", { is: "uip-test-probe-button" });
        (el as HTMLButtonElement).type = "button";
      } else {
        el = document.createElement("uip-test-probe");
        el.tabIndex = 0;
      }
      el.setAttribute("data-probe", label);
      this.ownListen(el, "focus", () => {
        this.write({ el, name: "focus", old: null, value: null });
        this.hit(`focus@${label}`, el);
      });
      return el;
    });
  }

  /** An open `popover="auto"` probe, which `showModal()` hides: its `beforetoggle` is a hit. */
  openPopover(): void {
    this.quiet(() => {
      const pop = this.probe("popover");
      pop.popover = "auto";
      document.body.append(pop);
      pop.showPopover();
    });
  }

  /** Run `fn` with hits suspended (harness setup and reads); its writes are not effects. */
  quiet<T>(fn: () => T): T {
    this.takeWrites(this.window);
    this.suspended++;
    try {
      return fn();
    } finally {
      this.suspended--;
      this.takeWrites(null);
    }
  }

  /** Move the probe attribute writes made since the last take into `win`, or drop them.
   *  A record is queued at the write itself, where its custom element reaction can
   *  be delivered later, at the end of the platform call that made it. */
  private takeWrites(win: EffectWindow | null): void {
    const records = this.writes.takeRecords();
    for (const [i, r] of records.entries()) {
      const el = r.target as Element;
      const name = r.attributeName ?? "";
      // A record holds the value before its write; the value after it is the
      // next record's old value for the same attribute, or the live one.
      const next = records.find((n, j) => j > i && n.target === el && n.attributeName === name);
      const value = next === undefined ? el.getAttribute(name) : next.oldValue;
      if (name === "open" && (r.oldValue === null) !== (value === null)) {
        this.endToggle(el);
        if (value === null && this.raw?.dialog !== el) {
          this.ownClosing.delete(el);
        }
      }
      if (win !== null && isProbe(el) && (OBSERVED as readonly string[]).includes(name)) {
        this.write({ el, name, old: r.oldValue, value }, win);
      }
    }
  }

  inject(at: number, actions: (() => void)[]): void {
    this.injections.set(at, actions);
  }

  /** Run `actions` at the `nth` hit of `kind` (0 for the first) in this run. */
  injectHit(kind: string, nth: number, actions: (() => void)[]): void {
    const at = this.byKind.get(kind) ?? new Map<number, (() => void)[]>();
    at.set(nth, [...(at.get(nth) ?? []), ...actions]);
    this.byKind.set(kind, at);
  }

  /** The pinned hits this run never reached. */
  unreached(): string[] {
    return [...this.byKind].flatMap(([kind, at]) =>
      [...at.keys()].map((nth) => `${kind} #${nth.toString()}`),
    );
  }

  /** Place a generated or pinned injection: a pinned hit by kind, else a dry-run trace index. */
  place(
    inj: { readonly at: number; readonly hit?: string },
    traceLength: number,
    actions: (() => void)[],
  ): void {
    if (inj.hit === undefined) {
      this.inject(placeAt(inj.at, traceLength), actions);
    } else {
      this.injectHit(inj.hit, inj.at, actions);
    }
  }

  hit(kind: string, el?: Element): void {
    if (this.suspended > 0) {
      return;
    }
    // Ruled on while the page is as the write left it: a probe's reaction to its own write is a hit.
    this.takeWrites(this.window);
    const toggle = /beforetoggle:(open|closed)/.exec(kind);
    if (el !== undefined && toggle !== null) {
      this.toggling.set(el, [...(this.toggling.get(el) ?? []), toggle[1] ?? ""]);
      if (toggle[1] === "closed" && this.raw?.dialog !== el) {
        this.ownClosing.add(el);
      }
    }
    this.checkRaw();
    const index = this.trace.length;
    this.trace.push(kind);
    this.onHit?.(kind);
    const seen = this.kindSeen.get(kind) ?? 0;
    this.kindSeen.set(kind, seen + 1);
    let actions = this.injections.get(index);
    this.injections.delete(index);
    const pinned = this.byKind.get(kind)?.get(seen);
    if (pinned !== undefined) {
      this.byKind.get(kind)?.delete(seen);
      if (this.byKind.get(kind)?.size === 0) {
        this.byKind.delete(kind);
      }
      actions = [...(actions ?? []), ...pinned];
    }
    if (actions === undefined) {
      return;
    }
    const raw = this.raw;
    this.raw = null;
    this.endWindow(false);
    try {
      for (const action of actions) {
        if (kind === "window-error") {
          // The platform does not report an error thrown from an error listener again.
          try {
            action();
          } catch (error) {
            this.observed.push({ error, channel: "window" });
          }
        } else {
          action();
        }
        // A drive an injection inside this action interrupted ended with the action.
        this.endWindow(true);
      }
    } finally {
      this.raw = raw;
      if (raw !== null) {
        raw.seen = raw.dialog.open;
      }
      // The injected actions' own writes are not the window's.
      this.takeWrites(null);
      this.window = {
        at: kind,
        inFlight: new Set(this.toggling.keys()),
        refused: [],
        writes: [],
        timers: new Map(),
        cancelled: false,
        listeners: [],
      };
    }
  }

  /** The case's own code changes the target outside an injection (a world hook
   *  that disposes): the open window ends with its writes unjudged. */
  newTarget(): void {
    this.endWindow(false);
  }

  /** A controller was rolled back inside the entry that is returning, at a moment
   *  the case cannot place, so its teardown may be in the window: nothing in it is judged. */
  rolledBack(): void {
    this.takeWrites(null);
    this.window = null;
  }

  /** End the open window; `judge` compares its writes with the element now, the
   *  newest target realized, which holds only when its drive has ended. */
  private endWindow(judge: boolean): void {
    this.takeWrites(this.window);
    const win = this.window;
    if (win === null) {
      return;
    }
    this.window = null;
    const at = `after the injection at ${win.at}`;
    const why: string[] = win.refused.map((r) => `${r} is not admissible`);
    if (judge) {
      for (const w of win.writes) {
        const bad = this.quiet(() => inadmissible(w));
        if (bad !== null) {
          why.push(`${describe(w)}: ${bad}`);
        }
      }
    }
    if (win.cancelled) {
      why.push("a timer was started and then cancelled");
    }
    const gone = win.listeners.find((e) => !this.ledger.holds(e));
    if (gone !== undefined) {
      why.push(`a ${gone.type} listener was added and then removed`);
    }
    if (why[0] !== undefined) {
      this.inadmissible.push(`${at}, ${why[0]}`);
    }
  }

  /** The dialog's next `toggle` is a hit, at the target after the listeners already
   *  there, as page code registered after the library's would run. */
  private armToggleHit(target: Element): void {
    if (!(target instanceof HTMLDialogElement) || this.toggleArmed.has(target)) {
      return;
    }
    this.toggleArmed.add(target);
    const onToggle = (e: Event): void => {
      this.toggleArmed.delete(target);
      const state = (e as ToggleEvent).newState;
      if (isProbe(target)) {
        probeHit(target, `toggle:${state}`);
      } else {
        this.hit(`doc:toggle:${state}`);
      }
    };
    nativeAdd.call(target, "toggle", onToggle, { once: true });
    this.cleanups.push(() => {
      nativeRemove.call(target, "toggle", onToggle);
    });
  }

  private endToggle(el: Element): void {
    const stack = this.toggling.get(el);
    stack?.pop();
    if (stack?.length === 0) {
      this.toggling.delete(el);
    }
  }

  /** Whether a showModal of `dialog` is under way: it will be open when the call returns. */
  opening(dialog: Element): boolean {
    return this.toggling.get(dialog)?.at(-1) === "open" && !dialog.hasAttribute("open");
  }

  /** A probe write or focus: ruled on now by `admit`, or kept for the default rule at the window's end. */
  write(w: Write, win: EffectWindow | null = this.window): void {
    if (win === null || this.suspended > 0) {
      return;
    }
    if (w.name === "open" && (w.old === null) !== (w.value === null) && win.inFlight.delete(w.el)) {
      return;
    }
    const verdict = this.admit?.(w);
    if (verdict === false) {
      win.refused.push(describe(w));
    } else if (verdict === undefined && w.name !== "focus") {
      win.writes.push(w);
    }
  }

  /** At a quiescent point: the first effect since an injection that the newest target does not call for. */
  checkEffects(where: string): string | null {
    this.endWindow(true);
    this.toggling.clear();
    this.ownClosing.clear();
    const first = this.inadmissible.splice(0)[0];
    return first === undefined ? null : `${where}: ${first}`;
  }

  /** Run one entry of the script or an injection; the outermost one's return ends the window. */
  private nest(fn: () => void): void {
    this.depth++;
    try {
      fn();
    } finally {
      this.depth--;
      if (this.depth === 0) {
        this.endWindow(true);
        this.toggling.clear();
        this.ownClosing.clear();
      }
    }
  }

  effect(probe: string, kind: string, value: string | null): void {
    if (this.suspended === 0) {
      this.effects.push({ probe, kind, value, seq: this.seq });
    }
  }

  /** A fresh sentinel, recorded as thrown. */
  sentinel(): Sentinel {
    const s = new Sentinel(this.thrown.length + this.lossy.length + 1);
    (this.reporting > 0 ? this.lossy : this.thrown).push(s.id);
    return s;
  }

  emit(event: E): void {
    this.events.push(event);
  }

  /** Mark a release in the effect stream (see constructiveAfterRelease). */
  markRelease(): void {
    this.effects.push({ probe: "", kind: "release", value: null, seq: this.seq });
  }

  /** Take the events emitted since the last call, in order. */
  drain(): E[] {
    return this.events.splice(0);
  }

  /** One entry by the script or an injection: logged, and its throw observed. */
  entry(event: E | null, fn: () => void): void {
    this.seq++;
    if (event !== null) {
      this.events.push(event);
    }
    this.nest(() => {
      try {
        fn();
      } catch (error) {
        this.observed.push({ error, channel: "entry" });
      }
    });
  }

  /** Advance fake timers as one entry; a timer callback's throw is observed. */
  advance(ms: number, event: E | null): void {
    this.seq++;
    if (event !== null) {
      this.events.push(event);
    }
    this.nest(() => {
      try {
        vi.advanceTimersByTime(ms);
      } catch (error) {
        this.observed.push({ error, channel: "timer" });
      }
    });
  }

  private checkRaw(): void {
    const raw = this.raw;
    if (raw === null || raw.dialog.open === raw.seen) {
      return;
    }
    raw.seen = raw.dialog.open;
    this.endWindow(false);
    if (this.onEdge !== undefined) {
      this.events.push(
        this.onEdge(
          raw.dialog,
          raw.seen,
          this.opening(raw.dialog),
          this.ownClosing.has(raw.dialog),
        ),
      );
    }
  }

  /** A change the browser makes without the library. */
  platform(dialog: HTMLDialogElement, kind: PlatformKind): void {
    this.seq++;
    this.nest(() => {
      this.platformCall(dialog, kind);
    });
  }

  private platformCall(dialog: HTMLDialogElement, kind: PlatformKind): void {
    this.rawTouched.add(dialog);
    // Taken at both ends, so every `open` record is read with the raw frame that made it.
    this.takeWrites(this.window);
    const previous = this.raw;
    this.raw = { dialog, seen: dialog.open };
    try {
      switch (kind) {
        case "form":
          dialog.querySelector<HTMLFormElement>("form[method=dialog]")?.requestSubmit();
          break;
        case "requestClose":
          dialog.requestClose();
          break;
        case "closeWatcher":
          if (dialog.open) {
            dialog.dispatchEvent(new Event("cancel", { cancelable: false }));
            this.checkRaw();
            dialog.close();
          }
          break;
        case "close":
          dialog.close();
          break;
        case "showModal":
          dialog.showModal();
          break;
        case "show":
          dialog.show();
          break;
        case "sameOpen":
          // eslint-disable-next-line no-self-assign -- the reflected write queues an `open` record and changes nothing
          dialog.open = dialog.open;
          break;
        case "addOpen":
          if (!dialog.open) {
            dialog.setAttribute("open", "");
          }
          break;
        case "removeOpen":
          if (dialog.open && dialog.matches(":modal")) {
            this.stranded.add(dialog);
          }
          dialog.removeAttribute("open");
          break;
        case "reopenModal":
          dialog.close();
          dialog.showModal();
          break;
        case "reopenShow":
          dialog.close();
          dialog.show();
          break;
      }
    } catch (error) {
      this.observed.push({ error, channel: "platform" });
    } finally {
      this.takeWrites(this.window);
      this.checkRaw();
      this.raw = previous;
      if (previous !== null) {
        previous.seen = previous.dialog.open;
      }
    }
  }

  /** Deliver the queued `toggle` events of every dialog a raw call changed. */
  async quiesce(): Promise<void> {
    while (this.rawTouched.size > 0) {
      const touched = [...this.rawTouched];
      this.rawTouched.clear();
      for (const dialog of touched) {
        await this.settleToggle(dialog);
      }
    }
  }

  /** Wait for the queued `toggle` event of a raw change to be delivered. */
  async settleToggle(dialog: HTMLDialogElement): Promise<void> {
    await new Promise<void>((resolve) => {
      const done = (): void => {
        nativeRemove.call(dialog, "toggle", done);
        resolve();
      };
      nativeAdd.call(dialog, "toggle", done);
      // A change undone in the same task may queue no event, and task sources
      // are not ordered against each other, so a few task ticks bound the wait.
      queueTask(() => {
        queueTask(() => {
          queueTask(done);
        });
      });
    });
    // Code a `toggle` listener queued runs before the case looks.
    await nextTask();
  }

  /** Deliver the `toggle` events a case's cleanup queued, with hits suspended,
   *  so they run inside this world rather than the next case's. */
  async settleCleanup(): Promise<void> {
    this.suspended++;
    try {
      for (let i = 0; i < 3; i++) {
        await nextTask();
      }
    } finally {
      this.suspended--;
    }
  }

  private wrapTimers(): void {
    const g = globalThis as unknown as Record<string, (...args: unknown[]) => unknown>;
    const pairs = [
      ["setTimeout", "clearTimeout"],
      ["requestAnimationFrame", "cancelAnimationFrame"],
    ] as const;
    for (const [start, stop] of pairs) {
      const startFn = g[start];
      const stopFn = g[stop];
      if (startFn === undefined || stopFn === undefined) {
        continue;
      }
      g[start] = (fn: unknown, ...rest: unknown[]): unknown => {
        const record = { fired: false };
        const id = startFn(
          typeof fn === "function"
            ? (...args: unknown[]): unknown => {
                record.fired = true;
                this.onTimer?.();
                try {
                  return (fn as (...a: unknown[]) => unknown)(...args);
                } finally {
                  // The callback's drive has ended.
                  this.endWindow(true);
                }
              }
            : fn,
          ...rest,
        );
        this.window?.timers.set(id, record);
        return id;
      };
      g[stop] = (id: unknown): unknown => {
        const win = this.window;
        if (win !== null && win.timers.get(id)?.fired === false) {
          win.cancelled = true;
        }
        return stopFn(id);
      };
      this.cleanups.push(() => {
        g[start] = startFn;
        g[stop] = stopFn;
      });
    }
  }

  dispose(): void {
    this.writes.disconnect();
    this.ledger.uninstall();
    for (const fn of this.cleanups.splice(0).reverse()) {
      fn();
    }
    if (this.originalBody !== null) {
      document.documentElement.replaceChild(this.originalBody, document.body);
      this.originalBody = null;
    }
    if (current === this) {
      current = null;
    }
  }
}

function describe(w: Write): string {
  const label = labelOf(w.el);
  return w.name === "focus"
    ? `a focus of ${label}`
    : `${label} ${w.name}=${JSON.stringify(w.value)}`;
}

const scratch = document.createElement("div");

function styleMap(value: string | null): Map<string, string> {
  scratch.setAttribute("style", value ?? "");
  const out = new Map<string, string>();
  for (const prop of Array.from(scratch.style)) {
    out.set(prop, scratch.style.getPropertyValue(prop));
  }
  return out;
}

/** The inline style properties a `style` write changed, with their new values ("" when removed). */
export function changedStyles(w: Write): Map<string, string> {
  const before = styleMap(w.old);
  const after = styleMap(w.value);
  const out = new Map<string, string>();
  for (const prop of new Set([...before.keys(), ...after.keys()])) {
    const v = after.get(prop) ?? "";
    if (v !== (before.get(prop) ?? "")) {
      out.set(prop, v);
    }
  }
  return out;
}

/** Whether a write only takes something away, as a popup's terminal pass does:
 *  no class token added, `hidden` set, any other attribute removed; a focus is
 *  left to the default rule. */
export function takesAway(w: Write): boolean | undefined {
  if (w.name === "focus") {
    return undefined;
  }
  if (w.name === "class") {
    const before = new Set((w.old ?? "").split(/\s+/));
    return (w.value ?? "").split(/\s+/).every((t) => t === "" || before.has(t));
  }
  return w.name === "hidden" ? w.value !== null : w.value === null;
}

/** Why a write is not a step toward the element's state at this quiescent point, or null. */
function inadmissible(w: Write): string | null {
  const now = w.el.getAttribute(w.name);
  if (w.name === "class") {
    const before = new Set((w.old ?? "").split(/\s+/).filter((t) => t !== ""));
    const after = new Set((w.value ?? "").split(/\s+/).filter((t) => t !== ""));
    const final = new Set((now ?? "").split(/\s+/).filter((t) => t !== ""));
    for (const t of after) {
      if (!before.has(t) && !final.has(t)) {
        return `added ${t}, which the quiescent element does not carry`;
      }
    }
    for (const t of before) {
      if (!after.has(t) && final.has(t)) {
        return `removed ${t}, which the quiescent element carries`;
      }
    }
    return null;
  }
  if (w.name === "style") {
    const before = styleMap(w.old);
    const after = styleMap(w.value);
    const final = styleMap(now);
    for (const prop of new Set([...before.keys(), ...after.keys()])) {
      const v = after.get(prop) ?? "";
      if (v !== (before.get(prop) ?? "") && v !== (final.get(prop) ?? "")) {
        return `${prop}: ${v}, quiescent ${final.get(prop) ?? "(none)"}`;
      }
    }
    return null;
  }
  return w.value === now ? null : `quiescent ${JSON.stringify(now)}`;
}

/** A real task (fake timers leave MessageChannel alone). */
function queueTask(fn: () => void): void {
  const channel = new MessageChannel();
  channel.port1.onmessage = (): void => {
    channel.port1.close();
    fn();
  };
  channel.port2.postMessage(null);
}

export async function nextTask(): Promise<void> {
  await new Promise<void>((resolve) => {
    queueTask(resolve);
  });
}

/** Every sentinel thrown in a case observed as `World.thrown` and `World.lossy`
 *  say, and nothing unexpected. */
export function sentinelsOnce(w: World, allowed: (error: unknown) => boolean): string | null {
  const seen: number[] = [];
  const visit = (error: unknown): string | null => {
    if (error instanceof Sentinel) {
      seen.push(error.id);
      return null;
    }
    if (error instanceof AggregateError) {
      for (const inner of error.errors) {
        const bad = visit(inner);
        if (bad !== null) {
          return bad;
        }
      }
      return null;
    }
    return allowed(error) ? null : `unexpected error: ${String(error)}`;
  };
  for (const o of w.observed) {
    const bad = visit(o.error);
    if (bad !== null) {
      return `${bad} (${o.channel})`;
    }
  }
  const lost = new Set(w.lossy.filter((id) => !seen.includes(id)));
  const want = [...w.thrown, ...w.lossy].filter((id) => !lost.has(id)).sort((a, b) => a - b);
  const got = [...seen].sort((a, b) => a - b);
  return want.join(",") === got.join(",")
    ? null
    : `sentinels thrown [${want.join(",")}] but observed [${got.join(",")}]`;
}

export function isInvalidState(error: unknown): boolean {
  return error instanceof DOMException && error.name === "InvalidStateError";
}

/** Map a generated position onto a trace of length `n`. */
function placeAt(at: number, n: number): number {
  return n === 0 ? 0 : at % n;
}

/** Steps placed on trace indices of the dry run. */
export interface Injection<S> {
  /** A trace index, or with `hit`, which occurrence of that hit kind (0 for the first). */
  readonly at: number;
  /** Pins the injection to a hit kind, so a pinned case survives a change in the trace. */
  readonly hit?: string;
  readonly actions: readonly S[];
}

/** One generated case: script steps, then injections onto the dry run's trace. */
export interface Run<S> {
  readonly script: readonly S[];
  readonly injections: readonly Injection<S>[];
}

/** What a property supplies for one world. */
export interface Scenario<S> {
  /** Build the controllers (an entry: its hits are injection points). */
  start(): void;
  /** Perform one step. An injected step runs synchronously inside a hit; a
   *  script step may return a promise that ends at quiescence. */
  act(step: S, injected: boolean): Promise<void> | undefined;
  /** At a quiescent point: the first mismatch with the model, or null. */
  check(where: string): string | null;
  /** After the script: errors and logs, or null. */
  finish(): string | null;
  /** Put back what the case changed outside its own nodes. */
  cleanup(): void;
}

/** Run `run` as a dry run and then for real with its injections; the first failure. */
export async function explore<S, E>(
  make: (w: World<E>) => Scenario<S>,
  run: Run<S>,
  options: WorldOptions<E> = {},
): Promise<string | null> {
  const once = async (
    traceLength: number | null,
  ): Promise<{ n: number; failure: string | null }> => {
    const w = new World<E>(options);
    const s = make(w);
    try {
      if (traceLength !== null) {
        for (const inj of run.injections) {
          w.place(
            inj,
            traceLength,
            inj.actions.map((a) => () => {
              void s.act(a, true);
            }),
          );
        }
      }
      const checkAt = (where: string): string | null => s.check(where) ?? w.checkEffects(where);
      s.start();
      await w.quiesce();
      let failure = checkAt("start");
      for (const [i, step] of run.script.entries()) {
        if (failure !== null) {
          break;
        }
        await s.act(step, false);
        await w.quiesce();
        failure = checkAt(`step ${i.toString()} ${JSON.stringify(step)}`);
      }
      failure ??= s.finish();
      const missed = w.unreached()[0];
      if (traceLength !== null && failure === null && missed !== undefined) {
        failure = `the pinned hit ${missed} was never reached`;
      }
      return { n: w.trace.length, failure };
    } finally {
      w.quiet(() => {
        s.cleanup();
      });
      await w.settleCleanup();
      w.dispose();
      vi.clearAllTimers();
      document.body.replaceChildren();
    }
  };
  const dry = await once(null);
  return (await once(dry.n)).failure;
}

/** Throw a fresh sentinel and record it as thrown. */
export function throwSentinel(w: World): never {
  throw w.sentinel();
}

/** The first failing [ok, why] pair, prefixed with `where`. */
export function firstFailure(
  where: string,
  checks: readonly (readonly [boolean, string])[],
): string | null {
  for (const [ok, why] of checks) {
    if (!ok) {
      return `${where}: ${why}`;
    }
  }
  return null;
}

/** Class writes adding one of `tokens` made after a release with no newer entry since. */
export function constructiveAfterRelease(
  w: World,
  probe: string,
  tokens: readonly string[],
): string | null {
  let releasedAt: number | null = null;
  let last = "";
  for (const e of w.effects) {
    if (e.kind === "release") {
      releasedAt = e.seq;
      continue;
    }
    if (e.probe !== probe || e.kind !== "attr:class") {
      continue;
    }
    const before = last.split(/\s+/);
    const after = (e.value ?? "").split(/\s+/);
    last = e.value ?? "";
    if (releasedAt === null || e.seq !== releasedAt) {
      continue;
    }
    const added = tokens.find((t) => after.includes(t) && !before.includes(t));
    if (added !== undefined) {
      return `${added} added to ${probe} after a release, before any newer entry`;
    }
  }
  return null;
}
