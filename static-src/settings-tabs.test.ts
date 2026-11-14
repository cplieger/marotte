import type * as Dom from "./dom.js";
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { TABS, TAB_LABELS } from "./settings-tabs.js";
import type * as ModSettingsTabs from "./settings-tabs.js";
import type { SettingsTab } from "./route-path.js";

/** Cache-buster for the re-imports below. */
let bootSeq = 0;

// swapViews spy: runs its callback synchronously (so the panel swap still happens) while recording
// every invocation. One invocation == one swap.
const { swapViewsSpy, route } = vi.hoisted(() => ({
  swapViewsSpy: vi.fn((fn: () => HTMLElement | null) => {
    fn();
  }),
  /** The subscriber's second gate reads it, and this suite runs the REAL tabs.ts otherwise — where
   *  no row exists, so it would answer `null` and refuse every load. */
  route: { current: { kind: "settings" } as { readonly kind: string } | null },
}));

vi.mock("./view-swap.js", () => ({
  swapViews: swapViewsSpy,
}));
/** The Settings row's route as the strip holds it, null while no row is open. A fresh signal per
 *  case: every re-imported module instance stays subscribed to the cell it first read. */
const settingsRoute = await vi.hoisted(async () => {
  const { signal } = await import("@cplieger/reactive");
  return { cell: signal<SettingsTab | null>(null) };
});
vi.mock("./tabs.js", async () => ({
  ...(await import("./__test-helpers__/tabs-mock.js")).tabsMock(),
  getActiveTabRoute: () => route.current,
  subTabOf: (kind: string) => (kind === "settings" ? settingsRoute.cell.value : null),
  // The real setter's no-op while no row is open.
  setSettingsTab: (tab: SettingsTab) => {
    if (settingsRoute.cell.peek() !== null) {
      settingsRoute.cell.value = tab;
    }
  },
}));

// Mock ./dom.js so `$` resolves against the test-built DOM. The registry resolves the tab bar /
// select lazily from the live DOM, mirroring the real lazy getters in dom.ts so initSettingsTabs()
// works against whatever was built in beforeEach.
vi.mock("./dom.js", async (importOriginal) => {
  const idFor: Record<string, string> = {
    settingsTabBar: "settings-tab-bar",
  };
  const byIdReal = (id: string): HTMLElement => {
    const e = document.getElementById(id);
    if (e === null) {
      throw new Error(`Missing element: #${id}`);
    }
    return e;
  };
  return {
    ...(await importOriginal<typeof Dom>()),
    $: new Proxy(
      {},
      {
        get(_target, prop): unknown {
          if (typeof prop === "string") {
            const id = idFor[prop];
            if (id !== undefined) {
              return byIdReal(id);
            }
          }
          return document.createElement("div");
        },
      },
    ),
    byId: byIdReal,
    maybeEl: (id: string): HTMLElement | null => document.getElementById(id),
  };
});

describe("settings-tabs TAB_LABELS coverage", () => {
  it("every TABS entry has a non-empty label", () => {
    for (const tab of TABS) {
      const label = TAB_LABELS[tab];
      expect(label, `TAB_LABELS["${tab}"] should be a non-empty string`).toBeTruthy();
      expect(typeof label).toBe("string");
      expect(label.length).toBeGreaterThan(0);
    }
  });

  it.each([
    { tab: "general", label: "General" },
    { tab: "tools", label: "Tools" },
    { tab: "permissions", label: "Permissions" },
    { tab: "instructions", label: "Custom instructions" },
  ] as const)("$tab → $label", ({ tab, label }) => {
    expect(TAB_LABELS[tab]).toBe(label);
  });

  it("TABS has exactly 4 entries", () => {
    expect(TABS).toHaveLength(4);
  });
});

