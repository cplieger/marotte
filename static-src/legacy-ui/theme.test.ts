import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";

import { createTheme } from "./theme.js";
import type { ThemeStorage } from "./theme.js";

interface MediaState {
  matches: boolean;
  listeners: Set<(e: MediaQueryListEvent) => void>;
}

let media: MediaState;
let originalMatchMedia: typeof window.matchMedia;

function installMatchMedia(state: MediaState): void {
  window.matchMedia = ((query: string) => ({
    media: query,
    get matches() {
      return state.matches;
    },
    addEventListener: (_type: string, cb: (e: MediaQueryListEvent) => void) => {
      state.listeners.add(cb);
    },
    removeEventListener: (_type: string, cb: (e: MediaQueryListEvent) => void) => {
      state.listeners.delete(cb);
    },
    addListener: () => undefined,
    removeListener: () => undefined,
    onchange: null,
    dispatchEvent: () => true,
  })) as unknown as typeof window.matchMedia;
}

function fireSystemChange(matches: boolean): void {
  media.matches = matches;
  for (const cb of media.listeners) {
    cb({ matches } as MediaQueryListEvent);
  }
}

/** A single-slot in-memory ThemeStorage adapter (the value IS the preference;
 *  the adapter owns where it lives, so there is no key). */
function memoryStorage(): ThemeStorage {
  let value: string | null = null;
  return {
    get: () => value,
    set: (v) => {
      value = v;
    },
  };
}

beforeEach(() => {
  originalMatchMedia = window.matchMedia;
  media = { matches: false, listeners: new Set() };
  installMatchMedia(media);
  document.documentElement.removeAttribute("data-theme");
  document.documentElement.removeAttribute("data-mode");
});

afterEach(() => {
  window.matchMedia = originalMatchMedia;
});

describe("createTheme", () => {
  it("defaults to system and resolves the concrete theme via matchMedia", () => {
    media.matches = true; // system = dark
    const t = createTheme({ storageKey: "k", storage: memoryStorage() });
    expect(t.get()).toBe("system");
    expect(t.resolved()).toBe("dark");
    expect(t.getSystem()).toBe("dark");
    expect(document.documentElement.getAttribute("data-theme")).toBe("dark");
    t.dispose();
  });

  it("set() persists and applies a concrete choice", () => {
    const storage = memoryStorage();
    const t = createTheme({ storageKey: "k", storage });
    t.set("light");
    expect(t.get()).toBe("light");
    expect(t.resolved()).toBe("light");
    expect(document.documentElement.getAttribute("data-theme")).toBe("light");
    expect(storage.get()).toBe("light");
    t.dispose();
  });

  it("cycle goes light -> dark -> system -> light", () => {
    const t = createTheme({ storageKey: "k", storage: memoryStorage() });
    t.set("light");
    t.cycle();
    expect(t.get()).toBe("dark");
    t.cycle();
    expect(t.get()).toBe("system");
    t.cycle();
    expect(t.get()).toBe("light");
    t.dispose();
  });

  it("follows the OS while in system, but not once pinned", () => {
    const onChange = vi.fn();
    const t = createTheme({ storageKey: "k", storage: memoryStorage(), onChange });
    expect(t.resolved()).toBe("light");
    fireSystemChange(true);
    expect(document.documentElement.getAttribute("data-theme")).toBe("dark");
    expect(onChange).toHaveBeenCalledWith("dark");
    t.set("light");
    fireSystemChange(false);
    expect(document.documentElement.getAttribute("data-theme")).toBe("light");
    t.dispose();
  });

  it("reads a persisted preference on creation", () => {
    const storage = memoryStorage();
    storage.set("dark");
    const t = createTheme({ storageKey: "k", storage });
    expect(t.get()).toBe("dark");
    expect(t.resolved()).toBe("dark");
    t.dispose();
  });

  it("honors a persisted light preference over a dark OS preference", () => {
    media.matches = true; // OS = dark
    const storage = memoryStorage();
    storage.set("light");
    const t = createTheme({ storageKey: "k", storage });
    expect(t.get()).toBe("light");
    expect(t.resolved()).toBe("light");
    expect(document.documentElement.getAttribute("data-theme")).toBe("light");
    t.dispose();
  });

  it("ignores an OS change once a concrete choice is pinned", () => {
    const onChange = vi.fn();
    const t = createTheme({ storageKey: "k", storage: memoryStorage(), onChange });
    t.set("light");
    onChange.mockClear();
    fireSystemChange(true); // OS flips to dark
    expect(onChange).not.toHaveBeenCalled();
    expect(document.documentElement.getAttribute("data-theme")).toBe("light");
    t.dispose();
  });

  it("dispose removes the matchMedia listener", () => {
    const t = createTheme({ storageKey: "k", storage: memoryStorage() });
    expect(media.listeners.size).toBe(1);
    t.dispose();
    expect(media.listeners.size).toBe(0);
  });

  it("supports a custom attribute", () => {
    const t = createTheme({ storageKey: "k", storage: memoryStorage(), attribute: "data-mode" });
    t.set("dark");
    expect(document.documentElement.getAttribute("data-mode")).toBe("dark");
    t.dispose();
  });
});

describe("createTheme — default localStorage adapter", () => {
  it("reads and writes the bare localStorage key when no storage is supplied", () => {
    localStorage.removeItem("dt");
    const t = createTheme({ storageKey: "dt" });
    t.set("dark");
    expect(localStorage.getItem("dt")).toBe("dark");
    const t2 = createTheme({ storageKey: "dt" });
    expect(t2.get()).toBe("dark");
    t.dispose();
    t2.dispose();
    localStorage.removeItem("dt");
  });
});

