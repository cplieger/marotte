// popup-core.ts — INTERNAL lifecycle core shared by popup and popover. `popup`
// re-exports it as-is; `popover` layers placement on
// top through `PopupLayer`. One module keeps the group registry shared, so a
// popup and a popover can share a `group`.

import { untracked } from "@cplieger/reactive";

import { isContextPressRelease } from "./context-press.js";
import type { Fx } from "./fx.js";
import { topmostOpenDialog } from "./modal-host.js";
import { createDriver, ownedBinding, type Driver } from "./owner.js";
import { watchSettle } from "./transition.js";

/** The lifecycle half of a popup or popover: the trigger to annotate, which
 *  gestures dismiss the panel, single-open grouping, the two opt-in focus
 *  hand-offs, and the open/close hooks. All optional. Activation is
 *  deliberately absent — the caller decides what opens the panel. */
export interface PopupOptions {
  /** Trigger element to wire ARIA on (`aria-expanded`, `aria-haspopup`) and to
   *  exempt from outside-click dismissal (so a trigger click-handler can
   *  toggle without the document listener closing first). The controller does
   *  NOT wire click/keyboard activation on it — the caller owns that. Omit or
   *  pass `null` for a trigger-less popup. */
  trigger?: HTMLElement | null;
  /** Click outside the panel and trigger closes the popup. Default `true`. */
  closeOnOutside?: boolean;
  /** Escape closes the popup. Default `true`. */
  closeOnEscape?: boolean;
  /** Stop the Escape keydown's propagation when this popup consumes it, so a
   *  popup inside a modal doesn't also close the modal. Default `true`.
   *  Disable when an app-level Escape coordinator must still observe the key. */
  isolateEscape?: boolean;
  /** Single-open coordination group: opening this popup closes any open peer
   *  created with the same group name. See also `closePopupGroup`. */
  group?: string;
  /** Focus this element after the popup opens, if it is a connected element.
   *  Omit (or pass `null`) to leave focus alone — by default the caller owns
   *  focus. Opt-in. */
  initialFocus?: HTMLElement | null;
  /** Restore focus when the popup closes with focus inside it. `true` captures
   *  whatever was focused at open time and refocuses it on close; an element
   *  refocuses that element; `false`/omitted leaves focus alone. Opt-in. */
  returnFocus?: boolean | HTMLElement;
  /** aria-haspopup value advertised on the trigger. Match the panel's role.
   *  Default `true` (menu). Ignored without a trigger. */
  haspopup?: "menu" | "listbox" | "tree" | "grid" | "dialog" | true;
  /** Invoked after the popup opens. */
  onOpen?: () => void;
  /** Invoked after the popup closes. */
  onClose?: () => void;
}

/** A merge-patch for `setOptions`: keys PRESENT in the patch override the
 *  current value — including an explicit `undefined`, which clears the option
 *  back to its default. Keys absent from the patch are left unchanged. */
type PopupOptionsPatch = {
  [K in keyof PopupOptions]?: PopupOptions[K] | undefined;
};

/** Handle on a revealed-and-dismissed panel. The panel is the caller's element:
 *  this reveals it, hides it, and wires light dismissal, but never positions it
 *  and never removes it — motion belongs to the app's CSS, keyed off the state
 *  classes. */
export interface PopupController {
  show(): void;
  hide(): void;
  toggle(): void;
  readonly isOpen: boolean;
  /** Merge-patch the options (see {@link PopupOptionsPatch}), each key read
   *  once. Changed dismissal flags re-arm the listeners while open; a
   *  `trigger` or `haspopup` change applies at once: the old trigger loses the
   *  ARIA and, while open, the new one gains it. A patch whose values equal
   *  the current ones changes nothing. */
  setOptions(patch: PopupOptionsPatch): void;
  /** Hide the panel at once, with no fade and no `onClose`, restore focus the
   *  popup moved, drop the trigger's ARIA and leave the group. */
  dispose(): void;
}

/** Every PopupOptions key present, defaults resolved, frozen; `null` for an
 *  absent option whose default is no value. */