describe("settings-tabs paints the Settings row's sub-tab", () => {
  // Build the panel layout contract initSettingsTabs() expects: a tab bar with one button per tab,
  // one panel per tab, and a page title.
  function buildSettingsDom(): void {
    document.body.replaceChildren();
    const bar = document.createElement("div");
    bar.id = "settings-tab-bar";
    for (const t of TABS) {
      const btn = document.createElement("button");
      btn.setAttribute("data-settings-tab", t);
      bar.appendChild(btn);
      const panel = document.createElement("div");
      panel.setAttribute("data-settings-panel", t);
      document.body.appendChild(panel);
    }
    const title = document.createElement("div");
    title.id = "settings-page-title";
    document.body.append(bar, title);
  }

  function shownPanels(): string[] {
    return [...document.querySelectorAll<HTMLElement>("[data-settings-panel]")]
      .filter((p) => !p.classList.contains("hidden"))
      .map((p) => p.dataset["settingsPanel"] ?? "");
  }

  async function boot(): Promise<typeof ModSettingsTabs> {
    return (await import(
      /* @vite-ignore */ `./settings-tabs.ts?boot=${bootSeq}`
    )) as typeof ModSettingsTabs;
  }

  beforeEach(async () => {
    route.current = { kind: "settings" };
    const { signal } = await import("@cplieger/reactive");
    settingsRoute.cell = signal<SettingsTab | null>("general");
    vi.resetModules();
    bootSeq++;
    buildSettingsDom();
    swapViewsSpy.mockClear();
  });

  afterEach(() => {
    document.body.replaceChildren();
  });

  it("a route write naming the shown tab does not re-run the panel swap", async () => {
    const { initSettingsTabs } = await boot();
    initSettingsTabs(); // subscribe fires immediately → one swap for the row's "general"
    swapViewsSpy.mockClear();

    settingsRoute.cell.value = "general";

    expect(swapViewsSpy).not.toHaveBeenCalled();
  });

  it("fires no panel loader on the first paint, and one on each switch", async () => {
    const { initSettingsTabs } = await boot();
    const general = vi.fn();
    const tools = vi.fn();

    // The subscribe-time paint is the open row's; its activation's `refresh` loads it.
    initSettingsTabs({ general, tools });
    expect(general).not.toHaveBeenCalled();
    expect(tools).not.toHaveBeenCalled();

    // A sub-tab SWITCH is a subject change, so it refetches every time. The sequence ALTERNATES:
    // the read is deduped, so two consecutive writes of one value notify once.
    settingsRoute.cell.value = "tools";
    expect(tools).toHaveBeenCalledTimes(1);
    settingsRoute.cell.value = "general";
    expect(general).toHaveBeenCalledTimes(1);
    settingsRoute.cell.value = "tools";
    expect(tools).toHaveBeenCalledTimes(2);
  });

  it("paints nothing with no row, and a row's birth paints its tab without a load", async () => {
    settingsRoute.cell.value = null;
    const { initSettingsTabs } = await boot();
    const tools = vi.fn();
    initSettingsTabs({ tools });
    expect(swapViewsSpy).not.toHaveBeenCalled();

    route.current = { kind: "chat" };
    settingsRoute.cell.value = "tools";

    expect(shownPanels()).toEqual(["tools"]);
    expect(tools).not.toHaveBeenCalled();
  });

  it("a close repaints nothing", async () => {
    const { initSettingsTabs } = await boot();
    const general = vi.fn();
    initSettingsTabs({ general });
    swapViewsSpy.mockClear();

    settingsRoute.cell.value = null;

    expect(swapViewsSpy).not.toHaveBeenCalled();
    expect(shownPanels()).toEqual(["general"]);
    expect(general).not.toHaveBeenCalled();
  });

  it("refuses a switch while the panel is OFF screen — that load is the activation's", async () => {
    const { initSettingsTabs } = await boot();
    const tools = vi.fn();
    initSettingsTabs({ tools });
    route.current = { kind: "chat" };

    // A door aimed at a background Settings row writes its route before the activation, so without
    // this term the panel loads twice — and for General that loader is a server-side
    // `kiro-cli settings` spawn.
    settingsRoute.cell.value = "tools";

    expect(tools).not.toHaveBeenCalled();
  });

  it("refreshSettingsPanel loads the panel the row names, not the canonical one", async () => {
    const { initSettingsTabs, refreshSettingsPanel } = await boot();
    const general = vi.fn();
    const tools = vi.fn();
    initSettingsTabs({ general, tools });
    settingsRoute.cell.value = "tools";
    tools.mockClear();

    refreshSettingsPanel();

    expect(tools).toHaveBeenCalledTimes(1);
    expect(general).not.toHaveBeenCalled();
  });

  it("refreshSettingsPanel loads nothing with no row", async () => {
    settingsRoute.cell.value = null;
    const { initSettingsTabs, refreshSettingsPanel } = await boot();
    const general = vi.fn();
    initSettingsTabs({ general });

    refreshSettingsPanel();

    expect(general).not.toHaveBeenCalled();
  });

  it("a pill click writes the row's route and loads its panel", async () => {
    const { initSettingsTabs } = await boot();
    const tools = vi.fn();
    initSettingsTabs({ tools });

    document.querySelector<HTMLElement>('[data-settings-tab="tools"]')?.click();

    expect(settingsRoute.cell.peek()).toBe("tools");
    expect(shownPanels()).toEqual(["tools"]);
    expect(tools).toHaveBeenCalledTimes(1);
  });

  it("loads the General panel through the door the tab factory uses", async () => {
    const { initSettingsTabs, loadSettingsTabData } = await boot();
    const general = vi.fn();
    initSettingsTabs({ general });

    // What a materialized settings tab's `onShow` calls: the gear, a path link and a restored tab
    // all arrive here, with the panel actually shown.
    loadSettingsTabData("general");
    expect(general).toHaveBeenCalledTimes(1);
  });

  it("two consecutive route writes of tools swap once", async () => {
    const { initSettingsTabs } = await boot();
    initSettingsTabs();
    swapViewsSpy.mockClear();

    settingsRoute.cell.value = "tools"; // general → tools: one swap
    settingsRoute.cell.value = "tools"; // tools → tools: deduped, no swap

    expect(swapViewsSpy).toHaveBeenCalledTimes(1);
  });
});