describe("createTheme — custom storage adapter", () => {
  it("reads via adapter.get() on creation and writes via adapter.set() on set()", () => {
    let stored: string | null = "dark";
    let getCalls = 0;
    const writes: string[] = [];
    const storage: ThemeStorage = {
      get: () => {
        getCalls++;
        return stored;
      },
      set: (v) => {
        writes.push(v);
        stored = v;
      },
    };
    const t = createTheme({ storageKey: "unused", storage });
    expect(getCalls).toBeGreaterThan(0);
    expect(t.get()).toBe("dark");
    t.set("light");
    expect(writes).toEqual(["light"]);
    expect(stored).toBe("light");
    t.dispose();
  });

  it("degrades to in-memory when the adapter throws on get and set", () => {
    const t = createTheme({
      storageKey: "k",
      storage: {
        get: () => {
          throw new Error("blocked");
        },
        set: () => {
          throw new Error("blocked");
        },
      },
    });
    expect(t.get()).toBe("system");
    expect(() => {
      t.set("dark");
    }).not.toThrow();
    expect(t.get()).toBe("dark");
    expect(document.documentElement.getAttribute("data-theme")).toBe("dark");
    t.dispose();
  });

  it("persists into a JSON blob field via a read-modify-write adapter (marotte pattern)", () => {
    const KEY = "app.ui-state";
    localStorage.setItem(KEY, JSON.stringify({ sidebar: "open", theme: "dark" }));
    const jsonAdapter: ThemeStorage = {
      get: () => {
        const raw = localStorage.getItem(KEY);
        if (raw === null) {
          return null;
        }
        const parsed = JSON.parse(raw) as { theme?: string };
        return parsed.theme ?? null;
      },
      set: (value) => {
        const raw = localStorage.getItem(KEY);
        const blob = raw !== null ? (JSON.parse(raw) as Record<string, unknown>) : {};
        blob["theme"] = value;
        localStorage.setItem(KEY, JSON.stringify(blob));
      },
    };
    const t = createTheme({ storageKey: KEY, storage: jsonAdapter });
    expect(t.get()).toBe("dark");
    t.set("light");
    const after = JSON.parse(localStorage.getItem(KEY) ?? "{}") as Record<string, unknown>;
    expect(after["theme"]).toBe("light");
    expect(after["sidebar"]).toBe("open");
    t.dispose();
    localStorage.removeItem(KEY);
  });
});

describe("createTheme: an engine with no matchMedia", () => {
  it("degrades to light and attaches no OS listener", () => {
    vi.stubGlobal("matchMedia", undefined);
    const t = createTheme({ storageKey: "k", storage: memoryStorage() });
    // No matchMedia means nothing to subscribe to; consulting it would throw.
    expect(t.getSystem()).toBe("light");
    expect(t.resolved()).toBe("light");
    expect(document.documentElement.getAttribute("data-theme")).toBe("light");
    t.dispose();
  });
});

describe("createTheme: other tabs", () => {
  const KEY = "theme-tabs-test";
  afterEach(() => {
    localStorage.removeItem(KEY);
  });

  function otherTab(key: string | null, value: string | null, area: Storage = localStorage): void {
    if (key !== null && area === localStorage) {
      if (value === null) {
        localStorage.removeItem(key);
      } else {
        localStorage.setItem(key, value);
      }
    }
    window.dispatchEvent(new StorageEvent("storage", { key, newValue: value, storageArea: area }));
  }

  it("follows a choice another tab stored, without writing it back", () => {
    const changes: string[] = [];
    const theme = createTheme({ storageKey: KEY, onChange: (r) => changes.push(r) });
    changes.length = 0;
    otherTab(KEY, "dark");
    expect([theme.get(), document.documentElement.getAttribute("data-theme"), changes]).toEqual([
      "dark",
      "dark",
      ["dark"],
    ]);
    expect(localStorage.getItem(KEY)).toBe("dark");
    theme.dispose();
  });

  it("re-reads after another tab clears storage", () => {
    localStorage.setItem(KEY, "dark");
    const theme = createTheme({ storageKey: KEY });
    localStorage.clear();
    otherTab(null, null);
    expect([theme.get(), localStorage.getItem(KEY)]).toEqual(["system", null]);
    theme.dispose();
  });

  it("ignores another key, session storage, and everything once disposed", () => {
    const theme = createTheme({ storageKey: KEY });
    localStorage.setItem(KEY, "dark");
    otherTab("another-key", "dark");
    otherTab(KEY, "dark", sessionStorage);
    expect(theme.get()).toBe("system");
    theme.dispose();
    otherTab(KEY, "dark");
    expect(theme.get()).toBe("system");
  });

  it("follows a custom adapter that subscribes", () => {
    const storage = memoryStorage();
    let notify: () => void = () => undefined;
    const theme = createTheme({
      storageKey: KEY,
      storage: { ...storage, subscribe: (cb) => ((notify = cb), () => undefined) },
    });
    storage.set("dark");
    notify();
    expect(theme.get()).toBe("dark");
    theme.dispose();
  });
});

describe("createTheme: onSystemChange", () => {
  it("reports an OS change while pinned and while following it", () => {
    const seen: string[] = [];
    const storage = memoryStorage();
    const theme = createTheme({ storageKey: "k", storage, onSystemChange: (s) => seen.push(s) });
    fireSystemChange(true);
    theme.set("light");
    fireSystemChange(false);
    expect(seen).toEqual(["dark", "light"]);
    expect(document.documentElement.getAttribute("data-theme")).toBe("light");
    theme.dispose();
  });
});
