// Chat retention state. Reads the marotte-owned chat_retention_days setting (/api/settings) and
// exposes it to the tab-close handler (keep vs delete) and the history button (hidden when
// retention is off).

import { signal, subscribe } from "@cplieger/reactive";
import { apiGetTyped } from "./api-client.js";
import { defineAction, retryNetwork } from "./actions/index.js";
import { decodeEffectiveSettings } from "./wire/decoders.gen.js";

// It mirrored settings.DefaultChatRetentionDays with nothing holding the two together, applying to
// a config.json that exists WITHOUT the key — which is every install that never touched the
// setting.
const retentionDays = signal(7);

// eslint-disable-next-line @typescript-eslint/no-invalid-void-type
const refreshRetentionAction = defineAction<void, number>({
  name: "settings.refresh_retention",
  dedupe: true,
  retryable: retryNetwork,
  retry: { count: 2, delay: 300 },
  run: async (_args, signal) => {
    // Decoded through the generated decoder, so the required field this reads is checked at the
    // boundary rather than asserted by a cast.
    const s = await apiGetTyped("/api/settings", decodeEffectiveSettings, signal);
    if (s === null) {
      // Network, non-2xx or a payload the decoder rejected: throw so dispatch resolves null and
      // refreshRetention leaves the current value in place rather than clobbering it.
      throw new Error("retention: /api/settings unavailable");
    }
    // No coalesce: the field is required on the payload, and the server has already resolved its
    // default and type-checked the stored value.
    return s.chat_retention_days;
  },
  error: false,
});

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

export async function refreshRetention(): Promise<void> {
  const result = await refreshRetentionAction.dispatch(undefined);
  if (result === null) {
    return;
  }
  retentionDays.value = result;
}