export interface PopupConfig {
  readonly trigger: HTMLElement | null;
  readonly closeOnOutside: boolean;
  readonly closeOnEscape: boolean;
  readonly isolateEscape: boolean;
  readonly group: string | null;
  readonly initialFocus: HTMLElement | null;
  readonly returnFocus: boolean | HTMLElement;
  /** The serialized attribute; `true` and absence normalize to `"true"`. */
  readonly haspopup: "true" | "menu" | "listbox" | "tree" | "grid" | "dialog";
  readonly onOpen: (() => void) | null;
  readonly onClose: (() => void) | null;
}

/** The normalized value of one popup option, or `undefined` for a key that is not one. */
export function normalizePopupKey(key: string, value: unknown): unknown {
  switch (key) {
    case "trigger":
    case "initialFocus":
      return value ?? null;
    case "closeOnOutside":
    case "closeOnEscape":
    case "isolateEscape":
      return value ?? true;
    case "group":
      return value ?? null;
    case "returnFocus":
      return value ?? false;
    case "haspopup":
      return value === undefined || value === true ? "true" : value;
    case "onOpen":
    case "onClose":
      return value ?? null;
    default:
      return undefined;
  }
}

export const POPUP_KEYS = [
  "trigger",
  "closeOnOutside",
  "closeOnEscape",
  "isolateEscape",
  "group",
  "initialFocus",
  "returnFocus",
  "haspopup",
  "onOpen",
  "onClose",
] as const;

/** Read each of `keys` from `opts` once, normalized, into a frozen record. */
export function readOptions(
  opts: object | undefined,
  keys: readonly string[],
  normalize: (key: string, value: unknown) => unknown,
): Readonly<Record<string, unknown>> {
  const source = (opts ?? {}) as Partial<Record<string, unknown>>;
  return Object.freeze(Object.fromEntries(keys.map((k) => [k, normalize(k, source[k])])));
}

/** Read each popup option once into a frozen config. */
export function normalizePopupOptions(opts: PopupOptions | undefined): PopupConfig {
  return readOptions(opts, POPUP_KEYS, normalizePopupKey) as unknown as PopupConfig;
}

/** A patch read once: each own enumerable key's normalized value. */
type ReadPatch = readonly (readonly [string, unknown])[];

/** Read `patch`'s own enumerable keys once each, normalized; keys `normalize`
 *  does not know are dropped. An explicit `undefined` resets a key to its default. */
function readPatch(patch: object, normalize: (key: string, value: unknown) => unknown): ReadPatch {
  const source = patch as Record<string, unknown>;
  const out: (readonly [string, unknown])[] = [];
  for (const key of Object.keys(source)) {
    const value = normalize(key, source[key]);
    if (value !== undefined) {
      out.push([key, value]);
    }
  }
  return out;
}

/** `current` with `entries` applied, or `current` itself when no field differs under Object.is. */
function mergeOptions<C extends object>(current: C, entries: ReadPatch): C {
  let next: Record<string, unknown> | null = null;
  for (const [key, value] of entries) {
    if (!Object.is((current as Record<string, unknown>)[key], value)) {
      next ??= { ...(current as Record<string, unknown>) };
      next[key] = value;
    }
  }
  return next === null ? current : (Object.freeze(next) as C);
}

/** What a layer adds to a popup's realization (popover's placement). */
interface PopupLayer<C> {
  readonly stateClass: string;
  /** Realize the layer's part of an open panel, on every pass while open. */
  shown?(fx: Fx, config: C): void;
  /** Realize the layer's part of a finished leave. */
  leaveEnd?(fx: Fx): void;
  /** Alongside the dismissal listeners' arm and disarm. */
  listeners?(armed: boolean): void;
}

interface Member {
  readonly isOpen: () => boolean;
  /** Set this member closed without driving it. */
  readonly close: () => void;
  readonly flush: () => void;
}

const groups = new Map<string, Set<Member>>();

function joinGroup(name: string, member: Member): void {
  let set = groups.get(name);
  if (set === undefined) {
    set = new Set();
    groups.set(name, set);
  }
  set.add(member);
}

