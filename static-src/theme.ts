// Theme toggle (light / dark / system) over ui-primitives' createTheme, which applies the choice to
// <html data-theme>. The storage adapter is a PARAMETER: the preference lives in config.json with a
// paint cache, both owned by settings.ts, which imports this module. THREE states, or "follow the
// OS" is unreachable after one click. The button shows the CHOICE, not the resolved theme.

import { $, forceReflow } from "./dom.js";
import { LS_UI_STATE_KEY, THEME_ATTRIBUTE } from "./ls-keys.js";
import { createTheme } from "@cplieger/ui-primitives/theme";
import type { ThemeController, ThemeStorage } from "@cplieger/ui-primitives/theme";
// The vocabulary is declared where the paint cache stores it, so the two
// cannot drift; a type import couples nothing at runtime.
import type { ThemeChoice } from "./device-view.js";

let controller: ThemeController | null = null;

// The CHOICE the toggle icon shows; `shown` tracks the icon on screen, which a 3-icon cycle cannot
// derive from the incoming one.
let current: ThemeChoice = "dark";
let shown: ThemeChoice | null = null;

/** Next state in the cycle, matching the library's own order. */
const NEXT: Record<ThemeChoice, ThemeChoice> = {
  light: "dark",
  dark: "system",
  system: "light",
};

const LABEL: Record<ThemeChoice, string> = {
  light: "light theme",
  dark: "dark theme",
  system: "system theme",
};

/** A Record rather than a ternary so a fourth choice fails the type check
 *  instead of silently borrowing another state's wash. */
const GLOW: Record<ThemeChoice, string> = {
  light: "glow-sun",
  dark: "glow-moon",
  system: "glow-system",
};

/** Every class GLOW can add, so the cleanup cannot fall behind the table. */
const GLOW_CLASSES = Object.values(GLOW);

function updateIcon(): void {
  const btn = $.themeBtn;
  const icons: Record<ThemeChoice, Element | null> = {
    dark: btn.querySelector(".theme-icon-dark"),
    light: btn.querySelector(".theme-icon-light"),
    system: btn.querySelector(".theme-icon-system"),
  };
  const incoming = icons[current];
  if (incoming === null) {
    return;
  }
  const outgoing = shown === null || shown === current ? null : icons[shown];
  shown = current;

  // Subflux-inspired vertical slide: the outgoing icon slides down
  // ("setting"), then we swap visibility and the incoming icon slides
  // up from below ("rising").
  const glowClass = GLOW[current];

  let settled = false;
  const settle = (): void => {
    if (settled) {
      return;
    }
    settled = true;
    // Swap: hide every non-current icon, show incoming at the "rising" position.
    for (const [choice, icon] of Object.entries(icons)) {
      if (icon !== null && choice !== current) {
        icon.classList.add("hidden");
        icon.classList.remove("icon-setting");
      }
    }
    incoming.classList.remove("hidden");
    incoming.classList.add("icon-rising");
    btn.classList.add(glowClass);
    // Force reflow so the browser sees the "rising" start state.
    forceReflow(incoming);
    // Remove "rising" so the transition runs from below → center.
    incoming.classList.remove("icon-rising");
    incoming.addEventListener(
      "transitionend",
      () => {
        btn.classList.remove(...GLOW_CLASSES);
      },
      { once: true },
    );
    // Safety: clear glow if transitionend doesn't fire (e.g. reduced motion).
    setTimeout(() => {
      btn.classList.remove(...GLOW_CLASSES);
    }, 350);
  };

  // Start: slide the outgoing icon down. On the first paint there is nothing on
  // screen to slide out, so settle immediately rather than animating from blank.
  if (outgoing === null) {
    settle();
  } else {
    outgoing.classList.add("icon-setting");
    outgoing.addEventListener("transitionend", settle, { once: true });
    // Safety timeout in case transitionend doesn't fire.
    setTimeout(settle, 350);
  }

  // The label names what a click DOES, and for "system" also what it resolved
  // to, because that is the one state whose appearance does not name itself.
  const suffix =
    current === "system" && controller !== null ? ` (now ${controller.resolved()})` : "";
  const label = `Theme: ${LABEL[current]}${suffix}. Switch to ${LABEL[NEXT[current]]}`;
  btn.setAttribute("data-tooltip", label);
  btn.setAttribute("aria-label", label);
}

/** Create the theme controller (applies the persisted / OS theme now) and wire the toggle. Call once
 *  during UI init. `storageKey` is still passed for the library's cross-tab `storage` filter. */
export function initThemeToggle(storage: ThemeStorage): void {
  controller = createTheme({
    storageKey: LS_UI_STATE_KEY,
    storage,
    attribute: THEME_ATTRIBUTE,
    // The resolved theme drives the <html> attribute (the library's job); the
    // icon follows the CHOICE, read back from the controller.
    onChange: () => {
      current = controller?.get() ?? "system";
      updateIcon();
    },
  });
  // onChange fires during createTheme, before `controller` is assigned, so the
  // first icon paint happens here with the controller in hand.
  current = controller.get();
  updateIcon();

  $.themeBtn.addEventListener("click", () => {
    controller?.cycle();
  });
}

/** Apply a choice the SERVER reported: repaint and move the toggle. The controller's one write verb
 *  means "the user chose this", so settings.ts's adopt guard stops the echo. */
export function applyThemeChoice(choice: ThemeChoice): void {
  controller?.set(choice);
}
