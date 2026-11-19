// Whether Settings lets chats message each other: the server's chat_messaging_enabled setting, which is what puts each
// chat's own switch in Chat options.

import { signal, type ReadonlySignal } from "@cplieger/reactive";
import type { EffectiveSettings } from "./wire/types.gen.js";

const on = signal(false);

/** False until a settings answer is adopted, so a chat's switch never shows ahead of the setting. */
export const chatMessagingOn: ReadonlySignal<boolean> = on;

/** Takes the setting from a settings answer, at boot and on every `settings_updated`. */
export function adoptChatMessaging(s: Pick<EffectiveSettings, "chat_messaging_enabled">): void {
  on.value = s.chat_messaging_enabled;
}
