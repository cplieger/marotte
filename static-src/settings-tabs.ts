// Settings tab bar: paints the panel, the pill and the subtitle the Settings row's route names.

import { computed, subscribe } from "@cplieger/reactive";
import { $, queryAll } from "./dom.js";
import { swapViews } from "./view-swap.js";
import type { SettingsTab } from "./route-path.js";
import { getActiveTabRoute, setSettingsTab, subTabOf } from "./tabs.js";
import { initSegmentedBar } from "./segmented-bar.js";
import { setPageSubtitle } from "./page-title.js";

export const TABS: readonly SettingsTab[] = [
  "general",
  "tools",
  "permissions",
  "instructions",
] as const;

export const TAB_LABELS: Readonly<Record<SettingsTab, string>> = {
  general: "General",
  tools: "Tools",
  permissions: "Permissions",
  instructions: "Custom instructions",
};

/** Null while no Settings row is open; a computed, so a write that names the shown tab fires
 *  nothing. */
const shownTab = computed(() => subTabOf("settings"));

// Two doors reach a loader: the tab's own ACTIVATION, through `refreshSettingsPanel`, and a sub-tab
// SWITCH, through the subscriber below. Both are gated there rather than latched here, because a
// panel loads once per activation and once per switch rather than once per page.
const tabLoaders = new Map<SettingsTab, () => void>();

/** Whether a first paint has run. See the `painted` gate below. */
let painted = false;

/** Run the tab's registered loader. `?.` is defensive rather than a tolerance for a missing
 *  loader: `initSettingsTabs` populates the map at boot. */
export function loadSettingsTabData(tab: SettingsTab): void {
  tabLoaders.get(tab)?.();
}

/** Reload the panel the reader is looking at: a settings tab's `refresh`. */
export function refreshSettingsPanel(): void {
  const tab = shownTab.peek();
  if (tab !== null) {
    loadSettingsTabData(tab);
  }
}

/** Build the tab bar and wire panel visibility. Called once from settings.ts initUI, which
 *  registers the per-tab lazy data loaders here. */
export function initSettingsTabs(loaders?: Partial<Record<SettingsTab, () => void>>): void {
  if (loaders !== undefined) {
    for (const [tab, fn] of Object.entries(loaders)) {
      tabLoaders.set(tab as SettingsTab, fn);
    }
  }
  const bar = $.settingsTabBar;
  bar.setAttribute("aria-label", "Settings sections");

  const paint = initSegmentedBar(bar, {
    attr: "data-settings-tab",
    idPrefix: "settings",
    tabs: TABS.map((id) => ({ id, label: TAB_LABELS[id] })),
    onSelect: setSettingsTab,
  });

  subscribe(shownTab, (tab) => {
    // No row: nothing is on screen, and a repaint of the hidden view would run a swap the
    // successor's entry fade is using.
    if (tab === null) {
      return;
    }
    paint(tab);
    const swap = (): HTMLElement | null => {
      let active: HTMLElement | null = null;
      for (const panel of queryAll<HTMLDivElement>("[data-settings-panel]")) {
        // eslint-disable-next-line @typescript-eslint/no-non-null-assertion
        const panelTab = panel.dataset["settingsPanel"]!;
        const isActive = panelTab === tab;
        panel.classList.toggle("hidden", !isActive);
        panel.setAttribute("role", "tabpanel");
        panel.id = `settings-panel-${panelTab}`;
        panel.setAttribute("aria-labelledby", `settings-tab-${panelTab}`);
        if (isActive) {
          active = panel;
        }
      }
      return active;
    };
    swapViews(swap);
    // The title bar's subtitle names the active section. The bar's own segmented control names it
    // too, so 12-chat.css suppresses this while that control shows its labels and reveals it when
    // tab-bar-fit.ts drops them.
    setPageSubtitle("settings", TAB_LABELS[tab]);
    // TWO gates, and neither subsumes the other. `painted` refuses the FIRST paint whatever is on
    // screen: a row's birth or the subscribe-time fire, which the activation's `refresh` loads. The
    // route gate refuses a door aimed at a background row, which loads when it is activated.
    if (painted && getActiveTabRoute()?.kind === "settings") {
      loadSettingsTabData(tab);
    }
    painted = true;
  });
}
