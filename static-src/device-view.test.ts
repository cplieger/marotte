// device-view.ts owns the device fields of one localStorage JSON blob; the one-owner rule matters most, since two
// read-modify-write writers on one key drop each other's fields.

import { describe, it, expect, beforeEach } from "vitest";

import {
  activeView,
  cachePointerTier,
  cacheTheme,
  cachedPointerTier,
  cachedTheme,
  coarseEverSeen,
  loadDeviceView,
  markCoarseSeen,
  pointerModeChoice,
  setActiveView,
  setPointerModeChoice,
  setShellHeight,
  setShellOpen,
  setSidebarWidth,
  shellHeight,
  shellOpen,
  sidebarWidth,
} from "./device-view.js";
import { LS_UI_STATE_KEY } from "./ls-keys.js";

function blob(): Record<string, unknown> {
  const raw = localStorage.getItem(LS_UI_STATE_KEY);
  return raw === null ? {} : (JSON.parse(raw) as Record<string, unknown>);
}

beforeEach(() => {
  localStorage.clear();
});

describe("the four fields this screen keeps to itself", () => {
  it("reads back what it wrote, per field", () => {
    setActiveView("chat-x");
    setShellOpen(true);
    setShellHeight(320);
    setSidebarWidth(400);

    expect(activeView()).toBe("chat-x");
    expect(shellOpen()).toBe(true);
    expect(shellHeight()).toBe(320);
    expect(sidebarWidth()).toBe(400);
  });

  it("answers the defaults when nothing has been written", () => {
    expect(loadDeviceView()).toEqual({
      active_view: "",
      shell_open: false,
      shell_h: 0,
      sidebar_w: 0,
    });
  });

  it("keeps a valid field when a sibling is the wrong type", () => {
    // `shell_h` feeds sizing arithmetic; one bad field must not reset the good ones.
    localStorage.setItem(
      LS_UI_STATE_KEY,
      JSON.stringify({ active_view: "chat-y", shell_open: "yes", shell_h: "tall" }),
    );

    expect(loadDeviceView()).toEqual({
      active_view: "chat-y",
      shell_open: false,
      shell_h: 0,
      sidebar_w: 0,
    });
  });

  it("refuses a negative or non-finite height", () => {
    for (const bad of [-1, Number.NaN, Number.POSITIVE_INFINITY]) {
      localStorage.setItem(LS_UI_STATE_KEY, JSON.stringify({ shell_h: bad }));
      expect(shellHeight()).toBe(0);
    }
  });

  it("refuses a negative, non-finite or non-number width and keeps its siblings", () => {
    // JSON turns NaN and Infinity into null; the raw literals are planted via a hand-written blob.
    for (const raw of [
      '{"sidebar_w":-5,"shell_h":300}',
      '{"sidebar_w":"400","shell_h":300}',
      '{"sidebar_w":null,"shell_h":300}',
      JSON.stringify({ sidebar_w: Number.NaN, shell_h: 300 }),
      JSON.stringify({ sidebar_w: Number.POSITIVE_INFINITY, shell_h: 300 }),
    ]) {
      localStorage.setItem(LS_UI_STATE_KEY, raw);
      expect(sidebarWidth(), raw).toBe(0);
      expect(shellHeight(), raw).toBe(300);
    }
  });

  it("survives a blob that is not an object, and one that is not JSON at all", () => {
    const defaults = { active_view: "", shell_open: false, shell_h: 0, sidebar_w: 0 };
    localStorage.setItem(LS_UI_STATE_KEY, '"a string"');
    expect(loadDeviceView()).toEqual(defaults);

    localStorage.setItem(LS_UI_STATE_KEY, "{not json");
    expect(loadDeviceView()).toEqual(defaults);
  });
});

describe("the theme's pre-paint cache", () => {
  it("round-trips the three choices, and 'system' is one of them", () => {
    // "system" is a real choice; coercing it away made Auto unreachable.
    for (const choice of ["dark", "light", "system"] as const) {
      cacheTheme(choice);
      expect(cachedTheme()).toBe(choice);
    }
  });

  it("reads an unrecognised or absent value as no choice", () => {
    expect(cachedTheme()).toBeNull();
    localStorage.setItem(LS_UI_STATE_KEY, JSON.stringify({ theme: "chartreuse" }));
    expect(cachedTheme()).toBeNull();
  });

  it("clearing removes the field, so prepaint.js falls back to the OS preference", () => {
    cacheTheme("light");
    cacheTheme(null);
    expect("theme" in blob()).toBe(false);
    expect(cachedTheme()).toBeNull();
  });
});

