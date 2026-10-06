// The per-device localStorage keys and the sign-out sweep over them.

import { describe, it, expect, afterEach } from "vitest";
import * as lsKeys from "./ls-keys.js";
import {
  clearDeviceKeys,
  LS_DISMISSED_BANNERS_KEY,
  LS_TURN_FOLDS_KEY,
  LS_UI_STATE_KEY,
} from "./ls-keys.js";

/** Enumerated from the exports, so a new `LS_*` key left out of the sweep turns this red. */
function declaredKeys(): string[] {
  const out: string[] = [];
  for (const [name, value] of Object.entries(lsKeys)) {
    if (name.startsWith("LS_") && typeof value === "string") {
      out.push(value);
    }
  }
  return out;
}

afterEach(() => {
  localStorage.clear();
});

describe("clearDeviceKeys", () => {
  it("drops every key this module declares", () => {
    const keys = declaredKeys();
    // The three the sweep exists for, so a broken enumeration cannot assert over an empty list.
    expect(keys).toEqual(
      expect.arrayContaining([LS_UI_STATE_KEY, LS_TURN_FOLDS_KEY, LS_DISMISSED_BANNERS_KEY]),
    );
    for (const key of keys) {
      localStorage.setItem(key, '{"held":true}');
    }

    clearDeviceKeys();

    for (const key of keys) {
      expect(localStorage.getItem(key), key).toBeNull();
    }
  });

  it("leaves a key it does not own alone", () => {
    // This origin is shared with whatever else is stored here.
    localStorage.setItem("marotte.something-else", "keep me");
    localStorage.setItem(LS_UI_STATE_KEY, "{}");

    clearDeviceKeys();

    expect(localStorage.getItem("marotte.something-else")).toBe("keep me");
  });

  it("does not throw where storage is denied", () => {
    // A sign-out must complete where localStorage is refused (Safari private window, quota exhausted).
    const denied = {
      removeItem: () => {
        throw new DOMException("denied", "SecurityError");
      },
    };
    const original = Reflect.getOwnPropertyDescriptor(globalThis, "localStorage");
    Object.defineProperty(globalThis, "localStorage", { configurable: true, value: denied });
    try {
      expect(() => {
        clearDeviceKeys();
      }).not.toThrow();
    } finally {
      if (original !== undefined) {
        Object.defineProperty(globalThis, "localStorage", original);
      }
    }
  });
});
