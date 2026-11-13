// Counts the layouts a task forces: a layout read, or a layout-forcing write, made while a DOM write is pending since
// the previous one. A real browser exposes no layout counter, so a pending MutationObserver record stands for dirty
// layout; a style recalculation with no DOM write behind it (a `:hover` change) is not seen.

type LayoutAccessKind = "read" | "write";

interface LayoutAccess {
  readonly kind: LayoutAccessKind;
  /** The reader or writer's name, `getBoundingClientRect` or `scrollTop=`. */
  readonly what: string;
  readonly target: unknown;
  /** Whether a DOM write was pending, so the access forced a layout. */
  readonly forced: boolean;
}

export interface LayoutSpy {
  /** Discard every pending record and open a counting window: the layout is clean from here. */
  begin(): void;
  /** Close the window: the forced count and every read and forcing write in it, in order. */
  end(): { readonly forced: number; readonly log: readonly LayoutAccess[] };
  /** Restore every wrapped reader and writer. */
  dispose(): void;
}

const ELEMENT_GETTERS = [
  "scrollTop",
  "scrollHeight",
  "clientHeight",
  "clientWidth",
  "scrollWidth",
] as const;
const HTML_GETTERS = ["offsetHeight", "offsetWidth", "offsetTop"] as const;

/** Wrap the layout readers and forcing writers until `dispose`. One spy at a time. */
export function installLayoutSpy(): LayoutSpy {
  const restores: (() => void)[] = [];
  let open = false;
  let dirty = false;
  let log: LayoutAccess[] = [];
  // Records the microtask checkpoint delivers never reach `takeRecords`, so the callback marks the layout dirty too.
  const observer = new MutationObserver(() => {
    dirty = true;
  });
  observer.observe(document, {
    subtree: true,
    attributes: true,
    childList: true,
    characterData: true,
  });

  const note = (kind: LayoutAccessKind, what: string, target: unknown): void => {
    const pending = observer.takeRecords().length > 0 || dirty;
    dirty = false;
    if (open) {
      log.push({ kind, what, target, forced: pending });
    }
  };

  const wrapMethod = <T extends object>(proto: T, name: keyof T & string): void => {
    const original = proto[name] as unknown as (...args: unknown[]) => unknown;
    Object.defineProperty(proto, name, {
      configurable: true,
      writable: true,
      value: function (this: unknown, ...args: unknown[]): unknown {
        note(name === "scrollTo" ? "write" : "read", name, this);
        return original.apply(this, args);
      },
    });
    restores.push(() => {
      Object.defineProperty(proto, name, { configurable: true, writable: true, value: original });
    });
  };

  const wrapGetter = (proto: object, name: string, writes: boolean): void => {
    const desc = Object.getOwnPropertyDescriptor(proto, name);
    if (desc?.get === undefined) {
      throw new Error(`layout-spy: no getter ${name}`);
    }
    const { get, set } = desc;
    Object.defineProperty(proto, name, {
      configurable: true,
      enumerable: desc.enumerable ?? false,
      get(this: unknown): unknown {
        note("read", name, this);
        return get.call(this);
      },
      ...(set !== undefined && {
        set(this: unknown, v: unknown): void {
          if (writes) {
            note("write", `${name}=`, this);
          }
          set.call(this, v);
        },
      }),
    });
    restores.push(() => {
      Object.defineProperty(proto, name, desc);
    });
  };

  wrapMethod(Element.prototype, "getBoundingClientRect");
  wrapMethod(Element.prototype, "getClientRects");
  wrapMethod(Element.prototype, "scrollTo");
  wrapMethod(SVGGraphicsElement.prototype, "getScreenCTM");
  for (const name of ELEMENT_GETTERS) {
    wrapGetter(Element.prototype, name, name === "scrollTop");
  }
  for (const name of HTML_GETTERS) {
    wrapGetter(HTMLElement.prototype, name, false);
  }
  const computed = window.getComputedStyle.bind(window);
  window.getComputedStyle = (el: Element, pseudo?: string | null): CSSStyleDeclaration => {
    note("read", "getComputedStyle", el);
    return computed(el, pseudo);
  };
  restores.push(() => {
    window.getComputedStyle = computed;
  });

  return {
    begin(): void {
      observer.takeRecords();
      dirty = false;
      log = [];
      open = true;
    },
    end() {
      open = false;
      const out = log;
      log = [];
      return { forced: out.filter((a) => a.forced).length, log: out };
    },
    dispose(): void {
      open = false;
      observer.disconnect();
      for (const restore of restores.splice(0).reverse()) {
        restore();
      }
    },
  };
}
