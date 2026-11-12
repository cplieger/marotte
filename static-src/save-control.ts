import { patchSettings, settingsAsShown } from "./persist.js";
import type { EffectiveSettings } from "./persist.js";

/** Send patch from control, a control persist holds no switch for. Once the write settles the
 *  control shows the settings as shown, through paint, unless the reader changed it since: a
 *  refused write leaves no unsaved value on screen. */
export function saveControl(
  control: HTMLInputElement | HTMLSelectElement,
  patch: Partial<EffectiveSettings>,
  paint: (shown: EffectiveSettings) => void,
): void {
  const sent = control.value;
  void patchSettings(patch).then(() => {
    const shown = settingsAsShown();
    if (shown !== null && control.value === sent) {
      paint(shown);
    }
  });
}
