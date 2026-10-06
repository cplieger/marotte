// Styled tooltips: ui-primitives' `initTooltips` configured for marotte's `data-tooltip` attribute.
// It brings the placement engine (flip, clamp, visualViewport-aware), aria-describedby token-list
// preservation, `\n`→<br>, and re-parenting into an open <dialog>'s top layer. Skin: .uip-tooltip
// (css/04-uip-skin.css).

import { initTooltips as uipInitTooltips } from "@cplieger/ui-primitives/tooltip";

/** Hover time every tooltip waits out, matching a native `title` (Firefox 500ms, Windows 400ms).
 *  ONE value, cold or warm: a warm group made every neighbouring control pop instantly. */
const HOVER_DELAY_MS = 500;

/** Install the delegated tooltip controller once (idempotent). Focus shows a tooltip only under
 *  `:focus-visible`, so programmatic focus does not pop one. */
export function initTooltips(): void {
  uipInitTooltips({
    attribute: "data-tooltip",
    // Both equal ui-primitives' defaults since 3.0.1 (`delayWarm` follows `delayCold`).
    delayCold: HOVER_DELAY_MS,
    delayWarm: HOVER_DELAY_MS,
  });
}
