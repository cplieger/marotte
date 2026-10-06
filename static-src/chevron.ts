// THE disclosure chevron: one SVG glyph, one class, one rotation for every expand affordance.
// Closed points along the inline axis, open points down (the `<details>` convention); the CSS owns direction.
// Position carries the interaction type: a disclosing chevron leads its header and rotates, a navigating one
// trails and never rotates. A link that leaves the app keeps `ICON_EXTERNAL`.

import { el } from "@cplieger/reactive";
import { iconEl } from "./icon-el.js";
import { ICON_CHEVRON_DOWN } from "./icons.js";

/**
 * A disclosure chevron, closed. `.disclosure-chevron` owns rotation, flipped by each site's own open state.
 * Always `aria-hidden`: the control around it already carries the accessible name.
 */
export function chevronEl(): HTMLElement {
  return el(
    "span",
    { className: "disclosure-chevron", "aria-hidden": "true" },
    iconEl(ICON_CHEVRON_DOWN),
  );
}
