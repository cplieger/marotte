// Chat retention state. Holds the marotte-owned chat_retention_days setting from the settings
// payloads the page adopts, and exposes it to the tab-close handler (keep vs delete) and the
// history button (hidden when retention is off).

import { signal, subscribe } from "@cplieger/reactive";
import type { EffectiveSettings } from "./persist.js";

// It mirrored settings.DefaultChatRetentionDays with nothing holding the two together, applying to
// a config.json that exists WITHOUT the key — which is every install that never touched the
// setting.
const retentionDays = signal(7);

/** Whether closing a tab KEEPS the chat (and History is shown). True for a positive day count
 *  AND forever (-1); false only when retention is off (0). */
export function isRetentionEnabled(): boolean {
  return retentionDays.value !== 0;
}

/** Subscribe to retention changes. Returns an unsubscribe function. Fires immediately with the
 *  current value on subscribe. */
export function onRetentionChange(fn: () => void): () => void {
  return subscribe(retentionDays, fn);
}

/** Take the retention a settings payload states; pass only a payload persist adopted. */
export function adoptRetention(s: EffectiveSettings): void {
  retentionDays.value = s.chat_retention_days;
}