function leaveGroup(name: string, member: Member): void {
  const set = groups.get(name);
  if (set === undefined) {
    return;
  }
  set.delete(member);
  if (set.size === 0) {
    groups.delete(name);
  }
}

/** Close every open popup in `group` (e.g. collapse all expandable pills when
 *  the app moves focus elsewhere). Every member is hidden, then a callback's
 *  error is thrown, or an `AggregateError` of several. Unknown group names are
 *  a no-op. */
export function closePopupGroup(group: string): void {
  const members = [...(groups.get(group) ?? [])].filter((m) => m.isOpen());
  for (const m of members) {
    m.close();
  }
  const errors: unknown[] = [];
  for (const m of members) {
    try {
      m.flush();
    } catch (error) {
      errors.push(error);
    }
  }
  if (errors.length === 1) {
    throw errors[0];
  }
  if (errors.length > 1) {
    throw new AggregateError(errors, "legacy-ui: several callbacks threw");
  }
}

interface PopupState<C> {
  readonly open: boolean;
  readonly config: C;
  /** The element focused when show() was called. */
  readonly opener: HTMLElement | null;
  readonly shows: number;
  readonly hides: number;
  /** The hide whose leave has settled. */
  readonly settled: number;
  /** The peers this show set closed, flushed by its realization. */
  readonly peers: readonly Member[];
}

/** Core implementation behind `createPopup` and `createPopover`. `config` is
 *  already normalized; `normalizeKey` normalizes a `setOptions` patch. */
