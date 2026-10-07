import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";

import { LS_UI_STATE_KEY } from "./ls-keys.js";
import { PREPAINT_STEPS, applyStoredTheme, runPrepaint } from "./prepaint-steps.js";

const root = document.documentElement;
const ROOT_ATTRS = ["data-theme", "data-pointer", "data-shell-open"];
const ROOT_PROPS = ["--sidebar-w-pref", "--shell-h"];

function reset(): void {
  localStorage.clear();
  for (const a of ROOT_ATTRS) {
    root.removeAttribute(a);
  }
  for (const p of ROOT_PROPS) {
    root.style.removeProperty(p);
  }
}

const SEEDED = {
  theme: "dark",
  sidebar_w: 480,
  pointer_mode: "coarse",
  shell_open: true,
  shell_h: 300,
};

function seed(blob: unknown): void {
  localStorage.setItem(LS_UI_STATE_KEY, typeof blob === "string" ? blob : JSON.stringify(blob));
}

function painted(): Record<string, string | boolean> {
  return {
    theme: root.getAttribute("data-theme") ?? "",
    sidebar: root.style.getPropertyValue("--sidebar-w-pref"),
    pointer: root.getAttribute("data-pointer") ?? "",
    shellOpen: root.hasAttribute("data-shell-open"),
    shellH: root.style.getPropertyValue("--shell-h"),
  };
}

const ALL_FOUR = {
  theme: "dark",
  sidebar: "480px",
  pointer: "coarse",
  shellOpen: true,
  shellH: "300px",
};

function stubScheme(dark: boolean): void {
  vi.stubGlobal("matchMedia", (q: string) => ({
    matches: q === "(prefers-color-scheme: dark)" ? dark : false,
    media: q,
    addEventListener: (): void => undefined,
    removeEventListener: (): void => undefined,
  }));
}

beforeEach(reset);
afterEach(() => {
  vi.unstubAllGlobals();
  reset();
});

describe("runPrepaint", () => {
  it("applies all four stored settings", () => {
    seed(SEEDED);
    runPrepaint();
    expect(painted()).toEqual(ALL_FOUR);
  });

  it.each([
    ["an empty blob", "{}"],
    ["no blob", null],
    ["a corrupt blob", "{not json"],
  ])("falls back to the defaults for %s, quietly", (_name, raw) => {
    if (raw !== null) {
      seed(raw);
    }
    const errors = vi.spyOn(console, "error");
    const warns = vi.spyOn(console, "warn");
    expect(() => {
      runPrepaint();
    }).not.toThrow();
    expect(errors).not.toHaveBeenCalled();
    expect(warns).not.toHaveBeenCalled();
    expect(root.getAttribute("data-theme")).toMatch(/^(dark|light)$/);
    expect(root.style.getPropertyValue("--sidebar-w-pref")).toBe("");
    expect(root.hasAttribute("data-shell-open")).toBe(false);
  });

  it("still applies every step when an earlier one throws", () => {
    seed(SEEDED);
    runPrepaint([
      () => {
        throw new Error("boom");
      },
      ...PREPAINT_STEPS,
    ]);
    expect(painted()).toEqual(ALL_FOUR);
  });

  it("ignores an invalid stored width and keeps a zero shell height on the CSS default", () => {
    seed({ ...SEEDED, sidebar_w: -5, shell_h: 0 });
    runPrepaint();
    expect(root.style.getPropertyValue("--sidebar-w-pref")).toBe("");
    expect(root.hasAttribute("data-shell-open")).toBe(true);
    expect(root.style.getPropertyValue("--shell-h")).toBe("");

    reset();
    seed({ ...SEEDED, sidebar_w: "wide" });
    runPrepaint();
    expect(root.style.getPropertyValue("--sidebar-w-pref")).toBe("");
  });

  it("runs the theme step first, so the stylesheet resolves the right tokens", () => {
    expect(PREPAINT_STEPS[0]).toBe(applyStoredTheme);
    expect(PREPAINT_STEPS).toHaveLength(4);
  });
});

describe("applyStoredTheme resolves the cached choice like the live controller", () => {
  it.each([
    ["light, OS dark", { theme: "light" }, true, "light"],
    ["dark, OS light", { theme: "dark" }, false, "dark"],
    ["system, OS dark", { theme: "system" }, true, "dark"],
    ["system, OS light", { theme: "system" }, false, "light"],
    ["an unknown value, OS dark", { theme: "chartreuse" }, true, "dark"],
    ["no theme field, OS dark", {}, true, "dark"],
    ["a corrupt blob, OS dark", "{not json", true, "dark"],
    ["a corrupt blob, OS light", "{not json", false, "light"],
  ] as const)("%s", (_name, blob, osDark, want) => {
    stubScheme(osDark);
    seed(blob);
    applyStoredTheme();
    expect(root.getAttribute("data-theme")).toBe(want);
  });

  it("follows the OS when storage itself throws", () => {
    stubScheme(true);
    vi.stubGlobal("localStorage", {
      getItem: (): never => {
        throw new Error("denied");
      },
      setItem: (): never => {
        throw new Error("denied");
      },
      removeItem: (): void => undefined,
      clear: (): void => undefined,
    });
    applyStoredTheme();
    expect(root.getAttribute("data-theme")).toBe("dark");
  });

  it("still sets a theme when the controller itself fails", () => {
    stubScheme(true);
    const setAttribute = vi.spyOn(root, "setAttribute").mockImplementationOnce(() => {
      throw new Error("controller failed");
    });
    applyStoredTheme();
    setAttribute.mockRestore();
    expect(root.getAttribute("data-theme")).toBe("dark");
  });
});
