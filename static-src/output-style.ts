// Settings → Output style: its options are the styles kiro-cli's template serves, which reach
// this module through session-catalog.ts.

import { el } from "@cplieger/reactive";
import { settingsAsShown } from "./persist.js";
import type { OutputStyle } from "./wire/types.gen.js";

const DEFAULT_STYLE = "default";
const DEFAULT_HINT = "Default uses Kiro's standard response style.";

let served: readonly OutputStyle[] = [];

function outputStyleSelect(): HTMLSelectElement | null {
  return document.getElementById("output-style") as HTMLSelectElement | null;
}

/** Rebuilds the select from the served styles and re-selects the stored one. Default stays an
 *  option whatever is served, since it is the one KAS applies to an id it does not serve. */
export function setCatalogOutputStyles(styles: readonly OutputStyle[]): void {
  served = styles;
  const select = outputStyleSelect();
  if (select === null) {
    return;
  }
  const stored = settingsAsShown()?.output_style ?? select.value;
  const options = styles.map((s) => el("option", { value: s.id }, s.name));
  if (!styles.some((s) => s.id === DEFAULT_STYLE)) {
    options.unshift(el("option", { value: DEFAULT_STYLE }, "Default"));
  }
  select.replaceChildren(...options);
  paintOutputStyle(stored);
}

/** Selects `stored`, or Default when no served style has that id (KAS reads it as default too);
 *  never writes the setting. */
export function paintOutputStyle(stored: string): void {
  const select = outputStyleSelect();
  if (select === null) {
    return;
  }
  const listed = Array.from(select.options).some((o) => o.value === stored);
  select.value = listed ? stored : DEFAULT_STYLE;
  paintOutputStyleHint(select.value);
}

/** The hint under the select: the shown style's own description, in kiro-cli's words. */
export function paintOutputStyleHint(id: string): void {
  const hint = document.getElementById("output-style-hint");
  if (hint === null) {
    return;
  }
  const description = served.find((s) => s.id === id)?.description ?? "";
  if (description !== "") {
    hint.textContent = `${description} From your next message on.`;
  } else if (id === DEFAULT_STYLE) {
    hint.textContent = DEFAULT_HINT;
  } else {
    hint.textContent = "Applies from your next message on.";
  }
}