export function createPopupCore<C extends PopupConfig>(
  panel: HTMLElement,
  config: C,
  layer: PopupLayer<C>,
  normalizeKey: (key: string, value: unknown) => unknown,
): { readonly controller: PopupController; readonly kick: () => void } {
  return ownedBinding((binding) => {
    let revealed = false;
    let annotated: HTMLElement | null = null;
    let joined: string | null = null;
    // forShow is the show the listeners were armed for: a hide and a show that
    // one pass coalesces still arm them again.
    let armed: {
      readonly outside: boolean;
      readonly escape: boolean;
      readonly forShow: number;
    } | null = null;
    // The event in dispatch when the listeners armed, usually the gesture that
    // opened the popup: it reaches the document listeners after them and must
    // not dismiss what it opened.
    let opening: Event | null = null;
    let listening = false;
    let leave: { readonly forHide: number; readonly cancel: () => void } | null = null;
    let focus: { forShow: number; restore: HTMLElement | null; moved: boolean } = {
      forShow: 0,
      restore: null,
      moved: false,
    };
    let restoredFor = 0;
    let announced = false;
    let endingFor: number | null = null;

    const hide = (): void => {
      const d = driver.state;
      if (!binding.ended && d.open) {
        driver.commit({ ...d, open: false, hides: d.hides + 1 });
      }
    };
    const show = (): void => {
      if (binding.ended) {
        return;
      }
      const d = driver.state;
      if (d.open) {
        // A show() while open re-places a popover.
        driver.kick();
        return;
      }
      const peers =
        d.config.group === null
          ? []
          : [...(groups.get(d.config.group) ?? [])].filter((m) => m !== member && m.isOpen());
      for (const peer of peers) {
        peer.close();
      }
      const active = document.activeElement;
      driver.commit({
        ...d,
        open: true,
        shows: d.shows + 1,
        opener: active instanceof HTMLElement ? active : null,
        peers,
      });
    };

    const onDocClick = (e: MouseEvent): void => {
      if (e === opening || isContextPressRelease(e)) {
        return;
      }
      const target = e.target;
      // A click on the trigger keeps the popup open: its own handler typically toggles.
      if (
        target instanceof Node &&
        (panel.contains(target) || driver.state.config.trigger?.contains(target) === true)
      ) {
        return;
      }
      hide();
    };
    const onKeyDown = (e: KeyboardEvent): void => {
      if (e.key === "Escape" && e !== opening) {
        // Isolated by default so the same Escape doesn't also close a parent modal.
        if (driver.state.config.isolateEscape) {
          e.stopPropagation();
        }
        hide();
      }
    };
    const disarm = (): void => {
      armed = null;
      opening = null;
      if (listening) {
        listening = false;
        document.removeEventListener("click", onDocClick);
        document.removeEventListener("keydown", onKeyDown);
        layer.listeners?.(false);
      }
    };
    const arm = (flags: {
      readonly outside: boolean;
      readonly escape: boolean;
      readonly forShow: number;
    }): void => {
      disarm();
      armed = flags;
      // Armed now, never on a timer: a page busy with queued tasks still runs input,
      // so a deferred arm left Escape and outside clicks unanswered while it waited.
      // `window.event` is the DOM's "current event" (https://dom.spec.whatwg.org/#dom-window-event).
      // eslint-disable-next-line @typescript-eslint/no-deprecated -- the one API naming the event in dispatch
      opening = window.event ?? null;
      listening = true;
      if (flags.outside) {
        document.addEventListener("click", onDocClick);
      }
      if (flags.escape) {
        document.addEventListener("keydown", onKeyDown);
      }
      layer.listeners?.(true);
    };
    const syncGroup = (name: string | null): void => {
      if (joined !== name) {
        if (joined !== null) {
          leaveGroup(joined, member);
        }
        joined = name;
        if (name !== null) {
          joinGroup(name, member);
        }
      }
    };
    const cancelLeave = (): void => {
      leave?.cancel();
      leave = null;
    };
    /** Move focus back out, once per hide, only while it is inside the panel: to the restore
     *  target, else off the panel. Focus a click put elsewhere, `<body>` included, stays
     *  (https://html.spec.whatwg.org/multipage/popover.html, the hide popover algorithm). */
    const restoreFocus = (fx: Fx): void => {
      const { restore, moved } = focus;
      focus = { ...focus, restore: null, moved: false };
      const active = document.activeElement;
      if (!(active instanceof HTMLElement) || !panel.contains(active)) {
        return;
      }
      if (restore?.isConnected === true) {
        fx.focus(restore);
        return;
      }
      if (moved) {
        fx.blur(active);
      }
    };
    const settle = (h: number): void => {
      const d = driver.state;
      if (!d.open && d.hides === h && d.settled !== h) {
        driver.commit({ ...d, settled: h });
      }
    };

    const driver: Driver<PopupState<C>> = createDriver<PopupState<C>>({
      initial: { open: false, config, opener: null, shows: 0, hides: 0, settled: 0, peers: [] },
      realize(fx, d) {
        const c = d.config;
        syncGroup(c.group);
        if (annotated !== null && annotated !== c.trigger) {
          const old = annotated;
          fx.removeAttr(old, "aria-expanded");
          fx.removeAttr(old, "aria-haspopup");
          annotated = null;
        }
        if (d.open) {
          cancelLeave();
          fx.removeClass(panel, "is-leaving");
          for (const peer of d.peers) {
            fx.call(peer.flush);
          }
          revealed = true;
          fx.addClass(panel, layer.stateClass);
          fx.setProp(panel, "hidden", false);
          if (!panel.isConnected) {
            // showModal() inerts everything outside the dialog subtree, so a
            // disconnected panel must host inside the open dialog to stay usable.
            const host = c.trigger?.closest("dialog[open]") ?? topmostOpenDialog() ?? document.body;
            fx.append(host, panel);
          }
          if (!panel.classList.contains("is-open")) {
            // The resting state is committed first, so the transition plays from it.
            panel.getBoundingClientRect();
            fx.addClass(panel, "is-open");
          }
          layer.shown?.(fx, c);
          if (c.trigger !== null) {
            annotated = c.trigger;
            fx.setAttr(c.trigger, "aria-expanded", "true");
            fx.setAttr(c.trigger, "aria-haspopup", c.haspopup);
          }
          if (focus.forShow !== d.shows) {
            const moved = c.initialFocus?.isConnected === true;
            const restore =
              c.returnFocus instanceof HTMLElement
                ? c.returnFocus
                : c.returnFocus || moved
                  ? d.opener
                  : null;
            // The token first: a restart must not move focus twice.
            focus = { forShow: d.shows, restore, moved };
            if (moved) {
              fx.focus(c.initialFocus);
            }
          }
          if (
            armed?.forShow !== d.shows ||
            armed.outside !== c.closeOnOutside ||
            armed.escape !== c.closeOnEscape
          ) {
            arm({ outside: c.closeOnOutside, escape: c.closeOnEscape, forShow: d.shows });
          }
          return;
        }
        disarm();
        if (annotated !== null) {
          // Both halves: a restart inside the open branch's pair can leave one unwritten.
          fx.setAttr(annotated, "aria-haspopup", c.haspopup);
          fx.setAttr(annotated, "aria-expanded", "false");
        }
        if (restoredFor !== d.hides) {
          restoredFor = d.hides;
          restoreFocus(fx);
        }
        if (!revealed) {
          return;
        }
        // The leave: two class diffs, the watcher, then the end writes once it
        // settled. A panel a restart left hidden has nothing to fade.
        if (d.settled !== d.hides && !(panel.hidden && !panel.classList.contains("is-leaving"))) {
          if (panel.classList.contains("is-open") || !panel.classList.contains("is-leaving")) {
            panel.getBoundingClientRect();
          }
          fx.removeClass(panel, "is-open");
          fx.addClass(panel, "is-leaving");
          if (leave?.forHide !== d.hides) {
            cancelLeave();
            const h = d.hides;
            leave = {
              forHide: h,
              cancel: watchSettle(panel, () => {
                settle(h);
              }),
            };
          }
          return;
        }
        if (endingFor !== d.hides) {
          if (!panel.classList.contains("is-leaving")) {
            // Nothing to fade: another controller revealed the panel, or a
            // restart left it hidden before it was revealed.
            cancelLeave();
            revealed = false;
            return;
          }
          // Recorded first: a restart inside the end writes must finish them.
          endingFor = d.hides;
        }
        cancelLeave();
        fx.removeClass(panel, "is-leaving");
        layer.leaveEnd?.(fx);
        fx.setProp(panel, "hidden", true);
        revealed = false;
        endingFor = null;
      },
      notify(fx, d) {
        if (d.open && !announced) {
          announced = true;
          const onOpen = d.config.onOpen;
          if (onOpen !== null) {
            fx.call(onOpen);
          }
        } else if (!d.open && announced) {
          announced = false;
          const onClose = d.config.onClose;
          if (onClose !== null) {
            fx.call(onClose);
          }
        }
      },
      terminal(fx) {
        disarm();
        cancelLeave();
        syncGroup(null);
        restoreFocus(fx);
        if (annotated !== null) {
          const trigger = annotated;
          annotated = null;
          fx.removeAttr(trigger, "aria-haspopup");
          fx.removeAttr(trigger, "aria-expanded");
        }
        if (revealed) {
          fx.removeClass(panel, "is-open", "is-leaving");
          layer.leaveEnd?.(fx);
          fx.setProp(panel, "hidden", true);
          revealed = false;
        }
      },
    });

    const member: Member = {
      isOpen: () => !binding.ended && driver.state.open,
      close: () => {
        const d = driver.state;
        if (d.open) {
          driver.set({ ...d, open: false, hides: d.hides + 1 });
        }
      },
      flush: () => {
        driver.flush();
      },
    };

    const controller: PopupController = {
      show,
      hide,
      toggle(): void {
        if (driver.state.open) {
          hide();
        } else {
          show();
        }
      },
      get isOpen(): boolean {
        return !binding.ended && driver.state.open;
      },
      setOptions(patch: PopupOptionsPatch): void {
        if (binding.ended) {
          return;
        }
        // Read before the state, so a getter that re-enters commits first.
        const entries = untracked(() => readPatch(patch, normalizeKey));
        const d = driver.state;
        const next = mergeOptions(d.config, entries);
        // A getter that disposed the controller released the driver, which then ignores this.
        if (next !== d.config) {
          driver.commit({ ...d, config: next });
        }
      },
      dispose: binding.dispose,
    };

    return {
      controller: {
        controller,
        kick: () => {
          driver.kick();
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
}
