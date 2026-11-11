/**
 * theme.ts — The stored theme choice, light, dark or system, written to an
 * attribute on `<html>` as the concrete theme and following the OS while it
 * is system. The module imports nothing, so marotte's pre-paint bundle
 * (`prepaint-steps.ts`) loads it alone.
 * @module
 */

/** The persisted preference: a pinned concrete theme, or `"system"` to follow
 *  the OS preference live. Distinct from the resolved value written to the
 *  document element, which is always concrete. */
type ThemeChoice = "light" | "dark" | "system";
type Resolved = "light" | "dark";

/** Persistence adapter for the theme preference. Encapsulates WHERE and HOW the
 *  value is stored, so an app can keep the theme inside its own structure (for
 *  example a field of a larger JSON blob) rather than a bare localStorage key. */
export interface ThemeStorage {
  /** Read the persisted preference, or `null` when nothing is stored yet. */
  get(): string | null;
  /** Persist the preference value. */
  set(value: string): void;
  /** Call `onExternalChange` when the stored value changes outside this
   *  controller, as in another tab. Returns an unsubscribe. The default adapter
   *  implements it over the `storage` event. */
  subscribe?(onExternalChange: () => void): () => void;
}

/** Where the preference is persisted, which attribute carries the resolved
 *  theme, and a hook for each application. `storageKey` is the only required
 *  field; supply `storage` when the value must live somewhere other than a
 *  bare localStorage key. */
interface ThemeOptions {
  /** localStorage key the default adapter persists under. Unused when a custom
   *  `storage` adapter is supplied (the adapter owns where the value lives). */
  storageKey: string;
  /** Persistence adapter. Defaults to bare `localStorage[storageKey]`. Supply a
   *  custom adapter to read/write the theme inside your own structure — e.g. a
   *  `theme` field of a JSON blob: `get` parses the blob and returns the field,
   *  `set` does a read-modify-write of that field. Everything else (tri-state
   *  resolution, following the OS while `system`, the applied attribute) is
   *  unchanged; a 2-state app simply never calls `set("system")`. */
  storage?: ThemeStorage;
  /** Attribute set on `<html>` with the resolved value. Default `data-theme`. */
  attribute?: string;
  /** Called with the resolved concrete theme whenever it changes/applies. */
  onChange?: (resolved: Resolved) => void;
  /** Called when the OS preference changes, whatever the choice, so a label
   *  such as "System (now dark)" stays true. */
  onSystemChange?: (system: Resolved) => void;
}

/** A live theme controller. The preference (`get`) and the theme actually
 *  applied (`resolved`) are separate values and differ whenever the preference
 *  is `"system"`. A controller keeps a matchMedia listener until `dispose`. */
export interface ThemeController {
  /** The stored preference (may be `"system"`). */
  get(): ThemeChoice;
  /** Persist + apply a preference. */
  set(choice: ThemeChoice): void;
  /** The concrete resolved theme (`"system"` → OS value via matchMedia). */
  resolved(): Resolved;
  /** Cycle light → dark → system → light. */
  cycle(): void;
  /** The current OS preference. */
  getSystem(): Resolved;
  /** Remove the matchMedia and storage listeners. */
  dispose(): void;
}

const DARK_QUERY = "(prefers-color-scheme: dark)";
const DEFAULT_ATTRIBUTE = "data-theme";

function isChoice(value: string | null): value is ThemeChoice {
  return value === "light" || value === "dark" || value === "system";
}

/** The bare-localStorage adapter. Reads and writes may throw where storage is
 *  blocked; the callers catch. */
function defaultStorage(storageKey: string): ThemeStorage {
  return {
    get: () => window.localStorage.getItem(storageKey),
    set: (value) => {
      window.localStorage.setItem(storageKey, value);
    },
    subscribe: (onExternalChange) => {
      // The storage event fires only in other documents of the origin, so a
      // tab's own write never calls back (HTML "The storage event").
      const onStorage = (e: StorageEvent): void => {
        let ours: boolean;
        try {
          ours = e.storageArea === window.localStorage;
        } catch {
          // Reading localStorage throws where storage is blocked.
          ours = false;
        }
        if (ours && (e.key === storageKey || e.key === null)) {
          onExternalChange();
        }
      };
      window.addEventListener("storage", onStorage);
      return () => {
        window.removeEventListener("storage", onStorage);
      };
    },
  };
}

function readChoice(storage: ThemeStorage): ThemeChoice {
  let stored: string | null;
  try {
    stored = storage.get();
  } catch {
    stored = null;
  }
  return isChoice(stored) ? stored : "system";
}

function resolve(choice: ThemeChoice, system: Resolved): Resolved {
  return choice === "system" ? system : choice;
}

/** Create a theme controller bound to the given storage key. Applies the
 *  resolved theme immediately and starts following the OS while in "system". */
export function createTheme(opts: ThemeOptions): ThemeController {
  const attribute = opts.attribute ?? DEFAULT_ATTRIBUTE;
  // try/catch-wrapped below, so blocked or absent storage degrades to
  // in-memory rather than throwing at construction.
  const storage: ThemeStorage = opts.storage ?? defaultStorage(opts.storageKey);
  const mql = typeof window.matchMedia === "function" ? window.matchMedia(DARK_QUERY) : null;

  let choice: ThemeChoice = readChoice(storage);

  const getSystem = (): Resolved => (mql?.matches === true ? "dark" : "light");
  const resolved = (): Resolved => resolve(choice, getSystem());

  const apply = (): void => {
    const value = resolved();
    document.documentElement.setAttribute(attribute, value);
    opts.onChange?.(value);
  };

  const set = (next: ThemeChoice): void => {
    choice = next;
    try {
      storage.set(next);
    } catch {
      // Storage denied, or a custom adapter threw — apply in memory only.
    }
    apply();
  };

  const cycle = (): void => {
    set(choice === "light" ? "dark" : choice === "dark" ? "system" : "light");
  };

  const onSystemChange = (): void => {
    if (choice === "system") {
      apply();
    }
    opts.onSystemChange?.(getSystem());
  };
  mql?.addEventListener("change", onSystemChange);

  // Another tab's choice re-applies here without writing it back, so two tabs
  // never echo each other.
  const unsubscribe = storage.subscribe?.(() => {
    choice = readChoice(storage);
    apply();
  });

  apply();

  return {
    get: () => choice,
    set,
    resolved,
    cycle,
    getSystem,
    dispose: () => {
      mql?.removeEventListener("change", onSystemChange);
      unsubscribe?.();
    },
  };
}