describe("the pointer fields", () => {
  it("round-trips the stated choice", () => {
    for (const tier of ["fine", "coarse"] as const) {
      setPointerModeChoice(tier);
      expect(pointerModeChoice()).toBe(tier);
    }
  });

  it("reads an unrecognised or absent choice as none", () => {
    expect(pointerModeChoice()).toBeNull();
    localStorage.setItem(LS_UI_STATE_KEY, JSON.stringify({ pointer_mode: "chunky" }));
    expect(pointerModeChoice()).toBeNull();
  });

  it("latches the coarse-seen flag and never reads a non-boolean as set", () => {
    expect(coarseEverSeen()).toBe(false);
    markCoarseSeen();
    expect(coarseEverSeen()).toBe(true);

    // The flag reveals a control, so a wrong-typed truthy value must not turn it on.
    for (const bad of ["true", 1, {}]) {
      localStorage.setItem(LS_UI_STATE_KEY, JSON.stringify({ pointer_coarse_seen: bad }));
      expect(coarseEverSeen()).toBe(false);
    }
  });

  it("keeps the choice and the observation apart", () => {
    // Two fields because the detector writes `pointer` on every tier change and would erase a shared choice.
    setPointerModeChoice("coarse");
    cachePointerTier("fine");

    expect(pointerModeChoice()).toBe("coarse");
    expect(cachedPointerTier()).toBe("fine");
  });
});

describe("one owner of the key", () => {
  it("a device-field write preserves the theme cache", () => {
    cacheTheme("light");
    setShellHeight(420);
    setActiveView("chat-z");
    setShellOpen(true);

    expect(cachedTheme()).toBe("light");
  });

  it("a pointer write preserves the theme cache and the three device fields", () => {
    cacheTheme("light");
    setActiveView("chat-z");
    setShellOpen(true);
    setShellHeight(420);
    setSidebarWidth(380);

    setPointerModeChoice("coarse");
    markCoarseSeen();
    cachePointerTier("coarse");

    expect(cachedTheme()).toBe("light");
    expect(loadDeviceView()).toEqual({
      active_view: "chat-z",
      shell_open: true,
      shell_h: 420,
      sidebar_w: 380,
    });
  });

  it("a theme or device write preserves the pointer fields", () => {
    setPointerModeChoice("coarse");
    markCoarseSeen();
    cachePointerTier("fine");

    cacheTheme("dark");
    setShellHeight(200);

    expect(pointerModeChoice()).toBe("coarse");
    expect(coarseEverSeen()).toBe(true);
    expect(cachedPointerTier()).toBe("fine");
  });

  it("a theme write preserves the three device fields", () => {
    setActiveView("chat-z");
    setShellOpen(true);
    setShellHeight(420);
    setSidebarWidth(380);
    cacheTheme("dark");

    expect(loadDeviceView()).toEqual({
      active_view: "chat-z",
      shell_open: true,
      shell_h: 420,
      sidebar_w: 380,
    });
  });

  it("a sidebar-width write preserves the theme, the pointer fields and the other three", () => {
    cacheTheme("light");
    setPointerModeChoice("coarse");
    markCoarseSeen();
    cachePointerTier("fine");
    setActiveView("chat-z");
    setShellOpen(true);
    setShellHeight(420);

    setSidebarWidth(380);

    expect(cachedTheme()).toBe("light");
    expect(pointerModeChoice()).toBe("coarse");
    expect(coarseEverSeen()).toBe(true);
    expect(cachedPointerTier()).toBe("fine");
    expect(loadDeviceView()).toEqual({
      active_view: "chat-z",
      shell_open: true,
      shell_h: 420,
      sidebar_w: 380,
    });
  });

  it("leaves a field it does not own alone", () => {
    // A field added by another module must survive the next write.
    localStorage.setItem(LS_UI_STATE_KEY, JSON.stringify({ someone_elses: "value" }));
    setShellHeight(200);
    cacheTheme("dark");

    expect(blob()["someone_elses"]).toBe("value");
    expect(blob()["shell_h"]).toBe(200);
    expect(blob()["theme"]).toBe("dark");
  });
});
